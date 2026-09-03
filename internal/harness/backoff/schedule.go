// Package backoff is the retry arithmetic of plan 6.6 and 6.8 item 7
// (U-16): an exponential schedule with jitter for the watcher's restart of
// its `message watch` child (1 s doubling to a 30 s cap) and for adapter
// errors (1 s doubling to a 5 min cap), and the retryability predicate —
// only `rate_limited` and `unavailable` (and a crash or signal, which
// adapterkit.Spawn already maps to `unavailable`) are ever retried;
// `invalid_input`, `unauthorized`, `loop_detected` and every other code are
// terminal. Nothing here sleeps or keeps time: a Schedule hands back
// durations and the caller (P3-5's watcher, the socket poster's retry
// loop) waits on them, which is what keeps the schedule testable under
// testing/synctest and its sequence exact for a seeded source.
package backoff

import (
	crand "crypto/rand"
	"encoding/binary"
	"math/rand/v2"
	"time"
)

// The two schedules the plan names.
const (
	// WatchRestartMin and WatchRestartMax bound the restart of the
	// `message watch` child (6.6: "exponential backoff 1 s..30 s with
	// jitter").
	WatchRestartMin = time.Second
	WatchRestartMax = 30 * time.Second
	// AdapterErrorMin and AdapterErrorMax bound retries of a failed
	// adapter call (6.8 item 7: "exponential with jitter, capped at 5 min").
	AdapterErrorMin = time.Second
	AdapterErrorMax = 5 * time.Minute
)

// Factor is the growth per attempt.
const Factor = 2

// A Schedule yields the delay before each successive attempt. Delay n is
// drawn uniformly from [base/2, base] where base = min·Factor^n capped at
// max ("equal jitter"): it never drops below half the minimum, never
// exceeds the cap, and doubles in expectation until the cap. Reset returns
// to attempt 0 after a success. A Schedule is not safe for concurrent use;
// each retry loop owns one.
type Schedule struct {
	minDelay time.Duration
	maxDelay time.Duration
	rng      *rand.Rand
	attempt  int
}

// New builds a schedule from minDelay doubling to maxDelay. rng is the
// jitter source; tests pass rand.New(rand.NewPCG(seed, seed)) for an exact
// sequence, and nil means a fresh ChaCha8 source seeded from crypto/rand
// (jitter needs no secrecy, but an unseeded source would make every
// watcher on a machine retry in lockstep). minDelay must be positive and
// maxDelay at least minDelay; New clamps rather than panics, because a
// retry loop is the wrong place to crash.
func New(minDelay, maxDelay time.Duration, rng *rand.Rand) *Schedule {
	if minDelay <= 0 {
		minDelay = time.Millisecond
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	if rng == nil {
		rng = freshSource()
	}
	return &Schedule{minDelay: minDelay, maxDelay: maxDelay, rng: rng}
}

// WatchRestart is the 6.6 schedule for restarting the watch child.
func WatchRestart(rng *rand.Rand) *Schedule {
	return New(WatchRestartMin, WatchRestartMax, rng)
}

// AdapterError is the 6.8 schedule for retrying a failed adapter call.
func AdapterError(rng *rand.Rand) *Schedule {
	return New(AdapterErrorMin, AdapterErrorMax, rng)
}

// Next returns the delay before the next attempt and advances the
// schedule. The first call after New or Reset is attempt 0: a delay in
// [min/2, min].
func (s *Schedule) Next() time.Duration {
	base := s.Base()
	s.attempt++
	half := base / 2
	// Uniform in [half, base]: half plus a fraction of the other half.
	return half + time.Duration(s.rng.Float64()*float64(base-half))
}

// Base returns the un-jittered delay of the NEXT attempt: min·Factor^n
// capped at max. Exposed so a caller can log "retrying in about N s"
// without consuming the draw.
func (s *Schedule) Base() time.Duration {
	base := s.minDelay
	for range s.attempt {
		if base >= s.maxDelay/Factor {
			return s.maxDelay
		}
		base *= Factor
	}
	return base
}

// Attempt is how many delays Next has handed out since New or Reset.
func (s *Schedule) Attempt() int { return s.attempt }

// Reset returns the schedule to attempt 0, after a success.
func (s *Schedule) Reset() { s.attempt = 0 }

// Min and Max report the bounds the schedule was built with.
func (s *Schedule) Min() time.Duration { return s.minDelay }

// Max is the cap.
func (s *Schedule) Max() time.Duration { return s.maxDelay }

// freshSource is a ChaCha8 generator seeded from the operating system.
// If the OS source fails (it does not on the supported platforms), the
// seed falls back to the clock: jitter, not a key.
func freshSource() *rand.Rand {
	var seed [32]byte
	if _, err := crand.Read(seed[:]); err != nil {
		binary.LittleEndian.PutUint64(seed[:], uint64(time.Now().UnixNano())) //nolint:gosec // G115: a clock value as a fallback jitter seed, not a secret
	}
	return rand.New(rand.NewChaCha8(seed)) //nolint:gosec // G404: retry jitter, not a secret; the seed above comes from crypto/rand
}
