//go:build darwin || linux

package procutil_test

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
)

// Hang catchers, never performance bounds.
const (
	pollTimeout  = 30 * time.Second
	pollInterval = 10 * time.Millisecond
)

// mustLookup is Lookup with the error folded into the test.
func mustLookup(tb testing.TB, pid int) procutil.Info {
	tb.Helper()
	info, err := procutil.Lookup(pid)
	if err != nil {
		tb.Fatalf("Lookup(%d): %v", pid, err)
	}
	return info
}

// startUnreaped starts a child that exits at once and deliberately does
// NOT wait on it, so the pid stays a zombie until cleanup reaps it. This
// is the E0-5 corpse: kill(pid, 0) succeeds on it for as long as it is
// unreaped.
func startUnreaped(tb testing.TB) int {
	tb.Helper()
	cmd := exec.CommandContext(tb.Context(), "sleep", "0")
	cmd.Env = testutil.Env(tb)
	if err := cmd.Start(); err != nil {
		tb.Fatalf("start: %v", err)
	}
	tb.Cleanup(func() { _ = cmd.Wait() })
	return cmd.Process.Pid
}

func TestLookupSelfIsAlive(t *testing.T) {
	t.Parallel()
	info := mustLookup(t, os.Getpid())
	if info.PID != os.Getpid() {
		t.Fatalf("PID = %d, want %d", info.PID, os.Getpid())
	}
	if !info.Exists || info.Zombie || info.Foreign {
		t.Fatalf("self = %+v, want Exists and neither Zombie nor Foreign", info)
	}
	if info.StartToken == "" {
		t.Fatalf("self = %+v, want a start token", info)
	}
}

func TestLookupRejectsNonPositivePIDs(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{0, -1, -12345} {
		info, err := procutil.Lookup(pid)
		if !errors.Is(err, procutil.ErrInvalidPID) {
			t.Errorf("Lookup(%d) err = %v, want ErrInvalidPID", pid, err)
		}
		if info != (procutil.Info{}) {
			t.Errorf("Lookup(%d) info = %+v, want the zero Info", pid, info)
		}
	}
}

func TestLookupLiveSleeper(t *testing.T) {
	t.Parallel()
	pid := testutil.NewSleeper(t)
	info := mustLookup(t, pid)
	if !info.Exists || info.Zombie || info.Foreign || info.StartToken == "" {
		t.Fatalf("live sleeper = %+v, want Exists with a token, not Zombie, not Foreign", info)
	}
}

func TestLookupReapedSleeperIsGone(t *testing.T) {
	t.Parallel()
	pid := testutil.NewSleeper(t)
	// Positive control first: it is alive before the signal.
	if info := mustLookup(t, pid); !info.Exists {
		t.Fatalf("sleeper %d not alive before SIGTERM: %+v", pid, info)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool {
		return !mustLookup(t, pid).Exists
	})
	info := mustLookup(t, pid)
	if info != (procutil.Info{PID: pid}) {
		t.Fatalf("reaped sleeper = %+v, want only PID set", info)
	}
}

func TestLookupZombieExistsAndReadsAsZombie(t *testing.T) {
	t.Parallel()
	pid := startUnreaped(t)
	// The corpse is a zombie once it has exited; `sleep 0` exits at once,
	// but "at once" is not "before this line", so poll for the state.
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool {
		return mustLookup(t, pid).Zombie
	})
	info := mustLookup(t, pid)
	// This is the E0-5 trap made visible: kill(pid, 0) still says yes …
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(%d, 0) = %v on an unreaped child; E0-5 measured nil (the zombie is kill-alive)", pid, err)
	}
	// … and Lookup says Exists, so Exists alone must never be read as alive.
	if !info.Exists {
		t.Fatalf("zombie = %+v, want Exists (the kernel still knows the pid)", info)
	}
	if !info.Zombie {
		t.Fatalf("zombie = %+v, want Zombie", info)
	}
	if info.Foreign {
		t.Fatalf("zombie = %+v, want not Foreign (it is our own child)", info)
	}
	if info.StartToken == "" {
		t.Fatalf("zombie = %+v, want the start token to survive exit (E0-5: `ps -o lstart=` still printed it)", info)
	}
}

func TestLookupZombieIsGoneOnceReaped(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "sleep", "0")
	cmd.Env = testutil.Env(t)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool {
		return mustLookup(t, pid).Zombie
	})
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if info := mustLookup(t, pid); info.Exists {
		t.Fatalf("reaped zombie = %+v, want Exists false", info)
	}
}

func TestLookupForeignProcess(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root can signal every process; there is no foreign pid to look at")
	}
	// pid 1 (launchd / init) belongs to root on every supported OS.
	info := mustLookup(t, 1)
	if !info.Exists || !info.Foreign {
		t.Fatalf("pid 1 = %+v, want Exists and Foreign (kill returns EPERM)", info)
	}
	if info.Zombie {
		t.Fatalf("pid 1 = %+v, want not Zombie", info)
	}
	if info.StartToken == "" {
		t.Fatalf("pid 1 = %+v, want a start token (the state table is readable for foreign pids)", info)
	}
}

func TestStartTokenIsStableAcrossLookups(t *testing.T) {
	t.Parallel()
	pid := testutil.NewSleeper(t)
	first := mustLookup(t, pid).StartToken
	for range 5 {
		if again := mustLookup(t, pid).StartToken; again != first {
			t.Fatalf("token changed between lookups: %q then %q", first, again)
		}
	}
	// A token identifies an incarnation of a pid, compared together with
	// the pid, so two processes MAY share one: on linux it is the start
	// time in 10 ms clock ticks, and CI run 33906610649 started this test
	// binary and its sleeper inside the same tick (both "23817"). What
	// must hold is that a process started later gets a later token: the
	// second sleeper starts at least two ticks after the first — on
	// darwin the token is a microsecond, so the gap is generous there.
	time.Sleep(25 * time.Millisecond)
	if later := mustLookup(t, testutil.NewSleeper(t)).StartToken; later == first {
		t.Fatalf("two sleepers started 25 ms apart share a token %q; the token does not tell incarnations apart", first)
	}
}
