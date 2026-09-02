package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c36Redelivery: at least once (4.5.2) and the acknowledgement point
// (4.5.3). A watch emits `message` for m; the watch is SIGKILLed without an
// ack; a restarted watch emits m again (persistence preceded the push,
// 4.5.1); after `message ack` a restarted watch stays silent for the
// deadline. mutant_noack fails the last arm.
func c36Redelivery() conformance.Case {
	return conformance.Case{
		ID:    "C-36",
		Rule:  "4.5.2 redelivery until ack",
		Title: "an unacked message is re-emitted after a watch restart; an acked one is not",
		Tags:  []string{conformance.TagCore},
		Run:   runC36,
	}
}

func runC36(t *conformance.T) {
	a, b := t.A(), t.B()
	_, watched := t.Register(a, nameFor(t, "C-36", "watched"), nil)
	_, sender := t.Register(b, nameFor(t, "C-36", "sender"), nil)

	w1 := t.Watch(a, watched)
	w1.Expect(protocol.EventReady, t.PushDeadline())
	m := t.Send(b, sender, watched, "redeliver me", nil).MessageID
	expectMessageID(t, w1, m, t.PushDeadline())
	w1.Kill()
	w1.Wait(5 * time.Second)

	w2 := t.Watch(a, watched)
	w2.Expect(protocol.EventReady, t.PushDeadline())
	expectMessageID(t, w2, m, t.PushDeadline())
	w2.Kill()
	w2.Wait(5 * time.Second)

	res := t.Ack(a, watched, m)
	if len(res.Acked) != 1 || res.Acked[0] != m {
		t.Errorf("message ack: acked %v, want [%s] (4.5.3)", res.Acked, m)
	}

	w3 := t.Watch(a, watched)
	w3.Expect(protocol.EventReady, t.PushDeadline())
	w3.ExpectNone(t.PushDeadline())
}
