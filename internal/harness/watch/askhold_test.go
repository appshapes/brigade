//go:build !windows

package watch_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// A holdCase is one watched session with a state folder, its entry and
// one decision held for its own user (card 236). None of it is the
// inbound `hold` policy of hold_test.go.
//
// The watcher's clock is the case's own and stands still until the test
// moves it (work items 333 and 336, state_test.go): a held ask's wait ends
// when the test says it has, with waitOver, and not while the test is still
// writing what the wait was meant to find. What a test says did not happen,
// it says of checks that ran (checks.pass). And while a test waits for the
// watcher to send, the clock runs (sent), so that an ask it could not send
// at once is tried again.
type holdCase struct {
	fx     *fixture
	folder string
	store  state.Store
	id     string
	// giveUp is the watcher's HoldGiveUp; zero is long enough that no test
	// meets it by accident.
	giveUp time.Duration
	clock  *fakeClock
	checks *checkCount
	// spawn, when set, stands between the watcher and its adapter for the
	// calls it makes one at a time: a test refuses one of them.
	spawn adapterclient.SpawnFunc
}

// holdWait is how long askNow's ask is held, by the watcher's clock: it is
// not waited for. holdEvery is the watcher's check interval.
const (
	holdWait  = time.Minute
	holdEvery = 100 * time.Millisecond
)

// newHoldCase lays the session out, its entry last prompted at
// lastPrompt. No workspace label, so this watcher ranks for no digest and
// a gateway's inbox holds asks alone.
func newHoldCase(t *testing.T, lastPrompt time.Time) *holdCase {
	t.Helper()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	root := t.TempDir()
	folder := filepath.Join(root, ".brigade", "state")
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, syncScope
		m.StateFolder = folder
	})
	c := &holdCase{fx: fx, folder: folder, store: state.Store{StateDir: fx.dirs.BrigadeState, Folder: folder}, id: "d-20261110-abcdef"}
	c.clock, c.checks = &fakeClock{now: time.Now().UTC()}, &checkCount{}
	e := state.New(fx.sessionID, fx.name, lastPrompt.Add(-time.Hour))
	e.Member, e.Repository, e.LastPromptAt, e.UpdatedAt = "owner@example.com", syncRepo, lastPrompt, lastPrompt
	if err := c.store.Save(e); err != nil {
		t.Fatal(err)
	}
	return c
}

// ask records the decision as `brigade decision` would have: asked at
// asked for the session's own member, held until until.
func (c *holdCase) ask(t *testing.T, asked, until time.Time) {
	t.Helper()
	c.askAs(t, c.id, asked, until)
}

// askAs is ask for a decision id of the caller's.
func (c *holdCase) askAs(t *testing.T, id string, asked, until time.Time) {
	t.Helper()
	d := &state.Decision{
		Version: state.Version, ID: id, Question: "Ship 0.48.0 now, or wait?", To: "owner@example.com", Options: []string{"ship now", "wait"},
		Member: "owner@example.com", SessionName: c.fx.name, SessionID: c.fx.sessionID, Repository: syncRepo, CreatedAt: asked, UpdatedAt: asked,
	}
	d.Hold(until)
	if err := c.store.SaveDecision(d); err != nil {
		t.Fatal(err)
	}
}

// askNow starts the watcher and then asks: the order a session has, where
// the watcher runs before any command does. It returns when the wait ends,
// which is when the test calls waitOver.
func (c *holdCase) askNow(t *testing.T) time.Time {
	t.Helper()
	c.start(t)
	c.fx.waitLog("watcher starting", nil)
	// Past the watcher's own start.
	c.clock.Advance(time.Second)
	asked := c.clock.Now()
	until := asked.Add(holdWait)
	c.ask(t, asked, until)
	return until
}

// waitOver moves the watcher's clock to the end of askNow's wait.
func (c *holdCase) waitOver() { c.clock.Advance(holdWait) }

// sent waits until the watcher's log has n lines of an ask sent with the
// attributes, the watcher's clock running meanwhile (acts).
func (c *holdCase) sent(t *testing.T, n int, attrs map[string]any) {
	t.Helper()
	what := fmt.Sprintf("%d held ask(s) sent, by the watcher's log", n)
	if len(attrs) > 0 {
		what += fmt.Sprintf(" with %v", attrs)
	}
	acts(t, c.fx, what, c.clock, holdEvery, func() bool { return c.fx.logCount("decision: held ask sent", attrs) == n })
}

// ended waits until the watcher's log says a hold ended with nothing sent,
// in the way how names.
func (c *holdCase) ended(t *testing.T, how string) {
	t.Helper()
	testutil.Await(t, waitShort, pollEvery, "the watcher's line that a hold ended with nothing sent, how="+how, func() bool {
		return c.fx.logHas("decision: hold ended with nothing sent", map[string]any{"how": how})
	}, nil)
}

// subscribe writes the owner's subscription with the recipients.
func (c *holdCase) subscribe(t *testing.T, recipients string) {
	t.Helper()
	if err := os.WriteFile(state.ReportsPath(c.folder), []byte(`{"version":2,"subscribers":[{"label":"owner@example.com","recipients":[`+recipients+`],"every":"24h"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// start runs the watcher with a short check interval, the case's clock
// and no sync engine.
func (c *holdCase) start(t *testing.T) {
	t.Helper()
	deps := c.fx.deps()
	deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
	deps.StateCheckInterval = holdEvery
	deps.StatePruneDelay = time.Hour
	deps.HoldGiveUp = time.Hour
	if c.giveUp > 0 {
		deps.HoldGiveUp = c.giveUp
	}
	deps.Clock, deps.StateChecked, deps.Spawn = c.clock.Now, c.checks.checked, c.spawn
	c.fx.start(deps, c.fx.args()...)
}

// gateway registers a session with the Slack gateway's harness, as the
// peer, and answers its id.
func (c *holdCase) gateway(t *testing.T) string {
	t.Helper()
	return c.fx.register(c.fx.peer, &protocol.SessionRegistration{
		Harness: "gateway-slack", HarnessVersion: "0.1.0", SessionName: "slack-gateway", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
	}).SessionID
}

// second registers another session of the asking member, and answers its
// id.
func (c *holdCase) second(t *testing.T) string {
	t.Helper()
	return c.fx.register(c.fx.owner, &protocol.SessionRegistration{
		Harness: "codex", HarnessVersion: "test", SessionName: "second", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
	}).SessionID
}

// decision loads the canonical copy.
func (c *holdCase) decision(t *testing.T) *state.Decision {
	t.Helper()
	return c.decisionOf(t, c.id)
}

// decisionOf loads the canonical copy of the id.
func (c *holdCase) decisionOf(t *testing.T, id string) *state.Decision {
	t.Helper()
	d, err := c.store.LoadDecision(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// inbox is what the client receives for a session.
func (c *holdCase) inbox(t *testing.T, client *adapterclient.Client, sessionID string) []protocol.MessageEnvelope {
	t.Helper()
	return c.fx.received(client, sessionID)
}

// TestHeldAskIsSentAfterTheWait (card 236, askhold.go): the asking session's
// watcher sends an ask held for its own user once the wait has passed
// with no prompt in the conversation — through the first gateway of the
// member's recipients on the roster, under a `to:` first line — records
// how it went on both copies and tells the session in a notice; never
// before the wait has passed.
func TestHeldAskIsSentAfterTheWait(t *testing.T) {
	t.Parallel()
	c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
	gw := c.gateway(t)
	c.subscribe(t, `"@owner","owner@example.com"`)
	until := c.askNow(t)
	// Never before the wait has passed: the checks that run meanwhile send
	// nothing.
	c.checks.pass(t)
	if d := c.decision(t); !d.Held() || c.fx.logHas("decision: held ask sent", nil) {
		t.Fatalf("the ask went out before its wait had passed: %+v", *d)
	}
	c.waitOver()
	c.sent(t, 1, map[string]any{"how": "slack"})
	d := c.decision(t)
	if d.Held() || d.Sent != state.SentSlack || d.SentTo != "@owner" || d.HoldEnded != "" || !d.Open() || d.UpdatedAt.Before(until) {
		t.Fatalf("decision after the release: %+v (the wait ended %s)", *d, until)
	}
	if theirs, err := state.ReadDecision(c.folder, c.id); err != nil || theirs.Sent != state.SentSlack {
		t.Fatalf("the folder copy %+v %v", theirs, err)
	}
	msgs := c.inbox(t, c.fx.peer, gw)
	// The text `brigade decision` sends at once (card 193): the question,
	// the options, and the question alone as the summary through a gateway.
	if len(msgs) != 1 || msgs[0].Body != "to: @owner\n\nShip 0.48.0 now, or wait?\n\n- ship now\n- wait" || msgs[0].Summary != "Ship 0.48.0 now, or wait?" || msgs[0].Sender.SessionID != c.fx.sessionID {
		t.Fatalf("the gateway's inbox: %+v", msgs)
	}
	awaitNotice(t, c.fx, "Brigade decision "+c.id+": your user did not answer here within their wait, so it was sent to them through the slack gateway; close it with the outcome when they answer.")
	// Once: the checks that follow find nothing held.
	c.checks.pass(t)
	if n := c.fx.logCount("decision: held ask sent", nil); n != 1 {
		t.Fatalf("the ask was sent %d times", n)
	}
}

// TestHeldAskEndsWithNothingSent (card 236, askhold.go): a prompt in the
// conversation since the ask ends the hold for good, before the wait and
// after it; a watcher that started after the wait had ended gives the ask
// up, since the session was not running then; and a decision closed
// meanwhile is nobody's to send. In each the gateway gets nothing.
func TestHeldAskEndsWithNothingSent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for name, tc := range map[string]struct {
		asked, lastPrompt, until time.Time
		closed                   bool
		ended                    string
	}{
		"a prompt since the ask, the wait still running": {now.Add(-time.Minute), now.Add(-30 * time.Second), now.Add(time.Hour), false, state.HoldPrompt},
		"a prompt since the ask, the wait over":          {now.Add(-time.Hour), now.Add(-30 * time.Minute), now.Add(-50 * time.Minute), false, state.HoldPrompt},
		"the session was not running when it ended":      {now.Add(-time.Hour), now.Add(-2 * time.Hour), now.Add(-50 * time.Minute), false, state.HoldMissed},
		"closed meanwhile":                               {now.Add(-time.Hour), now.Add(-2 * time.Hour), now.Add(-50 * time.Minute), true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newHoldCase(t, tc.lastPrompt)
			c.ask(t, tc.asked, tc.until)
			gw := c.gateway(t)
			c.subscribe(t, `"@owner"`)
			if tc.closed {
				d := c.decision(t)
				d.ClosedAt, d.Outcome, d.UpdatedAt = now, "ship now", now
				if err := c.store.SaveDecision(d); err != nil {
					t.Fatal(err)
				}
			}
			c.start(t)
			if tc.ended != "" {
				c.ended(t, tc.ended)
			}
			c.checks.pass(t)
			d := c.decision(t)
			if d.Held() || d.Sent != "" || d.HoldEnded != tc.ended || d.Open() == tc.closed {
				t.Fatalf("decision %+v", *d)
			}
			if c.fx.logHas("decision: held ask sent", nil) {
				t.Fatal("an ask went out")
			}
			if msgs := c.inbox(t, c.fx.peer, gw); len(msgs) != 0 {
				t.Fatalf("the gateway's inbox: %+v", msgs)
			}
		})
	}
}

// TestHeldAskWithNoGateway (card 236, askhold.go): with no gateway of the
// member's recipients on the roster, a held ask goes to another session
// of the same member as a plain team message — never to the asking one,
// whose conversation already has the question — and with no other session
// either, the ask is tried until the give-up and then the hold ends
// unsent, the notice says so and the digest lists the decision.
func TestHeldAskWithNoGateway(t *testing.T) {
	t.Parallel()
	t.Run("another session of the member", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		other := c.second(t)
		c.askNow(t)
		c.waitOver()
		c.sent(t, 1, map[string]any{"how": "session"})
		if d := c.decision(t); d.Sent != state.SentSession || d.SentTo != other || d.Held() {
			t.Fatalf("decision %+v", *d)
		}
		msgs := c.inbox(t, c.fx.owner, other)
		if len(msgs) != 1 || msgs[0].Body != "Ship 0.48.0 now, or wait?\n\n- ship now\n- wait" || msgs[0].Summary != "Decision for owner@example.com: Ship 0.48.0 now, or wait?" {
			t.Fatalf("the other session's inbox: %+v", msgs)
		}
		awaitNotice(t, c.fx, "so it was sent to another session of theirs as a team message")
	})
	t.Run("nobody to send to", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		c.giveUp = 10 * time.Minute
		c.subscribe(t, `"@owner"`)
		until := c.askNow(t)
		// Tried for as long as the give-up allows, not dropped at the first
		// check that found no road: the wait is over, checks run, and the ask
		// is still held.
		c.waitOver()
		c.checks.pass(t)
		if d := c.decision(t); !d.Held() {
			t.Fatalf("the hold ended at the first checks that found no road: %+v", *d)
		}
		c.clock.Advance(c.giveUp)
		c.ended(t, state.HoldUnsent)
		if d := c.decision(t); d.UpdatedAt.Before(until.Add(c.giveUp)) {
			t.Fatalf("the hold ended unsent at %s, before the give-up (%s after %s)", d.UpdatedAt, c.giveUp, until)
		}
		if d := c.decision(t); d.Sent != "" || d.HoldEnded != state.HoldUnsent || d.Held() || !d.Open() {
			t.Fatalf("decision %+v", *d)
		}
		awaitNotice(t, c.fx, "Brigade decision "+c.id+": your user did not answer here within their wait, and it could not be sent")
		// The peer's own session is another member's: it got nothing.
		if msgs := c.inbox(t, c.fx.peer, c.fx.senderSessionID); len(msgs) != 0 {
			t.Fatalf("another member's session got the ask: %+v", msgs)
		}
	})
}

// TestHeldAskWaitsForARoad (card 236, askhold.go; the review's findings):
// a due ask with no road yet is tried again rather than dropped or sent
// somewhere else. A gateway that comes onto the roster after the wait
// ended carries it; and while reports.json cannot be read, so that the
// member's recipients are unknown, the ask waits — it does not go to
// another session of theirs — and goes through the gateway once the file
// reads again. A send the backend refuses is the same: held back for some
// checks, then sent, once (work item 336, where a clock that stood still
// showed that no test had the watcher try a send again).
func TestHeldAskWaitsForARoad(t *testing.T) {
	t.Parallel()
	t.Run("the gateway comes back", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		c.subscribe(t, `"@owner"`)
		c.askNow(t)
		// Two checks past the wait with no gateway: still held.
		c.waitOver()
		c.checks.pass(t)
		if d := c.decision(t); !d.Held() {
			t.Fatalf("the hold ended with no road: %+v", *d)
		}
		gw := c.gateway(t)
		c.sent(t, 1, map[string]any{"how": "slack"})
		if msgs := c.inbox(t, c.fx.peer, gw); len(msgs) != 1 || !strings.HasPrefix(msgs[0].Body, "to: @owner\n\n") {
			t.Fatalf("the gateway's inbox: %+v", msgs)
		}
	})
	t.Run("reports.json unreadable", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		gw := c.gateway(t)
		other := c.second(t)
		if err := os.WriteFile(state.ReportsPath(c.folder), []byte(`{"version":9}`), 0o600); err != nil {
			t.Fatal(err)
		}
		c.askNow(t)
		c.waitOver()
		c.fx.waitLog("decision: reports.json unreadable; the held ask waits", nil)
		c.checks.pass(t)
		if d := c.decision(t); !d.Held() {
			t.Fatalf("the hold ended while the file was unreadable: %+v", *d)
		}
		if msgs := c.inbox(t, c.fx.owner, other); len(msgs) != 0 {
			t.Fatalf("the ask went to another session of the member while their recipients were unknown: %+v", msgs)
		}
		c.subscribe(t, `"@owner"`)
		c.sent(t, 1, map[string]any{"how": "slack"})
		if msgs := c.inbox(t, c.fx.peer, gw); len(msgs) != 1 {
			t.Fatalf("the gateway's inbox: %+v", msgs)
		}
		if msgs := c.inbox(t, c.fx.owner, other); len(msgs) != 0 {
			t.Fatalf("the other session's inbox: %+v", msgs)
		}
	})
	t.Run("the backend refuses the send once", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		gw := c.gateway(t)
		c.subscribe(t, `"@owner"`)
		c.spawn = refusingOnce("message send")
		c.askNow(t)
		c.waitOver()
		// Until the refusal the clock runs a millisecond at a look: enough for
		// a roster read that failed to be tried again, and too little to move
		// past the hold-back that the refusal starts.
		acts(t, c.fx, "the watcher's line that the held ask was not sent", c.clock, time.Millisecond, func() bool {
			return c.fx.logHas("decision: the held ask was not sent", map[string]any{"how": "slack"})
		})
		// Held back, by the watcher's clock, which now stands still: the
		// checks that run neither send it again nor give it up.
		c.checks.pass(t)
		if d := c.decision(t); !d.Held() || c.fx.logHas("decision: held ask sent", nil) {
			t.Fatalf("an ask whose send was refused, before its hold-back had passed: %+v", *d)
		}
		// And tried again when it has: one message, whatever the attempts.
		c.sent(t, 1, map[string]any{"how": "slack"})
		if msgs := c.inbox(t, c.fx.peer, gw); len(msgs) != 1 {
			t.Fatalf("the gateway's inbox: %+v", msgs)
		}
	})
}

// TestHeldAsksEachGetTheirNotice (card 236, askhold.go; the review's
// finding): two asks released between two prompts each leave their own
// line for the session, where one topic would have kept only the last.
func TestHeldAsksEachGetTheirNotice(t *testing.T) {
	t.Parallel()
	c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
	c.gateway(t)
	c.subscribe(t, `"@owner"`)
	until := c.askNow(t)
	// The second is asked a moment after the first and waits a little
	// longer.
	second := "d-20261110-fedcba"
	c.askAs(t, second, c.clock.Now().Add(10*time.Millisecond), until.Add(300*time.Millisecond))
	c.waitOver()
	c.sent(t, 2, nil)
	awaitNotice(t, c.fx, "Brigade decision "+c.id+": your user did not answer here within their wait", "Brigade decision "+second+": your user did not answer here within their wait")
	for _, id := range []string{c.id, second} {
		if d := c.decisionOf(t, id); d.Sent != state.SentSlack || d.Held() {
			t.Fatalf("decision %s: %+v", id, *d)
		}
	}
}

// TestHeldAskYieldsToACloseMadeElsewhere (card 236, askhold.go; the
// review's finding): another session of the member, on another machine,
// closes the decision through the folder copy, which is all it has. The
// asking session's watcher takes that close over — it does not send the
// ask, and it does not write its own open copy back over the close.
//
// The close lands while the ask is held, and the wait ends when the test
// says so, after the watcher has taken the close over (work item 333). The
// wait was 400 ms of real time, and the test's read of the decision and its
// write of the close, two files synced to disk, had to end inside them: on
// a loaded machine they did not, the watcher sent the ask first, as it
// should for an ask nobody closed, and the line this test waits for never
// came.
func TestHeldAskYieldsToACloseMadeElsewhere(t *testing.T) {
	t.Parallel()
	c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
	gw := c.gateway(t)
	c.subscribe(t, `"@owner"`)
	c.askNow(t)
	// The other machine's close: its own store, the same folder.
	theirs := state.Store{StateDir: filepath.Join(t.TempDir(), "their-state"), Folder: c.folder}
	d, err := state.ReadDecision(c.folder, c.id)
	if err != nil {
		t.Fatal(err)
	}
	closedAt := c.clock.Now()
	d.ClosedAt, d.Outcome, d.UpdatedAt = closedAt, "ship now", closedAt
	d.ClosedBy = &state.Closer{Member: "owner@example.com", SessionName: "laptop", SessionID: "their-session"}
	if err := theirs.SaveDecision(d); err != nil {
		t.Fatal(err)
	}
	c.fx.waitLog("decision: closed by another session of the member; the hold is over", nil)
	// The wait passes, and the checks that follow send nothing for a
	// decision that is closed.
	c.waitOver()
	c.checks.pass(t)
	for name, got := range map[string]*state.Decision{"the asking machine's copy": c.decision(t), "the folder's copy": mustFolderDecision(t, c.folder, c.id)} {
		if got.Open() || got.Outcome != "ship now" || got.Sent != "" || got.ClosedBy == nil || got.ClosedBy.SessionName != "laptop" {
			t.Fatalf("%s: %+v", name, *got)
		}
	}
	if c.fx.logHas("decision: held ask sent", nil) {
		t.Fatal("the ask went out although the decision was closed")
	}
	if msgs := c.inbox(t, c.fx.peer, gw); len(msgs) != 0 {
		t.Fatalf("the gateway's inbox: %+v", msgs)
	}
}

// mustFolderDecision reads the folder's copy of a decision.
func mustFolderDecision(t *testing.T, folder, id string) *state.Decision {
	t.Helper()
	d, err := state.ReadDecision(folder, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
