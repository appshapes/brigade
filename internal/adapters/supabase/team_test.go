package supabase

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// testSecret is a well-formed join secret for testTeamID. It is not a
// real secret: the fake backend accepts whatever the test scripts.
var testSecret = protocol.JoinSecretPrefix + testTeamID + "." + strings.Repeat("ab", 16)

// otherTeamID is a second uuid-shaped team id.
const otherTeamID = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"

// answerRPC scripts one function's 200 answer and records its arguments;
// any other function answers 404 PGRST202 (migration drift) so a verb
// calling the wrong RPC fails loudly.
func answerRPC(t *testing.T, be *fakeBackend, fn, body string) *map[string]any {
	t.Helper()
	var got map[string]any
	be.onRPC = func(w http.ResponseWriter, _ *http.Request, called, _ string, args map[string]any) {
		if called != fn {
			postgrest(w, http.StatusNotFound, "PGRST202", "Could not find the function")
			return
		}
		got = args
		writeJSON(w, http.StatusOK, body)
	}
	return &got
}

// createdBody is create_team's answer for testTeamID, team "ops".
func createdBody() string {
	return `{"status":"created","team_id":"` + testTeamID + `","team_name":"ops","join_secret":"` + testSecret + `"}`
}

// joinedBody is join_team's answer.
func joinedBody(rejoined bool, failures int) string {
	r := "false"
	if rejoined {
		r = "true"
	}
	return `{"status":"joined","team_id":"` + testTeamID + `","team_name":"ops","rejoined":` + r + `,"team_failures":` + itoa(failures) + `}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// withBackendEnv is the rig's environment plus the BRIGADE_SUPABASE_*
// pair naming the fake backend.
func (r *rig) withBackendEnv() []string {
	return append(append([]string{}, r.env...), backendURLVar+"="+r.be.srv.URL, backendKeyVar+"="+testKey)
}

// noSecretLeak asserts that neither stream nor any file of the profile
// directory carries a join secret (4.5.14, C-05).
func noSecretLeak(t *testing.T, r *rig, got outcome, allowStdout bool) {
	t.Helper()
	if !allowStdout && strings.Contains(got.stdout, protocol.JoinSecretPrefix) {
		t.Fatalf("stdout carries a join secret: %s", got.stdout)
	}
	if strings.Contains(got.stderr, protocol.JoinSecretPrefix) {
		t.Fatalf("stderr carries a join secret: %s", got.stderr)
	}
	for _, rel := range entries(t, r.profileDir()) {
		data, err := os.ReadFile(filepath.Join(r.profileDir(), rel)) //nolint:gosec // the rig's own temp files
		if err == nil && strings.Contains(string(data), protocol.JoinSecretPrefix) {
			t.Fatalf("%s carries a join secret", rel)
		}
	}
}

// TestTeamCreateReturnsAParseableSecret covers C-03 against the fake
// backend: the profile is initialised, session.json is absent, so `team
// create` signs up once, calls create_team with the name and label, binds
// the profile and prints the backend's secret once with the stderr
// warning. The secret is in no file and in no log line.
func TestTeamCreateReturnsAParseableSecret(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	args := answerRPC(t, r.be, rpcCreateTeam, createdBody())
	got := r.exec(`{"team_name":"ops","human_label":"alice@example.com"}`, "--log-level", "debug", "team", "create")
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	secret, err := protocol.ParseJoinSecret(str(t, result, "join_secret"))
	if err != nil || secret.TeamRef() != testTeamID {
		t.Fatalf("join_secret does not parse or names another team: %v", err)
	}
	if str(t, result, "team_ref") != testTeamID || str(t, result, "team_name") != "ops" || str(t, result, "principal_ref") != testUserID {
		t.Fatalf("result = %v", result)
	}
	if (*args)["p_name"] != "ops" || (*args)["p_human_label"] != "alice@example.com" {
		t.Fatalf("create_team args = %v", *args)
	}
	if r.be.calls("/auth/v1/signup") != 1 {
		t.Fatalf("sign-ups = %d, want exactly one", r.be.calls("/auth/v1/signup"))
	}
	if !strings.Contains(got.stderr, "printed once") {
		t.Fatalf("no shown-once warning on stderr: %s", got.stderr)
	}
	noSecretLeak(t, r, got, true)
	s := r.readSession()
	if s == nil || s.LastTeamRef != testTeamID {
		t.Fatalf("session.json = %+v, want last_team_ref", s)
	}
	d := r.ok("describe")
	profile, _ := d["profile"].(map[string]any)
	if profile["state"] != "joined" || profile["team_ref"] != testTeamID || profile["human_label"] != "alice@example.com" || profile["principal_ref"] != testUserID {
		t.Fatalf("describe.profile = %v", profile)
	}
}

// TestTeamCreateBindingConflict covers C-03b: on a bound profile a second
// `team create` and a `team join` naming ANOTHER team are `conflict`
// profile_bound without any network call, while a join naming the bound
// team is a rejoin that reaches join_team.
func TestTeamCreateBindingConflict(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	other := protocol.JoinSecretPrefix + otherTeamID + "." + strings.Repeat("cd", 16)
	for _, attempt := range []struct {
		input string
		args  []string
	}{
		{`{"team_name":"second"}`, []string{"team", "create"}},
		{`{"join_secret":"` + other + `"}`, []string{"team", "join"}},
	} {
		got := r.fails("conflict", 7, attempt.input, attempt.args...)
		if details(t, got.stdout)["reason"] != reasonProfileBound {
			t.Fatalf("%v: details = %v", attempt.args, details(t, got.stdout))
		}
	}
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls, want none before the binding check", r.be.total())
	}
	args := answerRPC(t, r.be, rpcJoinTeam, joinedBody(true, 0))
	got := r.exec(`{"join_secret":"`+testSecret+`"}`, "team", "join")
	if got.code != 0 {
		t.Fatalf("rejoin: exit %d %s", got.code, got.stdout)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if result["rejoined"] != true || (*args)["p_join_secret"] != testSecret {
		t.Fatalf("rejoin result %v args %v", result, *args)
	}
	// The label was not in the request: the profile's remembered one is
	// sent, the fs adapter's rule, so the membership keeps it.
	if (*args)["p_human_label"] != "alice@example.com" {
		t.Fatalf("p_human_label = %v, want the profile's label", (*args)["p_human_label"])
	}
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil || p.HumanLabel != "alice@example.com" {
		t.Fatalf("profile after rejoin: %+v %v", p, err)
	}
}

// TestTeamCreateWithSecretFileOmitsTheSecret covers 4.4.10's named
// exception: the secret goes to a 0600 file and NOT to stdout; the
// result carries the other three members.
func TestTeamCreateWithSecretFileOmitsTheSecret(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	answerRPC(t, r.be, rpcCreateTeam, createdBody())
	path := filepath.Join(r.base, "secret.txt")
	got := r.exec(`{"team_name":"ops"}`, "team", "create", "--secret-file", path)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	noSecretLeak(t, r, got, false)
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if _, present := result["join_secret"]; present || result["team_ref"] != testTeamID {
		t.Fatalf("result = %v", result)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file: %v %v", info, err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // the test's own temp file
	if err != nil || string(data) != testSecret+"\n" {
		t.Fatalf("secret file content = %q %v", data, err)
	}
	if !strings.Contains(got.stderr, "--secret-file") {
		t.Fatalf("no warning naming the file on stderr: %s", got.stderr)
	}
}

// TestTeamCreateSecretFileRefusals: a relative --secret-file is `usage`
// and a missing parent directory is `config`, both before any network
// call; a directory that vanishes between the check and the write leaves
// the profile unbound.
func TestTeamCreateSecretFileRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	r.fails("usage", 2, `{"team_name":"ops"}`, "team", "create", "--secret-file", "relative.txt")
	got := r.fails("config", 11, `{"team_name":"ops"}`, "team", "create", "--secret-file", filepath.Join(r.base, "missing", "s.txt"))
	if details(t, got.stdout)["reason"] != "secret_file_unwritable" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls before the local checks passed", r.be.total())
	}
	dir := filepath.Join(r.base, "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		_ = os.Remove(dir)
		writeJSON(w, http.StatusOK, createdBody())
	}
	got = r.fails("config", 11, `{"team_name":"ops"}`, "team", "create", "--secret-file", filepath.Join(dir, "s.txt"))
	noSecretLeak(t, r, got, false)
	if state := str(t, r.ok("profile", "status"), "state"); state != protocol.ProfileStateNotMember {
		t.Fatalf("profile state after the failed write = %q, want not_member (unbound, credential kept)", state)
	}
}

// TestTeamCreateFlagsInsteadOfStdin: --name and --label build the request
// without reading stdin (a terminal there would otherwise be `usage`).
func TestTeamCreateFlagsInsteadOfStdin(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	args := answerRPC(t, r.be, rpcCreateTeam, createdBody())
	got := r.exec("", "team", "create", "--name", "ops", "--label", "alice@example.com")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if (*args)["p_name"] != "ops" || (*args)["p_human_label"] != "alice@example.com" {
		t.Fatalf("args = %v", *args)
	}
	// --label overrides the document's label.
	r2 := newRig(t)
	r2.initProfile()
	args = answerRPC(t, r2.be, rpcCreateTeam, createdBody())
	if got := r2.exec(`{"team_name":"ops","human_label":"doc@example.com"}`, "team", "create", "--label", "flag@example.com"); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if (*args)["p_human_label"] != "flag@example.com" {
		t.Fatalf("args = %v", *args)
	}
}

// TestPromptWithoutATerminalIsUsage is B-7 for both verbs: stdin is a
// pipe carrying a valid document, and --prompt still answers `usage`
// without touching the backend.
func TestPromptWithoutATerminalIsUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	r.fails("usage", 2, "{\"team_name\": \"ops\",\n\"human_label\": \"x\"}\n", "team", "create", "--prompt")
	r.fails("usage", 2, "{\"join_secret\": \""+testSecret+"\",\n\"human_label\": \"x\"}\n", "team", "join", "--prompt")
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls, want none", r.be.total())
	}
}

// TestTeamCreateNeedsABackend: with no team.json and no environment
// pair `team create` is `config` profile_missing before stdin is read; a
// profile without a backend is `config` backend_unconfigured; the
// BRIGADE_SUPABASE_* pair bootstraps a fresh profile exactly as `profile
// init` would (the file is written, the environment never read again),
// and never overrides a configured profile.
func TestTeamCreateNeedsABackend(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := r.fails("config", 11, `{"team_name":"ops"}`, "team", "create")
	if details(t, got.stdout)["reason"] != reasonProfileMissing {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	absent(t, r.profileDir())
	if err := adapterkit.SaveProfile(r.cfg, "default", &adapterkit.Profile{Version: adapterkit.ProfileVersion, Adapter: adapterKind}); err != nil {
		t.Fatal(err)
	}
	got = r.fails("config", 11, `{"team_name":"ops"}`, "team", "create")
	if details(t, got.stdout)["reason"] != reasonBackendUnconfigured {
		t.Fatalf("details = %v", details(t, got.stdout))
	}

	fresh := newRig(t)
	answerRPC(t, fresh.be, rpcCreateTeam, createdBody())
	got = fresh.execEnv(fresh.withBackendEnv(), `{"team_name":"ops"}`, "team", "create")
	if got.code != 0 {
		t.Fatalf("bootstrapped create: exit %d %s %s", got.code, got.stdout, got.stderr)
	}
	p, err := adapterkit.LoadProfile(fresh.cfg, "default")
	if err != nil || p.URL != fresh.be.srv.URL || p.PublishableKey != testKey || p.TeamRef != testTeamID || p.Adapter != adapterKind {
		t.Fatalf("bootstrapped profile = %+v %v", p, err)
	}
	// Without the pair, every later command reads the file: describe and
	// team members work with the rig's plain environment.
	answerRPC(t, fresh.be, rpcListMembers, `{"team_ref":"`+testTeamID+`","team_name":"ops","server_time":"2026-09-02T12:00:00+00:00","members":[]}`)
	fresh.ok("team", "members")

	// A configured profile pointing elsewhere is not overridden by the
	// environment pair.
	configured := newRig(t)
	configured.initProfile()
	answerRPC(t, configured.be, rpcCreateTeam, createdBody())
	env := append(append([]string{}, configured.env...), backendURLVar+"=https://elsewhere.example", backendKeyVar+"=sb_publishable_other")
	if got := configured.execEnv(env, `{"team_name":"ops"}`, "team", "create"); got.code != 0 {
		t.Fatalf("exit %d %s", got.code, got.stdout)
	}
	p, _ = adapterkit.LoadProfile(configured.cfg, "default")
	if p.URL != configured.be.srv.URL {
		t.Fatalf("the environment overrode the profile's backend: %q", p.URL)
	}

	// An unusable pair (http, non-loopback) is `config`.
	bad := newRig(t)
	env = append(append([]string{}, bad.env...), backendURLVar+"=http://elsewhere.example", backendKeyVar+"=k")
	got = bad.execEnv(env, `{"team_name":"ops"}`, "team", "create")
	if got.code != 11 {
		t.Fatalf("exit %d, want config: %s", got.code, got.stdout)
	}
}

// TestTeamCreateOrderOfChecks (section 3): a refused document on a fresh
// profile creates nothing — no team.json, no sign-up, no RPC — and a
// backend refusal after sign-up leaves the credential but no binding.
func TestTeamCreateOrderOfChecks(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := r.execEnv(r.withBackendEnv(), `{"team_name":""}`, "team", "create")
	if got.code != 3 {
		t.Fatalf("exit %d, want invalid_input: %s", got.code, got.stdout)
	}
	absent(t, r.profileDir())
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls for a refused document", r.be.total())
	}
	r.initProfile()
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusBadRequest, "P0001", "brigade:rate_limited:team_create:3600")
	}
	got = r.fails("rate_limited", 8, `{"team_name":"ops"}`, "team", "create")
	env := decode(t, got.stdout)
	errObj, _ := env["error"].(map[string]any)
	if errObj["retry_after_ms"] != float64(3600000) || details(t, got.stdout)["reason"] != "team_create" {
		t.Fatalf("error = %v", errObj)
	}
	if r.readSession() == nil {
		t.Fatalf("the credential minted before the refusal is gone")
	}
	if state := str(t, r.ok("profile", "status"), "state"); state != protocol.ProfileStateNotMember {
		t.Fatalf("state = %q, want not_member", state)
	}
	// An answer this adapter cannot read is `internal`, never a binding.
	answerRPC(t, r.be, rpcCreateTeam, `{"status":"created","team_id":"not-a-uuid","team_name":"ops","join_secret":"x"}`)
	r.fails("internal", 1, `{"team_name":"ops"}`, "team", "create")
	answerRPC(t, r.be, rpcCreateTeam, `{"status":"created","team_id":"`+testTeamID+`","team_name":"ops","join_secret":"brg1.`+otherTeamID+`.abc"}`)
	r.fails("internal", 1, `{"team_name":"ops"}`, "team", "create")
	if state := str(t, r.ok("profile", "status"), "state"); state != protocol.ProfileStateNotMember {
		t.Fatalf("state = %q, want not_member after unreadable answers", state)
	}
}

// TestJoinRefusalsAreByteIdentical covers C-04's identity rule against
// every 200 status join_team answers: invalid_secret (wrong secret,
// unknown team, banned principal) and invalid_input (a well-formed secret
// whose team_ref is not a backend id) are one fixed `unauthorized` with
// no details; rate_limited carries the limiter's retry_after; a malformed
// secret is `invalid_input` naming join_secret without echoing it; an
// unknown status is `internal`. Nothing binds, and no secret is stored.
func TestJoinRefusalsAreByteIdentical(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	want := newFailure(t, errSecretRejected())
	for _, body := range []string{`{"status": "invalid_secret"}`, `{"status": "invalid_input"}`} {
		answerRPC(t, r.be, rpcJoinTeam, body)
		got := r.fails("unauthorized", 5, `{"join_secret":"`+testSecret+`","human_label":"x"}`, "--log-level", "debug", "team", "join")
		if got.stdout != want {
			t.Fatalf("%s: stdout %q, want %q", body, got.stdout, want)
		}
		noSecretLeak(t, r, got, false)
	}
	answerRPC(t, r.be, rpcJoinTeam, `{"status":"rate_limited","retry_after_seconds":120}`)
	got := r.fails("rate_limited", 8, `{"join_secret":"`+testSecret+`"}`, "team", "join")
	errObj, _ := decode(t, got.stdout)["error"].(map[string]any)
	if errObj["retry_after_ms"] != float64(120000) || details(t, got.stdout)["reason"] != reasonJoinAttempts {
		t.Fatalf("rate_limited error = %v", errObj)
	}
	answerRPC(t, r.be, rpcJoinTeam, `{"status":"weird"}`)
	r.fails("internal", 1, `{"join_secret":"`+testSecret+`"}`, "team", "join")
	answerRPC(t, r.be, rpcJoinTeam, `{"status":"joined","team_id":"`+otherTeamID+`","team_name":"ops","rejoined":false}`)
	r.fails("internal", 1, `{"join_secret":"`+testSecret+`"}`, "team", "join")

	calls := r.be.calls(rpcPath)
	got = r.fails("invalid_input", 3, `{"join_secret":"nope-not-a-secret","human_label":"x"}`, "team", "join")
	if details(t, got.stdout)["field"] != "join_secret" || strings.Contains(got.stdout+got.stderr, "nope-not") {
		t.Fatalf("malformed secret: %s %s", got.stdout, got.stderr)
	}
	if r.be.calls(rpcPath) != calls {
		t.Fatalf("a malformed secret reached the backend")
	}
	if state := str(t, r.ok("profile", "status"), "state"); state != protocol.ProfileStateNotMember {
		t.Fatalf("state = %q, want not_member: a refused join must not bind", state)
	}
}

// TestJoinBindsTheProfile covers C-04's positive half: the secret and the
// label reach join_team, the answer binds the profile with the backend's
// team name, rejoined is answered as given, a non-zero team_failures is
// logged at warn without the secret, and the secret is stored nowhere.
func TestJoinBindsTheProfile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	args := answerRPC(t, r.be, rpcJoinTeam, joinedBody(false, 3))
	got := r.exec(`{"join_secret":"`+testSecret+`","human_label":"bob@example.com"}`, "--log-level", "debug", "team", "join")
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if result["team_ref"] != testTeamID || result["team_name"] != "ops" || result["principal_ref"] != testUserID || result["rejoined"] != false {
		t.Fatalf("result = %v", result)
	}
	if (*args)["p_join_secret"] != testSecret || (*args)["p_human_label"] != "bob@example.com" {
		t.Fatalf("join_team args = %v", *args)
	}
	if !strings.Contains(got.stderr, "team_failures") {
		t.Fatalf("no warn line for team_failures: %s", got.stderr)
	}
	noSecretLeak(t, r, got, false)
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil || p.TeamRef != testTeamID || p.TeamName != "ops" || p.HumanLabel != "bob@example.com" || p.PrincipalRef != testUserID {
		t.Fatalf("profile = %+v %v", p, err)
	}
	if r.be.calls("/auth/v1/signup") != 1 {
		t.Fatalf("sign-ups = %d", r.be.calls("/auth/v1/signup"))
	}
	// --label overrides the document's label.
	r2 := newRig(t)
	r2.initProfile()
	args = answerRPC(t, r2.be, rpcJoinTeam, joinedBody(false, 0))
	if got := r2.exec(`{"join_secret":"`+testSecret+`","human_label":"doc@example.com"}`, "team", "join", "--label", "flag@example.com"); got.code != 0 {
		t.Fatalf("exit %d", got.code)
	}
	if (*args)["p_human_label"] != "flag@example.com" {
		t.Fatalf("args = %v", *args)
	}
}

// TestJoinBackendMember covers 4.4.10's adapter-specific `backend`
// member: on a profile with no backend it configures the profile
// (checked with the https rule and the secret-key refusal); a non-object
// (B-8) or an incomplete object is `invalid_input` naming backend; on a
// configured profile the same pair is accepted and a different one is
// `conflict`.
func TestJoinBackendMember(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := r.fails("invalid_input", 3, `{"join_secret":"`+testSecret+`","backend":"x"}`, "team", "join")
	if details(t, got.stdout)["field"] != "backend" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	got = r.fails("invalid_input", 3, `{"join_secret":"`+testSecret+`","backend":{"url":"`+r.be.srv.URL+`"}}`, "team", "join")
	if details(t, got.stdout)["field"] != "backend" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	got = r.fails("invalid_input", 3, `{"join_secret":"`+testSecret+`","backend":{"url":"http://elsewhere.example","publishable_key":"k"}}`, "team", "join")
	if details(t, got.stdout)["field"] != "url" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	got = r.fails("invalid_input", 3, `{"join_secret":"`+testSecret+`","backend":{"url":"`+r.be.srv.URL+`","publishable_key":"`+secretKeyPrefix()+`abc"}}`, "team", "join")
	if details(t, got.stdout)["field"] != "key" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	absent(t, r.profileDir())

	answerRPC(t, r.be, rpcJoinTeam, joinedBody(false, 0))
	got = r.exec(`{"join_secret":"`+testSecret+`","backend":{"url":"`+r.be.srv.URL+`/","publishable_key":"`+testKey+`"}}`, "team", "join")
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil || p.URL != r.be.srv.URL || p.PublishableKey != testKey || p.TeamRef != testTeamID {
		t.Fatalf("profile = %+v %v", p, err)
	}

	configured := newRig(t)
	configured.initProfile()
	got = configured.fails("conflict", 7, `{"join_secret":"`+testSecret+`","backend":{"url":"https://elsewhere.example","publishable_key":"`+testKey+`"}}`, "team", "join")
	if details(t, got.stdout)["reason"] != reasonProfileExists || configured.be.total() != 0 {
		t.Fatalf("details = %v, calls %d", details(t, got.stdout), configured.be.total())
	}
	answerRPC(t, configured.be, rpcJoinTeam, joinedBody(false, 0))
	if got := configured.exec(`{"join_secret":"`+testSecret+`","backend":{"url":"`+configured.be.srv.URL+`","publishable_key":"`+testKey+`"}}`, "team", "join"); got.code != 0 {
		t.Fatalf("the same backend again: exit %d %s", got.code, got.stdout)
	}
}

// TestLeaveIsIdempotent covers C-08's leave half: leave_team is called
// with the bound team, the profile is unbound and the credential kept
// (not_member), a second leave answers the remembered team with no
// network call, and a never-bound profile is `config`.
func TestLeaveIsIdempotent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	args := answerRPC(t, r.be, rpcLeaveTeam, `{"team_id":"`+testTeamID+`","left":true}`)
	result := r.ok("team", "leave")
	if result["team_ref"] != testTeamID || result["principal_ref"] != testUserID || result["left"] != true {
		t.Fatalf("result = %v", result)
	}
	if (*args)["p_team_id"] != testTeamID {
		t.Fatalf("leave_team args = %v", *args)
	}
	status := r.ok("profile", "status")
	if status["state"] != protocol.ProfileStateNotMember || status["team_ref"] != nil {
		t.Fatalf("profile status after leave = %v", status)
	}
	if s := r.readSession(); s == nil || s.LastTeamRef != testTeamID {
		t.Fatalf("session.json after leave = %+v", s)
	}
	calls := r.be.calls(rpcPath)
	again := r.ok("team", "leave")
	if again["team_ref"] != testTeamID || again["left"] != true || r.be.calls(rpcPath) != calls {
		t.Fatalf("second leave = %v (rpc calls %d → %d)", again, calls, r.be.calls(rpcPath))
	}

	never := newRig(t)
	never.initProfile()
	never.writeSession(never.session(time.Hour, "rt-1"))
	got := never.fails("config", 11, "", "team", "leave")
	if details(t, got.stdout)["reason"] != reasonNoTeamBound {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	// The ladder: no credential is unauthenticated, no profile is config.
	noCred := newRig(t)
	noCred.initProfile()
	noCred.fails("unauthenticated", 4, "", "team", "leave")
	newRig(t).fails("config", 11, "", "team", "leave")
}

// TestLeaveKeepsTheBindingOnABackendFailure: leave_team refused (a 5xx,
// a revoked credential) leaves team.json bound so the leave can be
// retried; a bound team_ref that is not a backend id is unbound locally
// without a call.
func TestLeaveKeepsTheBindingOnABackendFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		writeJSON(w, http.StatusBadGateway, `<html>bad gateway</html>`)
	}
	r.fails("unavailable", 9, "", "team", "leave")
	if state := str(t, r.ok("profile", "status"), "state"); state != protocol.ProfileStateJoined {
		t.Fatalf("state = %q, want joined after a failed leave", state)
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusUnauthorized, "28000", "brigade:unauthenticated")
	}
	r.fails("unauthenticated", 4, "", "team", "leave")

	hex := newRig(t)
	hex.joined()
	hex.bindTeam("team-0123456789abcdef", "ops")
	result := hex.ok("team", "leave")
	if result["team_ref"] != "team-0123456789abcdef" || hex.be.calls(rpcPath) != 0 {
		t.Fatalf("result = %v, rpc calls %d", result, hex.be.calls(rpcPath))
	}
	if state := str(t, hex.ok("profile", "status"), "state"); state != protocol.ProfileStateNotMember {
		t.Fatalf("state = %q", state)
	}
}

// TestTeamMembersRoster covers C-43: the RPC's jsonb is re-emitted in the
// protocol's shape — joined_secret_version dropped, a null human_label
// omitted, a null last_seen_at kept as null, an empty roster as `[]` —
// and a non-member is the uniform `unauthorized`, byte-identical whether
// the backend raised it or the bound team_ref could name no team.
func TestTeamMembersRoster(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	args := answerRPC(t, r.be, rpcListMembers, `{"team_ref":"`+testTeamID+`","team_name":"ops","server_time":"2026-09-02T12:00:00.123456+00:00","members":[`+
		`{"principal_ref":"`+testUserID+`","human_label":"alice@example.com","status":"active","joined_at":"2026-09-02T11:50:00+00:00","joined_secret_version":1,"last_seen_at":"2026-09-02T11:59:00+00:00","session_count":2},`+
		`{"principal_ref":"`+otherTeamID+`","human_label":null,"status":"active","joined_at":"2026-09-02T11:52:00+00:00","joined_secret_version":1,"last_seen_at":null,"session_count":0}]}`)
	got := r.exec("", "team", "members")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if (*args)["p_team_id"] != testTeamID {
		t.Fatalf("list_members args = %v", *args)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if result["team_ref"] != testTeamID || result["team_name"] != "ops" {
		t.Fatalf("result = %v", result)
	}
	if _, err := time.Parse(time.RFC3339Nano, str(t, result, "server_time")); err != nil {
		t.Fatalf("server_time: %v", err)
	}
	members, _ := result["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %v", result["members"])
	}
	first, _ := members[0].(map[string]any)
	second, _ := members[1].(map[string]any)
	if first["human_label"] != "alice@example.com" || first["session_count"] != float64(2) || first["last_seen_at"] == nil {
		t.Fatalf("members[0] = %v", first)
	}
	if _, present := first["joined_secret_version"]; present {
		t.Fatalf("joined_secret_version leaked into the roster: %v", first)
	}
	if _, present := second["human_label"]; present {
		t.Fatalf("a null human_label was emitted: %v", second)
	}
	if v, present := second["last_seen_at"]; !present || v != nil {
		t.Fatalf("last_seen_at = %v (present %v), want null", v, present)
	}
	if !strings.Contains(got.stdout, `"last_seen_at":null`) {
		t.Fatalf("stdout does not carry a literal null last_seen_at: %s", got.stdout)
	}

	answerRPC(t, r.be, rpcListMembers, `{"team_ref":"`+testTeamID+`","team_name":"ops","server_time":"2026-09-02T12:00:00+00:00","members":[]}`)
	got = r.exec("", "team", "members")
	if !strings.Contains(got.stdout, `"members":[]`) {
		t.Fatalf("an empty roster is not []: %s", got.stdout)
	}

	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized")
	}
	raised := r.fails("unauthorized", 5, "", "team", "members")
	rewriteProfile(t, r, func(m map[string]any) { m["team_ref"] = "team-0123456789abcdef" })
	local := r.fails("unauthorized", 5, "", "team", "members")
	if raised.stdout != local.stdout || raised.stdout != newFailure(t, errNotMember()) {
		t.Fatalf("unauthorized differs: %q vs %q", raised.stdout, local.stdout)
	}
	// The ladder: an unbound profile is config, no credential unauthenticated.
	unbound := newRig(t)
	unbound.initProfile()
	unbound.writeSession(unbound.session(time.Hour, "rt-1"))
	unbound.fails("config", 11, "", "team", "members")
	noCred := newRig(t)
	noCred.initProfile()
	noCred.fails("unauthenticated", 4, "", "team", "members")
}

// TestTeamVerbsRefuseArgv: the poison flag, an unknown flag and a stray
// positional are `usage` on every team verb, before anything is read.
func TestTeamVerbsRefuseArgv(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	for _, verb := range []string{"create", "join", "leave", "members", "rotate-secret", "revoke-member", "transfer"} {
		got := r.fails("usage", 2, "", "team", verb, "--join-secret", "brg1.x.y")
		if strings.Contains(got.stdout+got.stderr, "brg1.x.y") {
			t.Fatalf("%s echoed the poison value", verb)
		}
		r.fails("usage", 2, "", "team", verb, "--bogus")
		r.fails("usage", 2, "", "team", verb, "extra")
	}
	r.fails("usage", 2, "", "team", "unknown")
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls for argv refusals", r.be.total())
	}
}

// ---- team administration (P5-2, brief 5.6) ----

// rotatedBody is rotate_join_secret's answer for testTeamID: version 2
// and the given secret.
func rotatedBody(secret string) string {
	return `{"status":"rotated","team_id":"` + testTeamID + `","team_name":"ops","secret_version":2,` +
		`"secret_rotated_at":"2026-09-02T12:00:00.123456+00:00","join_secret":"` + secret + `"}`
}

// rotatedSecret is the canary the fake backend answers from
// rotate_join_secret: distinct from testSecret, so a leak of THIS string
// is the rotation's and no other fixture's.
var rotatedSecret = protocol.JoinSecretPrefix + testTeamID + "." + strings.Repeat("ef", 16)

// TestTeamRotateSecretWritesTheFileOnly: rotate_join_secret is called with
// the bound team, the new secret reaches the --secret-file (0600) and
// nothing else — not stdout, not the result, not the debug log, not the
// profile directory (the C-05 plant-and-grep form with rotatedSecret as
// the canary) — the result carries the version, the time and the path,
// and the stderr line names the file and says the old secret is dead.
func TestTeamRotateSecretWritesTheFileOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	args := answerRPC(t, r.be, rpcRotateJoinSecret, rotatedBody(rotatedSecret))
	path := filepath.Join(r.base, "rotated.secret")
	got := r.exec("", "--log-level", "debug", "team", "rotate-secret", "--secret-file", path)
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	if (*args)["p_team_id"] != testTeamID || len(*args) != 1 {
		t.Fatalf("rotate_join_secret args = %v", *args)
	}
	noSecretLeak(t, r, got, false)
	if strings.Contains(got.stdout+got.stderr, rotatedSecret) || strings.Contains(got.stdout, "join_secret") {
		t.Fatalf("the rotated secret or a join_secret member reached a stream: %s %s", got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if result["team_ref"] != testTeamID || result["team_name"] != "ops" || result["secret_version"] != float64(2) || result["secret_file"] != path {
		t.Fatalf("result = %v", result)
	}
	if _, err := time.Parse(time.RFC3339Nano, str(t, result, "rotated_at")); err != nil {
		t.Fatalf("rotated_at: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file: %v %v", info, err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // the test's own temp file
	if err != nil || string(data) != rotatedSecret+"\n" {
		t.Fatalf("secret file content = %q %v", data, err)
	}
	if !strings.Contains(got.stderr, "--secret-file") || !strings.Contains(got.stderr, "no longer works") {
		t.Fatalf("stderr = %q, want the shown-once line naming the file and the dead old secret", got.stderr)
	}
	// The profile is untouched: still bound, same team.
	if state := str(t, r.ok("profile", "status"), "state"); state != protocol.ProfileStateJoined {
		t.Fatalf("state = %q after a rotation, want joined", state)
	}
}

// TestTeamRotateSecretRefusals: --secret-file missing is `usage` and a
// relative one is `usage`, a missing parent is `config`, all before any
// call; the ladder (no team bound is `config`, a non-uuid team_ref is the
// uniform unauthorized without a dial); an answer this adapter cannot
// read is `internal`; and a write failure AFTER the rotation is `config`
// secret_file_unwritable with no secret on any stream.
func TestTeamRotateSecretRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.fails("usage", 2, "", "team", "rotate-secret")
	r.fails("usage", 2, "", "team", "rotate-secret", "--secret-file", "relative.secret")
	got := r.fails("config", 11, "", "team", "rotate-secret", "--secret-file", filepath.Join(r.base, "missing", "s"))
	if details(t, got.stdout)["reason"] != "secret_file_unwritable" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls before the local checks passed", r.be.total())
	}
	path := filepath.Join(r.base, "s.secret")
	answerRPC(t, r.be, rpcRotateJoinSecret, `{"status":"rotated","team_id":"`+otherTeamID+`","secret_version":2,"join_secret":"`+rotatedSecret+`"}`)
	r.fails("internal", 1, "", "team", "rotate-secret", "--secret-file", path)
	answerRPC(t, r.be, rpcRotateJoinSecret, rotatedBody("brg1."+otherTeamID+".abc"))
	r.fails("internal", 1, "", "team", "rotate-secret", "--secret-file", path)
	absent(t, path)
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized")
	}
	refused := r.fails("unauthorized", 5, "", "team", "rotate-secret", "--secret-file", path)
	if refused.stdout != newFailure(t, errNotMember()) {
		t.Fatalf("a non-creator's rotate: %q, want the uniform unauthorized", refused.stdout)
	}
	// The write fails after the rotation: config, the secret is on no stream.
	dir := filepath.Join(r.base, "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		_ = os.Remove(dir)
		writeJSON(w, http.StatusOK, rotatedBody(rotatedSecret))
	}
	got = r.fails("config", 11, "", "--log-level", "debug", "team", "rotate-secret", "--secret-file", filepath.Join(dir, "s"))
	noSecretLeak(t, r, got, false)
	if strings.Contains(got.stdout+got.stderr, rotatedSecret) || details(t, got.stdout)["reason"] != "secret_file_unwritable" {
		t.Fatalf("after a failed write: %s %s", got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "rotate again") {
		t.Fatalf("stderr = %q, want the lost-secret warning", got.stderr)
	}

	unbound := newRig(t)
	unbound.initProfile()
	unbound.writeSession(unbound.session(time.Hour, "rt-1"))
	unbound.fails("config", 11, "", "team", "rotate-secret", "--secret-file", filepath.Join(unbound.base, "s"))
	hex := newRig(t)
	hex.joined()
	hex.bindTeam("team-0123456789abcdef", "ops")
	local := hex.fails("unauthorized", 5, "", "team", "rotate-secret", "--secret-file", filepath.Join(hex.base, "s"))
	if local.stdout != newFailure(t, errNotMember()) || hex.be.calls(rpcPath) != 0 {
		t.Fatalf("non-uuid team_ref: %q (rpc calls %d)", local.stdout, hex.be.calls(rpcPath))
	}
}

// TestTeamRevokeMemberForms: the principal form (flag, flag with --ban,
// stdin with ban) reaches revoke_membership with p_user_id and p_ban; the
// version form (flag, stdin) reaches revoke_memberships_by_version with
// p_max_version; each answer is re-emitted in the fixed result shape.
func TestTeamRevokeMemberForms(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	revoked := `{"team_id":"` + testTeamID + `","principal_ref":"` + otherTeamID + `","status":"revoked","changed":true,"sessions_closed":1}`
	banned := strings.Replace(revoked, `"revoked"`, `"banned"`, 1)
	for _, tc := range []struct {
		name    string
		stdin   string
		args    []string
		body    string
		wantBan bool
	}{
		{"flag", "", []string{"--principal", otherTeamID}, revoked, false},
		{"flag with --ban", "", []string{"--principal", otherTeamID, "--ban"}, banned, true},
		{"stdin", `{"principal_ref":"` + otherTeamID + `"}`, nil, revoked, false},
		{"stdin with ban", `{"principal_ref":"` + otherTeamID + `","ban":true}`, nil, banned, true},
	} {
		args := answerRPC(t, r.be, rpcRevokeMembership, tc.body)
		got := r.exec(tc.stdin, append([]string{"team", "revoke-member"}, tc.args...)...)
		if got.code != 0 {
			t.Fatalf("%s: exit %d %s %s", tc.name, got.code, got.stdout, got.stderr)
		}
		if (*args)["p_team_id"] != testTeamID || (*args)["p_user_id"] != otherTeamID || (*args)["p_ban"] != tc.wantBan || len(*args) != 3 {
			t.Fatalf("%s: revoke_membership args = %v", tc.name, *args)
		}
		result, _ := decode(t, got.stdout)["result"].(map[string]any)
		wantStatus := "revoked"
		if tc.wantBan {
			wantStatus = "banned"
		}
		if result["team_ref"] != testTeamID || result["principal_ref"] != otherTeamID || result["status"] != wantStatus ||
			result["changed"] != true || result["sessions_closed"] != float64(1) {
			t.Fatalf("%s: result = %v", tc.name, result)
		}
	}
	byVersion := `{"team_id":"` + testTeamID + `","max_version":1,"revoked":3,"sessions_closed":4}`
	for _, tc := range []struct {
		name  string
		stdin string
		args  []string
	}{
		{"flag", "", []string{"--max-version", "1"}},
		{"stdin", `{"joined_secret_version_lte":1}`, nil},
	} {
		args := answerRPC(t, r.be, rpcRevokeByVersion, byVersion)
		got := r.exec(tc.stdin, append([]string{"team", "revoke-member"}, tc.args...)...)
		if got.code != 0 {
			t.Fatalf("%s: exit %d %s", tc.name, got.code, got.stdout)
		}
		if (*args)["p_team_id"] != testTeamID || (*args)["p_max_version"] != float64(1) || len(*args) != 2 {
			t.Fatalf("%s: revoke_memberships_by_version args = %v", tc.name, *args)
		}
		result, _ := decode(t, got.stdout)["result"].(map[string]any)
		if result["team_ref"] != testTeamID || result["max_version"] != float64(1) || result["revoked"] != float64(3) || result["sessions_closed"] != float64(4) {
			t.Fatalf("%s: result = %v", tc.name, result)
		}
		if _, present := result["principal_ref"]; present {
			t.Fatalf("%s: a version revoke answered a principal_ref: %v", tc.name, result)
		}
	}
}

// TestTeamRevokeMemberRefusals: both forms on argv, --ban alone, an empty
// --principal and a --max-version outside 0..2147483647 are `usage`; a
// stdin document with neither member, both members, ban on the version
// form or a version out of range is `invalid_input` naming the member;
// none of them dials. A --principal that is not uuid-shaped answers the
// uniform unauthorized with ZERO requests (the counting fake, not
// BRIGADE_TEST_OFFLINE), byte-identical to the backend's own refusal of a
// non-creator; the backend's invalid_input:principal_ref (the self-target)
// is exit 3 naming principal_ref; a 28000 is exit 4.
func TestTeamRevokeMemberRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	for _, args := range [][]string{
		{"--principal", otherTeamID, "--max-version", "1"},
		{"--ban"},
		{"--principal", ""},
		{"--max-version", "-1"},
		{"--max-version", "2147483648"},
		{"--max-version", "one"},
	} {
		r.fails("usage", 2, "", append([]string{"team", "revoke-member"}, args...)...)
	}
	for stdin, field := range map[string]string{
		`{}`: "principal_ref",
		`{"principal_ref":"` + otherTeamID + `","joined_secret_version_lte":1}`: "principal_ref",
		`{"joined_secret_version_lte":1,"ban":true}`:                            "ban",
		`{"joined_secret_version_lte":-1}`:                                      "joined_secret_version_lte",
	} {
		got := r.fails("invalid_input", 3, stdin, "team", "revoke-member")
		if details(t, got.stdout)["field"] != field {
			t.Fatalf("%s: details = %v, want field %s", stdin, details(t, got.stdout), field)
		}
	}
	r.fails("invalid_input", 3, "not json", "team", "revoke-member")
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls for argv and document refusals", r.be.total())
	}
	local := r.fails("unauthorized", 5, "", "team", "revoke-member", "--principal", "principal-0123456789abcdef")
	if r.be.total() != 0 {
		t.Fatalf("a non-uuid --principal dialled: %d requests", r.be.total())
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized")
	}
	raised := r.fails("unauthorized", 5, "", "team", "revoke-member", "--principal", otherTeamID)
	if local.stdout != raised.stdout || raised.stdout != newFailure(t, errNotMember()) {
		t.Fatalf("unauthorized differs: local %q, raised %q", local.stdout, raised.stdout)
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusBadRequest, "22023", "brigade:invalid_input:principal_ref")
	}
	got := r.fails("invalid_input", 3, "", "team", "revoke-member", "--principal", testUserID)
	if details(t, got.stdout)["field"] != "principal_ref" {
		t.Fatalf("self-target: details = %v", details(t, got.stdout))
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusUnauthorized, "28000", "brigade:unauthenticated")
	}
	r.fails("unauthenticated", 4, "", "team", "revoke-member", "--max-version", "1")
	answerRPC(t, r.be, rpcRevokeMembership, `{"changed":false}`)
	r.fails("internal", 1, "", "team", "revoke-member", "--principal", otherTeamID)
	// The ladder: unbound is config, no credential unauthenticated.
	unbound := newRig(t)
	unbound.initProfile()
	unbound.writeSession(unbound.session(time.Hour, "rt-1"))
	unbound.fails("config", 11, "", "team", "revoke-member", "--principal", otherTeamID)
	noCred := newRig(t)
	noCred.initProfile()
	noCred.fails("unauthenticated", 4, "", "team", "revoke-member", "--max-version", "1")
}

// TestTeamTransfer: --principal (or stdin {principal_ref}) reaches
// transfer_team as p_new_creator and the result is re-emitted; a document
// without the member is invalid_input; an empty --principal is usage; a
// non-uuid principal is the uniform unauthorized without a dial; the
// backend's invalid_input:principal_ref (not an active member) is exit 3
// naming the member; an unconfirmed answer is internal.
func TestTeamTransfer(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	body := `{"team_id":"` + testTeamID + `","principal_ref":"` + otherTeamID + `","transferred":true}`
	for _, tc := range []struct {
		name  string
		stdin string
		args  []string
	}{
		{"flag", "", []string{"--principal", otherTeamID}},
		{"stdin", `{"principal_ref":"` + otherTeamID + `"}`, nil},
	} {
		args := answerRPC(t, r.be, rpcTransferTeam, body)
		got := r.exec(tc.stdin, append([]string{"team", "transfer"}, tc.args...)...)
		if got.code != 0 {
			t.Fatalf("%s: exit %d %s", tc.name, got.code, got.stdout)
		}
		if (*args)["p_team_id"] != testTeamID || (*args)["p_new_creator"] != otherTeamID || len(*args) != 2 {
			t.Fatalf("%s: transfer_team args = %v", tc.name, *args)
		}
		result, _ := decode(t, got.stdout)["result"].(map[string]any)
		if result["team_ref"] != testTeamID || result["principal_ref"] != otherTeamID || result["transferred"] != true {
			t.Fatalf("%s: result = %v", tc.name, result)
		}
	}
	calls := r.be.calls(rpcPath)
	got := r.fails("invalid_input", 3, `{}`, "team", "transfer")
	if details(t, got.stdout)["field"] != "principal_ref" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	r.fails("usage", 2, "", "team", "transfer", "--principal", "")
	local := r.fails("unauthorized", 5, "", "team", "transfer", "--principal", "principal-0123456789abcdef")
	if r.be.calls(rpcPath) != calls {
		t.Fatalf("a refused transfer dialled")
	}
	if local.stdout != newFailure(t, errNotMember()) {
		t.Fatalf("non-uuid principal: %q", local.stdout)
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusBadRequest, "22023", "brigade:invalid_input:principal_ref")
	}
	got = r.fails("invalid_input", 3, "", "team", "transfer", "--principal", otherTeamID)
	if details(t, got.stdout)["field"] != "principal_ref" {
		t.Fatalf("not an active member: details = %v", details(t, got.stdout))
	}
	answerRPC(t, r.be, rpcTransferTeam, `{"transferred":false}`)
	r.fails("internal", 1, "", "team", "transfer", "--principal", otherTeamID)
}
