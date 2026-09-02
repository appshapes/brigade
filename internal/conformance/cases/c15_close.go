package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c15Close: `session close` answers {session_id, state offline}; a second
// close answers the identical result with exit 0 (idempotent); a
// heartbeat afterwards is `conflict` (4.2, 4.5.8).
func c15Close() conformance.Case {
	return conformance.Case{
		ID:    "C-15",
		Rule:  "4.5.8 close idempotent",
		Title: "session close → offline; second close identical; heartbeat after close → conflict",
		Tags:  []string{conformance.TagCore},
		Run:   runC15,
	}
}

func runC15(t *conformance.T) {
	a := t.A()
	_, id := t.Register(a, "c15-"+t.RunID(), nil)
	first := t.Close(a, id)
	result, _ := first.Raw["result"].(map[string]any)
	if got, _ := result["session_id"].(string); got != id {
		t.Errorf("session close: result.session_id %q, want %s (4.4.3)", got, id)
	}
	if got, _ := result["state"].(string); got != protocol.SessionStateOffline {
		t.Errorf("session close: result.state %q, want offline (4.4.3)", got)
	}
	second := t.Close(a, id)
	t.SameBytes("first vs second session close (4.5.8 idempotent)", first, second)
	hb := t.Exec(a, []byte("{}"), "session", "heartbeat", "--session", id)
	t.Fail(hb, protocol.CodeConflict)
	t.DifferentBytes("close result vs heartbeat conflict (positive control)", first, hb)
}
