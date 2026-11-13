package cases

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c48RotateSecret (cap team.admin): on a team of the case's own, `team
// rotate-secret` without --secret-file, and with one that is not an
// absolute path, is `usage`; with one in a directory that does not exist
// it is `config` with details.reason secret_file_unwritable; a member that
// is not the owner is `unauthorized`; and none of the four writes a file
// or costs a rotation, because the owner's rotation that follows answers
// secret_version 2. That rotation answers {team_ref, team_name,
// secret_version, rotated_at, secret_file} with the same team_ref, the new
// secret is in the file (whose mode is exactly 0600 where the platform has
// modes), parses, names the same team, differs from the previous one and
// appears on neither stdout nor stderr; an existing member is still a
// member; a join with the previous secret is `unauthorized`,
// byte-identical to a join with a well-formed secret that was never
// issued; a join with the new secret works; and a second rotation answers
// secret_version 3 (4.4.10).
func c48RotateSecret() conformance.Case {
	return conformance.Case{
		ID:    "C-48",
		Rule:  "4.4.10 team rotate-secret",
		Title: "the owner's rotation puts a new join secret in the file and in no output, counts the versions, keeps every member and makes the previous secret the rejected join",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.admin")},
		Run:   runC48,
	}
}

// relativeSecretFile is the --secret-file C-48 gives that is not an
// absolute path, which an adapter must refuse.
const relativeSecretFile = "brigade-conformance-c48-relative"

func runC48(t *conformance.T) {
	owner, member, created := scratchTeam(t, "c48")
	rotate := func(p *conformance.Principal, args ...string) *conformance.Result {
		return t.Exec(p, nil, append([]string{"team", "rotate-secret"}, args...)...)
	}

	// The four refusals that come before a rotation. None of them may make
	// one: the version the owner's rotation answers below is still 2.
	noFile := rotate(owner)
	t.Fail(noFile, protocol.CodeUsage)
	t.Fail(rotate(owner, "--secret-file", relativeSecretFile), protocol.CodeUsage)
	// The path is relative to the directory the suite runs in. An adapter
	// that took it has written a join secret there: the file is removed, and
	// its having been there is the failure.
	if os.Remove(relativeSecretFile) == nil {
		t.Errorf("team rotate-secret --secret-file with a relative path: the file was written (4.4.10)")
	}
	path := t.SecretFile("rotated")
	nowhere := filepath.Join(filepath.Dir(path), "c48-no-such-directory", "rotated")
	if e := t.Fail(rotate(owner, "--secret-file", nowhere), protocol.CodeConfig); e.Details["reason"] != "secret_file_unwritable" {
		t.Errorf("team rotate-secret --secret-file in a directory that does not exist: details.reason %q, want secret_file_unwritable (4.4.10)", e.Details["reason"])
	}
	refused := t.SecretFile("member")
	t.Fail(rotate(member, "--secret-file", refused), protocol.CodeUnauthorized)
	if _, err := os.Stat(refused); err == nil {
		t.Errorf("team rotate-secret by a member that is not the owner: the secret file was written (4.4.10)")
	}

	r := rotate(owner, "--secret-file", path)
	var rotated protocol.TeamRotateSecretResult
	t.OK(r, &rotated)
	result, _ := r.Raw["result"].(map[string]any)
	for _, key := range []string{"team_ref", "team_name", "secret_version", "rotated_at", "secret_file"} {
		if _, has := result[key]; !has {
			t.Errorf("team rotate-secret: result has no `%s`; all five members are required (4.4.10)", key)
		}
	}
	if _, has := result["join_secret"]; has {
		t.Errorf("team rotate-secret: result has a `join_secret` member; the new secret goes to the file and to no output (4.4.10)")
	}
	if rotated.TeamRef != created.TeamRef || rotated.TeamName != created.TeamName || rotated.SecretVersion != 2 || rotated.SecretFile != path {
		t.Errorf("team rotate-secret: {team_ref match %v, team_name match %v, secret_version %d, secret_file match %v}; want the team's own, 2 on a first rotation (a refused one is none) and the path given (4.4.10)",
			rotated.TeamRef == created.TeamRef, rotated.TeamName == created.TeamName, rotated.SecretVersion, rotated.SecretFile == path)
	}

	next, mode0600 := t.TakeSecretFile(path)
	if !mode0600 {
		t.Errorf("team rotate-secret: the secret file is not a regular file of mode 0600 exactly (4.4.10)")
	}
	if parsed, err := protocol.ParseJoinSecret(next); err != nil {
		t.Errorf("team rotate-secret: the secret file does not hold one join secret of the 4.4.10 format")
	} else if parsed.TeamRef() != created.TeamRef {
		t.Errorf("team rotate-secret: the new secret names another team_ref; want the same (4.4.10)")
	}
	if next == created.JoinSecret {
		t.Errorf("team rotate-secret: the secret in the file is the previous secret (4.4.10)")
	}
	if next != "" && (bytes.Contains(r.Stdout, []byte(next)) || bytes.Contains(r.Stderr, []byte(next))) {
		t.Errorf("team rotate-secret: the new secret is on stdout or stderr; it goes to the file and to no output (4.4.10, 4.5.14)")
	}

	// Every member stays a member.
	t.List(member, "", false)

	// The previous secret no longer joins, and says nothing about why.
	stranger := t.Scratch("stranger")
	previous := t.Exec(stranger, joinDocument(t, created.JoinSecret), "team", "join")
	t.Fail(previous, protocol.CodeUnauthorized)
	never := t.Exec(stranger, joinDocument(t, syntheticSecret(created.TeamRef)), "team", "join")
	t.Fail(never, protocol.CodeUnauthorized)
	t.SameBytes("team join with the previous secret vs with a secret that was never issued (4.4.10)", previous, never)
	t.DifferentBytes("unauthorized vs usage (positive control)", previous, noFile)
	if joined := joinTeam(t, stranger, next, "c48-stranger@example.com"); joined.TeamRef != created.TeamRef || joined.Rejoined {
		t.Errorf("team join with the new secret: {team_ref match %v, rejoined %v}; want the team and false (4.4.10)",
			joined.TeamRef == created.TeamRef, joined.Rejoined)
	}

	again := t.SecretFile("again")
	var second protocol.TeamRotateSecretResult
	t.OK(rotate(owner, "--secret-file", again), &second)
	if second.SecretVersion != 3 || second.TeamRef != created.TeamRef {
		t.Errorf("second team rotate-secret: {team_ref match %v, secret_version %d}; want the same team and 3 (4.4.10)",
			second.TeamRef == created.TeamRef, second.SecretVersion)
	}
	if third, _ := t.TakeSecretFile(again); third == next || third == created.JoinSecret {
		t.Errorf("second team rotate-secret: the secret in the file is one the team had before (4.4.10)")
	}
}
