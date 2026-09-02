package cases

import (
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c17Unknown: a registration carrying an unknown member (`future`) and a
// wrongly-cased one (`SESSION_NAME`) is accepted; neither is echoed in
// the result and `session_name` is the lowercase member's value (JSON
// convention 2, 4.5.13).
func c17Unknown() conformance.Case {
	return conformance.Case{
		ID:    "C-17",
		Rule:  "4.5.13 unknown members ignored",
		Title: "session register with unknown and wrongly-cased members is accepted and does not echo them",
		Tags:  []string{conformance.TagCore},
		Run:   runC17,
	}
}

func runC17(t *conformance.T) {
	name := "c17-" + t.RunID()
	doc := map[string]any{
		"harness":         conformance.Harness,
		"harness_version": buildinfo.String(),
		"session_name":    name,
		"activity":        protocol.ActivityBusy,
		"inbound":         protocol.InboundAccept,
		"future":          map[string]any{"x": 1},
		"SESSION_NAME":    "shout",
	}
	r := t.Exec(t.A(), mustMarshal(t, doc), "session", "register")
	result := t.OKRaw(r)
	if _, has := result["future"]; has {
		t.Errorf("session register: the unknown member `future` is echoed in the result (JSON convention 2)")
	}
	if _, has := result["SESSION_NAME"]; has {
		t.Errorf("session register: the wrongly-cased member `SESSION_NAME` is echoed in the result (JSON convention 2)")
	}
	if got, _ := result["session_name"].(string); got != name {
		t.Errorf("session register: session_name %q, want the lowercase member's value (JSON convention 1)", got)
	}
	var rec protocol.SessionRecord
	if err := protocol.Decode(r.Envelope.Result, &rec); err != nil {
		t.Errorf("session register: result does not validate as a SessionRecord: %v", err)
	}
}
