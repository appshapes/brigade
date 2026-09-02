package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestTeamCreateReturnsAParseableSecret covers C-03: all four members, a
// secret the protocol helper parses, and `describe` joined afterwards.
func TestTeamCreateReturnsAParseableSecret(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.ok(`{"team_name":"ops","human_label":"alice@example.com"}`, "team", "create")
	secret, err := protocol.ParseJoinSecret(str(t, result, "join_secret"))
	if err != nil {
		t.Fatalf("the created secret does not parse: %v", err)
	}
	if secret.TeamRef() != str(t, result, "team_ref") {
		t.Fatalf("the secret names %q, the result %q", secret.TeamRef(), str(t, result, "team_ref"))
	}
	if str(t, result, "principal_ref") == "" || str(t, result, "team_name") != "ops" {
		t.Fatalf("result = %v", result)
	}
}

// TestTeamCreateTwiceIsProfileBound covers C-03b: the second `team create`
// and a `team join` to another team are both `conflict` with reason
// profile_bound, and the first binding is untouched.
func TestTeamCreateTwiceIsProfileBound(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, _ := r.team("alice", "ops")
	_, other := r.team("carol", "other")

	for _, attempt := range [][]any{
		{`{"team_name":"second"}`, []string{"--profile", "alice", "team", "create"}},
		{`{"join_secret":"` + other + `"}`, []string{"--profile", "alice", "team", "join"}},
	} {
		input, _ := attempt[0].(string)
		args, _ := attempt[1].([]string)
		got := r.fails("conflict", 7, input, args...)
		object, _ := decode(t, got.stdout)["error"].(map[string]any)
		details, _ := object["details"].(map[string]any)
		if details["reason"] != reasonProfileBound {
			t.Fatalf("%v: details = %v", args, details)
		}
	}
	if got := str(t, r.ok("", "--profile", "alice", "profile", "status"), "team_ref"); got != teamRef {
		t.Fatalf("the binding changed: %q, want %q", got, teamRef)
	}
}

// TestTeamCreateWithSecretFileOmitsTheSecret covers 4.4.10's named
// exception: the secret goes to a 0600 file and NOT to stdout.
func TestTeamCreateWithSecretFileOmitsTheSecret(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	path := filepath.Join(r.base, "secret.txt")
	got := r.exec(`{"team_name":"ops"}`, "team", "create", "--secret-file", path)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if strings.Contains(got.stdout, protocol.JoinSecretPrefix) {
		t.Fatalf("stdout carries the secret: %s", got.stdout)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path) //nolint:gosec // the path is this test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ParseJoinSecret(strings.TrimSpace(string(data))); err != nil {
		t.Fatalf("the file does not hold a parseable secret: %v", err)
	}
}

// TestTeamCreateSecretFileFailsBeforeAnythingIsCreated: the join secret is
// printed ONCE (4.4.10), so a caller whose --secret-file could not be
// written has no copy of it. The command must therefore fail before the
// team exists and the profile is bound — otherwise the profile is stuck on
// `conflict profile_bound` for a team nobody can ever join.
func TestTeamCreateSecretFileFailsBeforeAnythingIsCreated(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// A relative path resolves against the process's working directory,
	// which under a hook is the project tree; --root already refuses one
	// for exactly that reason, and a join secret must never land there.
	r.fails("usage", 2, `{"team_name":"ops"}`, "team", "create", "--secret-file", "relative-secret.txt")
	// An unwritable path is a local configuration failure, not an adapter bug.
	r.fails("config", 11, `{"team_name":"ops"}`,
		"team", "create", "--secret-file", filepath.Join(r.base, "nope", "deep", "secret.txt"))
	// Neither attempt created a team or bound the profile, so the caller
	// can fix the path and try again.
	if got, present := r.ok("", "profile", "status")["team_ref"]; present && got != "" {
		t.Fatalf("a failed --secret-file bound the profile to %v", got)
	}
	absent(t, filepath.Join(r.root, teamsDirName))
	result := r.ok(`{"team_name":"ops"}`, "team", "create", "--secret-file", filepath.Join(r.base, "s.txt"))
	if str(t, result, "team_name") != "ops" {
		t.Fatalf("the retry failed: %v", result)
	}
}

// TestTeamCreateFlagsInsteadOfStdin: with --name the request comes from
// argv and stdin is not read at all.
func TestTeamCreateFlagsInsteadOfStdin(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.ok("", "team", "create", "--name", "ops", "--label", "alice@example.com")
	if str(t, result, "team_name") != "ops" {
		t.Fatalf("result = %v", result)
	}
	members, _ := r.ok("", "team", "members")["members"].([]any)
	first, _ := members[0].(map[string]any)
	if str(t, first, "human_label") != "alice@example.com" {
		t.Fatalf("member = %v", first)
	}
}

// TestPromptWithoutATerminalIsUsage covers B-7.
func TestPromptWithoutATerminalIsUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.fails("usage", 2, "", "team", "create", "--prompt")
	r.fails("usage", 2, "", "team", "join", "--prompt")
}

// TestJoinRefusalsAreByteIdentical covers C-04 and 4.5.7: a wrong secret,
// an unknown team and a malformed one are answered without any oracle —
// the first two byte for byte, and the third as invalid_input naming the
// member but never its value.
func TestJoinRefusalsAreByteIdentical(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")

	wrong := strings.Split(secret, ".")
	wrongSecret := protocol.JoinSecretPrefix + teamRef + ".0123456789abcdef0123456789abcdef"
	if len(wrong) != 3 {
		t.Fatalf("unexpected secret shape")
	}
	unknownTeam := protocol.JoinSecretPrefix + "ffffffffffffffffffffffffffffffff.0123456789abcdef0123456789abcdef"

	a := r.fails("unauthorized", 5, `{"join_secret":"`+wrongSecret+`"}`, "--profile", "bob", "team", "join")
	b := r.fails("unauthorized", 5, `{"join_secret":"`+unknownTeam+`"}`, "--profile", "carol", "team", "join")
	if errorJSON(t, a.stdout) != errorJSON(t, b.stdout) {
		t.Fatalf("a wrong secret and an unknown team differ:\n%s\n%s", a.stdout, b.stdout)
	}

	malformed := r.fails("invalid_input", 3, `{"join_secret":"not-a-secret"}`, "--profile", "dave", "team", "join")
	if strings.Contains(malformed.stdout, "not-a-secret") {
		t.Fatalf("the malformed secret was echoed: %s", malformed.stdout)
	}
	// The positive control: two DIFFERENT failures are NOT byte-identical,
	// so the comparison above can actually fail.
	if errorJSON(t, a.stdout) == errorJSON(t, malformed.stdout) {
		t.Fatal("the byte-identity check cannot distinguish two different errors")
	}
}

// TestLeaveIsIdempotentAndHidesTheSessions covers C-08 end to end.
func TestLeaveIsIdempotentAndHidesTheSessions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.register("alice", "alice-1")
	bobJoin := r.join("bob", secret)
	bob := r.register("bob", "bob-1")

	left := r.ok("", "--profile", "bob", "team", "leave")
	if str(t, left, "team_ref") != teamRef || left["left"] != true {
		t.Fatalf("leave = %v", left)
	}
	profile, _ := r.ok("", "--profile", "bob", "describe")["profile"].(map[string]any)
	if str(t, profile, "state") != protocol.ProfileStateNotMember {
		t.Fatalf("state after leave = %v", profile)
	}
	for _, args := range [][]string{
		{"--profile", "bob", "session", "list"},
		{"--profile", "bob", "message", "receive", "--session", bob},
	} {
		r.fails("config", 11, "", args...)
	}
	// A's list no longer shows B's sessions, even with --include-offline.
	sessions, _ := r.ok("", "--profile", "alice", "session", "list", "--include-offline")["sessions"].([]any)
	for _, s := range sessions {
		entry, _ := s.(map[string]any)
		if str(t, entry, "session_id") == bob {
			t.Fatal("a revoked member's session is still listed (C-08)")
		}
	}
	// A second leave is still success, and the rejoin restores the same
	// principal with rejoined true.
	again := r.ok("", "--profile", "bob", "team", "leave")
	if str(t, again, "team_ref") != teamRef || again["left"] != true {
		t.Fatalf("second leave = %v", again)
	}
	back := r.join("bob", secret)
	if back["rejoined"] != true || str(t, back, "principal_ref") != str(t, bobJoin, "principal_ref") {
		t.Fatalf("rejoin = %v, want the same principal as %v", back, bobJoin)
	}
}

// TestLeaveOnANeverBoundProfileIsConfig: idempotency needs a team_ref to
// answer with, and a profile that was never bound has none.
func TestLeaveOnANeverBoundProfileIsConfig(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.ok("", "--profile", "alice", "profile", "init")
	r.fails("config", 11, "", "--profile", "alice", "team", "leave")
}

// TestTeamMembersRoster covers C-43: every active member with its
// last_seen_at and session_count, never a member of another team, and one
// `unauthorized` byte-identical for a rebound profile and a random ref.
func TestTeamMembersRoster(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.join("bob", secret)
	r.register("alice", "alice-1")
	otherRef, _ := r.team("carol", "other")

	result := r.ok("", "--profile", "alice", "team", "members")
	if str(t, result, "team_ref") != teamRef || str(t, result, "team_name") != "ops" {
		t.Fatalf("result = %v", result)
	}
	members, _ := result["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %v, want two", members)
	}
	seenNull, seenValue := false, false
	for _, m := range members {
		entry, _ := m.(map[string]any)
		if str(t, entry, "status") != memberActive {
			t.Fatalf("member = %v", entry)
		}
		if _, present := entry["last_seen_at"]; !present {
			t.Fatalf("last_seen_at is absent, it must be null when unknown: %v", entry)
		}
		if entry["last_seen_at"] == nil {
			seenNull = true
			if entry["session_count"] != float64(0) {
				t.Fatalf("a member with no session has session_count %v", entry["session_count"])
			}
		} else {
			seenValue = true
			if entry["session_count"] != float64(1) {
				t.Fatalf("alice's session_count = %v", entry["session_count"])
			}
		}
	}
	if !seenNull || !seenValue {
		t.Fatalf("expected one member with and one without a session: %v", members)
	}

	// C's roster names only C: no cross-team leak.
	carol, _ := r.ok("", "--profile", "carol", "team", "members")["members"].([]any)
	if len(carol) != 1 {
		t.Fatalf("carol's roster = %v", carol)
	}

	// Rebound to T1 and to a team that does not exist: one answer.
	rebind(t, r, "carol", teamRef)
	bound := r.fails("unauthorized", 5, "", "--profile", "carol", "team", "members")
	rebind(t, r, "carol", "ffffffffffffffffffffffffffffffff")
	random := r.fails("unauthorized", 5, "", "--profile", "carol", "team", "members")
	if errorJSON(t, bound.stdout) != errorJSON(t, random.stdout) {
		t.Fatalf("a real team and a random one differ:\n%s\n%s", bound.stdout, random.stdout)
	}
	if otherRef == "" {
		t.Fatal("no other team ref")
	}
}
