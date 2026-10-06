package polling_test

import (
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/polling"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestTheDefaultsKeepThreeBeatsInTheProtocolRange pins the 0.24.0 values
// 0.26.0 restored and the one relationship: a 30 s heartbeat asks for a 90 s lease,
// and every heartbeat in bounds asks for a lease the protocol's default
// range holds — the bounds are that range divided by the beats.
func TestTheDefaultsKeepThreeBeatsInTheProtocolRange(t *testing.T) {
	t.Parallel()
	if polling.DefaultHeartbeatSeconds != 30 || polling.DefaultRosterSeconds != 15 {
		t.Fatalf("defaults heartbeat %d s, roster %d s; want 30 and 15", polling.DefaultHeartbeatSeconds, polling.DefaultRosterSeconds)
	}
	if polling.MinHeartbeatSeconds != 10 || polling.MaxHeartbeatSeconds != 200 {
		t.Fatalf("heartbeat bounds %d..%d, want 10..200", polling.MinHeartbeatSeconds, polling.MaxHeartbeatSeconds)
	}
	if polling.MinRosterSeconds != 15 || polling.MaxRosterSeconds != 3600 {
		t.Fatalf("roster bounds %d..%d, want 15..3600", polling.MinRosterSeconds, polling.MaxRosterSeconds)
	}
	if got := polling.LeaseSeconds(polling.Heartbeat(0)); got != 90 {
		t.Fatalf("the default heartbeat asks for a %d s lease, want 90", got)
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

// TestLeaseIsThreeHeartbeatsHeldInTheRange: the lease a session asks for
// — at registration and on every heartbeat — is LeaseBeats of the team's
// heartbeats (the default for 0), held into the adapter's advertised
// range; a range not advertised leaves it.
func TestLeaseIsThreeHeartbeatsHeldInTheRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		heartbeat int
		l         protocol.Lease
		want      int
	}{
		{"the default in the protocol's range", 0, protocol.DefaultLease(), 90},
		{"a team's heartbeat", 150, protocol.DefaultLease(), 450},
		{"the slowest heartbeat", polling.MaxHeartbeatSeconds, protocol.DefaultLease(), 600},
		{"a range that ends under it", 100, protocol.Lease{DefaultSeconds: 60, MinSeconds: 30, MaxSeconds: 120}, 120},
		{"a range that starts over it", 0, protocol.Lease{DefaultSeconds: 900, MinSeconds: 600, MaxSeconds: 3600}, 600},
		{"no range advertised", 0, protocol.Lease{}, 90},
	} {
		if got := polling.Lease(tc.heartbeat, tc.l); got != tc.want {
			t.Errorf("%s: Lease(%d, %+v) = %d, want %d", tc.name, tc.heartbeat, tc.l, got, tc.want)
		}
	}
}

// TestIdleCloseBoundsDefaultAndHosts (card 61) pins the idle close: six
// hours by default, 0 to 168 hours usable with 0 meaning off, and the
// host set — the VS Code extension's `claude-vscode` and nothing else, so
// the CLI, `-p` and a value nobody has measured are never closed.
func TestIdleCloseBoundsDefaultAndHosts(t *testing.T) {
	t.Parallel()
	if polling.DefaultIdleCloseHours != 6 || polling.MinIdleCloseHours != 0 || polling.MaxIdleCloseHours != 168 {
		t.Fatalf("idle close default %d, bounds %d..%d; want 6 and 0..168",
			polling.DefaultIdleCloseHours, polling.MinIdleCloseHours, polling.MaxIdleCloseHours)
	}
	for hours, want := range map[int]bool{-1: false, 0: true, 1: true, 6: true, 168: true, 169: false} {
		if got := polling.IdleCloseInRange(hours); got != want {
			t.Errorf("IdleCloseInRange(%d) = %v, want %v", hours, got, want)
		}
	}
	if got := polling.IdleClose(nil); got != 6*time.Hour {
		t.Errorf("IdleClose(nil) = %v, want the 6 h default", got)
	}
	zero, twelve := 0, 12
	if got := polling.IdleClose(&zero); got != 0 {
		t.Errorf("IdleClose(0) = %v, want off (0)", got)
	}
	if got := polling.IdleClose(&twelve); got != 12*time.Hour {
		t.Errorf("IdleClose(12) = %v, want 12h", got)
	}
	for entrypoint, want := range map[string]bool{
		"claude-vscode": true, "cli": false, "sdk-cli": false, "sdk-ts": false, "jetbrains": false,
		"claude-desktop": false, "": false, "CLAUDE-VSCODE": false, "claude_vscode": false,
	} {
		if got := polling.IdleCloseHost(entrypoint); got != want {
			t.Errorf("IdleCloseHost(%q) = %v, want %v", entrypoint, got, want)
		}
	}
}
