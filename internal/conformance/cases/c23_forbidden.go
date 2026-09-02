package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c23Forbidden: a SendRequest carrying any of the six adapter-stamped
// members — `sender`, `principal_ref`, `human_label`, `team_ref`,
// `created_at`, `hop_count` — with a value or with `null` is
// `invalid_input` with `details.field` naming that member, and nothing
// reaches the recipient's inbox (4.4.6, 4.5.5).
func c23Forbidden() conformance.Case {
	return conformance.Case{
		ID:    "C-23",
		Rule:  "4.5.5 forbidden sender members",
		Title: "a send carrying sender, principal_ref, human_label, team_ref, created_at or hop_count (any value or null) → invalid_input naming it",
		Tags:  []string{conformance.TagCore},
		Run:   runC23,
	}
}

func runC23(t *conformance.T) {
	a, b := t.A(), t.B()
	_, sa := t.Register(a, "c23-a-"+t.RunID(), nil)
	_, sb := t.Register(b, "c23-b-"+t.RunID(), nil)
	values := []struct {
		member string
		value  any
	}{
		{"sender", map[string]any{"principal_ref": "x", "session_id": sb, "session_name": "forged"}},
		{"principal_ref", "x"},
		{"human_label", "mallory@example.com"},
		{"team_ref", "x"},
		{"created_at", "2026-08-30T12:00:00Z"},
		{"hop_count", 1},
	}
	for _, v := range values {
		for _, value := range []any{v.value, nil} {
			doc := map[string]any{
				"sender_session_id":    sa,
				"recipient_session_id": sb,
				"body":                 "c23: forged " + v.member,
				v.member:               value,
			}
			e := t.Fail(t.SendRaw(a, mustMarshal(t, doc)), protocol.CodeInvalidInput)
			if e.Details["field"] != v.member {
				t.Errorf("message send with %s=%v: details.field %q, want %s (4.4.6)", v.member, value, e.Details["field"], v.member)
			}
		}
	}
	if n := len(t.Receive(b, sb, 0)); n != 0 {
		t.Errorf("message receive: %d messages reached B's inbox from the refused sends; want none (4.4.6)", n)
	}
}
