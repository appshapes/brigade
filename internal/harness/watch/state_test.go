//go:build !windows

package watch_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/protocol"
)

// How the tests of the state goroutine wait (work item 336). The goroutine
// checks on a real ticker, and what a check decides it decides by the
// watcher's clock: when a held ask's wait has passed, when a rank's delay
// has, when a subscriber who was not reached is tried again. These tests
// measured all three in real time and did their own work, a file synced to
// disk or an adapter child, inside the same milliseconds: on a loaded
// machine the work outlasted the window and the watcher did the next thing
// first. So the clock is the test's (stateWaits), and stands still until
// the test moves it: a window is open for as long as the test needs it and
// closes when the test says so. Where a test says that nothing happened, it
// says it of checks that ran (checkCount), not of a time that passed. And
// where a test waits for the watcher to do something, the clock runs
// (acts): a watcher whose backend refused it tries again by its own clock.

// A checkCount counts the checks the watcher's state goroutine has made
// (Deps.StateChecked): one for each tick, counted when the check has ended.
type checkCount struct{ n atomic.Int64 }

func (c *checkCount) checked() { c.n.Add(1) }

// pass waits until two more checks have ended. The first of them may have
// begun before the call; the second began after the first ended, so it
// read everything the test wrote, and the clock as the test set it, before
// the call.
func (c *checkCount) pass(t *testing.T) {
	t.Helper()
	from := c.n.Load()
	await(t, "two more checks of the watcher's state goroutine", func() bool { return c.n.Load() >= from+2 })
}

// acts waits until the watcher has done what cond tells of, which what
// names for the failure, and moves the watcher's clock on by step, one
// check interval as a rule, at each look. A watcher that could not do it at once, its
// backend busy or its roster unread, tries again after some check intervals
// by its own clock: under a clock that stood still it would wait for ever,
// where in real time it tries again. (The first proof of work item 336 met
// it: an adapter's lock timed out under load, the watcher held the ask back
// for three intervals of a clock that no longer moved, and the test timed
// out.) Nothing may be held open across a call: the watcher's time runs.
func acts(t *testing.T, what string, clock *fakeClock, step time.Duration, cond func() bool) {
	t.Helper()
	await(t, what+" (the watcher's clock running)", func() bool {
		if cond() {
			return true
		}
		clock.Advance(step)
		return false
	})
}

// awaitNotice waits until the session's notice file carries text.
func awaitNotice(t *testing.T, fx *fixture, text string) {
	t.Helper()
	await(t, "the session's notice with "+strconv.Quote(text), func() bool { return strings.Contains(fx.notice(), text) })
}

// stateWaits gives the watcher a clock that stands at now until the test
// moves it and counts its checks, every interval of real time.
func stateWaits(deps *watch.Deps, now time.Time, every time.Duration) (*fakeClock, *checkCount) {
	clock, checks := &fakeClock{now: now}, &checkCount{}
	deps.Clock, deps.StateChecked, deps.StateCheckInterval = clock.Now, checks.checked, every
	return clock, checks
}

// TestStatePrunesTheFolder (card 162, state.go): a watcher whose map
// names a state folder prunes the folder's expired entries — a
// teammate's session that ended long ago, one whose machine never came
// back — on its cadence, and keeps the live and the recently ended ones;
// a watcher whose map names none touches no folder.
func TestStatePrunesTheFolder(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	root := t.TempDir()
	folder := filepath.Join(root, ".brigade", "state")
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.WorkspaceLabel = syncRepo
		m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, syncScope
		m.StateFolder = folder
	})
	now := time.Now().UTC()
	theirs := state.Store{StateDir: filepath.Join(t.TempDir(), "s"), Folder: folder}
	expired := state.New("ended-long-ago", "a", now.Add(-40*24*time.Hour))
	expired.EndedAt = now.Add(-30 * 24 * time.Hour)
	gone := state.New("machine-never-back", "b", now.Add(-40*24*time.Hour))
	gone.UpdatedAt = now.Add(-20 * 24 * time.Hour)
	live := state.New("live", "c", now)
	live.UpdatedAt = now
	recent := state.New("ended-recently", "d", now.Add(-5*24*time.Hour))
	recent.EndedAt = now.Add(-2 * 24 * time.Hour)
	for _, e := range []*state.Entry{expired, gone, live, recent} {
		if err := theirs.Save(e); err != nil {
			t.Fatal(err)
		}
	}
	deps := fx.deps()
	// No sync engine in this test: an adapter that is not there ends the
	// sync goroutine at its describe, and the state goroutine runs alone.
	deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
	deps.StatePruneDelay = 50 * time.Millisecond
	deps.StatePruneInterval = time.Hour
	fx.start(deps, fx.args()...)
	fx.waitLog("state: expired entries pruned from the folder", map[string]any{"removed": float64(2)})
	ids := map[string]bool{}
	for _, e := range state.ReadFolder(folder) {
		ids[e.SessionID] = true
	}
	if len(ids) != 2 || !ids["live"] || !ids["ended-recently"] {
		t.Fatalf("folder after the prune: %v", ids)
	}
}

// TestStateProducesTheDigest (card 162, state.go §5.4): a watcher whose
// map names a state folder with a subscriber produces their digest when
// it is due and something changed — the roster read once, the digest
// sent to the Slack gateway on the roster under a `to:` first line with
// the summary's counts, the subscriber's marker written — and produces
// nothing more while the period runs.
func TestStateProducesTheDigest(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	// The Slack gateway and the manager's own session, as the peer.
	gw := fx.register(fx.peer, &protocol.SessionRegistration{
		Harness: "gateway-slack", HarnessVersion: "0.1.0", SessionName: "slack-gateway", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
	})
	// The watched session carries the repository's name, as the hook
	// registers it (P11-5): the rank is read off the roster by it.
	self := fx.register(fx.owner, &protocol.SessionRegistration{
		Harness: "claude-code", HarnessVersion: "test", SessionName: fx.name, Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
		WorkspaceLabel: ptr(syncRepo),
	})
	fx.sessionID = self.SessionID
	root := t.TempDir()
	folder := filepath.Join(root, ".brigade", "state")
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.WorkspaceLabel = syncRepo
		m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, syncScope
		m.StateFolder = folder
	})
	now := time.Now().UTC()
	mine := state.Store{StateDir: fx.dirs.BrigadeState, Folder: folder}
	e := state.New(fx.sessionID, fx.name, now.Add(-time.Hour))
	e.Member, e.Repository = "owner@example.com", syncRepo
	e.Commits = []state.Commit{{Hash: strings.Repeat("a", 40), Subject: "162: Add the digest", Card: 162, At: now.Add(-30 * time.Minute)}}
	e.AddNote("Done: the producer", now.Add(-10*time.Minute))
	e.UpdatedAt = now.Add(-10 * time.Minute)
	if err := mine.Save(e); err != nil {
		t.Fatal(err)
	}
	d := &state.Decision{Version: state.Version, ID: "d-20261029-aaaaaa", Question: "Ship it?", Member: "owner@example.com", SessionName: fx.name, SessionID: fx.sessionID, CreatedAt: now.Add(-20 * time.Minute), UpdatedAt: now.Add(-20 * time.Minute)}
	if err := mine.SaveDecision(d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportsPath(folder), []byte(`{"version":2,"subscribers":[{"label":"peer@example.com","recipients":["@peer","peer@example.com"],"every":"1h"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := fx.deps()
	deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
	deps.StateCheckInterval = 100 * time.Millisecond
	deps.StatePruneDelay = time.Hour
	checks := &checkCount{}
	deps.StateChecked = checks.checked
	fx.start(deps, fx.args()...)
	fx.waitLog("state: digest produced", nil)
	marker, err := digest.ReadMarker(folder, "peer@example.com")
	if err != nil || marker.Label != "peer@example.com" || marker.SessionID != fx.sessionID || marker.DecisionsOpen != 1 || marker.Commits != 1 || marker.Active != 1 {
		t.Fatalf("marker %+v %v", marker, err)
	}
	got := fx.received(fx.peer, gw.SessionID)
	if len(got) != 1 {
		t.Fatalf("the gateway's inbox: %+v", got)
	}
	msg := got[0]
	if !strings.HasPrefix(msg.Body, "to: @peer\n\nBrigade digest: team ops, repository brigade, since the start, produced ") || !strings.Contains(msg.Body, "Decisions open (1):\n  Ship it?\n    asked by owner@example.com (session receiver), open for ") ||
		!strings.Contains(msg.Body, "card 162 | aaaaaaa | 162: Add the digest | owner@example.com (session receiver)") || !strings.Contains(msg.Body, "Active now (1):") {
		t.Fatalf("digest body:\n%s", msg.Body)
	}
	if msg.Summary != "Brigade digest brigade: 1 decisions open, 1 commits, 1 sessions active" || msg.Sender.SessionID != fx.sessionID {
		t.Fatalf("summary %q sender %+v", msg.Summary, msg.Sender)
	}
	awaitNotice(t, fx, "Brigade digest: sent to peer@example.com (slack)")
	// Nothing more while the period runs, whatever is written: a note newer
	// than the marker, and the checks that read it.
	later := time.Now().UTC()
	e.AddNote("Next: the report command", later)
	e.UpdatedAt = later
	if err := mine.Save(e); err != nil {
		t.Fatal(err)
	}
	checks.pass(t)
	if n := fx.logCount("state: digest produced", nil); n != 1 {
		t.Fatalf("produced %d times within the period", n)
	}
}

// TestStateRankHandsOver (§5.4): a session listed after another of the
// repository has rank 1 and waits one check interval before producing;
// a marker another session writes meanwhile stands it down with no send;
// with none, it produces after the delay — which is what happens when the
// first-ranked session has stopped.
//
// The delay is one check interval by the watcher's clock, and the clock is
// the test's (work item 336). It was 400 ms of real time, and the first
// session's marker had to land inside what the watcher's roster read, an
// adapter child, left of them: on a loaded machine it landed after this
// session had produced.
func TestStateRankHandsOver(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		markerArrives bool
	}{{"another produced meanwhile", true}, {"nobody else produced", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{sink: true})
			fx.useFS()
			// The peer's session of the same repository is listed first: this
			// one ranks second. The peer is also the manager, with no gateway.
			first := fx.register(fx.peer, &protocol.SessionRegistration{
				Harness: "claude-code", HarnessVersion: "test", SessionName: "first", Activity: protocol.ActivityBusy, Inbound: protocol.InboundAccept, WorkspaceLabel: ptr(syncRepo),
			})
			self := fx.register(fx.owner, &protocol.SessionRegistration{
				Harness: "claude-code", HarnessVersion: "test", SessionName: fx.name, Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept, WorkspaceLabel: ptr(syncRepo),
			})
			fx.sessionID = self.SessionID
			root := t.TempDir()
			folder := filepath.Join(root, ".brigade", "state")
			fx.writeMapWith(func(m *sessionmap.ByPID) {
				m.WorkspaceLabel = syncRepo
				m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, syncScope
				m.StateFolder = folder
			})
			now := time.Now().UTC()
			mine := state.Store{StateDir: fx.dirs.BrigadeState, Folder: folder}
			e := state.New(fx.sessionID, fx.name, now.Add(-time.Hour))
			e.Member, e.UpdatedAt = "owner@example.com", now
			if err := mine.Save(e); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(state.ReportsPath(folder), []byte(`{"version":2,"subscribers":[{"label":"peer@example.com","recipients":["@peer"],"every":"1h"}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			deps := fx.deps()
			deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
			deps.StatePruneDelay = time.Hour
			clock, checks := stateWaits(&deps, now, 50*time.Millisecond)
			fx.start(deps, fx.args()...)
			fx.waitLog("state: a digest is due", map[string]any{"rank": float64(1)})
			// The delay has not passed, the clock standing where it stood when
			// the digest came due: the checks that run produce nothing.
			checks.pass(t)
			if fx.logHas("state: digest produced", nil) {
				t.Fatal("produced before the rank's delay had passed")
			}
			if tc.markerArrives {
				// The first session's marker lands during this one's delay: the
				// checks after the delay find the period not elapsed and stand
				// down.
				if err := digest.WriteMarker(folder, &digest.Marker{Label: "peer@example.com", ProducedAt: now, SessionID: first.SessionID, SessionName: "first"}); err != nil {
					t.Fatal(err)
				}
				clock.Advance(deps.StateCheckInterval)
				checks.pass(t)
				if fx.logHas("state: digest produced", nil) {
					t.Fatal("produced although another session had")
				}
				if got := fx.received(fx.peer, first.SessionID); len(got) != 0 {
					t.Fatalf("the manager's session got a digest: %+v", got)
				}
				if marker, err := digest.ReadMarker(folder, "peer@example.com"); err != nil || marker.SessionID != first.SessionID {
					t.Fatalf("the first session's marker was replaced: %+v %v", marker, err)
				}
				return
			}
			// The delay passes, and the clock runs on: a send that failed is
			// tried again.
			clock.Advance(deps.StateCheckInterval)
			acts(t, "the watcher's line of a digest produced to the manager's session", clock, deps.StateCheckInterval, func() bool {
				return fx.logHas("state: digest produced", map[string]any{"how": "session"})
			})
			marker, err := digest.ReadMarker(folder, "peer@example.com")
			if err != nil || marker.SessionID != fx.sessionID {
				t.Fatalf("marker %+v %v", marker, err)
			}
			if got := fx.received(fx.peer, first.SessionID); len(got) != 1 || strings.HasPrefix(got[0].Body, "to:") {
				t.Fatalf("the manager's session inbox: %+v", got)
			}
		})
	}
}

// TestStateFansOut (card 167, §5.5): with two subscribers due, the
// producer sends each their own digest by their own precedence — the
// first through the Slack gateway, the second, who has no recipient a
// gateway carries and no session, not at all — writes the marker of the
// one it reached only, says who got it and who did not in one notice,
// and holds the unreached one back rather than trying every tick; a
// 0.43 file's one manager is a subscriber too, judged by the legacy
// marker.
//
// The unreached subscriber is held back by the watcher's clock, which is
// the test's (work item 336). The hold was 1.5 s of real time, fifteen
// checks of 100 ms, and what this test does between the digests and its
// last look, an adapter child that reads the gateway's inbox among it, took
// longer than that on a loaded machine: the watcher tried again, as it
// should, and the test counted two.
//
// A backend that refuses the first send leaves both subscribers unreached
// and held back, and is the second case: the test moves the clock past the
// hold-back, the watcher tries again and reaches the first, and what the
// test holds of the one still unreached it counts from there.
func TestStateFansOut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// refuses is whether the backend refuses the first send it is asked
		// for, which is the first subscriber's digest.
		refuses bool
	}{{"the backend takes every send", false}, {"the backend refuses the first send", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{sink: true})
			fx.useFS()
			gw := fx.register(fx.peer, &protocol.SessionRegistration{
				Harness: "gateway-slack", HarnessVersion: "0.1.0", SessionName: "slack-gateway", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
			})
			self := fx.register(fx.owner, &protocol.SessionRegistration{
				Harness: "claude-code", HarnessVersion: "test", SessionName: fx.name, Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept, WorkspaceLabel: ptr(syncRepo),
			})
			fx.sessionID = self.SessionID
			root := t.TempDir()
			folder := filepath.Join(root, ".brigade", "state")
			fx.writeMapWith(func(m *sessionmap.ByPID) {
				m.WorkspaceLabel = syncRepo
				m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, syncScope
				m.StateFolder = folder
			})
			now := time.Now().UTC()
			mine := state.Store{StateDir: fx.dirs.BrigadeState, Folder: folder}
			e := state.New(fx.sessionID, fx.name, now.Add(-time.Hour))
			e.Member, e.Repository, e.UpdatedAt = "owner@example.com", syncRepo, now
			if err := mine.Save(e); err != nil {
				t.Fatal(err)
			}
			forPeer := &state.Decision{Version: state.Version, ID: "d-20261029-bbbbbb", Question: "Peer's?", To: "peer@example.com", SessionName: fx.name, SessionID: fx.sessionID, CreatedAt: now, UpdatedAt: now}
			forNobody := &state.Decision{Version: state.Version, ID: "d-20261029-cccccc", Question: "Nobody's?", To: "Nobody", SessionName: fx.name, SessionID: fx.sessionID, CreatedAt: now, UpdatedAt: now}
			for _, d := range []*state.Decision{forPeer, forNobody} {
				if err := mine.SaveDecision(d); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(state.ReportsPath(folder), []byte(`{"version":2,"subscribers":[{"label":"peer@example.com","recipients":["@peer"],"every":"1h","only":"mine"},{"label":"Nobody","every":"1h"}]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			deps := fx.deps()
			deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
			deps.StatePruneDelay = time.Hour
			clock, checks := stateWaits(&deps, now, 100*time.Millisecond)
			productions := 1
			if tc.refuses {
				deps.Spawn, productions = refusingOnce("message send"), 2
			}
			fx.start(deps, fx.args()...)
			// The clock stands still while digests are produced, so the
			// hold-back of whoever was not reached is counted from where it
			// stands. After a production that did not reach the first
			// subscriber the test moves it well past the hold-back, fifteen
			// intervals, and it stands again while the next is produced.
			produced := func() int { return fx.logCount("state: digests produced", nil) }
			for n := 1; ; n++ {
				await(t, fmt.Sprintf("the watcher's line of the digests it produced, number %d", n), func() bool { return produced() >= n })
				if fx.logHas("state: digests produced", map[string]any{"sent": float64(1), "missed": float64(1)}) {
					break
				}
				if n == productions {
					t.Fatalf("the first subscriber was not reached in %d production(s): %v", n, fx.logLines())
				}
				clock.Advance(100 * deps.StateCheckInterval)
			}
			if n := produced(); n != productions {
				t.Fatalf("produced %d times, want %d", n, productions)
			}
			if !fx.logHas("state: digest produced", map[string]any{"label": "peer@example.com", "how": "slack", "decisions_open": float64(1)}) {
				t.Fatal("peer's digest was not produced through slack with the one decision for them")
			}
			if m, err := digest.ReadMarker(folder, "peer@example.com"); err != nil || m.Label != "peer@example.com" || m.DecisionsOpen != 1 {
				t.Fatalf("peer's marker %+v %v", m, err)
			}
			if _, err := digest.ReadMarker(folder, "Nobody"); err == nil {
				t.Fatal("Nobody's marker was written with nothing sent")
			}
			got := fx.received(fx.peer, gw.SessionID)
			if len(got) != 1 || !strings.HasPrefix(got[0].Body, "to: @peer\n\nBrigade digest: team ops, repository brigade, since the start") ||
				!strings.Contains(got[0].Body, "Decisions open for peer@example.com (1):\n  Peer's?\n    asked by session receiver, open for ") || strings.Contains(got[0].Body, "Nobody's?") {
				t.Fatalf("the gateway's inbox: %+v", got)
			}
			awaitNotice(t, fx, "Brigade digest: sent to peer@example.com (slack); not sent to Nobody (no gateway of their recipients and no session of theirs on the roster, or the send failed)")
			// Nobody is held back: six check intervals on, the checks that run
			// make no further attempt.
			clock.Advance(6 * deps.StateCheckInterval)
			checks.pass(t)
			if n := produced(); n != productions {
				t.Fatalf("produced %d times, want %d; the unreached subscriber was tried again", n, productions)
			}
		})
	}
}

// TestStateStandsDownAtTheLastLook (§5.4): between the look that finds a
// subscriber's digest due and the producing lies the read of the roster,
// an adapter call. A marker another session writes meanwhile is found by
// the last look before producing, and this session stands down for that
// subscriber: nothing is sent, and the other session's marker stays.
//
// No test failed with the last look taken out (work item 336, a reviewer's
// finding): TestStateRankHandsOver's marker lands between two checks,
// where the next check's first look finds it. Here the other session's
// marker is written from inside the roster read itself, on the watcher's
// way to its adapter, so it lands after the first look and before the last
// whatever the machine does.
func TestStateStandsDownAtTheLastLook(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	gw := fx.register(fx.peer, &protocol.SessionRegistration{
		Harness: "gateway-slack", HarnessVersion: "0.1.0", SessionName: "slack-gateway", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
	})
	// The one session of the repository: rank 0, no delay.
	self := fx.register(fx.owner, &protocol.SessionRegistration{
		Harness: "claude-code", HarnessVersion: "test", SessionName: fx.name, Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept, WorkspaceLabel: ptr(syncRepo),
	})
	fx.sessionID = self.SessionID
	root := t.TempDir()
	folder := filepath.Join(root, ".brigade", "state")
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.WorkspaceLabel = syncRepo
		m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, syncScope
		m.StateFolder = folder
	})
	now := time.Now().UTC()
	mine := state.Store{StateDir: fx.dirs.BrigadeState, Folder: folder}
	e := state.New(fx.sessionID, fx.name, now.Add(-time.Hour))
	e.Member, e.Repository, e.UpdatedAt = "owner@example.com", syncRepo, now
	if err := mine.Save(e); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportsPath(folder), []byte(`{"version":2,"subscribers":[{"label":"peer@example.com","recipients":["@peer"],"every":"1h"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := fx.deps()
	deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
	deps.StatePruneDelay = time.Hour
	_, checks := stateWaits(&deps, now, 50*time.Millisecond)
	const other = "another-machines-session"
	deps.Spawn = beforeFirst("session list", func() {
		if err := digest.WriteMarker(folder, &digest.Marker{Label: "peer@example.com", ProducedAt: now, SessionID: other, SessionName: "first"}); err != nil {
			t.Errorf("the other session's marker: %v", err)
		}
	})
	fx.start(deps, fx.args()...)
	await(t, "the watcher's line that another session produced the digest meanwhile", func() bool {
		if fx.logHas("state: digest produced", nil) {
			t.Fatal("this session produced the digest another session had produced while it read the roster")
		}
		return fx.logHas("state: another session produced the digest meanwhile", map[string]any{"producer": "first", "label": "peer@example.com"})
	})
	checks.pass(t)
	if fx.logHas("state: digest produced", nil) || fx.logHas("state: digests produced", nil) {
		t.Fatal("a digest was produced after this session had stood down")
	}
	if got := fx.received(fx.peer, gw.SessionID); len(got) != 0 {
		t.Fatalf("the gateway got a digest: %+v", got)
	}
	if m, err := digest.ReadMarker(folder, "peer@example.com"); err != nil || m.SessionID != other {
		t.Fatalf("the other session's marker was replaced: %+v %v", m, err)
	}
}
