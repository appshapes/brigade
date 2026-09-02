package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
)

// c11Names: two registrations with one session_name are two sessions —
// different ids, both listed (4.5.8).
func c11Names() conformance.Case {
	return conformance.Case{
		ID:    "C-11",
		Rule:  "4.5.8 same name, two sessions",
		Title: "two registrations with one name get different ids and are both listed",
		Tags:  []string{conformance.TagCore},
		Run:   runC11,
	}
}

func runC11(t *conformance.T) {
	a := t.A()
	name := "c11-dup-" + t.RunID()
	_, first := t.Register(a, name, nil)
	_, second := t.Register(a, name, nil)
	if first == second {
		t.Errorf("session register: two registrations named %q share one session_id (4.5.8)", name)
	}
	sessions, _ := t.List(a, "", false)
	seen := map[string]bool{}
	for _, s := range sessions {
		seen[s.SessionID] = true
	}
	if !seen[first] || !seen[second] {
		t.Errorf("session list: both sessions named %q must be listed (first %v, second %v)", name, seen[first], seen[second])
	}
}
