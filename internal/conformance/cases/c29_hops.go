package cases

import (
	"fmt"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c29Hops: the explicit reply chain of 4.5.12. ACK-FREE by design: the chain
// ROTATES — every hop is sent by the session that received the previous
// message to a fresh session of the other principal — so no sender-recipient
// pair ever holds more than one unacknowledged message and mutant_noack
// cannot fail it. `hop_count` is asserted on the SendResponse of every step
// (exactly k for message k), the send that would exceed max_hop_count is
// loop_detected (exit 12), `reply_to` naming a message the sender sent but
// never received is not_found and byte-identical to `reply_to` naming an
// unknown id, with loop_detected as the positive control.
func c29Hops() conformance.Case {
	return conformance.Case{
		ID:    "C-29",
		Rule:  "4.5.12 reply chain",
		Title: "reply_to chain increments hop_count by one per hop up to max_hop_count, then loop_detected; unreceived reply_to is not_found",
		Tags:  []string{conformance.TagCore},
		Run:   runC29,
	}
}

func runC29(t *conformance.T) {
	a, b := t.A(), t.B()
	maxHop := t.Describe().Limits.MaxHopCount
	// R(k) is owned by B for even k and by A for odd k, so the principals
	// alternate and each sends about half the chain.
	owner := func(k int) *conformance.Principal {
		if k%2 == 0 {
			return b
		}
		return a
	}

	_, s0 := t.Register(a, nameFor(t, "C-29", "s0"), nil)
	_, r0 := t.Register(b, nameFor(t, "C-29", "r0"), nil)
	m0 := t.Send(a, s0, r0, "m0", nil)
	if m0.HopCount != 0 {
		t.Errorf("message send m0 (no reply_to): hop_count %d, want 0 (4.5.12)", m0.HopCount)
	}

	prevID, prevSession, prevOwner := m0.MessageID, r0, b
	for k := 1; k <= maxHop; k++ {
		_, rk := t.Register(owner(k), nameFor(t, "C-29", fmt.Sprintf("r%d", k)), nil)
		res := t.Send(prevOwner, prevSession, rk, fmt.Sprintf("m%d", k), func(req *protocol.SendRequest) { req.ReplyTo = prevID })
		if res.HopCount != k {
			t.Errorf("message send m%d (reply_to m%d): hop_count %d, want %d (4.5.12)", k, k-1, res.HopCount, k)
		}
		prevID, prevSession, prevOwner = res.MessageID, rk, owner(k)
	}

	// The reply to m<maxHop> would be hop maxHop+1: loop_detected.
	_, next := t.Register(owner(maxHop+1), nameFor(t, "C-29", "next"), nil)
	loop := t.SendRaw(prevOwner, mustMarshal(t, protocol.SendRequest{
		SenderSessionID: prevSession, RecipientSessionID: next, Body: "one hop too many", ReplyTo: prevID,
	}))
	t.Fail(loop, protocol.CodeLoopDetected)

	// S0 sent m0 and never received it: not_found, byte-identical to an
	// unknown message id; loop_detected is the positive control.
	sent := t.SendRaw(a, mustMarshal(t, protocol.SendRequest{
		SenderSessionID: s0, RecipientSessionID: r0, Body: "reply to my own message", ReplyTo: m0.MessageID,
	}))
	t.Fail(sent, protocol.CodeNotFound)
	unknown := t.SendRaw(a, mustMarshal(t, protocol.SendRequest{
		SenderSessionID: s0, RecipientSessionID: r0, Body: "reply to nothing", ReplyTo: randomRef(),
	}))
	t.Fail(unknown, protocol.CodeNotFound)
	t.SameBytes("reply_to a message the sender did not receive vs an unknown id", sent, unknown)
	t.DifferentBytes("not_found vs loop_detected", sent, loop)
}

// c29bImplicitHops: the implicit reply chain of 4.5.12 — no `reply_to` at
// all, one pair of sessions ping-ponging within implicit_reply_window_seconds,
// hop_count still one more per message and loop_detected after
// max_hop_count. NECESSARILY ACK-DEPENDENT: the implicit hop is keyed on the
// most recent message the recipient sent to the sender, so the chain must
// stay on ONE pair, and 17 messages in one direction exceed the 15-per-pair
// unacknowledged cap; after every 10 messages each side acknowledges its
// inbox, which is what keeps the pair under the cap. mutant_noack therefore
// fails this case legitimately (brief section 8). A first message between two
// other fresh sessions is hop 0; the after-window arm (600 s) is a Note.
func c29bImplicitHops() conformance.Case {
	return conformance.Case{
		ID:    "C-29b",
		Rule:  "4.5.12 implicit reply chain",
		Title: "alternating sends with no reply_to increment hop_count by one per message, then loop_detected; a first message is hop 0",
		Tags:  []string{conformance.TagCore},
		Run:   runC29b,
	}
}

func runC29b(t *conformance.T) {
	a, b := t.A(), t.B()
	lim := t.Describe().Limits
	maxHop := lim.MaxHopCount
	_, sa := t.Register(a, nameFor(t, "C-29b", "sa"), nil)
	_, sb := t.Register(b, nameFor(t, "C-29b", "sb"), nil)

	const ackEvery = 10
	// Message k goes SA → SB for even k and SB → SA for odd k.
	for k := 0; k <= maxHop; k++ {
		p, sender, recipient := a, sa, sb
		if k%2 == 1 {
			p, sender, recipient = b, sb, sa
		}
		res := t.Send(p, sender, recipient, fmt.Sprintf("m%d", k), nil)
		if res.HopCount != k {
			t.Errorf("message send m%d (no reply_to): hop_count %d, want %d (4.5.12)", k, res.HopCount, k)
		}
		if (k+1)%ackEvery == 0 {
			ackInbox(t, a, sa)
			ackInbox(t, b, sb)
		}
	}
	// Message maxHop+1 would be hop maxHop+1: loop_detected.
	p, sender, recipient := a, sa, sb
	if (maxHop+1)%2 == 1 {
		p, sender, recipient = b, sb, sa
	}
	loop := t.SendRaw(p, mustMarshal(t, protocol.SendRequest{SenderSessionID: sender, RecipientSessionID: recipient, Body: "one hop too many"}))
	t.Fail(loop, protocol.CodeLoopDetected)

	// The "none → 0" arm: a first message between two other fresh sessions.
	_, fa := t.Register(a, nameFor(t, "C-29b", "fresh-a"), nil)
	_, fb := t.Register(b, nameFor(t, "C-29b", "fresh-b"), nil)
	if res := t.Send(a, fa, fb, "first", nil); res.HopCount != 0 {
		t.Errorf("message send between fresh sessions: hop_count %d, want 0 (4.5.12)", res.HopCount)
	}
	t.Note("the after-window arm (a fresh message after implicit_reply_window_seconds = %d s has hop_count 0) is not assertable in-suite",
		lim.ImplicitReplyWindowSeconds)
}

// ackInbox acknowledges everything currently in session's inbox.
func ackInbox(t *conformance.T, p *conformance.Principal, session string) {
	msgs := t.Receive(p, session, 0)
	if len(msgs) == 0 {
		return
	}
	ids := make([]string, 0, len(msgs))
	for i := range msgs {
		ids = append(ids, msgs[i].MessageID)
	}
	res := t.Ack(p, session, ids...)
	if len(res.Acked) != len(ids) {
		t.Errorf("message ack --session %s: acked %d of %d received ids (4.5.3)", session, len(res.Acked), len(ids))
	}
}
