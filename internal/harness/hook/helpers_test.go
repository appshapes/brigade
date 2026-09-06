package hook

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// Hang catchers, never performance bounds (plan 7.3).
const (
	pollTimeout  = 30 * time.Second
	pollInterval = 10 * time.Millisecond
)

// The scripted fake adapter, built once for the package (a real BAP/1
// executable; its dump file records every child's argv and environment).
var fakeAdapterBin string

// TestMain has two jobs. Run as `<test binary> watch …` it IS the watcher
// the hook detaches — a stand-in for P3-5's `brigade watch` that writes the
// pidfile the real watcher writes, records what it received in the --sink
// file, and exits on SIGTERM or when its CLAUDE_PID dies — so RealSpawner
// is exercised end to end (Setsid, the null stdin, the log file, the
// from-scratch environment) without the watcher binary existing yet.
// Otherwise it builds the fake adapter and runs the tests.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "watch" {
		os.Exit(stubWatcher(os.Args[2:])) //nolint:forbidigo // this is a process entrypoint
	}
	dir, err := os.MkdirTemp("", "hook-bins-")
	if err != nil {
		panic(err)
	}
	fakeAdapterBin = mustBuild(dir, "brigade-fake-adapter", "github.com/appshapes/brigade/cmd/brigade-fake-adapter")
	warmExecutable(fakeAdapterBin)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code) //nolint:forbidigo // TestMain owns the process exit
}

// mustBuild compiles a repository binary the way `make build` does.
func mustBuild(dir, name, pkg string) string {
	out := filepath.Join(dir, name)
	//nolint:gosec // G204: the package path is a constant; no shell is involved
	cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building %s: %v\n%s", pkg, err, b))
	}
	return out
}

// warmExecutable runs a freshly built binary once, with no deadline, and
// discards the result. On macOS the FIRST exec of a newly created
// executable is suspended while the system assesses it — measured
// 2026-09-03: 0.2–0.5 s on an idle host and 3–6 s while a whole-tree
// `go test` creates every package's fresh binaries at once — which is
// longer than the 3 s describe budget of 4.1 that the real-child tests
// here exercise (TestSessionStartRegistersSession timed out on describe
// in six whole-tree runs and never in isolation). The second exec costs
// about 10 ms.
func warmExecutable(bin string) {
	//nolint:gosec // G204: a binary this package just built; no shell is involved
	cmd := exec.CommandContext(context.Background(), bin, "describe")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	_ = cmd.Run()
}

// stubRecord is what the stub watcher writes to its --sink file: the
// facts a test asserts about the detached process. It never carries the
// token, only whether one was present.
type stubRecord struct {
	PID          int      `json:"pid"`
	Args         []string `json:"args"`
	EnvKeys      []string `json:"env_keys"`
	TokenPresent bool     `json:"token_present"`
	Socket       string   `json:"socket"`
	Cwd          string   `json:"cwd"`
	OwnSession   bool     `json:"own_session"`
	OwnGroup     bool     `json:"own_group"`
	StdinNull    bool     `json:"stdin_null"`
	StdoutIsLog  bool     `json:"stdout_is_log"`
}

// stubWatcher is the stand-in watcher (see TestMain). It reads its
// configuration exactly as P3-5 will (config.FromWatcherEnv plus the two
// CLAUDE_CODE_MESSAGING_* variables), writes the 6.6 pidfile and waits.
func stubWatcher(args []string) int {
	environ := os.Environ()
	w, err := config.FromWatcherEnv(environ)
	if err != nil {
		return protocol.CodeConfig.Exit()
	}
	sink := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--sink" {
			sink = args[i+1]
		}
	}
	socket := adapterkit.Getenv(environ, envMessagingSocket)
	token := adapterkit.Getenv(environ, envMessagingToken)
	m, err := sessionmap.Store{StateDir: w.StateDir}.ReadByPID(w.ClaudePID)
	if err != nil {
		return protocol.CodeConfig.Exit()
	}
	self := os.Getpid()
	info, err := procutil.Lookup(self)
	if err != nil {
		return 1
	}
	entry := pidfile.Entry{
		PID: self, StartToken: info.StartToken, BrigadeSessionID: m.BrigadeSessionID,
		SocketPath: socket, TokenSHA256: pidfile.TokenSHA256(token),
	}
	path := pidfile.Path(w.StateDir, w.ClaudePID)
	if cerr := pidfile.Create(path, entry); cerr != nil {
		if !errors.Is(cerr, fs.ErrExist) {
			return 1
		}
		v, verr := pidfile.Check(path, procutil.Lookup)
		if verr != nil {
			return 1
		}
		if v.Alive {
			return 0 // a duplicate: the live holder stays
		}
		if rerr := pidfile.Replace(path, v.Entry, entry); rerr != nil {
			return 1
		}
	}
	if sink != "" {
		writeStubRecord(sink, args, environ, token != "", socket, self)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case <-sig:
			break loop
		case <-tick.C:
			if info, lerr := procutil.Lookup(w.ClaudePID); lerr != nil || !info.Exists || info.Zombie {
				break loop
			}
		}
	}
	_, _ = pidfile.Remove(path, entry)
	return 0
}

func writeStubRecord(sink string, args, environ []string, tokenPresent bool, socket string, self int) {
	keys := make([]string, 0, len(environ))
	for _, e := range environ {
		name, _, _ := strings.Cut(e, "=")
		keys = append(keys, name)
	}
	sort.Strings(keys)
	cwd, _ := os.Getwd()
	sid, _ := unix.Getsid(0)
	rec := stubRecord{
		PID: self, Args: args, EnvKeys: keys, TokenPresent: tokenPresent, Socket: socket, Cwd: cwd,
		OwnSession: sid == self, OwnGroup: syscall.Getpgrp() == self,
		StdinNull: fdIsType(0, unix.S_IFCHR), StdoutIsLog: fdIsType(1, unix.S_IFREG),
	}
	data, _ := json.Marshal(rec)
	f, err := os.OpenFile(sink, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G703: the sink path is the test harness's own argument
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}

// fdIsType reports whether descriptor fd is a file of the given S_IF* type
// (fstat, so no os.Stdin/os.Stdout is named).
func fdIsType(fd int, want uint32) bool {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false
	}
	return uint64(st.Mode)&unix.S_IFMT == uint64(want)
}

// --- the recorder spawner -------------------------------------------------

// spawnRecorder is the Spawner of the unit tests: it records every
// SpawnSpec and, like the real watcher, writes the pidfile for a pid the
// test controls (a live sleeper) from the environment the hook built.
type spawnRecorder struct {
	mu    sync.Mutex
	specs []SpawnSpec
	// watcherPID is the pid the pidfile names; 0 writes no pidfile
	// (a watcher that never came up).
	watcherPID int
	err        error
}

func (s *spawnRecorder) Spawn(_ context.Context, spec SpawnSpec) (int, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	if s.watcherPID == 0 {
		return 4242, nil
	}
	w, err := config.FromWatcherEnv(spec.Env)
	if err != nil {
		return 0, err
	}
	m, err := sessionmap.Store{StateDir: w.StateDir}.ReadByPID(w.ClaudePID)
	if err != nil {
		return 0, err
	}
	info, err := procutil.Lookup(s.watcherPID)
	if err != nil {
		return 0, err
	}
	entry := pidfile.Entry{
		PID: s.watcherPID, StartToken: info.StartToken, BrigadeSessionID: m.BrigadeSessionID,
		SocketPath:  adapterkit.Getenv(spec.Env, envMessagingSocket),
		TokenSHA256: pidfile.TokenSHA256(adapterkit.Getenv(spec.Env, envMessagingToken)),
	}
	path := pidfile.Path(w.StateDir, w.ClaudePID)
	if cerr := pidfile.Create(path, entry); cerr != nil {
		v, verr := pidfile.Check(path, procutil.Lookup)
		if verr != nil {
			return 0, verr
		}
		if rerr := pidfile.Replace(path, v.Entry, entry); rerr != nil {
			return 0, rerr
		}
	}
	return s.watcherPID, nil
}

func (s *spawnRecorder) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.specs)
}

func (s *spawnRecorder) last(t *testing.T) SpawnSpec {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) == 0 {
		t.Fatal("no watcher spawn was recorded")
	}
	return s.specs[len(s.specs)-1]
}

// --- the in-process adapter spawn seam ------------------------------------

// adapterCall is one recorded adapter child: its argv, environment and
// stdin document, as adapterclient handed them to the seam.
type adapterCall struct {
	Argv  []string
	Env   []string
	Stdin []byte
}

// adapterSeam answers adapter calls in-process (adapterclient.Client.Spawn)
// so a test sees the exact stdin document (U-22) and can assert zero
// spawns. Responses are keyed by "<group> <verb>" ("describe" for
// describe) and consumed in order, the last repeating.
type adapterSeam struct {
	mu        sync.Mutex
	calls     []adapterCall
	responses map[string][]fakeadapter.Response
	describe  jsontext.Value
}

func (a *adapterSeam) spawn(_ context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, adapterCall{Argv: append([]string{}, spec.Argv...), Env: append([]string{}, spec.Env...), Stdin: append([]byte{}, spec.Stdin...)})
	key := verbOf(spec.Argv)
	if key == "describe" {
		return okResult(a.describe), nil
	}
	list := a.responses[key]
	if len(list) == 0 {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "the seam has no response for " + key}
	}
	resp := list[0]
	if len(list) > 1 {
		a.responses[key] = list[1:]
	}
	if resp.Error != nil {
		return &adapterkit.SpawnResult{
			Envelope: &protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: resp.Error},
			ExitCode: resp.Error.Code.Exit(),
		}, nil
	}
	return okResult(resp.Result), nil
}

func okResult(result jsontext.Value) *adapterkit.SpawnResult {
	return &adapterkit.SpawnResult{Envelope: &protocol.Envelope{OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: result}}
}

// verbOf finds "<group> <verb>" in an adapter argv (after --profile <p>).
func verbOf(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--profile" {
			rest := argv[i+2:]
			if len(rest) == 0 {
				return ""
			}
			if rest[0] == "describe" {
				return "describe"
			}
			if len(rest) > 1 {
				return rest[0] + " " + rest[1]
			}
			return rest[0]
		}
	}
	return ""
}

func (a *adapterSeam) verbs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.calls))
	for _, c := range a.calls {
		out = append(out, verbOf(c.Argv))
	}
	return out
}

func (a *adapterSeam) callsFor(verb string) []adapterCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []adapterCall
	for _, c := range a.calls {
		if verbOf(c.Argv) == verb {
			out = append(out, c)
		}
	}
	return out
}

// --- wire documents -------------------------------------------------------

const (
	teamRef  = "team-ref-opaque-1"
	teamName = "ops"
	msgTok   = "cc-messaging-secret-MUST-NOT-LEAK-7d3e9c1a"
	senderA  = "6f0f2b41-5a3c-49d7-b8e2-0c7a4f1e6d33"
	senderB  = "1111aaaa-0000-4bbb-8ccc-dddddddddddd"
)

// describeDoc is a valid describe result for a joined profile.
func describeDoc(version, team string) jsontext.Value {
	d := protocol.DescribeResult{
		ProtocolVersion: version,
		Adapter:         protocol.AdapterInfo{Name: fakeadapter.AdapterName, Version: fakeadapter.AdapterVersion},
		Delivery:        protocol.DeliveryInfo{Guarantee: protocol.GuaranteeAtLeastOnce, Ordering: "none", AckState: protocol.AckStateInjected},
		Capabilities:    []string{"team.roster", "message.receive", "message.watch.push", "message.watch.stdin_commands", "session.resume", "session.workspace_label", "session.inbound"},
		Limits:          protocol.DefaultLimits(),
		Lease:           protocol.DefaultLease(),
		Retention:       protocol.DefaultRetention(),
		Profile:         protocol.ProfileInfo{Name: "default", State: protocol.ProfileStateJoined, TeamRef: teamRef, TeamName: team, PrincipalRef: "principal-self", HumanLabel: "me@example.com"},
	}
	b, err := json.Marshal(&d)
	if err != nil {
		panic(err)
	}
	return b
}

func mustJSON(v any) jsontext.Value {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var fixedTime = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// registerDoc is a `session register` result.
func registerDoc(id, name string, resumed bool) jsontext.Value {
	return mustJSON(adapterclient.RegisterResult{
		SessionRecord: protocol.SessionRecord{
			SessionID: id, SessionName: name, PrincipalRef: "principal-self", HumanLabel: "me@example.com",
			State: protocol.SessionStateActive, Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
			LastSeenAt: fixedTime, LeaseUntil: fixedTime.Add(90 * time.Second), CreatedAt: fixedTime, IsSelf: true,
		},
		Resumed: resumed, LeaseSeconds: 90, ServerTime: fixedTime,
	})
}

func heartbeatDoc(id string) jsontext.Value {
	return mustJSON(protocol.HeartbeatResult{SessionID: id, State: protocol.SessionStateActive, LeaseUntil: fixedTime.Add(90 * time.Second), ServerTime: fixedTime})
}

func closeDoc() jsontext.Value {
	return mustJSON(adapterclient.CloseResult{SessionID: "brigade-sess-1", State: protocol.SessionStateOffline})
}

func ackDoc(ids ...string) jsontext.Value {
	if ids == nil {
		ids = []string{}
	}
	return mustJSON(protocol.AckResult{Acked: ids, Unknown: []string{}})
}

func msgDoc(id, sender, name, body string) protocol.MessageEnvelope {
	return protocol.MessageEnvelope{
		ProtocolVersion: protocol.ProtocolVersion, Kind: protocol.KindText, MessageID: id, TeamRef: teamRef,
		Sender:             protocol.Sender{PrincipalRef: "principal-" + sender, HumanLabel: "alice@example.com", SessionID: sender, SessionName: name},
		RecipientSessionID: "self-session", Body: body, CreatedAt: fixedTime, DeliveryState: protocol.DeliveryStateAccepted,
	}
}

func receiveDoc(msgs ...protocol.MessageEnvelope) jsontext.Value {
	if msgs == nil {
		msgs = []protocol.MessageEnvelope{}
	}
	return mustJSON(adapterclient.ReceiveResult{Messages: msgs})
}

func errResp(code protocol.Code, reason string) fakeadapter.Response {
	e := &protocol.ErrorObject{Code: code, Message: "scripted " + string(code), Retryable: code.Retryable()}
	if reason != "" {
		e.Details = map[string]string{"reason": reason}
	}
	return fakeadapter.Response{Error: e}
}

func okResp(result jsontext.Value) fakeadapter.Response { return fakeadapter.Response{Result: result} }

// --- the fixture ----------------------------------------------------------

// A fixture is one hermetic session: a temp XDG triple, a reaped sleeper
// as CLAUDE_PID, a fake registry entry, the scripted fake adapter selected
// through the adapter_command option, and a recorder spawner. Nothing in
// it touches the developer's account.
type fixture struct {
	t          *testing.T
	dirs       testutil.Dirs
	pid        int
	nativeID   string
	socket     string
	stateDir   string
	configDir  string
	cwd        string
	teamKey    string
	pluginBin  string
	scriptPath string
	dumpPath   string
	script     fakeadapter.Script
	registry   *fakeregistry.Recorder
	spawner    *spawnRecorder
	seam       *adapterSeam
	now        time.Time
	extraEnv   []string
	deps       Deps
	// noSocket omits the two CLAUDE_CODE_MESSAGING_* variables (a host
	// without an inbox socket); noAdapterOption omits the adapter_command
	// option (the D36 chain then runs from the sidecar); entrypoint is
	// CLAUDE_CODE_ENTRYPOINT, omitted when "". adapterkit.Getenv treats an
	// empty later entry as unset, so these cannot be expressed by appending
	// `VAR=`.
	noSocket        bool
	noAdapterOption bool
	entrypoint      string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := testutil.NewDirs(t.TempDir())
	if err := d.Mkdir(); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(d.PluginRoot, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "brigade"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // G306: an executable fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "VERSION"), []byte("0.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid := testutil.NewSleeper(t)
	f := &fixture{
		t:          t,
		dirs:       d,
		pid:        pid,
		nativeID:   "503ea697-f737-41f1-82d3-168f4fbc2c00",
		socket:     filepath.Join(d.Root, "inbox.sock"),
		stateDir:   filepath.Join(d.XDGState, "brigade"),
		configDir:  filepath.Join(d.XDGConfig, "brigade"),
		pluginBin:  realpath(t, filepath.Join(binDir, "brigade")),
		dumpPath:   filepath.Join(d.Root, "dump.ndjson"),
		spawner:    &spawnRecorder{},
		now:        fixedTime,
		entrypoint: "cli",
	}
	// P7-6: every fixture is born attachable — a checkout, its team
	// file, the binding and the pin. Tests of the not-joined/drift/silent
	// paths override f.cwd or the store afterwards.
	f.seedTeam(t)
	f.scriptPath = filepath.Join(d.Root, "script.json")
	f.script = fakeadapter.Script{
		Describe: describeDoc(protocol.ProtocolVersion, teamName),
		Responses: map[string][]fakeadapter.Response{
			"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
			"session heartbeat": {okResp(heartbeatDoc("brigade-sess-1"))},
			"session close":     {okResp(closeDoc())},
			"message receive":   {okResp(receiveDoc())},
			"message ack":       {okResp(ackDoc())},
		},
		DumpFile: f.dumpPath,
	}
	f.registry = fakeregistry.New(t, map[int]string{pid: fakeregistry.Observed(pid, "payments-api", "busy", f.socket)})
	f.deps = Deps{
		Now:      func() time.Time { return f.now },
		Spawner:  f.spawner,
		Registry: f.registry,
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Lookup:   procutil.Lookup,
		// The recorder writes the pidfile synchronously when it has a
		// watcher pid, and never when it has none; a test that drives the
		// real spawner raises this to a hang catcher.
		PidfileWait: 100 * time.Millisecond,
	}
	t.Cleanup(func() { f.assertTokenInNoFile() })
	return f
}

// writeScript (re)writes the fake adapter script; call after editing
// f.script. The script path is unique per test, which keeps the
// process-wide describe cache from crossing tests.
func (f *fixture) writeScript() {
	f.t.Helper()
	data, err := json.Marshal(&f.script)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.scriptPath, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// useSeam routes adapter calls through the in-process seam instead of
// the fake binary.
func (f *fixture) useSeam(responses map[string][]fakeadapter.Response) *adapterSeam {
	f.t.Helper()
	f.seam = &adapterSeam{responses: responses, describe: describeDoc(protocol.ProtocolVersion, teamName)}
	f.deps.Spawn = f.seam.spawn
	return f.seam
}

// adapterOption is the adapter_command option naming the fake binary and
// its script (a JSON array, the D36 array form).
func (f *fixture) adapterOption() string {
	argv := []string{fakeAdapterBin, "--script", f.scriptPath}
	if f.seam != nil {
		// The seam answers in-process; the path only has to be absolute
		// and unique for the describe cache.
		argv = []string{filepath.Join(f.dirs.Root, "seam-adapter"), "--script", f.scriptPath}
	}
	return string(mustJSON(argv))
}

// env is the hook's environment: the hermetic triple, the session
// variables and the adapter option, plus extra (last wins).
func (f *fixture) env(extra ...string) []string {
	f.t.Helper()
	// PATH is an empty directory of the fixture's own: the adapter is
	// named by its absolute path, and the shadowing check must not see
	// whatever `brigade` the developer's shell has on its PATH.
	vars := []string{
		"PATH=" + f.emptyPath(),
		"HOME=" + f.dirs.Home,
	}
	vars = append(vars, f.dirs.Vars()...)
	vars = append(vars,
		"CLAUDE_PID="+strconv.Itoa(f.pid),
		"CLAUDE_CODE_SESSION_ID="+f.nativeID,
		"CLAUDECODE=1",
	)
	if !f.noSocket {
		vars = append(vars, "CLAUDE_CODE_MESSAGING_SOCKET="+f.socket, "CLAUDE_CODE_MESSAGING_TOKEN="+msgTok)
	}
	if f.entrypoint != "" {
		vars = append(vars, "CLAUDE_CODE_ENTRYPOINT="+f.entrypoint)
	}
	if !f.noAdapterOption {
		vars = append(vars, config.OptionAdapterCommand+"="+f.adapterOption())
	}
	vars = append(vars, f.extraEnv...)
	return append(vars, extra...)
}

// emptyPath is a directory with nothing in it, for PATH.
func (f *fixture) emptyPath() string {
	f.t.Helper()
	p := filepath.Join(f.dirs.Root, "path-empty")
	if err := os.MkdirAll(p, 0o700); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// run executes one hook subcommand in-process with stdin and returns the
// exit status, stdout and stderr.
func (f *fixture) run(sub, stdin string, extraEnv ...string) (int, string, string) {
	f.t.Helper()
	if f.seam == nil {
		f.writeScript()
	}
	var out, errOut bytes.Buffer
	code := Run([]string{sub}, cli.Streams{In: strings.NewReader(stdin), Out: &out, Err: &errOut}, f.env(extraEnv...), f.deps)
	return code, out.String(), errOut.String()
}

// startDoc is a SessionStart stdin document (the 2.1.251 shape).
func (f *fixture) startDoc(source string) string {
	cwd := f.cwd
	if cwd == "" {
		cwd = "/work/project"
	}
	return f.doc(map[string]any{
		"session_id": f.nativeID, "cwd": cwd, "hook_event_name": "SessionStart",
		"source": source, "session_title": "titled-session", "transcript_path": "/never/read.jsonl",
	})
}

// seedTeam builds the P7-6 attach preconditions for this fixture: a
// checkout with a .brigade.json, the binding under the derived key, and
// the pin — written BY HAND, because depguard rightly bars even this
// package's tests from importing teamstore/write.
func (f *fixture) seedTeam(t *testing.T) {
	t.Helper()
	f.seedTeamAdapter(t, "supabase")
}

// seedTeamAdapter is seedTeam with the team file's dialect a parameter.
func (f *fixture) seedTeamAdapter(t *testing.T, adapter string) {
	t.Helper()
	checkout := filepath.Join(t.TempDir(), "checkout")
	//nolint:gosec // G301: a checkout tree is an ordinary directory
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	const url, key, ref, name = "https://abc.supabase.co", "sb_publishable_x", "team-1", "ops"
	doc := `{"version":1,"adapter":"` + adapter + `","url":"` + url + `","publishable_key":"` + key + `","team_ref":"` + ref + `","team_name":"` + name + `"}`
	if err := os.WriteFile(filepath.Join(checkout, teamfile.FileName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	tk := teamstore.Key(adapter, url, ref)
	if err := adapterkit.SaveProfile(f.configDir, tk, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: adapter,
		URL: url, PublishableKey: key, TeamRef: ref, TeamName: name,
	}); err != nil {
		t.Fatal(err)
	}
	canon, err := teamfile.Canonicalize(checkout)
	if err != nil {
		t.Fatal(err)
	}
	pins := `{"version":1,"projects":{` + string(mustJSON(canon)) + `:{"adapter":"` + adapter + `","url":"` + url + `","publishable_key":"` + key + `","team_ref":"` + ref + `","consented_at":"2026-09-06T18:00:00Z"}}}`
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), []byte(pins)); err != nil {
		t.Fatal(err)
	}
	f.cwd = checkout
	f.teamKey = tk
}

// promptDoc is a UserPromptSubmit document.
func (f *fixture) promptDoc(mode string) string {
	cwd := f.cwd
	if cwd == "" {
		cwd = "/work/project"
	}
	return f.doc(map[string]any{
		"session_id": f.nativeID, "cwd": cwd, "hook_event_name": "UserPromptSubmit",
		"permission_mode": mode, "prompt": "never read", "prompt_id": "p1", "transcript_path": "/never/read.jsonl",
	})
}

// endDoc is a SessionEnd document.
func (f *fixture) endDoc(reason string) string {
	return f.doc(map[string]any{
		"session_id": f.nativeID, "cwd": "/work/project", "hook_event_name": "SessionEnd",
		"reason": reason, "transcript_path": "/never/read.jsonl",
	})
}

func (f *fixture) doc(m map[string]any) string {
	f.t.Helper()
	return string(mustJSON(m))
}

// store is the fixture's map store.
func (f *fixture) store() sessionmap.Store { return sessionmap.Store{StateDir: f.stateDir} }

// mustMap reads this session's by-pid map.
func (f *fixture) mustMap() *sessionmap.ByPID {
	f.t.Helper()
	m, err := f.store().ReadByPID(f.pid)
	if err != nil {
		f.t.Fatalf("by-pid map: %v", err)
	}
	return m
}

// mapExists reports whether the by-pid map file exists.
func (f *fixture) mapExists() bool {
	p, _ := f.store().ByPIDPath(f.pid)
	_, err := os.Lstat(p)
	return err == nil
}

// pidfilePath is this session's watcher pidfile.
func (f *fixture) pidfilePath() string { return pidfile.Path(f.stateDir, f.pid) }

// dump reads the fake adapter's invocation dump.
func (f *fixture) dump() []fakeadapter.Invocation {
	f.t.Helper()
	data, err := os.ReadFile(f.dumpPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var out []fakeadapter.Invocation
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var inv fakeadapter.Invocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			f.t.Fatalf("dump line: %v", err)
		}
		out = append(out, inv)
	}
	return out
}

// dumpVerbs lists "<group> <verb>" per dumped invocation, in order.
func (f *fixture) dumpVerbs() []string {
	f.t.Helper()
	var out []string
	for _, inv := range f.dump() {
		v := inv.Group
		if inv.Verb != "" {
			v += " " + inv.Verb
		}
		out = append(out, v)
	}
	return out
}

// assertTokenInNoFile walks every file under the fixture root (the XDG
// triple, the config and state dirs, the dump, the sink) and fails if the
// messaging token appears in any of them (U-25).
func (f *fixture) assertTokenInNoFile() {
	f.t.Helper()
	root, err := os.OpenRoot(f.dirs.Root)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, rerr := root.ReadFile(p); rerr == nil && bytes.Contains(data, []byte(msgTok)) {
				f.t.Errorf("the messaging token is in %s", p)
			}
		}
		return nil // best effort: an unreadable entry is skipped, never a failure
	})
}

// realpath resolves symlinks (macOS /var → /private/var), as the hook does
// for plugin_bin (3.2: the bootstrap's resolved realpath).
func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// liveEntry is the pidfile a watcher with pid would write for itself.
func liveEntry(t *testing.T, pid int, sessionID, socket, tokenHash string) pidfile.Entry {
	t.Helper()
	info, err := procutil.Lookup(pid)
	if err != nil || !info.Exists || info.StartToken == "" {
		t.Fatalf("pid %d is not live: %+v %v", pid, info, err)
	}
	return pidfile.Entry{PID: pid, StartToken: info.StartToken, BrigadeSessionID: sessionID, SocketPath: socket, TokenSHA256: tokenHash}
}

// forge changes the last character of a token.
func forge(s string) string {
	if s == "" {
		return "x"
	}
	last := s[len(s)-1]
	repl := byte('0')
	if last == '0' {
		repl = '1'
	}
	return s[:len(s)-1] + string(repl)
}

// alive reports whether pid is a live process (not a zombie).
func alive(pid int) bool {
	info, err := procutil.Lookup(pid)
	return err == nil && info.Exists && !info.Zombie
}

// lines splits output into non-empty lines.
func lines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// countEnv counts the entries of env whose name is name.
func countEnv(env []string, name string) int {
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			n++
		}
	}
	return n
}

// envValue is the last value of name in env.
func envValue(env []string, name string) string { return adapterkit.Getenv(env, name) }
