package hook

import (
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestWatcherEnvironFromScratch pins the watcher environment of 6.6: the
// allow-listed inherited names, the six hook-built BRIGADE_*, the log
// level, the socket and the token — each once — and nothing else, whatever
// the hook's own environment carried.
func TestWatcherEnvironFromScratch(t *testing.T) {
	t.Parallel()
	hostile := []string{
		"PATH=/usr/bin", "HOME=/home/u", "TMPDIR=/tmp/t", "LANG=C.UTF-8", "LC_ALL=C", "XDG_STATE_HOME=/home/u/.local/state",
		"CLAUDE_CONFIG_DIR=/home/u/.claude-x", "HTTPS_PROXY=http://proxy:3128", "SSL_CERT_FILE=/etc/ca.pem",
		"BRIGADE_PROFILE=evil", "BRIGADE_STATE_DIR=/evil", "BRIGADE_CONFIG_DIR=/evil", "BRIGADE_ADAPTER_COMMAND=/evil/a", "BRIGADE_TEAM_INBOUND=refuse", "BRIGADE_LOG_LEVEL=debug", "BRIGADE_CLAUDE_PID=1",
		"CLAUDE_CODE_MESSAGING_TOKEN=old-token", "CLAUDE_CODE_MESSAGING_SOCKET=/old.sock",
		"CLAUDE_PID=4064", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=x", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_PLUGIN_ROOT=/p", "CLAUDE_PLUGIN_OPTION_PROFILE=z",
		"GODEBUG=x", "NODE_OPTIONS=y", "AI_AGENT=1", "CLAUDE_EFFORT=high",
	}
	w := config.WatcherEnv{ClaudePID: 4064, Profile: "default", ConfigDir: "/home/u/.config/brigade", StateDir: "/home/u/.local/state/brigade",
		Adapter: config.Adapter{Argv: []string{"/opt/adapter", "--root", "/x"}}, TeamInbound: config.InboundAccept}
	env, err := watcherEnviron(hostile, w, "/new.sock", "new-token", "info")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		names[name]++
	}
	want := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR", "HTTPS_PROXY", "SSL_CERT_FILE",
		"BRIGADE_CLAUDE_PID", "BRIGADE_PROFILE", "BRIGADE_CONFIG_DIR", "BRIGADE_STATE_DIR", "BRIGADE_ADAPTER_COMMAND", "BRIGADE_TEAM_INBOUND", "BRIGADE_LOG_LEVEL",
		"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN"}
	sort.Strings(want)
	got := make([]string, 0, len(names))
	for n, c := range names {
		if c != 1 {
			t.Errorf("%s appears %d times", n, c)
		}
		got = append(got, n)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("names %q\nwant  %q", got, want)
	}
	for name, value := range map[string]string{
		"BRIGADE_PROFILE": "default", "BRIGADE_STATE_DIR": w.StateDir, "BRIGADE_CLAUDE_PID": "4064", "BRIGADE_LOG_LEVEL": "info",
		"BRIGADE_ADAPTER_COMMAND": `["/opt/adapter","--root","/x"]`, "BRIGADE_TEAM_INBOUND": "accept",
		"CLAUDE_CODE_MESSAGING_TOKEN": "new-token", "CLAUDE_CODE_MESSAGING_SOCKET": "/new.sock",
	} {
		if envValue(env, name) != value {
			t.Errorf("%s=%q, want %q", name, envValue(env, name), value)
		}
	}
	back, err := config.FromWatcherEnv(env)
	if err != nil || back.ClaudePID != 4064 || back.Profile != "default" || strings.Join(back.Adapter.Argv, " ") != "/opt/adapter --root /x" || back.TeamInbound != config.InboundAccept {
		t.Fatalf("round trip %+v %v", back, err)
	}
	// Without a socket or token neither variable is set at all.
	env, err = watcherEnviron(hostile, w, "", "", "info")
	if err != nil || countEnv(env, "CLAUDE_CODE_MESSAGING_SOCKET") != 0 || countEnv(env, "CLAUDE_CODE_MESSAGING_TOKEN") != 0 {
		t.Fatalf("sinkless env carries socket variables: %v %v", env, err)
	}
}

// TestSpawnRecorderTokenOnlyInEnv is U-25's hook half: the token is on no
// watcher argv, exactly once in the watcher's environment, in no adapter
// child's argv or environment, in no file under the state and config dirs
// (the fixture's cleanup grep) and in no log line even at debug.
func TestSpawnRecorderTokenOnlyInEnv(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	f.writeScript()
	var out, errOut strings.Builder
	code := Run([]string{SubSessionStart, "--log-level", "debug"}, streamsWith(f.startDoc("startup"), &out, &errOut), f.env(), f.deps)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), msgTok) || strings.Contains(out.String(), msgTok) {
		t.Fatal("the token reached a stream")
	}
	if errOut.Len() == 0 {
		t.Fatal("debug logging produced nothing; the redaction assertion is vacuous")
	}
	spec := f.spawner.last(t)
	for _, a := range spec.Args {
		if strings.Contains(a, msgTok) {
			t.Fatalf("the token is on the watcher argv: %q", spec.Args)
		}
	}
	if strings.Contains(spec.Dir, msgTok) || strings.Contains(spec.LogPath, msgTok) {
		t.Fatal("the token is in a path")
	}
	if n := countEnv(spec.Env, "CLAUDE_CODE_MESSAGING_TOKEN"); n != 1 || envValue(spec.Env, "CLAUDE_CODE_MESSAGING_TOKEN") != msgTok {
		t.Fatalf("the token must be in the watcher environment exactly once: %d", n)
	}
	for i, c := range seam.calls {
		for _, a := range c.Argv {
			if strings.Contains(a, msgTok) {
				t.Errorf("adapter child %d: token on argv", i)
			}
		}
		for _, e := range c.Env {
			if strings.Contains(e, msgTok) || strings.HasPrefix(e, "CLAUDE_CODE_MESSAGING_") {
				t.Errorf("adapter child %d: %q", i, e)
			}
		}
	}
	e, err := pidfile.Read(f.pidfilePath())
	if err != nil || e.TokenSHA256 != pidfile.TokenSHA256(msgTok) {
		t.Fatalf("pidfile hash %+v %v", e, err)
	}
	// Positive control for the grep in the fixture's cleanup: a file that
	// DOES carry the token is caught by the same walk.
	spy := &fixture{t: t, dirs: testutil.NewDirs(t.TempDir())}
	if err := os.MkdirAll(spy.dirs.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spy.dirs.Root, "leak"), []byte("x"+msgTok+"y"), 0o600); err != nil {
		t.Fatal(err)
	}
	found := false
	root, _ := os.OpenRoot(spy.dirs.Root)
	defer func() { _ = root.Close() }()
	if data, err := root.ReadFile("leak"); err == nil && strings.Contains(string(data), msgTok) {
		found = true
	}
	if !found {
		t.Fatal("the control leak was not found by the same read")
	}
}

func streamsWith(stdin string, out, errOut *strings.Builder) cli.Streams {
	return cli.Streams{In: strings.NewReader(stdin), Out: out, Err: errOut}
}

// TestSpawnFailureIsLoggedNotFatal: a watcher that cannot be started does
// not fail the hook; the session is registered, the maps are written and
// the line is printed.
func TestSpawnFailureIsLoggedNotFatal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.err = errors.New("fork failed")
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 || !strings.Contains(out, "(brigade-sess-1)") || !strings.Contains(errOut, "watcher not started") || !f.mapExists() {
		t.Fatalf("exit %d out %q err %q map %v", exit, out, errOut, f.mapExists())
	}
}

// TestPidfileWaitTimesOut: a watcher that never writes its pidfile is
// logged after the bounded wait and the hook still succeeds.
func TestPidfileWaitTimesOut(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.deps.PidfileWait = 50 * time.Millisecond
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 || !strings.Contains(out, "(brigade-sess-1)") || !strings.Contains(errOut, "pidfile did not appear") {
		t.Fatalf("exit %d out %q err %q", exit, out, errOut)
	}
}

// TestRealSpawnerDetachesWatcher drives RealSpawner for real: the hook
// starts this test binary as `watch --sink <file>` (the stand-in watcher
// of TestMain), waits for its pidfile, and the stub's record proves the
// 6.6 shape — its own session and process group, a null stdin, both
// output streams on the 0600 log file, HOME as cwd, the exact from-scratch
// environment with the token present — then session-end stops it and the
// prompt hook respawns a dead one. Every stub is SIGTERMed and reaped in
// cleanup.
func TestRealSpawnerDetachesWatcher(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.deps.Spawner = RealSpawner{}
	f.deps.Sink = filepath.Join(f.dirs.Root, "sink.ndjson")
	// The stub is a real process: the wait for its pidfile is a hang
	// catcher here (production waits DefaultPidfileWait).
	f.deps.PidfileWait = pollTimeout
	stubs := &stubTracker{}
	t.Cleanup(func() { stubs.reap(t, f) })
	seam := f.useSeam(map[string][]fakeadapter.Response{
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"session close":    {okResp(closeDoc())},
	})
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 || !strings.Contains(out, "(brigade-sess-1)") {
		t.Fatalf("exit %d out %q err %q", exit, out, errOut)
	}
	if strings.Contains(errOut, "pidfile did not appear") {
		t.Fatalf("the hook did not see the watcher's pidfile: %s", errOut)
	}
	v, err := pidfile.Check(f.pidfilePath(), procutil.Lookup)
	if err != nil || !v.Found || !v.Alive || v.Entry.BrigadeSessionID != "brigade-sess-1" || v.Entry.TokenSHA256 != pidfile.TokenSHA256(msgTok) {
		t.Fatalf("pidfile %+v %v", v, err)
	}
	first := v.Entry.PID
	stubs.add(first)
	rec := readStubRecords(t, f.deps.Sink, 1)[0]
	if rec.PID != first || strings.Join(rec.Args, " ") != "--sink "+f.deps.Sink {
		t.Fatalf("record %+v", rec)
	}
	if !rec.OwnSession || !rec.OwnGroup || !rec.StdinNull || !rec.StdoutIsLog || !rec.TokenPresent || rec.Socket != f.socket {
		t.Fatalf("detach facts %+v", rec)
	}
	if !samePath(rec.Cwd, f.dirs.Home) {
		t.Fatalf("cwd %q, want HOME %q", rec.Cwd, f.dirs.Home)
	}
	wantKeys := []string{"PATH", "HOME", "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"BRIGADE_CLAUDE_PID", "BRIGADE_PROFILE", "BRIGADE_CONFIG_DIR", "BRIGADE_STATE_DIR", "BRIGADE_ADAPTER_COMMAND", "BRIGADE_TEAM_INBOUND", "BRIGADE_LOG_LEVEL",
		"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN"}
	sort.Strings(wantKeys)
	if strings.Join(rec.EnvKeys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("watcher env names %q\nwant %q", rec.EnvKeys, wantKeys)
	}
	logPath := watcherLogPath(f.stateDir, f.pid)
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("watcher log %v %v", fi, err)
	}
	// The prompt hook leaves a live watcher alone…
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
		t.Fatalf("prompt: exit %d out %q", exit, out)
	}
	if len(readStubRecords(t, f.deps.Sink, 1)) != 1 {
		t.Fatal("the prompt hook respawned a live watcher")
	}
	// …and respawns a dead one (a new pid in the pidfile).
	stubs.kill(t, first)
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || !strings.Contains(errOut, "respawning") {
		t.Fatalf("prompt after death: exit %d err %q", exit, errOut)
	}
	v, err = pidfile.Check(f.pidfilePath(), procutil.Lookup)
	if err != nil || !v.Alive || v.Entry.PID == first {
		t.Fatalf("pidfile after respawn %+v %v", v, err)
	}
	second := v.Entry.PID
	stubs.add(second)
	if len(readStubRecords(t, f.deps.Sink, 2)) != 2 {
		t.Fatal("no second record")
	}
	// session-end stops it: the watcher exits and its pidfile is gone.
	if exit, _, _ := f.run(SubSessionEnd, f.endDoc("other")); exit != 0 {
		t.Fatal(exit)
	}
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(second) })
	if _, err := os.Lstat(f.pidfilePath()); err == nil {
		t.Fatal("the pidfile survived session-end")
	}
	if len(seam.callsFor("session close")) != 1 {
		t.Fatal("no close")
	}
}

// stubTracker remembers every stub watcher pid a test started, so cleanup
// can SIGTERM and reap each one (they are this process's children).
type stubTracker struct{ pids []int }

func (s *stubTracker) add(pid int) { s.pids = append(s.pids, pid) }

func (s *stubTracker) kill(t *testing.T, pid int) {
	t.Helper()
	_ = syscall.Kill(pid, syscall.SIGTERM)
	reapChild(t, pid)
}

func (s *stubTracker) reap(t *testing.T, f *fixture) {
	t.Helper()
	if v, err := pidfile.Check(f.pidfilePath(), procutil.Lookup); err == nil && v.Found {
		s.add(v.Entry.PID)
	}
	for _, pid := range s.pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		reapChild(t, pid)
	}
	for _, pid := range s.pids {
		if info, err := procutil.Lookup(pid); err == nil && info.Exists && !info.Zombie {
			t.Errorf("stub watcher %d survived cleanup", pid)
		}
	}
}

// reapChild waits for a child pid (SIGKILL after the hang catcher).
func reapChild(t *testing.T, pid int) {
	t.Helper()
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Wait()
	}()
	select {
	case <-done:
	case <-time.After(pollTimeout):
		_ = p.Kill()
		<-done
	}
}

// readStubRecords waits until the sink holds at least n records.
func readStubRecords(t *testing.T, sink string, n int) []stubRecord {
	t.Helper()
	var out []stubRecord
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool {
		data, err := os.ReadFile(sink)
		if err != nil {
			return false
		}
		out = out[:0]
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var rec stubRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				return false
			}
			out = append(out, rec)
		}
		return len(out) >= n
	})
	return out
}

// TestWatcherLogPath pins the 3.2 location.
func TestWatcherLogPath(t *testing.T) {
	t.Parallel()
	if got := watcherLogPath("/s", 42); got != "/s/logs/watcher-42.log" {
		t.Fatal(got)
	}
	if got := noticePath("/s", 42); got != "/s/state/42.notice" {
		t.Fatal(got)
	}
	if got := strconv.Itoa(7); got != "7" {
		t.Fatal(got)
	}
}
