package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c31Closed: sending FROM a closed session is conflict (4.5.8); sending TO
// a closed recipient is accepted and the message waits: `message receive`
// on the closed, owned session returns it (4.5.7 requires ownership only;
// 4.5.8 names heartbeat and send-from as the closed-session conflicts), and
// with session.resume the re-opened session still holds it.
func c31Closed() conformance.Case {
	return conformance.Case{
		ID:    "C-31",
		Rule:  "4.5.8 closed sessions",
		Title: "send from a closed sender is conflict; send to a closed recipient is accepted and waits",
		Tags:  []string{conformance.TagCore},
		Run:   runC31,
	}
}

func runC31(t *conformance.T) {
	a, b := t.A(), t.B()

	_, closedSender := t.Register(a, nameFor(t, "C-31", "closed-sender"), nil)
	_, open := t.Register(b, nameFor(t, "C-31", "open-recipient"), nil)
	t.Close(a, closedSender)
	t.Fail(t.SendRaw(a, mustMarshal(t, protocol.SendRequest{SenderSessionID: closedSender, RecipientSessionID: open, Body: "from a closed session"})), protocol.CodeConflict)

	_, sender := t.Register(a, nameFor(t, "C-31", "sender"), nil)
	_, closedRecipient := t.Register(b, nameFor(t, "C-31", "closed-recipient"), nil)
	t.Close(b, closedRecipient)
	res := t.Send(a, sender, closedRecipient, "parked until resume", nil)

	if !hasMessage(t.Receive(b, closedRecipient, 0), res.MessageID) {
		t.Errorf("message receive on the closed recipient: the accepted message is missing (4.5.8: it waits for retention or resume)")
	}

	if t.HasCap("session.resume") {
		rec, id := t.Register(b, nameFor(t, "C-31", "closed-recipient"), func(reg *protocol.SessionRegistration) {
			reg.Resume = &protocol.ResumeRef{SessionID: closedRecipient}
		})
		if id != closedRecipient {
			t.Errorf("session register with resume: session_id %s, want %s (4.5.8)", id, closedRecipient)
		}
		if resumed, _ := rec["resumed"].(bool); !resumed {
			t.Errorf("session register with resume: resumed %v, want true (4.5.8)", rec["resumed"])
		}
		if !hasMessage(t.Receive(b, closedRecipient, 0), res.MessageID) {
			t.Errorf("message receive after resume: the parked message is missing (4.5.8: pending messages kept)")
		}
	}
}

// hasMessage reports whether id is among the envelopes.
func hasMessage(msgs []protocol.MessageEnvelope, id string) bool {
	for i := range msgs {
		if msgs[i].MessageID == id {
			return true
		}
	}
	return false
}
