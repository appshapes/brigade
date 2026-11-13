//go:build !windows

package rest

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapters/rest/stub"
	"github.com/appshapes/brigade/internal/protocol"
)

// The team administration commands (4.4.10, capability team.admin; the
// contract's section 3a): `team rotate-secret`, `team revoke-member` and
// `team transfer`, against the stand-in server, which carries the three
// endpoints, and against one that leaves them out.

// principalRef is the principal_ref a rig's own files name.
func (r *rig) principalRef() string {
	r.t.Helper()
	return str(r.t, r.ok("", "profile", "status"), "principal_ref")
}

// secretIn reads the join secret a command wrote to path and checks the
// file: mode 0600, one line, a secret the protocol parses.
func secretIn(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the secret file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the secret file has mode %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") || strings.Count(string(data), "\n") != 1 {
		t.Errorf("the secret file is not one line")
	}
	secret := strings.TrimSpace(string(data))
	if _, err := protocol.ParseJoinSecret(secret); err != nil {
		t.Fatalf("the secret file does not hold a join secret: %v", err)
	}
	return secret
}

// versions reads joined_secret_version by principal_ref out of a roster; a
// member that carries none is absent from the map.
func versions(t *testing.T, roster map[string]any) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	members, _ := roster["members"].([]any)
	for _, m := range members {
		member, _ := m.(map[string]any)
		if v, present := member["joined_secret_version"].(float64); present {
			out[str(t, member, "principal_ref")] = v
		}
	}
	return out
}

// TestTeamRotateSecret: the owner's rotation writes the new secret to the
// file and to nothing else, answers the five members of 4.4.10, keeps
// every member, and makes the previous secret the rejected join; a second
// rotation counts on; and a member that is not the owner is refused with
// the bytes a non-member gets.
func TestTeamRotateSecret(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	path := filepath.Join(t.TempDir(), "rotated.secret")

	got := p.alice.exec("", "team", "rotate-secret", "--secret-file", path)
	if got.code != 0 {
		t.Fatalf("team rotate-secret: exit %d (%s %s)", got.code, got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if len(result) != 5 || result["team_ref"] != teamRef || result["team_name"] != "ops" ||
		result["secret_version"] != float64(2) || result["secret_file"] != path {
		t.Errorf("team rotate-secret: result %v, want exactly {team_ref, team_name, secret_version 2, rotated_at, secret_file}", result)
	}
	if _, err := time.Parse(time.RFC3339Nano, str(t, result, "rotated_at")); err != nil {
		t.Errorf("rotated_at: %v", err)
	}
	rotated := secretIn(t, path)
	if parsed, _ := protocol.ParseJoinSecret(rotated); parsed.TeamRef() != teamRef || rotated == p.secret {
		t.Errorf("the rotated secret names another team or is the previous secret again")
	}
	if strings.Contains(p.alice.output(), rotated) || strings.Contains(got.stdout, "join_secret") {
		t.Errorf("the new secret, or a join_secret member, reached stdout or stderr")
	}
	if !strings.Contains(got.stderr, "--secret-file") || !strings.Contains(got.stderr, "no longer works") {
		t.Errorf("stderr %q does not say where the secret went and that the previous one is dead", got.stderr)
	}

	// Every member is still a member, and the version each joined with is
	// the first: a rotation revokes nobody.
	roster := p.bob.ok("", "team", "members")
	if joined := versions(t, roster); len(joined) != 2 || joined[p.alice.principalRef()] != 1 || joined[p.bob.principalRef()] != 1 {
		t.Errorf("the roster after a rotation: versions %v, want both members at 1", joined)
	}

	// The previous secret is the rejected join, byte for byte what a secret
	// that was never issued gets; the new one joins, at version 2.
	stranger := newRig(t, b.url())
	old := stranger.fails(protocol.CodeUnauthorized, `{"join_secret":"`+p.secret+`"}`, "team", "join")
	never := stranger.fails(protocol.CodeUnauthorized,
		`{"join_secret":"`+protocol.JoinSecretPrefix+teamRef+"."+strings.Repeat("0", 32)+`"}`, "team", "join")
	if old.stdout != never.stdout {
		t.Errorf("a join with the previous secret and one with a secret never issued differ:\n%s%s", old.stdout, never.stdout)
	}
	carol := newRig(t, b.url())
	carol.join(rotated, "carol@example.com")
	if joined := versions(t, p.alice.ok("", "team", "members")); joined[carol.principalRef()] != 2 {
		t.Errorf("a member that joined with the rotated secret: versions %v, want it at 2", joined)
	}

	// A second rotation counts on, and replaces the file.
	again := p.alice.ok("", "team", "rotate-secret", "--secret-file", path)
	if again["secret_version"] != float64(3) {
		t.Errorf("a second rotation: secret_version %v, want 3", again["secret_version"])
	}
	if secretIn(t, path) == rotated {
		t.Errorf("a second rotation left the first rotation's secret in the file")
	}

	// A member that is not the owner, and a profile rebound to a team that
	// does not exist: one refusal.
	other := filepath.Join(t.TempDir(), "bob.secret")
	notOwner := p.bob.fails(protocol.CodeUnauthorized, "", "team", "rotate-secret", "--secret-file", other)
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("a refused rotation wrote the secret file (%v)", err)
	}
	p.bob.ok("", "team", "leave")
	p.bob.join(secretIn(t, path), "bob@example.com")
	if rejoined := p.bob.fails(protocol.CodeUnauthorized, "", "team", "rotate-secret", "--secret-file", other); rejoined.stdout != notOwner.stdout {
		t.Errorf("a member's refusal changed after it left and came back:\n%s%s", notOwner.stdout, rejoined.stdout)
	}
}

// TestTeamRotateSecretRefusals: --secret-file is required, absolute and in
// a directory that exists, all checked before any request; an answer that
// says ok and that the adapter cannot use is `internal`, with a line on
// stderr that says the rotation was made and its secret lost, which
// `internal` alone would not; and a file that cannot be written AFTER the
// rotation is `config` secret_file_unwritable, with the secret on no
// stream.
func TestTeamRotateSecretRefusals(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	b.forget()
	p.alice.fails(protocol.CodeUsage, "", "team", "rotate-secret")
	p.alice.fails(protocol.CodeUsage, "", "team", "rotate-secret", "--secret-file", "relative.secret")
	p.alice.fails(protocol.CodeUsage, "", "team", "rotate-secret", "--secret-file", filepath.Join(t.TempDir(), "s"), "extra")
	missing := p.alice.fails(protocol.CodeConfig, "", "team", "rotate-secret", "--secret-file", filepath.Join(t.TempDir(), "missing", "s"))
	if details(t, missing.stdout)["reason"] != reasonSecretFile {
		t.Errorf("a --secret-file in a directory that does not exist: details %v", details(t, missing.stdout))
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("a refused --secret-file made %d requests, want none", n)
	}

	// An answer that says ok and is not this command's result is `internal`,
	// nothing is written, and stderr says what it cost: the server rotated,
	// so the previous secret is dead and the new one is lost.
	const lostLine = "the previous secret no longer works and the new one is lost: rotate again"
	path := filepath.Join(t.TempDir(), "s.secret")
	elsewhere := protocol.JoinSecretPrefix + "another-team." + strings.Repeat("ab", 16)
	here := protocol.JoinSecretPrefix + teamRef + "." + strings.Repeat("cd", 16)
	answer := func(team, version, secret string) string {
		return okEnvelope(`{"team_ref":"` + team + `","team_name":"ops","secret_version":` + version +
			`,"rotated_at":"2026-11-12T12:00:00Z","join_secret":"` + secret + `"}`)
	}
	for name, body := range map[string]string{
		"another team":               answer("another-team", "2", elsewhere),
		"a secret of another team":   answer(teamRef, "2", elsewhere),
		"a secret that is not one":   answer(teamRef, "2", "not-a-secret"),
		"the created secret's count": answer(teamRef, "1", here),
		"no secret":                  okEnvelope(`{"team_ref":"` + teamRef + `","team_name":"ops","secret_version":2,"rotated_at":"2026-11-12T12:00:00Z"}`),
		"a version that is a string": okEnvelope(`{"team_ref":"` + teamRef + `","team_name":"ops","secret_version":"2","rotated_at":"2026-11-12T12:00:00Z","join_secret":"` + here + `"}`),
	} {
		b.answerWith(only(pathTeamRotateSecret, http.StatusOK, body))
		got := p.alice.fails(protocol.CodeInternal, "", "team", "rotate-secret", "--secret-file", path)
		if strings.Contains(got.stdout+got.stderr, elsewhere) || strings.Contains(got.stdout+got.stderr, here) {
			t.Errorf("%s: the refused answer's secret reached a stream", name)
		}
		if !strings.Contains(got.stderr, lostLine) {
			t.Errorf("%s: stderr does not say the rotation was made and its secret lost: %q", name, got.stderr)
		}
	}
	// What is not an ok answer says nothing of a rotation, and neither does
	// the adapter: a body that is no envelope, and the server's own refusal.
	b.answerWith(only(pathTeamRotateSecret, http.StatusOK, "not an envelope"))
	if got := p.alice.fails(protocol.CodeInternal, "", "team", "rotate-secret", "--secret-file", path); strings.Contains(got.stderr, lostLine) {
		t.Errorf("an answer that is not an envelope: stderr claims a rotation: %q", got.stderr)
	}
	b.answerWith(nil)
	if got := p.bob.fails(protocol.CodeUnauthorized, "", "team", "rotate-secret", "--secret-file", path); strings.Contains(got.stderr, lostLine) {
		t.Errorf("a refused rotation: stderr claims a rotation: %q", got.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a malformed answer wrote the secret file (%v)", err)
	}

	// The directory goes away between the check and the write: the rotation
	// happened, the secret is lost, and the answer says so.
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path == pathTeamRotateSecret {
			_ = os.Remove(dir)
		}
		return nil
	})
	lost := p.alice.fails(protocol.CodeConfig, "", "team", "rotate-secret", "--secret-file", filepath.Join(dir, "s"))
	b.answerWith(nil)
	if details(t, lost.stdout)["reason"] != reasonSecretFile || !strings.Contains(lost.stdout, "rotate again") {
		t.Errorf("a write that failed after the rotation: %s", lost.stdout)
	}
	if strings.Contains(lost.stdout+lost.stderr, protocol.JoinSecretPrefix+teamRef+".") {
		t.Errorf("the lost secret reached stdout or stderr")
	}
	// The rotation did happen: the secret alice created the team with is dead.
	newRig(t, b.url()).fails(protocol.CodeUnauthorized, `{"join_secret":"`+p.secret+`"}`, "team", "join")

	// The ladder is the other commands': an unbound profile is `config`.
	p.bob.ok("", "team", "leave")
	unbound := p.bob.fails(protocol.CodeConfig, "", "team", "rotate-secret", "--secret-file", path)
	if details(t, unbound.stdout)["reason"] != reasonNoTeamBound {
		t.Errorf("an unbound profile: details %v", details(t, unbound.stdout))
	}
}

// TestTeamRevokeMember: the principal form revokes one member, closes its
// session, ends its watch and refuses its commands, and lets it back in
// with the secret; a ban makes its join the rejected join until a
// revocation without ban lifts it; the owner cannot name itself; and a
// member that is not the owner is refused.
func TestTeamRevokeMember(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	alice, bob := p.alice.principalRef(), p.bob.principalRef()

	tm := fastTiming()
	tm.wait = 25 * time.Second
	w := p.bob.watch(tm, "--session", p.bobSes)
	w.ready()
	eventually(t, "the long poll", func() bool { return len(receives(b, p.bobSes)) >= 2 })

	started := time.Now()
	revoked := p.alice.ok("", "team", "revoke-member", "--principal", bob)
	if len(revoked) != 5 || revoked["team_ref"] != teamRef || revoked["principal_ref"] != bob ||
		revoked["status"] != "revoked" || revoked["changed"] != true || revoked["sessions_closed"] != float64(1) {
		t.Errorf("team revoke-member: %v, want {team_ref, principal_ref, status revoked, changed true, sessions_closed 1}", revoked)
	}
	object, _ := w.expect(protocol.EventError)["error"].(map[string]any)
	if object["code"] != "unauthorized" || object["retryable"] != false {
		t.Errorf("the watch of a revoked member: %v, want unauthorized with retryable false", object)
	}
	if code := w.exit(); code != protocol.CodeUnauthorized.Exit() {
		t.Errorf("the watch exited %d, want %d", code, protocol.CodeUnauthorized.Exit())
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("the watch took %s to end after the revocation; the suite gives it 5 s", took)
	}

	// The revoked member's profile still names the team, so its commands
	// reach the server, which refuses each one with the same words.
	var refusals []string
	for _, tc := range []struct {
		input string
		args  []string
	}{
		{"", []string{"session", "list"}},
		{"", []string{"team", "members"}},
		{"", []string{"message", "receive", "--session", p.bobSes}},
		{registration("back"), []string{"session", "register"}},
	} {
		refusals = append(refusals, p.bob.fails(protocol.CodeUnauthorized, tc.input, tc.args...).stdout)
	}
	for _, refusal := range refusals[1:] {
		if refusal != refusals[0] {
			t.Errorf("a revoked member's refusals differ:\n%s%s", refusals[0], refusal)
		}
	}
	if members, _ := p.alice.ok("", "team", "members")["members"].([]any); len(members) != 1 {
		t.Errorf("the roster after a revocation: %d members, want the owner alone", len(members))
	}

	// A repeat moves nothing, and a principal that never was a member is no
	// error.
	again := p.alice.ok(`{"principal_ref":"`+bob+`"}`, "team", "revoke-member")
	if again["changed"] != false || again["sessions_closed"] != float64(0) || again["status"] != "revoked" {
		t.Errorf("a second revocation: %v, want changed false and no session closed", again)
	}
	nobody := p.alice.ok("", "team", "revoke-member", "--principal", strings.Repeat("0", 32), "--ban")
	if nobody["changed"] != false || nobody["status"] != "banned" {
		t.Errorf("a revocation of a principal that never was a member: %v", nobody)
	}

	// The member comes back with the secret: the same membership.
	rejoined := p.bob.join(p.secret, "bob@example.com")
	if rejoined["rejoined"] != true || rejoined["principal_ref"] != bob {
		t.Errorf("a revoked member's join: %v, want rejoined true and the same principal_ref", rejoined)
	}

	// A ban: the correct secret is refused as a wrong one is.
	banned := p.alice.ok(`{"principal_ref":"`+bob+`","ban":true}`, "team", "revoke-member")
	if banned["status"] != "banned" || banned["changed"] != true {
		t.Errorf("team revoke-member with ban: %v", banned)
	}
	right := p.bob.fails(protocol.CodeUnauthorized, `{"join_secret":"`+p.secret+`"}`, "team", "join")
	wrong := p.bob.fails(protocol.CodeUnauthorized,
		`{"join_secret":"`+protocol.JoinSecretPrefix+teamRef+"."+strings.Repeat("0", 32)+`"}`, "team", "join")
	if right.stdout != wrong.stdout {
		t.Errorf("a banned member's join with the secret and with a wrong one differ:\n%s%s", right.stdout, wrong.stdout)
	}
	// And as a principal that was never banned is refused for a wrong one.
	stranger := newRig(t, b.url()).fails(protocol.CodeUnauthorized,
		`{"join_secret":"`+protocol.JoinSecretPrefix+teamRef+"."+strings.Repeat("0", 32)+`"}`, "team", "join")
	if right.stdout != stranger.stdout {
		t.Errorf("a banned member's join and a stranger's with a wrong secret differ:\n%s%s", right.stdout, stranger.stdout)
	}
	if repeat := p.alice.ok("", "team", "revoke-member", "--principal", bob, "--ban"); repeat["changed"] != false {
		t.Errorf("a second ban: %v, want changed false", repeat)
	}
	lifted := p.alice.ok("", "team", "revoke-member", "--principal", bob)
	if lifted["status"] != "revoked" || lifted["changed"] != true || lifted["sessions_closed"] != float64(0) {
		t.Errorf("a revocation without ban of a banned member: %v, want the ban lifted", lifted)
	}
	if back := p.bob.join(p.secret, "bob@example.com"); back["rejoined"] != true {
		t.Errorf("the join of a member whose ban was lifted: %v", back)
	}

	// The owner cannot name itself, and a member is not the owner.
	self := p.alice.fails(protocol.CodeInvalidInput, "", "team", "revoke-member", "--principal", alice)
	if details(t, self.stdout)["field"] != "principal_ref" {
		t.Errorf("the owner naming itself: details %v, want field principal_ref", details(t, self.stdout))
	}
	notOwner := p.bob.fails(protocol.CodeUnauthorized, "", "team", "revoke-member", "--principal", alice)
	if notOwner.stdout != refusals[0] {
		t.Errorf("a member that is not the owner and a revoked member are refused differently:\n%s%s", notOwner.stdout, refusals[0])
	}
	if members, _ := p.alice.ok("", "team", "members")["members"].([]any); len(members) != 2 {
		t.Errorf("the roster at the end: %d members, want 2", len(members))
	}
}

// TestTeamRevokeMemberByVersion is the second step after a rotation: every
// active member that joined with the old secret is revoked, the owner
// never, and a member that came back through the new secret is left alone
// by a second sweep.
func TestTeamRevokeMemberByVersion(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	carol := newRig(t, b.url())
	carol.join(p.secret, "carol@example.com")
	carol.register("carol-session")
	path := filepath.Join(t.TempDir(), "rotated.secret")
	p.alice.ok("", "team", "rotate-secret", "--secret-file", path)
	rotated := secretIn(t, path)

	// Nobody joined with version 0.
	none := p.alice.ok("", "team", "revoke-member", "--max-version", "0")
	if len(none) != 4 || none["team_ref"] != teamRef || none["max_version"] != float64(0) || none["revoked"] != float64(0) || none["sessions_closed"] != float64(0) {
		t.Errorf("a sweep of version 0: %v, want {team_ref, max_version 0, revoked 0, sessions_closed 0}", none)
	}
	swept := p.alice.ok(`{"joined_secret_version_lte":1}`, "team", "revoke-member")
	if swept["max_version"] != float64(1) || swept["revoked"] != float64(2) || swept["sessions_closed"] != float64(2) {
		t.Errorf("a sweep of version 1: %v, want bob and carol revoked and their two sessions closed", swept)
	}
	if _, present := swept["principal_ref"]; present {
		t.Errorf("a version sweep answered a principal_ref: %v", swept)
	}
	if members, _ := p.alice.ok("", "team", "members")["members"].([]any); len(members) != 1 {
		t.Errorf("the roster after the sweep: %d members, want the owner alone", len(members))
	}
	p.bob.fails(protocol.CodeUnauthorized, "", "session", "list")

	// bob comes back through the new secret and is at version 2.
	if back := p.bob.join(rotated, "bob@example.com"); back["rejoined"] != true {
		t.Errorf("a swept member's join with the new secret: %v", back)
	}
	if joined := versions(t, p.alice.ok("", "team", "members")); joined[p.alice.principalRef()] != 1 || joined[p.bob.principalRef()] != 2 {
		t.Errorf("the roster after the rejoin: versions %v, want the owner at 1 and bob at 2", joined)
	}
	if second := p.alice.ok("", "team", "revoke-member", "--max-version", "1"); second["revoked"] != float64(0) {
		t.Errorf("a second sweep of version 1: %v, want nobody revoked", second)
	}
	if high := p.alice.ok("", "team", "revoke-member", "--max-version", strconv.Itoa(protocol.MaxSecretVersion)); high["revoked"] != float64(1) {
		t.Errorf("a sweep of every version: %v, want bob revoked and the owner kept", high)
	}
	if members, _ := p.alice.ok("", "team", "members")["members"].([]any); len(members) != 1 {
		t.Errorf("the roster after the last sweep: %d members, want the owner alone", len(members))
	}
}

// TestTeamRevokeMemberRefusals: both forms on argv, --ban alone, an empty
// --principal and a --max-version outside its range are `usage`; a stdin
// document with neither form, both, ban on the version form or a version
// out of range is `invalid_input` naming the member; none of them makes a
// request.
func TestTeamRevokeMemberRefusals(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	bob := p.bob.principalRef()
	b.forget()
	for _, args := range [][]string{
		{"--principal", bob, "--max-version", "1"},
		{"--ban"},
		{"--ban", "--max-version", "1"},
		{"--principal", ""},
		{"--max-version", "-1"},
		{"--max-version", "2147483648"},
		{"--max-version", "one"},
		{"--principal", bob, "extra"},
	} {
		p.alice.fails(protocol.CodeUsage, "", append([]string{"team", "revoke-member"}, args...)...)
	}
	for stdin, field := range map[string]string{
		`{}`:           "principal_ref",
		`{"ban":true}`: "principal_ref",
		`{"principal_ref":"` + bob + `","joined_secret_version_lte":1}`: "principal_ref",
		`{"joined_secret_version_lte":1,"ban":true}`:                    "ban",
		`{"joined_secret_version_lte":-1}`:                              "joined_secret_version_lte",
		`{"joined_secret_version_lte":2147483648}`:                      "joined_secret_version_lte",
	} {
		got := p.alice.fails(protocol.CodeInvalidInput, stdin, "team", "revoke-member")
		if details(t, got.stdout)["field"] != field {
			t.Errorf("%s: details %v, want field %s", stdin, details(t, got.stdout), field)
		}
	}
	p.alice.fails(protocol.CodeInvalidInput, "not json", "team", "revoke-member")
	p.alice.fails(protocol.CodeInvalidInput, "", "team", "revoke-member")
	if n := len(b.seen("")); n != 0 {
		t.Errorf("the refused requests made %d requests, want none", n)
	}
	// The ladder comes after the request: an unbound profile is `config`.
	p.bob.ok("", "team", "leave")
	unbound := p.bob.fails(protocol.CodeConfig, "", "team", "revoke-member", "--max-version", "1")
	if details(t, unbound.stdout)["reason"] != reasonNoTeamBound {
		t.Errorf("an unbound profile: details %v", details(t, unbound.stdout))
	}
}

// TestTeamTransfer: an active member becomes the owner; the previous owner
// stays a member, keeps its sessions and is refused its next
// administration command; a principal that is not an active member is
// `invalid_input` naming principal_ref; and the owner naming itself is
// accepted and changes nothing.
func TestTeamTransfer(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	alice, bob := p.alice.principalRef(), p.bob.principalRef()
	b.forget()
	p.alice.fails(protocol.CodeUsage, "", "team", "transfer", "--principal", "")
	missing := p.alice.fails(protocol.CodeInvalidInput, `{}`, "team", "transfer")
	if details(t, missing.stdout)["field"] != "principal_ref" {
		t.Errorf("a transfer document without principal_ref: details %v", details(t, missing.stdout))
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("the refused transfers made %d requests, want none", n)
	}

	self := p.alice.ok("", "team", "transfer", "--principal", alice)
	if len(self) != 3 || self["team_ref"] != teamRef || self["principal_ref"] != alice || self["transferred"] != true {
		t.Errorf("the owner naming itself: %v, want {team_ref, principal_ref, transferred true}", self)
	}
	stranger := newRig(t, b.url())
	stranger.create("other", "carol@example.com")
	for name, ref := range map[string]string{
		"a principal of another team": stranger.principalRef(),
		"a principal nobody has":      strings.Repeat("0", 32),
	} {
		got := p.alice.fails(protocol.CodeInvalidInput, "", "team", "transfer", "--principal", ref)
		if details(t, got.stdout)["field"] != "principal_ref" {
			t.Errorf("a transfer to %s: details %v, want field principal_ref", name, details(t, got.stdout))
		}
	}
	notOwner := p.bob.fails(protocol.CodeUnauthorized, "", "team", "transfer", "--principal", bob)

	moved := p.alice.ok(`{"principal_ref":"`+bob+`"}`, "team", "transfer")
	if moved["team_ref"] != teamRef || moved["principal_ref"] != bob || moved["transferred"] != true {
		t.Errorf("team transfer: %v", moved)
	}
	// The previous owner is a member like any other: its administration is
	// refused as bob's was, and everything else of its still works.
	path := filepath.Join(t.TempDir(), "s.secret")
	for _, args := range [][]string{
		{"team", "rotate-secret", "--secret-file", path},
		{"team", "revoke-member", "--principal", bob},
		{"team", "transfer", "--principal", alice},
	} {
		if got := p.alice.fails(protocol.CodeUnauthorized, "", args...); got.stdout != notOwner.stdout {
			t.Errorf("%v by the previous owner: %s, want the refusal a member that is not the owner gets", args, got.stdout)
		}
	}
	sessions, _ := p.alice.ok("", "session", "list")["sessions"].([]any)
	if len(sessions) != 2 {
		t.Errorf("the previous owner's session list: %d sessions, want both", len(sessions))
	}
	// The new owner administers: it rotates, and it may revoke the previous
	// owner.
	if rotated := p.bob.ok("", "team", "rotate-secret", "--secret-file", path); rotated["secret_version"] != float64(2) {
		t.Errorf("the new owner's rotation: %v", rotated)
	}
	if revoked := p.bob.ok("", "team", "revoke-member", "--principal", alice); revoked["changed"] != true || revoked["sessions_closed"] != float64(1) {
		t.Errorf("the new owner revoking the previous one: %v", revoked)
	}
}

// TestTheAdministrationRequestsAreTheContracts pins what each
// administration command sends against the table of the contract's
// section 2: first the question to /v1/info, then one POST whose body is
// the command's input with the profile's team beside it.
func TestTheAdministrationRequestsAreTheContracts(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	alice, bob := p.alice.principalRef(), p.bob.principalRef()
	bearer := "Bearer " + p.alice.storedCredential()

	// sent runs a command for alice and returns the request it made after
	// the one to /v1/info.
	sent := func(path, input string, args ...string) seenRequest {
		t.Helper()
		b.forget()
		if got := p.alice.exec(input, args...); got.code != 0 {
			t.Fatalf("%v: exit %d (%s)", args, got.code, got.stdout)
		}
		seen := b.seen("")
		if len(seen) != 2 || seen[0].Path != pathInfo || seen[0].Raw != "{}" || seen[1].Path != path {
			t.Fatalf("%v made %d requests (%v), want /v1/info with an empty object and then %s", args, len(seen), seen, path)
		}
		if seen[1].Authorization != bearer || seen[1].ContentType != "application/json" {
			t.Errorf("%s: Authorization or Content-Type is not the contract's", path)
		}
		return seen[1]
	}
	members := func(r seenRequest, want map[string]any) {
		t.Helper()
		if len(r.Body) != len(want) {
			t.Errorf("%s: body %s, want exactly %v", r.Path, r.Raw, want)
		}
		for name, value := range want {
			if r.Body[name] != value {
				t.Errorf("%s: body %s, want %s = %v", r.Path, r.Raw, name, value)
			}
		}
	}

	members(sent(pathTeamRevokeMember, "", "team", "revoke-member", "--principal", bob),
		map[string]any{"team_ref": teamRef, "principal_ref": bob})
	members(sent(pathTeamRevokeMember, `{"principal_ref":"`+bob+`","ban":true,"unknown":1}`, "team", "revoke-member"),
		map[string]any{"team_ref": teamRef, "principal_ref": bob, "ban": true})
	members(sent(pathTeamRevokeMember, "", "team", "revoke-member", "--max-version", "0"),
		map[string]any{"team_ref": teamRef, "joined_secret_version_lte": float64(0)})
	members(sent(pathTeamRevokeMember, `{"joined_secret_version_lte":7,"principal_ref":""}`, "team", "revoke-member"),
		map[string]any{"team_ref": teamRef, "joined_secret_version_lte": float64(7)})
	members(sent(pathTeamTransfer, "", "team", "transfer", "--principal", alice),
		map[string]any{"team_ref": teamRef, "principal_ref": alice})
	members(sent(pathTeamRotateSecret, "", "team", "rotate-secret", "--secret-file", filepath.Join(t.TempDir(), "s")),
		map[string]any{"team_ref": teamRef})
}

// TestAnAdministrationAnswerAboutSomethingElseIsInternal: a result that
// has the command's shape and speaks of another team, another principal,
// another version or another status than the one asked for is not this
// command's answer.
func TestAnAdministrationAnswerAboutSomethingElseIsInternal(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	bob := p.bob.principalRef()
	revoked := func(team, principal, status string) string {
		return okEnvelope(`{"team_ref":"` + team + `","principal_ref":"` + principal + `","status":"` + status + `","changed":true,"sessions_closed":0}`)
	}
	swept := func(team, version string) string {
		return okEnvelope(`{"team_ref":"` + team + `","max_version":` + version + `,"revoked":0,"sessions_closed":0}`)
	}
	moved := func(team, principal, transferred string) string {
		return okEnvelope(`{"team_ref":"` + team + `","principal_ref":"` + principal + `","transferred":` + transferred + `}`)
	}
	for _, tc := range []struct {
		name, path, body string
		args             []string
	}{
		{"a revocation in another team", pathTeamRevokeMember, revoked("elsewhere", bob, "revoked"), []string{"team", "revoke-member", "--principal", bob}},
		{"a revocation of another principal", pathTeamRevokeMember, revoked(teamRef, "someone", "revoked"), []string{"team", "revoke-member", "--principal", bob}},
		{"a ban that was not asked for", pathTeamRevokeMember, revoked(teamRef, bob, "banned"), []string{"team", "revoke-member", "--principal", bob}},
		{"a status that is none", pathTeamRevokeMember, revoked(teamRef, bob, "active"), []string{"team", "revoke-member", "--principal", bob}},
		{"a sweep answered as one member", pathTeamRevokeMember, revoked(teamRef, bob, "revoked"), []string{"team", "revoke-member", "--max-version", "1"}},
		{"a sweep of another version", pathTeamRevokeMember, swept(teamRef, "2"), []string{"team", "revoke-member", "--max-version", "1"}},
		{"a sweep in another team", pathTeamRevokeMember, swept("elsewhere", "1"), []string{"team", "revoke-member", "--max-version", "1"}},
		{"a transfer to another principal", pathTeamTransfer, moved(teamRef, "someone", "true"), []string{"team", "transfer", "--principal", bob}},
		{"a transfer in another team", pathTeamTransfer, moved("elsewhere", bob, "true"), []string{"team", "transfer", "--principal", bob}},
		{"a transfer that did not happen", pathTeamTransfer, moved(teamRef, bob, "false"), []string{"team", "transfer", "--principal", bob}},
	} {
		b.answerWith(only(tc.path, http.StatusOK, tc.body))
		got := p.alice.fails(protocol.CodeInternal, "", tc.args...)
		if details(t, got.stdout)["reason"] != reasonBackendResponse {
			t.Errorf("%s: details %v, want reason %s", tc.name, details(t, got.stdout), reasonBackendResponse)
		}
	}
}

// TestAServerWithoutTeamAdministration: a server that leaves the three
// endpoints out lists no `team.admin` in /v1/info (the contract's section
// 5). The adapter asks before each administration command and answers
// `config` server_lacks_team_admin, in words an administrator can act on,
// without sending the request such a server would answer `not_found`.
// `describe` names the capability either way, because it never asks.
func TestAServerWithoutTeamAdministration(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, b *backend) {
		t.Helper()
		p := newPair(t, b)
		bob := p.bob.principalRef()
		path := filepath.Join(t.TempDir(), "s.secret")
		for _, args := range [][]string{
			{"team", "rotate-secret", "--secret-file", path},
			{"team", "revoke-member", "--principal", bob},
			{"team", "revoke-member", "--max-version", "1"},
			{"team", "transfer", "--principal", bob},
		} {
			b.forget()
			got := p.alice.fails(protocol.CodeConfig, "", args...)
			if details(t, got.stdout)["reason"] != reasonServerLacksTeamAdmin {
				t.Errorf("%v: details %v, want reason %s", args, details(t, got.stdout), reasonServerLacksTeamAdmin)
			}
			if !strings.Contains(got.stdout, "does not offer team administration") || !strings.Contains(got.stdout, "operator") {
				t.Errorf("%v: the message does not say what is missing and whose it is to mend: %s", args, got.stdout)
			}
			if seen := b.seen(""); len(seen) != 1 || seen[0].Path != pathInfo {
				t.Errorf("%v made %v, want the one request to /v1/info", args, seen)
			}
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("a refused rotation wrote the secret file (%v)", err)
		}
		if members, _ := p.alice.ok("", "team", "members")["members"].([]any); len(members) != 2 {
			t.Errorf("the roster: %d members, want both: nothing was revoked", len(members))
		}
		capabilities, _ := p.alice.ok("", "describe")["capabilities"].([]any)
		named := false
		for _, c := range capabilities {
			named = named || c == capTeamAdmin
		}
		if !named {
			t.Errorf("describe does not name %s; it never asks the server, so it names it either way", capTeamAdmin)
		}
	}
	t.Run("a server that leaves the endpoints out", func(t *testing.T) {
		t.Parallel()
		b := newBackendWith(t, stub.Options{Poll: 20 * time.Millisecond, WithoutTeamAdmin: true})
		check(t, b)
		p := newPair(t, b)
		if joined := versions(t, p.alice.ok("", "team", "members")); len(joined) != 0 {
			t.Errorf("the roster of a server without team administration carries joined_secret_version: %v", joined)
		}
	})
	t.Run("an empty capabilities list", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		b.answerWith(only(pathInfo, http.StatusOK, okEnvelope(`{"contract_version":"1","server":{"name":"s","version":"1"},"capabilities":[]}`)))
		check(t, b)
	})
	t.Run("capabilities that name others", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		b.answerWith(only(pathInfo, http.StatusOK, okEnvelope(`{"contract_version":"1","capabilities":["team.administration","TEAM.ADMIN"]}`)))
		check(t, b)
	})

	// A server that cannot be asked is the failure it is, not a missing
	// capability.
	b := newBackend(t)
	p := newPair(t, b)
	b.answerWith(only(pathInfo, http.StatusServiceUnavailable, "down"))
	p.alice.fails(protocol.CodeUnavailable, "", "team", "transfer", "--principal", p.bob.principalRef())
	b.answerWith(only(pathInfo, http.StatusOK, okEnvelope(`{"server":{"name":"s"}}`)))
	p.alice.fails(protocol.CodeInternal, "", "team", "transfer", "--principal", p.bob.principalRef())
}
