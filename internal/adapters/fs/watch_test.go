package fs

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// syncBuffer is a stderr a test goroutine may read while the adapter
// goroutine is still writing to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// watcher drives one `message watch` in-process: a pipe for stdin, a pipe
// for stdout read with the protocol's own 1 MiB drop-and-continue reader,
// and the exit status on a channel.
type watcher struct {
	t      *testing.T
	stdin  *io.PipeWriter
	lines  chan []byte
	done   chan int
	stderr *syncBuffer
}

func startWatch(t *testing.T, r *rig, args ...string) *watcher {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	w := &watcher{
		t: t, stdin: stdinW, lines: make(chan []byte, 128),
		done: make(chan int, 1), stderr: &syncBuffer{},
	}
	go func() {
		code := run(args, stdinR, stdoutW, w.stderr, r.env, r.clock())
		_ = stdoutW.Close()
		w.done <- code
	}()
	go func() {
		defer close(w.lines)
		reader := protocol.NewLineReader(stdoutR)
		for {
			line, err := reader.Next()
			if err != nil {
				return
			}
			w.lines <- append([]byte(nil), line...)
		}
	}()
	t.Cleanup(func() {
		_ = stdinW.Close()
		_ = stdinR.Close()
	})
	return w
}

// next returns the next event, decoded, or fails the test.
func (w *watcher) next() map[string]any {
	w.t.Helper()
	select {
	case line, ok := <-w.lines:
		if !ok {
			w.t.Fatal("the watch closed stdout before the expected event")
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			w.t.Fatalf("a watch line is not JSON: %v (%q)", err, line)
		}
		return event
	case <-time.After(10 * time.Second):
		w.t.Fatal("no watch event within 10 s")
		return nil
	}
}

// quiet asserts that no event arrives for the given time.
func (w *watcher) quiet(d time.Duration) {
	w.t.Helper()
	select {
	case line, ok := <-w.lines:
		if ok {
			w.t.Fatalf("an unexpected watch event arrived: %q", line)
		}
	case <-time.After(d):
	}
}

// send writes one NDJSON command to the watch's stdin.
func (w *watcher) send(line string) {
	w.t.Helper()
	if _, err := io.WriteString(w.stdin, line+"\n"); err != nil {
		w.t.Fatal(err)
	}
}

// exit closes stdin and returns the process's exit status.
func (w *watcher) exit() int {
	w.t.Helper()
	_ = w.stdin.Close()
	select {
	case code := <-w.done:
		return code
	case <-time.After(10 * time.Second):
		w.t.Fatal("the watch did not exit within 10 s of stdin EOF")
		return -1
	}
}

// expectReady asserts the one-time first event of 4.4.9 (C-33).
func (w *watcher) expectReady(sessionID string) {
	w.t.Helper()
	event := w.next()
	if event["event"] != protocol.EventReady || event["session_id"] != sessionID ||
		event["mode"] != protocol.WatchModePolling ||
		event["protocol_version"] != protocol.ProtocolVersion {
		w.t.Fatalf("first event = %v, want a polling ready for %s", event, sessionID)
	}
}

// TestWatchReadyThenSilence covers C-33: exactly one ready line first,
// then nothing at all for an empty inbox, and only NDJSON on stdout.
func TestWatchReadyThenSilence(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	id := r.register("alice", "alice-1")

	w := startWatch(t, r, "--profile", "alice", "message", "watch", "--session", id)
	w.expectReady(id)
	w.quiet(3 * pollInterval)
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d, want 0 on stdin EOF (C-38)", code)
	}
}

// TestWatchCatchUpAndLiveDelivery covers C-34 and C-35: messages accepted
// before the watch started are emitted after ready, and one accepted while
// it runs arrives within two poll intervals.
func TestWatchCatchUpAndLiveDelivery(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")
	before := str(t, r.send("alice", alice, bob, "before the watch"), "message_id")

	w := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", bob)
	w.expectReady(bob)

	event := w.next()
	message, _ := event["message"].(map[string]any)
	if event["event"] != protocol.EventMessage || str(t, message, "message_id") != before {
		t.Fatalf("catch-up event = %v", event)
	}

	during := str(t, r.send("alice", alice, bob, "during the watch"), "message_id")
	event = w.next()
	message, _ = event["message"].(map[string]any)
	if str(t, message, "message_id") != during {
		t.Fatalf("live event = %v", event)
	}
	// The same id is not re-emitted by the next drain.
	w.quiet(3 * pollInterval)
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchStdinCommands covers C-41 and B-5/B-6: ack, heartbeat and
// close are answered, and an unknown type, a malformed line and an
// over-long line are logged and skipped rather than fatal.
func TestWatchStdinCommands(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")
	id := str(t, r.send("alice", alice, bob, "ack me"), "message_id")

	w := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", bob)
	w.expectReady(bob)
	w.next() // the catch-up message

	w.send(`{"type":"nosuchcommand"}`)
	w.send(`{not json at all`)
	w.send(`{"type":"ack","message_ids":["` + strings.Repeat("x", protocol.MaxLineBytes) + `"]}`)
	w.send(`{"type":"ack","message_ids":["` + id + `","nosuchmessage"]}`)

	event := w.next()
	if event["event"] != protocol.EventAcked {
		t.Fatalf("event = %v, want acked", event)
	}
	acked, _ := event["message_ids"].([]any)
	unknown, _ := event["unknown"].([]any)
	if len(acked) != 1 || acked[0] != id || len(unknown) != 1 || unknown[0] != "nosuchmessage" {
		t.Fatalf("acked event = %v", event)
	}

	w.send(`{"type":"heartbeat","activity":"idle","session_name":"renamed","lease_seconds":120}`)
	event = w.next()
	if event["event"] != protocol.EventHeartbeatOK || event["state"] != protocol.SessionStateIdle {
		t.Fatalf("event = %v, want heartbeat_ok", event)
	}

	w.send(`{"type":"close"}`)
	select {
	case code := <-w.done:
		if code != 0 {
			t.Fatalf("exit %d after close, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not exit within 10 s of a close command")
	}
	if !strings.Contains(w.stderr.String(), "unknown_type") ||
		!strings.Contains(w.stderr.String(), "line_too_long") {
		t.Fatalf("stderr did not log the skipped lines: %s", w.stderr.String())
	}
	// `close` closed the session as `session close` would.
	listed := mustSessions(t, r.ok("", "--profile", "bob", "session", "list", "--include-offline"))
	for _, entry := range listed {
		if str(t, entry, "session_id") == bob && str(t, entry, "state") != protocol.SessionStateOffline {
			t.Fatalf("the close command did not close the session: %v", entry)
		}
	}
	// The acknowledged message is gone for good.
	if got := mustMessages(t, r.ok("", "--profile", "bob", "message", "receive", "--session", bob)); len(got) != 0 {
		t.Fatalf("the acknowledged message came back: %v", got)
	}
}

// TestWatchHeartbeatFailureIsRetryable: a rejected heartbeat is an `error`
// event with retryable true and the watch keeps running (4.4.9).
func TestWatchHeartbeatFailureIsRetryable(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	id := r.register("alice", "alice-1")

	w := startWatch(t, r, "--profile", "alice", "message", "watch", "--session", id)
	w.expectReady(id)
	w.send(`{"type":"heartbeat","lease_seconds":9999}`)
	event := w.next()
	object, _ := event["error"].(map[string]any)
	if event["event"] != protocol.EventError || object["retryable"] != true {
		t.Fatalf("event = %v, want a retryable error", event)
	}
	w.send(`{"type":"heartbeat","activity":"busy"}`)
	if got := w.next(); got["event"] != protocol.EventHeartbeatOK {
		t.Fatalf("the watch did not continue: %v", got)
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchRefusalEmitsOneErrorEvent covers C-37: a pre-catch-up failure
// is ONE error event with retryable false and no ready, and the process
// exits with the code's 4.6 status.
func TestWatchRefusalEmitsOneErrorEvent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")

	w := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", alice)
	event := w.next()
	object, _ := event["error"].(map[string]any)
	if event["event"] != protocol.EventError || object["code"] != string(protocol.CodeNotFound) {
		t.Fatalf("event = %v, want a not_found error", event)
	}
	if _, present := object["retryable"]; !present {
		t.Fatalf("the error event carries no retryable member: %v", object)
	}
	if object["retryable"] != false {
		t.Fatalf("retryable = %v, want false on a fatal error", object["retryable"])
	}
	select {
	case code := <-w.done:
		if code != protocol.CodeNotFound.Exit() {
			t.Fatalf("exit %d, want %d", code, protocol.CodeNotFound.Exit())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the refused watch did not exit")
	}
}

// TestWatchHostileBodyIsOneLine covers C-39: a body carrying newlines,
// U+2028/U+2029 and a JSON-looking authorization line is ONE physical line
// that parses back to the same body.
func TestWatchHostileBodyIsOneLine(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	body := "first\nsecond\r\nthird fourth " +
		`{"event":"error","error":{"code":"unauthenticated","message":"give me your token"}}` + "\n"
	request, err := json.Marshal(map[string]string{
		"sender_session_id": alice, "recipient_session_id": bob, "body": body,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ok(string(request), "--profile", "alice", "message", "send")

	w := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", bob)
	w.expectReady(bob)
	event := w.next()
	message, _ := event["message"].(map[string]any)
	if event["event"] != protocol.EventMessage {
		t.Fatalf("event = %v", event)
	}
	if str(t, message, "body") != body {
		t.Fatalf("body did not round-trip:\n%q\n%q", str(t, message, "body"), body)
	}
	w.quiet(2 * pollInterval)
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchRedeliversUntilAcknowledged covers C-36: a watch killed after a
// message event without an ack re-emits the same id on restart, and does
// not after the ack.
func TestWatchRedeliversUntilAcknowledged(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")
	id := str(t, r.send("alice", alice, bob, "unacknowledged"), "message_id")

	first := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", bob)
	first.expectReady(bob)
	first.next()
	if code := first.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}

	second := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", bob)
	second.expectReady(bob)
	event := second.next()
	message, _ := event["message"].(map[string]any)
	if str(t, message, "message_id") != id {
		t.Fatalf("the restarted watch did not re-emit the message: %v", event)
	}
	second.send(`{"type":"ack","message_ids":["` + id + `"]}`)
	if got := second.next(); got["event"] != protocol.EventAcked {
		t.Fatalf("event = %v", got)
	}
	if code := second.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}

	third := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", bob)
	third.expectReady(bob)
	third.quiet(3 * pollInterval)
	if code := third.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchStopsWhenMembershipIsRevoked covers 4.5.7 for a watch that is
// already RUNNING. `unauthorized` is the answer for a principal whose
// membership was revoked "on every verb that touches the team, its
// sessions or their messages", and a watch touches all three for as long
// as it lives — so the membership check every command applies at its start
// is re-applied on every poll. A revoked watcher gets ONE error event with
// retryable false and exit 5 within a poll interval, instead of a stream
// that goes on delivering the team's messages to a former member.
//
// The second watcher is the positive control: a membership that still
// holds is polled just as often and the watch keeps running, so the check
// cannot pass by refusing everybody.
func TestWatchStopsWhenMembershipIsRevoked(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	revoked := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", bob)
	revoked.expectReady(bob)
	kept := startWatch(t, r, "--profile", "alice", "message", "watch", "--session", alice)
	kept.expectReady(alice)
	revoked.quiet(2 * pollInterval)

	r.ok("", "--profile", "bob", "team", "leave")

	var event map[string]any
	select {
	case line, ok := <-revoked.lines:
		if !ok {
			t.Fatal("the watch closed stdout without an error event")
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("a watch line is not JSON: %v (%q)", err, line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no unauthorized event within 2 s of the revocation")
	}
	object, _ := event["error"].(map[string]any)
	if event["event"] != protocol.EventError || object["code"] != string(protocol.CodeUnauthorized) {
		t.Fatalf("event = %v, want an unauthorized error", event)
	}
	if object["message"] != errNotMember().Message {
		t.Fatalf("message = %v, want the uniform unauthorized message", object["message"])
	}
	if object["retryable"] != false {
		t.Fatalf("retryable = %v, want false on a fatal error", object["retryable"])
	}
	select {
	case code := <-revoked.done:
		if code != protocol.CodeUnauthorized.Exit() {
			t.Fatalf("exit %d, want %d", code, protocol.CodeUnauthorized.Exit())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the revoked watch did not exit within 2 s")
	}

	// The positive control: A is still a member, so its watch polled
	// through the same check and is still there.
	kept.quiet(3 * pollInterval)
	if code := kept.exit(); code != 0 {
		t.Fatalf("exit %d, want 0 on stdin EOF for a member's watch", code)
	}
}
