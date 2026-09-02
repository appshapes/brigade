package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c33WatchReady: `message watch` on an empty inbox emits exactly one `ready`
// line first — protocol_version "1", the session id, `mode` push or polling
// with polling exactly when message.watch.push is not advertised (4.4.9,
// 4.7) — then nothing; every stdout line is a JSON object with an `event`
// (the WatchProc reader records any other line as a C-33 failure); stdin
// EOF ends the process with exit 0.
func c33WatchReady() conformance.Case {
	return conformance.Case{
		ID:    "C-33",
		Rule:  "4.4.9 ready first",
		Title: "watch emits one ready line first with the right mode, then nothing for an empty inbox; NDJSON only",
		Tags:  []string{conformance.TagCore},
		Run:   runC33,
	}
}

func runC33(t *conformance.T) {
	b := t.B()
	_, sb := t.Register(b, nameFor(t, "C-33", "watched"), nil)
	w := t.Watch(b, sb)

	ev, ok := w.Next(t.PushDeadline())
	if !ok {
		t.Fatalf("message watch: no event within %s; want `ready` first (4.4.9)", t.PushDeadline())
	}
	if ev.Kind != protocol.EventReady {
		t.Fatalf("message watch: first event is `%s`, want `ready` (4.4.9)", ev.Kind)
	}
	if ev.Ready == nil {
		t.Fatalf("message watch: the ready event did not validate (4.4.9)")
	}
	if ev.Ready.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("ready: protocol_version %q, want %q", ev.Ready.ProtocolVersion, protocol.ProtocolVersion)
	}
	if ev.Ready.SessionID != sb {
		t.Errorf("ready: session_id %q, want the watched session %q", ev.Ready.SessionID, sb)
	}
	push := t.HasCap("message.watch.push")
	switch ev.Ready.Mode {
	case protocol.WatchModePush:
		if !push {
			t.Errorf("ready: mode push but message.watch.push is not advertised; want polling (4.7)")
		}
	case protocol.WatchModePolling:
		if push {
			t.Errorf("ready: mode polling but message.watch.push is advertised; want push (4.7)")
		}
	default:
		t.Errorf("ready: mode %q, want push or polling (4.4.9)", ev.Ready.Mode)
	}

	w.ExpectNone(time.Second)
	w.CloseStdin()
	exit, exited := w.Wait(5 * time.Second)
	if !exited {
		t.Errorf("message watch: still running 5 s after stdin EOF (4.4.9)")
	} else if exit != 0 {
		t.Errorf("message watch: exit %d after stdin EOF, want 0 (4.4.9)", exit)
	}
	readies := 0
	for _, e := range w.Events() {
		if e.Kind == protocol.EventReady {
			readies++
		}
	}
	if readies != 1 {
		t.Errorf("message watch: %d ready events, want exactly one (4.4.9)", readies)
	}
}
