package pidfile_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
)

// Hang catchers, never performance bounds.
const (
	pollTimeout  = 30 * time.Second
	pollInterval = 10 * time.Millisecond
)

func mustLookup(tb testing.TB, pid int) procutil.Info {
	tb.Helper()
	info, err := procutil.Lookup(pid)
	if err != nil {
		tb.Fatalf("Lookup(%d): %v", pid, err)
	}
	return info
}

// liveEntry is the pidfile a watcher with pid would write for itself.
func liveEntry(tb testing.TB, pid int) pidfile.Entry {
	tb.Helper()
	info := mustLookup(tb, pid)
	if !info.Exists || info.StartToken == "" {
		tb.Fatalf("pid %d is not live: %+v", pid, info)
	}
	return entry(pid, info.StartToken)
}

func TestAliveLiveSleeper(t *testing.T) {
	t.Parallel()
	pid := testutil.NewSleeper(t)
	e := liveEntry(t, pid)
	if !pidfile.Alive(e, mustLookup(t, pid)) {
		t.Fatalf("a live, reaped sleeper %d judged dead: %+v", pid, mustLookup(t, pid))
	}
}

func TestAliveAfterSIGTERMAndReapIsDead(t *testing.T) {
	t.Parallel()
	pid := testutil.NewSleeper(t)
	e := liveEntry(t, pid)
	// Control arm: alive before the signal.
	if !pidfile.Alive(e, mustLookup(t, pid)) {
		t.Fatal("control: judged dead before SIGTERM")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool {
		return !pidfile.Alive(e, mustLookup(t, pid))
	})
	// Dead to the guard first — as a zombie, the instant it exits — and
	// gone to the kernel only once the sleeper's reaper goroutine has
	// waited on it, which on a loaded runner is a moment later (CI run
	// 33759439330 on macOS saw Exists:true Zombie:true here); so the
	// reap is waited for, not assumed.
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool {
		return !mustLookup(t, pid).Exists
	})
	if info := mustLookup(t, pid); info.Exists || info.Zombie {
		t.Fatalf("after reap Lookup = %+v, want gone", info)
	}
}

// TestForgedStartTokenIsDead is E0-5 (i)'s two-arm control: the unedited
// pidfile of a live watcher is judged alive and left alone, the SAME file
// with a forged start_token is judged dead. A guard that always replaced
// would pass the second arm without the first.
func TestForgedStartTokenIsDead(t *testing.T) {
	t.Parallel()
	pid := testutil.NewSleeper(t)
	dir := stateDir(t)
	path := pidfile.Path(dir, 4242)
	genuine := liveEntry(t, pid)
	if err := pidfile.Create(path, genuine); err != nil {
		t.Fatal(err)
	}
	// Arm 1: unedited → alive.
	v, err := pidfile.Check(path, procutil.Lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Found || !v.Alive || v.Entry != genuine {
		t.Fatalf("unedited pidfile: %+v, want Found and Alive", v)
	}
	// Arm 2: the same file with the token edited by one character → dead.
	forged := genuine
	forged.StartToken = forge(genuine.StartToken)
	if forged.StartToken == genuine.StartToken {
		t.Fatal("forge produced the same token")
	}
	if err := os.WriteFile(path, pidfile.Encode(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err = pidfile.Check(path, procutil.Lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Found || v.Alive || v.Entry != forged {
		t.Fatalf("forged pidfile: %+v, want Found and NOT Alive", v)
	}
	// And the live process itself is untouched by the judgement.
	if info := mustLookup(t, pid); !info.Exists || info.Zombie {
		t.Fatalf("the sleeper was disturbed: %+v", info)
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

// TestZombieReadsAsDead is E0-5 item 1: an exited child that nobody has
// reaped is still kill-alive — kill(pid, 0) returns nil — and the guard
// must read the STATE and call it dead anyway. The child is reaped in
// cleanup, after the assertions.
func TestZombieReadsAsDead(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "sleep", "0")
	cmd.Env = testutil.Env(t)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pid := cmd.Process.Pid
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool {
		return mustLookup(t, pid).Zombie
	})
	info := mustLookup(t, pid)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(%d, 0) = %v; E0-5 measured nil on a zombie, so this corpse is not the case under test", pid, err)
	}
	if !info.Exists || !info.Zombie || info.StartToken == "" {
		t.Fatalf("zombie Lookup = %+v, want Exists, Zombie and a token", info)
	}
	// The pidfile a watcher would have written for this pid before it
	// died: the token matches exactly, so ONLY the zombie state can make
	// the verdict "dead".
	e := entry(pid, info.StartToken)
	if pidfile.Alive(e, info) {
		t.Fatalf("a zombie with a matching token judged alive: %+v", info)
	}
	// Control: the same Info with Zombie cleared would be alive, so the
	// verdict above came from the state and nothing else.
	control := info
	control.Zombie = false
	if !pidfile.Alive(e, control) {
		t.Fatalf("control: the same Info without Zombie judged dead: %+v", control)
	}
	path := pidfile.Path(stateDir(t), 4242)
	if err := pidfile.Create(path, e); err != nil {
		t.Fatal(err)
	}
	v, err := pidfile.Check(path, procutil.Lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Found || v.Alive {
		t.Fatalf("Check on a zombie's pidfile = %+v, want Found and NOT Alive", v)
	}
}

func TestAliveTable(t *testing.T) {
	t.Parallel()
	startTok := "1788394706.376379"
	e := entry(4242, startTok)
	good := procutil.Info{PID: 4242, Exists: true, StartToken: startTok}
	if !pidfile.Alive(e, good) {
		t.Fatalf("control: %+v judged dead", good)
	}
	for _, tc := range []struct {
		name string
		info procutil.Info
	}{
		{"does not exist", procutil.Info{PID: 4242}},
		{"exists, no token", procutil.Info{PID: 4242, Exists: true}},
		{"zombie", procutil.Info{PID: 4242, Exists: true, Zombie: true, StartToken: startTok}},
		{"foreign", procutil.Info{PID: 4242, Exists: true, Foreign: true, StartToken: startTok}},
		{"other token", procutil.Info{PID: 4242, Exists: true, StartToken: forge(startTok)}},
		{"token differs in padding", procutil.Info{PID: 4242, Exists: true, StartToken: startTok[:len(startTok)-1]}},
		{"token with trailing space", procutil.Info{PID: 4242, Exists: true, StartToken: startTok + " "}},
		{"other pid", procutil.Info{PID: 4243, Exists: true, StartToken: startTok}},
		{"zombie and foreign and gone", procutil.Info{PID: 4242, Zombie: true, Foreign: true, StartToken: startTok}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if pidfile.Alive(e, tc.info) {
				t.Fatalf("%+v judged alive", tc.info)
			}
		})
	}
	// An Info built by hand without a PID skips the cross-check only.
	if !pidfile.Alive(e, procutil.Info{Exists: true, StartToken: startTok}) {
		t.Fatal("an Info with PID 0 and matching facts judged dead")
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	startTok := "1788394706.376379"
	e := entry(4242, startTok)
	path := pidfile.Path(dir, 4242)

	// Missing file: not found, no error, lookup never consulted.
	looked := false
	v, err := pidfile.Check(path, func(int) (procutil.Info, error) { looked = true; return procutil.Info{}, nil })
	if err != nil || v != (pidfile.Verdict{}) {
		t.Fatalf("Check on a missing file = %+v, %v", v, err)
	}
	if looked {
		t.Fatal("Check consulted the lookup for a missing file")
	}

	if err := pidfile.Create(path, e); err != nil {
		t.Fatal(err)
	}
	// The lookup is asked about the file's pid, and its answer decides.
	var asked int
	v, err = pidfile.Check(path, func(pid int) (procutil.Info, error) {
		asked = pid
		return procutil.Info{PID: pid, Exists: true, StartToken: startTok}, nil
	})
	if err != nil || !v.Found || !v.Alive || v.Entry != e {
		t.Fatalf("Check alive = %+v, %v", v, err)
	}
	if asked != 4242 {
		t.Fatalf("lookup asked about pid %d, want 4242", asked)
	}
	v, err = pidfile.Check(path, func(pid int) (procutil.Info, error) {
		return procutil.Info{PID: pid, Exists: true, Zombie: true, StartToken: startTok}, nil
	})
	if err != nil || !v.Found || v.Alive {
		t.Fatalf("Check zombie = %+v, %v; want Found and NOT Alive", v, err)
	}
	// A lookup error is returned with the entry, never as "alive".
	boom := errors.New("boom")
	v, err = pidfile.Check(path, func(int) (procutil.Info, error) { return procutil.Info{}, boom })
	if !errors.Is(err, boom) || !v.Found || v.Alive || v.Entry != e {
		t.Fatalf("Check with a failing lookup = %+v, %v", v, err)
	}
	// An unreadable file is an error, not a verdict.
	//nolint:gosec // G302: a group-readable pidfile is the PRECONDITION of this refusal check
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	v, err = pidfile.Check(path, func(int) (procutil.Info, error) {
		t.Fatal("lookup called for an unreadable file")
		return procutil.Info{}, nil
	})
	if err == nil || v != (pidfile.Verdict{}) {
		t.Fatalf("Check on a 0644 file = %+v, %v; want an error and no verdict", v, err)
	}
}

func TestTokenSHA256(t *testing.T) {
	t.Parallel()
	// Stable, and a known vector: SHA-256("abc").
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := pidfile.TokenSHA256("abc"); got != abc {
		t.Fatalf("TokenSHA256(abc) = %s, want %s", got, abc)
	}
	h := pidfile.TokenSHA256(fakeMessagingTok)
	if h != pidfile.TokenSHA256(fakeMessagingTok) {
		t.Fatal("TokenSHA256 is not stable")
	}
	if len(h) != 64 || strings.ToLower(h) != h || strings.Trim(h, "0123456789abcdef") != "" {
		t.Fatalf("TokenSHA256 = %q, want 64 lowercase hex digits", h)
	}
	if h == fakeMessagingTok || strings.Contains(h, fakeMessagingTok) || strings.Contains(fakeMessagingTok, h) {
		t.Fatal("the hash equals or contains the token")
	}
	if pidfile.TokenSHA256(fakeMessagingTok+"x") == h {
		t.Fatal("two different tokens hash alike")
	}
	if pidfile.TokenSHA256("") == h {
		t.Fatal("the empty token hashes like a real one")
	}
}

// TestTokenNeverReachesTheStateDir writes a pidfile for a live sleeper
// with the token hashed, exercises every write path, and lets the cleanup
// grep prove the token string itself is in no file under the state dir.
func TestTokenNeverReachesTheStateDir(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	pid := testutil.NewSleeper(t)
	path := pidfile.Path(dir, 4242)
	e := liveEntry(t, pid)
	if err := pidfile.Create(path, e); err != nil {
		t.Fatal(err)
	}
	next := e
	next.BrigadeSessionID = "bs_fedcba9876543210"
	if err := pidfile.Replace(path, e, next); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), pidfile.TokenSHA256(fakeMessagingTok)) {
		t.Fatalf("the hash is not in the file: %s", raw)
	}
	if strings.Contains(string(raw), fakeMessagingTok) {
		t.Fatalf("the token is in the file: %s", raw)
	}
}
