package cases

import (
	"strconv"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c03TeamCreate: `team create` on a scratch principal answers the four
// members of 4.4.10, the join secret parses with the protocol helper and
// names the returned team_ref, and `describe` afterwards is `joined` with
// the four profile members (4.2, 4.4.1). B-7: `team create --prompt` with
// stdin a pipe (no TTY) is `usage`.
func c03TeamCreate() conformance.Case {
	return conformance.Case{
		ID:    "C-03",
		Rule:  "4.2 team create",
		Title: "team create returns team_ref, team_name, join_secret, principal_ref; describe is joined; --prompt on a pipe is usage",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.create")},
		Run:   runC03,
	}
}

func runC03(t *conformance.T) {
	p := t.Scratch("c03")
	name := "c03-" + t.RunID()
	res := createTeam(t, p, name, "c03@example.com")
	if res.TeamName != name {
		t.Errorf("team create: team_name %q, want %q", res.TeamName, name)
	}
	secret, err := protocol.ParseJoinSecret(res.JoinSecret)
	if err != nil {
		t.Errorf("team create: join_secret does not parse with protocol.ParseJoinSecret: %v (4.4.10)", err)
	} else if secret.TeamRef() != res.TeamRef {
		t.Errorf("team create: the team_ref inside join_secret differs from result.team_ref (4.4.10)")
	}

	d := describe(t, p, false)
	if d.Profile.State != protocol.ProfileStateJoined {
		t.Errorf("describe after team create: profile.state %q, want joined (4.4.1)", d.Profile.State)
	}
	if d.Profile.TeamRef != res.TeamRef || d.Profile.TeamName != res.TeamName || d.Profile.PrincipalRef != res.PrincipalRef {
		t.Errorf("describe after team create: profile {team_ref, team_name, principal_ref} differ from the team create result (4.4.1)")
	}
	if d.Profile.HumanLabel != "c03@example.com" {
		t.Errorf("describe after team create: profile.human_label %q, want the label given (4.4.1)", d.Profile.HumanLabel)
	}

	// B-7: --prompt asks on a TTY; on a pipe it is usage. On a second,
	// unbound scratch so the profile_bound rule of 4.4.10 cannot answer
	// first (the spec orders neither). The document on the pipe is valid
	// JSON, so an adapter that ignored --prompt would create a team, never
	// answer usage; and it is TWO non-empty lines, so an adapter that
	// prompted on the pipe anyway gets an answer to both of its questions
	// (a short first line as the name, the second as the label) and
	// creates a team too, instead of hitting EOF on the second prompt and
	// answering usage by accident (measured: a one-line document let
	// exactly that mutant pass).
	prompt := t.Scratch("prompt")
	doc := []byte("{\"team_name\": " + strconv.Quote(name+"-prompt") + ",\n\"human_label\": \"c03@example.com\"}\n")
	t.Fail(t.Exec(prompt, doc, "team", "create", "--prompt"), protocol.CodeUsage)
}

// c03bProfileBound: on a profile bound by `team create`, a second `team
// create` and a `team join` naming another team are both `conflict` with
// `details.reason = "profile_bound"` — the check is local, before any
// network — and the binding is unchanged afterwards (4.4.10).
func c03bProfileBound() conformance.Case {
	return conformance.Case{
		ID:    "C-03b",
		Rule:  "4.4.10 profile_bound",
		Title: "team create and team join on a bound profile → conflict profile_bound; the binding is unchanged",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.create")},
		Run:   runC03b,
	}
}

func runC03b(t *conformance.T) {
	p := t.Scratch("c03b")
	first := createTeam(t, p, "c03b-"+t.RunID(), "c03b@example.com")

	req := protocol.TeamCreateRequest{TeamName: "c03b-second-" + t.RunID(), HumanLabel: "c03b@example.com"}
	e := t.Fail(t.Exec(p, mustMarshal(t, &req), "team", "create"), protocol.CodeConflict)
	if e.Details["reason"] != "profile_bound" {
		t.Errorf("second team create: details.reason %q, want profile_bound (4.4.10)", e.Details["reason"])
	}

	// A syntactically valid secret for a team that does not exist: the
	// local binding check must refuse it before any lookup could.
	join := protocol.TeamJoinRequest{JoinSecret: syntheticSecret(randomRef()), HumanLabel: "c03b@example.com"}
	e = t.Fail(t.Exec(p, mustMarshal(t, &join), "team", "join"), protocol.CodeConflict)
	if e.Details["reason"] != "profile_bound" {
		t.Errorf("team join on a bound profile: details.reason %q, want profile_bound (4.4.10)", e.Details["reason"])
	}

	d := describe(t, p, false)
	if d.Profile.State != protocol.ProfileStateJoined {
		t.Errorf("describe after the refused commands: profile.state %q, want joined", d.Profile.State)
	}
	if d.Profile.TeamRef != first.TeamRef || d.Profile.PrincipalRef != first.PrincipalRef {
		t.Errorf("describe after the refused commands: team_ref or principal_ref changed; the first binding must be untouched (4.4.10)")
	}
}
