package cases

import (
	"bytes"
	"strconv"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c04TeamJoin: `team join` with a valid secret binds a scratch principal
// (`rejoined false`); a wrong secret and an unknown team are `unauthorized`
// with byte-identical stdout (no team-existence oracle, 4.4.10, 4.5.7); a
// malformed secret is `invalid_input` naming `join_secret` without echoing
// the input; B-8: a non-object `backend` is `invalid_input`; B-7: `--prompt`
// on a pipe is `usage`; a join to another team on the bound profile is
// `conflict` `profile_bound`. The team is created by a second scratch
// principal when the adapter has team.create, else the fixture's T1 secret
// is used.
func c04TeamJoin() conformance.Case {
	return conformance.Case{
		ID:    "C-04",
		Rule:  "4.2 team join",
		Title: "team join binds; wrong secret ≡ unknown team (unauthorized); malformed → invalid_input; bound → conflict profile_bound",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.join")},
		Run:   runC04,
	}
}

func runC04(t *conformance.T) {
	var secret string
	if t.HasCap("team.create") {
		owner := t.Scratch("owner")
		secret = createTeam(t, owner, "c04-"+t.RunID(), "c04-owner@example.com").JoinSecret
	} else {
		secret = t.JoinSecret()
		if secret == "" {
			t.Skip("needs team.create or a known join secret to join a team")
		}
	}
	parsed, err := protocol.ParseJoinSecret(secret)
	if err != nil {
		t.Fatalf("the join secret does not parse: %v", err)
	}
	teamRef := parsed.TeamRef()

	joiner := t.Scratch("joiner")
	res := joinTeam(t, joiner, secret, "c04@example.com")
	if res.TeamRef != teamRef {
		t.Errorf("team join: team_ref differs from the team_ref inside the secret (4.4.10)")
	}
	if res.Rejoined {
		t.Errorf("team join: rejoined true on a first join, want false (4.4.10)")
	}
	d := describe(t, joiner, false)
	if d.Profile.State != protocol.ProfileStateJoined || d.Profile.PrincipalRef != res.PrincipalRef {
		t.Errorf("describe after team join: state %q principal_ref match %v; want joined with the join's principal_ref", d.Profile.State, d.Profile.PrincipalRef == res.PrincipalRef)
	}

	// Wrong secret (the real team_ref, a different tail) and an unknown
	// team (a random team_ref): both unauthorized, byte-identical.
	wrong := t.Scratch("wrong")
	wrongReq := protocol.TeamJoinRequest{JoinSecret: syntheticSecret(teamRef), HumanLabel: "c04@example.com"}
	rWrong := t.Exec(wrong, mustMarshal(t, &wrongReq), "team", "join")
	t.Fail(rWrong, protocol.CodeUnauthorized)
	unknown := t.Scratch("unknown")
	unknownReq := protocol.TeamJoinRequest{JoinSecret: syntheticSecret(randomRef()), HumanLabel: "c04@example.com"}
	rUnknown := t.Exec(unknown, mustMarshal(t, &unknownReq), "team", "join")
	t.Fail(rUnknown, protocol.CodeUnauthorized)
	t.SameBytes("wrong secret vs unknown team (4.4.10)", rWrong, rUnknown)

	// Malformed secret: invalid_input naming the member, input not echoed.
	malformed := t.Scratch("malformed")
	rBad := t.Exec(malformed, []byte(`{"join_secret":"nope","human_label":"c04@example.com"}`), "team", "join")
	e := t.Fail(rBad, protocol.CodeInvalidInput)
	if e.Details["field"] != "join_secret" {
		t.Errorf("malformed secret: details.field %q, want join_secret (4.4.10)", e.Details["field"])
	}
	if bytes.Contains(rBad.Stdout, []byte("nope")) || bytes.Contains(rBad.Stderr, []byte("nope")) {
		t.Errorf("malformed secret: the input value is echoed on stdout or stderr (4.4.10, 4.3.1)")
	}
	t.DifferentBytes("unauthorized vs invalid_input (positive control)", rWrong, rBad)

	// B-8: backend must be a JSON object when present.
	backend := t.Scratch("backend")
	doc := mustMarshal(t, map[string]any{"join_secret": secret, "human_label": "c04@example.com", "backend": "x"})
	e = t.Fail(t.Exec(backend, doc, "team", "join"), protocol.CodeInvalidInput)
	if e.Details["field"] != "backend" {
		t.Note("backend \"x\": details.field %q (the reference adapter names backend; not frozen)", e.Details["field"])
	}

	// B-7: --prompt without a TTY is usage, whatever the pipe carries. As
	// in C-03 the document is valid JSON on two non-empty lines: an
	// adapter that ignored --prompt would join, and one that prompted on
	// the pipe reads a line that is not a secret (invalid_input) rather
	// than running out of input and answering usage by accident.
	prompt := t.Scratch("prompt")
	promptDoc := []byte("{\"join_secret\": " + strconv.Quote(secret) + ",\n\"human_label\": \"c04@example.com\"}\n")
	t.Fail(t.Exec(prompt, promptDoc, "team", "join", "--prompt"), protocol.CodeUsage)

	// A second join to ANOTHER team on the bound profile: conflict, locally.
	otherReq := protocol.TeamJoinRequest{JoinSecret: syntheticSecret(randomRef()), HumanLabel: "c04@example.com"}
	e = t.Fail(t.Exec(joiner, mustMarshal(t, &otherReq), "team", "join"), protocol.CodeConflict)
	if e.Details["reason"] != "profile_bound" {
		t.Errorf("team join to another team on a bound profile: details.reason %q, want profile_bound (4.4.10)", e.Details["reason"])
	}
}
