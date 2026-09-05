package watch_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// Hang catchers, never performance bounds (plan 7.3).
const (
	waitLong  = 60 * time.Second
	waitShort = 30 * time.Second
	pollEvery = 10 * time.Millisecond
)

// helperMarker makes this test binary act as another process: the
// watcher itself (`watcher`, for the detachment tests) or a scripted
// adapter that exits with a chosen status on `message watch` (`adapter
// exit=<n>`), which the fake adapter's watch script cannot express.
const helperMarker = "watch-test-helper"

// The two adapter binaries the tests drive as real children, built once.
var (
	fsAdapterBin   string
	fakeAdapterBin string
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == helperMarker {
		os.Exit(runHelper(os.Args[2], os.Args[3:])) //nolint:forbidigo // the helper IS a process entrypoint
	}
	dir, err := os.MkdirTemp("", "watch-bins-")
	if err != nil {
		panic(err)
	}
	fsAdapterBin = mustBuild(dir, "brigade-adapter-fs", "github.com/appshapes/brigade/cmd/brigade-adapter-fs")
	fakeAdapterBin = mustBuild(dir, "brigade-fake-adapter", "github.com/appshapes/brigade/cmd/brigade-fake-adapter")
	warmExecutable(fsAdapterBin)
	warmExecutable(fakeAdapterBin)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code) //nolint:forbidigo // TestMain owns the process exit
}

// mustBuild compiles a repository binary the way `make build` does
// (CGO_ENABLED=0, -trimpath, no -race), the same shape as testutil.Build
// and adapterclient's helpers: TestMain has no testing.TB for Build.
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
// `go test` creates every package's fresh binaries at once — longer than
// the 3 s one-shot budgets (WatchRequestTimeout, DescribeTimeout) the
// real-child tests here exercise (TestOneShotCommandsWithoutStdinCommands
// failed that way in a five-package run). The second exec costs ~10 ms.
func warmExecutable(bin string) {
	//nolint:gosec // G204: a binary this package just built; no shell is involved
	cmd := exec.CommandContext(context.Background(), bin, "describe")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	_ = cmd.Run()
}

// runHelper is the helper process body.
func runHelper(mode string, rest []string) int {
	switch mode {
	case "watcher":
		//nolint:forbidigo // the detached watcher under test names the real process streams
		return watch.Run(rest, cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, os.Environ(), watch.RealDeps())
	case "adapter":
		return helperAdapter(rest)
	default:
		return 64
	}
}

// helperAdapter is a BAP/1 adapter whose `message watch` exits at once
// with the status in its leading `exit=<n>` argument (after an optional
// `ready` event when the argument is `ready-exit=<n>`); every other
// verb is `describe`, answered with a valid document. The harness appends
// `--profile <p> <group> [verb] [flags]` after the fixed arguments.
func helperAdapter(argv []string) int {
	if len(argv) == 0 {
		return 64
	}
	spec := argv[0]
	group, verb := "", ""
	for i := 1; i < len(argv); i++ {
		if argv[i] == "--profile" || argv[i] == "--log-level" {
			i++
			continue
		}
		if group == "" {
			group = argv[i]
			continue
		}
		if verb == "" {
			verb = argv[i]
		}
	}
	//nolint:forbidigo // a helper adapter speaks the protocol on the real stdout
	out := os.Stdout
	if group == "describe" {
		env := protocol.Envelope{OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: fakeadapter.DescribeJSON(protocol.ProtocolVersion)}
		b, _ := json.Marshal(&env)
		_, _ = out.Write(append(b, '\n'))
		return 0
	}
	if group == "message" && verb == "watch" {
		kind, value, _ := strings.Cut(spec, "=")
		code, _ := strconv.Atoi(value)
		if kind == "ready-exit" {
			b, _ := json.Marshal(&protocol.WatchReady{Event: protocol.EventReady, ProtocolVersion: protocol.ProtocolVersion, SessionID: "s1", Mode: protocol.WatchModePolling})
			_, _ = out.Write(append(b, '\n'))
		}
		return code
	}
	env := protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: &protocol.ErrorObject{Code: protocol.CodeInternal, Message: "helper adapter: unscripted verb"}}
	b, _ := json.Marshal(&env)
	_, _ = out.Write(append(b, '\n'))
	return 1
}

// helperAdapterArgv is the adapter_command that runs this test binary as
// the helper adapter in the given mode.
func helperAdapterArgv(t *testing.T, mode string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return []string{self, helperMarker, "adapter", mode}
}

// A fixture is one watched Brigade session: the directories, the by-pid
// map, the sleeper standing in for the Claude Code process, the socket
// (nil in sink mode), the messaging token and, with the fs adapter, a
// store holding two principals so a peer can send to the watched session.
type fixture struct {
	t         *testing.T
	dirs      testutil.Dirs
	claudePID int
	sock      *fakesock.Server
	token     string
	sink      string

	adapterArgv []string
	extraEnv    []string // appended last to environ (a test's override)
	sessionID   string
	teamRef     string
	teamName    string
	inbound     string
	name        string

	// fs adapter only
	root            string
	owner           *adapterclient.Client
	peer            *adapterclient.Client
	senderSessionID string

	// bodies are the message bodies the cleanup grep must find in no file
	// under the Brigade state directory (P5-9: the pending file never
	// carries a body); the fs store under FSRoot legitimately holds them.
	bodies []string
}

// fixtureOptions tune a fixture.
type fixtureOptions struct {
	sink    bool   // sink mode: no socket, no token
	inbound string // the map's inbound policy; default accept
	pid     int    // the Claude pid; default a fresh sleeper
}

// newFixture lays out the directories, the sleeper and (unless sink) the
// fake socket and a token, and registers the token grep for cleanup: after
// the watcher has exited, no file under any directory the test owns may
// carry the token (U-25's watcher half).
func newFixture(t *testing.T, o fixtureOptions) *fixture {
	t.Helper()
	dirs := testutil.NewDirs(t.TempDir())
	if err := dirs.Mkdir(); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fx := &fixture{t: t, dirs: dirs, inbound: o.inbound, name: "receiver", teamRef: "team-1", teamName: "ops"}
	if fx.inbound == "" {
		fx.inbound = protocol.InboundAccept
	}
	fx.claudePID = o.pid
	if fx.claudePID == 0 {
		fx.claudePID = testutil.NewSleeper(t)
	}
	if o.sink {
		fx.sink = filepath.Join(dirs.Root, "sink.ndjson")
	} else {
		fx.sock = fakesock.New(t)
		fx.token = "tok-" + testutil.RunID() + "-" + strconv.Itoa(fx.claudePID)
	}
	// Cleanup runs LIFO: registered here, before any watcher starts, the
	// grep runs after every watcher registered later has been stopped.
	t.Cleanup(func() { fx.assertTokenInNoFile() })
	t.Cleanup(func() { fx.assertBodiesInNoStateFile() })
	return fx
}

// forbidBody registers body for the cleanup grep over the state directory.
func (fx *fixture) forbidBody(body string) { fx.bodies = append(fx.bodies, body) }

// useFake points the fixture at the fake adapter with the given script.
func (fx *fixture) useFake(script fakeadapter.Script) {
	fx.t.Helper()
	fx.adapterArgv = []string{fakeAdapterBin, "--script", writeFakeScript(fx.t, script)}
	fx.sessionID = "s1"
}

// useHelper points the fixture at the helper adapter in the given mode.
func (fx *fixture) useHelper(mode string) {
	fx.t.Helper()
	fx.adapterArgv = helperAdapterArgv(fx.t, mode)
	fx.sessionID = "s1"
}

// useFS builds the fs store: the owner creates the team and registers the
// watched session; the peer joins with the secret and registers a sender
// session.
func (fx *fixture) useFS() {
	t := fx.t
	t.Helper()
	fx.root = fx.dirs.FSRoot
	fx.adapterArgv = []string{fsAdapterBin, "--root", fx.root}
	fx.owner = fx.fsClient("default")
	fx.peer = fx.fsClient("peer")
	ctx, cancel := context.WithTimeout(t.Context(), waitShort)
	defer cancel()

	env, err := fx.owner.Call(ctx, "team", "create", nil, &protocol.TeamCreateRequest{TeamName: fx.teamName, HumanLabel: "owner@example.com"})
	if err != nil {
		t.Fatalf("team create: %v", err)
	}
	var created protocol.TeamCreateResult
	if err := json.Unmarshal([]byte(env.Result), &created); err != nil {
		t.Fatalf("team create result: %v", err)
	}
	fx.teamRef = created.TeamRef
	reg, err := fx.owner.Register(ctx, &protocol.SessionRegistration{
		Harness: "claude-code", HarnessVersion: "test", SessionName: fx.name,
		Activity: protocol.ActivityIdle, Inbound: fx.inbound,
	})
	if err != nil {
		t.Fatalf("register receiver: %v", err)
	}
	fx.sessionID = reg.SessionID

	if _, err := fx.peer.Call(ctx, "team", "join", nil, &protocol.TeamJoinRequest{JoinSecret: created.JoinSecret, HumanLabel: "peer@example.com"}); err != nil {
		t.Fatalf("team join: %v", err)
	}
	sender, err := fx.peer.Register(ctx, &protocol.SessionRegistration{
		Harness: "claude-code", HarnessVersion: "test", SessionName: "sender",
		Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
	})
	if err != nil {
		t.Fatalf("register sender: %v", err)
	}
	fx.senderSessionID = sender.SessionID
}

// fsClient is a Client on the fs adapter for one profile.
func (fx *fixture) fsClient(profile string) *adapterclient.Client {
	return &adapterclient.Client{
		Adapter:   config.Adapter{Argv: fx.adapterArgv, Source: config.SourceMap},
		Profile:   profile,
		ConfigDir: fx.dirs.BrigadeConfig,
		StateDir:  fx.dirs.BrigadeState,
		Environ:   []string{"PATH=" + os.Getenv("PATH")},
	}
}

// send sends body from the peer's sender session to the watched session
// and returns the message id.
func (fx *fixture) send(body string) string {
	fx.t.Helper()
	return fx.sendWith(body, "")
}

// sendWith is send with an idempotency key.
func (fx *fixture) sendWith(body, key string) string {
	fx.t.Helper()
	ctx, cancel := context.WithTimeout(fx.t.Context(), waitShort)
	defer cancel()
	res, err := fx.peer.Send(ctx, &protocol.SendRequest{
		SenderSessionID: fx.senderSessionID, RecipientSessionID: fx.sessionID, Body: body, IdempotencyKey: key,
	})
	if err != nil {
		fx.t.Fatalf("send: %v", err)
	}
	return res.MessageID
}

// writeMap writes the by-pid map for the sleeper.
func (fx *fixture) writeMap() {
	fx.t.Helper()
	fx.writeMapWith(func(*sessionmap.ByPID) {})
}

// writeMapWith writes the map after applying edit.
func (fx *fixture) writeMapWith(edit func(*sessionmap.ByPID)) {
	fx.t.Helper()
	socket := ""
	if fx.sock != nil {
		socket = fx.sock.Path()
	}
	now := time.Now().UTC()
	m := &sessionmap.ByPID{
		ClaudePID:        fx.claudePID,
		ClaudeSessionID:  "native-1",
		BrigadeSessionID: fx.sessionID,
		TeamRef:          fx.teamRef,
		TeamName:         fx.teamName,
		SessionName:      fx.name,
		Inbound:          fx.inbound,
		SocketPath:       socket,
		Profile:          "default",
		ConfigDir:        fx.dirs.BrigadeConfig,
		AdapterCommand:   fx.adapterArgv,
		HarnessVersion:   "test",
		RegisteredAt:     now,
		UpdatedAt:        now,
	}
	edit(m)
	if err := fx.store().WriteByPID(m); err != nil {
		fx.t.Fatalf("write map: %v", err)
	}
}

func (fx *fixture) store() sessionmap.Store {
	return sessionmap.Store{StateDir: fx.dirs.BrigadeState}
}

// environ is the hook-built watcher environment (6.6): PATH, HOME, the
// XDG triple, CLAUDE_CONFIG_DIR, the socket and token (socket mode), and
// the six watcher variables. Nothing of the test process's environment
// but PATH reaches it.
func (fx *fixture) environ(extra ...string) []string {
	fx.t.Helper()
	adapter, err := config.DecodeAdapter(mustEncode(fx.t, fx.adapterArgv))
	if err != nil {
		fx.t.Fatalf("decode adapter: %v", err)
	}
	we := config.WatcherEnv{
		ClaudePID: fx.claudePID, Profile: "default",
		ConfigDir: fx.dirs.BrigadeConfig, StateDir: fx.dirs.BrigadeState,
		Adapter: adapter, TeamInbound: config.Inbound(fx.inbound),
	}
	vars, err := we.Vars()
	if err != nil {
		fx.t.Fatalf("watcher vars: %v", err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fx.dirs.Home,
		"XDG_CONFIG_HOME=" + fx.dirs.XDGConfig,
		"XDG_STATE_HOME=" + fx.dirs.XDGState,
		"XDG_CACHE_HOME=" + fx.dirs.XDGCache,
		"CLAUDE_CONFIG_DIR=" + fx.dirs.ClaudeConfig,
		"BRIGADE_LOG_LEVEL=debug",
	}
	if fx.sock != nil {
		env = append(env, watch.SocketVar+"="+fx.sock.Path(), watch.TokenVar+"="+fx.token)
	}
	env = append(env, vars...)
	env = append(env, fx.extraEnv...)
	return append(env, extra...)
}

func mustEncode(t *testing.T, argv []string) string {
	t.Helper()
	b, err := json.Marshal(argv)
	if err != nil {
		t.Fatalf("encode argv: %v", err)
	}
	return string(b)
}

// args are the watcher's argv: `--sink <file>` in sink mode.
func (fx *fixture) args() []string {
	if fx.sink != "" {
		return []string{"--sink", fx.sink}
	}
	return nil
}

// deps are fast production-shaped dependencies: real process facts, the
// real registry and poster, tiny intervals and schedules so a test runs
// in seconds. The real defaults (30 s heartbeats, 1..30 s restarts) are
// what RealDeps returns and TestRealDeps pins.
func (fx *fixture) deps() watch.Deps {
	return watch.Deps{
		PollInterval:      50 * time.Millisecond,
		HeartbeatInterval: 300 * time.Millisecond,
		ReadyTimeout:      20 * time.Second,
		ReplaceWait:       500 * time.Millisecond,
		RestartSchedule: func() *backoff.Schedule {
			return backoff.New(5*time.Millisecond, 40*time.Millisecond, seeded(1))
		},
		InjectSchedule: func() *backoff.Schedule {
			return backoff.New(5*time.Millisecond, 40*time.Millisecond, seeded(2))
		},
	}
}

// seeded is a fixed-seed jitter source: the delays are pinned, not secret.
func seeded(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // G404: jitter for a test schedule, not a secret
}

// A running is one in-process watcher started by start.
type running struct {
	t      *testing.T
	exit   chan int
	stop   chan struct{}
	once   sync.Once
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	mu     sync.Mutex
	code   int
	done   bool
}

// start runs watch.Run in a goroutine with deps.Stop wired to the handle;
// Cleanup stops it and waits (a hang catcher fails the test).
func (fx *fixture) start(deps watch.Deps, args ...string) *running {
	fx.t.Helper()
	r := &running{t: fx.t, exit: make(chan int, 1), stop: make(chan struct{}), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	deps.Stop = r.stop
	environ := fx.environ()
	go func() {
		r.exit <- watch.Run(args, cli.Streams{In: strings.NewReader(""), Out: r.stdout, Err: r.stderr}, environ, deps)
	}()
	fx.t.Cleanup(func() {
		r.requestStop()
		code := r.wait()
		if out := r.stdout.String(); out != "" {
			fx.t.Errorf("the watcher wrote to stdout: %q", out)
		}
		fx.t.Logf("watcher exit %d; stderr %q", code, r.stderr.String())
	})
	return r
}

// requestStop closes the stop channel once.
func (r *running) requestStop() { r.once.Do(func() { close(r.stop) }) }

// wait blocks until Run returned and returns its exit status.
func (r *running) wait() int {
	r.mu.Lock()
	if r.done {
		defer r.mu.Unlock()
		return r.code
	}
	r.mu.Unlock()
	select {
	case code := <-r.exit:
		r.mu.Lock()
		r.code, r.done = code, true
		r.mu.Unlock()
		return code
	case <-time.After(waitLong):
		r.t.Fatalf("the watcher did not exit within %s", waitLong)
		return -1
	}
}

// exited reports whether Run has returned, without blocking.
func (r *running) exited() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return true
	}
	select {
	case code := <-r.exit:
		r.code, r.done = code, true
		return true
	default:
		return false
	}
}

// stopAndWait ends the watcher and returns its exit status.
func (r *running) stopAndWait() int {
	r.requestStop()
	return r.wait()
}

// --- files the tests read -------------------------------------------------

func (fx *fixture) logPath() string {
	return filepath.Join(fx.dirs.BrigadeState, "logs", "watcher-"+strconv.Itoa(fx.claudePID)+".log")
}

func (fx *fixture) pidfilePath() string {
	return filepath.Join(fx.dirs.BrigadeState, "watchers", strconv.Itoa(fx.claudePID)+".json")
}

func (fx *fixture) noticePath() string {
	return filepath.Join(fx.dirs.BrigadeState, "state", strconv.Itoa(fx.claudePID)+".notice")
}

// seenPath is the seen file of the fixture's Brigade session (P5-14: keyed
// by the session id, not the pid; the literal join pins the layout
// independently of inbound.SeenPath). Every fixture session id — "s1", or
// the fs adapter's 32 hex — is a safe stem, so the plain branch applies.
func (fx *fixture) seenPath() string {
	return filepath.Join(fx.dirs.BrigadeState, "state", "seen", fx.sessionID+".json")
}

// pendingPath and releasePath are the hold policy's two files for the
// fixture's Brigade session (P5-9: keyed like the seen file; the literal
// join pins the layout independently of inbound.PendingPath).
func (fx *fixture) pendingPath() string {
	return filepath.Join(fx.dirs.BrigadeState, "state", "pending", fx.sessionID+".json")
}

func (fx *fixture) releasePath() string {
	return filepath.Join(fx.dirs.BrigadeState, "state", "release", fx.sessionID+".json")
}

// pendingEntries reads the fixture's pending file; a missing file is nil.
func (fx *fixture) pendingEntries() []inbound.PendingEntry {
	fx.t.Helper()
	f, err := inbound.FilePendingStore{Path: fx.pendingPath(), SessionID: fx.sessionID}.Load()
	if err != nil {
		fx.t.Fatalf("pending file: %v", err)
	}
	return f.Entries
}

// pendingIDs lists the ids of the pending file, oldest first, with a "+"
// suffix on a released one.
func (fx *fixture) pendingIDs() []string {
	fx.t.Helper()
	var ids []string
	for _, e := range fx.pendingEntries() {
		id := e.MessageID
		if e.Released() {
			id += "+"
		}
		ids = append(ids, id)
	}
	return ids
}

// writeRelease writes a release file for sessionID naming ids.
func (fx *fixture) writeRelease(sessionID string, ids ...string) {
	fx.t.Helper()
	if err := inbound.WriteRelease(fx.releasePath(), inbound.ReleaseFile{SessionID: sessionID, MessageIDs: ids, WrittenAt: time.Now().UTC()}); err != nil {
		fx.t.Fatalf("write release: %v", err)
	}
}

// logLines parses the watcher log (NDJSON) into maps; a missing log is
// empty.
func (fx *fixture) logLines() []map[string]any {
	fx.t.Helper()
	data, err := os.ReadFile(fx.logPath())
	if err != nil {
		return nil
	}
	var out []map[string]any
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			fx.t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// logCount counts log lines whose msg is msg and whose attributes match.
func (fx *fixture) logCount(msg string, attrs map[string]any) int {
	n := 0
	for _, l := range fx.logLines() {
		if l["msg"] != msg {
			continue
		}
		ok := true
		for k, v := range attrs {
			if fmt.Sprint(l[k]) != fmt.Sprint(v) {
				ok = false
				break
			}
		}
		if ok {
			n++
		}
	}
	return n
}

// logHas reports whether a line with msg and the attributes exists.
func (fx *fixture) logHas(msg string, attrs map[string]any) bool {
	return fx.logCount(msg, attrs) > 0
}

// waitLog polls until logHas is true.
func (fx *fixture) waitLog(msg string, attrs map[string]any) {
	fx.t.Helper()
	testutil.Eventually(fx.t, waitShort, pollEvery, func() bool { return fx.logHas(msg, attrs) })
}

// sinkRecords parses the sink file.
type sinkRecord struct {
	TS              time.Time `json:"ts"`
	Frame           string    `json:"frame"`
	MessageID       string    `json:"message_id"`
	SenderSessionID string    `json:"sender_session_id"`
}

func (fx *fixture) sinkRecords() []sinkRecord {
	fx.t.Helper()
	data, err := os.ReadFile(fx.sink)
	if err != nil {
		return nil
	}
	var out []sinkRecord
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r sinkRecord
		if err := json.Unmarshal(line, &r); err != nil {
			fx.t.Fatalf("sink line is not JSON: %q", line)
		}
		out = append(out, r)
	}
	return out
}

// --- the fs store ---------------------------------------------------------

// storeIDs lists the message ids under <root>/teams/<team>/<kind>/<sid>/.
func (fx *fixture) storeIDs(kind string) []string {
	fx.t.Helper()
	dir := filepath.Join(fx.root, "teams", fx.teamRef, kind, fx.sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		// The fs adapter writes atomically through a `.tmp-` sibling; a
		// listing that catches one mid-rename must not read it as an id.
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if _, id, ok := strings.Cut(name, "."); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func (fx *fixture) inboxIDs() []string { return fx.storeIDs("inbox") }
func (fx *fixture) ackedIDs() []string { return fx.storeIDs("acked") }

// sessionFile is what the fs store holds for the watched session.
type sessionFile struct {
	SessionName string     `json:"session_name"`
	Activity    string     `json:"activity"`
	Inbound     string     `json:"inbound"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	LeaseUntil  time.Time  `json:"lease_until"`
	ClosedAt    *time.Time `json:"closed_at"`
}

func (fx *fixture) session() sessionFile {
	fx.t.Helper()
	path := filepath.Join(fx.root, "teams", fx.teamRef, "sessions", fx.sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		fx.t.Fatalf("read session file: %v", err)
	}
	var s sessionFile
	if err := json.Unmarshal(data, &s); err != nil {
		fx.t.Fatalf("session file: %v", err)
	}
	return s
}

// --- fake adapter helpers ---------------------------------------------------

func writeFakeScript(t *testing.T, s fakeadapter.Script) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.json")
	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func rawLine(t *testing.T, v any) jsontext.Value {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal watch line: %v", err)
	}
	return b
}

func readyLine(t *testing.T) fakeadapter.WatchLine {
	t.Helper()
	return fakeadapter.WatchLine{Raw: rawLine(t, &protocol.WatchReady{
		Event: protocol.EventReady, ProtocolVersion: protocol.ProtocolVersion, SessionID: "s1", Mode: protocol.WatchModePush,
	})}
}

func messageLine(t *testing.T, m protocol.MessageEnvelope, delayMS int) fakeadapter.WatchLine {
	t.Helper()
	return fakeadapter.WatchLine{Raw: rawLine(t, &protocol.WatchMessage{Event: protocol.EventMessage, Message: m}), DelayMS: delayMS}
}

// fakeMessage is a valid envelope for the fake adapter's replay.
func fakeMessage(id, sender, body string) protocol.MessageEnvelope {
	return protocol.MessageEnvelope{
		ProtocolVersion:    protocol.ProtocolVersion,
		Kind:               protocol.KindText,
		MessageID:          id,
		TeamRef:            "team-1",
		Sender:             protocol.Sender{PrincipalRef: "p-" + sender, SessionID: sender, SessionName: "peer " + sender},
		RecipientSessionID: "s1",
		Body:               body,
		CreatedAt:          time.Unix(1_700_000_000, 0).UTC(),
		DeliveryState:      protocol.DeliveryStateAccepted,
	}
}

// --- the token grep (U-25, watcher half) -------------------------------------

// assertTokenInNoFile walks every file under the fixture's directories
// and the socket directory and fails when the token appears in any of them.
// A positive control (TestTokenGrepBites) plants the token and proves the
// walk finds it.
func (fx *fixture) assertTokenInNoFile() {
	fx.t.Helper()
	if fx.token == "" {
		return
	}
	hits := tokenHits(fx.t, fx.token, fx.dirs.Root)
	if len(hits) > 0 {
		fx.t.Errorf("the messaging token appears in files: %v", hits)
	}
}

// assertBodiesInNoStateFile is the body half of the grep (P5-9): after the
// watcher has exited, no file under the Brigade STATE directory may carry
// a held message's body. A positive control (TestBodyGrepBites) plants one
// and proves the walk finds it.
func (fx *fixture) assertBodiesInNoStateFile() {
	fx.t.Helper()
	for _, body := range fx.bodies {
		if hits := tokenHits(fx.t, body, fx.dirs.BrigadeState); len(hits) > 0 {
			fx.t.Errorf("a held body appears in state files: %v", hits)
		}
	}
}

// tokenHits lists the regular files under root that contain token.
func tokenHits(t *testing.T, token, root string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil //nolint:nilerr // an unreadable entry is not a hit
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // G304: walking the test's own tree
		if rerr != nil {
			return nil //nolint:nilerr // same
		}
		if bytes.Contains(data, []byte(token)) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}
