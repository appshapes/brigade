package supabase

// The live checks of the P2-6..P2-10 adversarial pass, kept as
// integration tests (brief section 7; every one self-skips through
// liveRig without the BRIGADE_TEST_LIVE opt-in — testutil.LiveTestVar,
// which only `make test-integration` sets — or without the stack): the
// one-behind rule PAST the 10 s reuse window, refresh_token_already_used
// through the adapter's own state machine against the real GoTrue (the
// terminal path and the re-read-and-retry path, the latter with a reverse
// proxy standing in for the concurrent writer), the sign-out of `profile
// revoke-credentials` with an access token GoTrue will not verify (the
// defect the pass found: the family survived), the foreign-topic refusal
// timing and the ids-only broadcast payload. The three tests that must
// wait out the reuse window need BRIGADE_TEST_DOCKER=1 on top of the
// opt-in (`make test-integration` sets both), as
// TestIntegrationRefreshRotationAndOneBehind does, so an ad-hoc
// `BRIGADE_TEST_LIVE=1 go test` does not pay 33 s for them — `make test`
// reaches none of this file at all now.

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
	frames := liveFrames(t, conn, phxVersionV1)
	topic := sessionTopic(randomUUID)
	start := time.Now()
	joinRef, err := p.join(t.Context(), topic, token)
	if err != nil {
		t.Fatal(err)
	}
	// The wait carries slack past the join timeout so that an OVER-BUDGET
	// refusal is observed and fails the `took` assertion below with the
	// number it took, rather than being pre-empted by the wait itself. The
	// bound that matters is that this cannot reach the server's close of an
	// un-heartbeated socket, 60 s from the dial.
	const refusalSlack = 5 * time.Second
	f := mustAwaitFrame(t, frames, "phx_reply to the foreign join", topic, watchTiming.joinTimeout+refusalSlack,
		func(f phxFrame) bool { return f.Event == phxEventReply && f.Ref == joinRef })
	took := time.Since(start)
	var reply phxReply
	if err := json.Unmarshal(f.Payload, &reply); err != nil {
		t.Fatal(err)
	}
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
	frames := liveFrames(t, conn, phxVersionV1)
	topic := sessionTopic(sb)
	joinRef, err := p.join(t.Context(), topic, token)
	if err != nil {
		t.Fatal(err)
	}
	joined := mustAwaitFrame(t, frames, "phx_reply to the join", topic, watchTiming.joinTimeout,
		func(f phxFrame) bool { return f.Event == phxEventReply && f.Ref == joinRef })
	var reply phxReply
	_ = json.Unmarshal(joined.Payload, &reply)
	if reply.Status != "ok" {
		t.Fatalf("own topic refused: %s", reply.Response.Reason)
	}
	// One retry of send-and-wait. The fan-out to a FRESH join is not warm
	// the instant phx_reply says ok (watch.go:68-78, measured three times:
	// "a broadcast issued in the first seconds after a socket joined its
	// topic ... reached no socket"), and this test sends one statement
	// after its join, so a first send can be missed under load. A second
	// message costs nothing here: what is asserted is the SHAPE of the
	// broadcast payload, and every message_accepted has the same shape.
	const hintWindow = 10 * time.Second
	sent := map[string]bool{}
	var hint phxFrame
	for attempt := 1; attempt <= 2; attempt++ {
		res := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"a body that must not travel on the channel","summary":"nor this summary"}`)
		id, _ := res["message_id"].(string)
		if id == "" {
			// Without this the membership check below is satisfiable by a
			// broadcast that carries no message_id either: sent[""] would be
			// true. The equality this replaced could not be fooled that way.
			t.Fatalf("message send returned no message_id: %v", res)
		}
		sent[id] = true
		f, got := awaitFrame(frames, hintWindow, func(f phxFrame) bool { return hintFor(f, topic) })
		if got == frameFound {
			hint = f
			break
		}
		if got == frameSocketClosed {
			t.Fatalf("the socket closed while waiting for the message_accepted broadcast on %s", topic)
		}
		if attempt == 2 {
			t.Fatalf("no message_accepted broadcast on %s within %s after each of the %d sends", topic, hintWindow, len(sent))
		}
		t.Logf("no broadcast on %s within %s after send %s; sending once more", topic, hintWindow, id)
	}
	var bc phxBroadcast
	if err := json.Unmarshal(hint.Payload, &bc); err != nil {
		t.Fatal(err)
	}
	var inner map[string]any
	if err := json.Unmarshal(bc.Payload, &inner); err != nil {
		t.Fatalf("inner payload %s: %v", bc.Payload, err)
	}
	t.Logf("broadcast frame payload: %s", hint.Payload)
	// Membership, not equality: hintFor matches on event and topic only
	// (realtime.go:227), so after a retry a late FIRST broadcast can be the
	// one that arrives.
	mid, _ := inner["message_id"].(string)
	if !sent[mid] {
		t.Fatalf("message_id %v, want one of the %d sent", inner["message_id"], len(sent))
	}
	// The migration sends {message_id, seq}; Realtime adds the
	// realtime.messages row id as `id`. All three are ids.
	for k := range inner {
		if k != "message_id" && k != "seq" && k != "id" {
			t.Errorf("the broadcast carries %q; want ids only", k)
		}
	}
	if strings.Contains(string(hint.Payload), "must not travel") || strings.Contains(string(hint.Payload), "nor this summary") {
		t.Fatalf("the broadcast carries message content")
	}
}
