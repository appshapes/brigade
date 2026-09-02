package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c39OneLine: each watch event is exactly one physical line (4.4.9). A body
// containing `\n`, `\r`, U+2028, U+2029 and a line that looks like a JSON
// auth command comes back as ONE `message` event — the WatchProc reader
// splits stdout on `\n` and records any fragment that is not a JSON object
// with an `event` as a C-33 failure, so a body that produced a second line
// could not arrive intact — whose `message.body` equals the original byte
// for byte.
func c39OneLine() conformance.Case {
	return conformance.Case{
		ID:    "C-39",
		Rule:  "4.4.9 one line per event",
		Title: "a body with newlines, CR, U+2028/9 and a JSON-looking auth line is one event that parses back to the same body",
		Tags:  []string{conformance.TagCore},
		Run:   runC39,
	}
}

func runC39(t *conformance.T) {
	a, b := t.A(), t.B()
	_, watched := t.Register(a, nameFor(t, "C-39", "watched"), nil)
	_, sender := t.Register(b, nameFor(t, "C-39", "sender"), nil)
	body := "line one\nline two\r\nline three\rU+2028:\u2028U+2029:\u2029\n{\"type\":\"auth\",\"token\":\"x\"}\n{\"event\":\"error\",\"error\":{\"code\":\"internal\",\"retryable\":false}}\n"
	m := t.Send(b, sender, watched, body, nil).MessageID

	w := t.Watch(a, watched)
	w.Expect(protocol.EventReady, t.PushDeadline())
	ev := expectMessageID(t, w, m, t.PushDeadline())
	if got := ev.Message.Message.Body; got != body {
		t.Errorf("message event: body differs from the sent body (%d bytes vs %d) (4.4.9)", len(got), len(body))
	}
	// Every line the adapter wrote was ready or this message, whole.
	t.Sleep(500 * time.Millisecond)
	for _, e := range w.Events() {
		switch e.Kind {
		case protocol.EventReady, protocol.EventStatus:
		case protocol.EventMessage:
			if e.Message == nil || e.Message.Message.MessageID != m || e.Message.Message.Body != body {
				t.Errorf("message watch: a `message` event that is not the sent message, whole (4.4.9)")
			}
		default:
			t.Errorf("message watch: unexpected `%s` event after a multi-line body (4.4.9)", e.Kind)
		}
	}
}
