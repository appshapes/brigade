//go:build !windows

package rest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapters/rest/stub"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestDescribeIsLocal: describe answers from local files, creates nothing
// and never asks the server anything, in any state of the profile.
func TestDescribeIsLocal(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	if state := r.state(); state != protocol.ProfileStateUnconfigured {
		t.Errorf("a fresh profile: state %q, want unconfigured", state)
	}
	if _, err := os.Stat(r.cfg); !os.IsNotExist(err) {
		t.Errorf("describe created the configuration directory (%v)", err)
	}
	created := r.create("ops", "alice@example.com")
	b.forget()
	if state := r.state(); state != protocol.ProfileStateJoined {
		t.Errorf("after team create: state %q, want joined", state)
	}
	got := r.execEnv([]string{offlineVar + "=1"}, "", "describe")
	if got.code != 0 {
		t.Fatalf("describe offline: exit %d (%s)", got.code, got.stdout)
	}
	profile, _ := decode(t, got.stdout)["result"].(map[string]any)["profile"].(map[string]any)
	if profile["state"] != protocol.ProfileStateJoined || profile["team_ref"] != created["team_ref"] ||
		profile["team_name"] != "ops" || profile["principal_ref"] != created["principal_ref"] || profile["human_label"] != "alice@example.com" {
		t.Errorf("describe after team create: profile %v", profile)
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("describe made %d requests, with and without the network; want none", n)
	}
}

// TestProfileInit: `profile init --url` stores the base url, refuses one
// the url rule refuses, and is `conflict` on a configured profile unless
// --force, which changes the url and nothing else.
func TestProfileInit(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, "")
	r.fails(protocol.CodeUsage, "", "profile", "init")
	got := r.fails(protocol.CodeInvalidInput, "", "profile", "init", "--url", "http://brigade.example.com")
	if details(t, got.stdout)["field"] != "url" {
		t.Errorf("an http url of another host: details %v, want field url", details(t, got.stdout))
	}
	if state := r.state(); state != protocol.ProfileStateUnconfigured {
		t.Errorf("after a refused init: state %q, want unconfigured", state)
	}

	result := r.ok("", "profile", "init", "--url", b.url()+"/")
	if result["url"] != b.url() || result["state"] != protocol.ProfileStateUnauthenticated {
		t.Errorf("profile init: %v, want the url without its trailing slash and state unauthenticated", result)
	}
	conflict := r.fails(protocol.CodeConflict, "", "profile", "init", "--url", b.url())
	if details(t, conflict.stdout)["reason"] != reasonProfileExists {
		t.Errorf("a second init: details %v, want reason %s", details(t, conflict.stdout), reasonProfileExists)
	}

	// The profile's url is the backend: no variable is needed, and one that
	// names another server is not honoured.
	created := r.execEnv([]string{backendURLVar + "=https://elsewhere.example.com"}, `{"team_name":"ops"}`, "team", "create")
	if created.code != 0 {
		t.Fatalf("team create on an initialised profile: exit %d (%s)", created.code, created.stdout)
	}
	before := r.storedCredential()
	forced := r.ok("", "profile", "init", "--url", "https://moved.example.com", "--force")
	if forced["url"] != "https://moved.example.com" || forced["state"] != protocol.ProfileStateJoined {
		t.Errorf("profile init --force: %v, want the new url and the binding kept", forced)
	}
	if r.storedCredential() != before {
		t.Errorf("profile init --force touched the credential")
	}
	status := r.ok("", "profile", "status")
	if status["url"] != "https://moved.example.com" || status["team_name"] != "ops" || status["state"] != protocol.ProfileStateJoined {
		t.Errorf("profile status: %v", status)
	}
}

// TestTheEnvironmentConfiguresOnlyAnEmptyProfile: BRIGADE_REST_URL is
// honoured by `team create` and `team join` when the profile names no
// backend, is written to team.json as `profile init` would write it, and
// is ignored by every other command.
func TestTheEnvironmentConfiguresOnlyAnEmptyProfile(t *testing.T) {
	t.Parallel()
	b := newBackend(t)

	// Every other command needs the profile: the variable alone is not one.
	bare := newRig(t, b.url())
	for _, args := range [][]string{{"session", "list"}, {"team", "members"}, {"message", "receive", "--session", "s"}} {
		got := bare.fails(protocol.CodeConfig, "", args...)
		if details(t, got.stdout)["reason"] != reasonProfileMissing {
			t.Errorf("%v with only the variable: details %v, want reason %s", args, details(t, got.stdout), reasonProfileMissing)
		}
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("commands on an unconfigured profile made %d requests, want none", n)
	}

	// A binding command with neither a profile nor the variable has no backend.
	none := newRig(t, "")
	got := none.fails(protocol.CodeConfig, `{"team_name":"ops"}`, "team", "create")
	if details(t, got.stdout)["reason"] != reasonProfileMissing {
		t.Errorf("team create with no backend anywhere: details %v", details(t, got.stdout))
	}

	// A variable the url rule refuses is `config`, and nothing is written.
	bad := newRig(t, "http://brigade.example.com")
	got = bad.fails(protocol.CodeConfig, `{"team_name":"ops"}`, "team", "create")
	if details(t, got.stdout)["reason"] != reasonInvalidValue {
		t.Errorf("team create with an http url of another host in the variable: details %v", details(t, got.stdout))
	}
	if _, err := os.Stat(bad.profilePath()); !os.IsNotExist(err) {
		t.Errorf("a refused variable wrote a profile (%v)", err)
	}

	// The variable bootstraps the profile, and from then on the file is read.
	r := newRig(t, b.url()+"/")
	r.create("ops", "alice@example.com")
	if url := r.ok("", "profile", "status")["url"]; url != b.url() {
		t.Errorf("profile status after a create from the variable: url %v, want %s", url, b.url())
	}
	r.env = r.env[:len(r.env)-1] // the variable is gone, as under a live session
	if got := r.exec("", "session", "list"); got.code != 0 {
		t.Errorf("session list without the variable: exit %d (%s)", got.code, got.stdout)
	}
}

// TestTheBackendMemberOfJoin: `backend` is {url}, it configures a profile
// that names no backend, it is refused on one that names another, and it
// never travels to the server.
func TestTheBackendMemberOfJoin(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	secret := str(t, newRig(t, b.url()).create("ops", "alice@example.com"), "join_secret")
	join := func(backend string) string {
		return `{"join_secret":"` + secret + `","human_label":"bob@example.com","backend":` + backend + `}`
	}

	r := newRig(t, "")
	for _, bad := range []string{`"x"`, `[]`, `{}`, `{"url":""}`, `{"url":7}`} {
		got := r.fails(protocol.CodeInvalidInput, join(bad), "team", "join")
		if details(t, got.stdout)["field"] != "backend" {
			t.Errorf("backend %s: details %v, want field backend", bad, details(t, got.stdout))
		}
	}
	got := r.fails(protocol.CodeInvalidInput, join(`{"url":"http://brigade.example.com"}`), "team", "join")
	if details(t, got.stdout)["field"] != "url" {
		t.Errorf("a backend with an http url of another host: details %v, want field url", details(t, got.stdout))
	}
	if _, err := os.Stat(r.profilePath()); !os.IsNotExist(err) {
		t.Errorf("a refused backend member wrote a profile (%v)", err)
	}

	b.forget()
	joined := r.ok(join(`{"url":"`+b.url()+`/","ignored":true}`), "team", "join")
	if joined["rejoined"] != false {
		t.Errorf("team join with a backend member: %v", joined)
	}
	seen := b.seen(pathTeamJoin)
	if len(seen) != 1 || len(seen[0].Body) != 2 || seen[0].Body["join_secret"] != secret || seen[0].Body["human_label"] != "bob@example.com" {
		t.Errorf("the join request is not {join_secret, human_label}: %v", seen)
	}
	if seen[0].Authorization != "" {
		t.Errorf("a profile with no credential sent an Authorization header")
	}
	if url := r.ok("", "profile", "status")["url"]; url != b.url() {
		t.Errorf("profile status after a join with a backend member: url %v, want %s", url, b.url())
	}

	// On a configured profile: the same backend again is a no-op, another
	// one is `conflict`.
	r.ok("", "team", "leave")
	if again := r.ok(join(`{"url":"`+b.url()+`"}`), "team", "join"); again["rejoined"] != true {
		t.Errorf("a rejoin naming the profile's own backend: %v, want rejoined true", again)
	}
	r.ok("", "team", "leave")
	other := r.fails(protocol.CodeConflict, join(`{"url":"https://elsewhere.example.com"}`), "team", "join")
	if details(t, other.stdout)["reason"] != reasonProfileExists {
		t.Errorf("a backend member naming another server: details %v, want reason %s", details(t, other.stdout), reasonProfileExists)
	}
}

// TestAProfileWithAnUnusableURLIsNeverSentTheCredential: the url rule
// holds where the url is used, not only where it was first stored.
func TestAProfileWithAnUnusableURLIsNeverSentTheCredential(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	r.create("ops", "alice@example.com")
	data, err := os.ReadFile(r.profilePath())
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), b.url(), "http://brigade.example.com", 1)
	if err := os.WriteFile(r.profilePath(), []byte(edited), 0o600); err != nil { //nolint:gosec // G703: the rig's own team.json under t.TempDir()
		t.Fatal(err)
	}
	got := r.fails(protocol.CodeConfig, "", "session", "list")
	if details(t, got.stdout)["reason"] != reasonBackendURL {
		t.Errorf("a team.json edited to an http url of another host: details %v, want reason %s", details(t, got.stdout), reasonBackendURL)
	}
}

// TestTheCredentialIsStoredPrivatelyAndNeverShown is the credential rule
// of the contract's sections 1 and 3: the credential the server issues is
// written to credential.json with mode 0600, rides every later request as
// a bearer token, and appears on neither stream of any command at debug
// level, the watch included.
func TestTheCredentialIsStoredPrivatelyAndNeverShown(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	for _, r := range []*rig{p.alice, p.bob} {
		cred := r.storedCredential()
		if !validCredential(cred) {
			t.Fatalf("credential.json holds no credential of the contract's shape")
		}
		info, err := os.Stat(r.credentialPath())
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("credential.json has mode %o, want 0600", mode)
		}
		dir, err := os.Stat(filepath.Dir(r.credentialPath()))
		if err != nil {
			t.Fatal(err)
		}
		if mode := dir.Mode().Perm(); mode != 0o700 {
			t.Errorf("the profile directory has mode %o, want 0700", mode)
		}
		profile, err := os.ReadFile(r.profilePath())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(profile), cred) || strings.Contains(string(profile), p.secret) {
			t.Errorf("team.json carries the credential or the join secret")
		}
		if stored, _ := os.ReadFile(r.credentialPath()); strings.Contains(string(stored), p.secret) {
			t.Errorf("credential.json carries the join secret")
		}
	}

	// One of everything, at debug level, failures included.
	alice, bob := p.alice, p.bob
	cred := alice.storedCredential()
	w := alice.watch(fastTiming(), "--session", p.aliceSes)
	w.ready()
	id := bob.send(p.bobSes, p.aliceSes, "hello")
	if got := w.message(); got != id {
		t.Fatalf("the watch emitted %s, want %s", got, id)
	}
	w.command(`{"type":"ack","message_ids":["` + id + `"]}`)
	w.expect(protocol.EventAcked)
	w.command(`{"type":"heartbeat","activity":"idle"}`)
	w.expect(protocol.EventHeartbeatOK)
	alice.ok(`{}`, "session", "heartbeat", "--session", p.aliceSes)
	alice.ok("", "session", "list", "--include-offline")
	alice.ok("", "team", "members")
	alice.ok("", "message", "receive", "--session", p.aliceSes)
	alice.ok("", "profile", "status")
	alice.ok("", "describe")
	alice.fails(protocol.CodeNotFound, "", "message", "receive", "--session", "no-such-session")
	b.answerWith(only(pathSessionList, http.StatusBadGateway, "gateway"))
	alice.fails(protocol.CodeUnavailable, "", "session", "list")
	b.answerWith(nil)
	w.command(`{"type":"close"}`)
	if code := w.exit(); code != 0 {
		t.Fatalf("the watch exited %d after close", code)
	}
	alice.ok("", "team", "leave")
	alice.join(p.secret, "alice@example.com")
	alice.ok("", "profile", "revoke-credentials")

	everything := alice.output() + bob.output() + w.stderr.String()
	if !strings.Contains(everything, `"level":"DEBUG"`) {
		t.Fatalf("the commands did not log at debug level, so this test proves nothing")
	}
	for _, held := range []string{cred, bob.storedCredential()} {
		if held == "" || strings.Contains(everything, held) {
			t.Errorf("a credential is missing from its file, or appears on stdout or stderr of a command")
		}
	}
	if strings.Contains(everything, `"credential"`) {
		t.Errorf("a result's credential member reached stdout")
	}
	if strings.Contains(strings.ToLower(everything), "bearer ") {
		t.Errorf("an Authorization header value appears on stdout or stderr of a command")
	}
	sent := 0
	for _, r := range b.seen("") {
		if r.Authorization == "Bearer "+cred {
			sent++
		}
	}
	if sent < 10 {
		t.Errorf("only %d requests carried the credential; the commands above make more", sent)
	}
}

// TestACredentialFileOthersCanReadIsRefused: a group- or world-readable
// credential.json is treated as leaked (`config`), never read.
func TestACredentialFileOthersCanReadIsRefused(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	r.create("ops", "alice@example.com")
	if err := os.Chmod(r.credentialPath(), 0o644); err != nil { //nolint:gosec // G302: the too-wide mode is the fixture
		t.Fatal(err)
	}
	b.forget()
	got := r.fails(protocol.CodeConfig, "", "session", "list")
	if details(t, got.stdout)["reason"] != "insecure_mode" {
		t.Errorf("a world-readable credential.json: details %v, want reason insecure_mode", details(t, got.stdout))
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("the refused command made %d requests, want none", n)
	}
}

// TestJoinLeaveRejoin is the membership flow of the contract's section 3
// and of 4.4.10: a join with no credential creates a principal and stores
// its credential; a leave unbinds the profile and keeps the credential; a
// join with that credential is the same membership again.
func TestJoinLeaveRejoin(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	bob := p.bob
	status := bob.ok("", "profile", "status")
	principal, teamRef := str(t, status, "principal_ref"), str(t, status, "team_ref")
	cred := bob.storedCredential()

	// The first requests of each profile carried no credential; the result's
	// credential never reached stdout.
	for _, path := range []string{pathTeamCreate, pathTeamJoin} {
		seen := b.seen(path)
		if len(seen) != 1 {
			t.Fatalf("%s: %d requests, want one", path, len(seen))
		}
		if seen[0].Authorization != "" {
			t.Errorf("%s: a profile with no credential sent an Authorization header", path)
		}
	}
	if strings.Contains(p.alice.output()+bob.output(), `"credential"`) {
		t.Errorf("a binding command printed the result's credential member")
	}

	left := bob.ok("", "team", "leave")
	if left["team_ref"] != teamRef || left["principal_ref"] != principal || left["left"] != true {
		t.Errorf("team leave: %v", left)
	}
	if state := bob.state(); state != protocol.ProfileStateNotMember {
		t.Errorf("after team leave: state %q, want not_member", state)
	}
	if bob.storedCredential() != cred {
		t.Errorf("team leave changed the credential; it stays (4.2)")
	}
	// Unbound, with a credential: `config`, locally.
	b.forget()
	for _, args := range [][]string{{"session", "list"}, {"message", "receive", "--session", p.bobSes}, {"team", "members"}} {
		got := bob.fails(protocol.CodeConfig, "", args...)
		if details(t, got.stdout)["reason"] != reasonNoTeamBound {
			t.Errorf("%v after team leave: details %v", args, details(t, got.stdout))
		}
	}
	// A second leave is answered from the local files.
	again := bob.ok("", "team", "leave")
	if again["team_ref"] != teamRef || again["principal_ref"] != principal || again["left"] != true {
		t.Errorf("a second team leave: %v, want the first one's answer", again)
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("commands on an unbound profile made %d requests, want none", n)
	}
	// The server closed the member's session and dropped it from the roster.
	if members, _ := p.alice.ok("", "team", "members")["members"].([]any); len(members) != 1 {
		t.Errorf("alice's roster after bob left: %d members, want 1", len(members))
	}

	rejoined := bob.join(p.secret, "bob@example.com")
	if rejoined["rejoined"] != true || rejoined["principal_ref"] != principal || rejoined["team_ref"] != teamRef {
		t.Errorf("team join after a leave: %v, want rejoined true and the same principal_ref", rejoined)
	}
	if seen := b.seen(pathTeamJoin); len(seen) != 1 || seen[0].Authorization != "Bearer "+cred {
		t.Errorf("the rejoin did not carry the profile's credential")
	}
	if bob.storedCredential() != cred {
		t.Errorf("a rejoin that was handed no new credential changed the stored one")
	}
	if state := bob.state(); state != protocol.ProfileStateJoined {
		t.Errorf("after the rejoin: state %q, want joined", state)
	}
}

// TestBindingRulesAreLocal: `team create` and a `team join` naming another
// team on a bound profile are `conflict` profile_bound before any request,
// and a rejected or malformed secret creates nothing.
func TestBindingRulesAreLocal(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	b.forget()
	for _, tc := range []struct {
		input string
		args  []string
	}{
		{`{"team_name":"second"}`, []string{"team", "create"}},
		{`{"join_secret":"brg1.another-team.0123456789abcdef"}`, []string{"team", "join"}},
	} {
		got := p.alice.fails(protocol.CodeConflict, tc.input, tc.args...)
		if details(t, got.stdout)["reason"] != reasonProfileBound {
			t.Errorf("%v on a bound profile: details %v", tc.args, details(t, got.stdout))
		}
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("the refused binding commands made %d requests, want none", n)
	}

	fresh := newRig(t, b.url())
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	wrong := fresh.fails(protocol.CodeUnauthorized, `{"join_secret":"brg1.`+teamRef+`.0123456789abcdef"}`, "team", "join")
	unknown := fresh.fails(protocol.CodeUnauthorized, `{"join_secret":"brg1.0123456789abcdef0123456789abcdef.0123456789abcdef"}`, "team", "join")
	if wrong.stdout != unknown.stdout {
		t.Errorf("a wrong secret and an unknown team differ:\n%s%s", wrong.stdout, unknown.stdout)
	}
	malformed := fresh.fails(protocol.CodeInvalidInput, `{"join_secret":"nope"}`, "team", "join")
	if details(t, malformed.stdout)["field"] != "join_secret" || strings.Contains(malformed.stdout+malformed.stderr, "nope") {
		t.Errorf("a malformed secret: %s", malformed.stdout)
	}
	if fresh.storedCredential() != "" {
		t.Errorf("a rejected join stored a credential")
	}
	if state := fresh.state(); state != protocol.ProfileStateUnauthenticated {
		t.Errorf("after rejected joins: state %q, want unauthenticated (a backend, no credential)", state)
	}
	fresh.fails(protocol.CodeUsage, `{"join_secret":"`+p.secret+`"}`, "team", "join", "--prompt")
	fresh.fails(protocol.CodeUsage, `{"team_name":"ops"}`, "team", "create", "--prompt")
}

// TestRevokeCredentials is `profile revoke-credentials` and the join that
// follows it (the contract's section 3): the server forgets the
// credential, the local file goes, the profile keeps its binding, the
// principal's membership is not changed, and the next join, which carries
// no credential, is a NEW principal with a new credential.
func TestRevokeCredentials(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	bob := p.bob
	before := bob.ok("", "profile", "status")
	cred := bob.storedCredential()

	revoked := bob.ok("", "profile", "revoke-credentials")
	if revoked["state"] != protocol.ProfileStateUnauthenticated {
		t.Errorf("profile revoke-credentials: %v, want state unauthenticated", revoked)
	}
	if bob.storedCredential() != "" {
		t.Errorf("credential.json is still there after revoke-credentials")
	}
	after := bob.ok("", "profile", "status")
	if after["team_ref"] != before["team_ref"] || after["team_name"] != before["team_name"] {
		t.Errorf("revoke-credentials changed the binding: %v", after)
	}
	got := bob.fails(protocol.CodeUnauthenticated, "", "session", "list")
	if details(t, got.stdout)["reason"] != reasonCredentialMissing {
		t.Errorf("a command with no credential: details %v", details(t, got.stdout))
	}

	// The server no longer knows the credential: a copy of the file is
	// worth nothing.
	thief := newRig(t, b.url())
	if err := os.MkdirAll(filepath.Dir(thief.credentialPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	profile, err := os.ReadFile(bob.profilePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thief.profilePath(), profile, 0o600); err != nil { //nolint:gosec // G703: the rig's own team.json under t.TempDir()
		t.Fatal(err)
	}
	if err := os.WriteFile(thief.credentialPath(), []byte(`{"credential":"`+cred+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stolen := thief.fails(protocol.CodeUnauthenticated, "", "session", "list")
	if details(t, stolen.stdout)["reason"] == reasonCredentialMissing {
		t.Errorf("the copied credential was refused locally; the server should have refused it")
	}

	// The membership is not changed by the revocation.
	members, _ := p.alice.ok("", "team", "members")["members"].([]any)
	if len(members) != 2 {
		t.Errorf("alice's roster after bob revoked his credential: %d members, want 2", len(members))
	}

	// The join that follows: the bound team's secret is a rejoin locally
	// (4.4.10), and a new principal to the server.
	b.forget()
	joined := bob.join(p.secret, "bob@example.com")
	if seen := b.seen(pathTeamJoin); len(seen) != 1 || seen[0].Authorization != "" {
		t.Errorf("the join after revoke-credentials carried a credential")
	}
	if joined["principal_ref"] == before["principal_ref"] || joined["rejoined"] != false {
		t.Errorf("team join after revoke-credentials: %v; want a new principal_ref and rejoined false", joined)
	}
	if next := bob.storedCredential(); next == "" || next == cred {
		t.Errorf("the join after revoke-credentials did not store a new credential")
	}
	if state := bob.state(); state != protocol.ProfileStateJoined {
		t.Errorf("after the join: state %q, want joined", state)
	}
	bob.ok("", "session", "list")

	// A revocation the server cannot be asked for deletes nothing.
	b.answerWith(only(pathCredentialRevoke, http.StatusBadGateway, "gateway"))
	bob.fails(protocol.CodeUnavailable, "", "profile", "revoke-credentials")
	if bob.storedCredential() == "" {
		t.Errorf("a revocation that failed deleted the credential")
	}
	// A server that already forgot the credential has nothing left to do.
	b.answerWith(only(pathCredentialRevoke, http.StatusUnauthorized,
		`{"ok":false,"protocol_version":"1","error":{"code":"unauthenticated","message":"unknown","retryable":false}}`))
	bob.ok("", "profile", "revoke-credentials")
	if bob.storedCredential() != "" {
		t.Errorf("revoke-credentials kept a credential the server no longer knows")
	}
}

// TestProfileReset: the server is asked to forget the credential, best
// effort, and the profile directory goes either way.
func TestProfileReset(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	b.forget()
	reset := p.bob.ok("", "profile", "reset")
	if reset["state"] != protocol.ProfileStateUnconfigured {
		t.Errorf("profile reset: %v", reset)
	}
	if seen := b.seen(pathCredentialRevoke); len(seen) != 1 {
		t.Errorf("profile reset made %d revocation requests, want 1", len(seen))
	}
	if _, err := os.Stat(filepath.Dir(p.bob.profilePath())); !os.IsNotExist(err) {
		t.Errorf("the profile directory is still there (%v)", err)
	}

	b.srv.Close()
	reset = p.alice.ok("", "profile", "reset")
	if reset["state"] != protocol.ProfileStateUnconfigured || p.alice.state() != protocol.ProfileStateUnconfigured {
		t.Errorf("profile reset with the server gone: %v", reset)
	}
}

// TestTheServerMayReplaceTheCredential: a join by a known principal may
// come back with a new credential, which replaces the old (section 3); a
// command that is refused as `unauthenticated` while the file holds a
// newer credential retries once with it; and a result that leaves a
// profile with no credential at all is malformed.
func TestTheServerMayReplaceTheCredential(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	bob := p.bob
	old := bob.storedCredential()
	status := bob.ok("", "profile", "status")
	const rotated = "rotated-credential-0123456789"
	joinResult := `{"team_ref":"` + str(t, status, "team_ref") + `","team_name":"ops","principal_ref":"` + str(t, status, "principal_ref") + `","rejoined":true`

	bob.ok("", "team", "leave")
	b.answerWith(only(pathTeamJoin, http.StatusOK, okEnvelope(joinResult+`,"credential":"`+rotated+`"}`)))
	got := bob.exec(`{"join_secret":"`+p.secret+`"}`, "team", "join")
	if got.code != 0 || strings.Contains(got.stdout+got.stderr, rotated) {
		t.Fatalf("a join that rotates the credential: exit %d, or the new credential was printed (%s)", got.code, got.stdout)
	}
	if bob.storedCredential() != rotated {
		t.Errorf("the new credential did not replace the old one")
	}

	// A command that read the old credential while another process was being
	// handed the new one: the server refuses it, the file holds the new one
	// by then, one retry.
	b.forget()
	if err := os.WriteFile(bob.credentialPath(), []byte(`{"credential":"`+old+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path != pathSessionList {
			return nil
		}
		if r.Authorization == "Bearer "+rotated {
			return &canned{status: http.StatusOK, body: okEnvelope(emptyList)}
		}
		// Another process is handed the new credential while this request
		// is on its way.
		if !swapped {
			swapped = true
			if err := os.WriteFile(bob.credentialPath(), []byte(`{"credential":"`+rotated+`"}`), 0o600); err != nil {
				t.Error(err)
			}
		}
		return &canned{status: http.StatusUnauthorized, body: `{"ok":false,"protocol_version":"1","error":{"code":"unauthenticated","message":"unknown","retryable":false}}`}
	})
	if got := bob.exec("", "session", "list"); got.code != 0 {
		t.Errorf("a command refused with a stale credential while the file holds a newer one: exit %d (%s), want one retry and success", got.code, got.stdout)
	}
	if seen := b.seen(pathSessionList); len(seen) != 2 {
		t.Errorf("the command made %d requests, want the refused one and one retry", len(seen))
	}
	// With nothing newer in the file the refusal stands, after one request.
	b.forget()
	b.answerWith(only(pathSessionList, http.StatusUnauthorized, `{"ok":false,"protocol_version":"1","error":{"code":"unauthenticated","message":"unknown","retryable":false}}`))
	bob.fails(protocol.CodeUnauthenticated, "", "session", "list")
	if seen := b.seen(pathSessionList); len(seen) != 1 {
		t.Errorf("an unauthenticated answer with no newer credential made %d requests, want 1", len(seen))
	}

	// A new principal with no credential in the result, and a credential
	// outside the contract's shape.
	fresh := newRig(t, b.url())
	b.answerWith(only(pathTeamJoin, http.StatusOK, okEnvelope(joinResult+`}`)))
	fresh.fails(protocol.CodeInternal, `{"join_secret":"`+p.secret+`"}`, "team", "join")
	b.answerWith(only(pathTeamJoin, http.StatusOK, okEnvelope(joinResult+`,"credential":"short"}`)))
	fresh.fails(protocol.CodeInternal, `{"join_secret":"`+p.secret+`"}`, "team", "join")
	// A join answered for another team than the secret's.
	b.answerWith(only(pathTeamJoin, http.StatusOK, okEnvelope(strings.Replace(joinResult, str(t, status, "team_ref"), "another-team", 1)+`,"credential":"`+rotated+`"}`)))
	fresh.fails(protocol.CodeInternal, `{"join_secret":"`+p.secret+`"}`, "team", "join")
	if fresh.storedCredential() != "" || fresh.state() == protocol.ProfileStateJoined {
		t.Errorf("a malformed join result bound the profile or stored a credential")
	}
}

// TestTeamCreateSecretFile: with --secret-file the join secret goes to
// that file, 0600, and is omitted from stdout.
func TestTeamCreateSecretFile(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	path := filepath.Join(t.TempDir(), "ops.secret")
	r.fails(protocol.CodeUsage, "", "team", "create", "--name", "ops", "--secret-file", "relative.secret")
	r.fails(protocol.CodeConfig, "", "team", "create", "--name", "ops", "--secret-file", filepath.Join(t.TempDir(), "missing", "ops.secret"))
	if n := len(b.seen("")); n != 0 {
		t.Errorf("a refused --secret-file made %d requests, want none", n)
	}
	got := r.exec("", "team", "create", "--name", "ops", "--label", "alice@example.com", "--secret-file", path)
	if got.code != 0 {
		t.Fatalf("team create --secret-file: exit %d (%s)", got.code, got.stdout)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(string(data))
	if _, err := protocol.ParseJoinSecret(secret); err != nil {
		t.Errorf("the secret file does not hold a join secret: %v", err)
	}
	if strings.Contains(got.stdout+got.stderr, secret) || strings.Contains(got.stdout, "join_secret") {
		t.Errorf("the join secret reached stdout or stderr although --secret-file was given")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("the secret file has mode %o, want 0600", info.Mode().Perm())
	}
	newRig(t, b.url()).join(secret, "bob@example.com")
}

// TestCreateToken: a server may require a creation token on
// /v1/team/create (the contract's section 3). The adapter reads it from a
// file, never from argv, and sends it as Brigade-Create-Token.
func TestCreateToken(t *testing.T) {
	t.Parallel()
	const token = "operator-creation-token"
	b := newBackendWith(t, stub.Options{CreateToken: token})
	r := newRig(t, b.url())
	r.fails(protocol.CodeUnauthorized, `{"team_name":"ops"}`, "team", "create")

	dir := t.TempDir()
	wrong, right, empty := filepath.Join(dir, "wrong"), filepath.Join(dir, "right"), filepath.Join(dir, "empty")
	for path, content := range map[string]string{wrong: "another-token\n", right: token + "\n", empty: "\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.fails(protocol.CodeUnauthorized, `{"team_name":"ops"}`, "team", "create", "--create-token-file", wrong)
	b.forget()
	r.fails(protocol.CodeUsage, `{"team_name":"ops"}`, "team", "create", "--create-token-file", "relative")
	for _, path := range []string{empty, filepath.Join(dir, "missing")} {
		got := r.fails(protocol.CodeConfig, `{"team_name":"ops"}`, "team", "create", "--create-token-file", path)
		if details(t, got.stdout)["reason"] != reasonCreateTokenFile {
			t.Errorf("--create-token-file %s: details %v", filepath.Base(path), details(t, got.stdout))
		}
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("a refused --create-token-file made %d requests, want none", n)
	}
	got := r.exec(`{"team_name":"ops"}`, "team", "create", "--create-token-file", right)
	if got.code != 0 {
		t.Fatalf("team create with the token: exit %d (%s)", got.code, got.stdout)
	}
	if seen := b.seen(pathTeamCreate); len(seen) != 1 || seen[0].CreateToken != token {
		t.Errorf("the create request did not carry the token in Brigade-Create-Token")
	}
	if strings.Contains(r.output(), token) {
		t.Errorf("the creation token appears on stdout or stderr")
	}
}

// TestANonMemberIsRefusedUniformly: the profile's team travels with the
// request, so a profile rebound to a team its principal is not a member of
// is `unauthorized`, with the same bytes for a team that exists and one
// that does not (4.5.7, C-26, C-43).
func TestANonMemberIsRefusedUniformly(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	outsider := newRig(t, b.url())
	outsider.create("other", "carol@example.com")
	teamRef := str(t, p.alice.ok("", "profile", "status"), "team_ref")
	own := str(t, outsider.ok("", "profile", "status"), "team_ref")
	original, err := os.ReadFile(outsider.profilePath())
	if err != nil {
		t.Fatal(err)
	}
	rebind := func(to string) {
		//nolint:gosec // G703: the rig's own team.json under t.TempDir()
		if err := os.WriteFile(outsider.profilePath(), []byte(strings.Replace(string(original), own, to, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		input string
		args  []string
	}{
		{"", []string{"team", "members"}},
		{"", []string{"session", "list"}},
		{registration("intruder"), []string{"session", "register"}},
	} {
		rebind(teamRef)
		existing := outsider.fails(protocol.CodeUnauthorized, tc.input, tc.args...)
		rebind("0123456789abcdef0123456789abcdef")
		random := outsider.fails(protocol.CodeUnauthorized, tc.input, tc.args...)
		if existing.stdout != random.stdout {
			t.Errorf("%v: a team that exists and one that does not differ:\n%s%s", tc.args, existing.stdout, random.stdout)
		}
	}
	// Leaving a team the principal is not a member of changes nothing there.
	rebind(teamRef)
	left := outsider.ok("", "team", "leave")
	if left["team_ref"] != teamRef || left["left"] != true {
		t.Errorf("team leave on a rebound profile: %v", left)
	}
	if members, _ := p.alice.ok("", "team", "members")["members"].([]any); len(members) != 2 {
		t.Errorf("the rebound leave changed alice's team: %d members, want 2", len(members))
	}
}
