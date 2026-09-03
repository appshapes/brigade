package testutil

import (
	"testing"
	"time"
)

// Eventually polls cond every interval until it returns true, and fails
// the test through tb.Fatalf when timeout elapses first (plan 7.3: no
// sleeps in assertions; a poll with a deadline is the replacement).
//
// The timeout is a HANG CATCHER, not a performance bound. Choose it so a
// loaded `-race` run on a slow CI runner still passes with room to spare —
// a 5 s bound on a 300 ms operation tripped at 5.56 s on 2026-09-02 — and
// keep the interval short so a passing test does not wait out a whole
// interval it did not need.
//
// The clock is the standard one, so under testing/synctest the polling
// loop advances the bubble's fake time and a timeout costs no wall time.
// cond is called at least once, before any wait.
func Eventually(tb testing.TB, timeout, interval time.Duration, cond func() bool) {
	tb.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if !time.Now().Before(deadline) {
			tb.Fatalf("testutil.Eventually: condition not met within %v (polled every %v)", timeout, interval)
			return
		}
		time.Sleep(interval)
	}
}
