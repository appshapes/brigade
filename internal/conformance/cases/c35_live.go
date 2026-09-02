package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c35Live: a message accepted while the watch runs is emitted within the
// push deadline (4.5.2, 4.4.9: 5 s with message.watch.push; the suite uses
// the same 5 s for a polling adapter, see the package comment).
func c35Live() conformance.Case {
	return conformance.Case{
		ID:    "C-35",
		Rule:  "4.5.2 live delivery",
		Title: "a message sent while watching is emitted within the deadline",
		Tags:  []string{conformance.TagCore},
		Run:   runC35,
	}
}

func runC35(t *conformance.T) {
	a, b := t.A(), t.B()
	_, watched := t.Register(a, nameFor(t, "C-35", "watched"), nil)
	_, sender := t.Register(b, nameFor(t, "C-35", "sender"), nil)

	w := t.Watch(a, watched)
	w.Expect(protocol.EventReady, t.PushDeadline())
	m := t.Send(b, sender, watched, "while watching", nil).MessageID
	ev := expectMessageID(t, w, m, t.PushDeadline())
	if ev.Message.Message.RecipientSessionID != watched {
		t.Errorf("message event: recipient_session_id %q, want the watched session %q", ev.Message.Message.RecipientSessionID, watched)
	}
}
