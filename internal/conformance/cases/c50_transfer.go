package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c50Transfer (cap team.admin): on a team of the case's own, O its owner,
// M a member and N a member O has revoked. A transfer to a principal_ref
// of the backend's own shape that names nobody, and one to N, which is not
// an active member, are `invalid_input` naming principal_ref; O naming
// itself is accepted, answers {team_ref, principal_ref, transferred true}
// and changes nothing; M's `team transfer`, M not being the owner, is
// `unauthorized`; O's transfer to M answers M's principal_ref; the same
// `team transfer` that was refused to M is now refused to O, byte for
// byte, while O is still a member and its `session list` works; and it is
// now answered to M, the owner (4.4.10).
func c50Transfer() conformance.Case {
	return conformance.Case{
		ID:    "C-50",
		Rule:  "4.4.10 team transfer",
		Title: "the owner makes an active member the owner; the previous owner stays a member and is refused its next administration command",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.admin")},
		Run:   runC50,
	}
}

func runC50(t *conformance.T) {
	o, m, created := scratchTeam(t, "c50")
	teamRef := created.TeamRef
	transfer := func(p *conformance.Principal, stdin *protocol.TeamTransferRequest, args ...string) *conformance.Result {
		var doc []byte
		if stdin != nil {
			doc = mustMarshal(t, stdin)
		}
		return t.Exec(p, doc, append([]string{"team", "transfer"}, args...)...)
	}
	moved := func(what string, r *conformance.Result, ref string) {
		var res protocol.TeamTransferResult
		t.OK(r, &res)
		if res.TeamRef != teamRef || res.PrincipalRef != ref || !res.Transferred {
			t.Errorf("%s: {team_ref match %v, principal_ref match %v, transferred %v}; want the team, the principal named and true (4.4.10)",
				what, res.TeamRef == teamRef, res.PrincipalRef == ref, res.Transferred)
		}
	}
	notAMember := func(what string, r *conformance.Result) {
		if e := t.Fail(r, protocol.CodeInvalidInput); e.Details["field"] != "principal_ref" {
			t.Errorf("%s: details.field %q, want principal_ref (4.4.10)", what, e.Details["field"])
		}
	}
	// toM is the one command both sides of the comparison below run: 4.4.10
	// gives two callers the same bytes, and says nothing of two commands.
	toM := func(p *conformance.Principal) *conformance.Result {
		return transfer(p, nil, "--principal", m.PrincipalRef)
	}

	// ---- a principal that is not an active member cannot be the owner ----
	stranger := transfer(o, nil, "--principal", strangerLike(o.PrincipalRef))
	notAMember("team transfer to a principal that never was a member", stranger)
	n := t.Scratch("n")
	joinTeam(t, n, created.JoinSecret, "c50-n@example.com")
	var revoked protocol.TeamRevokeMemberResult
	t.OK(t.Exec(o, nil, "team", "revoke-member", "--principal", n.PrincipalRef), &revoked)
	if !revoked.Changed {
		t.Errorf("team revoke-member: changed false for an active member (4.4.10)")
	}
	notAMember("team transfer to a member that was revoked", transfer(o, nil, "--principal", n.PrincipalRef))

	// ---- the owner alone transfers ----
	moved("team transfer by the owner to itself", transfer(o, nil, "--principal", o.PrincipalRef), o.PrincipalRef)
	notOwner := toM(m)
	t.Fail(notOwner, protocol.CodeUnauthorized)
	moved("team transfer", transfer(o, &protocol.TeamTransferRequest{PrincipalRef: m.PrincipalRef}), m.PrincipalRef)

	// ---- the previous owner is a member like any other ----
	previous := toM(o)
	t.Fail(previous, protocol.CodeUnauthorized)
	t.SameBytes("team transfer by a member that never was the owner vs by the previous owner (4.4.10)", notOwner, previous)
	t.DifferentBytes("unauthorized vs invalid_input (positive control)", previous, stranger)
	t.List(o, "", false)

	// ---- and the new owner is answered what it was refused ----
	moved("team transfer by the new owner to itself", toM(m), m.PrincipalRef)
}
