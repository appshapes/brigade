package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c45HumanLabel (cap session.human_label): the registration's
// `human_label` is the harness's DEFAULT for its principal, and an adapter
// announcing the capability adopts it into the membership only when that
// membership has none (4.4.2, 4.7). A principal joined without a label
// registers with one: the session record and `team members` both show it.
// The same principal then registers with a DIFFERENT label: the first one
// stays, everywhere — this member never overwrites a label, which is what
// makes the fill safe for a member who chose one. A third registration
// carrying no label at all changes nothing.
//
// The case asserts storage and echo, never meaning: the label is
// harness-reported and unverified (4.5.11).
func c45HumanLabel() conformance.Case {
	return conformance.Case{
		ID:    "C-45",
		Rule:  "4.4.2 human_label",
		Title: "a registration's human_label fills an empty membership label and never overwrites one",
		Tags: []string{
			conformance.TagCore,
			conformance.TagCap("session.human_label"),
			conformance.TagCap("team.roster"),
		},
		Run: runC45,
	}
}

func runC45(t *conformance.T) {
	// A member with NO label: the state every membership created before
	// this member existed is in.
	p := t.JoinPrincipalLabelled("unlabelled", "")

	first := "c45-first@example.com"
	rec, _ := t.Register(p, nameFor(t, "C-45", "fill"), func(r *protocol.SessionRegistration) {
		r.HumanLabel = &first
	})
	if got, _ := rec["human_label"].(string); got != first {
		t.Errorf("session register into an unlabelled membership: human_label %q, want %q (4.4.2, cap session.human_label)", got, first)
	}
	checkRosterLabel(t, p, first, "after the first registration")

	// A second registration offering a DIFFERENT label: the adopted one
	// stands. An adapter that assigns unconditionally fails here.
	second := "c45-second@example.com"
	rec, _ = t.Register(p, nameFor(t, "C-45", "keep"), func(r *protocol.SessionRegistration) {
		r.HumanLabel = &second
	})
	if got, _ := rec["human_label"].(string); got != first {
		t.Errorf("session register with a different human_label: the record says %q, want the adopted %q kept (4.4.2: an existing label is never overwritten)", got, first)
	}
	checkRosterLabel(t, p, first, "after a registration offering a different label")

	// A third registration with no label at all: nothing changes.
	rec, _ = t.Register(p, nameFor(t, "C-45", "absent"), nil)
	if got, _ := rec["human_label"].(string); got != first {
		t.Errorf("session register without a human_label: the record says %q, want the adopted %q kept (4.4.2)", got, first)
	}
	checkRosterLabel(t, p, first, "after a registration carrying no label")
}

// checkRosterLabel asserts the principal's own `team members` entry
// carries exactly this label, so the adoption is visible where the roster
// is read and not only in the register result.
func checkRosterLabel(t *conformance.T, p *conformance.Principal, want, when string) {
	if !t.HasCap("team.roster") {
		return
	}
	for _, m := range rosterOf(t, t.Exec(p, nil, "team", "members")) {
		if ref, _ := m["principal_ref"].(string); ref != p.PrincipalRef {
			continue
		}
		if got, _ := m["human_label"].(string); got != want {
			t.Errorf("team members %s: human_label %q, want %q (4.4.10)", when, got, want)
		}
		return
	}
	t.Errorf("team members %s: the principal is not in its own roster (4.4.10)", when)
}
