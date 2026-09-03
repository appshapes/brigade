package testutil

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// sleeperReapTimeout bounds how long a test's cleanup waits for the
// sleeper to die after SIGTERM before escalating to SIGKILL. It is a hang
// catcher for a stuck child, never a bound the test measures against.
const sleeperReapTimeout = 10 * time.Second

// NewSleeper starts `sleep 300` as a stand-in for a live Claude Code
// process and returns its pid (plan 9.5; the fake CLAUDE_PID of the
// watcher lifecycle tests).
//
// The child is started with an explicit environment from [Env] — nothing
// of the test process's environment reaches it — and it is REAPED: a
// goroutine calls Wait the moment it starts, so that when the test (or
// the code under test) SIGTERMs the pid, kill(pid, 0) turns to ESRCH
// promptly instead of succeeding on a zombie for as long as nobody waits
// (E0-5 item 1; the same trap the procutil package exists for). A test
// that wants an UNREAPED corpse to prove the zombie path must start its
// own child and deliberately not wait — this helper is the reaped kind.
//
// Cleanup sends SIGTERM and waits for the reap, escalating to SIGKILL if
// the child ignores it, so nothing survives the test that started it.
// A sleeper the test has already terminated is fine: the signal to a
// finished process is a no-op.
func NewSleeper(tb testing.TB) int {
	tb.Helper()
	// The sleeper's lifetime is Cleanup's to end, not the test context's:
	// the context is cancelled BEFORE cleanups run, and a context-driven
	// SIGKILL there would pre-empt the SIGTERM path this helper promises.
	cmd := exec.CommandContext(context.WithoutCancel(tb.Context()), "sleep", "300")
	cmd.Env = Env(tb)
	if err := cmd.Start(); err != nil {
		tb.Fatalf("testutil.NewSleeper: start sleep: %v", err)
	}
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		_ = cmd.Wait()
	}()
	tb.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-reaped:
			return
		case <-time.After(sleeperReapTimeout):
		}
		_ = cmd.Process.Kill()
		<-reaped
	})
	return cmd.Process.Pid
}
