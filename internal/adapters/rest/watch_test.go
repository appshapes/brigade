//go:build !windows

package rest

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// receives returns the bodies of the receive requests a session's watch
// has made so far.
func receives(b *backend, session string) []map[string]any {
	var out []map[string]any
	for _, r := range b.seen(pathMessageReceive) {
		if r.Body["session_id"] == session {
			out = append(out, r.Body)
		}
	}
	return out
}

// eventually polls cond until it holds or hangCatcher passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangCatcher)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, hangCatcher)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWatchCatchUpThenLongPoll is the loop of the contract's section 4:
// `ready` first, then every message accepted before the watch (the
// catch-up, a receive without `after`), then long polls that carry the
// last cursor and wait_seconds, through which a message accepted while the
// watch runs arrives without any polling by the adapter.
func TestWatchCatchUpThenLongPoll(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	before := []string{
		p.bob.send(p.bobSes, p.aliceSes, "before the watch 1"),
		p.bob.send(p.bobSes, p.aliceSes, "before the watch 2"),
	}
	b.forget()

	tm := fastTiming()
	w := p.alice.watch(tm, "--session", p.aliceSes)
	event := w.expect(protocol.EventReady)
	if event["session_id"] != p.aliceSes || event["protocol_version"] != "1" || event["mode"] != protocol.WatchModePush {
		t.Errorf("ready: %v", event)
	}
	for _, want := range before {
		if got := w.message(); got != want {
			t.Errorf("catch-up emitted %s, want %s (oldest first)", got, want)
		}
	}
	w.quiet(150 * time.Millisecond)

	// The first request is the catch-up; the next is a long poll behind it.
	eventually(t, "the first long poll", func() bool { return len(receives(b, p.aliceSes)) >= 2 })
	seen := receives(b, p.aliceSes)
	if _, has := seen[0]["after"]; has || seen[0]["wait_seconds"] != nil || seen[0]["limit"] != float64(receiveMaxLimit) {
		t.Errorf("the catch-up request is %v, want no after, no wait_seconds and limit %d", seen[0], receiveMaxLimit)
	}
	cursor, _ := seen[1]["after"].(string)
	if cursor == "" || seen[1]["wait_seconds"] != float64(2) {
		t.Errorf("the long poll is %v, want the catch-up's cursor in after and wait_seconds 2", seen[1])
	}

	// A message accepted while the watch runs arrives through the request
	// that is already waiting: no new request is needed to see it.
	polls := len(receives(b, p.aliceSes))
	live := p.bob.send(p.bobSes, p.aliceSes, "while watching")
	if got := w.message(); got != live {
		t.Errorf("the watch emitted %s, want the live message %s", got, live)
	}
	if now := len(receives(b, p.aliceSes)); now > polls+1 {
		t.Errorf("the adapter made %d receive requests to see one message; a long poll needs none", now-polls)
	}
	// The poll after it carries a NEWER cursor, so the message is not sent
	// again; and it is not emitted again.
	eventually(t, "the next long poll", func() bool { return len(receives(b, p.aliceSes)) > polls })
	all := receives(b, p.aliceSes)
	if next, _ := all[len(all)-1]["after"].(string); next == "" || next == cursor {
		t.Errorf("the long poll after a delivery carries cursor %q, want a newer one than %q", next, cursor)
	}
	w.quiet(150 * time.Millisecond)

	_ = w.stdin.Close()
	if code := w.exit(); code != 0 {
		t.Errorf("the watch exited %d after stdin EOF, want 0", code)
	}
}

// TestWatchPeriodicDrain: every timing.drain the watch asks without
// `after` and emits every message that is still unacknowledged again,
// once; an acknowledged message is never emitted again (4.4.9, 4.5.3).
func TestWatchPeriodicDrain(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	kept := p.bob.send(p.bobSes, p.aliceSes, "never acknowledged")
	acked := p.bob.send(p.bobSes, p.aliceSes, "acknowledged over stdin")
	b.forget()

	tm := fastTiming()
	tm.wait, tm.drain = time.Second, 1500*time.Millisecond
	w := p.alice.watch(tm, "--session", p.aliceSes)
	w.ready()
	if a, c := w.message(), w.message(); a != kept || c != acked {
		t.Fatalf("catch-up emitted %s, %s", a, c)
	}
	w.command(`{"type":"ack","message_ids":["` + acked + `"]}`)
	w.expect(protocol.EventAcked)

	// Between two drains nothing is emitted twice.
	w.quiet(500 * time.Millisecond)
	// The drain: the unacknowledged message again, exactly once, and the
	// acknowledged one not at all.
	for round := 1; round <= 2; round++ {
		if got := w.message(); got != kept {
			t.Fatalf("drain %d emitted %s, want the unacknowledged %s", round, got, kept)
		}
		w.quiet(500 * time.Millisecond)
	}
	drains := 0
	for _, body := range receives(b, p.aliceSes)[1:] {
		if _, has := body["after"]; !has {
			drains++
			if body["wait_seconds"] != nil {
				t.Errorf("a periodic drain asked the server to wait: %v", body)
			}
		}
	}
	if drains < 2 {
		t.Errorf("the watch made %d periodic drains (receives without after), want at least 2", drains)
	}

	// A message that arrives between drains is emitted once by the long
	// poll and again only by the next drain.
	live := p.bob.send(p.bobSes, p.aliceSes, "between drains")
	if got := w.message(); got != live {
		t.Fatalf("the watch emitted %s, want %s", got, live)
	}
	next := []string{w.message(), w.message()}
	if next[0] != kept || next[1] != live {
		t.Errorf("the next drain emitted %v, want [%s %s]", next, kept, live)
	}
}

// TestWatchStdinCommands: ack, heartbeat and close are answered while a
// long poll waits; an unknown type, a malformed line and an over-long line
// are ignored; a heartbeat the protocol refuses is an `error` event with
// retryable true and the watch goes on (4.4.9, C-41).
func TestWatchStdinCommands(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	w := p.alice.watch(fastTiming(), "--session", p.aliceSes)
	w.ready()
	id := p.bob.send(p.bobSes, p.aliceSes, "ack me")
	if got := w.message(); got != id {
		t.Fatalf("the watch emitted %s, want %s", got, id)
	}

	w.command(`{"type":"frobnicate","x":1}`)
	w.command(`not json`)
	w.command(`{"type":"ack","message_ids":["` + strings.Repeat("x", protocol.MaxLineBytes) + `"]}`)
	w.command(`{"type":"ack","message_ids":["` + id + `","no-such-message"]}`)
	event := w.expect(protocol.EventAcked)
	ids, _ := event["message_ids"].([]any)
	unknown, _ := event["unknown"].([]any)
	if len(ids) != 1 || ids[0] != id || len(unknown) != 1 || unknown[0] != "no-such-message" {
		t.Errorf("acked event %v, want message_ids [%s] and unknown [no-such-message]", event, id)
	}
	if got, _ := p.alice.ok("", "message", "receive", "--session", p.aliceSes)["messages"].([]any); len(got) != 0 {
		t.Errorf("message receive after the stdin ack: %d messages, want none (4.5.3)", len(got))
	}

	b.forget()
	w.command(`{"type":"heartbeat","activity":"idle","session_name":"renamed","lease_seconds":120}`)
	event = w.expect(protocol.EventHeartbeatOK)
	if event["session_id"] != p.aliceSes || event["state"] != protocol.SessionStateIdle {
		t.Errorf("heartbeat_ok %v, want this session, idle", event)
	}
	if seen := b.seen(pathSessionHeartbeat); len(seen) != 1 || seen[0].Body["session_id"] != p.aliceSes {
		t.Errorf("the heartbeat command made %d requests", len(seen))
	} else if heartbeat, _ := seen[0].Body["heartbeat"].(map[string]any); len(heartbeat) != 3 || heartbeat["session_name"] != "renamed" {
		t.Errorf("the heartbeat request carries %v, want exactly the three members given", heartbeat)
	}

	// Refused by the protocol's own rules, before any request.
	b.forget()
	for _, bad := range []string{
		`{"type":"heartbeat","lease_seconds":5}`,
		`{"type":"heartbeat","lease_seconds":601}`,
	} {
		w.command(bad)
		event = w.expect(protocol.EventError)
		object, _ := event["error"].(map[string]any)
		if object["code"] != "invalid_input" || object["retryable"] != true {
			t.Errorf("%s: error event %v, want invalid_input with retryable true", bad, event)
		}
	}
	if n := len(b.seen(pathSessionHeartbeat)); n != 0 {
		t.Errorf("a refused heartbeat made %d requests, want none", n)
	}
	// Refused by the server about the command: the watch goes on.
	b.answerWith(only(pathMessageAck, http.StatusBadGateway, "gateway"))
	w.command(`{"type":"ack","message_ids":["m"]}`)
	object, _ := w.expect(protocol.EventError)["error"].(map[string]any)
	if object["code"] != "unavailable" || object["retryable"] != true {
		t.Errorf("an ack the server could not take: %v, want unavailable with retryable true", object)
	}
	b.answerWith(nil)

	w.command(`{"type":"close"}`)
	if code := w.exit(); code != 0 {
		t.Fatalf("the watch exited %d after close, want 0", code)
	}
	sessions, _ := p.alice.ok("", "session", "list", "--include-offline")["sessions"].([]any)
	closed := false
	for _, s := range sessions {
		record, _ := s.(map[string]any)
		if record["session_id"] == p.aliceSes {
			closed = record["state"] == protocol.SessionStateOffline && record["session_name"] == "renamed"
		}
	}
	if !closed {
		t.Errorf("after the close command the session is not offline and renamed in the list")
	}
}

// TestWatchExitRules: stdin EOF and the stop SIGTERM produces each end the
// watch with exit 0 while a long poll is waiting, and the request in
// flight is abandoned rather than waited for (4.4.9, C-38).
func TestWatchExitRules(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	tm := fastTiming()
	tm.wait = 25 * time.Second // production's: the exit must not wait for it

	for _, how := range []string{"stdin EOF", "the stop signal"} {
		w := p.alice.watch(tm, "--session", p.aliceSes)
		w.ready()
		eventually(t, "the long poll", func() bool { return len(receives(b, p.aliceSes)) >= 2 })
		started := time.Now()
		if how == "stdin EOF" {
			_ = w.stdin.Close()
		} else {
			w.stop()
		}
		if code := w.exit(); code != 0 {
			t.Errorf("%s: the watch exited %d, want 0", how, code)
		}
		if took := time.Since(started); took > 5*time.Second {
			t.Errorf("%s: the watch took %s to exit, want well under the 5 s of 4.4.9", how, took)
		}
		select {
		case line, open := <-w.lines:
			if open {
				t.Errorf("%s: an event after ready: %q", how, line)
			}
		case <-time.After(hangCatcher):
			t.Errorf("%s: stdout was not closed", how)
		}
		b.forget()
	}
}

// TestWatchRefusals: a refusal before the catch-up is ONE `error` event
// with retryable false, no `ready`, and the code's exit status; a foreign
// session and one that never existed are refused with the same bytes
// (4.5.7, C-37).
func TestWatchRefusals(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	refusal := func(r *rig, args ...string) (string, int) {
		t.Helper()
		w := r.watch(fastTiming(), args...)
		select {
		case line := <-w.lines:
			code := w.exit()
			if _, open := <-w.lines; open {
				t.Fatalf("watch %v: a second line after the refusal", args)
			}
			return string(line), code
		case <-time.After(hangCatcher):
			t.Fatalf("watch %v: no event", args)
			return "", 0
		}
	}
	foreign, code := refusal(p.alice, "--session", p.bobSes)
	if code != protocol.CodeNotFound.Exit() || !strings.Contains(foreign, `"code":"not_found"`) || !strings.Contains(foreign, `"retryable":false`) {
		t.Errorf("a watch on another principal's session: exit %d, %s", code, foreign)
	}
	unknown, code := refusal(p.alice, "--session", "0123456789abcdef0123456789abcdef")
	if code != protocol.CodeNotFound.Exit() || unknown != foreign {
		t.Errorf("a watch on a session that never existed: exit %d, %s; want the foreign session's bytes %s", code, unknown, foreign)
	}
	usage, code := refusal(p.alice)
	if code != protocol.CodeUsage.Exit() || !strings.Contains(usage, `"code":"usage"`) || usage == foreign {
		t.Errorf("a watch with no --session: exit %d, %s", code, usage)
	}
	unbound, code := refusal(newRig(t, b.url()), "--session", p.aliceSes)
	if code != protocol.CodeConfig.Exit() || !strings.Contains(unbound, `"code":"config"`) {
		t.Errorf("a watch on an unconfigured profile: exit %d, %s", code, unbound)
	}
}

// TestWatchEndsWhenTheMemberLeaves: the uniform `unauthorized` reaches a
// running watch through the long poll that was waiting when the member
// left, and ends it with exit 5 (4.5.7, C-08).
func TestWatchEndsWhenTheMemberLeaves(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	tm := fastTiming()
	tm.wait = 25 * time.Second
	w := p.bob.watch(tm, "--session", p.bobSes)
	w.ready()
	eventually(t, "the long poll", func() bool { return len(receives(b, p.bobSes)) >= 2 })
	started := time.Now()
	p.bob.ok("", "team", "leave")
	object, _ := w.expect(protocol.EventError)["error"].(map[string]any)
	if object["code"] != "unauthorized" || object["retryable"] != false {
		t.Errorf("the watch of a member who left: %v, want unauthorized with retryable false", object)
	}
	if code := w.exit(); code != protocol.CodeUnauthorized.Exit() {
		t.Errorf("the watch exited %d, want %d", code, protocol.CodeUnauthorized.Exit())
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("the watch took %s to end after the leave; C-08 gives it 5 s", took)
	}
}

// TestWatchRidesOutAnOutage: a retryable failure of a receive is reported
// once, with retryable true, the watch keeps asking, and a message
// accepted meanwhile is emitted when the server answers again. A failure
// that is not retryable ends the watch with its code.
func TestWatchRidesOutAnOutage(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	w := p.alice.watch(fastTiming(), "--session", p.aliceSes)
	w.ready()
	eventually(t, "the long poll", func() bool { return len(receives(b, p.aliceSes)) >= 2 })

	var failing atomic.Bool
	var refused atomic.Int32
	failing.Store(true)
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path != pathMessageReceive || r.Body["session_id"] != p.aliceSes || !failing.Load() {
			return nil
		}
		refused.Add(1)
		return &canned{status: http.StatusBadGateway, body: "gateway"}
	})
	// The poll in flight is answered by the next message; the one after it
	// meets the outage.
	first := p.bob.send(p.bobSes, p.aliceSes, "before the outage")
	if got := w.message(); got != first {
		t.Fatalf("the watch emitted %s, want %s", got, first)
	}
	object, _ := w.expect(protocol.EventError)["error"].(map[string]any)
	if object["code"] != "unavailable" || object["retryable"] != true {
		t.Fatalf("the first failed receive: %v, want unavailable with retryable true", object)
	}
	during := p.bob.send(p.bobSes, p.aliceSes, "during the outage")
	eventually(t, "more failed receives", func() bool { return refused.Load() >= 3 })
	w.quiet(100 * time.Millisecond) // reported once, not once per failure
	failing.Store(false)
	if got := w.message(); got != during {
		t.Errorf("after the outage the watch emitted %s, want %s", got, during)
	}

	// A failure that is not retryable: the server's own code ends the watch.
	b.answerWith(only(pathMessageReceive, http.StatusUnauthorized,
		`{"ok":false,"protocol_version":"1","error":{"code":"unauthenticated","message":"unknown credential","retryable":false}}`))
	p.bob.send(p.bobSes, p.aliceSes, "wake the poll")
	for {
		event := w.next()
		if event["event"] == protocol.EventMessage {
			continue
		}
		object, _ = event["error"].(map[string]any)
		if event["event"] != protocol.EventError || object["code"] != "unauthenticated" || object["retryable"] != false {
			t.Errorf("a receive the server refused for good: %v", event)
		}
		break
	}
	if code := w.exit(); code != protocol.CodeUnauthenticated.Exit() {
		t.Errorf("the watch exited %d, want %d", code, protocol.CodeUnauthenticated.Exit())
	}
}

// TestWatchSurvivesAServerThatIgnoresTheCursor: a server that does not
// recognise a cursor answers the whole unacknowledged set (the contract's
// section 4). Between two drains the watch emits each id once all the
// same.
func TestWatchSurvivesAServerThatIgnoresTheCursor(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	first := p.bob.send(p.bobSes, p.aliceSes, "one")
	// Strip `after` from every receive, so the stub treats each as a drain.
	var stripped atomic.Int32
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path == pathMessageReceive && r.Body["after"] != nil {
			stripped.Add(1)
			return &canned{status: http.StatusOK, body: okEnvelope(
				`{"messages":[` + envelopeFor(first, p.aliceSes) + `],"cursor":"same-every-time"}`)}
		}
		return nil
	})
	w := p.alice.watch(fastTiming(), "--session", p.aliceSes)
	w.ready()
	if got := w.message(); got != first {
		t.Fatalf("catch-up emitted %s, want %s", got, first)
	}
	eventually(t, "several answers that repeat the message", func() bool { return stripped.Load() >= 5 })
	w.quiet(100 * time.Millisecond)
}

// envelopeFor is a valid 4.4.5 message with the given id for a session.
func envelopeFor(id, recipient string) string {
	return `{"protocol_version":"1","kind":"text","message_id":"` + id + `","team_ref":"t",` +
		`"sender":{"principal_ref":"p","session_id":"s","session_name":"n"},"recipient_session_id":"` + recipient + `",` +
		`"body":"one","hop_count":0,"created_at":"2026-11-12T10:00:00Z","delivery_state":"accepted"}`
}

// TestWatchNeverEmitsAnAcknowledgedMessageAgain: a periodic drain that was
// answered before the server took the acknowledgement still names the
// message; the watch that acknowledged it does not emit it again (4.5.3).
func TestWatchNeverEmitsAnAcknowledgedMessageAgain(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	id := p.bob.send(p.bobSes, p.aliceSes, "acknowledged once")

	tm := fastTiming()
	tm.wait, tm.drain = time.Second, 500*time.Millisecond
	w := p.alice.watch(tm, "--session", p.aliceSes)
	w.ready()
	if got := w.message(); got != id {
		t.Fatalf("catch-up emitted %s, want %s", got, id)
	}
	// From here on every drain is answered as if it had been built before
	// the acknowledgement: it still holds the message.
	var stale atomic.Int32
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path != pathMessageReceive || r.Body["session_id"] != p.aliceSes || r.Body["after"] != nil {
			return nil
		}
		stale.Add(1)
		return &canned{status: http.StatusOK, body: okEnvelope(`{"messages":[` + envelopeFor(id, p.aliceSes) + `],"cursor":"stale"}`)}
	})
	w.command(`{"type":"ack","message_ids":["` + id + `"]}`)
	w.expect(protocol.EventAcked)
	eventually(t, "two stale drains", func() bool { return stale.Load() >= 2 })
	w.quiet(200 * time.Millisecond)
}
