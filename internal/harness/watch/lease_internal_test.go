package watch

import (
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/polling"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestChooseLeaseClampsIntoTheRange: the heartbeat always names a lease,
// the one wanted clamped into the adapter's advertised range, so a
// session never falls back to an adapter default (the 4.4.1 default is
// 90 s) shorter than the heartbeat cadence (card 53).
func TestChooseLeaseClampsIntoTheRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		l      protocol.Lease
		wanted int
		want   int
	}{
		{"the protocol's default range", protocol.DefaultLease(), DefaultLeaseSeconds, DefaultLeaseSeconds},
		{"the fs adapter's range", protocol.Lease{DefaultSeconds: 90, MinSeconds: 1, MaxSeconds: 600}, DefaultLeaseSeconds, DefaultLeaseSeconds},
		{"a team's longer heartbeat", protocol.DefaultLease(), 450, 450},
		{"a range that ends under it", protocol.Lease{DefaultSeconds: 60, MinSeconds: 30, MaxSeconds: 120}, DefaultLeaseSeconds, 120},
		{"a range that starts over it", protocol.Lease{DefaultSeconds: 900, MinSeconds: 600, MaxSeconds: 3600}, DefaultLeaseSeconds, 600},
		{"no range advertised", protocol.Lease{}, DefaultLeaseSeconds, DefaultLeaseSeconds},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := chooseLease(tc.l, tc.wanted)
			if got == nil || *got != tc.want {
				t.Fatalf("chooseLease(%+v, %d) = %v, want %d", tc.l, tc.wanted, got, tc.want)
			}
		})
	}
}

// TestHeartbeatTimingFollowsTheLease is the coordinator's clamp rule of
// card 53: the lease wanted is polling.LeaseBeats heartbeats, the adapter's
// range clamps it, and the effective interval is min(heartbeat, effective
// lease / LeaseBeats) — never more than a third of the lease asked for,
// whatever the range, and always positive (a ticker of zero panics). The
// deadline for an unanswered stdin heartbeat falls mid-way between two
// ticks whenever the lease is three whole intervals, so the next
// heartbeat goes out a whole interval inside the lease.
func TestHeartbeatTimingFollowsTheLease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                       string
		heartbeat                  time.Duration
		testLease                  int
		lease                      protocol.Lease
		wantLease                  int
		wantInterval, wantDeadline time.Duration
	}{
		{"the defaults", DefaultHeartbeatInterval, 0, protocol.DefaultLease(), 300, 100 * time.Second, 150 * time.Second},
		{"a team's 150 s heartbeat", 150 * time.Second, 0, protocol.DefaultLease(), 450, 150 * time.Second, 225 * time.Second},
		{"the slowest heartbeat", 200 * time.Second, 0, protocol.DefaultLease(), 600, 200 * time.Second, 300 * time.Second},
		{"an adapter whose lease ends under 300 s", DefaultHeartbeatInterval, 0, protocol.Lease{DefaultSeconds: 60, MinSeconds: 30, MaxSeconds: 120}, 120, 40 * time.Second, 60 * time.Second},
		{"an adapter whose lease ends at 2 s", DefaultHeartbeatInterval, 0, protocol.Lease{DefaultSeconds: 1, MinSeconds: 1, MaxSeconds: 2}, 2, 2 * time.Second / 3, 2*time.Second - (2*time.Second/3)*3/2},
		{"an adapter whose lease is 1 s", DefaultHeartbeatInterval, 0, protocol.Lease{DefaultSeconds: 1, MinSeconds: 1, MaxSeconds: 1}, 1, time.Second / 3, time.Second - (time.Second/3)*3/2},
		{"a test's fast heartbeat and own lease", 150 * time.Millisecond, 90, protocol.DefaultLease(), 90, 150 * time.Millisecond, 90*time.Second - 225*time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &watcher{deps: Deps{HeartbeatInterval: tc.heartbeat, LeaseSeconds: tc.testLease}}
			s := &session{lease: chooseLease(tc.lease, w.leaseWanted())}
			if *s.lease != tc.wantLease {
				t.Fatalf("lease %d s, want %d", *s.lease, tc.wantLease)
			}
			interval, deadline := w.heartbeatInterval(s), w.heartbeatAnswerWait(s)
			if interval != tc.wantInterval || deadline != tc.wantDeadline {
				t.Fatalf("interval %s and answer deadline %s, want %s and %s", interval, deadline, tc.wantInterval, tc.wantDeadline)
			}
			lease := w.leaseOf(s)
			if interval <= 0 || lease < polling.LeaseBeats*interval {
				t.Errorf("a %s lease and a %s interval: want a positive interval at most a third of the lease", lease, interval)
			}
			if lease == polling.LeaseBeats*interval && deadline%interval != interval/2 {
				t.Errorf("the %s deadline is not mid-way between two %s ticks", deadline, interval)
			}
		})
	}
}

// TestHeartbeatIntervalIsNeverZero: a lease that cannot be divided — a
// zero one no adapter range should yield — leaves the configured interval
// rather than hand the ticker zero.
func TestHeartbeatIntervalIsNeverZero(t *testing.T) {
	t.Parallel()
	zero := 0
	w := &watcher{deps: Deps{HeartbeatInterval: DefaultHeartbeatInterval}}
	if got := w.heartbeatInterval(&session{lease: &zero}); got != DefaultHeartbeatInterval {
		t.Fatalf("interval under a zero lease = %s, want the configured %s", got, DefaultHeartbeatInterval)
	}
}

// TestDepsFromMap: in production the heartbeat and the roster read come
// from the map the hook froze the team file's `polling` member into, the
// defaults when it carries none; a test's own values stand.
func TestDepsFromMap(t *testing.T) {
	t.Parallel()
	team := &sessionmap.ByPID{HeartbeatSeconds: 150, RosterSeconds: 900}
	if d := RealDeps().withDefaults().fromMap(team); d.HeartbeatInterval != 150*time.Second || d.SyncListInterval != 15*time.Minute {
		t.Fatalf("from a team's map: heartbeat %s, roster %s; want 2m30s and 15m", d.HeartbeatInterval, d.SyncListInterval)
	}
	if d := RealDeps().withDefaults().fromMap(&sessionmap.ByPID{}); d.HeartbeatInterval != DefaultHeartbeatInterval || d.SyncListInterval != DefaultSyncListInterval {
		t.Fatalf("from a map without polling: heartbeat %s, roster %s; want the defaults", d.HeartbeatInterval, d.SyncListInterval)
	}
	test := Deps{HeartbeatInterval: time.Second, SyncListInterval: time.Minute}
	if d := test.withDefaults().fromMap(team); d.HeartbeatInterval != time.Second || d.SyncListInterval != time.Minute {
		t.Fatalf("a test's knobs: heartbeat %s, roster %s; want its own 1s and 1m", d.HeartbeatInterval, d.SyncListInterval)
	}
}
