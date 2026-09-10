package supabase

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// `message watch` against the REAL stack (P2-10, brief section 7): the
// private-channel join of the principal's own topic, a message_accepted
// broadcast turning into a `message` event, the stdin commands through
// the real RPCs, the revocation of a running watch (C-08) and the
// closed-session fall-back, none of which the fake Phoenix can prove.
// Every test self-skips without the BRIGADE_TEST_LIVE opt-in or without
// the stack (liveRig, through testutil.RequireSupabase) and mints its own
// principals and team.

// TestIntegrationWatchLiveDelivery: ready with mode push, status live
// once the topic is joined, a message sent while watching arrives within
// the push deadline, ack and heartbeat over stdin reach the RPCs, and
// close closes the session and exits 0 (C-33, C-35, C-41).
func TestIntegrationWatchLiveDelivery(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-10-live"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	w := startWatch(t, b, "message", "watch", "--session", sb)
	event := w.next(5 * time.Second)
	if event["event"] != protocol.EventReady || event["session_id"] != sb || event["mode"] != protocol.WatchModePush {
		t.Fatalf("first event = %v, want a push ready for %s", event, sb)
	}
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	sent := time.Now()
	res := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"while watching"}`)
	id, _ := res["message_id"].(string)
	event = w.expect(protocol.EventMessage, 5*time.Second)
	if messageID(t, event) != id {
		t.Fatalf("message event %v, want %s", event, id)
	}
	t.Logf("message delivered %s after the send returned", time.Since(sent).Round(time.Millisecond))
	message, _ := event["message"].(map[string]any)
	if message["delivery_state"] != "accepted" || message["recipient_session_id"] != sb {
		t.Errorf("envelope = %v", message)
	}

	w.send(`{"type":"ack","message_ids":["` + id + `","not-a-uuid"]}`)
	acked := w.expect(protocol.EventAcked, 5*time.Second)
	ids, _ := acked["message_ids"].([]any)
	unknown, _ := acked["unknown"].([]any)
	if len(ids) != 1 || ids[0] != id || len(unknown) != 1 || unknown[0] != "not-a-uuid" {
		t.Errorf("acked event = %v", acked)
	}
	if got := receiveLive(t, b, sb); len(got) != 0 {
		t.Errorf("the acknowledged message is still in the inbox: %v", got)
	}

	w.send(`{"type":"heartbeat","activity":"idle","lease_seconds":120,"model":"claude-sonnet-5","context_used_tokens":2048}`)
	hb := w.expect(protocol.EventHeartbeatOK, 5*time.Second)
	if hb["session_id"] != sb || hb["state"] != protocol.SessionStateIdle {
		t.Errorf("heartbeat_ok = %v", hb)
	}
	lease := mustTime(t, hb, "lease_until")
	server := mustTime(t, hb, "server_time")
	if d := lease.Sub(server); d < 115*time.Second || d > 125*time.Second {
		t.Errorf("lease_until - server_time = %s, want about 120 s", d)
	}

	w.send(`{"type":"close"}`)
	if code := w.wait(5 * time.Second); code != 0 {
		t.Fatalf("exit %d after close, want 0", code)
	}
	listed := b.ok("session", "list", "--include-offline")
	sessions, _ := listed["sessions"].([]any)
	for _, item := range sessions {
		s, _ := item.(map[string]any)
		if s["session_id"] == sb && s["state"] != protocol.SessionStateOffline {
			t.Errorf("the close command did not close the session: %v", s)
		}
		// The stdin heartbeat's model and context_used_tokens reached the
		// row (C-44), and closing kept them.
		if s["session_id"] == sb && (s["model"] != "claude-sonnet-5" || s["context_used_tokens"] != float64(2048)) {
			t.Errorf("the stdin heartbeat's model/context_used_tokens did not reach the session: %v", s)
		}
	}
}

// TestIntegrationWatchRevocation covers C-08's watch arm: `team leave`
// while the watch runs ends it with ONE unauthorized error event,
// retryable false and the fixed message, exit 5, within 2 s.
func TestIntegrationWatchRevocation(t *testing.T) {
	_, secret := liveTeam(t, liveName(t, "p2-10-revoke"))
	b := liveJoin(t, secret)
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	w := startWatch(t, b, "message", "watch", "--session", sb)
	w.expect(protocol.EventReady, 5*time.Second)
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	left := b.exec("", "team", "leave")
	if left.code != 0 {
		t.Fatalf("team leave: exit %d, %s", left.code, left.stdout)
	}
	revoked := time.Now()
	event := w.expect(protocol.EventError, 2*time.Second)
	object, _ := event["error"].(map[string]any)
	if object["code"] != string(protocol.CodeUnauthorized) || object["retryable"] != false || object["message"] != errNotMember().Message {
		t.Fatalf("event = %v, want the uniform unauthorized with retryable false", event)
	}
	if code := w.wait(2 * time.Second); code != protocol.CodeUnauthorized.Exit() {
		t.Fatalf("exit %d, want 5", code)
	}
	t.Logf("the watch ended %s after the leave", time.Since(revoked).Round(time.Millisecond))
}

// TestIntegrationWatchForeignAndClosed: a session another principal
// owns is the uniform not_found, byte-identical to an id that is not
// uuid-shaped (C-37); an OWNED session closed elsewhere is still watched
// — the private topic refuses the join (owns_session_topic wants it
// open), so the watch reports polling and keeps draining, and a message
// sent to the closed session (accepted, C-31) still arrives.
func TestIntegrationWatchForeignAndClosed(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-10-foreign"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	foreign := b.exec("", "message", "watch", "--session", sa)
	unknown := b.exec("", "message", "watch", "--session", "deadbeef")
	if foreign.code != protocol.CodeNotFound.Exit() || foreign.stdout != unknown.stdout {
		t.Fatalf("foreign watch: exit %d\n%s%s", foreign.code, foreign.stdout, unknown.stdout)
	}
	if strings.Contains(foreign.stdout, `"ready"`) {
		t.Fatalf("a ready event preceded the refusal: %s", foreign.stdout)
	}

	b.ok("session", "close", "--session", sb)
	w := startWatch(t, b, "message", "watch", "--session", sb)
	w.expect(protocol.EventReady, 5*time.Second)
	// The refusal arrives after the server's fixed 5 s backoff (E0-2).
	w.expectStatusWithin(protocol.StatusStatePolling, linkReasonUnauthorized, 20*time.Second)
	res := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"to a closed session"}`)
	id, _ := res["message_id"].(string)
	// No channel, no hint: the message arrives on the polling drain timer
	// (plan 5.6: 10 s while polling), the latency the status announced.
	if got := messageID(t, w.expect(protocol.EventMessage, watchTiming.drainPolling+5*time.Second)); got != id {
		t.Fatalf("message %s, want %s", got, id)
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// expectStatusWithin waits up to d for a status with state and detail.
func (w *watchRun) expectStatusWithin(state, detail string, d time.Duration) {
	w.t.Helper()
	event := w.expect(protocol.EventStatus, d)
	if event["state"] != state || event["detail"] != detail {
		w.t.Fatalf("status = %v, want %s/%s", event, state, detail)
	}
}

// TestIntegrationRevokedChannelStopsAtTokenPush is I-16's lag half (P5-2,
// plan row "a revoked member's open realtime channel stops at the next
// token push or JWT expiry (lag recorded)"), both arms against the real
// stack, each self-contained.
//
// Arm A, the hint path — what the shipped watcher actually does. A
// revokes B with `team revoke-member --principal <B>`; revoke_membership
// writes the same membership_revoked broadcast leave_team writes, B's
// running watch treats it as a drain hint, the drain's fetch_inbox raises
// the uniform unauthorized and the watch exits 5 — within the 2 s bound
// C-08 holds `team leave` to (TestIntegrationWatchRevocation), with no
// adapter change. The interval is logged.
//
// Arm B, the token-push path — the residual for a channel that never
// reacts to the hint, on the raw-socket rig (no drain loop). B joins its
// own topic privately; a real send proves the channel delivers; A revokes
// B and the hint frame arrives (its latency logged, for free); a probe
// broadcast written as postgres (realtime.send on B's topic) STILL
// arrives, because Realtime authorizes a private topic at join and on a
// token push, not per broadcast — that positive control is what makes the
// death below non-vacuous; then a fresh JWT is pushed as `access_token`
// (the realtime.go:516 frame, what the watcher does after every refresh),
// the server re-runs the topic policy, and the channel is closed: a
// `system` error or a phx_close/phx_error on the topic, measured from the
// push. A second probe after the close does not arrive.
//
// The third clock — no hint AND no push, a client that never refreshes —
// is bounded by the JWT's remaining lifetime, jwt_expiry = 3600 s on this
// stack (supabase/config.toml), and is reasoned, not exercised: a Brigade
// watcher pushes a token on every refresh, at least once every
// jwt_expiry − 90 s, so that row is unreachable in normal operation and
// belongs to a client that is not this adapter.
func TestIntegrationRevokedChannelStopsAtTokenPush(t *testing.T) {
	t.Run("hint", func(t *testing.T) {
		a, secret := liveTeam(t, liveName(t, "p5-2-lag-hint"))
		b := liveJoin(t, secret)
		_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
		bRef := b.readSession().principalRef()

		w := startWatch(t, b, "message", "watch", "--session", sb)
		w.expect(protocol.EventReady, 5*time.Second)
		w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

		revoked := a.ok("team", "revoke-member", "--principal", bRef)
		t0 := time.Now()
		if revoked["status"] != "revoked" || revoked["changed"] != true || revoked["sessions_closed"] != float64(1) || revoked["principal_ref"] != bRef {
			t.Fatalf("revoke-member = %v", revoked)
		}
		event := w.expect(protocol.EventError, 2*time.Second)
		object, _ := event["error"].(map[string]any)
		if object["code"] != string(protocol.CodeUnauthorized) || object["retryable"] != false || object["message"] != errNotMember().Message {
			t.Fatalf("event = %v, want the uniform unauthorized with retryable false", event)
		}
		if code := w.wait(2 * time.Second); code != protocol.CodeUnauthorized.Exit() {
			t.Fatalf("exit %d, want 5", code)
		}
		t.Logf("I-16 arm A (hint → drain → unauthorized → exit 5): the watch ended %s after revoke_membership returned", time.Since(t0).Round(time.Millisecond))
	})

	t.Run("token push", func(t *testing.T) {
		db := liveDB(t)
		a, secret := liveTeam(t, liveName(t, "p5-2-lag-push"))
		b := liveJoin(t, secret)
		_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
		_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
		bRef := b.readSession().principalRef()

		s := liveSocket(t, b)
		topic := sessionTopic(sb)
		if reply := s.join(topic, true, false); reply.Status != "ok" {
			t.Fatalf("B's own private topic was refused: %s", reply.Response.Reason)
		}
		// 2. Positive control: the channel delivers a database broadcast.
		sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"before the revoke"}`)
		mustAwaitFrame(t, s.frames, "the message_accepted broadcast", topic, 10*time.Second, broadcastOn(topic, "message_accepted"))

		// 3. A revokes B; this rig has no drain loop, so the hint is merely counted.
		revokedAt := time.Now()
		if got := a.ok("team", "revoke-member", "--principal", bRef); got["changed"] != true {
			t.Fatalf("revoke-member = %v", got)
		}
		mustAwaitFrame(t, s.frames, "the membership_revoked hint", topic, 10*time.Second, broadcastOn(topic, "membership_revoked"))
		t.Logf("I-16 arm B: the membership_revoked hint arrived %s after revoke_membership returned (not acted on by this rig)", time.Since(revokedAt).Round(time.Millisecond))

		// 4. Positive control that the channel is STILL authorized after the
		// revoke: the join's decision is cached until the next token push.
		probe := func(n string) {
			t.Helper()
			execSQL(t, db, `select realtime.send($1::jsonb, 'p5_2_probe', $2, true)`, `{"probe":"`+n+`"}`, "brigade:session:"+sb)
		}
		probe("before the push")
		mustAwaitFrame(t, s.frames, "a probe broadcast after the revoke", topic, 10*time.Second, broadcastOn(topic, "p5_2_probe"))

		// 5. Push a fresh JWT on the joined channel and time the server's answer.
		next, err := s.c.forceRefresh(t.Context())
		if err != nil {
			t.Fatalf("forceRefresh: %v", err)
		}
		if next == s.token {
			t.Fatalf("forceRefresh answered the same token; the push would prove nothing")
		}
		t0 := time.Now()
		if _, err := s.p.push(t.Context(), s.joinRef, topic, phxEventToken, map[string]string{"access_token": next}); err != nil {
			t.Fatalf("access_token push: %v", err)
		}
		f, got := awaitFrame(s.frames, 5*time.Second, func(f phxFrame) bool {
			if f.Topic != topic {
				return false
			}
			if f.Event == phxEventSystem {
				var sys phxSystem
				return json.Unmarshal(f.Payload, &sys) == nil && sys.Status == "error"
			}
			return f.Event == phxEventClose || f.Event == phxEventError
		})
		lag := time.Since(t0)
		if got != frameFound {
			t.Fatalf("the revoked member's channel survived the access_token push for %s (I-16: the push must re-run the topic policy)", lag)
		}
		reason := ""
		if f.Event == phxEventSystem {
			var sys phxSystem
			_ = json.Unmarshal(f.Payload, &sys)
			reason = classifyReason(sys.Message)
			if reason != linkReasonUnauthorized {
				t.Errorf("the system error after the push reads %q (%s), want the permissions refusal", sys.Message, reason)
			}
		}
		t.Logf("I-16 arm B (access_token push → policy re-run → channel closed): %s after the push (%s%s)", lag.Round(time.Millisecond), f.Event, strings.TrimPrefix(" "+reason, " "))
		if lag >= 5*time.Second {
			t.Errorf("lag %s, want under 5 s", lag)
		}

		// 6. Negative control: nothing reaches the closed channel.
		probe("after the push")
		if _, got := awaitFrame(s.frames, 3*time.Second, broadcastOn(topic, "p5_2_probe")); got == frameFound {
			t.Errorf("a probe broadcast reached a channel the server had just closed")
		}
	})
}
