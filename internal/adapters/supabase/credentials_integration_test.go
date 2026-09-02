package supabase

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// The paths only the real stack shows (brief section 7). Every test here
// self-skips through testutil.RequireSupabase without `.env.test` or a
// running stack, so `make test` stays Docker-free; `make test-integration`
// runs them against the local stack. Each test mints its own anonymous
// principal and never resets the database. P2-11 completes this file.

// liveRig is a rig whose profile points at the real stack.
func liveRig(t *testing.T) *rig {
	t.Helper()
	env := testutil.RequireSupabase(t)
	r := newRig(t)
	r.now = time.Now()
	r.ok("profile", "init", "--url", env.URL, "--key", env.PublishableKey)
	return r
}

// TestIntegrationAnonymousSignUpAndClaims: sign-up mints an anonymous
// principal whose JWT carries role authenticated, is_anonymous true and
// sub = user.id (5.1), persisted 0600.
func TestIntegrationAnonymousSignUpAndClaims(t *testing.T) {
	r := liveRig(t)
	c := r.command("team", "create")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatalf("sign-up: %v", err)
	}
	s := r.readSession()
	if s == nil || !s.usable() {
		t.Fatalf("session.json = %+v", s)
	}
	claims, ok := jwtClaims(s.AccessToken)
	if !ok || claims["role"] != "authenticated" || claims["is_anonymous"] != true || claims["sub"] != s.User.ID || s.User.ID == "" {
		t.Fatalf("claims = %v (user %+v)", claims, s.User)
	}
	if exp, ok := s.expiry(); !ok || time.Until(exp) < time.Minute {
		t.Fatalf("expiry = %v %v", exp, ok)
	}
	info, err := os.Stat(r.sessionPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	if !validUUID(s.principalRef()) {
		t.Fatalf("principal_ref %q is not a uuid", s.principalRef())
	}
}

// TestIntegrationRefreshRotationAndOneBehind: a forced refresh rotates
// the pair; the token one step behind is still answered 200 (E0-6:
// one-behind tolerance, load-bearing for crash recovery); inside the 10 s
// reuse window the same token refreshed twice yields the same family.
func TestIntegrationRefreshRotationAndOneBehind(t *testing.T) {
	r := liveRig(t)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := r.readSession()
	if _, err := c.forceRefresh(t.Context()); err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	second := r.readSession()
	if second.RefreshToken == first.RefreshToken || second.AccessToken == first.AccessToken {
		t.Fatalf("refresh did not rotate")
	}
	// One behind: the first token again. Inside the reuse window this is
	// answered with the active pair; past it, still 200 with the active
	// token (E0-6). Either way the family survives and the call is 200.
	got, ae, err := c.client.refresh(t.Context(), first.RefreshToken)
	if err != nil || ae != nil || got == nil {
		t.Fatalf("one-behind refresh: %v %+v", err, ae)
	}
	// The active token still rotates normally afterwards.
	third, ae, err := c.client.refresh(t.Context(), got.RefreshToken)
	if err != nil || ae != nil || third == nil {
		t.Fatalf("active refresh after one-behind: %v %+v", err, ae)
	}
	if os.Getenv("BRIGADE_TEST_DOCKER") == "" {
		t.Log("BRIGADE_TEST_DOCKER unset: the 10 s reuse-window and two-behind checks were not run")
		return
	}
	// Past the 10 s reuse interval: two-or-more-behind is refused with
	// refresh_token_already_used, and the family SURVIVES (E0-6).
	time.Sleep(11 * time.Second)
	_, ae, err = c.client.refresh(t.Context(), first.RefreshToken)
	if err != nil || ae == nil || ae.code != authRefreshTokenAlreadyUsed {
		t.Fatalf("two-behind refresh: %v %+v", err, ae)
	}
	fourth, ae, err := c.client.refresh(t.Context(), third.RefreshToken)
	if err != nil || ae != nil || fourth == nil {
		t.Fatalf("the family did not survive a two-behind refusal: %v %+v", err, ae)
	}
}

// TestIntegrationGlobalSignOutRevokesTheFamily: after `profile
// revoke-credentials` a saved copy of the old session.json answers
// refresh_token_not_found, which the state machine treats as terminal.
func TestIntegrationGlobalSignOutRevokesTheFamily(t *testing.T) {
	r := liveRig(t)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved := r.readSession()
	r.ok("profile", "revoke-credentials")
	if r.readSession() != nil {
		t.Fatalf("session.json survived revoke-credentials")
	}
	// The leaked copy comes back.
	r.writeSession(saved)
	r.bindTeam(testTeamID, "ops")
	_, ae, err := c.client.refresh(t.Context(), saved.RefreshToken)
	if err != nil || ae == nil || !ae.terminal() {
		t.Fatalf("refresh of a signed-out token: %v %+v", err, ae)
	}
	// Through the state machine: the copy is unusable, terminal, deleted.
	c = r.command("session", "list")
	if err := c.authenticate(true); err != nil {
		t.Fatal(err)
	}
	_, err = c.forceRefresh(t.Context())
	if perr := asProtocolError(err); perr.Code != protocol.CodeUnauthenticated || perr.Details["reason"] != reasonCredentialRevoked {
		t.Fatalf("forced refresh of a revoked copy = %+v", perr)
	}
	if r.readSession() != nil {
		t.Fatalf("the revoked copy was not deleted")
	}
}

// TestIntegrationErrorMappingLive: the finished migrations' answers
// through the real gateway — PT404 as HTTP 404 → not_found (byte-identical
// for a random id), 42501 as 403 with a JWT → unauthorized for a random
// team (byte-identical to a foreign one), 42501 as 401 without a usable
// JWT → unauthenticated, PGRST202 → migration drift — and the raw server
// text never reaches the mapped message.
func TestIntegrationErrorMappingLive(t *testing.T) {
	r := liveRig(t)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	token, err := c.accessToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	random := "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"

	resp, err := c.client.callRPC(t.Context(), token, "fetch_inbox", rpcArgs{"p_session_id": random})
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusNotFound {
		t.Fatalf("fetch_inbox of a random id: HTTP %d, want 404 (PT404); body %s", resp.status, resp.body)
	}
	if got := newFailure(t, mapRPCFailure(resp.status, resp.body)); got != newFailure(t, errNotFound()) {
		t.Fatalf("not_found differs: %s", got)
	}

	resp, err = c.client.callRPC(t.Context(), token, "list_members", rpcArgs{"p_team_id": random})
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusForbidden {
		t.Fatalf("list_members of a random team: HTTP %d, want 403; body %s", resp.status, resp.body)
	}
	if got := newFailure(t, mapRPCFailure(resp.status, resp.body)); got != newFailure(t, errNotMember()) {
		t.Fatalf("unauthorized differs: %s", got)
	}

	// The publishable key alone, no JWT: 401 with 42501 → unauthenticated.
	resp, err = c.client.post(t.Context(), rpcPath+"list_members", map[string]string{
		"Accept-Profile": schemaProfile, "Content-Profile": schemaProfile, "Content-Type": "application/json",
	}, []byte(`{"p_team_id":"`+random+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("no JWT: HTTP %d, want 401; body %s", resp.status, resp.body)
	}
	if got := mapRPCFailure(resp.status, resp.body); got.Code != protocol.CodeUnauthenticated {
		t.Fatalf("no JWT maps to %+v", got)
	}

	resp, err = c.client.callRPC(t.Context(), token, "no_such_function", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := mapRPCFailure(resp.status, resp.body); got.Code != protocol.CodeInternal || got.Details["reason"] != reasonMigrationDrift {
		t.Fatalf("unknown function (HTTP %d %s) maps to %+v", resp.status, resp.body, got)
	}

	// The command-level rpc on a joined profile: the same uniform texts.
	r.bindTeam(random, "nope")
	c = r.command("session", "list")
	if err := c.authenticate(true); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	err = c.rpc(t.Context(), "list_members", rpcArgs{"p_team_id": random}, &out)
	if got := newFailure(t, asProtocolError(err)); got != newFailure(t, errNotMember()) {
		t.Fatalf("rpc list_members = %s", got)
	}
}

// TestIntegrationRealtimeForeignTopicRefused: the private channel of 5.6
// refuses a join on a topic the principal does not own — after the
// server's fixed backoff — with a phx_reply carrying status error, over
// the client's own dial (apikey in the query, vsn 1.0.0 JSON frames).
func TestIntegrationRealtimeForeignTopicRefused(t *testing.T) {
	r := liveRig(t)
	c := r.command("message", "watch")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	token, err := c.accessToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := c.client.dialRealtime(t.Context(), r.env)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	join, _ := json.Marshal(map[string]any{
		"topic": "realtime:brigade:session:0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f", "event": "phx_join", "ref": "1",
		"payload": map[string]any{"access_token": token, "config": map[string]any{
			"private": true, "broadcast": map[string]any{"self": false, "ack": false},
			"presence": map[string]any{"enabled": false, "key": ""}, "postgres_changes": []any{},
		}},
	})
	if err := conn.Write(t.Context(), websocket.MessageText, join); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := conn.Read(t.Context())
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Event   string `json:"event"`
			Payload struct {
				Status   string `json:"status"`
				Response struct {
					Reason string `json:"reason"`
				} `json:"response"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("frame %s: %v", data, err)
		}
		if frame.Event != "phx_reply" {
			continue
		}
		if frame.Payload.Status != "error" {
			t.Fatalf("a foreign topic was joined: %s", data)
		}
		if !strings.Contains(frame.Payload.Response.Reason, "Unauthorized") && !strings.Contains(frame.Payload.Response.Reason, "brigade:session:") {
			t.Logf("refusal reason: %s", frame.Payload.Response.Reason)
		}
		return
	}
	t.Fatalf("no phx_reply within 15 s")
}

// TestIntegrationProfileResetRevokesFamily is I-34, the credential
// revocation test 9.9 names: `profile reset` signs the principal out
// GLOBALLY before it deletes the profile directory, so a copy of
// session.json saved beforehand is worthless — its refresh token answers
// refresh_token_not_found, which the state machine treats as terminal.
// The distinction from TestIntegrationGlobalSignOutRevokesTheFamily is
// the verb: `profile reset` removes the whole profile (4.2) and its
// sign-out is best effort, so the assertion that the family really died
// has to be made against the backend rather than inferred from the exit
// status.
func TestIntegrationProfileResetRevokesFamily(t *testing.T) {
	r := liveRig(t)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved := r.readSession()
	if saved == nil || saved.RefreshToken == "" {
		t.Fatalf("no credential to revoke")
	}

	reset := r.ok("profile", "reset")
	if reset["state"] != protocol.ProfileStateUnconfigured {
		t.Errorf("profile reset: state %v, want unconfigured", reset["state"])
	}
	if r.readSession() != nil {
		t.Fatalf("session.json survived profile reset")
	}
	if got := entries(t, r.profileDir()); len(got) != 0 {
		t.Errorf("profile reset left %v behind", got)
	}

	// The saved copy: the family is gone at the backend.
	_, ae, err := c.client.refresh(t.Context(), saved.RefreshToken)
	if err != nil {
		t.Fatalf("refresh of the saved copy: %v", err)
	}
	if ae == nil {
		t.Fatalf("the refresh token saved before `profile reset` still rotates: the family was never revoked (I-34)")
	}
	if ae.code != authRefreshTokenNotFound || !ae.terminal() {
		t.Errorf("refresh after profile reset: code %q terminal %v, want %q and terminal", ae.code, ae.terminal(), authRefreshTokenNotFound)
	}
	t.Logf("the saved refresh token answers %q after `profile reset`", ae.code)
}
