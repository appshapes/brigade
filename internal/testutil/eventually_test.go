package testutil

import (
	"fmt"
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
	// said is each Fatalf's whole message, its arguments in it.
	said []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = append(r.fatal, strings.TrimSpace(strings.ReplaceAll(format, "%v", "")))
	r.said = append(r.said, fmt.Sprintf(format, args...))
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

// TestAwaitSaysWhatDidNotCome: Await gives up as Eventually does, one poll
// past its deadline and once, and its message names what was awaited and,
// when it was given the means, what was there instead.
func TestAwaitSaysWhatDidNotCome(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		seen func() string
		want string
	}{
		"what was awaited":           {nil, "waited 100ms for the watcher's `ready` line, which did not come"},
		"and what was there instead": {func() string { return "2 line(s) came" }, "waited 100ms for the watcher's `ready` line, which did not come; 2 line(s) came"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				tb := &recordingTB{TB: t}
				calls := 0
				start := time.Now()
				Await(tb, 100*time.Millisecond, 30*time.Millisecond, "the watcher's `ready` line", func() bool { calls++; return false }, tc.seen)
				if len(tb.said) != 1 || tb.said[0] != tc.want {
					t.Fatalf("Fatalf said %q, want once %q", tb.said, tc.want)
				}
				if got := time.Since(start); calls != 5 || got != 120*time.Millisecond {
					t.Fatalf("cond called %d times in %v of fake time, want 5 in 120ms", calls, got)
				}
			})
		})
	}
}

// TestAwaitReturnsWhenTheConditionHolds: at once with no wait, a zero
// timeout included, and after as many polls as the condition takes; seen
// is not asked.
func TestAwaitReturnsWhenTheConditionHolds(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		tb := &recordingTB{TB: t}
		seen := func() string { t.Error("seen was asked of a wait that did not give up"); return "" }
		start := time.Now()
		Await(tb, 0, time.Millisecond, "a condition that holds", func() bool { return true }, seen)
		if got := time.Since(start); len(tb.said) != 0 || got != 0 {
			t.Fatalf("a condition that holds at once: Fatalf said %q after %v", tb.said, got)
		}
		calls := 0
		Await(tb, time.Second, 10*time.Millisecond, "the fourth poll", func() bool { calls++; return calls == 4 }, seen)
		if got := time.Since(start); len(tb.said) != 0 || calls != 4 || got != 30*time.Millisecond {
			t.Fatalf("a condition that holds at the fourth poll: Fatalf said %q, %d polls, %v of fake time", tb.said, calls, got)
		}
	})
}
