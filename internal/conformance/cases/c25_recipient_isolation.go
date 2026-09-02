package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c25RecipientIsolation: C (team T2) sending to B's session and to a
// random id are both `not_found` with byte-identical stdout — a session
// outside the team cannot be messaged and there is no existence oracle
// (4.4.6, 4.5.6). The positive control is an `invalid_input` envelope.
func c25RecipientIsolation() conformance.Case {
	return conformance.Case{
		ID:    "C-25",
		Rule:  "4.5.6 recipient outside the team",
		Title: "send to another team's session ≡ unknown id (not_found)",
		Tags:  []string{conformance.TagCore},
		Run:   runC25,
	}
}

func runC25(t *conformance.T) {
	b, c := t.B(), t.C()
	_, sc := t.Register(c, "c25-c-"+t.RunID(), nil)
	_, sb := t.Register(b, "c25-b-"+t.RunID(), nil)
	toB := protocol.SendRequest{SenderSessionID: sc, RecipientSessionID: sb, Body: "c25: across teams"}
	rB := t.SendRaw(c, mustMarshal(t, &toB))
	t.Fail(rB, protocol.CodeNotFound)
	toRandom := protocol.SendRequest{SenderSessionID: sc, RecipientSessionID: randomRef(), Body: "c25: across teams"}
	rRandom := t.SendRaw(c, mustMarshal(t, &toRandom))
	t.Fail(rRandom, protocol.CodeNotFound)
	t.SameBytes("B's session vs a random id as recipient (4.5.6)", rB, rRandom)

	empty := protocol.SendRequest{SenderSessionID: sc, RecipientSessionID: sc, Body: ""}
	rInvalid := t.SendRaw(c, mustMarshal(t, &empty))
	t.Fail(rInvalid, protocol.CodeInvalidInput)
	t.DifferentBytes("not_found vs invalid_input (positive control)", rB, rInvalid)
	if n := len(t.Receive(b, sb, 0)); n != 0 {
		t.Errorf("message receive: %d messages reached B's session from another team; want none (4.5.6)", n)
	}
}
