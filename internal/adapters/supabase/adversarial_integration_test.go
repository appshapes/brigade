package supabase

// The live checks of the P2-6..P2-10 adversarial pass, kept as
// integration tests (brief section 7; every one self-skips without the
// stack through liveRig): the one-behind rule PAST the 10 s reuse window,
// refresh_token_already_used through the adapter's own state machine
// against the real GoTrue (the terminal path and the re-read-and-retry
// path, the latter with a reverse proxy standing in for the concurrent
// writer), the sign-out of `profile revoke-credentials` with an access
// token GoTrue will not verify (the defect the pass found: the family
// survived), the foreign-topic refusal timing and the ids-only broadcast
// payload. The three tests that must wait out the reuse window run only
// under BRIGADE_TEST_DOCKER=1 (`make test-integration` sets it), as
// TestIntegrationRefreshRotationAndOneBehind does, so a developer's
// `make test` with the stack up does not pay 33 s for them.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil"
)

// requireReuseWindow skips a test that must wait out the 10 s refresh
// token reuse interval unless the integration target asked for it.
func requireReuseWindow(t *testing.T) {
	t.Helper()
	if os.Getenv("BRIGADE_TEST_DOCKER") == "" {
		t.Skip("BRIGADE_TEST_DOCKER unset: this test waits out the 10 s reuse window")
	}
}

func verifyRotate(t *testing.T, c *command, rt string) *session {
	t.Helper()
	s, ae, err := c.client.refresh(t.Context(), rt)
	if err != nil || ae != nil || s == nil {
		t.Fatalf("refresh: err %v, auth error %+v", err, ae)
	}
	return s
}

// One-behind PAST the 10 s reuse window: rotate, wait 11 s, redeem the
// previous token: 200 with the active token (E0-6's one-behind rule,
// distinct from the reuse window); the active token still rotates.
func TestIntegrationAdversarialOneBehindPastReuseWindow(t *testing.T) {
	requireReuseWindow(t)
	r := liveRig(t)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	s0 := r.readSession()
	s1 := verifyRotate(t, c, s0.RefreshToken)
	time.Sleep(11 * time.Second)
	start := time.Now()
	got := verifyRotate(t, c, s0.RefreshToken) // one behind, past the window
	t.Logf("one-behind past the window: 200 in %s; answered the active token: %v (refresh token equal to s1's: %v)",
		time.Since(start).Round(time.Millisecond), got.AccessToken != "", got.RefreshToken == s1.RefreshToken)
	s2 := verifyRotate(t, c, got.RefreshToken)
	if s2.RefreshToken == got.RefreshToken {
		t.Fatalf("the active token did not rotate")
	}
}

// refresh_token_already_used through the adapter's state machine, live:
// two rotations, the two-behind token on disk under an access token
// inside the 90 s margin, then one command: exactly ONE /token call
// (already_used), the re-read finds nothing newer, terminal: exit 4 with
// the fixed rejoin text, details.reason credential_revoked, session.json
// deleted, no token on either stream — and the family survives (E0-6).
func TestIntegrationAdversarialAlreadyUsedTerminalLive(t *testing.T) {
	requireReuseWindow(t)
	r := liveRig(t)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	s0 := r.readSession()
	s1 := verifyRotate(t, c, s0.RefreshToken)
	s2 := verifyRotate(t, c, s1.RefreshToken)
	time.Sleep(11 * time.Second)
	stale := *s0
	stale.AccessToken = mintJWT(s0.User.ID, r.now.Add(30*time.Second)) // inside the margin: a refresh is forced
	r.writeSession(&stale)
	r.bindTeam(randomUUID, "verify")
	env := append(append([]string{}, r.env...), "BRIGADE_LOG_LEVEL=debug")
	got := r.execEnv(env, "", "session", "list")
	if got.code != 4 {
		t.Fatalf("exit %d, want 4: %s %s", got.code, got.stdout, got.stderr)
	}
	assertErrorCode(t, got.stdout, "unauthenticated")
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	if object["message"] != errCredentialRevoked().Message || details(t, got.stdout)["reason"] != reasonCredentialRevoked {
		t.Fatalf("error = %v", object)
	}
	if r.readSession() != nil {
		t.Fatalf("session.json survived the terminal error")
	}
	if n := strings.Count(got.stderr, "refresh refused"); n != 1 {
		t.Fatalf("/token refusals logged: %d, want exactly 1\n%s", n, got.stderr)
	}
	if !strings.Contains(got.stderr, "credential is terminal") || !strings.Contains(got.stderr, authRefreshTokenAlreadyUsed) {
		t.Fatalf("stderr does not show the already_used → terminal path:\n%s", got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, "eyJ") {
		t.Fatalf("a token reached a stream")
	}
	t.Logf("stderr:\n%s", got.stderr)
	if s3 := verifyRotate(t, c, s2.RefreshToken); s3 == nil {
		t.Fatalf("the family did not survive")
	}
}

// The re-read-and-retry against the real GoTrue answers: a reverse proxy
// in front of the stack stores the ACTIVE pair into session.json the
// instant the adapter presents the two-behind token, so the re-read
// after already_used finds a newer token and the one retry succeeds —
// two /token calls, no terminal error, the file rotated once more.
func TestIntegrationAdversarialAlreadyUsedRereadRetryLive(t *testing.T) {
	requireReuseWindow(t)
	live := testutil.RequireSupabase(t)
	target, err := url.Parse(live.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	r.now = time.Now()
	var armed atomic.Bool
	var tokenCalls atomic.Int32
	var active atomic.Pointer[session]
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/auth/v1/token" && armed.Load() {
			if tokenCalls.Add(1) == 1 {
				r.writeSession(active.Load()) // another process stores the active pair mid-flight
			}
		}
		proxy.ServeHTTP(w, req)
	}))
	defer front.Close()
	r.ok("profile", "init", "--url", front.URL, "--key", live.PublishableKey)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	s0 := r.readSession()
	s1 := verifyRotate(t, c, s0.RefreshToken)
	s2 := verifyRotate(t, c, s1.RefreshToken)
	time.Sleep(11 * time.Second)
	stale := *s0
	stale.AccessToken = mintJWT(s0.User.ID, r.now.Add(30*time.Second))
	r.writeSession(&stale)
	r.bindTeam(randomUUID, "verify")
	active.Store(s2)
	armed.Store(true)
	c = r.command("session", "list")
	if err := c.authenticate(true); err != nil {
		t.Fatal(err)
	}
	token, err := c.accessToken(t.Context())
	if err != nil {
		t.Fatalf("accessToken after already_used with a newer token on disk: %v", err)
	}
	if n := tokenCalls.Load(); n != 2 {
		t.Fatalf("/token calls = %d, want exactly 2 (the refusal, then the one retry)", n)
	}
	final := r.readSession()
	if final == nil || final.AccessToken != token || final.RefreshToken == s2.RefreshToken || final.RefreshToken == stale.RefreshToken {
		t.Fatalf("session.json after the retry = %+v", final)
	}
	t.Logf("already_used → re-read → one retry with the newer token → 200; the file now holds the rotated pair")
}

// DEFECT PROBE: `profile revoke-credentials` with an access token GoTrue
// rejects (an expired one after an idle hour, or an unverifiable one) —
// the sign-out's 401 is taken for "already dead" and session.json is
// deleted while the refresh-token family stays alive at the backend.
func TestIntegrationAdversarialRevokeCredentialsWithRejectedAccessToken(t *testing.T) {
	r := liveRig(t)
	c := r.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved := r.readSession()
	forged := *saved
	forged.AccessToken = mintJWT(saved.User.ID, time.Now().Add(time.Hour)) // GoTrue answers 401 for it, as for an expired token
	r.writeSession(&forged)
	env := append(append([]string{}, r.env...), "BRIGADE_LOG_LEVEL=debug")
	got := r.execEnv(env, "", "profile", "revoke-credentials")
	t.Logf("revoke-credentials: exit %d, stdout %s, session.json present afterwards: %v", got.code, strings.TrimSpace(got.stdout), r.readSession() != nil)
	next, ae, err := c.client.refresh(t.Context(), saved.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		_ = c.client.signOutGlobal(t.Context(), next.AccessToken) // clean up the family this probe left alive
		t.Fatalf("DEFECT: revoke-credentials answered exit %d and removed the local file, but the saved refresh token still rotated: the family was never revoked", got.code)
	}
	if !ae.terminal() {
		t.Fatalf("refresh of the saved token after revoke: %+v, want a terminal code", ae)
	}
	t.Logf("family revoked: the saved refresh token answers %s (exit %d)", ae.code, got.code)
}

// The private channel refuses a FOREIGN topic as a phx_reply error after
// the server's fixed backoff — classified `unauthorized` by this adapter,
// never a join timeout — measured through the adapter's own Phoenix
// client.
func TestIntegrationAdversarialForeignTopicRefusedNotTimedOut(t *testing.T) {
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
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	p := &phxClient{conn: conn, vsn: phxVersionV1, log: c.log}
	topic := sessionTopic(randomUUID)
	start := time.Now()
	joinRef, err := p.join(t.Context(), topic, token)
	if err != nil {
		t.Fatal(err)
	}
	for {
		typ, data, err := conn.Read(t.Context())
		if err != nil {
			t.Fatalf("read after %s: %v", time.Since(start), err)
		}
		f, derr := decodePhxFrame(phxVersionV1, typ, data)
		if derr != nil || f.Event != phxEventReply || f.Ref != joinRef {
			continue
		}
		var reply phxReply
		if err := json.Unmarshal(f.Payload, &reply); err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		if reply.Status == "ok" {
			t.Fatalf("a foreign topic was joined")
		}
		if got := classifyReason(reply.Response.Reason); got != linkReasonUnauthorized {
			t.Fatalf("refusal classified %q, want unauthorized (reason %q)", got, reply.Response.Reason)
		}
		if took > watchTiming.joinTimeout {
			t.Fatalf("the refusal took %s, past the %s join timeout", took, watchTiming.joinTimeout)
		}
		t.Logf("foreign topic refused as a phx_reply error after %s (join timeout %s): %s", took.Round(time.Millisecond), watchTiming.joinTimeout, reply.Response.Reason)
		return
	}
}

// The message_accepted broadcast carries ids only — message_id and seq —
// never a body or a summary (D21: the channel is a hint).
func TestIntegrationAdversarialBroadcastPayloadIsIdsOnly(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "verify-ids"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
	c := b.command("message", "watch")
	if err := c.authenticate(true); err != nil {
		t.Fatal(err)
	}
	token, err := c.accessToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := c.client.dialRealtime(t.Context(), b.env)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	p := &phxClient{conn: conn, vsn: phxVersionV1, log: c.log}
	topic := sessionTopic(sb)
	joinRef, err := p.join(t.Context(), topic, token)
	if err != nil {
		t.Fatal(err)
	}
	next := func() phxFrame {
		for {
			typ, data, err := conn.Read(t.Context())
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if f, derr := decodePhxFrame(phxVersionV1, typ, data); derr == nil {
				return f
			}
		}
	}
	for {
		f := next()
		if f.Event == phxEventReply && f.Ref == joinRef {
			var reply phxReply
			_ = json.Unmarshal(f.Payload, &reply)
			if reply.Status != "ok" {
				t.Fatalf("own topic refused: %s", reply.Response.Reason)
			}
			break
		}
	}
	res := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"a body that must not travel on the channel","summary":"nor this summary"}`)
	id, _ := res["message_id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f := next()
		if !hintFor(f, topic) {
			continue
		}
		var bc phxBroadcast
		if err := json.Unmarshal(f.Payload, &bc); err != nil {
			t.Fatal(err)
		}
		var inner map[string]any
		if err := json.Unmarshal(bc.Payload, &inner); err != nil {
			t.Fatalf("inner payload %s: %v", bc.Payload, err)
		}
		t.Logf("broadcast frame payload: %s", f.Payload)
		if inner["message_id"] != id {
			t.Fatalf("message_id %v, want %s", inner["message_id"], id)
		}
		// The migration sends {message_id, seq}; Realtime adds the
		// realtime.messages row id as `id`. All three are ids.
		for k := range inner {
			if k != "message_id" && k != "seq" && k != "id" {
				t.Errorf("the broadcast carries %q; want ids only", k)
			}
		}
		if strings.Contains(string(f.Payload), "must not travel") || strings.Contains(string(f.Payload), "nor this summary") {
			t.Fatalf("the broadcast carries message content")
		}
		return
	}
	t.Fatalf("no message_accepted broadcast within 10 s")
}
