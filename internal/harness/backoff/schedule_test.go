package backoff

import (
	"math/rand/v2"
	"testing"
	"time"
)

// seeded is a deterministic jitter source for exact-sequence tests.
func seeded(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed)) } //nolint:gosec // G404: a fixed seed is the point

func drawMillis(s *Schedule, n int) []int64 {
	out := make([]int64, 0, n)
	for range n {
		out = append(out, s.Next().Milliseconds())
	}
	return out
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The exact sequences for a fixed seed (U-16), recorded on 2026-09-02 from
// math/rand/v2's PCG, whose algorithm is specified and stable. A change
// here is a change to the schedule the watcher runs, and a deliberate one.
var (
	watchSeq42  = []int64{652, 1386, 3724, 5724, 10134, 15872, 24637, 18947, 28032, 26596}
	adapterSeq7 = []int64{590, 1861, 2970, 7788, 15677, 18144, 46931, 75918, 251750, 178218, 166380, 281675}
)

func TestWatchRestartSequenceForFixedSeed(t *testing.T) {
	t.Parallel()
	got := drawMillis(WatchRestart(seeded(42)), len(watchSeq42))
	if !equalInts(got, watchSeq42) {
		t.Fatalf("watch restart, seed 42: %v, want %v", got, watchSeq42)
	}
	// The cap is visible in the sequence itself: from attempt 5 (base
	// 32 s → 30 s) every delay is within [15 s, 30 s].
	for i, ms := range got[5:] {
		if ms < 15000 || ms > 30000 {
			t.Errorf("attempt %d: %d ms outside the capped range [15000, 30000]", i+5, ms)
		}
	}
}

func TestAdapterErrorSequenceForFixedSeed(t *testing.T) {
	t.Parallel()
	got := drawMillis(AdapterError(seeded(7)), len(adapterSeq7))
	if !equalInts(got, adapterSeq7) {
		t.Fatalf("adapter error, seed 7: %v, want %v", got, adapterSeq7)
	}
	for i, ms := range got[9:] {
		if ms < 150000 || ms > 300000 {
			t.Errorf("attempt %d: %d ms outside the capped range [150000, 300000]", i+9, ms)
		}
	}
}

func TestSameSeedSameSequenceDifferentSeedDiffers(t *testing.T) {
	t.Parallel()
	a := drawMillis(WatchRestart(seeded(99)), 8)
	b := drawMillis(WatchRestart(seeded(99)), 8)
	if !equalInts(a, b) {
		t.Fatalf("same seed differs: %v vs %v", a, b)
	}
	// Positive control: the seed is what determines the sequence.
	c := drawMillis(WatchRestart(seeded(100)), 8)
	if equalInts(a, c) {
		t.Fatalf("different seeds gave the same sequence: %v", a)
	}
}

func TestEveryDelayWithinHalfBaseAndBase(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 50; seed++ {
		s := New(250*time.Millisecond, 8*time.Second, seeded(seed))
		for n := range 12 {
			base := s.Base()
			wantBase := min(250*time.Millisecond<<n, 8*time.Second)
			if base != wantBase {
				t.Fatalf("seed %d attempt %d: Base %v, want %v", seed, n, base, wantBase)
			}
			d := s.Next()
			if d < base/2 || d > base {
				t.Fatalf("seed %d attempt %d: %v outside [%v, %v]", seed, n, d, base/2, base)
			}
			if s.Attempt() != n+1 {
				t.Fatalf("Attempt %d after %d draws", s.Attempt(), n+1)
			}
		}
		// After the cap is reached the base stays at the cap for good
		// (no overflow past it, no wrap-around of the shift).
		for range 100 {
			if d := s.Next(); d > 8*time.Second || d < 4*time.Second {
				t.Fatalf("seed %d: capped delay %v outside [4s, 8s]", seed, d)
			}
		}
	}
}

func TestResetReturnsToAttemptZero(t *testing.T) {
	t.Parallel()
	s := WatchRestart(seeded(3))
	for range 6 {
		s.Next()
	}
	if s.Base() != WatchRestartMax {
		t.Fatalf("after 6 attempts Base is %v, want the cap", s.Base())
	}
	s.Reset()
	if s.Attempt() != 0 || s.Base() != WatchRestartMin {
		t.Fatalf("after Reset: attempt %d base %v", s.Attempt(), s.Base())
	}
	if d := s.Next(); d < WatchRestartMin/2 || d > WatchRestartMin {
		t.Fatalf("first delay after Reset %v outside [%v, %v]", d, WatchRestartMin/2, WatchRestartMin)
	}
}

func TestNilSourceIsSeededPerSchedule(t *testing.T) {
	t.Parallel()
	a := WatchRestart(nil)
	b := WatchRestart(nil)
	if a.Min() != WatchRestartMin || a.Max() != WatchRestartMax {
		t.Fatalf("bounds %v..%v", a.Min(), a.Max())
	}
	da := drawMillis(a, 10)
	db := drawMillis(b, 10)
	for i := range da {
		base := min(int64(WatchRestartMin/time.Millisecond)<<i, int64(WatchRestartMax/time.Millisecond))
		if da[i] < base/2 || da[i] > base {
			t.Fatalf("nil source attempt %d: %d ms outside [%d, %d]", i, da[i], base/2, base)
		}
	}
	// Two independently seeded schedules do not retry in lockstep (ten
	// identical draws from a 32-byte seed would be a broken seeding).
	if equalInts(da, db) {
		t.Fatalf("two nil-source schedules produced the same sequence: %v", da)
	}
}

func TestNewClampsDegenerateBounds(t *testing.T) {
	t.Parallel()
	s := New(0, -time.Second, seeded(1))
	if s.Min() != time.Millisecond || s.Max() != time.Millisecond {
		t.Fatalf("clamped bounds %v..%v, want 1ms..1ms", s.Min(), s.Max())
	}
	if d := s.Next(); d < time.Millisecond/2 || d > time.Millisecond {
		t.Fatalf("delay %v", d)
	}
	s = New(5*time.Second, time.Second, seeded(1))
	if s.Max() != 5*time.Second {
		t.Fatalf("max below min was not raised: %v", s.Max())
	}
}

func TestBaseDoesNotConsumeADraw(t *testing.T) {
	t.Parallel()
	s := WatchRestart(seeded(11))
	_ = s.Base()
	_ = s.Base()
	got := drawMillis(s, 3)
	want := drawMillis(WatchRestart(seeded(11)), 3)
	if !equalInts(got, want) {
		t.Fatalf("Base consumed randomness: %v vs %v", got, want)
	}
}

func TestPlanConstants(t *testing.T) {
	t.Parallel()
	if WatchRestartMin != time.Second || WatchRestartMax != 30*time.Second {
		t.Fatalf("watch restart bounds %v..%v", WatchRestartMin, WatchRestartMax)
	}
	if AdapterErrorMin != time.Second || AdapterErrorMax != 5*time.Minute {
		t.Fatalf("adapter error bounds %v..%v", AdapterErrorMin, AdapterErrorMax)
	}
	if Factor != 2 {
		t.Fatalf("factor %d", Factor)
	}
}
