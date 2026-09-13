package watch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
)

// logIndex is the index of the first log line with msg, or -1.
func (fx *fixture) logIndex(msg string) int {
	for i, l := range fx.logLines() {
		if l["msg"] == msg {
			return i
		}
	}
	return -1
}

// lastLogIndex is the index of the last log line with msg, or -1.
func (fx *fixture) lastLogIndex(msg string) int {
	idx := -1
	for i, l := range fx.logLines() {
		if l["msg"] == msg {
			idx = i
		}
	}
	return idx
}

// TestExitsAfterClaudeDeath is U-21 and E2E-11's watcher half: the
// sleeper standing in for Claude Code dies; on its next poll the watcher
// stops heartbeats, closes the session through the child's stdin (the fs
// store shows closed_at), the child exits, the pidfile is removed and Run
// returns 0. The wall time from the death to the exit is logged, not
// bounded: 6.6's 5 s is a spec figure, and the exit waits on the fs
// child's close (one store write, measured up to 6.2 s under a
// whole-tree-shaped fsync storm on 2026-09-11); the deadline is a hang
// catcher.
func TestExitsAfterClaudeDeath(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	r := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	// A message before the death proves the socket path worked end to end.
	id := fx.send("before the end")
	if frames := fx.sock.WaitFrames(1, waitShort); len(frames) != 1 || !strings.Contains(frames[0], "before the end") {
		t.Fatalf("frames = %q", frames)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.ackedIDs()) == 1 })

	if err := syscall.Kill(fx.claudePID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	died := time.Now()
	code := r.wait()
	latency := time.Since(died)
	t.Logf("exit %d, %s after the sleeper's SIGTERM (poll interval %s)", code, latency, fx.deps().PollInterval)
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present: %v", err)
	}
	s := fx.session()
	if s.ClosedAt == nil {
		t.Errorf("the session was not closed in the store: %+v", s)
	}
	if !fx.logHas("closing the session", map[string]any{"reason": "claude_gone"}) {
		t.Errorf("no closing line: %v", fx.logLines())
	}
	// U-21: heartbeats stop first — no heartbeat after the closing line.
	if closing, hb := fx.logIndex("closing the session"), fx.lastLogIndex("heartbeat"); hb > closing {
		t.Errorf("a heartbeat (line %d) was sent after the close began (line %d)", hb, closing)
	}
	// The by-pid map is the hook's to delete, not the watcher's.
	if _, err := fx.store().ReadByPID(fx.claudePID); err != nil {
		t.Errorf("the watcher removed the by-pid map: %v", err)
	}
	if got := fx.ackedIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("acked = %v, want [%s]", got, id)
	}
}

// TestZombieClaudeReadsAsDead: an unreaped corpse as the Claude pid is
// still kill-alive (E0-5 defect 1) but the watcher reads the STATE and
// exits.
func TestZombieClaudeReadsAsDead(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "sleep", "0")
	cmd.Env = testutil.Env(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		info, _ := procutil.Lookup(cmd.Process.Pid)
		return info.Zombie
	})
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("kill(pid, 0) on the zombie = %v; the E0-5 trap is not present", err)
	}
	fx := newFixture(t, fixtureOptions{sink: true, pid: cmd.Process.Pid})
	fx.useFake(readyScript(t))
	fx.writeMap()
	r := fx.start(fx.deps(), fx.args()...)
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !fx.logHas("claude process is a zombie", nil) {
		t.Errorf("log: %v", fx.logLines())
	}
}

// TestMapGoneOrMismatchedExits: the by-pid map deleted, or rewritten to
// name another Brigade session, ends the watcher on the next poll with
// the session closed.
func TestMapGoneOrMismatchedExits(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(fx *fixture){
		"deleted": func(fx *fixture) {
			if err := fx.store().DeleteByPID(fx.claudePID); err != nil {
				fx.t.Fatal(err)
			}
		},
		"other session": func(fx *fixture) {
			fx.writeMapWith(func(m *sessionmap.ByPID) { m.BrigadeSessionID = "someone-else" })
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{})
			fx.useFS()
			fx.writeMap()
			r := fx.start(fx.deps())
			fx.waitLog("watch ready", nil)
			mutate(fx)
			if code := r.wait(); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if s := fx.session(); s.ClosedAt == nil {
				t.Errorf("session not closed: %+v", s)
			}
			if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
				t.Errorf("pidfile still present: %v", err)
			}
		})
	}
}

// TestSocketMissingIsNotAnExit is E0-5 defect 2: a socket path that does
// not exist only means "not injected" — the watcher keeps running,
// re-reads the registry for a new path, and the message stays unacked.
func TestSocketMissingIsNotAnExit(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	// Remove the socket file itself (the registry names nothing yet).
	if err := os.Remove(fx.sock.Path()); err != nil {
		t.Fatal(err)
	}
	r := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	id := fx.send("nowhere to go")
	fx.waitLog("injection failed; backing off", map[string]any{"message_id": id})
	// Several polls later the watcher is still alive and the message unacked.
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("heartbeat", nil) >= 3
	})
	if r.exited() {
		t.Fatalf("the watcher exited on a missing socket: %v", fx.logLines())
	}
	if got := fx.inboxIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("inbox = %v, want [%s] still pending", got, id)
	}
	if got := fx.ackedIDs(); len(got) != 0 {
		t.Errorf("acked = %v, want none", got)
	}
	if fx.sock.Accepted() != 0 {
		t.Errorf("a dial reached the listener through a removed path")
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestDetachedWatcherHandlesSIGTERM runs the watcher as a REAL detached
// process (this test binary in helper mode, Setsid) with the real
// dependencies: its pidfile names its own pid and start token, a message
// reaches the socket with the auth token, its stdout stays empty, and a
// SIGTERM ends it with exit 0, the pidfile removed and the session closed.
func TestDetachedWatcherHandlesSIGTERM(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdout := filepath.Join(t.TempDir(), "stdout")
	stderr := filepath.Join(t.TempDir(), "stderr")
	outF, err := os.OpenFile(stdout, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.OpenFile(stderr, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// The real dependencies but for the close budget, a hang catcher here
	// (runHelper says why).
	//nolint:gosec // G204: this test binary in helper mode; no shell
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), self, helperMarker, "watcher", "close-wait="+waitShort.String())
	// Under -race the helper is TSan-instrumented, and TSan sleeps
	// atexit_sleep_ms (1000 by default) before a clean exit to catch
	// at-exit races; measured here as a flat second on the SIGTERM
	// latency. The watcher's own exit is what this test times.
	cmd.Env = fx.environ("GORACE=atexit_sleep_ms=0")
	cmd.Dir = fx.dirs.Home
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = outF, errF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = outF.Close()
	_ = errF.Close()
	// reaped closes when Wait returned; waitErr is then readable.
	reaped := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-reaped:
		case <-time.After(waitShort):
			_ = cmd.Process.Kill()
			<-reaped
		}
		if info, _ := procutil.Lookup(cmd.Process.Pid); info.Exists && !info.Zombie {
			t.Errorf("the detached watcher %d survived cleanup", cmd.Process.Pid)
		}
	})

	// Its pidfile names it, alive, with the socket and the token hash.
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		v, verr := pidfile.Check(fx.pidfilePath(), procutil.Lookup)
		return verr == nil && v.Found && v.Alive && v.Entry.PID == cmd.Process.Pid
	})
	e := readPidfile(t, fx.pidfilePath())
	if e.SocketPath != fx.sock.Path() || e.TokenSHA256 != pidfile.TokenSHA256(fx.token) {
		t.Errorf("pidfile = %+v", e)
	}
	fx.waitLog("watch ready", nil)
	id := fx.send("hello detached")
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], "hello detached") {
		t.Fatalf("frames = %q", frames)
	}
	conns := fx.sock.Connections()
	if len(conns) < 1 || len(conns[0].Auth) != 1 || conns[0].Auth[0] != fx.token {
		t.Errorf("auth lines = %+v, want the token once", conns)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.ackedIDs()) == 1 })

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	sent := time.Now()
	select {
	case <-reaped:
		t.Logf("exit after SIGTERM in %s: %v", time.Since(sent), waitErr)
		if waitErr != nil {
			t.Fatalf("the watcher did not exit 0 on SIGTERM: %v", waitErr)
		}
	case <-time.After(waitLong):
		t.Fatal("the watcher did not exit after SIGTERM")
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present: %v", err)
	}
	if s := fx.session(); s.ClosedAt == nil {
		t.Errorf("session not closed: %+v", s)
	}
	if out, _ := os.ReadFile(stdout); len(out) != 0 {
		t.Errorf("the watcher wrote to stdout: %q", out)
	}
	if !fx.logHas("watcher exiting", map[string]any{"exit": 0, "reason": "signal"}) {
		t.Errorf("log: %v", fx.logLines())
	}
	if got := fx.ackedIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("acked = %v", got)
	}
}
