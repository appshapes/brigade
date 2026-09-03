package watch_test

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"

	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// readyScript is a fake watch that becomes ready and then waits for
// commands.
func readyScript(t *testing.T) fakeadapter.Script {
	t.Helper()
	return fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}}}
}

func readPidfile(t *testing.T, path string) pidfile.Entry {
	t.Helper()
	e, err := pidfile.Read(path)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	return e
}

// TestDuplicateWatcherExitsQuietly: a second watcher for the same session,
// socket and token finds a live pidfile with the same values and exits 0
// without touching it; the first keeps running.
func TestDuplicateWatcherExitsQuietly(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFake(readyScript(t))
	fx.writeMap()
	first := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	before := readPidfile(t, fx.pidfilePath())

	second := fx.start(fx.deps())
	if code := second.wait(); code != 0 {
		t.Fatalf("duplicate exit %d, want 0", code)
	}
	if !fx.logHas("another watcher already serves this session", nil) {
		t.Errorf("the duplicate did not log the quiet exit: %v", fx.logLines())
	}
	if after := readPidfile(t, fx.pidfilePath()); after != before {
		t.Errorf("the duplicate changed the pidfile: %+v -> %+v", before, after)
	}
	if first.exited() {
		t.Fatalf("the first watcher exited")
	}
	if code := first.stopAndWait(); code != 0 {
		t.Fatalf("first exit %d, want 0", code)
	}
}

// TestForgedStartTokenIsReplaced is E0-5 (i)'s replace arm: a pidfile
// whose start_token does not match the live process is dead and is
// replaced; the control arm (a genuine token) is the duplicate case above.
func TestForgedStartTokenIsReplaced(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFake(readyScript(t))
	fx.writeMap()
	self := os.Getpid()
	info, err := procutil.Lookup(self)
	if err != nil || info.StartToken == "" {
		t.Fatalf("lookup self: %+v %v", info, err)
	}
	forged := pidfile.Entry{
		PID: self, StartToken: forge(info.StartToken), BrigadeSessionID: fx.sessionID,
		SocketPath: fx.sock.Path(), TokenSHA256: pidfile.TokenSHA256(fx.token),
	}
	if err := pidfile.Create(fx.pidfilePath(), forged); err != nil {
		t.Fatal(err)
	}
	r := fx.start(fx.deps())
	fx.waitLog("pidfile replaced", nil)
	if !fx.logHas("replacing a dead watcher's pidfile", nil) {
		t.Errorf("log: %v", fx.logLines())
	}
	got := readPidfile(t, fx.pidfilePath())
	if got.StartToken != info.StartToken || got.PID != self {
		t.Fatalf("pidfile after replace = %+v, want the live token %q", got, info.StartToken)
	}
	fx.waitLog("watch ready", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present after exit: %v", err)
	}
}

// forge changes the last digit of a token.
func forge(token string) string {
	last := token[len(token)-1]
	repl := byte('0')
	if last == '0' {
		repl = '1'
	}
	return token[:len(token)-1] + string(repl)
}

// TestDeadHolderIsReplaced: a pidfile naming a process that has exited
// (a reaped sleeper) is replaced; a zombie holder reads as dead too.
func TestDeadHolderIsReplaced(t *testing.T) {
	t.Parallel()
	for name, mk := range map[string]func(t *testing.T) int{
		"reaped": func(t *testing.T) int {
			t.Helper()
			pid := testutil.NewSleeper(t)
			// Its start token while alive, then kill and wait for the reap.
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			testutil.Eventually(t, waitShort, pollEvery, func() bool {
				info, _ := procutil.Lookup(pid)
				return !info.Exists
			})
			return pid
		},
		"zombie": func(t *testing.T) int {
			t.Helper()
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
			return cmd.Process.Pid
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{})
			fx.useFake(readyScript(t))
			fx.writeMap()
			holder := mk(t)
			// The token the dead holder would have recorded: for the
			// zombie its real one (only the STATE makes it dead); for the
			// reaped pid any token, since the pid no longer exists.
			tok := "1.000001"
			if info, _ := procutil.Lookup(holder); info.StartToken != "" {
				tok = info.StartToken
			}
			e := pidfile.Entry{PID: holder, StartToken: tok, BrigadeSessionID: fx.sessionID,
				SocketPath: fx.sock.Path(), TokenSHA256: pidfile.TokenSHA256(fx.token)}
			if err := pidfile.Create(fx.pidfilePath(), e); err != nil {
				t.Fatal(err)
			}
			r := fx.start(fx.deps())
			fx.waitLog("pidfile replaced", nil)
			if got := readPidfile(t, fx.pidfilePath()); got.PID != os.Getpid() {
				t.Fatalf("pidfile pid = %d, want this process %d", got.PID, os.Getpid())
			}
			fx.waitLog("watch ready", nil)
			if code := r.stopAndWait(); code != 0 {
				t.Fatalf("exit %d", code)
			}
		})
	}
}

// TestLiveHolderWithDifferentValuesIsSignalledAndReplaced: a live watcher
// whose pidfile carries another token hash (D9: the token rotated) is
// SIGTERMed through the injected signal seam, given ReplaceWait to retire,
// and replaced; the recorder proves exactly one SIGTERM went to it and
// the old watcher exited 0 on its own exit path.
func TestLiveHolderWithDifferentValuesIsSignalledAndReplaced(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFake(readyScript(t))
	fx.writeMap()
	first := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	oldEntry := readPidfile(t, fx.pidfilePath())

	// The second watcher has a different token: same session, same socket.
	newToken := fx.token + "-rotated"
	var mu sync.Mutex
	var signalled []int
	deps := fx.deps()
	deps.Signal = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		signalled = append(signalled, pid)
		mu.Unlock()
		if sig != syscall.SIGTERM {
			t.Errorf("signal %v, want SIGTERM", sig)
		}
		// In-process both watchers share this pid: the recorder stops
		// the first one as the SIGTERM would.
		first.requestStop()
		return nil
	}
	oldToken := fx.token
	fx.token = newToken // start() builds the environment from the fixture; the grep checks the rotated token
	second := fx.start(deps)

	fx.waitLog("pidfile replaced", nil)
	if code := first.wait(); code != 0 {
		t.Fatalf("superseded watcher exit %d, want 0", code)
	}
	mu.Lock()
	n := len(signalled)
	mu.Unlock()
	if n != 1 || signalled[0] != oldEntry.PID {
		t.Fatalf("signalled %v, want exactly [%d]", signalled, oldEntry.PID)
	}
	got := readPidfile(t, fx.pidfilePath())
	if got.TokenSHA256 != pidfile.TokenSHA256(newToken) || got.TokenSHA256 == oldEntry.TokenSHA256 {
		t.Fatalf("pidfile token hash = %q, want the rotated token's", got.TokenSHA256)
	}
	if !fx.logHas("replacing a live watcher with different values", map[string]any{"hash_changed": true}) {
		t.Errorf("log: %v", fx.logLines())
	}
	if code := second.stopAndWait(); code != 0 {
		t.Fatalf("second exit %d", code)
	}
	if hits := tokenHits(t, oldToken, fx.dirs.Root); len(hits) > 0 {
		t.Errorf("the superseded token appears in files: %v", hits)
	}
}

// TestPidfileRemovedByContentOnly: on exit the watcher removes its own
// pidfile, but not one that by then belongs to someone else (E0-5 item 3).
func TestPidfileRemovedByContentOnly(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFake(readyScript(t))
	fx.writeMap()
	r := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	// Someone else's entry takes the file's place while we run.
	foreign := readPidfile(t, fx.pidfilePath())
	foreign.StartToken = forge(foreign.StartToken)
	if err := os.WriteFile(fx.pidfilePath(), pidfile.Encode(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := readPidfile(t, fx.pidfilePath()); got != foreign {
		t.Fatalf("the foreign pidfile was removed or changed: %+v", got)
	}
	if !fx.logHas("pidfile released", map[string]any{"removed": false}) {
		t.Errorf("log: %v", fx.logLines())
	}
	// Control: an untouched pidfile IS removed.
	fx2 := newFixture(t, fixtureOptions{})
	fx2.useFake(readyScript(t))
	fx2.writeMap()
	r2 := fx2.start(fx2.deps())
	fx2.waitLog("watch ready", nil)
	if code := r2.stopAndWait(); code != 0 {
		t.Fatalf("control exit %d", code)
	}
	if _, err := os.Stat(fx2.pidfilePath()); !os.IsNotExist(err) {
		t.Fatalf("control: pidfile still present: %v", err)
	}
	if !fx2.logHas("pidfile released", map[string]any{"removed": true}) {
		t.Errorf("control log: %v", fx2.logLines())
	}
}
