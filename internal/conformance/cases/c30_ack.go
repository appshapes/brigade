package cases

import (
	"slices"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c30Ack: `message ack` is idempotent and never an error (4.5.3, 4.4.8): the
// sender acking a message addressed to another session gets it under
// `unknown` (an id the session does not own), the recipient's ack lists it
// under `acked`, a second identical ack answers identical bytes, and
// `message receive` afterwards returns nothing. Both arrays are present as
// `[]` when empty in every answer (JSON convention 3), checked on the raw
// keys. The positive control for the byte identity is the sender's ack,
// which must differ from the recipient's.
func c30Ack() conformance.Case {
	return conformance.Case{
		ID:    "C-30",
		Rule:  "4.5.3 ack idempotent",
		Title: "ack by a non-owner is unknown, by the owner acked, again identical; receive then empty; arrays always present",
		Tags:  []string{conformance.TagCore},
		Run:   runC30,
	}
}

func runC30(t *conformance.T) {
	a, b := t.A(), t.B()
	_, sa := t.Register(a, nameFor(t, "C-30", "sender"), nil)
	_, sb := t.Register(b, nameFor(t, "C-30", "recipient"), nil)
	m := t.Send(a, sa, sb, "ack me", nil).MessageID

	// A's own session does not own m: unknown, never an error.
	ra := ackRaw(t, a, sa, m)
	acked, unknown := ackArrays(t, ra)
	if len(acked) != 0 {
		t.Errorf("message ack by the sender's session: acked %v, want [] (4.5.3)", acked)
	}
	if !slices.Contains(unknown, m) {
		t.Errorf("message ack by the sender's session: unknown %v does not contain the message id (4.5.3)", unknown)
	}

	rb1 := ackRaw(t, b, sb, m)
	acked, unknown = ackArrays(t, rb1)
	if !slices.Equal(acked, []string{m}) {
		t.Errorf("message ack by the recipient: acked %v, want [%s] (4.5.3)", acked, m)
	}
	if len(unknown) != 0 {
		t.Errorf("message ack by the recipient: unknown %v, want [] (4.5.3)", unknown)
	}

	rb2 := ackRaw(t, b, sb, m)
	acked, unknown = ackArrays(t, rb2)
	if !slices.Equal(acked, []string{m}) || len(unknown) != 0 {
		t.Errorf("second message ack by the recipient: acked %v, unknown %v; want [%s] and [] (4.5.3 idempotent)", acked, unknown, m)
	}
	t.SameBytes("second ack of an acknowledged id", rb1, rb2)
	t.DifferentBytes("sender's ack vs recipient's ack (positive control)", ra, rb1)

	if msgs := t.Receive(b, sb, 0); len(msgs) != 0 {
		t.Errorf("message receive after ack: %d messages, want none (4.5.3)", len(msgs))
	}
}

// ackRaw runs `message ack --session session` for one id and asserts a
// valid AckResult; the Result is returned for the raw-key and byte checks.
func ackRaw(t *conformance.T, p *conformance.Principal, session, id string) *conformance.Result {
	r := t.Exec(p, mustMarshal(t, &protocol.AckRequest{MessageIDs: []string{id}}), "message", "ack", "--session", session)
	var res protocol.AckResult
	t.OK(r, &res)
	return r
}

// ackArrays reads `acked` and `unknown` from the raw result, recording a
// failure when either KEY is absent (JSON convention 3: a required array is
// always present, `[]` when empty).
func ackArrays(t *conformance.T, r *conformance.Result) (acked, unknown []string) {
	raw := t.OKRaw(r)
	acked = rawStrings(t, "message ack", raw, "acked")
	unknown = rawStrings(t, "message ack", raw, "unknown")
	return acked, unknown
}

// rawStrings reads a required string array from a raw result object; an
// absent key is a failure and reads as empty.
func rawStrings(t *conformance.T, what string, raw map[string]any, key string) []string {
	v, present := raw[key]
	if !present {
		t.Errorf("%s: result has no `%s` member; a required array is always present, [] when empty (JSON convention 3)", what, key)
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		t.Errorf("%s: `%s` is not an array", what, key)
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}
