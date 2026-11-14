package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c55TeamCloseMove (cap team.take_in): on a team the case took in itself,
// O its owner and M a member. With no move open, O's `team close-move`
// answers {team_ref, team_name, closed false}. M, who is not the owner, is
// `unauthorized`, with the bytes its `team open-move` is refused with; a
// principal of another team whose profile names this team is
// `unauthorized` too, byte-identical to its answer for a team that does
// not exist. O opens a move and a principal comes in with its ticket; M's
// close-move is still `unauthorized` and closes nothing, because O's then
// answers closed true. From then on the ticket admits nobody, the member
// it let in is still a member, and a second close-move answers closed
// false. Where team.admin is advertised, a rotation of the join secret
// closes a move that is open: its ticket admits nobody and close-move
// answers closed false (4.4.10).
func c55TeamCloseMove() conformance.Case {
	return conformance.Case{
		ID:    "C-55",
		Rule:  "4.4.10 team close-move",
		Title: "the owner alone closes the team's move; its ticket then admits nobody, every member it let in stays one, and a team with no move has nothing to close",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.take_in")},
		Run:   runC55,
	}
}

// closedIs asserts one answer of `team close-move`: the team, its name and
// closed, and no other member.
func closedIs(t *conformance.T, team *takenTeam, what string, r *conformance.Result, want bool) {
	var res protocol.TeamCloseMoveResult
	t.OK(r, &res)
	if res.TeamRef != team.teamRef || res.TeamName != team.name || res.Closed != want {
		t.Errorf("%s: {team_ref match %v, team_name match %v, closed %v}; want the team's own and %v (4.4.10)",
			what, res.TeamRef == team.teamRef, res.TeamName == team.name, res.Closed, want)
	}
	raw := t.OKRaw(r)
	for _, key := range []string{"team_ref", "team_name", "closed"} {
		if _, has := raw[key]; !has {
			t.Errorf("%s: result has no `%s`; all three members are required (4.4.10)", what, key)
		}
	}
}

func runC55(t *conformance.T) {
	team := takenInTeam(t, "c55")
	o, m := team.owner, team.member
	run := func(p *conformance.Principal) *conformance.Result { return t.Exec(p, nil, "team", "close-move") }
	// Whatever the case finds, it leaves no move open behind it.
	defer func() { _ = run(o) }()

	// ---- a team with no move has nothing to close ----
	closedIs(t, team, "team close-move with no move open", run(o), false)

	// ---- the owner alone ----
	notOwner := run(m)
	t.Fail(notOwner, protocol.CodeUnauthorized)
	unwritten := t.SecretFile("unwritten")
	notOwnerOpen := openMove(t, m, "--ticket-file", unwritten)
	t.Fail(notOwnerOpen, protocol.CodeUnauthorized)
	t.SameBytes("a member that is not the owner: team close-move vs team open-move (4.4.10)", notOwner, notOwnerOpen)
	// A principal of another team, bound by hand to this one, and to a team
	// that does not exist: one refusal.
	foreigner := t.Scratch("foreigner")
	takeIn(t, foreigner, syntheticSecret(elsewhereRef()), "c55-foreign-"+t.RunID(), "")
	t.Rebind(foreigner, team.teamRef)
	defer t.Restore(foreigner)
	foreign := run(foreigner)
	t.Fail(foreign, protocol.CodeUnauthorized)
	t.Rebind(foreigner, strangerLike(team.teamRef))
	unknown := run(foreigner)
	t.Fail(unknown, protocol.CodeUnauthorized)
	t.SameBytes("team close-move for a team the caller is not a member of vs a team that does not exist (4.5.7)", foreign, unknown)
	t.DifferentBytes("unauthorized vs ok (positive control)", foreign, run(o))

	// ---- an open move is closed by its owner, and by nobody else ----
	path := t.SecretFile("ticket")
	var opened protocol.TeamOpenMoveResult
	t.OK(openMove(t, o, "--ticket-file", path), &opened)
	ticket, _ := t.TakeSecretFile(path)
	f := t.Scratch("follower")
	if in := takeIn(t, f, ticket, team.name, "c55-follower@example.com"); in.TakenIn || in.Owner || in.TeamRef != team.teamRef {
		t.Errorf("team take-in with the move ticket: {team_ref match %v, taken_in %v, owner %v}; want the team, false and false (4.4.10)",
			in.TeamRef == team.teamRef, in.TakenIn, in.Owner)
	}
	// Neither the member the secret let in nor the one the ticket let in closes it.
	for _, p := range []*conformance.Principal{m, f} {
		t.Fail(run(p), protocol.CodeUnauthorized)
	}
	closedIs(t, team, "team close-move of an open move", run(o), true)
	ticketRefused(t, team, "the ticket of a move that was closed", "late", ticket)
	t.List(f, "", false)
	closedIs(t, team, "team close-move a second time", run(o), false)

	// ---- a rotation of the join secret closes a move ----
	if !t.HasCap("team.admin") {
		return
	}
	again := t.SecretFile("again")
	t.OK(openMove(t, o, "--ticket-file", again), &opened)
	if opened.Replaced {
		t.Errorf("team open-move after a close: replaced true; a move that was closed is not there to replace (4.4.10)")
	}
	current, _ := t.TakeSecretFile(again)
	rotated := t.SecretFile("rotated")
	var rotation protocol.TeamRotateSecretResult
	t.OK(t.Exec(o, nil, "team", "rotate-secret", "--secret-file", rotated), &rotation)
	next, _ := t.TakeSecretFile(rotated)
	ticketRefused(t, team, "the ticket of a move a rotation closed", "after-rotation", current)
	closedIs(t, team, "team close-move after a rotation", run(o), false)
	// The new secret is the team's now: a take-in with it is a join.
	if in := takeIn(t, t.Scratch("newcomer"), next, team.name, ""); in.TakenIn || in.TeamRef != team.teamRef {
		t.Errorf("team take-in with the rotated secret: {team_ref match %v, taken_in %v}; want the team and false (4.4.10)",
			in.TeamRef == team.teamRef, in.TakenIn)
	}
}
