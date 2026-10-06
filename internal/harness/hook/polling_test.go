package hook

import (
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
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
		// Card 61: the idle close is read at the watcher's start too.
		{"an idle close added", `,"polling":{"heartbeat_seconds":100,"idle_close_hours":0}`, true, 100},
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

// TestRegistrationAsksTheWatchersLease (card 53): SessionStart registers
// with the lease the watcher's heartbeats will ask for — three of the
// team's heartbeats, held into the adapter's advertised range — so the
// session never holds the adapter's own default lease (90 s on Supabase),
// shorter than a heartbeat, until the watcher's first heartbeat lands.
func TestRegistrationAsksTheWatchersLease(t *testing.T) {
	t.Parallel()
	narrow := protocol.Lease{DefaultSeconds: 60, MinSeconds: 30, MaxSeconds: 120}
	for _, tc := range []struct {
		name    string
		members string
		lease   *protocol.Lease // the adapter's advertised range; nil for the protocol's default
		want    int
	}{
		{"the default heartbeat", ``, nil, 90},
		{"a team's own heartbeat", `,"polling":{"heartbeat_seconds":150}`, nil, 450},
		{"an unusable member is the default", `,"polling":{"heartbeat_seconds":1}`, nil, 90},
		{"a range that ends under it", `,"polling":{"heartbeat_seconds":100}`, &narrow, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			if tc.lease != nil {
				var d protocol.DescribeResult
				if err := json.Unmarshal(seam.describe, &d); err != nil {
					t.Fatal(err)
				}
				d.Lease = *tc.lease
				seam.describe = mustJSON(&d)
			}
			f.writeTeamFile(tc.members)
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			var reg protocol.SessionRegistration
			if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
				t.Fatal(err)
			}
			if reg.LeaseSeconds == nil {
				t.Fatalf("registration carries no lease_seconds, want %d", tc.want)
			}
			if *reg.LeaseSeconds != tc.want {
				t.Fatalf("registration lease_seconds = %d, want %d", *reg.LeaseSeconds, tc.want)
			}
		})
	}
}

// hours is a pointer to n, the shape IdleCloseHours takes (card 61).
func hours(n int) *int { return &n }

// TestEntrypointAndIdleCloseAreFrozenIntoTheMap (card 61): SessionStart
// writes the session's entrypoint — the environment's
// CLAUDE_CODE_ENTRYPOINT, else the registry's — and the team's
// idle_close_hours into the by-pid map, which is how the watcher knows
// whether, and after how long, to close the session for want of
// activity: the VS Code extension's `claude-vscode` with the default or
// the team's hours, never the CLI, never a value the map cannot carry.
func TestEntrypointAndIdleCloseAreFrozenIntoTheMap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		env                string // CLAUDE_CODE_ENTRYPOINT; "" omits it
		registryEntrypoint string // the registry entry's `entrypoint`
		members            string
		wantEntrypoint     string
		wantHours          *int
		wantIdle           time.Duration
	}{
		{"VS Code with no member", "claude-vscode", "cli", ``, "claude-vscode", nil, 6 * time.Hour},
		{"VS Code with the team's hours", "claude-vscode", "cli", `,"polling":{"idle_close_hours":12}`, "claude-vscode", hours(12), 12 * time.Hour},
		{"VS Code switched off", "claude-vscode", "cli", `,"polling":{"idle_close_hours":0}`, "claude-vscode", hours(0), 0},
		{"the CLI never closes", "cli", "cli", `,"polling":{"idle_close_hours":12}`, "cli", hours(12), 0},
		{"no environment value falls back to the registry", "", "claude-vscode", ``, "claude-vscode", nil, 6 * time.Hour},
		{"a value the map cannot carry is blanked", "claude vscode", "cli", ``, "", nil, 0},
		{"an unusable member leaves the default", "claude-vscode", "cli", `,"polling":{"idle_close_hours":999}`, "claude-vscode", nil, 6 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			f.entrypoint = tc.env
			if tc.registryEntrypoint != "cli" {
				entry := strings.Replace(fakeregistry.Observed(f.pid, "payments-api", "busy", f.socket),
					`"entrypoint":"cli"`, `"entrypoint":"`+tc.registryEntrypoint+`"`, 1)
				f.registry = fakeregistry.New(t, map[int]string{f.pid: entry})
				f.deps.Registry = f.registry
			}
			f.writeTeamFile(tc.members)
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			m := f.mustMap()
			if m.Entrypoint != tc.wantEntrypoint {
				t.Fatalf("map entrypoint %q, want %q", m.Entrypoint, tc.wantEntrypoint)
			}
			if !sameHours(m.IdleCloseHours, tc.wantHours) {
				t.Fatalf("map idle_close_hours %v, want %v", m.IdleCloseHours, tc.wantHours)
			}
			if got := m.IdleClose(); got != tc.wantIdle {
				t.Fatalf("the map's IdleClose = %v, want %v", got, tc.wantIdle)
			}
		})
	}
}
