package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c21Idempotency: the same request sent twice with one idempotency_key
// yields one logical message — the same message_id, the second answer
// `duplicate true` — and receive returns it once (4.4.7, 4.5.4). B sends
// to A so A's principal send budget is spared for the later cases.
func c21Idempotency() conformance.Case {
	return conformance.Case{
		ID:    "C-21",
		Rule:  "4.5.4 idempotency",
		Title: "the same request twice with one key → same message_id, second duplicate true, received once",
		Tags:  []string{conformance.TagCore},
		Run:   runC21,
	}
}

func runC21(t *conformance.T) {
	a, b := t.A(), t.B()
	_, sb := t.Register(b, "c21-b-"+t.RunID(), nil)
	_, sa := t.Register(a, "c21-a-"+t.RunID(), nil)
	key := "c21-key-" + t.RunID()
	withKey := func(r *protocol.SendRequest) { r.IdempotencyKey = key }
	first := t.Send(b, sb, sa, "c21: once", withKey)
	second := t.Send(b, sb, sa, "c21: once", withKey)
	if first.Duplicate {
		t.Errorf("first send: duplicate true (4.5.4)")
	}
	if !second.Duplicate {
		t.Errorf("second send with the same key: duplicate false, want true (4.5.4)")
	}
	if first.MessageID != second.MessageID {
		t.Errorf("second send with the same key: message_id differs from the first (4.5.4)")
	}
	if second.RecipientSessionID != sa {
		t.Errorf("second send with the same key: recipient_session_id differs from the request (4.4.7)")
	}
	messages := t.Receive(a, sa, 0)
	count := 0
	for _, m := range messages {
		if m.MessageID == first.MessageID {
			count++
		}
	}
	if count != 1 || len(messages) != 1 {
		t.Errorf("message receive: %d messages (%d with the id), want exactly one (4.5.4)", len(messages), count)
	}
}
