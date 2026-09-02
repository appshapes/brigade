package supabase

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// call runs one list_sessions RPC through the command-level rpc on a
// joined rig and returns the *protocol.Error, or nil.
func call(t *testing.T, r *rig) *protocol.Error {
	t.Helper()
	c := r.command("session", "list")
	if err := c.authenticate(true); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var out map[string]any
	if err := c.rpc(t.Context(), "list_sessions", rpcArgs{"p_team_id": testTeamID}, &out); err != nil {
		return asProtocolError(err)
	}
	return nil
}

// TestFreshTokenIsNotRefreshed: a token with more than 90 s left is used
// as is; /token is never called.
func TestFreshTokenIsNotRefreshed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	if err := call(t, r); err != nil {
		t.Fatal(err)
	}
	if r.be.calls("/auth/v1/token") != 0 {
		t.Fatalf("a fresh token was refreshed")
	}
	last := r.be.last(rpcPath)
	if last.header.Get("Authorization") != "Bearer "+r.readSession().AccessToken {
		t.Fatalf("Authorization = %q", last.header.Get("Authorization"))
	}
	if last.header.Get("apikey") != testKey || last.header.Get("Accept-Profile") != "brigade" || last.header.Get("Content-Profile") != "brigade" {
		t.Fatalf("rpc headers = %v", last.header)
	}
	if !strings.HasPrefix(last.header.Get("X-Client-Info"), "brigade-adapter-supabase/") {
		t.Fatalf("X-Client-Info = %q", last.header.Get("X-Client-Info"))
	}
	if string(last.body) != `{"p_team_id":"`+testTeamID+`"}` {
		t.Fatalf("rpc body = %s", last.body)
	}
}

// TestMarginRefresh: inside the 90 s margin the token is refreshed under
// the lock, the new session is written atomically 0600 with last_team_ref
// kept, and the RPC carries the new token.
func TestMarginRefresh(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	old := r.session(80*time.Second, "rt-1")
	old.LastTeamRef = testTeamID
	r.writeSession(old)
	r.bindTeam(testTeamID, "ops")
	if err := call(t, r); err != nil {
		t.Fatal(err)
	}
	if r.be.calls("/auth/v1/token") != 1 {
		t.Fatalf("refresh calls = %d, want 1", r.be.calls("/auth/v1/token"))
	}
	refresh := r.be.last("/auth/v1/token")
	if !strings.Contains(refresh.path, "grant_type=refresh_token") || string(refresh.body) != `{"refresh_token":"rt-1"}` {
		t.Fatalf("refresh request = %s %s", refresh.path, refresh.body)
	}
	if refresh.header.Get("X-Supabase-Api-Version") != apiVersionHeader || refresh.header.Get("apikey") != testKey {
		t.Fatalf("refresh headers = %v", refresh.header)
	}
	next := r.readSession()
	if next.RefreshToken != "rt-1+1" || next.AccessToken == old.AccessToken || next.LastTeamRef != testTeamID {
		t.Fatalf("session.json after refresh = %+v", next)
	}
	info, err := os.Stat(r.sessionPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session.json mode %v %v", info, err)
	}
	if r.be.last(rpcPath).header.Get("Authorization") != "Bearer "+next.AccessToken {
		t.Fatalf("the RPC did not carry the refreshed token")
	}
	// A second command finds the fresh file and does not refresh again.
	if err := call(t, r); err != nil {
		t.Fatal(err)
	}
	if r.be.calls("/auth/v1/token") != 1 {
		t.Fatalf("refreshed a fresh token again")
	}
}

// TestRereadAfterLock: a fresher token another process stored while
// this one waited is adopted without a call — the re-read of 5.1.
func TestRereadAfterLock(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	r.writeSession(r.session(30*time.Second, "rt-old"))
	r.bindTeam(testTeamID, "ops")
	c := r.command("session", "list")
	if err := c.authenticate(true); err != nil {
		t.Fatal(err)
	}
	// Another process rotates the file between authenticate and the call.
	r.writeSession(r.session(time.Hour, "rt-new"))
	token, err := c.accessToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token != r.readSession().AccessToken || r.be.calls("/auth/v1/token") != 0 {
		t.Fatalf("the re-read token was not adopted (refresh calls %d)", r.be.calls("/auth/v1/token"))
	}
}

// TestAlreadyUsedRetriesWithTheNewerToken is E0-6's headline correction:
// refresh_token_already_used → re-read the file once under the lock, retry
// once with the newer token it holds, and succeed. The fake rotates the
// file mid-flight, the way a concurrent writer does.
func TestAlreadyUsedRetriesWithTheNewerToken(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	r.writeSession(r.session(30*time.Second, "rt-stale"))
	r.bindTeam(testTeamID, "ops")
	r.be.onRefresh = func(w http.ResponseWriter, _ *http.Request, rt string) {
		switch rt {
		case "rt-stale":
			r.writeSession(r.session(30*time.Second, "rt-newer"))
			authErrorNew(w, http.StatusBadRequest, authRefreshTokenAlreadyUsed)
		case "rt-newer":
			r.be.writeSession(w, "rt-newest")
		default:
			t.Errorf("unexpected refresh token presented: %q", rt)
			authErrorNew(w, http.StatusBadRequest, authRefreshTokenNotFound)
		}
	}
	if err := call(t, r); err != nil {
		t.Fatal(err)
	}
	if r.be.calls("/auth/v1/token") != 2 {
		t.Fatalf("refresh calls = %d, want exactly 2", r.be.calls("/auth/v1/token"))
	}
	if r.readSession().RefreshToken != "rt-newest" {
		t.Fatalf("session.json = %+v", r.readSession())
	}
}

// TestAlreadyUsedWithNoNewerTokenIsTerminal: when the file still holds
// the token the server refused, the credential is terminal — session.json
// is deleted and the answer is exit 4 with the fixed text — and /token was
// called exactly once (never in a loop).
func TestAlreadyUsedWithNoNewerTokenIsTerminal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	r.writeSession(r.session(30*time.Second, "rt-stale"))
	r.bindTeam(testTeamID, "ops")
	r.be.onRefresh = func(w http.ResponseWriter, _ *http.Request, _ string) {
		authErrorLegacy(w, http.StatusBadRequest, authRefreshTokenAlreadyUsed)
	}
	err := call(t, r)
	if err == nil || err.Code != protocol.CodeUnauthenticated || err.Message != errCredentialRevoked().Message {
		t.Fatalf("err = %+v", err)
	}
	if r.be.calls("/auth/v1/token") != 1 {
		t.Fatalf("refresh calls = %d, want 1", r.be.calls("/auth/v1/token"))
	}
	if r.readSession() != nil {
		t.Fatalf("session.json survived a terminal error")
	}
	// The next command is `unauthenticated` from local files, no dial.
	before := r.be.total()
	got := r.fails("unauthenticated", 4, "", "session", "list")
	if r.be.total() != before {
		t.Fatalf("dialled after the credential was removed: %s", got.stdout)
	}
}

// TestTerminalCodesDeleteTheCredential: each of the four terminal GoTrue
// codes, in either body shape, deletes session.json at once.
func TestTerminalCodesDeleteTheCredential(t *testing.T) {
	t.Parallel()
	for _, code := range []string{authRefreshTokenNotFound, authSessionNotFound, authSessionExpired, authUserNotFound} {
		for _, legacy := range []bool{false, true} {
			r := newRig(t)
			r.initProfile()
			r.writeSession(r.session(10*time.Second, "rt-1"))
			r.bindTeam(testTeamID, "ops")
			r.be.onRefresh = func(w http.ResponseWriter, _ *http.Request, _ string) {
				if legacy {
					authErrorLegacy(w, http.StatusBadRequest, code)
				} else {
					authErrorNew(w, http.StatusBadRequest, code)
				}
			}
			err := call(t, r)
			if err == nil || err.Code != protocol.CodeUnauthenticated || err.Message != errCredentialRevoked().Message || err.Details["reason"] != reasonCredentialRevoked {
				t.Errorf("%s legacy=%v: %+v", code, legacy, err)
			}
			if r.readSession() != nil {
				t.Errorf("%s legacy=%v: session.json survived", code, legacy)
			}
			if r.be.calls("/auth/v1/token") != 1 {
				t.Errorf("%s legacy=%v: refresh calls = %d, want 1", code, legacy, r.be.calls("/auth/v1/token"))
			}
			// The next command answers from local files: unauthenticated,
			// no dial, and no token on either stream.
			before := r.be.total()
			got := r.fails("unauthenticated", 4, "", "session", "list")
			if r.be.total() != before || strings.Contains(got.stderr+got.stdout, "eyJ") {
				t.Errorf("%s legacy=%v: dialled or leaked after the terminal error: %s %s", code, legacy, got.stdout, got.stderr)
			}
		}
	}
}

// TestReadOnlyProfileRefreshesInMemory is the read-only fallback of 5.1:
// with the profile directory unwritable the adapter refreshes in memory,
// runs the command with the new token and persists nothing; the older
// refresh token stays in the file for the next writer.
func TestReadOnlyProfileRefreshesInMemory(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	r := newRig(t)
	r.initProfile()
	r.writeSession(r.session(30*time.Second, "rt-1"))
	r.bindTeam(testTeamID, "ops")
	before, _ := os.ReadFile(r.sessionPath())
	if err := os.Chmod(r.profileDir(), 0o500); err != nil { //nolint:gosec // G302: the read-only directory IS the case under test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.profileDir(), 0o700) }) //nolint:gosec // G302: restoring the 0700 directory so TempDir can clean up
	if err := call(t, r); err != nil {
		t.Fatal(err)
	}
	if r.be.calls("/auth/v1/token") != 1 {
		t.Fatalf("refresh calls = %d", r.be.calls("/auth/v1/token"))
	}
	after, _ := os.ReadFile(r.sessionPath())
	if string(before) != string(after) {
		t.Fatalf("session.json changed in a read-only directory")
	}
	if got := r.be.last(rpcPath).header.Get("Authorization"); got == "Bearer "+r.readSession().AccessToken {
		t.Fatalf("the RPC carried the stale token")
	}
	absent(t, adapterkit.SidecarPath(r.sessionPath()))
}

// TestJWTRejectedTriggersOneRefreshAndOneRetry is the cold-start path:
// PGRST303 → a forced refresh → one retry; a second PGRST303 is
// `unauthenticated`, and /token was called once.
func TestJWTRejectedTriggersOneRefreshAndOneRetry(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	rejected := 0
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, token string, _ map[string]any) {
		if token == r.readSession().AccessToken && rejected == 0 {
			rejected++
			postgrest(w, http.StatusUnauthorized, "PGRST303", "JWT expired")
			return
		}
		writeJSON(w, http.StatusOK, `{"ok":true}`)
	}
	if err := call(t, r); err != nil {
		t.Fatal(err)
	}
	if r.be.calls("/auth/v1/token") != 1 || r.be.calls(rpcPath) != 2 {
		t.Fatalf("refresh %d rpc %d, want 1 and 2", r.be.calls("/auth/v1/token"), r.be.calls(rpcPath))
	}
	if r.readSession().RefreshToken != "rt-1+1" {
		t.Fatalf("the forced refresh was not persisted: %+v", r.readSession())
	}

	r2 := newRig(t)
	r2.joined()
	r2.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusUnauthorized, "PGRST301", "JWT invalid")
	}
	err := call(t, r2)
	if err == nil || err.Code != protocol.CodeUnauthenticated {
		t.Fatalf("err = %+v", err)
	}
	if r2.be.calls("/auth/v1/token") != 1 || r2.be.calls(rpcPath) != 2 {
		t.Fatalf("refresh %d rpc %d, want 1 and 2", r2.be.calls("/auth/v1/token"), r2.be.calls(rpcPath))
	}
}

// TestSignUpAndBind is ensureIdentity + bind, the binding commands' half
// of the ladder: a profile without a credential mints one anonymous
// principal (the auth-js body, the api-version header), persists it 0600,
// binds the team into profile.json and remembers it in session.json.
func TestSignUpAndBind(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := r.command("team", "create")
	if err := c.ensureIdentity(t.Context()); asProtocolError(err).Code != protocol.CodeConfig {
		t.Fatalf("ensureIdentity without a profile = %v, want config", err)
	}
	r.initProfile()
	c = r.command("team", "create")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	signup := r.be.last("/auth/v1/signup")
	if signup == nil || string(signup.body) != `{"data":{},"gotrue_meta_security":{}}` || signup.header.Get("X-Supabase-Api-Version") != apiVersionHeader {
		t.Fatalf("sign-up request = %+v", signup)
	}
	s := r.readSession()
	if s == nil || s.RefreshToken != "rt-1" || s.principalRef() != testUserID {
		t.Fatalf("session.json after sign-up = %+v", s)
	}
	if err := c.bind(testTeamID, "ops", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil || p.TeamRef != testTeamID || p.TeamName != "ops" || p.PrincipalRef != testUserID || p.HumanLabel != "alice@example.com" {
		t.Fatalf("profile after bind = %+v %v", p, err)
	}
	if r.readSession().LastTeamRef != testTeamID || c.lastTeamRef() != testTeamID {
		t.Fatalf("last_team_ref not remembered")
	}
	if err := c.unbind(); err != nil {
		t.Fatal(err)
	}
	if c.lastTeamRef() != testTeamID {
		t.Fatalf("lastTeamRef after unbind = %q", c.lastTeamRef())
	}
	got := r.ok("describe")
	if str(t, got["profile"].(map[string]any), "state") != "not_member" {
		t.Fatalf("describe after unbind = %v", got)
	}
	// A second ensureIdentity reuses the credential: no second sign-up.
	c = r.command("team", "join")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.be.calls("/auth/v1/signup") != 1 {
		t.Fatalf("sign-ups = %d, want 1", r.be.calls("/auth/v1/signup"))
	}
	// A refused sign-up is mapped, and nothing is written.
	r3 := newRig(t)
	r3.initProfile()
	r3.be.onSignup = func(w http.ResponseWriter, _ *http.Request) { authErrorLegacy(w, 422, authSignupDisabled) }
	c = r3.command("team", "join")
	if err := c.ensureIdentity(t.Context()); asProtocolError(err).Code != protocol.CodeConfig || asProtocolError(err).Details["reason"] != reasonSignupDisabled {
		t.Fatalf("disabled sign-up = %v", err)
	}
	if r3.readSession() != nil {
		t.Fatalf("a refused sign-up wrote session.json")
	}
}

// TestCredentialFileLockIsASidecar: the lock is taken on session.json.lock
// beside the file, never on the file the atomic write renames over, and
// a describe never creates it.
func TestCredentialFileLockIsASidecar(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	r.writeSession(r.session(10*time.Second, "rt-1"))
	r.bindTeam(testTeamID, "ops")
	r.ok("describe")
	absent(t, filepath.Join(r.profileDir(), "session.json.lock"))
	if err := call(t, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.profileDir(), "session.json.lock")); err != nil {
		t.Fatalf("no sidecar after a refresh: %v", err)
	}
}
