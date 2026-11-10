package digest_test

import (
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestHeldAsksStayOutOfTheDigest (card 236): a decision whose ask is still
// held — being asked in its session's conversation — is in no digest and
// makes none due; once the wait has passed, or a prompt ended the hold, it
// is listed as open with the fact that it was only asked there.
func TestHeldAsksStayOutOfTheDigest(t *testing.T) {
	t.Parallel()
	until := t0.Add(10 * time.Minute)
	held := &state.Decision{Version: 1, ID: "d-20261029-aaaaaa", Question: "Held?", To: "rjae@example.com", Member: "rjae@example.com", SessionName: "payments", SessionID: "s1", CreatedAt: t0, UpdatedAt: t0}
	held.Hold(until)
	sent := &state.Decision{Version: 1, ID: "d-20261029-bbbbbb", Question: "Sent?", To: "bob@example.com", SessionName: "payments", SessionID: "s1", Sent: state.SentSlack, CreatedAt: t0, UpdatedAt: t0}
	all := []*state.Decision{held, sent}

	if got := digest.Asked(all, t0.Add(time.Minute)); len(got) != 1 || got[0] != sent {
		t.Fatalf("Asked while the hold runs: %+v", got)
	}
	if got := digest.Asked(all, until); len(got) != 2 {
		t.Fatalf("Asked once the wait has passed: %+v", got)
	}
	// The producer judges "anything written since the marker" on the same
	// list: a held ask alone changes nothing.
	marker := &digest.Marker{ProducedAt: t0.Add(-time.Minute)}
	if digest.Changed(nil, digest.Asked([]*state.Decision{held}, t0.Add(time.Minute)), marker) {
		t.Fatal("a held ask alone made a digest due")
	}
	// A digest produced while the hold ran left the decision out; once the
	// wait has passed it is in the list, and that alone makes one due —
	// nothing may ever write the file again (its session ended).
	meanwhile := &digest.Marker{ProducedAt: t0.Add(5 * time.Minute)}
	if !digest.Changed(nil, digest.Asked([]*state.Decision{held}, until.Add(time.Second)), meanwhile) {
		t.Fatal("a decision whose wait passed since the last digest made none due")
	}
	if digest.Changed(nil, digest.Asked([]*state.Decision{held}, until.Add(time.Hour)), &digest.Marker{ProducedAt: until.Add(time.Minute)}) {
		t.Fatal("a wait that ended before the last digest made another due")
	}

	build := func(now time.Time) digest.Output {
		return digest.Build(digest.Input{Team: "ops", Repository: "brigade", Producer: "payments", Now: now, Decisions: all})
	}
	during := build(t0.Add(time.Minute))
	if during.DecisionsOpen != 1 || strings.Contains(during.Body, "Held?") || !strings.Contains(during.Body, "Sent?") {
		t.Fatalf("the digest while the hold runs:\n%s", during.Body)
	}
	after := build(until.Add(time.Minute))
	if after.DecisionsOpen != 2 || !strings.Contains(after.Body, "  Held?\n    for rjae@example.com, asked by rjae@example.com (session payments), open for 11 m, asked in the conversation") {
		t.Fatalf("the digest after the wait:\n%s", after.Body)
	}
	if strings.Contains(after.Body, "Sent?\n    for bob@example.com, asked by session payments, open for 11 m, asked in the conversation") {
		t.Fatalf("a sent ask carries the conversation fact:\n%s", after.Body)
	}
	// A prompt ended the hold before the wait: listed from then on.
	held.HoldEnded = state.HoldPrompt
	if got := build(t0.Add(time.Minute)); got.DecisionsOpen != 2 || !strings.Contains(got.Body, "asked in the conversation") {
		t.Fatalf("the digest after a prompt ended the hold:\n%s", got.Body)
	}
}

// TestPickRouteAndOtherSessionOf (card 236): a held ask takes the road
// every ask takes — the first gateway of the recipients on the roster,
// else the session picked for the member — and the session picked for the
// asking session's own member is their best other one: same principal,
// never the asking session, a gateway or one that refuses.
func TestPickRouteAndOtherSessionOf(t *testing.T) {
	t.Parallel()
	own := func(id, state, inbound, harness string, seen time.Time) protocol.SessionRecord {
		r := rec(id, id, "rjae@example.com", state, inbound, harness, seen, "")
		r.PrincipalRef = "p1"
		return r
	}
	roster := []protocol.SessionRecord{
		own("asking", protocol.SessionStateActive, protocol.InboundAccept, "claude-code", t0),
		own("idle-fresh", protocol.SessionStateIdle, protocol.InboundAccept, "codex", t0),
		own("active-old", protocol.SessionStateActive, protocol.InboundAccept, "claude-code", t0.Add(-time.Hour)),
		own("refusing", protocol.SessionStateActive, protocol.InboundRefuse, "claude-code", t0),
		rec("bob", "billing", "bob@example.com", protocol.SessionStateActive, protocol.InboundAccept, "claude-code", t0, ""),
		rec("slack", "slack-gateway", "", protocol.SessionStateIdle, protocol.InboundAccept, "gateway-slack", t0, ""),
	}
	if got := digest.OtherSessionOf("asking", roster); got != "active-old" {
		t.Errorf("OtherSessionOf = %q, want the member's active session", got)
	}
	if got := digest.OtherSessionOf("bob", roster); got != "" {
		t.Errorf("a member with one session: OtherSessionOf = %q", got)
	}
	if got := digest.OtherSessionOf("gone", roster); got != "" {
		t.Errorf("an asking session the roster does not list: OtherSessionOf = %q", got)
	}

	if r, ok := digest.PickRoute([]string{"rjae@example.com"}, "active-old", roster); !ok || r.How != state.SentSlack || r.To != "rjae@example.com" || r.SessionID != "slack" {
		t.Errorf("with a gateway on the roster: %+v %v", r, ok)
	}
	noGateway := roster[:5]
	if r, ok := digest.PickRoute([]string{"rjae@example.com"}, "active-old", noGateway); !ok || r.How != state.SentSession || r.SessionID != "active-old" || r.To != "active-old" {
		t.Errorf("with no gateway: %+v %v", r, ok)
	}
	if r, ok := digest.PickRoute(nil, "", roster); ok {
		t.Errorf("no recipient and no session: %+v", r)
	}

	// The text is the one `brigade decision` sends at once (card 193).
	d := &state.Decision{Version: 1, ID: "d-20261029-aaaaaa", Question: "Ship it?", To: "rjae@example.com", Options: []string{"ship now", "wait"}, Default: "wait", By: "2026-10-31", SessionName: "payments", SessionID: "asking", CreatedAt: t0}
	body, summary := digest.AskMessage(d, digest.Delivery{How: state.SentSlack, To: "@rjae", SessionID: "slack"})
	if body != "to: @rjae\n\n"+d.Ask() || body != "to: @rjae\n\nShip it?\n\n- ship now\n- [default] wait\n\nAnswer by 2026-10-31." || summary != "Ship it?" {
		t.Errorf("the ask through a gateway: %q / %q", body, summary)
	}
	body, summary = digest.AskMessage(d, digest.Delivery{How: state.SentSession, To: "active-old", SessionID: "active-old"})
	if body != d.Ask() || summary != "Decision for rjae@example.com: Ship it?" {
		t.Errorf("the ask to a session: %q / %q", body, summary)
	}
}
