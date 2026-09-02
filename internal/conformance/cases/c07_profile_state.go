package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c07ProfileState: `describe.profile.state` on a scratch principal walks
// `unconfigured` → `joined` (team create) → `not_member` (team leave) →
// `joined` again (team join), every describe run with
// BRIGADE_TEST_OFFLINE=1 so an adapter that touches the network to answer
// it fails loudly (4.2, 4.4.1). The T API offers no way to run the
// --setup hook on a scratch principal, so an adapter without team.create
// is skipped here; the leave/rejoin legs need team.join.
func c07ProfileState() conformance.Case {
	return conformance.Case{
		ID:    "C-07",
		Rule:  "4.4.1 profile.state offline",
		Title: "describe.profile.state walks unconfigured → joined → not_member → joined from local files only",
		Tags:  []string{conformance.TagCore},
		Run:   runC07,
	}
}

func runC07(t *conformance.T) {
	p := t.Scratch("c07")
	expectState(t, p, "before any setup", protocol.ProfileStateUnconfigured)
	if !t.HasCap("team.create") {
		// Without team.create the operator's --setup binds the scratch
		// (brief 7: "run --setup on the scratch → joined"); with neither
		// there is no way to bind it and the case is skipped.
		if !t.Setup(p) {
			t.Skip("needs team.create or --setup to bind a scratch principal")
		}
		d := expectState(t, p, "after --setup", protocol.ProfileStateJoined)
		if d.Profile.TeamRef == "" || d.Profile.PrincipalRef == "" {
			t.Errorf("describe after --setup: joined without team_ref or principal_ref (4.4.1)")
		}
		t.Note("team.create not advertised: the not_member and rejoin legs were not run")
		return
	}
	created := createTeam(t, p, "c07-"+t.RunID(), "c07@example.com")
	d := expectState(t, p, "after team create", protocol.ProfileStateJoined)
	if d.Profile.TeamRef != created.TeamRef || d.Profile.PrincipalRef != created.PrincipalRef {
		t.Errorf("describe after team create: team_ref or principal_ref differ from the team create result (4.4.1)")
	}
	if !t.HasCap("team.join") {
		t.Note("team.join not advertised: the not_member and rejoin legs were not run")
		return
	}
	var left protocol.TeamLeaveResult
	t.OK(t.Exec(p, nil, "team", "leave"), &left)
	d = expectState(t, p, "after team leave", protocol.ProfileStateNotMember)
	if d.Profile.TeamRef != "" || d.Profile.TeamName != "" || d.Profile.PrincipalRef != "" || d.Profile.HumanLabel != "" {
		t.Errorf("describe after team leave: profile carries team members while not_member (4.4.1)")
	}
	res := joinTeam(t, p, created.JoinSecret, "c07@example.com")
	if !res.Rejoined || res.PrincipalRef != created.PrincipalRef {
		t.Errorf("team join after leave: rejoined %v principal_ref unchanged %v; want true, true (4.4.10)", res.Rejoined, res.PrincipalRef == created.PrincipalRef)
	}
	expectState(t, p, "after team join", protocol.ProfileStateJoined)
}

// expectState runs an offline describe and records a failure unless
// profile.state is want.
func expectState(t *conformance.T, p *conformance.Principal, when, want string) *protocol.DescribeResult {
	d := describe(t, p, true)
	if d.Profile.State != want {
		t.Errorf("describe %s: profile.state %q, want %q (4.4.1)", when, d.Profile.State, want)
	}
	return d
}
