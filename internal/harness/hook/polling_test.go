package hook

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestPollingIsFrozenIntoTheMap (card 53): SessionStart freezes the team
// file's `polling` values into the by-pid map for the watcher; a value
// the member leaves out, a team file without the member and an unusable
// member are all 0 — the defaults.
func TestPollingIsFrozenIntoTheMap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name              string
		members           string
		heartbeat, roster int
	}{
		{"both values", `,"polling":{"heartbeat_seconds":150,"roster_seconds":900}`, 150, 900},
		{"the heartbeat alone", `,"polling":{"heartbeat_seconds":60}`, 60, 0},
		{"no member", ``, 0, 0},
		{"an unusable member", `,"polling":{"heartbeat_seconds":1,"roster_seconds":900}`, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			f.writeTeamFile(tc.members)
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if m := f.mustMap(); m.HeartbeatSeconds != tc.heartbeat || m.RosterSeconds != tc.roster {
				t.Fatalf("map heartbeat %d, roster %d; want %d and %d", m.HeartbeatSeconds, m.RosterSeconds, tc.heartbeat, tc.roster)
			}
		})
	}
}

// TestPollingChangeRespawnsTheWatcher (card 53): the watcher reads its
// intervals once, when it starts, so on the continue path (/clear) a
// changed `polling` member replaces it, as a changed `sync` member does,
// and the replacement finds the new values in the map; the same member
// keeps it.
func TestPollingChangeRespawnsTheWatcher(t *testing.T) {
	t.Parallel()
	const before = `,"polling":{"heartbeat_seconds":100}`
	for _, tc := range []struct {
		name      string
		members   string
		respawn   bool
		heartbeat int
	}{
		{"unchanged", before, false, 100},
		{"a new heartbeat", `,"polling":{"heartbeat_seconds":150}`, true, 150},
		{"a roster added", `,"polling":{"heartbeat_seconds":100,"roster_seconds":600}`, true, 100},
		{"the member removed", ``, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			old := testutil.NewSleeper(t)
			f.spawner.watcherPID = old
			f.useSeam(map[string][]fakeadapter.Response{
				"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
				"session heartbeat": {okResp(heartbeatDoc())},
			})
			f.writeTeamFile(before)
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			f.writeTeamFile(tc.members)
			replacement := testutil.NewSleeper(t)
			f.spawner.watcherPID = replacement
			exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear"))
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if !tc.respawn {
				if f.spawner.count() != 1 || !alive(old) {
					t.Fatalf("the watcher was respawned (%d) or killed (%v) with polling unchanged", f.spawner.count(), alive(old))
				}
			} else {
				testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(old) })
				if f.spawner.count() != 2 || !strings.Contains(errOut, "polling changed; respawning") {
					t.Fatalf("spawns %d, stderr %q: want a respawn for the polling change", f.spawner.count(), errOut)
				}
				if seen := f.spawner.lastMap(t); seen.HeartbeatSeconds != tc.heartbeat {
					t.Fatalf("the respawned watcher read heartbeat_seconds %d, want %d", seen.HeartbeatSeconds, tc.heartbeat)
				}
			}
			if m := f.mustMap(); m.HeartbeatSeconds != tc.heartbeat {
				t.Fatalf("map heartbeat_seconds = %d, want %d", m.HeartbeatSeconds, tc.heartbeat)
			}
		})
	}
}
