package cases

import (
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c41StdinCommands (cap message.watch.stdin_commands): the NDJSON commands
// of 4.4.9. After a `message` event: B-5, an unknown command type is
// ignored and the following `ack` is answered with an `acked` event whose
// `message_ids` is [m] and `unknown` is [] (both keys present, JSON
// convention 3); B-6, a stdin line over 1 MiB is dropped and the following
// `heartbeat` is answered with `heartbeat_ok` whose lease_until is later
// than the session's previous one; a restarted watch does not re-emit m
// (the stdin ack persisted, which is why mutant_noack fails this case);
// `close` closes the session and exits 0 within 5 s, and `session list
// --include-offline` then shows it offline.
func c41StdinCommands() conformance.Case {
	return conformance.Case{
		ID:    "C-41",
		Rule:  "4.4.9 stdin commands",
		Title: "ack, heartbeat and close on watch stdin are answered; unknown and over-long lines are ignored",
		Tags:  []string{conformance.TagCore, conformance.TagCap("message.watch.stdin_commands")},
		Run:   runC41,
	}
}

func runC41(t *conformance.T) {
	a, b := t.A(), t.B()
	_, watched := t.Register(a, nameFor(t, "C-41", "watched"), nil)
	_, sender := t.Register(b, nameFor(t, "C-41", "sender"), nil)
	sessions, _ := t.List(a, "", false)
	before := recordOf(t, sessions, watched)

	w := t.Watch(a, watched)
	w.Expect(protocol.EventReady, t.PushDeadline())
	m := t.Send(b, sender, watched, "ack me over stdin", nil).MessageID
	expectMessageID(t, w, m, t.PushDeadline())

	// B-5: an unknown type is ignored, never answered and never fatal.
	w.WriteStdin([]byte(`{"type":"frobnicate","x":1}` + "\n"))
	w.Command(protocol.WatchCommand{Type: protocol.CommandAck, MessageIDs: []string{m}})
	ev := expectAnswer(t, w, protocol.EventAcked, t.PushDeadline())
	if ev.Acked == nil {
		t.Fatalf("acked event did not validate (4.4.9)")
	}
	if len(ev.Acked.MessageIDs) != 1 || ev.Acked.MessageIDs[0] != m {
		t.Errorf("acked event: message_ids %v, want [%s] (4.4.9)", ev.Acked.MessageIDs, m)
	}
	if _, present := ev.Raw["message_ids"]; !present {
		t.Errorf("acked event: `message_ids` absent; a required array is always present (JSON convention 3)")
	}
	if u, present := ev.Raw["unknown"]; !present {
		t.Errorf("acked event: `unknown` absent; a required array is always present, [] when empty (JSON convention 3)")
	} else if items, ok := u.([]any); !ok || len(items) != 0 {
		t.Errorf("acked event: unknown %v, want [] (4.4.9)", u)
	}

	// B-6: a line over 1 MiB is dropped and reading continues with the next
	// line. Were it processed as an ack, an `acked` event naming the bogus
	// id would precede the heartbeat_ok.
	prefix, suffix := `{"type":"ack","message_ids":["`, `"]}`
	over := protocol.MaxLineBytes + 1 - len(prefix) - len(suffix)
	w.WriteStdin([]byte(prefix + strings.Repeat("x", over) + suffix + "\n"))
	idle := protocol.ActivityIdle
	w.Command(protocol.WatchCommand{Type: protocol.CommandHeartbeat, Activity: &idle})
	hb := expectAnswer(t, w, protocol.EventHeartbeatOK, t.PushDeadline())
	if hb.HeartbeatOK == nil {
		t.Fatalf("heartbeat_ok event did not validate (4.4.9)")
	}
	if hb.HeartbeatOK.SessionID != watched {
		t.Errorf("heartbeat_ok: session_id %q, want %q", hb.HeartbeatOK.SessionID, watched)
	}
	if !hb.HeartbeatOK.LeaseUntil.After(before.LeaseUntil) {
		t.Errorf("heartbeat_ok: lease_until %s is not later than the previous %s (4.4.4 renewal)", hb.HeartbeatOK.LeaseUntil, before.LeaseUntil)
	}

	// The stdin ack persisted (4.5.3): `message receive` no longer returns
	// m, and a restarted watch does not re-emit it. The quiet window here is
	// shorter than C-36's mandated PushDeadline: the receive check is the
	// deterministic half, and the whole fs run has a wall-time ceiling.
	w.CloseStdin()
	w.Wait(5 * time.Second)
	if hasMessage(t.Receive(a, watched, 0), m) {
		t.Errorf("message receive after the stdin ack: the acked message is still returned (4.5.3)")
	}
	w2 := t.Watch(a, watched)
	w2.Expect(protocol.EventReady, t.PushDeadline())
	w2.ExpectNone(2 * time.Second)

	w2.Command(protocol.WatchCommand{Type: protocol.CommandClose})
	if exit, exited := w2.Wait(5 * time.Second); !exited {
		t.Errorf("message watch: still running 5 s after a close command (4.4.9)")
	} else if exit != 0 {
		t.Errorf("message watch: exit %d after a close command, want 0 (4.4.9)", exit)
	}
	sessions, _ = t.List(a, "", true)
	if after := recordOf(t, sessions, watched); after.State != protocol.SessionStateOffline {
		t.Errorf("session list --include-offline after the close command: state %q, want offline (4.4.9)", after.State)
	}
}

// expectAnswer reads events until one of kind arrives. An `error` event or
// an `acked` event of another kind arriving first is a failure: the
// ignored lines of B-5/B-6 must produce nothing (4.4.9).
func expectAnswer(t *conformance.T, w *conformance.WatchProc, kind string, deadline time.Duration) conformance.Event {
	until := time.Now().Add(deadline)
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			t.Fatalf("message watch: no `%s` event within %s (4.4.9)", kind, deadline)
		}
		ev, ok := w.Next(remaining)
		if !ok {
			t.Fatalf("message watch: no `%s` event within %s (4.4.9)", kind, deadline)
		}
		if ev.Kind == kind {
			return ev
		}
		if ev.Kind == protocol.EventError || ev.Kind == protocol.EventAcked {
			t.Errorf("message watch: `%s` event before the `%s` answer; an unknown or over-long stdin line must be ignored (4.4.9, B-5, B-6)", ev.Kind, kind)
		}
	}
}

// recordOf finds the session record with id; its absence aborts the case.
func recordOf(t *conformance.T, sessions []protocol.SessionRecord, id string) *protocol.SessionRecord {
	for i := range sessions {
		if sessions[i].SessionID == id {
			return &sessions[i]
		}
	}
	t.Fatalf("session list: session %s is not listed", id)
	return nil
}
