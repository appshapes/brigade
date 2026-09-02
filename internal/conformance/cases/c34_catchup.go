package cases

import (
	"fmt"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c34Catchup: messages accepted before the watch starts are emitted after
// `ready` (4.5.2, 4.4.9): three sends, then a watch, then three `message`
// events carrying those ids in any order. The watched session belongs to A
// and the sender to B (the brief names no principal here; A's shared
// per-minute principal budget is the scarcer one across a whole run).
func c34Catchup() conformance.Case {
	return conformance.Case{
		ID:    "C-34",
		Rule:  "4.5.2 catch-up",
		Title: "messages sent before the watch are emitted after ready",
		Tags:  []string{conformance.TagCore},
		Run:   runC34,
	}
}

func runC34(t *conformance.T) {
	a, b := t.A(), t.B()
	_, watched := t.Register(a, nameFor(t, "C-34", "watched"), nil)
	_, sender := t.Register(b, nameFor(t, "C-34", "sender"), nil)
	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		want[t.Send(b, sender, watched, fmt.Sprintf("before the watch %d", i), nil).MessageID] = true
	}

	w := t.Watch(a, watched)
	w.Expect(protocol.EventReady, t.PushDeadline())
	if missing := collectMessageIDs(w, want, t.PushDeadline()); len(missing) > 0 {
		t.Errorf("message watch: %d of 3 messages sent before the watch were not emitted within %s (4.5.2 catch-up)", len(missing), t.PushDeadline())
	}
}

// collectMessageIDs reads `message` events until every id in want has been
// seen or deadline passes, and returns the ids still missing. Duplicates
// and other kinds are tolerated.
func collectMessageIDs(w *conformance.WatchProc, want map[string]bool, deadline time.Duration) []string {
	missing := map[string]bool{}
	for id := range want {
		missing[id] = true
	}
	until := time.Now().Add(deadline)
	for len(missing) > 0 {
		remaining := time.Until(until)
		if remaining <= 0 {
			break
		}
		ev, ok := w.Next(remaining)
		if !ok {
			break
		}
		if ev.Kind == protocol.EventMessage && ev.Message != nil {
			delete(missing, ev.Message.Message.MessageID)
		}
	}
	out := make([]string, 0, len(missing))
	for id := range missing {
		out = append(out, id)
	}
	return out
}

// expectMessageID reads events until a `message` event carrying id arrives;
// other events are skipped. Not seeing it within deadline is a failure that
// aborts the case.
func expectMessageID(t *conformance.T, w *conformance.WatchProc, id string, deadline time.Duration) conformance.Event {
	until := time.Now().Add(deadline)
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			t.Fatalf("message watch: no `message` event for %s within %s (4.5.2)", id, deadline)
		}
		ev, ok := w.Next(remaining)
		if !ok {
			t.Fatalf("message watch: no `message` event for %s within %s (4.5.2)", id, deadline)
		}
		if ev.Kind == protocol.EventMessage && ev.Message != nil && ev.Message.Message.MessageID == id {
			return ev
		}
	}
}
