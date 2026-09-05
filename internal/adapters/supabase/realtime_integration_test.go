package supabase

// The two Realtime guarantees of plan 5.6 that the adapter's own happy
// path never exercises, against the REAL server (I-13 local half, I-14):
//
//   * a PUBLIC join of a session topic succeeds — the local stack does
//     not enforce PrivateOnly, which is why the hosted half is P5-1 —
//     and receives NONE of the broadcasts the database emits on it,
//     because brigade_session_topic_read is a policy on realtime.messages
//     and a public channel never reads that table;
//   * a CLIENT broadcast on a private topic the principal owns is
//     dropped: the realtime migration grants no insert policy, so only
//     the database writes on a session topic.
//
// Both self-skip through liveRig (so both wait on the BRIGADE_TEST_LIVE
// opt-in, testutil.LiveTestVar). Neither goes through the watcher: the
// point is what the SERVER does with a frame the adapter would never
// send, so the frames are hand-built on the client's own dial.

import (
	"encoding/json/v2"
	"testing"
	"time"
)

// A rawSocket is one Realtime connection with a single reader goroutine
// behind it — liveFrames, in realtimewait_test.go, which the whole
// package now shares. The goroutine matters: coder/websocket CLOSES the
// connection when a Read's context is cancelled, so a helper that gave
// each wait its own timeout context would kill the socket at the end of
// the first wait — measured here, the second wait failed with "use of
// closed network connection" rather than timing out. One reader, one
// channel, and every wait is a select on time.After.
type rawSocket struct {
	t      *testing.T
	c      *command
	p      *phxClient
	token  string
	frames <-chan phxFrame
	// joinRef is the ref of the last join, which a later push on the
	// channel (an access_token push, P5-2's I-16 arm B) must carry.
	joinRef string
}

// liveSocket dials Realtime for the rig's principal and starts reading.
func liveSocket(t *testing.T, r *rig) *rawSocket {
	t.Helper()
	c := r.command("message", "watch")
	if err := c.authenticate(true); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	token, err := c.accessToken(t.Context())
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	conn, err := c.client.dialRealtime(t.Context(), r.env)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	s := &rawSocket{t: t, c: c, p: &phxClient{conn: conn, vsn: phxVersionV1, log: c.log}, token: token}
	s.frames = liveFrames(t, conn, s.p.vsn)
	return s
}

// join pushes a phx_join whose config this test controls (the adapter's
// own joinPayload is always private with self false) and returns the
// server's reply.
func (s *rawSocket) join(topic string, private, self bool) phxReply {
	s.t.Helper()
	ref := s.p.nextRef()
	payload := map[string]any{
		"config": map[string]any{
			"broadcast":        map[string]any{"ack": false, "self": self},
			"presence":         map[string]any{"key": "", "enabled": false},
			"postgres_changes": []any{},
			"private":          private,
		},
		"access_token": s.token,
	}
	if err := s.p.send(s.t.Context(), ref, ref, topic, phxEventJoin, payload); err != nil {
		s.t.Fatalf("phx_join: %v", err)
	}
	s.joinRef = ref
	f, got := awaitFrame(s.frames, watchTiming.joinTimeout,
		func(f phxFrame) bool { return f.Event == phxEventReply && f.Ref == ref })
	switch got {
	case frameSocketClosed:
		s.t.Fatalf("the socket closed before the join was answered")
	case frameTimedOut:
		s.t.Fatalf("no phx_reply to the join within %s", watchTiming.joinTimeout)
	case frameFound:
	}
	var reply phxReply
	if err := json.Unmarshal(f.Payload, &reply); err != nil {
		s.t.Fatalf("phx_reply %s: %v", f.Payload, err)
	}
	return reply
}

// broadcasts counts the broadcast frames on topic that arrive within d.
// The socket stays usable afterwards.
func (s *rawSocket) broadcasts(topic string, d time.Duration) int {
	s.t.Helper()
	deadline := time.After(d)
	seen := 0
	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				s.t.Fatalf("the socket closed while listening for broadcasts")
			}
			if hintFor(f, topic) {
				s.t.Logf("broadcast frame on %s: %s", topic, f.Payload)
				seen++
			}
		case <-deadline:
			return seen
		}
	}
}

// TestIntegrationRealtimePublicJoinReceivesNothing is I-13's local half:
// the public join of a session topic is NOT refused by this stack, and
// that is harmless — a public channel reads none of the broadcasts the
// database emits, so joining one buys an attacker nothing. Ten messages
// are sent while it listens and none of them arrives.
func TestIntegrationRealtimePublicJoinReceivesNothing(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-11-public"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	s := liveSocket(t, b)
	topic := sessionTopic(sb)
	reply := s.join(topic, false, true)
	if reply.Status != "ok" {
		// A stack with "Allow public access" off refuses it instead; that
		// is the hosted half of I-13 (P5-1) and is a stronger answer.
		t.Skipf("this stack refuses a public join outright (%q): the PrivateOnly half is P5-1", reply.Response.Reason)
	}
	t.Logf("the public join of %s was accepted; the assertion is that it receives nothing", topic)

	const sends = 10
	for i := 0; i < sends; i++ {
		sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"public join must not see this"}`)
	}
	if got := s.broadcasts(topic, 10*time.Second); got != 0 {
		t.Fatalf("a public join received %d of %d broadcasts on %s (I-13)", got, sends, topic)
	}
	// The messages did arrive — through the inbox, which is the only
	// delivery path (I-01).
	if n := len(receiveLive(t, b, sb)); n != sends {
		t.Fatalf("the inbox holds %d messages, want %d: the negative result above proves nothing", n, sends)
	}
}

// TestIntegrationRealtimeClientBroadcastIsDropped is I-14: on the
// principal's OWN private topic, joined with broadcast self true so that
// a permitted client broadcast would echo straight back, a client
// broadcast is dropped — realtime.messages has a select policy and no
// insert policy, so only the database writes on a session topic. The
// positive control is on the same socket: a real send DOES produce a
// broadcast, so the silence above is the policy and not a dead channel.
func TestIntegrationRealtimeClientBroadcastIsDropped(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-11-nosend"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	s := liveSocket(t, b)
	topic := sessionTopic(sb)
	if reply := s.join(topic, true, true); reply.Status != "ok" {
		t.Fatalf("the principal's own topic was refused: %s", reply.Response.Reason)
	}

	// A forged message_accepted from the client. If it were accepted the
	// join's self:true would deliver it straight back to this socket.
	if _, err := s.p.push(t.Context(), "", topic, phxEventBcast, map[string]any{
		"type": phxEventBcast, "event": "message_accepted",
		"payload": map[string]any{"message_id": randomUUID, "seq": 1},
	}); err != nil {
		t.Fatalf("client broadcast: %v", err)
	}
	if got := s.broadcasts(topic, 5*time.Second); got != 0 {
		t.Fatalf("a client broadcast on an owned private topic was delivered (%d frames): clients can write on the channel (I-14)", got)
	}

	// Control: the database's own broadcast still reaches this socket.
	sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"the database may broadcast"}`)
	if got := s.broadcasts(topic, 10*time.Second); got == 0 {
		t.Fatalf("no broadcast after a real send: the socket was not listening, so the drop above proves nothing")
	}
}

// broadcastOn reports whether f is a database broadcast on topic whose
// inner event is event — the one place in the package that needs the
// event's name, because I-16 arm B must tell a membership_revoked hint
// from a probe broadcast on the same topic.
func broadcastOn(topic, event string) func(phxFrame) bool {
	return func(f phxFrame) bool {
		if !hintFor(f, topic) {
			return false
		}
		var b phxBroadcast
		return json.Unmarshal(f.Payload, &b) == nil && b.Event == event
	}
}
