package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c43Roster (cap team.roster): `team members` (4.2, 4.4.10) lists every
// active member of the profile's team and no other: A's roster holds A and
// B, each with human_label, status, joined_at, a non-null last_seen_at
// (both have fixture sessions) and session_count ≥ 1, never C; C's roster
// holds only C. With C's profile rebound to T1 the command is unauthorized
// with bytes identical to a random team_ref (no team-existence oracle,
// 4.5.7); the positive control is C's not_found from a receive on an
// unknown session, taken before the rebind.
func c43Roster() conformance.Case {
	return conformance.Case{
		ID:    "C-43",
		Rule:  "4.2 team members",
		Title: "the roster lists the team's members with their fields and never another team's; a non-member is unauthorized without an oracle",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.roster")},
		Run:   runC43,
	}
}

func runC43(t *conformance.T) {
	a, b, c := t.A(), t.B(), t.C()

	members := rosterOf(t, t.Exec(a, nil, "team", "members"))
	seen := map[string]bool{}
	for _, m := range members {
		ref, _ := m["principal_ref"].(string)
		seen[ref] = true
		if ref == c.PrincipalRef {
			t.Errorf("team members for A: lists C's principal, a member of another team (4.5.6)")
		}
		if ref == a.PrincipalRef || ref == b.PrincipalRef {
			checkMember(t, m, ref)
		}
	}
	if !seen[a.PrincipalRef] || !seen[b.PrincipalRef] {
		t.Errorf("team members for A: A listed %v, B listed %v; want both (4.4.10)", seen[a.PrincipalRef], seen[b.PrincipalRef])
	}

	cMembers := rosterOf(t, t.Exec(c, nil, "team", "members"))
	if len(cMembers) != 1 {
		t.Errorf("team members for C: %d members, want exactly C (4.4.10)", len(cMembers))
	}
	for _, m := range cMembers {
		if ref, _ := m["principal_ref"].(string); ref != c.PrincipalRef {
			t.Errorf("team members for C: lists a principal other than C (4.5.6)")
		}
	}

	notFound := t.Exec(c, nil, "message", "receive", "--session", randomRef())
	t.Fail(notFound, protocol.CodeNotFound)

	defer t.Restore(c)
	t.Rebind(c, a.TeamRef)
	rebound := t.Exec(c, nil, "team", "members")
	t.Fail(rebound, protocol.CodeUnauthorized)
	t.Rebind(c, "team-"+randomRef())
	random := t.Exec(c, nil, "team", "members")
	t.Fail(random, protocol.CodeUnauthorized)
	t.SameBytes("team members as a non-member vs against a random team_ref", rebound, random)
	t.DifferentBytes("unauthorized vs not_found", rebound, notFound)
}

// rosterOf asserts a `team members` result — team_ref, team_name,
// server_time and the members array present — and returns the members.
func rosterOf(t *conformance.T, r *conformance.Result) []map[string]any {
	raw := t.OKRaw(r)
	for _, key := range []string{"team_ref", "team_name", "server_time"} {
		if s, _ := raw[key].(string); s == "" {
			t.Errorf("team members: `%s` absent or empty (4.4.10)", key)
		}
	}
	v, present := raw["members"]
	if !present {
		t.Errorf("team members: `members` absent; a required array is always present (JSON convention 3)")
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("team members: `members` is not an array")
	}
	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("team members: members[%d] is not an object", i)
		}
		out = append(out, m)
	}
	return out
}

// checkMember asserts the fields of one roster entry for a principal that
// has a registered session.
func checkMember(t *conformance.T, m map[string]any, ref string) {
	if s, _ := m["human_label"].(string); s == "" {
		t.Errorf("team members: %s has no human_label (4.4.10)", ref)
	}
	if s, _ := m["status"].(string); s == "" {
		t.Errorf("team members: %s has no status (4.4.10)", ref)
	}
	for _, key := range []string{"joined_at", "last_seen_at"} {
		s, isString := m[key].(string)
		if !isString {
			t.Errorf("team members: %s: %s is %v, want a timestamp (4.4.10; last_seen_at is non-null for a member with a session)", ref, key, m[key])
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			t.Errorf("team members: %s: %s does not parse as RFC 3339", ref, key)
		}
	}
	if n, isNumber := m["session_count"].(float64); !isNumber || n < 1 {
		t.Errorf("team members: %s: session_count %v, want a number ≥ 1 (4.4.10)", ref, m["session_count"])
	}
}
