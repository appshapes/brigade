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
// strictly later and the list shows the new name (4.4.4).
func c13Heartbeat() conformance.Case {
	return conformance.Case{
		ID:    "C-13",
		Rule:  "4.5.7 heartbeat ownership and renewal",
		Title: "heartbeat on another principal's session ≡ unknown id (not_found); own heartbeat renews lease_until and renames",
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
}
