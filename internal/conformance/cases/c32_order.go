package cases

import (
	"fmt"
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
)

// c32Order: ten sends on one fresh pair (ten is under the 15-per-pair
// unacknowledged cap, so nothing is acknowledged and mutant_noack cannot
// fail it); `message receive` returns all ten, and the ORDER is recorded as
// a Note, never asserted (4.5.10: oldest first is a SHOULD and the bundled
// adapters advertise `ordering: none`).
func c32Order() conformance.Case {
	return conformance.Case{
		ID:    "C-32",
		Rule:  "4.5.10 receive order",
		Title: "ten sends are all received; the order is recorded, not asserted",
		Tags:  []string{conformance.TagCore},
		Run:   runC32,
	}
}

func runC32(t *conformance.T) {
	a, b := t.A(), t.B()
	_, sa := t.Register(a, nameFor(t, "C-32", "sender"), nil)
	_, sb := t.Register(b, nameFor(t, "C-32", "recipient"), nil)
	const n = 10
	sent := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sent = append(sent, t.Send(a, sa, sb, fmt.Sprintf("m%d", i), nil).MessageID)
	}

	msgs := t.Receive(b, sb, 0)
	for _, id := range sent {
		if !hasMessage(msgs, id) {
			t.Errorf("message receive: sent message %s is missing (4.5.2)", id)
		}
	}
	if len(msgs) != n {
		t.Errorf("message receive: %d messages, want %d (4.5.2)", len(msgs), n)
	}

	// Recorded, not asserted: does the batch come back in send order?
	position := map[string]int{}
	for i, id := range sent {
		position[id] = i
	}
	var order []string
	inSendOrder := len(msgs) == n
	for i := range msgs {
		p, known := position[msgs[i].MessageID]
		if !known {
			order = append(order, "?")
			inSendOrder = false
			continue
		}
		order = append(order, fmt.Sprintf("m%d", p))
		if p != i {
			inSendOrder = false
		}
	}
	if inSendOrder {
		t.Note("receive order: oldest first (send order), delivery.ordering %q", t.Describe().Delivery.Ordering)
	} else {
		t.Note("receive order: not send order (%s), delivery.ordering %q", strings.Join(order, " "), t.Describe().Delivery.Ordering)
	}
}
