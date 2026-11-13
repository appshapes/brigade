package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c49RevokeMember (cap team.admin): on a team of the case's own, O its
// owner and M a member.
//
// The requests: both forms in one stdin document, `ban` beside the version
// form and a version outside 0..2147483647 are `invalid_input`; the same
// three as flags are `usage`; O naming itself is `invalid_input` naming
// principal_ref; M, which is not the owner, is `unauthorized`; and a
// principal_ref of the backend's own shape that names nobody is no error,
// with changed false.
//
// The principal form: O revokes M, which answers {team_ref, principal_ref,
// status revoked, changed true, sessions_closed 1} for M's one open
// session; M's running watch emits an `error` `unauthorized` and exits 5;
// M's `team revoke-member` is `unauthorized` with the bytes the same
// command had while M was an active member that is not the owner; its
// `session list`, `session register`, `session heartbeat`, `message send`
// and `message receive` are `unauthorized` too; a repeat answers changed
// false; M rejoins with the secret, rejoined true and the same
// principal_ref. With ban the status is banned, and M's join with the
// correct secret is `unauthorized`, byte-identical to its join with a
// wrong one.
//
// The version form, with M still banned: after a rotation,
// joined_secret_version_lte 1 revokes a second member N, which joined with
// version 1, and neither O nor M, and answers {team_ref, max_version 1,
// revoked 1, sessions_closed 1}; M's join with the new secret is still the
// rejected secret, so the sweep left the ban where it was; a revocation
// without ban lifts it (changed true) and M rejoins with the new secret;
// `team members` then shows M at joined_secret_version 2 beside O at 1,
// without N; and a second sweep of version 1 revokes nobody (4.4.10).
func c49RevokeMember() conformance.Case {
	return conformance.Case{
		ID:    "C-49",
		Rule:  "4.4.10 team revoke-member",
		Title: "the owner revokes one member, bans and unbans it, and sweeps the members that joined with an old secret; a revoked member is refused everywhere and comes back with the secret",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.admin")},
		Run:   runC49,
	}
}

func runC49(t *conformance.T) {
	o, m, created := scratchTeam(t, "c49")
	teamRef, secret, principal := created.TeamRef, created.JoinSecret, m.PrincipalRef
	revoke := func(p *conformance.Principal, stdin any, args ...string) *conformance.Result {
		var doc []byte
		if stdin != nil {
			doc = mustMarshal(t, stdin)
		}
		return t.Exec(p, doc, append([]string{"team", "revoke-member"}, args...)...)
	}
	one := func(what string, r *conformance.Result, ref, status string, changed bool, closed int) {
		var res protocol.TeamRevokeMemberResult
		t.OK(r, &res)
		if res.TeamRef != teamRef || res.PrincipalRef != ref || res.Status != status || res.Changed != changed || res.SessionsClosed != closed {
			t.Errorf("%s: {team_ref match %v, principal_ref match %v, status %s, changed %v, sessions_closed %d}; want the request's refs, %s, %v and %d (4.4.10)",
				what, res.TeamRef == teamRef, res.PrincipalRef == ref, res.Status, res.Changed, res.SessionsClosed, status, changed, closed)
		}
	}
	sweep := func(what string, r *conformance.Result, version, revoked, closed int) {
		var res protocol.TeamRevokeByVersionResult
		t.OK(r, &res)
		if res.TeamRef != teamRef || res.MaxVersion != version || res.Revoked != revoked || res.SessionsClosed != closed {
			t.Errorf("%s: {team_ref match %v, max_version %d, revoked %d, sessions_closed %d}; want the team, %d, %d and %d (4.4.10)",
				what, res.TeamRef == teamRef, res.MaxVersion, res.Revoked, res.SessionsClosed, version, revoked, closed)
		}
	}

	// ---- the requests: refused where they are read, before the backend ----
	beyond := int64(protocol.MaxSecretVersion) + 1
	for _, doc := range []map[string]any{
		{"principal_ref": principal, "joined_secret_version_lte": 1},
		{"joined_secret_version_lte": 1, "ban": true},
		{"joined_secret_version_lte": -1},
		{"joined_secret_version_lte": beyond},
	} {
		t.Fail(revoke(o, doc), protocol.CodeInvalidInput)
	}
	for _, flags := range [][]string{
		{"--principal", principal, "--max-version", "1"},
		{"--ban", "--max-version", "1"},
		{"--max-version", "-1"},
		{"--max-version", "2147483648"},
	} {
		t.Fail(revoke(o, nil, flags...), protocol.CodeUsage)
	}
	self := revoke(o, nil, "--principal", o.PrincipalRef)
	if e := t.Fail(self, protocol.CodeInvalidInput); e.Details["field"] != "principal_ref" {
		t.Errorf("team revoke-member of the owner itself: details.field %q, want principal_ref (4.4.10)", e.Details["field"])
	}
	notOwner := revoke(m, nil, "--principal", o.PrincipalRef)
	t.Fail(notOwner, protocol.CodeUnauthorized)
	nobody := strangerLike(o.PrincipalRef)
	one("team revoke-member of a principal that never was a member", revoke(o, nil, "--principal", nobody), nobody, protocol.MembershipRevoked, false, 0)

	// ---- the principal form ----
	_, so := t.Register(o, nameFor(t, "C-49", "o"), nil)
	_, s := t.Register(m, nameFor(t, "C-49", "m"), nil)
	w := t.Watch(m, s)
	w.Expect(protocol.EventReady, t.PushDeadline())
	one("team revoke-member", revoke(o, nil, "--principal", principal), principal, protocol.MembershipRevoked, true, 1)
	watchEndsUnauthorized(t, w, "team revoke-member")

	// The same command, by the same principal, before and after: the two
	// callers 4.4.10 gives identical bytes.
	gone := revoke(m, nil, "--principal", o.PrincipalRef)
	t.Fail(gone, protocol.CodeUnauthorized)
	t.SameBytes("team revoke-member by a member that is not the owner vs by a revoked member (4.4.10)", notOwner, gone)
	t.DifferentBytes("unauthorized vs invalid_input (positive control)", gone, self)
	// A revoked member is refused as one that left, on every command that
	// touches the team, its sessions or their messages (4.5.7): the
	// membership is asked before the session, which is its own and closed.
	for _, refused := range []*conformance.Result{
		t.Exec(m, nil, "session", "list"),
		t.Exec(m, registration(t, nameFor(t, "C-49", "back"), nil), "session", "register"),
		t.Exec(m, []byte(`{}`), "session", "heartbeat", "--session", s),
		t.Exec(m, mustMarshal(t, &protocol.SendRequest{SenderSessionID: s, RecipientSessionID: so, Body: "c49"}), "message", "send"),
		t.Exec(m, nil, "message", "receive", "--session", s),
	} {
		t.Fail(refused, protocol.CodeUnauthorized)
	}

	one("second team revoke-member", revoke(o, map[string]any{"principal_ref": principal}), principal, protocol.MembershipRevoked, false, 0)
	rejoin := func(what, with string) {
		back := joinTeam(t, m, with, "c49-member@example.com")
		if back.TeamRef != teamRef || !back.Rejoined || back.PrincipalRef != principal {
			t.Errorf("%s: {team_ref match %v, rejoined %v, principal_ref match %v}; want the team, true and the same membership (4.4.10)",
				what, back.TeamRef == teamRef, back.Rejoined, back.PrincipalRef == principal)
		}
	}
	rejoin("team join after a revocation", secret)

	// ---- a ban ----
	one("team revoke-member with ban", revoke(o, map[string]any{"principal_ref": principal, "ban": true}), principal, protocol.MembershipBanned, true, 0)
	right := t.Exec(m, joinDocument(t, secret), "team", "join")
	t.Fail(right, protocol.CodeUnauthorized)
	wrong := t.Exec(m, joinDocument(t, syntheticSecret(teamRef)), "team", "join")
	t.Fail(wrong, protocol.CodeUnauthorized)
	t.SameBytes("a banned member's team join with the secret vs with a wrong secret (4.4.10)", right, wrong)

	// ---- the version form, with M banned across it ----
	n := t.Scratch("n")
	joinTeam(t, n, secret, "c49-n@example.com")
	t.Register(n, nameFor(t, "C-49", "n"), nil)
	path := t.SecretFile("rotated")
	t.OK(t.Exec(o, nil, "team", "rotate-secret", "--secret-file", path), &protocol.TeamRotateSecretResult{})
	next, _ := t.TakeSecretFile(path)

	sweep("team revoke-member by version", revoke(o, map[string]any{"joined_secret_version_lte": 1}), 1, 1, 1)
	// The sweep never bans and never unbans: the secret M presents now is
	// the team's own, and its join is the rejected secret still.
	still := t.Exec(m, joinDocument(t, next), "team", "join")
	t.Fail(still, protocol.CodeUnauthorized)
	t.SameBytes("a banned member's team join before the version sweep vs after it (4.4.10)", right, still)

	// ---- the ban is lifted ----
	one("team revoke-member of a banned member, without ban", revoke(o, nil, "--principal", principal), principal, protocol.MembershipRevoked, true, 0)
	rejoin("team join with the new secret after the ban was lifted", next)
	if t.HasCap("team.roster") {
		versions := map[string]any{}
		for _, row := range rosterOf(t, t.Exec(o, nil, "team", "members")) {
			ref, _ := row["principal_ref"].(string)
			versions[ref] = row["joined_secret_version"]
		}
		if len(versions) != 2 || versions[o.PrincipalRef] != float64(1) || versions[principal] != float64(2) {
			t.Errorf("team members after the sweep and the rejoin: %d members, joined_secret_version %v for the owner and %v for the member that came back; want 2 members, 1 and 2 (4.4.10)",
				len(versions), versions[o.PrincipalRef], versions[principal])
		}
		if _, listed := versions[n.PrincipalRef]; listed {
			t.Errorf("team members after the sweep: a member that joined with version 1 and did not come back is still listed (4.4.10)")
		}
	}
	sweep("second team revoke-member by version", revoke(o, nil, "--max-version", "1"), 1, 0, 0)
}
