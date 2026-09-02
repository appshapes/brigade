package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c24SenderOwnership: B sending with `sender_session_id` = A's session,
// and with a random id, are both `not_found` with byte-identical stdout
// — a caller cannot impersonate a session by naming it (4.4.6, 4.5.5,
// 4.5.7). The positive control is an `invalid_input` envelope.
func c24SenderOwnership() conformance.Case {
	return conformance.Case{
		ID:    "C-24",
		Rule:  "4.5.7 sender ownership",
		Title: "send with another principal's sender_session_id ≡ unknown id (not_found)",
		Tags:  []string{conformance.TagCore},
		Run:   runC24,
	}
}

func runC24(t *conformance.T) {
	a, b := t.A(), t.B()
	_, sa := t.Register(a, "c24-a-"+t.RunID(), nil)
	_, sb := t.Register(b, "c24-b-"+t.RunID(), nil)
	asA := protocol.SendRequest{SenderSessionID: sa, RecipientSessionID: sb, Body: "c24: impersonation"}
	rA := t.SendRaw(b, mustMarshal(t, &asA))
	t.Fail(rA, protocol.CodeNotFound)
	asRandom := protocol.SendRequest{SenderSessionID: randomRef(), RecipientSessionID: sb, Body: "c24: impersonation"}
	rRandom := t.SendRaw(b, mustMarshal(t, &asRandom))
	t.Fail(rRandom, protocol.CodeNotFound)
	t.SameBytes("A's session as sender vs a random id (4.5.7)", rA, rRandom)

	empty := protocol.SendRequest{SenderSessionID: sb, RecipientSessionID: sa, Body: ""}
	rInvalid := t.SendRaw(b, mustMarshal(t, &empty))
	t.Fail(rInvalid, protocol.CodeInvalidInput)
	t.DifferentBytes("not_found vs invalid_input (positive control)", rA, rInvalid)
	if n := len(t.Receive(b, sb, 0)); n != 0 {
		t.Errorf("message receive: %d messages reached B's session from the refused sends; want none", n)
	}
}
