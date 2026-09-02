package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c26TeamIsolation: C's `session list` holds no T1 session; C's
// `message receive` on B's session is `not_found`; with C's profile
// rebound to T1's team_ref, C's `session register` and `session list` are
// `unauthorized` with stdout byte-identical to the same commands against a
// random team_ref (no team-existence oracle, 4.4.3, 4.5.6, 4.5.7). The
// positive control is unauthorized vs not_found. Restore is deferred.
func c26TeamIsolation() conformance.Case {
	return conformance.Case{
		ID:    "C-26",
		Rule:  "4.5.6 team isolation",
		Title: "C lists no T1 session; C's receive on B's session → not_found; C rebound to T1 ≡ rebound to a random team (unauthorized)",
		Tags:  []string{conformance.TagCore},
		Run:   runC26,
	}
}

func runC26(t *conformance.T) {
	a, b, c := t.A(), t.B(), t.C()
	sessions, raw := t.List(c, "", true)
	if teamRef, _ := raw["team_ref"].(string); teamRef == a.TeamRef {
		t.Errorf("C session list: team_ref is T1's (4.5.6)")
	}
	for _, s := range sessions {
		if s.SessionID == t.Session(a) || s.SessionID == t.Session(b) || s.PrincipalRef == a.PrincipalRef || s.PrincipalRef == b.PrincipalRef {
			t.Errorf("C session list: T1 session %s is listed (4.5.6, C-26)", s.SessionID)
		}
	}
	onB := t.Exec(c, nil, "message", "receive", "--session", t.Session(b))
	t.Fail(onB, protocol.CodeNotFound)

	defer t.Restore(c)
	t.Rebind(c, a.TeamRef)
	regT1 := t.Exec(c, registration(t, "c26-t1-"+t.RunID(), nil), "session", "register")
	t.Fail(regT1, protocol.CodeUnauthorized)
	listT1 := t.Exec(c, nil, "session", "list")
	t.Fail(listT1, protocol.CodeUnauthorized)

	t.Rebind(c, randomRef())
	regRandom := t.Exec(c, registration(t, "c26-random-"+t.RunID(), nil), "session", "register")
	t.Fail(regRandom, protocol.CodeUnauthorized)
	listRandom := t.Exec(c, nil, "session", "list")
	t.Fail(listRandom, protocol.CodeUnauthorized)

	t.SameBytes("session register rebound to T1 vs a random team (4.5.7)", regT1, regRandom)
	t.SameBytes("session list rebound to T1 vs a random team (4.5.7)", listT1, listRandom)
	t.DifferentBytes("unauthorized vs not_found (positive control)", listT1, onB)

	t.Restore(c)
	if st := describe(t, c, false).Profile.State; st != protocol.ProfileStateJoined {
		t.Errorf("describe after Restore: profile.state %q, want joined", st)
	}
}
