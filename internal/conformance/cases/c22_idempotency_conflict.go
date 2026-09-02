package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c22IdempotencyConflict: reusing an idempotency_key with a different body,
// or with a different recipient, is `conflict` (4.5.4). B sends to A.
func c22IdempotencyConflict() conformance.Case {
	return conformance.Case{
		ID:    "C-22",
		Rule:  "4.5.4 key reused with another payload",
		Title: "the same key with a different body or a different recipient → conflict",
		Tags:  []string{conformance.TagCore},
		Run:   runC22,
	}
}

func runC22(t *conformance.T) {
	a, b := t.A(), t.B()
	_, sb := t.Register(b, "c22-b-"+t.RunID(), nil)
	_, sa := t.Register(a, "c22-a-"+t.RunID(), nil)
	_, sa2 := t.Register(a, "c22-a2-"+t.RunID(), nil)
	key := "c22-key-" + t.RunID()
	t.Send(b, sb, sa, "c22: original", func(r *protocol.SendRequest) { r.IdempotencyKey = key })

	otherBody := protocol.SendRequest{SenderSessionID: sb, RecipientSessionID: sa, Body: "c22: changed", IdempotencyKey: key}
	t.Fail(t.SendRaw(b, mustMarshal(t, &otherBody)), protocol.CodeConflict)
	otherRecipient := protocol.SendRequest{SenderSessionID: sb, RecipientSessionID: sa2, Body: "c22: original", IdempotencyKey: key}
	t.Fail(t.SendRaw(b, mustMarshal(t, &otherRecipient)), protocol.CodeConflict)

	if n := len(t.Receive(a, sa2, 0)); n != 0 {
		t.Errorf("message receive on the second recipient: %d messages, want none after the refused send (4.5.4)", n)
	}
}
