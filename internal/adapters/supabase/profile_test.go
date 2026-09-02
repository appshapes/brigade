package supabase

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// TestBackendURLRule is U-26: https anywhere, http only for a loopback
// host, both branches, plus the malformed shapes.
func TestBackendURLRule(t *testing.T) {
	t.Parallel()
	for raw, ok := range map[string]bool{
		"https://abc.supabase.co":         true,
		"https://abc.supabase.co/":        true,
		"http://127.0.0.1:54321":          true,
		"http://localhost:54321":          true,
		"http://LOCALHOST":                true,
		"http://[::1]:54321":              true,
		"http://127.0.0.2:54321":          true,
		"http://abc.supabase.co":          false,
		"http://10.0.0.1":                 false,
		"http://localhost.example":        false,
		"ftp://abc.supabase.co":           false,
		"abc.supabase.co":                 false,
		"":                                false,
		"https://user:pw@abc.supabase.co": false,
		"https://abc.supabase.co?x=1":     false,
	} {
		err := checkBackendURL(raw)
		if (err == nil) != ok {
			t.Errorf("checkBackendURL(%q) = %v, want ok=%v", raw, err, ok)
		}
		if err != nil {
			perr := asProtocolError(err)
			if perr.Code != "invalid_input" || perr.Details["field"] != "url" || !strings.Contains(perr.Message, "https") {
				t.Errorf("checkBackendURL(%q): %+v", raw, perr)
			}
		}
	}
}

// TestProfileInit covers 5.2: the backend pair is written, an http
// non-loopback url is invalid_input, a secret key is refused, a second
// init is conflict unless --force, and --force rewrites the backend only.
func TestProfileInit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := r.fails("usage", 2, "", "profile", "init", "--key", testKey)
	_ = got
	r.fails("usage", 2, "", "profile", "init", "--url", "https://x.example")
	got = r.fails("invalid_input", 3, "", "profile", "init", "--url", "http://x.example", "--key", testKey)
	if details(t, got.stdout)["field"] != "url" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	got = r.fails("invalid_input", 3, "", "profile", "init", "--url", "https://x.example", "--key", "sb_secret_NOTREAL")
	if details(t, got.stdout)["field"] != "key" || strings.Contains(got.stdout+got.stderr, "NOTREAL") {
		t.Fatalf("secret key: %s %s", got.stdout, got.stderr)
	}
	absent(t, r.profileDir())

	result := r.ok("profile", "init", "--url", "https://x.example/", "--key", testKey)
	if str(t, result, "state") != "unauthenticated" || str(t, result, "url") != "https://x.example" {
		t.Fatalf("init result = %v", result)
	}
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil {
		t.Fatal(err)
	}
	if p.Adapter != adapterKind || p.URL != "https://x.example" || p.PublishableKey != testKey || p.SecretStore != adapterkit.SecretStoreFile {
		t.Fatalf("profile = %+v", p)
	}
	info, err := os.Stat(filepath.Join(r.profileDir(), "profile.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile.json mode: %v %v", info, err)
	}

	got = r.fails("conflict", 7, "", "profile", "init", "--url", "https://y.example", "--key", testKey)
	if details(t, got.stdout)["reason"] != reasonProfileExists {
		t.Fatalf("details = %v", details(t, got.stdout))
	}

	r.writeSession(r.session(time.Hour, "rt-1"))
	r.bindTeam(testTeamID, "ops")
	result = r.ok("profile", "init", "--url", r.be.srv.URL, "--key", testKey+"2", "--force")
	if str(t, result, "state") != "joined" || str(t, result, "principal_ref") != testUserID {
		t.Fatalf("force result = %v", result)
	}
	p, err = adapterkit.LoadProfile(r.cfg, "default")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != r.be.srv.URL || p.PublishableKey != testKey+"2" || p.TeamRef != testTeamID || p.TeamName != "ops" {
		t.Fatalf("--force rewrote more than the backend: %+v", p)
	}
	if r.readSession() == nil {
		t.Fatalf("--force removed session.json")
	}
}

// TestProfileStatusNeverPrintsATOKEN: the status carries the state, the
// url, the identity and the token's expiry — never a token.
func TestProfileStatusNeverPrintsAToken(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.ok("profile", "status")
	if str(t, result, "state") != "unconfigured" {
		t.Fatalf("status = %v", result)
	}
	r.joined()
	got := r.exec("", "profile", "status")
	if got.code != 0 {
		t.Fatal(got.stdout)
	}
	s := r.readSession()
	if strings.Contains(got.stdout, s.AccessToken) || strings.Contains(got.stdout, s.RefreshToken) || strings.Contains(got.stdout, "eyJ") {
		t.Fatalf("status leaks a token: %s", got.stdout)
	}
	result = decode(t, got.stdout)["result"].(map[string]any)
	if str(t, result, "state") != "joined" || str(t, result, "team_name") != "ops" || str(t, result, "principal_ref") != testUserID {
		t.Fatalf("status = %v", result)
	}
	if _, ok := result["token_expires_at"].(string); !ok {
		t.Fatalf("status carries no token_expires_at: %v", result)
	}
	if r.be.total() != 0 {
		t.Fatalf("profile status dialled the backend")
	}
}

// TestProfileReset: a best-effort global sign-out with the access token,
// then the profile directory is gone; a failing sign-out is ignored; a
// reset of nothing is still a success.
func TestProfileReset(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.ok("profile", "reset")
	if str(t, result, "state") != "unconfigured" {
		t.Fatalf("reset result = %v", result)
	}
	r.joined()
	token := r.readSession().AccessToken
	result = r.ok("profile", "reset")
	if str(t, result, "state") != "unconfigured" {
		t.Fatalf("reset result = %v", result)
	}
	absent(t, r.profileDir())
	last := r.be.last("/auth/v1/logout")
	if last == nil || !strings.Contains(last.path, "scope=global") {
		t.Fatalf("sign-out request = %+v", last)
	}
	// The sign-out carries a token refreshed just before it, never the
	// one the file held (which may be expired and would be refused).
	assertRefreshedBeforeSignOut(t, r, token)

	r.joined()
	r.be.onLogout = func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, `{"message":"boom"}`)
	}
	r.ok("profile", "reset")
	absent(t, r.profileDir())

	r.joined()
	offline := append(append([]string{}, r.env...), "BRIGADE_TEST_OFFLINE=1")
	if got := r.execEnv(offline, "", "profile", "reset"); got.code != 0 {
		t.Fatalf("reset offline: exit %d %s", got.code, got.stdout)
	}
	absent(t, r.profileDir())
}

// TestProfileRevokeCredentials: the sign-out only, then session.json is
// removed and profile.json with its binding stays (state
// unauthenticated); a backend that cannot be reached leaves the file and
// answers `unavailable`; a backend that says the token is already dead
// counts as done.
func TestProfileRevokeCredentials(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.ok("profile", "revoke-credentials")
	if str(t, result, "state") != "unconfigured" {
		t.Fatalf("result = %v", result)
	}

	r.joined()
	offline := append(append([]string{}, r.env...), "BRIGADE_TEST_OFFLINE=1")
	got := r.execEnv(offline, "", "profile", "revoke-credentials")
	if got.code != 9 {
		t.Fatalf("offline revoke: exit %d, want 9 (unavailable): %s", got.code, got.stdout)
	}
	assertErrorCode(t, got.stdout, "unavailable")
	if r.readSession() == nil {
		t.Fatalf("offline revoke removed session.json")
	}

	stale := r.readSession().AccessToken
	result = r.ok("profile", "revoke-credentials")
	if str(t, result, "state") != "unauthenticated" {
		t.Fatalf("result = %v", result)
	}
	if r.readSession() != nil {
		t.Fatalf("session.json survived revoke-credentials")
	}
	assertRefreshedBeforeSignOut(t, r, stale)
	absent(t, adapterkit.SidecarPath(r.sessionPath()))
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil || p.TeamRef != testTeamID {
		t.Fatalf("profile after revoke: %+v %v", p, err)
	}
	r.fails("unauthenticated", 4, "", "session", "list")

	r.writeSession(r.session(time.Hour, "rt-9"))
	r.be.onLogout = func(w http.ResponseWriter, _ *http.Request) {
		authErrorNew(w, http.StatusUnauthorized, "session_not_found")
	}
	r.ok("profile", "revoke-credentials")
	if r.readSession() != nil {
		t.Fatalf("a dead token was not treated as revoked")
	}
}

// assertRefreshedBeforeSignOut pins the order the sign-out needs: one
// /token call BEFORE /logout, and the logout's bearer is the refreshed
// token, not the stale one the file held. GoTrue answers 401 for an
// access token it will not verify (an expired one after an idle hour),
// and a sign-out that took that 401 for "already dead" would delete the
// local file while the refresh-token family lived on — measured live in
// the P2-6..P2-10 adversarial pass.
func assertRefreshedBeforeSignOut(t *testing.T, r *rig, stale string) {
	t.Helper()
	r.be.mu.Lock()
	defer r.be.mu.Unlock()
	refreshAt, logoutAt := -1, -1
	var bearer string
	for i, req := range r.be.requests {
		switch {
		case strings.HasPrefix(req.path, "/auth/v1/token") && refreshAt < 0:
			refreshAt = i
		case strings.HasPrefix(req.path, "/auth/v1/logout"):
			logoutAt = i
			bearer = strings.TrimPrefix(req.header.Get("Authorization"), "Bearer ")
		}
	}
	if refreshAt < 0 || logoutAt < 0 || refreshAt > logoutAt {
		t.Fatalf("sign-out order: /token at %d, /logout at %d; want a refresh before the sign-out", refreshAt, logoutAt)
	}
	if bearer == "" || bearer == stale {
		t.Fatalf("the sign-out carried the stale access token, not the refreshed one")
	}
}

// TestRevokeCredentialsRefusedRefresh: the sign-out's refresh decides
// what revoke-credentials means. A refresh that is TERMINAL (the family
// is already gone) is a sign-out already done — session.json is removed
// and no /logout is sent; a refresh the backend refuses otherwise (a 400
// that is not terminal) is reported and nothing is deleted; a refresh
// that cannot reach the backend is `unavailable` and nothing is deleted.
func TestRevokeCredentialsRefusedRefresh(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.be.onRefresh = func(w http.ResponseWriter, _ *http.Request, _ string) {
		authErrorNew(w, http.StatusBadRequest, authRefreshTokenNotFound)
	}
	result := r.ok("profile", "revoke-credentials")
	if str(t, result, "state") != "unauthenticated" || r.readSession() != nil {
		t.Fatalf("terminal refresh: result %v, session present %v", result, r.readSession() != nil)
	}
	if r.be.calls("/auth/v1/logout") != 0 || r.be.calls("/auth/v1/token") != 1 {
		t.Fatalf("terminal refresh: logout %d token %d, want 0 and 1", r.be.calls("/auth/v1/logout"), r.be.calls("/auth/v1/token"))
	}

	r2 := newRig(t)
	r2.joined()
	r2.be.onRefresh = func(w http.ResponseWriter, _ *http.Request, _ string) {
		authErrorNew(w, http.StatusBadRequest, "validation_failed")
	}
	r2.fails("unauthenticated", 4, "", "profile", "revoke-credentials")
	if r2.readSession() == nil || r2.be.calls("/auth/v1/logout") != 0 {
		t.Fatalf("a refused refresh deleted session.json or still signed out")
	}

	r3 := newRig(t)
	r3.joined()
	r3.be.onRefresh = func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusBadGateway, `<html>bad gateway</html>`)
	}
	r3.fails("unavailable", 9, "", "profile", "revoke-credentials")
	if r3.readSession() == nil {
		t.Fatalf("an unavailable backend lost the local credential")
	}
}
