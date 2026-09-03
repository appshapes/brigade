package testutil

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// pollTimeout and pollInterval are hang catchers for the reap checks.
const (
	pollTimeout  = 30 * time.Second
	pollInterval = 10 * time.Millisecond
)

// gone reports that kill(pid, 0) says ESRCH: the process is not just dead
// but reaped. A zombie would still answer nil here, so this is the
// property that proves the reaper goroutine is doing its job.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestNewSleeperIsAliveWhileTheTestRuns(t *testing.T) {
	t.Parallel()
	pid := NewSleeper(t)
	if pid <= 0 {
		t.Fatalf("pid = %d, want a positive pid", pid)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(%d, 0) = %v, want nil for a live sleeper", pid, err)
	}
}

func TestNewSleeperIsReapedAfterSIGTERM(t *testing.T) {
	t.Parallel()
	pid := NewSleeper(t)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	// Without the reaper goroutine the sleeper would sit as a zombie and
	// kill(pid, 0) would keep returning nil (E0-5): this poll can only
	// succeed because something waited on the child.
	Eventually(t, pollTimeout, pollInterval, func() bool { return gone(pid) })
}

func TestNewSleeperCleanupKillsAndReapsIt(t *testing.T) {
	t.Parallel()
	var pid int
	// A parent's Cleanup runs only after every parallel subtest — and the
	// subtest's own cleanups — have finished, so it can observe what the
	// helper's cleanup did to the sleeper the subtest started.
	t.Cleanup(func() {
		if pid == 0 {
			t.Error("the subtest did not start a sleeper")
			return
		}
		Eventually(t, pollTimeout, pollInterval, func() bool { return gone(pid) })
	})
	t.Run("start", func(t *testing.T) {
		t.Parallel()
		pid = NewSleeper(t)
		if gone(pid) {
			t.Fatalf("sleeper %d gone before the subtest finished", pid)
		}
	})
}

func TestNewSleeperCleanupToleratesAnAlreadyDeadSleeper(t *testing.T) {
	t.Parallel()
	pid := NewSleeper(t)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	Eventually(t, pollTimeout, pollInterval, func() bool { return gone(pid) })
	// The helper's cleanup now signals a reaped process; the test ending
	// cleanly (no hang, no failure) is the assertion.
}
