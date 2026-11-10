//go:build !windows

package watch_test

import (
	"context"
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
type holdCase struct {
	fx     *fixture
	folder string
	store  state.Store
	id     string
	// giveUp is the watcher's HoldGiveUp; zero is long enough that no test
	// meets it by accident.
	giveUp time.Duration
}

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

// askNow starts the watcher and then asks, the wait ending shortly: the
// order a session has, where the watcher runs before any command does. It
// returns when the wait ends.
func (c *holdCase) askNow(t *testing.T) time.Time {
	t.Helper()
	c.start(t)
	c.fx.waitLog("watcher starting", nil)
	// Past the watcher's own start by more than a clock's grain.
	time.Sleep(20 * time.Millisecond)
	asked := time.Now().UTC()
	until := asked.Add(500 * time.Millisecond)
	c.ask(t, asked, until)
	return until
}

// subscribe writes the owner's subscription with the recipients.
func (c *holdCase) subscribe(t *testing.T, recipients string) {
	t.Helper()
	if err := os.WriteFile(state.ReportsPath(c.folder), []byte(`{"version":2,"subscribers":[{"label":"owner@example.com","recipients":[`+recipients+`],"every":"24h"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// start runs the watcher with a short check interval and no sync engine.
func (c *holdCase) start(t *testing.T) time.Duration {
	t.Helper()
	deps := c.fx.deps()
	deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
	deps.StateCheckInterval = 100 * time.Millisecond
	deps.StatePruneDelay = time.Hour
	deps.HoldGiveUp = time.Hour
	if c.giveUp > 0 {
		deps.HoldGiveUp = c.giveUp
	}
	c.fx.start(deps, c.fx.args()...)
	return deps.StateCheckInterval
}

// gateway registers a session with the Slack gateway's harness, as the
// peer.
func (c *holdCase) gateway(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitShort)
	defer cancel()
	gw, err := c.fx.peer.Register(ctx, &protocol.SessionRegistration{
		Harness: "gateway-slack", HarnessVersion: "0.1.0", SessionName: "slack-gateway", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
	})
	if err != nil {
		t.Fatal(err)
	}
	return gw.SessionID
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

// inbox is what the peer's client receives for a session.
func (c *holdCase) inbox(t *testing.T, client *adapterclient.Client, sessionID string) []protocol.MessageEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitShort)
	defer cancel()
	got, err := client.Receive(ctx, sessionID, 10)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	return got.Messages
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
	ctx, cancel := context.WithTimeout(t.Context(), waitShort)
	defer cancel()
	gw, err := c.fx.peer.Register(ctx, &protocol.SessionRegistration{
		Harness: "gateway-slack", HarnessVersion: "0.1.0", SessionName: "slack-gateway", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.subscribe(t, `"@owner","owner@example.com"`)
	until := c.askNow(t)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return c.fx.logHas("decision: held ask sent", map[string]any{"how": "slack"}) })
	d := c.decision(t)
	if d.Held() || d.Sent != state.SentSlack || d.SentTo != "@owner" || d.HoldEnded != "" || !d.Open() || d.UpdatedAt.Before(until) {
		t.Fatalf("decision after the release: %+v (the wait ended %s)", *d, until)
	}
	if theirs, err := state.ReadDecision(c.folder, c.id); err != nil || theirs.Sent != state.SentSlack {
		t.Fatalf("the folder copy %+v %v", theirs, err)
	}
	msgs := c.inbox(t, c.fx.peer, gw.SessionID)
	// The text `brigade decision` sends at once (card 193): the question,
	// the options, and the question alone as the summary through a gateway.
	if len(msgs) != 1 || msgs[0].Body != "to: @owner\n\nShip 0.48.0 now, or wait?\n\n- ship now\n- wait" || msgs[0].Summary != "Ship 0.48.0 now, or wait?" || msgs[0].Sender.SessionID != c.fx.sessionID {
		t.Fatalf("the gateway's inbox: %+v", msgs)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return strings.Contains(c.fx.notice(), "Brigade decision "+c.id+": your user did not answer here within their wait, so it was sent to them through the slack gateway; close it with the outcome when they answer.")
	})
	// Once: the ticks that follow find nothing held.
	time.Sleep(6 * 100 * time.Millisecond)
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
			ctx, cancel := context.WithTimeout(t.Context(), waitShort)
			defer cancel()
			gw, err := c.fx.peer.Register(ctx, &protocol.SessionRegistration{
				Harness: "gateway-slack", HarnessVersion: "0.1.0", SessionName: "slack-gateway", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
			})
			if err != nil {
				t.Fatal(err)
			}
			c.subscribe(t, `"@owner"`)
			if tc.closed {
				d := c.decision(t)
				d.ClosedAt, d.Outcome, d.UpdatedAt = now, "ship now", now
				if err := c.store.SaveDecision(d); err != nil {
					t.Fatal(err)
				}
			}
			every := c.start(t)
			if tc.ended != "" {
				testutil.Eventually(t, waitShort, pollEvery, func() bool {
					return c.fx.logHas("decision: hold ended with nothing sent", map[string]any{"how": tc.ended})
				})
			}
			time.Sleep(6 * every)
			d := c.decision(t)
			if d.Held() || d.Sent != "" || d.HoldEnded != tc.ended || d.Open() == tc.closed {
				t.Fatalf("decision %+v", *d)
			}
			if c.fx.logHas("decision: held ask sent", nil) {
				t.Fatal("an ask went out")
			}
			if msgs := c.inbox(t, c.fx.peer, gw.SessionID); len(msgs) != 0 {
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
		ctx, cancel := context.WithTimeout(t.Context(), waitShort)
		defer cancel()
		other, err := c.fx.owner.Register(ctx, &protocol.SessionRegistration{
			Harness: "codex", HarnessVersion: "test", SessionName: "second", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
		})
		if err != nil {
			t.Fatal(err)
		}
		c.askNow(t)
		testutil.Eventually(t, waitShort, pollEvery, func() bool { return c.fx.logHas("decision: held ask sent", map[string]any{"how": "session"}) })
		if d := c.decision(t); d.Sent != state.SentSession || d.SentTo != other.SessionID || d.Held() {
			t.Fatalf("decision %+v", *d)
		}
		msgs := c.inbox(t, c.fx.owner, other.SessionID)
		if len(msgs) != 1 || msgs[0].Body != "Ship 0.48.0 now, or wait?\n\n- ship now\n- wait" || msgs[0].Summary != "Decision for owner@example.com: Ship 0.48.0 now, or wait?" {
			t.Fatalf("the other session's inbox: %+v", msgs)
		}
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return strings.Contains(c.fx.notice(), "so it was sent to another session of theirs as a team message")
		})
	})
	t.Run("nobody to send to", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		c.giveUp = 600 * time.Millisecond
		c.subscribe(t, `"@owner"`)
		until := c.askNow(t)
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return c.fx.logHas("decision: hold ended with nothing sent", map[string]any{"how": state.HoldUnsent})
		})
		// Tried for as long as the give-up allows, not dropped at the first
		// tick that found no road.
		if d := c.decision(t); d.UpdatedAt.Before(until.Add(c.giveUp)) {
			t.Fatalf("the hold ended unsent at %s, before the give-up (%s after %s)", d.UpdatedAt, c.giveUp, until)
		}
		if d := c.decision(t); d.Sent != "" || d.HoldEnded != state.HoldUnsent || d.Held() || !d.Open() {
			t.Fatalf("decision %+v", *d)
		}
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return strings.Contains(c.fx.notice(), "Brigade decision "+c.id+": your user did not answer here within their wait, and it could not be sent")
		})
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
// reads again.
func TestHeldAskWaitsForARoad(t *testing.T) {
	t.Parallel()
	t.Run("the gateway comes back", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		c.subscribe(t, `"@owner"`)
		until := c.askNow(t)
		// Two checks past the wait with no gateway: still held.
		for time.Now().Before(until.Add(250 * time.Millisecond)) {
			time.Sleep(20 * time.Millisecond)
		}
		if d := c.decision(t); !d.Held() {
			t.Fatalf("the hold ended with no road: %+v", *d)
		}
		gw := c.gateway(t)
		testutil.Eventually(t, waitShort, pollEvery, func() bool { return c.fx.logHas("decision: held ask sent", map[string]any{"how": "slack"}) })
		if msgs := c.inbox(t, c.fx.peer, gw); len(msgs) != 1 || !strings.HasPrefix(msgs[0].Body, "to: @owner\n\n") {
			t.Fatalf("the gateway's inbox: %+v", msgs)
		}
	})
	t.Run("reports.json unreadable", func(t *testing.T) {
		t.Parallel()
		c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
		gw := c.gateway(t)
		ctx, cancel := context.WithTimeout(t.Context(), waitShort)
		defer cancel()
		other, err := c.fx.owner.Register(ctx, &protocol.SessionRegistration{
			Harness: "codex", HarnessVersion: "test", SessionName: "second", Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state.ReportsPath(c.folder), []byte(`{"version":9}`), 0o600); err != nil {
			t.Fatal(err)
		}
		until := c.askNow(t)
		testutil.Eventually(t, waitShort, pollEvery, func() bool { return c.fx.logHas("decision: reports.json unreadable; the held ask waits", nil) })
		for time.Now().Before(until.Add(250 * time.Millisecond)) {
			time.Sleep(20 * time.Millisecond)
		}
		if d := c.decision(t); !d.Held() {
			t.Fatalf("the hold ended while the file was unreadable: %+v", *d)
		}
		if msgs := c.inbox(t, c.fx.owner, other.SessionID); len(msgs) != 0 {
			t.Fatalf("the ask went to another session of the member while their recipients were unknown: %+v", msgs)
		}
		c.subscribe(t, `"@owner"`)
		testutil.Eventually(t, waitShort, pollEvery, func() bool { return c.fx.logHas("decision: held ask sent", map[string]any{"how": "slack"}) })
		if msgs := c.inbox(t, c.fx.peer, gw); len(msgs) != 1 {
			t.Fatalf("the gateway's inbox: %+v", msgs)
		}
		if msgs := c.inbox(t, c.fx.owner, other.SessionID); len(msgs) != 0 {
			t.Fatalf("the other session's inbox: %+v", msgs)
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
	second := "d-20261110-fedcba"
	c.askAs(t, second, until.Add(-490*time.Millisecond), until.Add(300*time.Millisecond))
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return c.fx.logCount("decision: held ask sent", nil) == 2 })
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		n := c.fx.notice()
		return strings.Contains(n, "Brigade decision "+c.id+": your user did not answer here within their wait") &&
			strings.Contains(n, "Brigade decision "+second+": your user did not answer here within their wait")
	})
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
func TestHeldAskYieldsToACloseMadeElsewhere(t *testing.T) {
	t.Parallel()
	c := newHoldCase(t, time.Now().UTC().Add(-time.Minute))
	gw := c.gateway(t)
	c.subscribe(t, `"@owner"`)
	every := c.start(t)
	c.fx.waitLog("watcher starting", nil)
	time.Sleep(20 * time.Millisecond)
	asked := time.Now().UTC()
	c.ask(t, asked, asked.Add(400*time.Millisecond))
	// The other machine's close: its own store, the same folder.
	theirs := state.Store{StateDir: filepath.Join(t.TempDir(), "their-state"), Folder: c.folder}
	d, err := state.ReadDecision(c.folder, c.id)
	if err != nil {
		t.Fatal(err)
	}
	closedAt := time.Now().UTC()
	d.ClosedAt, d.Outcome, d.UpdatedAt = closedAt, "ship now", closedAt
	d.ClosedBy = &state.Closer{Member: "owner@example.com", SessionName: "laptop", SessionID: "their-session"}
	if err := theirs.SaveDecision(d); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return c.fx.logHas("decision: closed by another session of the member; the hold is over", nil)
	})
	time.Sleep(8 * every)
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
