// Package polling holds the cadences of the backend timers a team may
// tune in its team file's `polling` member (card 53), and the one
// relationship between them: a session's lease spans LeaseBeats
// heartbeats, so one missed heartbeat never takes it offline. The team
// file's parser, the by-pid map and the watcher all take their defaults
// and bounds from here, so the relationship is written once.
//
// The defaults are the 0.24.0 timers: a 30 s heartbeat, a 90 s lease and
// a 15 s roster read. 0.25.0 shipped 100 s and 5 min; 0.26.0 put the
// 0.24.0 values back (Rjae's ruling, 2026-10-06). A team whose backend
// plan caps requests or egress sets longer values in its team file. A
// longer heartbeat makes a session whose watcher died without closing it
// show online longer (up to one lease). A longer roster read makes a
// teammate's new session join folder sync later. Neither changes how long
// a message can wait: the Supabase adapter's backup drain is its own 30 s
// timer.
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
	DefaultHeartbeatSeconds = 30
	MinHeartbeatSeconds     = protocol.LeaseMinSeconds / LeaseBeats
	MaxHeartbeatSeconds     = protocol.LeaseMaxSeconds / LeaseBeats
)

// The `roster_seconds` value: how often folder sync reads the team's
// sessions to find the teammates to share folders with.
const (
	DefaultRosterSeconds = 15
	MinRosterSeconds     = 15
	MaxRosterSeconds     = 3600
)

// The `idle_close_hours` value (card 61, brief idle-close-vscode.md): how
// long a session may go without activity before its own watcher closes
// it, for the hosts that keep the Claude Code process after the person
// closes the conversation. The VS Code extension is such a host: closing
// a conversation leaves the `claude` process running and fires no hook,
// so without this the session stays `idle` on the roster, polling the
// backend, for as long as the window lives. The next prompt re-opens the
// session under its id (P10-7). 0 switches the close off.
const (
	DefaultIdleCloseHours = 6
	MinIdleCloseHours     = 0
	MaxIdleCloseHours     = 168
)

// idleCloseHosts are the CLAUDE_CODE_ENTRYPOINT values of the hosts the
// idle close applies to. Measured 2026-10-06: the VS Code extension sets
// `claude-vscode` on the process it spawns, and Claude Code 2.1.291 carries
// it into every hook's environment and the session registry entry. The
// CLI's `cli` and `-p`'s `sdk-cli` are absent on purpose — their process
// ends with the session — and so is every value not measured yet, so an
// unknown or missing entrypoint is never closed.
var idleCloseHosts = map[string]bool{"claude-vscode": true}

// IdleCloseHost reports whether entrypoint names a host whose sessions
// the idle close applies to.
func IdleCloseHost(entrypoint string) bool {
	return idleCloseHosts[entrypoint]
}

// IdleCloseInRange reports whether hours is a usable idle_close_hours.
func IdleCloseInRange(hours int) bool {
	return hours >= MinIdleCloseHours && hours <= MaxIdleCloseHours
}

// IdleClose is the threshold an idle_close_hours value asks for: nil, the
// value a team file or map without the member carries, is the default,
// and 0 is off, which the zero duration means.
func IdleClose(hours *int) time.Duration {
	if hours == nil {
		return DefaultIdleCloseHours * time.Hour
	}
	return time.Duration(*hours) * time.Hour
}

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
