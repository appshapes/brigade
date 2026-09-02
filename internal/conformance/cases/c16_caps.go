package cases

import (
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c16Caps: a session_name of 65 code points made of 2-byte runes is
// `invalid_input` naming `session_name`, while 64 such code points pass
// (so an adapter counting bytes fails); a session_description of 257
// code points is `invalid_input` naming `session_description`, 256 pass
// (4.3.1, 4.4.2, 4.5.11).
func c16Caps() conformance.Case {
	return conformance.Case{
		ID:    "C-16",
		Rule:  "4.5.11 name and description caps",
		Title: "session_name 65 code points → invalid_input, 64 pass; session_description 257 → invalid_input, 256 pass",
		Tags:  []string{conformance.TagCore},
		Run:   runC16,
	}
}

func runC16(t *conformance.T) {
	a := t.A()
	limits := t.Describe().Limits
	over := strings.Repeat("é", limits.MaxSessionNameCodepoints+1)
	e := t.Fail(t.Exec(a, registration(t, over, nil), "session", "register"), protocol.CodeInvalidInput)
	if e.Details["field"] != "session_name" {
		t.Errorf("session_name over the cap: details.field %q, want session_name (4.3.1)", e.Details["field"])
	}
	atCap := strings.Repeat("é", limits.MaxSessionNameCodepoints)
	record, _ := t.Register(a, atCap, nil)
	if got, _ := record["session_name"].(string); got != atCap {
		t.Errorf("session_name at the cap: the record's session_name differs from the one registered (4.4.3)")
	}

	overDesc := strings.Repeat("d", limits.MaxDescriptionChars+1)
	e = t.Fail(t.Exec(a, registration(t, "c16-desc-"+t.RunID(), &overDesc), "session", "register"), protocol.CodeInvalidInput)
	if e.Details["field"] != "session_description" {
		t.Errorf("session_description over the cap: details.field %q, want session_description (4.3.1)", e.Details["field"])
	}
	atCapDesc := strings.Repeat("d", limits.MaxDescriptionChars)
	t.Register(a, "c16-desc-ok-"+t.RunID(), func(r *protocol.SessionRegistration) { r.SessionDescription = &atCapDesc })
}
