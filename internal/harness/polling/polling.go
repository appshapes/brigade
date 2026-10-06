// Package polling holds the cadences of the backend timers a team may
// tune in its team file's `polling` member (card 53), and the one
// relationship between them: a session's lease spans LeaseBeats
// heartbeats, so one missed heartbeat never takes it offline. The team
// file's parser, the by-pid map and the watcher all take their defaults
// and bounds from here, so the relationship is written once.
//
// The timers are timers, not work: an idle session's heartbeat and its
// folder-sync roster read were most of a team's backend requests
// (measured 2026-10-06), and a free backend plan caps them. A longer
// heartbeat means a session whose watcher died without closing it shows
// online for longer (up to one lease); it does not change how long a
// message can wait (the Supabase adapter's backup drain is a fixed 5 min).
// A longer roster read means a teammate's new session joins folder sync
// later.
package polling

import (
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// LeaseBeats is how many heartbeat intervals one lease spans (6.6).
const LeaseBeats = 3

// The `heartbeat_seconds` value: how often a session heartbeats. Its
// bounds are the protocol's default lease range divided by LeaseBeats,
// so the lease it derives is one every adapter of that range grants.
const (
	DefaultHeartbeatSeconds = 100
	MinHeartbeatSeconds     = protocol.LeaseMinSeconds / LeaseBeats
	MaxHeartbeatSeconds     = protocol.LeaseMaxSeconds / LeaseBeats
)

// The `roster_seconds` value: how often folder sync reads the team's
// sessions to find the teammates to share folders with.
const (
	DefaultRosterSeconds = 300
	MinRosterSeconds     = 15
	MaxRosterSeconds     = 3600
)

// HeartbeatInRange reports whether seconds is a usable heartbeat_seconds.
func HeartbeatInRange(seconds int) bool {
	return seconds >= MinHeartbeatSeconds && seconds <= MaxHeartbeatSeconds
}

// RosterInRange reports whether seconds is a usable roster_seconds.
func RosterInRange(seconds int) bool {
	return seconds >= MinRosterSeconds && seconds <= MaxRosterSeconds
}

// Heartbeat is the interval a heartbeat_seconds value asks for; 0, the
// value a team file or map without one carries, is the default.
func Heartbeat(seconds int) time.Duration {
	if seconds == 0 {
		seconds = DefaultHeartbeatSeconds
	}
	return time.Duration(seconds) * time.Second
}

// Roster is the interval a roster_seconds value asks for; 0 is the
// default.
func Roster(seconds int) time.Duration {
	if seconds == 0 {
		seconds = DefaultRosterSeconds
	}
	return time.Duration(seconds) * time.Second
}

// LeaseSeconds is the lease a heartbeat interval asks for: LeaseBeats
// intervals, in whole seconds rounded up (lease_seconds is an integer).
func LeaseSeconds(heartbeat time.Duration) int {
	return int((LeaseBeats*heartbeat + time.Second - 1) / time.Second)
}

// ClampLease holds a lease into an adapter's advertised range (4.4.1),
// so it is never refused; a range not advertised leaves it as it is.
func ClampLease(seconds int, l protocol.Lease) int {
	if l.MinSeconds > 0 && l.MaxSeconds >= l.MinSeconds {
		return min(max(seconds, l.MinSeconds), l.MaxSeconds)
	}
	return seconds
}

// Lease is the lease_seconds a session asks for at the team's
// heartbeat_seconds (0 for the default): LeaseBeats heartbeats, held into
// the adapter's range. The SessionStart registration and the watcher's
// heartbeats ask the same, so the lease never drops to an adapter's own
// default (the 4.4.1 default is 90 s) between the two.
func Lease(heartbeatSeconds int, l protocol.Lease) int {
	return ClampLease(LeaseSeconds(Heartbeat(heartbeatSeconds)), l)
}
