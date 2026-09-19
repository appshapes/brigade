package cases

import (
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c13Heartbeat: B's heartbeat on A's session and on a random id are both
// `not_found` with byte-identical stdout (ownership, 4.5.7); A's
// heartbeat on its own closed session is `conflict` (the positive
// control); A's heartbeat with a new session_name renews `lease_until`
// strictly later and the list shows the new name (4.4.4). With
// session.description advertised, three more heartbeats on the same
// session: one carrying ONLY session_description applies it and leaves
// the name; one carrying only a name applies it and leaves the
// description (absent means unchanged, JSON convention 4); one carrying
// `""` is accepted and the list then shows `""` or no member at all —
// an empty description is a value like any other and a consumer presents
// it as none (4.4.4).
//
// The description arm is gated inside the body, not by a `cap:` tag: a
// case skips when any tag is missing, and the ownership and renewal
// assertions must keep running for an adapter without the capability.
func c13Heartbeat() conformance.Case {
	return conformance.Case{
		ID:    "C-13",
		Rule:  "4.5.7 heartbeat ownership and renewal",
		Title: "heartbeat on another principal's session ≡ unknown id (not_found); own heartbeat renews lease_until and renames; with session.description a description-only heartbeat applies it, a name-only one leaves it, and \"\" is accepted",
		Tags:  []string{conformance.TagCore},
		Run:   runC13,
	}
}

func runC13(t *conformance.T) {
	a, b := t.A(), t.B()
	empty := []byte("{}")
	onA := t.Exec(b, empty, "session", "heartbeat", "--session", t.Session(a))
	t.Fail(onA, protocol.CodeNotFound)
	onRandom := t.Exec(b, empty, "session", "heartbeat", "--session", randomRef())
	t.Fail(onRandom, protocol.CodeNotFound)
	t.SameBytes("heartbeat on A's session vs a random id (4.5.7)", onA, onRandom)

	_, closed := t.Register(a, "c13-closed-"+t.RunID(), nil)
	t.Close(a, closed)
	onClosed := t.Exec(a, empty, "session", "heartbeat", "--session", closed)
	t.Fail(onClosed, protocol.CodeConflict)
	t.DifferentBytes("not_found vs conflict (positive control)", onA, onClosed)

	record, id := t.Register(a, "c13-old-"+t.RunID(), nil)
	before, ok := rawTime(t, record, "lease_until")
	if !ok {
		return
	}
	// An adapter that stamps whole seconds could renew to the same
	// instant within the registration's second; give it a full second.
	if s, _ := record["lease_until"].(string); !strings.Contains(s, ".") {
		t.Sleep(1100 * time.Millisecond)
	}
	newName := "c13-new-" + t.RunID()
	hb := t.Heartbeat(a, id, &protocol.HeartbeatRequest{SessionName: &newName})
	if hb.SessionID != id {
		t.Errorf("session heartbeat: session_id %s, want %s (4.4.4)", hb.SessionID, id)
	}
	if !hb.LeaseUntil.After(before) {
		t.Errorf("session heartbeat: lease_until %s is not later than the registration's %s (4.4.4)", hb.LeaseUntil.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano))
	}
	sessions, _ := t.List(a, "", false)
	found := false
	for _, s := range sessions {
		if s.SessionID == id {
			found = true
			if s.SessionName != newName {
				t.Errorf("session list after heartbeat: session_name %q, want %q (4.4.4)", s.SessionName, newName)
			}
		}
	}
	if !found {
		t.Errorf("session list after heartbeat: the session is absent")
	}

	// The description arm (cap session.description). A heartbeat carrying
	// nothing but session_description, read back from `session list`, is
	// the path a harness publishing a session's description takes, so the
	// round trip is what the suite proves (4.4.4).
	if !t.HasCap("session.description") {
		return
	}
	described := "c13-doing-" + t.RunID()
	t.Heartbeat(a, id, &protocol.HeartbeatRequest{SessionDescription: &described})
	sessions, _ = t.List(a, "", false)
	rec := recordOf(t, sessions, id)
	checkDescription(t, "session list after a description-only heartbeat", rec, described)
	if rec.SessionName != newName {
		t.Errorf("session list after a description-only heartbeat: session_name %q, want %q unchanged (4.4.4: absent means unchanged)", rec.SessionName, newName)
	}

	// A name-only heartbeat leaves the description standing: absent means
	// unchanged applies member by member (JSON convention 4). An adapter
	// that assigns the description unconditionally clears it here.
	renamed := "c13-renamed-" + t.RunID()
	t.Heartbeat(a, id, &protocol.HeartbeatRequest{SessionName: &renamed})
	sessions, _ = t.List(a, "", false)
	rec = recordOf(t, sessions, id)
	if rec.SessionName != renamed {
		t.Errorf("session list after a name-only heartbeat: session_name %q, want %q (4.4.4)", rec.SessionName, renamed)
	}
	checkDescription(t, "session list after a name-only heartbeat", rec, described)

	// `""` is a value like any other: it replaces the stored one, and the
	// list afterwards shows `""` or omits the member — both are "none" to a
	// consumer (4.4.4). An adapter that refuses it, or treats it as
	// unchanged and keeps listing the old text, fails here.
	none := ""
	t.Heartbeat(a, id, &protocol.HeartbeatRequest{SessionDescription: &none})
	sessions, _ = t.List(a, "", false)
	rec = recordOf(t, sessions, id)
	if rec.SessionDescription != nil && *rec.SessionDescription != "" {
		t.Errorf("session list after a heartbeat with session_description \"\": session_description %q, want \"\" or absent (4.4.4: an empty description replaces the stored one)", *rec.SessionDescription)
	}
}
