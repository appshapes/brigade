package supabase

import (
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
// Every test self-skips without the stack (liveRig) and mints its own
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

	w.send(`{"type":"heartbeat","activity":"idle","lease_seconds":120}`)
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
