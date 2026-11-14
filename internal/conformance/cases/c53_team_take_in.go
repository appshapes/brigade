package cases

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c53TeamTakeIn (cap team.take_in): on scratch principals, with a join
// secret the case makes for a team no backend holds. A request with no
// team_name, and one whose secret is no join secret, is `invalid_input`
// naming the member; a --secret-file that is not an absolute path is
// `usage`, and one that is not there is `config` with details.reason
// secret_file_missing; and none of them takes anything in, because the
// first take-in that follows answers taken_in true. That take-in, with the
// secret from --secret-file and the name from --name, answers {team_ref,
// team_name, principal_ref, taken_in true, owner true} with the team_ref
// inside the secret, binds the profile, and makes its caller the team's
// owner: `team close-move` answers it. The same profile again, with the
// request on stdin and another name, answers taken_in false, owner true
// and the name the team has. A second principal with the same secret is a
// member that does not own: taken_in false, owner false, and its `team
// close-move` is `unauthorized`. Another secret for the same team_ref is
// `conflict` with details.reason other_join_secret, for a stranger and for
// the owner alike, and binds nothing. A secret naming another team on a
// bound profile is `conflict` profile_bound. Where team.join is advertised,
// a `team join` with the secret joins the team that was taken in. The
// secret is on neither stdout nor stderr of any command (the launcher
// searches every spawn for it) (4.4.10).
func c53TeamTakeIn() conformance.Case {
	return conformance.Case{
		ID:    "C-53",
		Rule:  "4.4.10 team take-in",
		Title: "the holder of a team's join secret takes the team in under the team_ref inside it and owns it; the same secret again is a join, another secret for the team_ref a conflict",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.take_in")},
		Run:   runC53,
	}
}

// relativeSecretFileTakeIn is the --secret-file C-53 gives that is not an
// absolute path, which an adapter must refuse.
const relativeSecretFileTakeIn = "brigade-conformance-c53-relative"

// elsewhereRef returns a team_ref as a backend that names its teams with
// UUIDs makes one: a team that moves keeps its reference, so the reference
// a take-in carries is another backend's, in that backend's shape.
func elsewhereRef() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// takeInDocument is the `team take-in` stdin document, for the take-ins a
// case sends through Exec.
func takeInDocument(t *conformance.T, secret, name, label string) []byte {
	return mustMarshal(t, &protocol.TeamTakeInRequest{JoinSecret: secret, TeamName: name, HumanLabel: label})
}

// takeIn runs `team take-in` for p with the request on stdin and returns
// the validated result.
func takeIn(t *conformance.T, p *conformance.Principal, secret, name, label string) *protocol.TeamTakeInResult {
	var res protocol.TeamTakeInResult
	t.OK(t.Exec(p, takeInDocument(t, secret, name, label), "team", "take-in"), &res)
	p.PrincipalRef, p.TeamRef, p.TeamName = res.PrincipalRef, res.TeamRef, res.TeamName
	return &res
}

// A takenTeam is a team of a case's own that came in through `team
// take-in`: its owner, a member that joined with the same secret, and the
// secret, which the launcher knows.
type takenTeam struct {
	owner, member *conformance.Principal
	teamRef, name string
	secret        string
}

// takenInTeam makes a team of its own for a case that opens or closes a
// move (C-54, C-55), with `team take-in` alone, so the case needs no other
// optional capability: a scratch principal takes in a team no backend
// holds, and a second one comes in with the same secret.
func takenInTeam(t *conformance.T, prefix string) *takenTeam {
	team := &takenTeam{teamRef: elsewhereRef(), name: prefix + "-" + t.RunID()}
	team.secret = syntheticSecret(team.teamRef)
	// The file is how the suite tells the launcher of a secret it made
	// itself: every later spawn's stdout and stderr are searched for it.
	t.GiveSecretFile("join-secret", team.secret)
	team.owner = t.Scratch("owner")
	if taken := takeIn(t, team.owner, team.secret, team.name, prefix+"-owner@example.com"); !taken.TakenIn || !taken.Owner || taken.TeamRef != team.teamRef {
		t.Fatalf("team take-in: {team_ref match %v, taken_in %v, owner %v}; want the team_ref inside the secret, true and true (4.4.10)",
			taken.TeamRef == team.teamRef, taken.TakenIn, taken.Owner)
	}
	team.member = t.Scratch("member")
	if joined := takeIn(t, team.member, team.secret, team.name, prefix+"-member@example.com"); joined.TakenIn || joined.Owner || joined.TeamRef != team.teamRef {
		t.Fatalf("team take-in by a second principal: {team_ref match %v, taken_in %v, owner %v}; want the team, false and false (4.4.10)",
			joined.TeamRef == team.teamRef, joined.TakenIn, joined.Owner)
	}
	return team
}

func runC53(t *conformance.T) {
	teamRef := elsewhereRef()
	secret := syntheticSecret(teamRef)
	file := t.GiveSecretFile("join-secret", secret)
	name := "c53-" + t.RunID()
	o := t.Scratch("owner")
	run := func(p *conformance.Principal, stdin []byte, args ...string) *conformance.Result {
		return t.Exec(p, stdin, append([]string{"team", "take-in"}, args...)...)
	}

	// ---- what is refused before anything is taken in ----
	noName := run(o, mustMarshal(t, map[string]string{"join_secret": secret}))
	if e := t.Fail(noName, protocol.CodeInvalidInput); e.Details["field"] != "team_name" {
		t.Errorf("team take-in with no team_name: details.field %q, want team_name (4.4.10)", e.Details["field"])
	}
	if e := t.Fail(run(o, takeInDocument(t, "not-a-join-secret", name, "")), protocol.CodeInvalidInput); e.Details["field"] != "join_secret" {
		t.Errorf("team take-in with a secret that is no join secret: details.field %q, want join_secret (4.4.10)", e.Details["field"])
	}
	t.Fail(run(o, nil, "--secret-file", relativeSecretFileTakeIn, "--name", name), protocol.CodeUsage)
	missing := filepath.Join(filepath.Dir(file), "c53-no-such-secret")
	if e := t.Fail(run(o, nil, "--secret-file", missing, "--name", name), protocol.CodeConfig); e.Details["reason"] != "secret_file_missing" {
		t.Errorf("team take-in --secret-file with a file that is not there: details.reason %q, want secret_file_missing (4.4.10)", e.Details["reason"])
	}
	notASecret := t.SecretFile("not-a-secret")
	if err := os.WriteFile(notASecret, []byte("this is not a join secret\n"), 0o600); err != nil {
		t.Fatalf("write a file that holds no secret: %v", err)
	}
	if e := t.Fail(run(o, nil, "--secret-file", notASecret, "--name", name), protocol.CodeInvalidInput); e.Details["field"] != "join_secret" {
		t.Errorf("team take-in with a file that holds no join secret: details.field %q, want join_secret (4.4.10)", e.Details["field"])
	}
	if state := describe(t, o, false).Profile.State; state == protocol.ProfileStateJoined {
		t.Errorf("describe after the refused take-ins: profile.state %q; a refused take-in binds nothing (4.4.10)", state)
	}

	// ---- taken in, and owned in the same step ----
	r := run(o, nil, "--secret-file", file, "--name", name, "--label", "c53-owner@example.com")
	var taken protocol.TeamTakeInResult
	t.OK(r, &taken)
	result := t.OKRaw(r)
	for _, key := range []string{"team_ref", "team_name", "principal_ref", "taken_in", "owner"} {
		if _, has := result[key]; !has {
			t.Errorf("team take-in: result has no `%s`; all five members are required (4.4.10)", key)
		}
	}
	if _, has := result["join_secret"]; has {
		t.Errorf("team take-in: result has a `join_secret` member; the secret appears in no output (4.4.10)")
	}
	if taken.TeamRef != teamRef || taken.TeamName != name || !taken.TakenIn || !taken.Owner {
		t.Errorf("team take-in: {team_ref match %v, team_name match %v, taken_in %v, owner %v}; want the team_ref inside the secret, the name given, true (a refused take-in takes nothing in) and true (4.4.10)",
			taken.TeamRef == teamRef, taken.TeamName == name, taken.TakenIn, taken.Owner)
	}
	if bytes.Contains(r.Stdout, []byte(secret)) || bytes.Contains(r.Stderr, []byte(secret)) {
		t.Errorf("team take-in: the join secret is on stdout or stderr (4.5.14)")
	}
	o.PrincipalRef, o.TeamRef, o.TeamName = taken.PrincipalRef, taken.TeamRef, taken.TeamName
	if p := describe(t, o, false).Profile; p.State != protocol.ProfileStateJoined || p.TeamRef != teamRef || p.PrincipalRef != taken.PrincipalRef {
		t.Errorf("describe after team take-in: {state %q, team_ref match %v, principal_ref match %v}; want joined, the team and the caller (4.4.10)",
			p.State, p.TeamRef == teamRef, p.PrincipalRef == taken.PrincipalRef)
	}
	// The taker is an active member, and the owner: a command that answers
	// the owner only answers it.
	t.List(o, "", false)
	var owned protocol.TeamCloseMoveResult
	t.OK(t.Exec(o, nil, "team", "close-move"), &owned)
	if owned.TeamRef != teamRef || owned.Closed {
		t.Errorf("team close-move by the taker, on a team that is not moving: {team_ref match %v, closed %v}; want the team and false (4.4.10)",
			owned.TeamRef == teamRef, owned.Closed)
	}

	// ---- the same profile again: a join that changes nothing ----
	again := takeIn(t, o, secret, name+"-again", "")
	if again.TakenIn || !again.Owner || again.TeamRef != teamRef || again.TeamName != name || again.PrincipalRef != taken.PrincipalRef {
		t.Errorf("the same team take-in again: {team_ref match %v, team_name the team's %v, principal_ref match %v, taken_in %v, owner %v}; want the team as it is, the same principal, false and true (4.4.10)",
			again.TeamRef == teamRef, again.TeamName == name, again.PrincipalRef == taken.PrincipalRef, again.TakenIn, again.Owner)
	}

	// ---- anyone else with the secret: a member that does not own ----
	m := t.Scratch("member")
	joined := takeIn(t, m, secret, name, "c53-member@example.com")
	if joined.TakenIn || joined.Owner || joined.TeamRef != teamRef || joined.PrincipalRef == taken.PrincipalRef {
		t.Errorf("team take-in by a second principal: {team_ref match %v, own principal_ref %v, taken_in %v, owner %v}; want the team, a principal of its own, false and false (4.4.10)",
			joined.TeamRef == teamRef, joined.PrincipalRef != taken.PrincipalRef, joined.TakenIn, joined.Owner)
	}
	t.List(m, "", false)
	notOwner := t.Exec(m, nil, "team", "close-move")
	t.Fail(notOwner, protocol.CodeUnauthorized)

	// ---- another secret for the same team_ref: a conflict ----
	other := syntheticSecret(teamRef)
	t.GiveSecretFile("other-secret", other)
	stranger := t.Scratch("stranger")
	conflict := run(stranger, takeInDocument(t, other, name, "c53-stranger@example.com"))
	if e := t.Fail(conflict, protocol.CodeConflict); e.Details["reason"] != "other_join_secret" {
		t.Errorf("team take-in under another secret for the team_ref: details.reason %q, want other_join_secret (4.4.10)", e.Details["reason"])
	}
	t.DifferentBytes("conflict vs unauthorized (positive control)", conflict, notOwner)
	if state := describe(t, stranger, false).Profile.State; state == protocol.ProfileStateJoined {
		t.Errorf("describe after a take-in that was a conflict: profile.state %q; a conflict binds nothing (4.4.10)", state)
	}
	if e := t.Fail(run(o, takeInDocument(t, other, name, "")), protocol.CodeConflict); e.Details["reason"] != "other_join_secret" {
		t.Errorf("team take-in under another secret by the team's owner: details.reason %q, want other_join_secret (4.4.10)", e.Details["reason"])
	}
	// The conflict changed nothing: the owner still owns the team.
	t.OK(t.Exec(o, nil, "team", "close-move"), &owned)

	// ---- the binding rule is `team join`'s ----
	elsewhere := syntheticSecret(elsewhereRef())
	if e := t.Fail(run(o, takeInDocument(t, elsewhere, name, "")), protocol.CodeConflict); e.Details["reason"] != "profile_bound" {
		t.Errorf("team take-in of another team on a bound profile: details.reason %q, want profile_bound (4.4.10)", e.Details["reason"])
	}

	// ---- the team's members join with the secret they have ----
	if t.HasCap("team.join") {
		j := t.Scratch("joiner")
		if in := joinTeam(t, j, secret, "c53-joiner@example.com"); in.TeamRef != teamRef || in.Rejoined {
			t.Errorf("team join with the secret of a team that was taken in: {team_ref match %v, rejoined %v}; want the team and false (4.4.10)",
				in.TeamRef == teamRef, in.Rejoined)
		}
	}
}
