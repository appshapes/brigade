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
// online for longer (up to one lease), and — the Supabase adapter drains
// its inbox once per lease while Realtime is joined — that a message
// whose Realtime signal was lost waits up to one lease. A longer roster
// read means a teammate's new session joins folder sync later.
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
