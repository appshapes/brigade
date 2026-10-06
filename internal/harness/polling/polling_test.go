package polling_test

import (
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/polling"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestTheDefaultsKeepThreeBeatsInTheProtocolRange pins card 53's values
// and the one relationship: a 100 s heartbeat asks for a 300 s lease,
// and every heartbeat in bounds asks for a lease the protocol's default
// range holds — the bounds are that range divided by the beats.
func TestTheDefaultsKeepThreeBeatsInTheProtocolRange(t *testing.T) {
	t.Parallel()
	if polling.DefaultHeartbeatSeconds != 100 || polling.DefaultRosterSeconds != 300 {
		t.Fatalf("defaults heartbeat %d s, roster %d s; want 100 and 300", polling.DefaultHeartbeatSeconds, polling.DefaultRosterSeconds)
	}
	if polling.MinHeartbeatSeconds != 10 || polling.MaxHeartbeatSeconds != 200 {
		t.Fatalf("heartbeat bounds %d..%d, want 10..200", polling.MinHeartbeatSeconds, polling.MaxHeartbeatSeconds)
	}
	if polling.MinRosterSeconds != 15 || polling.MaxRosterSeconds != 3600 {
		t.Fatalf("roster bounds %d..%d, want 15..3600", polling.MinRosterSeconds, polling.MaxRosterSeconds)
	}
	if got := polling.LeaseSeconds(polling.Heartbeat(0)); got != 300 {
		t.Fatalf("the default heartbeat asks for a %d s lease, want 300", got)
	}
	for s := polling.MinHeartbeatSeconds; s <= polling.MaxHeartbeatSeconds; s++ {
		lease := polling.LeaseSeconds(polling.Heartbeat(s))
		if lease != polling.LeaseBeats*s || lease < protocol.LeaseMinSeconds || lease > protocol.LeaseMaxSeconds {
			t.Fatalf("heartbeat %d s asks for a %d s lease, want %d inside %d..%d",
				s, lease, polling.LeaseBeats*s, protocol.LeaseMinSeconds, protocol.LeaseMaxSeconds)
		}
	}
}

// TestRanges: the bounds are inclusive, and 0 — no value — is not in
// range (a reader maps it to the default before asking).
func TestRanges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		seconds           int
		heartbeat, roster bool
	}{
		{0, false, false}, {9, false, false}, {10, true, false}, {14, true, false}, {15, true, true},
		{200, true, true}, {201, false, true}, {3600, false, true}, {3601, false, false}, {-100, false, false},
	} {
		if got := polling.HeartbeatInRange(tc.seconds); got != tc.heartbeat {
			t.Errorf("HeartbeatInRange(%d) = %v, want %v", tc.seconds, got, tc.heartbeat)
		}
		if got := polling.RosterInRange(tc.seconds); got != tc.roster {
			t.Errorf("RosterInRange(%d) = %v, want %v", tc.seconds, got, tc.roster)
		}
	}
}

// TestLeaseSecondsRoundsUp: lease_seconds is an integer, so a heartbeat
// shorter than a third of a second still asks for a whole one.
func TestLeaseSecondsRoundsUp(t *testing.T) {
	t.Parallel()
	for in, want := range map[time.Duration]int{
		100 * time.Second: 300, 10 * time.Second: 30, 150 * time.Millisecond: 1, 400 * time.Millisecond: 2,
	} {
		if got := polling.LeaseSeconds(in); got != want {
			t.Errorf("LeaseSeconds(%s) = %d, want %d", in, got, want)
		}
	}
}
