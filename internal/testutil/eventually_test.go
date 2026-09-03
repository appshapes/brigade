package testutil

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// recordingTB captures Fatalf instead of ending the goroutine, so a test
// can assert that Eventually gave up — and with which message.
type recordingTB struct {
	testing.TB
	fatal []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = append(r.fatal, strings.TrimSpace(strings.ReplaceAll(format, "%v", "")))
	_ = args
}

func TestEventuallyReturnsWhenTheConditionHoldsAtOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		tb := &recordingTB{TB: t}
		calls := 0
		start := time.Now()
		Eventually(tb, time.Second, time.Millisecond, func() bool { calls++; return true })
		if calls != 1 {
			t.Fatalf("cond called %d times, want 1", calls)
		}
		if time.Since(start) != 0 {
			t.Fatalf("a condition that holds at once must not wait; waited %v", time.Since(start))
		}
		if len(tb.fatal) != 0 {
			t.Fatalf("Fatalf called: %q", tb.fatal)
		}
	})
}

func TestEventuallyPollsUntilTheConditionHolds(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		tb := &recordingTB{TB: t}
		calls := 0
		start := time.Now()
		Eventually(tb, time.Second, 10*time.Millisecond, func() bool { calls++; return calls == 4 })
		if calls != 4 {
			t.Fatalf("cond called %d times, want 4", calls)
		}
		// Three waits of one interval each: the loop sleeps only between
		// polls, never after the one that succeeded.
		if got, want := time.Since(start), 30*time.Millisecond; got != want {
			t.Fatalf("waited %v, want exactly %v of fake time", got, want)
		}
		if len(tb.fatal) != 0 {
			t.Fatalf("Fatalf called: %q", tb.fatal)
		}
	})
}

func TestEventuallyFailsTheTestAtTheDeadline(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		tb := &recordingTB{TB: t}
		calls := 0
		start := time.Now()
		Eventually(tb, 100*time.Millisecond, 30*time.Millisecond, func() bool { calls++; return false })
		if len(tb.fatal) != 1 {
			t.Fatalf("Fatalf called %d times, want exactly once: %q", len(tb.fatal), tb.fatal)
		}
		if !strings.Contains(tb.fatal[0], "condition not met") {
			t.Fatalf("message = %q, want it to say the condition was not met", tb.fatal[0])
		}
		// Polls at 0, 30, 60, 90 and 120 ms: the poll at 120 ms is past
		// the 100 ms deadline and is the one that gives up, so the loop
		// never overshoots by more than one interval.
		if calls != 5 {
			t.Fatalf("cond called %d times, want 5", calls)
		}
		if got := time.Since(start); got != 120*time.Millisecond {
			t.Fatalf("gave up after %v of fake time, want 120ms", got)
		}
	})
}

func TestEventuallyCallsTheConditionBeforeAnyWait(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		tb := &recordingTB{TB: t}
		// A zero timeout with a condition that holds must still pass:
		// cond is consulted before the deadline is.
		Eventually(tb, 0, time.Millisecond, func() bool { return true })
		if len(tb.fatal) != 0 {
			t.Fatalf("Fatalf called with a zero timeout and a true condition: %q", tb.fatal)
		}
	})
}
