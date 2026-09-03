package e2e

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// Hang catchers, never performance bounds (plan 7.3): the fs adapter's
// watch polls every 200 ms and the production watcher polls liveness
// every 2 s, so an expected step is well under a second; a loaded -race
// run on a slow CI runner still has an order of magnitude to spare.
const (
	waitLong  = 60 * time.Second
	pollEvery = 20 * time.Millisecond
)

// The two principals of every test: alice creates the team and is the
// watched session; bob joins with the secret and sends to her.
const (
	teamName   = "ops"
	aliceLabel = "alice@example.com"
	bobLabel   = "bob@example.com"
	aliceName  = "payments-api"
	bobName    = "billing"
)

// A rig is one hermetic machine: the built binaries, a temp XDG triple
// and Claude config dir, the fs store, a recording adapter wrapper, and
// the sessions the test registers. Nothing in it touches the developer's
// account; every path is under t.TempDir except the fake socket, which
// lives under /tmp for sun_path's sake (7.3).
type rig struct {
	t          *testing.T
	dirs       testutil.Dirs
	brigade    string // the built bin/brigade
	fsAdapter  string // the built bin/brigade-adapter-fs
	wrapper    string // the recording adapter wrapper (a shebang adapter)
	argvLog    string // every adapter child's argv, one argument per line
	envLog     string // every adapter child's exported environment
	root       string // the fs store
	configDir  string // ${XDG_CONFIG_HOME}/brigade, the in-session config dir
	stateDir   string // ${XDG_STATE_HOME}/brigade, the in-session state dir
	pluginBin  string // ${CLAUDE_PLUGIN_ROOT}/bin/brigade
	emptyPath  string // an empty PATH entry (the shadowing check must see nothing)
	secretFile string
	teamRef    string
	adapterCmd []string // the by-pid map's adapter_command: the wrapper with --root

	mu       sync.Mutex
	watchers []int // every watcher pid seen, for cleanup
}

// newRig builds the binaries, lays out the directories and writes the
// recording wrapper. The token grep and the watcher reaping are registered
// here, before any process starts, so they run after every later Cleanup
// (Cleanup is LIFO): the sleepers registered later die first, then the
// watchers are reaped, then the grep walks the tree.
func newRig(t *testing.T) *rig {
	t.Helper()
	dirs := testutil.NewDirs(t.TempDir())
	if err := dirs.Mkdir(); err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t:         t,
		dirs:      dirs,
		brigade:   testutil.Build(t, "./cmd/brigade"),
		fsAdapter: testutil.Build(t, "./cmd/brigade-adapter-fs"),
		root:      dirs.FSRoot,
		configDir: filepath.Join(dirs.XDGConfig, "brigade"),
		stateDir:  filepath.Join(dirs.XDGState, "brigade"),
		emptyPath: filepath.Join(dirs.Root, "path-empty"),
		argvLog:   filepath.Join(dirs.Root, "adapter-argv.log"),
		envLog:    filepath.Join(dirs.Root, "adapter-env.log"),
	}
	for _, d := range []string{r.emptyPath, filepath.Join(dirs.PluginRoot, "bin"), filepath.Join(dirs.ClaudeConfig, "sessions"), filepath.Join(dirs.Root, "xdg", "data")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The plugin root the hook resolves plugin_bin and VERSION from (3.2,
	// the weekly prune): a stub bootstrap and the pre-release version.
	bootstrap := filepath.Join(dirs.PluginRoot, "bin", "brigade")
	writeExecutable(t, bootstrap, "#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(dirs.PluginRoot, "bin", "VERSION"), []byte("0.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.pluginBin = realpath(t, bootstrap)
	// The recording wrapper: an adapter with a shebang line that appends
	// its argv and its exported environment to two files and execs the
	// fs adapter. The harness execs the file (docs/adapter-authors.md); it
	// never spawns a shell of its own. Only shell builtins are used, so it
	// works with the empty PATH the hook and the commands hand their
	// children.
	r.wrapper = filepath.Join(dirs.Root, "adapter.sh")
	writeExecutable(t, r.wrapper, "#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+shellQuote(r.argvLog)+"\n"+
		"export -p >> "+shellQuote(r.envLog)+"\nexec "+shellQuote(r.fsAdapter)+" \"$@\"\n")
	r.adapterCmd = []string{r.wrapper, "--root", r.root}
	r.secretFile = filepath.Join(dirs.Root, "join.secret")
	t.Cleanup(r.reapWatchers)
	return r
}

// adapterJSON is the adapter command in the D36 array form: the value of
// the adapter_command option and of `profile init --adapter`.
func (r *rig) adapterJSON() string {
	b, err := json.Marshal(r.adapterCmd)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// baseEnv is the environment every process starts from: an empty PATH
// entry, the temp HOME, the XDG dirs, the Claude config dir and the plugin
// root. No BRIGADE_* at all, so inside and outside a session the config
// and state dirs resolve to the same XDG defaults. GOCOVERDIR travels
// when the ambient run has one (plan 9.4).
func (r *rig) baseEnv() []string {
	env := []string{
		"PATH=" + r.emptyPath,
		"HOME=" + r.dirs.Home,
		"XDG_CONFIG_HOME=" + r.dirs.XDGConfig,
		"XDG_STATE_HOME=" + r.dirs.XDGState,
		"XDG_CACHE_HOME=" + r.dirs.XDGCache,
		"XDG_DATA_HOME=" + filepath.Join(r.dirs.Root, "xdg", "data"),
		"CLAUDE_CONFIG_DIR=" + r.dirs.ClaudeConfig,
		"CLAUDE_PLUGIN_ROOT=" + r.dirs.PluginRoot,
	}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		env = append(env, "GOCOVERDIR="+dir)
	}
	return env
}

// A session is one Claude Code process as the hooks see it: a reaped
// sleeper as CLAUDE_PID, a native session id, the profile, and the inbox
// socket and token when the host has one.
type session struct {
	pid      int
	nativeID string
	profile  string
	title    string
	socket   string
	token    string
}

// newSession starts a sleeper and returns the session facts. A registry
// entry (the A.3 shape) is written for it when name is set, so the hook's
// identity resolution and the watcher's per-poll registry read run against
// the real reader.
func (r *rig) newSession(profile, title, name, socket, token string) session {
	r.t.Helper()
	pid := testutil.NewSleeper(r.t)
	s := session{pid: pid, nativeID: "native-" + profile + "-" + strconv.Itoa(pid), profile: profile, title: title, socket: socket, token: token}
	if name != "" {
		entry := fakeregistry.Observed(pid, name, "busy", socket)
		path := filepath.Join(r.dirs.ClaudeConfig, "sessions", strconv.Itoa(pid)+".json")
		if err := os.WriteFile(path, []byte(entry+"\n"), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	return s
}

// env is the hook environment of s (6.5): the base environment, the
// session facts and the plugin options — the profile and the adapter
// command as the JSON array with --root.
func (r *rig) env(s session) []string {
	env := append(r.baseEnv(),
		"CLAUDE_PID="+strconv.Itoa(s.pid),
		"CLAUDE_CODE_SESSION_ID="+s.nativeID,
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		config.OptionProfile+"="+s.profile,
		config.OptionAdapterCommand+"="+r.adapterJSON(),
	)
	if s.socket != "" {
		env = append(env, "CLAUDE_CODE_MESSAGING_SOCKET="+s.socket, "CLAUDE_CODE_MESSAGING_TOKEN="+s.token)
	}
	return env
}

// hookDoc is a hook stdin document (the 2.1.251 shape). transcript_path
// and prompt are present and never read.
func (s session) hookDoc(event string, extra map[string]any) string {
	m := map[string]any{
		"session_id": s.nativeID, "cwd": "/work/project", "hook_event_name": event,
		"transcript_path": "/never/read.jsonl",
	}
	if s.title != "" {
		m["session_title"] = s.title
	}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// result is what one process left behind.
type result struct {
	exit   int
	stdout string
	stderr string
}

// run executes the built brigade binary with env and stdin and waits for
// it. The hook's own children (adapter calls) finish before it exits; the
// watcher it detaches holds none of these pipes (Setsid, null stdin, the
// log file for both outputs), so Wait returns when the hook does.
func (r *rig) run(env []string, stdin string, args ...string) result {
	r.t.Helper()
	//nolint:gosec // G204: the argv is a binary this test built; no shell is involved
	cmd := exec.CommandContext(r.t.Context(), r.brigade, args...)
	cmd.Env = env
	cmd.Dir = r.dirs.Home
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := result{stdout: out.String(), stderr: errb.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		res.exit = exitErr.ExitCode()
	default:
		r.t.Fatalf("brigade %v did not run to completion: %v (stderr %q)", args, err, errb.String())
	}
	return res
}

// mustRun is run asserting exit 0.
func (r *rig) mustRun(env []string, stdin string, args ...string) result {
	r.t.Helper()
	res := r.run(env, stdin, args...)
	if res.exit != 0 {
		r.t.Fatalf("brigade %v: exit %d\nstdout: %s\nstderr: %s", args, res.exit, res.stdout, res.stderr)
	}
	return res
}

// hook runs `brigade hook <sub>` for s with the given document and asserts
// exit 0: a hook never fails its session (6.3).
func (r *rig) hook(s session, sub, doc string) result {
	r.t.Helper()
	res := r.run(r.env(s), doc, "hook", sub)
	if res.exit != 0 {
		r.t.Fatalf("hook %s: exit %d\nstdout: %s\nstderr: %s", sub, res.exit, res.stdout, res.stderr)
	}
	return res
}

// jsonResult decodes the `result` member of a --json envelope on stdout.
func jsonResult[T any](t *testing.T, stdout string) T {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result T    `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	if !env.OK {
		t.Fatalf("envelope is not ok: %s", stdout)
	}
	return env.Result
}

// createTeam is the terminal half (CLAUDE_PID unset, the pass-through with
// piped stdin): both profiles bound to the fs adapter through `profile init
// --adapter`, alice creates the team with the secret written to a file,
// bob joins with the secret from that file.
func (r *rig) createTeam() {
	t := r.t
	t.Helper()
	env := r.baseEnv()
	for _, p := range []string{"alice", "bob"} {
		res := r.mustRun(env, "", "profile", "init", "--profile", p, "--adapter", r.adapterJSON())
		if !strings.Contains(res.stdout, `"state":"not_member"`) {
			t.Fatalf("profile init %s: %s", p, res.stdout)
		}
	}
	sidecar, err := os.ReadFile(filepath.Join(r.configDir, "profiles", "alice", "adapter"))
	if err != nil || strings.TrimSpace(string(sidecar)) != r.adapterJSON() {
		t.Fatalf("alice's sidecar = %q (%v), want %s", sidecar, err, r.adapterJSON())
	}
	create := `{"team_name":"` + teamName + `","human_label":"` + aliceLabel + `"}`
	res := r.mustRun(env, create, "team", "create", "--profile", "alice", "--secret-file", r.secretFile)
	created := jsonResult[protocol.TeamCreateResult](t, res.stdout)
	if created.TeamRef == "" || created.TeamName != teamName || created.JoinSecret != "" {
		t.Fatalf("team create: %+v (the secret must be in the file, not on stdout)", created)
	}
	r.teamRef = created.TeamRef
	secret, err := os.ReadFile(r.secretFile)
	if err != nil {
		t.Fatalf("the secret file the fs adapter writes: %v", err)
	}
	if strings.Contains(res.stdout, strings.TrimSpace(string(secret))) {
		t.Fatal("the join secret reached stdout")
	}
	join, err := json.Marshal(&protocol.TeamJoinRequest{JoinSecret: strings.TrimSpace(string(secret)), HumanLabel: bobLabel})
	if err != nil {
		t.Fatal(err)
	}
	res = r.mustRun(env, string(join), "team", "join", "--profile", "bob")
	joined := jsonResult[protocol.TeamJoinResult](t, res.stdout)
	if joined.TeamRef != r.teamRef || joined.Rejoined {
		t.Fatalf("team join: %+v", joined)
	}
}

// store is the by-pid map store the hooks write to.
func (r *rig) store() sessionmap.Store { return sessionmap.Store{StateDir: r.stateDir} }

// mustMap reads s's by-pid map.
func (r *rig) mustMap(s session) *sessionmap.ByPID {
	r.t.Helper()
	m, err := r.store().ReadByPID(s.pid)
	if err != nil {
		r.t.Fatalf("by-pid map of %d: %v", s.pid, err)
	}
	return m
}

// mapExists reports whether s's by-pid map file exists.
func (r *rig) mapExists(s session) bool {
	p, _ := r.store().ByPIDPath(s.pid)
	_, err := os.Lstat(p)
	return err == nil
}

// pidfilePath is s's watcher pidfile.
func (r *rig) pidfilePath(s session) string { return pidfile.Path(r.stateDir, s.pid) }

// liveWatcher waits for s's pidfile to name a live watcher and returns
// the entry; the pid is remembered for cleanup.
func (r *rig) liveWatcher(s session) pidfile.Entry {
	r.t.Helper()
	var v pidfile.Verdict
	testutil.Eventually(r.t, waitLong, pollEvery, func() bool {
		var err error
		v, err = pidfile.Check(r.pidfilePath(s), procutil.Lookup)
		return err == nil && v.Found && v.Alive
	})
	r.track(v.Entry.PID)
	return v.Entry
}

// track remembers a watcher pid for cleanup.
func (r *rig) track(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watchers = append(r.watchers, pid)
}

// alive reports whether pid is a live, non-zombie process.
func alive(pid int) bool {
	info, err := procutil.Lookup(pid)
	return err == nil && info.Exists && !info.Zombie
}

// crash SIGKILLs pid and waits for it to be gone: a watcher that died
// without running its exit path, so its pidfile stays behind (dead) and the
// Brigade session stays open in the store. A SIGTERM would be the clean
// exit path (the watcher removes its own pidfile and closes the session),
// which would make the later session-end assertions vacuous.
func crash(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SIGKILL %d: %v", pid, err)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(pid) })
}

// reapWatchers is the cleanup: every watcher pid a test saw, plus whatever
// a pidfile still names, is SIGTERMed and must be gone; a survivor is
// SIGKILLed and reported. The detached watchers are not this process's
// children (Setsid, released by the hook), so "gone" is procutil's verdict
// after init reaped them.
func (r *rig) reapWatchers() {
	t := r.t
	pids := map[int]bool{}
	r.mu.Lock()
	for _, pid := range r.watchers {
		pids[pid] = true
	}
	r.mu.Unlock()
	entries, _ := os.ReadDir(filepath.Join(r.stateDir, "watchers"))
	for _, e := range entries {
		if v, err := pidfile.Check(filepath.Join(r.stateDir, "watchers", e.Name()), procutil.Lookup); err == nil && v.Found {
			pids[v.Entry.PID] = true
		}
	}
	for pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(waitLong)
	for pid := range pids {
		for alive(pid) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Errorf("watcher %d survived cleanup; SIGKILLed", pid)
				break
			}
			time.Sleep(pollEvery)
		}
	}
}

// --- the fs store ---------------------------------------------------------

// storeIDs lists the message ids under <root>/teams/<team>/<kind>/<sid>/.
func (r *rig) storeIDs(kind, sessionID string) []string {
	entries, err := os.ReadDir(filepath.Join(r.root, "teams", r.teamRef, kind, sessionID))
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if _, id, ok := strings.Cut(name, "."); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// storeMessages decodes every envelope under <kind>/<sid>/.
func (r *rig) storeMessages(kind, sessionID string) []protocol.MessageEnvelope {
	r.t.Helper()
	dir := filepath.Join(r.root, "teams", r.teamRef, kind, sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []protocol.MessageEnvelope
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			r.t.Fatal(err)
		}
		var m protocol.MessageEnvelope
		if err := json.Unmarshal(data, &m); err != nil {
			r.t.Fatalf("%s: %v", e.Name(), err)
		}
		out = append(out, m)
	}
	return out
}

// sessionFile is what the fs store holds for a session; closed_at is what
// `session close` sets.
type sessionFile struct {
	SessionName string     `json:"session_name"`
	Activity    string     `json:"activity"`
	Inbound     string     `json:"inbound"`
	ClosedAt    *time.Time `json:"closed_at"`
}

func (r *rig) sessionFile(sessionID string) sessionFile {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, "teams", r.teamRef, "sessions", sessionID+".json"))
	if err != nil {
		r.t.Fatalf("session file: %v", err)
	}
	var s sessionFile
	if err := json.Unmarshal(data, &s); err != nil {
		r.t.Fatalf("session file: %v", err)
	}
	return s
}

// --- the watcher log --------------------------------------------------------

// logHas reports whether s's watcher log has a line with msg.
func (r *rig) logHas(s session, msg string) bool { return r.logCount(s, msg) > 0 }

// logCount counts the lines with msg in s's watcher log (every watcher of
// the session appends to the same file, so a respawn adds its own lines).
func (r *rig) logCount(s session, msg string) int {
	data, err := os.ReadFile(filepath.Join(r.stateDir, "logs", "watcher-"+strconv.Itoa(s.pid)+".log"))
	if err != nil {
		return 0
	}
	n := 0
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		var m map[string]any
		if json.Unmarshal(line, &m) == nil && m["msg"] == msg {
			n++
		}
	}
	return n
}

// --- U-25: the token in no file, on no argv ---------------------------------

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

// assertTokenNowhere walks every file under the rig's root — the XDG
// triple, the Claude config dir, the fs store, the maps, pidfiles, logs,
// seen files, the recorded adapter argv and environments — and fails on
// any hit; the pidfile's SHA-256 is the one derived form and is asserted
// present as the control that the walk reads the right files.
func (r *rig) assertTokenNowhere(token string) {
	t := r.t
	t.Helper()
	if hits := tokenHits(t, token, r.dirs.Root); len(hits) > 0 {
		t.Errorf("the messaging token appears in files: %v", hits)
	}
	argv, err := os.ReadFile(r.argvLog)
	if err != nil || !bytes.Contains(argv, []byte("--root")) {
		t.Fatalf("the adapter wrapper recorded no argv (%v): the recorder is not live", err)
	}
	if bytes.Contains(argv, []byte(token)) {
		t.Error("the messaging token was on an adapter child's argv")
	}
	env, err := os.ReadFile(r.envLog)
	if err != nil || !bytes.Contains(env, []byte("BRIGADE_PROFILE")) {
		t.Fatalf("the adapter wrapper recorded no environment (%v): the recorder is not live", err)
	}
	if bytes.Contains(env, []byte(token)) || bytes.Contains(env, []byte("CLAUDE_CODE_MESSAGING")) {
		t.Error("the messaging token or its variable reached an adapter child's environment")
	}
	// The positive control: the walk finds a planted token.
	planted := filepath.Join(r.dirs.Root, "planted.txt")
	if err := os.WriteFile(planted, []byte("x"+token+"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := tokenHits(t, token, r.dirs.Root); len(hits) != 1 || hits[0] != planted {
		t.Errorf("the token walk did not find the planted file: %v", hits)
	}
	if err := os.Remove(planted); err != nil {
		t.Fatal(err)
	}
}

// --- small helpers ------------------------------------------------------------

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil { //nolint:gosec // G306: an executable fixture
		t.Fatal(err)
	}
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// realpath resolves symlinks (macOS /var → /private/var), as the hook does
// for plugin_bin.
func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A detached is a process a test started with startDetached: the sink
// watcher it runs directly.
type detached struct {
	cmd  *exec.Cmd
	done chan struct{} // closed when Wait returned
	err  error         // readable after done
}

// pid is the process id.
func (d *detached) pid() int { return d.cmd.Process.Pid }

// stop SIGTERMs the process and waits for it; the exit status is
// returned (-1 for a signal death).
func (d *detached) stop(t *testing.T) int {
	t.Helper()
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.done:
	case <-time.After(waitLong):
		_ = d.cmd.Process.Kill()
		<-d.done
		t.Fatalf("the detached process %d did not exit after SIGTERM", d.pid())
	}
	return d.cmd.ProcessState.ExitCode()
}

// startDetached starts the built binary as a detached process with the
// given environment and argv (the sink watcher a test runs directly), the
// way the hook's RealSpawner does: its own session, a null stdin, the two
// output files. It is waited for in a goroutine so its exit status is
// observable, and Cleanup SIGTERMs it.
func (r *rig) startDetached(env []string, stdoutPath, stderrPath string, args ...string) *detached {
	t := r.t
	t.Helper()
	outF, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: the argv is a binary this test built; no shell is involved
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), r.brigade, args...)
	cmd.Env = env
	cmd.Dir = r.dirs.Home
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = outF, errF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = outF.Close()
	_ = errF.Close()
	d := &detached{cmd: cmd, done: make(chan struct{})}
	go func() { d.err = cmd.Wait(); close(d.done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-d.done:
		case <-time.After(waitLong):
			_ = cmd.Process.Kill()
			<-d.done
			t.Errorf("the detached process %d survived cleanup; SIGKILLed", cmd.Process.Pid)
		}
	})
	return d
}
