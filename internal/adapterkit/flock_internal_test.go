//go:build darwin || linux

package adapterkit

import (
	"testing"
	"time"
)

// TestLockRetryIntervalIsE06sFix pins the retry interval on the constant:
// E0-6 measured the plan's 100 ms poll rounding every contended wait up to
// a whole quantum (min 100.2 ms, median 101.1 ms against a 36 ms hold) and
// named 5–10 ms as the fix. TestFlockExclusiveBetweenTwoProcesses used to
// witness it as a median contended wait under 50 ms, a stopwatch across
// two scheduled processes; the constant is the property, so it is asserted
// here and the latencies there are only logged.
func TestLockRetryIntervalIsE06sFix(t *testing.T) {
	t.Parallel()
	if lockRetryInterval > 10*time.Millisecond {
		t.Fatalf("lockRetryInterval is %s; E0-6 measured the 100 ms poll rounding every contended wait up to a quantum and named 5–10 ms as the fix", lockRetryInterval)
	}
}
