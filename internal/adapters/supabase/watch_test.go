package supabase

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/appshapes/brigade/internal/protocol"
)

// The watch's unit tests: a fake Phoenix server (join ok, a scripted
// refusal, hints, heartbeats, access_token pushes, a dropped socket) in
// front of the rig's fake GoTrue/PostgREST, and an in-process watch
// driven through pipes. Nothing here dials the real stack.

// ---- the fake Phoenix server ----

// A fakePhoenix answers /realtime/v1/websocket itself and proxies every
// other path to the rig's fake backend, so one profile url covers both.
type fakePhoenix struct {
	t   *testing.T
	srv *httptest.Server

	joined     chan *fakeChannel // every successful join, in order
	attempts   chan *fakeChannel // every join attempt as it arrives, before the reply (the channel not yet joined)
	tokens     chan string       // every access_token push
	joinTokens chan string       // the access_token of every join attempt
	refuse     atomic.Value      // a non-empty string refuses joins with that reason
	silent     atomic.Bool       // when set, heartbeats go unanswered
	joinDelay  atomic.Int64      // nanoseconds between a join's arrival and its ok reply (the server's join latency)
	heartbeats atomic.Int32
	leaves     atomic.Int32
	dials      atomic.Int32
	closed     atomic.Int32 // sockets whose read loop has ended
}

// A fakeChannel is one joined topic on one socket.
type fakeChannel struct {
	ph      *fakePhoenix
	conn    *websocket.Conn
	topic   string
	joinRef string
	joined  atomic.Bool  // the ok reply has been sent; broadcasts reach the socket
	dropped atomic.Int32 // broadcasts attempted before the join completed
	mu      sync.Mutex
}

func newFakePhoenix(t *testing.T, backend string) *fakePhoenix {
	t.Helper()
	target, err := url.Parse(backend)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	ph := &fakePhoenix{
		t: t, joined: make(chan *fakeChannel, 16), attempts: make(chan *fakeChannel, 16),
		tokens: make(chan string, 16), joinTokens: make(chan string, 16),
	}
	ph.refuse.Store("")
	ph.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realtime/v1/websocket" {
			proxy.ServeHTTP(w, r)
			return
		}
		ph.dials.Add(1)
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ph.serve(conn)
	}))
	t.Cleanup(ph.srv.Close)
	return ph
}

// serve is one socket's read loop at vsn 1.0.0.
func (ph *fakePhoenix) serve(conn *websocket.Conn) {
	ctx := ph.t.Context()
	defer func() {
		_ = conn.CloseNow()
		ph.closed.Add(1)
	}()
	var ch *fakeChannel
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var f phxObject
		if err := json.Unmarshal(data, &f); err != nil {
			ph.t.Errorf("fake phoenix: undecodable frame %s", data)
			return
		}
		ref := refString(f.Ref)
		switch f.Event {
		case phxEventJoin:
			var payload struct {
				AccessToken string `json:"access_token"`
				Config      struct {
					Private bool `json:"private"`
				} `json:"config"`
			}
			_ = json.Unmarshal(f.Payload, &payload)
			if !payload.Config.Private || payload.AccessToken == "" {
				ph.t.Errorf("fake phoenix: join without private:true and an access token: %s", data)
			}
			ph.joinTokens <- payload.AccessToken
			if reason, _ := ph.refuse.Load().(string); reason != "" {
				ph.reply(conn, f.Topic, ref, ref, `{"status":"error","response":{"reason":"`+reason+`"}}`)
				continue
			}
			ch = &fakeChannel{ph: ph, conn: conn, topic: f.Topic, joinRef: ref}
			ph.attempts <- ch
			// The server's join latency: the JWT check, the topic policy's
			// query on a cold tenant, a runner's Kong in front. A broadcast
			// on the topic meanwhile is not this socket's (see broadcast).
			if d := time.Duration(ph.joinDelay.Load()); d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					return
				}
			}
			ch.joined.Store(true)
			ph.reply(conn, f.Topic, ref, ref, `{"status":"ok","response":{"postgres_changes":[]}}`)
			ph.joined <- ch
		case phxEventHB:
			ph.heartbeats.Add(1)
			if !ph.silent.Load() {
				ph.reply(conn, phxSocketTopic, ref, "", `{"status":"ok","response":{}}`)
			}
		case phxEventToken:
			var payload struct {
				AccessToken string `json:"access_token"`
			}
			_ = json.Unmarshal(f.Payload, &payload)
			ph.tokens <- payload.AccessToken
		case phxEventLeave:
			ph.leaves.Add(1)
			ph.reply(conn, f.Topic, ref, refString(f.JoinRef), `{"status":"ok","response":{}}`)
		}
	}
}

// reply writes one vsn 1.0.0 frame.
func (ph *fakePhoenix) reply(conn *websocket.Conn, topic, ref, joinRef, payload string) {
	frame := `{"topic":"` + topic + `","event":"phx_reply","payload":` + payload + `,"ref":"` + ref + `"`
	if joinRef != "" {
		frame += `,"join_ref":"` + joinRef + `"`
	}
	frame += "}"
	ctx, cancel := context.WithTimeout(ph.t.Context(), 5*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, []byte(frame))
}

// broadcast writes one broadcast frame of the given event on the
// channel's topic, the shape realtime.send produces — to a socket whose
// join has completed. The server delivers a topic's broadcasts only to
// its subscribers, and a socket still joining is not one yet: a
// broadcast sent then is lost to it, which is the window C-08's `team
// leave` fell into on a CI runner.
func (ch *fakeChannel) broadcast(event, payload string) {
	if !ch.joined.Load() {
		ch.dropped.Add(1)
		ch.ph.t.Logf("fake phoenix: %s broadcast not delivered, the socket has not finished joining", event)
		return
	}
	ch.write(`{"topic":"` + ch.topic + `","event":"broadcast","payload":{"type":"broadcast","event":"` + event + `","payload":` + payload + `},"ref":null}`)
}

// hint sends the message_accepted broadcast notify_message_inserted
// writes for id.
func (ch *fakeChannel) hint(id string) {
	ch.broadcast(broadcastMessage, `{"message_id":"`+id+`","seq":1}`)
}

// revoke sends the membership_revoked broadcast leave_team writes on the
// topic of each of the leaver's open sessions before it closes them.
func (ch *fakeChannel) revoke() {
	ch.broadcast(broadcastRevoked, `{"session_id":"`+strings.TrimPrefix(ch.topic, phxTopicPrefix)+`"}`)
}

// systemError brings the channel down the way an expired or revoked JWT
// does: a system error, then phx_close.
func (ch *fakeChannel) systemError(message string) {
	ch.write(`{"topic":"` + ch.topic + `","event":"system","payload":{"status":"error","message":"` + message + `","extension":"system"},"ref":null}`)
	ch.write(`{"topic":"` + ch.topic + `","event":"phx_close","payload":{},"ref":null,"join_ref":"` + ch.joinRef + `"}`)
}

// drop closes the socket without a handshake.
func (ch *fakeChannel) drop() {
	_ = ch.conn.CloseNow()
}

func (ch *fakeChannel) write(frame string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ctx, cancel := context.WithTimeout(ch.ph.t.Context(), 5*time.Second)
	defer cancel()
	_ = ch.conn.Write(ctx, websocket.MessageText, []byte(frame))
}

// nextJoin waits for a join.
func (ph *fakePhoenix) nextJoin() *fakeChannel {
	ph.t.Helper()
	select {
	case ch := <-ph.joined:
		return ch
	case <-time.After(10 * time.Second):
		ph.t.Fatal("no realtime join within 10 s")
		return nil
	}
}

// ---- the fake inbox ----

// A fakeInbox scripts the four RPCs the watch calls on the rig's fake
// backend: fetch_inbox drains the accepted set, ack_messages moves ids
// to injected, session_heartbeat and close_session answer their shapes.
// refuse, when set, answers every RPC with a scripted PostgREST failure.
type fakeInbox struct {
	mu       sync.Mutex
	accepted []protocol.MessageEnvelope
	closed   bool
	fetches  int
	closes   int
	refuse   func(fn string) (status int, code, message string)
	once     map[string]func(w http.ResponseWriter) bool // a scripted answer for the next call of fn
	gate     chan struct{}                               // when set, the next fetch_inbox waits on it before anything else
}

func (in *fakeInbox) install(be *fakeBackend) {
	in.once = map[string]func(http.ResponseWriter) bool{}
	be.onRPC = func(w http.ResponseWriter, r *http.Request, fn, _ string, args map[string]any) {
		if fn == "fetch_inbox" {
			in.mu.Lock()
			gate := in.gate
			in.gate = nil
			in.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
		}
		in.mu.Lock()
		defer in.mu.Unlock()
		if hook := in.once[fn]; hook != nil {
			delete(in.once, fn)
			if hook(w) {
				return
			}
		}
		if in.refuse != nil {
			if status, code, message := in.refuse(fn); status != 0 {
				postgrest(w, status, code, message)
				return
			}
		}
		switch fn {
		case "fetch_inbox":
			in.fetches++
			page, _ := json.Marshal(in.accepted)
			writeJSON(w, http.StatusOK, string(page))
		case "ack_messages":
			ids, _ := args["p_message_ids"].([]any)
			acked, unknown := []string{}, []string{}
			for _, raw := range ids {
				id, _ := raw.(string)
				kept := in.accepted[:0]
				found := false
				for _, m := range in.accepted {
					if m.MessageID == id {
						found = true
						continue
					}
					kept = append(kept, m)
				}
				in.accepted = kept
				if found {
					acked = append(acked, id)
				} else {
					unknown = append(unknown, id)
				}
			}
			out, _ := json.Marshal(map[string]any{"acked": acked, "unknown": unknown})
			writeJSON(w, http.StatusOK, string(out))
		case "session_heartbeat":
			if in.closed {
				postgrest(w, http.StatusBadRequest, "P0001", "brigade:conflict:session_closed")
				return
			}
			state := "idle"
			if activity, _ := args["p_activity"].(string); activity == "busy" {
				state = "active"
			}
			id, _ := args["p_session_id"].(string)
			now := time.Now().UTC().Format(time.RFC3339Nano)
			writeJSON(w, http.StatusOK, `{"session_id":"`+id+`","state":"`+state+`","lease_until":"`+now+`","server_time":"`+now+`"}`)
		case "close_session":
			in.closes++
			in.closed = true
			id, _ := args["p_session_id"].(string)
			writeJSON(w, http.StatusOK, `{"session_id":"`+id+`","state":"offline"}`)
		default:
			postgrest(w, http.StatusNotFound, "PGRST202", "Could not find the function")
		}
	}
}

// accept adds one accepted message for the watched session and returns
// its id.
func (in *fakeInbox) accept(body string) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	id := fmt.Sprintf("%08x-0000-4000-8000-%012x", len(in.accepted)+1, time.Now().UnixNano()&0xffffffffffff)
	in.accepted = append(in.accepted, protocol.MessageEnvelope{
		ProtocolVersion: protocol.ProtocolVersion, Kind: "text", MessageID: id, TeamRef: testTeamID,
		Sender:             protocol.Sender{PrincipalRef: testUserID, SessionID: testTeamID, SessionName: "sender"},
		RecipientSessionID: watchSession, Body: body, CreatedAt: time.Now().UTC(), DeliveryState: "accepted",
	})
	return id
}

// fetched is the number of fetch_inbox calls answered so far.
func (in *fakeInbox) fetched() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.fetches
}

// settled waits until at least n fetch_inbox calls were answered — the
// catch-up before ready and the drain on join ok are two — so a test that
// counts later drains, or scripts a refusal, starts from a known point.
func (in *fakeInbox) settled(t *testing.T, n int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for in.fetched() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := in.fetched(); got < n {
		t.Fatalf("fetch_inbox ran %d time(s), want at least %d", got, n)
	}
	return in.fetched()
}

// holdFirstFetch makes the next fetch_inbox — the ownership check that
// precedes `ready` — wait until release is called (or the test ends),
// with the inbox's lock free meanwhile so the test can accept and hint:
// a slow backend on a CI runner, held for as long as the test wants.
// The refusal scripted by refuseAll, if any, is answered after the hold.
func (in *fakeInbox) holdFirstFetch(t *testing.T) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	in.mu.Lock()
	in.gate = gate
	in.mu.Unlock()
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	return release
}

// refuseAll scripts one PostgREST failure for every RPC.
func (in *fakeInbox) refuseAll(status int, code, message string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refuse = func(string) (int, string, string) { return status, code, message }
}

// allow clears refuseAll.
func (in *fakeInbox) allow() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refuse = nil
}

// next scripts the next call of fn.
func (in *fakeInbox) next(fn string, hook func(w http.ResponseWriter) bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.once[fn] = hook
}

// ---- the rig ----

// watchSession is the watched session's id in these tests.
const watchSession = "cccccccc-dddd-4eee-8fff-000000000001"

// watchRig is a joined rig whose backend url is the fake Phoenix front.
func watchRig(t *testing.T) (*rig, *fakePhoenix, *fakeInbox) {
	t.Helper()
	r := newRig(t)
	ph := newFakePhoenix(t, r.be.srv.URL)
	r.ok("profile", "init", "--url", ph.srv.URL, "--key", testKey)
	r.writeSession(r.session(time.Hour, "rt-1"))
	r.bindTeam(testTeamID, "ops")
	in := &fakeInbox{}
	in.install(r.be)
	return r, ph, in
}

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

// A watchRun drives one `message watch` in-process: a pipe for stdin, a
// pipe for stdout read with the protocol's own reader, and the exit
// status on a channel.
type watchRun struct {
	t      *testing.T
	stdin  *io.PipeWriter
	lines  chan []byte
	done   chan int
	exited chan struct{}
	stderr *syncBuffer
}

func startWatch(t *testing.T, r *rig, args ...string) *watchRun {
	t.Helper()
	return startWatchWith(t, r, func(w io.Writer) io.Writer { return w }, args...)
}

// startWatchWith is startWatch with the adapter's stdout wrapped by wrap,
// so a test can hold the adapter goroutine inside a write.
func startWatchWith(t *testing.T, r *rig, wrap func(io.Writer) io.Writer, args ...string) *watchRun {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	w := &watchRun{t: t, stdin: stdinW, lines: make(chan []byte, 256), done: make(chan int, 1), exited: make(chan struct{}), stderr: &syncBuffer{}}
	go func() {
		defer close(w.exited)
		code := run(args, stdinR, wrap(stdoutW), w.stderr, r.env, r.clock())
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
		// Wait for the watcher goroutine itself, not just for stdin to
		// close: a watcher still inside an RPC when its test ended kept
		// running into the next test, and the non-parallel tests that
		// rewrite the package-level watchTiming raced its reads (a DATA
		// RACE caught by the driver's -race gate on 2026-09-03, between
		// drainTiming and a previous test's rearm). The bound is a hang
		// catcher: stdin EOF ends a watch within 5 s by 4.4.9.
		select {
		case <-w.exited:
		case <-time.After(30 * time.Second):
			t.Errorf("the watch goroutine did not exit within 30 s of stdin closing")
		}
	})
	return w
}

// next returns the next event within d, or fails the test.
func (w *watchRun) next(d time.Duration) map[string]any {
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
		if _, isString := event["event"].(string); !isString {
			w.t.Fatalf("a watch line has no string event member: %q", line)
		}
		return event
	case <-time.After(d):
		w.t.Fatalf("no watch event within %s", d)
		return nil
	}
}

// expect reads until an event of kind arrives within d; `status` events
// met on the way are returned to the caller through statuses.
func (w *watchRun) expect(kind string, d time.Duration) map[string]any {
	w.t.Helper()
	until := time.Now().Add(d)
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			w.t.Fatalf("no %s event within %s", kind, d)
		}
		event := w.next(remaining)
		if event["event"] == kind {
			return event
		}
		if event["event"] != protocol.EventStatus {
			w.t.Fatalf("event %v arrived while waiting for %s", event, kind)
		}
	}
}

// quiet asserts that nothing but a `status` arrives for d.
func (w *watchRun) quiet(d time.Duration) {
	w.t.Helper()
	until := time.Now().Add(d)
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			return
		}
		select {
		case line, ok := <-w.lines:
			if !ok {
				w.t.Fatal("the watch closed stdout during a quiet period")
			}
			if !strings.Contains(string(line), `"event":"status"`) {
				w.t.Fatalf("an unexpected watch event arrived: %q", line)
			}
		case <-time.After(remaining):
			return
		}
	}
}

// send writes one NDJSON command to the watch's stdin.
func (w *watchRun) send(line string) {
	w.t.Helper()
	if _, err := io.WriteString(w.stdin, line+"\n"); err != nil {
		w.t.Fatal(err)
	}
}

// exit closes stdin and returns the exit status.
func (w *watchRun) exit() int {
	w.t.Helper()
	_ = w.stdin.Close()
	return w.wait(5 * time.Second)
}

// wait returns the exit status within d.
func (w *watchRun) wait(d time.Duration) int {
	w.t.Helper()
	select {
	case code := <-w.done:
		return code
	case <-time.After(d):
		w.t.Fatalf("the watch did not exit within %s", d)
		return -1
	}
}

// expectReady asserts the one-time first event of 4.4.9 with mode push.
func (w *watchRun) expectReady() {
	w.t.Helper()
	event := w.next(5 * time.Second)
	if event["event"] != protocol.EventReady || event["session_id"] != watchSession ||
		event["mode"] != protocol.WatchModePush || event["protocol_version"] != protocol.ProtocolVersion {
		w.t.Fatalf("first event = %v, want a push ready for %s", event, watchSession)
	}
}

// expectStatus asserts the next non-message event is a status with state
// and detail.
func (w *watchRun) expectStatus(state, detail string) {
	w.t.Helper()
	event := w.next(5 * time.Second)
	if event["event"] != protocol.EventStatus || event["state"] != state || event["detail"] != detail {
		w.t.Fatalf("event = %v, want status %s/%s", event, state, detail)
	}
}

func messageID(t *testing.T, event map[string]any) string {
	t.Helper()
	if event["event"] != protocol.EventMessage {
		t.Fatalf("event = %v, want message", event)
	}
	message, _ := event["message"].(map[string]any)
	return str(t, message, "message_id")
}

// ---- the tests ----

// TestWatchPushReadyLiveAndHints covers C-33, C-35 and C-36 in push mode
// at the shipped timers: ready first with mode push, status live once the
// private channel is joined, a hint makes the message arrive at once (the
// drain timer is 30 s away), a second hint for the same message drains
// again and emits nothing twice, and stdin EOF ends the watch with exit 0
// after a phx_leave.
func TestWatchPushReadyLiveAndHints(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ch := ph.nextJoin()
	if ch.topic != sessionTopic(watchSession) {
		t.Fatalf("joined topic %q, want %q", ch.topic, sessionTopic(watchSession))
	}
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	before := in.settled(t, 2)

	hinted := in.accept("hinted")
	ch.hint(hinted)
	start := time.Now()
	if got := messageID(t, w.expect(protocol.EventMessage, 5*time.Second)); got != hinted {
		t.Fatalf("message %s, want the hinted %s", got, hinted)
	}
	if took := time.Since(start); took > 700*time.Millisecond {
		t.Errorf("the hinted message took %s; the hint should beat the %s drain timer", took, watchTiming.drainLive)
	}
	// A duplicate hint (a burst of senders, a lost ack) is one more drain
	// and no second emission.
	ch.hint(hinted)
	w.quiet(1500 * time.Millisecond)
	if got := in.fetched(); got != before+2 {
		t.Errorf("fetch_inbox ran %d time(s) after the join, want 2 (one per hint)", got-before)
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d, want 0 on stdin EOF (C-38)", code)
	}
	if ph.leaves.Load() != 1 {
		t.Errorf("phx_leave sent %d times, want 1", ph.leaves.Load())
	}
}

// TestWatchTimerOnlyDoesNotDrainEarly is the negative control for plan
// 5.6's drain timer at the SHIPPED intervals, in two halves. First the
// settling window: a message accepted WITHOUT a hint right after the
// join is found by the two 3 s settling drains (watchTiming.settle) —
// the fan-out to a fresh join is not warm at once, and this is what
// bounds a lost broadcast to ~3 s instead of 30. Then the steady state:
// once those two drains have run, a hint-less message is NOT fetched
// within 5 s, because the live timer is 30 s. The channel is the fast
// path and the timer only bounds what a lost hint costs — one RPC per
// 30 s per watcher, not one per second — so the suite's deadlines
// (C-08, C-35) are met by hints and the settling drains, never by this
// timer.
func TestWatchTimerOnlyDoesNotDrainEarly(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	in.settled(t, 2)

	lost := in.accept("no hint, lost right after the join")
	joined := time.Now()
	if got := messageID(t, w.expect(protocol.EventMessage, 2*watchTiming.settle)); got != lost {
		t.Fatalf("message %s, want the hint-less %s found by a settling drain", got, lost)
	}
	t.Logf("the settling drain found a hint-less message %s after the join (settle %s)", time.Since(joined).Round(time.Millisecond), watchTiming.settle)
	before := in.settled(t, 4)

	in.accept("no hint, after the settling drains")
	w.quiet(5 * time.Second)
	if got := in.fetched(); got != before {
		t.Fatalf("fetch_inbox ran %d more time(s) within 5 s with no hint once settling was over; the live drain timer is %s, want none", got-before, watchTiming.drainLive)
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d, want 0 on stdin EOF (C-38)", code)
	}
}

// TestWatchCatchUpBeforeReady covers C-34 and C-40: messages accepted
// before the watch started are emitted right after ready, oldest first,
// once.
func TestWatchCatchUpBeforeReady(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	first := in.accept("first")
	second := in.accept("second")
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	if got := messageID(t, w.next(5*time.Second)); got != first {
		t.Fatalf("first catch-up message %s, want %s", got, first)
	}
	if got := messageID(t, w.next(5*time.Second)); got != second {
		t.Fatalf("second catch-up message %s, want %s", got, second)
	}
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	w.quiet(2500 * time.Millisecond)
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchForeignSessionIsTheUniformNotFound covers C-37: the backend's
// PT404 for a session the principal does not own is ONE not_found error
// event, no ready, exit 6, byte-identical to the event for an id that is
// not uuid-shaped.
func TestWatchForeignSessionIsTheUniformNotFound(t *testing.T) {
	t.Parallel()
	r, _, in := watchRig(t)
	in.refuseAll(http.StatusNotFound, "PT404", "brigade:not_found")
	foreign := r.exec("", "message", "watch", "--session", watchSession)
	if foreign.code != protocol.CodeNotFound.Exit() {
		t.Fatalf("exit %d, want %d (stdout %s)", foreign.code, protocol.CodeNotFound.Exit(), foreign.stdout)
	}
	if strings.Contains(foreign.stdout, `"ready"`) {
		t.Fatalf("a ready event preceded the refusal: %s", foreign.stdout)
	}
	unknown := r.exec("", "message", "watch", "--session", "deadbeef")
	if foreign.stdout != unknown.stdout {
		t.Fatalf("foreign and unknown differ:\n%s%s", foreign.stdout, unknown.stdout)
	}
	object, _ := decode(t, foreign.stdout)["error"].(map[string]any)
	if object["retryable"] != false || object["message"] != errNotFound().Message {
		t.Fatalf("error object = %v", object)
	}
}

// TestWatchRevocationEndsTheWatch covers C-08 at the shipped timers:
// leave_team writes a membership_revoked broadcast on the session's topic
// before it closes the session; the watch treats it as a hint and drains
// at once, the backend answers the uniform unauthorized, and the watch
// emits ONE unauthorized error event, retryable false, with the fixed
// message, and exits 5 — within 2 s of the broadcast (C-08 allows the
// 5 s push budget; the hint path is well inside it) although the drain
// timer is 30 s away, which is what pins the hint (a timer-only watch
// sits here).
func TestWatchRevocationEndsTheWatch(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ch := ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	in.settled(t, 2)

	in.refuseAll(http.StatusForbidden, "42501", "brigade:unauthorized")
	revoked := time.Now()
	ch.revoke()
	event := w.expect(protocol.EventError, 2*time.Second)
	object, _ := event["error"].(map[string]any)
	if object["code"] != string(protocol.CodeUnauthorized) || object["retryable"] != false || object["message"] != errNotMember().Message {
		t.Fatalf("event = %v, want the uniform unauthorized with retryable false", event)
	}
	if code := w.wait(2 * time.Second); code != protocol.CodeUnauthorized.Exit() {
		t.Fatalf("exit %d, want %d", code, protocol.CodeUnauthorized.Exit())
	}
	if took := time.Since(revoked); took > 2*time.Second {
		t.Errorf("the revocation took %s to end a watch whose channel was joined; want under 2 s (C-08 allows the 5 s push budget)", took)
	}
}

// TestWatchSlowJoinStillSeesARevocationAfterReady is C-08's failure in CI
// run 33678110011 ("no error event within 2s", 2.24 s on the runner)
// reproduced in-process, the latencies exaggerated so that only the
// START ORDER decides. The backend answers the first fetch_inbox after
// 2 s and the server completes the join 3 s after phx_join (a runner's
// Kong and Realtime, a cold tenant's policy query); the suite's `team
// leave` — here the membership_revoked broadcast — lands the instant
// `ready` is read. The socket has not finished joining, so the server
// never delivers that broadcast to it (the fake drops it the same way),
// and the revocation can only be found by the drain that follows the
// join. Dialled BEFORE the first fetch, the join is 2 s old at `ready`
// and completes 1 s later: the watch ends inside the 2 s bound. Dialled
// after the catch-up — the old order — it completes 3 s after the
// revocation, past the bound, and nothing but that late drain or the
// 30 s live timer would ever see it.
func TestWatchSlowJoinStillSeesARevocationAfterReady(t *testing.T) {
	t.Parallel()
	const fetchLatency, joinLatency, bound = 2 * time.Second, 3 * time.Second, 2 * time.Second
	r, ph, in := watchRig(t)
	ph.joinDelay.Store(int64(joinLatency))
	release := in.holdFirstFetch(t)
	time.AfterFunc(fetchLatency, release)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	in.refuseAll(http.StatusForbidden, "42501", "brigade:unauthorized")
	revoked := time.Now()
	var ch *fakeChannel
	select {
	case ch = <-ph.attempts:
		ch.revoke()
	default:
		t.Logf("no phx_join had reached the fake at ready; the revocation was broadcast to no socket of this watch")
	}
	event := w.expect(protocol.EventError, bound)
	object, _ := event["error"].(map[string]any)
	if object["code"] != string(protocol.CodeUnauthorized) || object["retryable"] != false || object["message"] != errNotMember().Message {
		t.Fatalf("event = %v, want the uniform unauthorized with retryable false", event)
	}
	if code := w.wait(2 * time.Second); code != protocol.CodeUnauthorized.Exit() {
		t.Fatalf("exit %d, want %d", code, protocol.CodeUnauthorized.Exit())
	}
	took := time.Since(revoked)
	if ch == nil || ch.dropped.Load() != 1 {
		t.Errorf("the revocation did not hit a socket still joining (attempt seen: %v); the test did not exercise the window", ch != nil)
	}
	if got := r.be.calls(rpcPath + "fetch_inbox"); got != 2 {
		t.Errorf("fetch_inbox was called %d time(s), want 2: the catch-up and the post-join drain that found the revocation", got)
	}
	t.Logf("the revocation ended the watch %s after a leave that hit a joining socket (join %s, first fetch %s)", took, joinLatency, fetchLatency)
}

// TestWatchHintDuringTheCatchUpIsNotLost: the channel joins while the
// adapter is still inside the catch-up (a gated stdout holds the first
// message's write, so the loop that reads the link's reports has not
// started), a message is accepted and hinted meanwhile, and once the
// catch-up completes every frame that queued has its effect: `status
// live`, the message on the post-join drain, and the hint's own drain —
// three fetches, nothing lost, the drain timers (10 s polling, 30 s
// live) never involved.
func TestWatchHintDuringTheCatchUpIsNotLost(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	held := in.accept("held in the catch-up")
	gate := make(chan struct{})
	open := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(open)
	w := startWatchWith(t, r, func(w io.Writer) io.Writer { return &gatedStdout{w: w, gate: gate} },
		"message", "watch", "--session", watchSession)
	w.expectReady()
	// Dialled before the first fetch, the channel joins while the
	// catch-up write is held (the old order never dialled before it).
	ch := ph.nextJoin()
	hinted := in.accept("hinted during the catch-up")
	ch.hint(hinted)
	open()
	if got := messageID(t, w.next(5*time.Second)); got != held {
		t.Fatalf("catch-up message %s, want %s", got, held)
	}
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	if got := messageID(t, w.expect(protocol.EventMessage, 2*time.Second)); got != hinted {
		t.Fatalf("message %s, want the hinted %s", got, hinted)
	}
	in.settled(t, 3)
	w.quiet(time.Second)
	if got := in.fetched(); got != 3 {
		t.Errorf("fetch_inbox ran %d time(s), want 3: the catch-up, the post-join drain and the hint's", got)
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d, want 0 on stdin EOF (C-38)", code)
	}
}

// TestWatchPreReadyFailureCancelsTheDial: the dial starts before the
// ownership check, so a watch of a session the principal does not own
// (C-37) may hold a joined channel by the time the backend refuses. The
// refusal is still ONE not_found error event and exit 6 with nothing else
// on stdout — the link only ever reports to the loop, which never starts
// — and the link is cancelled on the way out: phx_leave, then the socket
// closed, before the process ends.
func TestWatchPreReadyFailureCancelsTheDial(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	in.refuseAll(http.StatusNotFound, "PT404", "brigade:not_found")
	release := in.holdFirstFetch(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	ph.nextJoin() // the join completes while the ownership check is pending
	release()
	event := w.next(5 * time.Second)
	object, _ := event["error"].(map[string]any)
	if event["event"] != protocol.EventError || object["code"] != string(protocol.CodeNotFound) || object["retryable"] != false || object["message"] != errNotFound().Message {
		t.Fatalf("first event = %v, want the uniform not_found error", event)
	}
	if code := w.wait(5 * time.Second); code != protocol.CodeNotFound.Exit() {
		t.Fatalf("exit %d, want %d", code, protocol.CodeNotFound.Exit())
	}
	for line := range w.lines {
		t.Errorf("a line followed the refusal: %s", line)
	}
	// The socket MUST close on the refusal. Whether a phx_leave precedes
	// the close depends on a race this test cannot pin: the fake answered
	// the join, but the adapter may or may not have consumed that reply
	// before the refusal cancelled the link — a link still waiting on its
	// join is torn down at once without the leave handshake (a Phoenix
	// process in its refusal backoff never answers one), a joined link
	// leaves first. Both are correct; CI's macOS runner took the first
	// path (leaves 0) while this machine takes the second. The joined
	// path's leave is pinned by the EOF and SIGTERM exit tests after
	// `status live`.
	deadline := time.Now().Add(2 * time.Second)
	for ph.closed.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ph.dials.Load() != 1 || ph.closed.Load() != 1 || ph.leaves.Load() > 1 {
		t.Fatalf("dials %d, leaves %d, sockets closed %d; want 1, at most 1, 1: the pending link closed on the refusal",
			ph.dials.Load(), ph.leaves.Load(), ph.closed.Load())
	}
	t.Logf("pending link torn down with %d leave frame(s) before the close", ph.leaves.Load())
}

// TestWatchJoinRefusedKeepsPolling: a join the server refuses while the
// RPC still drains — a session closed by another process, or a
// realtime-only refusal — is a status polling event with a fixed detail,
// never an exit; messages still arrive on the POLLING drain timer (the
// live one is set to an hour here, so it is the polling interval that
// delivers).
func TestWatchJoinRefusedKeepsPolling(t *testing.T) {
	drainTiming(t, time.Hour, 250*time.Millisecond)
	r, ph, in := watchRig(t)
	ph.refuse.Store("Unauthorized: You do not have permissions to read from this Channel topic: brigade:session:x")
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	w.expectStatus(protocol.StatusStatePolling, linkReasonUnauthorized)
	id := in.accept("polled")
	if got := messageID(t, w.expect(protocol.EventMessage, 3*time.Second)); got != id {
		t.Fatalf("message %s, want %s", got, id)
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

// TestWatchBadTokenRefreshesAndRejoins: a join refused for the JWT earns
// one forced refresh and a rejoin with the new token; the channel then
// comes up.
func TestWatchBadTokenRefreshesAndRejoins(t *testing.T) {
	t.Parallel()
	r, ph, _ := watchRig(t)
	ph.refuse.Store("InvalidJWTToken: Token has expired 10 seconds ago")
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	select {
	case first := <-ph.joinTokens:
		ph.refuse.Store("")
		ph.nextJoin()
		second := <-ph.joinTokens
		if first == second {
			t.Fatalf("the rejoin presented the refused token again")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no join attempt")
	}
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	if r.be.calls("/auth/v1/token") != 1 {
		t.Fatalf("/token called %d times, want exactly one forced refresh", r.be.calls("/auth/v1/token"))
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchTransportLossReconnectsAndPushesToken: a dropped socket is a
// status polling event, the channel is rejoined after the backoff with a
// status live, and a token refreshed by the drain (PGRST303 once) is
// pushed on the channel as access_token.
func TestWatchTransportLossReconnectsAndPushesToken(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ch := ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	ch.drop()
	w.expectStatus(protocol.StatusStatePolling, linkReasonDisconnected)
	ch2 := ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	if ph.dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2", ph.dials.Load())
	}
	in.settled(t, 3)

	in.next("fetch_inbox", func(w http.ResponseWriter) bool {
		postgrest(w, http.StatusUnauthorized, "PGRST303", "JWT expired")
		return true
	})
	ch2.hint(in.accept("after the refresh"))
	select {
	case pushed := <-ph.tokens:
		if pushed != r.readSession().AccessToken {
			t.Fatalf("the pushed token is not the refreshed one on disk")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no access_token push within 5 s of the refresh")
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchChannelErrorTriggersRejoin: a system error on the channel
// (what a revoked member's open channel gets on the next token push) is
// a fall-back to polling and a rejoin; the RPC decides whether the
// watch lives on.
func TestWatchChannelErrorTriggersRejoin(t *testing.T) {
	t.Parallel()
	r, ph, _ := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ch := ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	ch.systemError("You do not have permissions to read from this Channel topic: brigade:session:x")
	w.expectStatus(protocol.StatusStatePolling, linkReasonUnauthorized)
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchStdinCommands covers C-41, B-5 and B-6: ack, heartbeat and
// close are answered; an unknown type, a malformed line and an over-long
// line are logged and skipped; a rejected heartbeat is a retryable error
// after which the watch continues; close closes the session and exits 0.
func TestWatchStdinCommands(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	id := in.accept("ack me")
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	if got := messageID(t, w.next(5*time.Second)); got != id {
		t.Fatalf("catch-up %s, want %s", got, id)
	}
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	w.send(`{"type":"nosuchcommand"}`)
	w.send(`{not json at all`)
	w.send(`{"type":"ack","message_ids":["` + strings.Repeat("x", protocol.MaxLineBytes) + `"]}`)
	w.send(`{"type":"ack","message_ids":["` + id + `","nosuchmessage"]}`)
	event := w.expect(protocol.EventAcked, 5*time.Second)
	acked, _ := event["message_ids"].([]any)
	unknown, _ := event["unknown"].([]any)
	if len(acked) != 1 || acked[0] != id || len(unknown) != 1 || unknown[0] != "nosuchmessage" {
		t.Fatalf("acked event = %v", event)
	}

	w.send(`{"type":"heartbeat","lease_seconds":9999}`)
	event = w.expect(protocol.EventError, 5*time.Second)
	object, _ := event["error"].(map[string]any)
	if object["retryable"] != true || object["code"] != string(protocol.CodeInvalidInput) {
		t.Fatalf("event = %v, want a retryable invalid_input", event)
	}
	w.send(`{"type":"heartbeat","activity":"busy","session_name":"renamed","lease_seconds":120}`)
	event = w.expect(protocol.EventHeartbeatOK, 5*time.Second)
	if event["state"] != protocol.SessionStateActive || event["session_id"] != watchSession {
		t.Fatalf("event = %v, want heartbeat_ok active", event)
	}
	last := r.be.last(rpcPath + "session_heartbeat")
	var args map[string]any
	if err := json.Unmarshal(last.body, &args); err != nil {
		t.Fatal(err)
	}
	if args["p_activity"] != "busy" || args["p_name"] != "renamed" || args["p_lease_seconds"] != float64(120) || args["p_description"] != nil {
		t.Fatalf("session_heartbeat args = %v", args)
	}

	// A session closed by another process: conflict, retryable, the watch
	// goes on (a closed owned session stays drainable, C-31).
	in.mu.Lock()
	in.closed = true
	in.mu.Unlock()
	w.send(`{"type":"heartbeat","activity":"idle"}`)
	event = w.expect(protocol.EventError, 5*time.Second)
	object, _ = event["error"].(map[string]any)
	if object["retryable"] != true || object["code"] != string(protocol.CodeConflict) {
		t.Fatalf("event = %v, want a retryable conflict", event)
	}

	w.send(`{"type":"close"}`)
	if code := w.wait(5 * time.Second); code != 0 {
		t.Fatalf("exit %d after close, want 0", code)
	}
	in.mu.Lock()
	closes := in.closes
	in.mu.Unlock()
	if closes != 1 {
		t.Fatalf("close_session called %d times, want 1", closes)
	}
	stderr := w.stderr.String()
	for _, want := range []string{"unknown_type", "malformed", "line_too_long"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr did not log %q: %s", want, stderr)
		}
	}
	if strings.Contains(stderr, "nosuchcommand") {
		t.Errorf("stderr echoed stdin content: %s", stderr)
	}
}

// TestWatchCommandRevocationIsFatal: a command the backend refuses with
// the uniform unauthorized ends the watch (the revocation window between
// two drains, the fs rule), where a transient failure does not.
func TestWatchCommandRevocationIsFatal(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	in.next("ack_messages", func(w http.ResponseWriter) bool {
		postgrest(w, http.StatusServiceUnavailable, "57014", "canceling statement")
		return true
	})
	w.send(`{"type":"ack","message_ids":["` + watchSession + `"]}`)
	event := w.expect(protocol.EventError, 5*time.Second)
	object, _ := event["error"].(map[string]any)
	if object["retryable"] != true || object["code"] != string(protocol.CodeUnavailable) {
		t.Fatalf("event = %v, want a retryable unavailable", event)
	}

	in.next("ack_messages", func(w http.ResponseWriter) bool {
		postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized")
		return true
	})
	w.send(`{"type":"ack","message_ids":["` + watchSession + `"]}`)
	event = w.expect(protocol.EventError, 5*time.Second)
	object, _ = event["error"].(map[string]any)
	if object["retryable"] != false || object["code"] != string(protocol.CodeUnauthorized) {
		t.Fatalf("event = %v, want a fatal unauthorized", event)
	}
	if code := w.wait(5 * time.Second); code != protocol.CodeUnauthorized.Exit() {
		t.Fatalf("exit %d, want 5", code)
	}
}

// TestWatchOutageIsReportedOnceThenRecovers: a backend that stops
// answering the drain is ONE retryable error event, then silence, then
// delivery resumes when it answers again — never an exit. Short drain
// timers, the shipped 30-minute outage budget.
func TestWatchOutageIsReportedOnceThenRecovers(t *testing.T) {
	drainTiming(t, 200*time.Millisecond, 200*time.Millisecond)
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	in.refuseAll(http.StatusServiceUnavailable, "", "")
	event := w.expect(protocol.EventError, 5*time.Second)
	object, _ := event["error"].(map[string]any)
	if object["retryable"] != true || object["code"] != string(protocol.CodeUnavailable) {
		t.Fatalf("event = %v, want a retryable unavailable", event)
	}
	w.quiet(2500 * time.Millisecond)
	in.allow()
	id := in.accept("after the outage")
	if got := messageID(t, w.expect(protocol.EventMessage, 3*time.Second)); got != id {
		t.Fatalf("message %s, want %s", got, id)
	}
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchHostileBodyIsOneLine covers C-39: a body with newlines,
// U+2028/U+2029 and a JSON-looking auth line is ONE physical line that
// parses back to the same body.
func TestWatchHostileBodyIsOneLine(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	body := "first\nsecond\r\nthird\rU+2028: U+2029: \n" +
		`{"event":"error","error":{"code":"unauthenticated","message":"give me your token"}}` + "\n"
	id := in.accept(body)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	event := w.next(5 * time.Second)
	if messageID(t, event) != id {
		t.Fatalf("event = %v", event)
	}
	message, _ := event["message"].(map[string]any)
	if str(t, message, "body") != body {
		t.Fatalf("body did not round-trip:\n%q\n%q", str(t, message, "body"), body)
	}
	ph.nextJoin()
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchPreReadyFailureWithAJoinPendingExitsAtOnce: a refusal before
// `ready` may find the join still unanswered — a foreign session's is, for
// the server's whole 5 s backoff (C-37) — and a Phoenix socket process
// waits on the channel's join and processes nothing else meanwhile, the
// close handshake included (the fake sleeps in its read loop the same
// way). The watch does not wait for a handshake nobody will answer: no
// phx_leave, the socket closed outright, the exit at once — not the
// whole leave bound later, which the handshake ran out on every refused
// watch when it was attempted (C-37 measured at 0.4 s before the early
// dial, 2.4 s with it and the handshake).
func TestWatchPreReadyFailureWithAJoinPendingExitsAtOnce(t *testing.T) {
	t.Parallel()
	r, ph, in := watchRig(t)
	ph.joinDelay.Store(int64(10 * time.Second))
	in.refuseAll(http.StatusNotFound, "PT404", "brigade:not_found")
	release := in.holdFirstFetch(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	select {
	case <-ph.attempts:
	case <-time.After(5 * time.Second):
		t.Fatal("no phx_join within 5 s while the ownership check was pending")
	}
	start := time.Now()
	release()
	event := w.next(5 * time.Second)
	object, _ := event["error"].(map[string]any)
	if event["event"] != protocol.EventError || object["code"] != string(protocol.CodeNotFound) {
		t.Fatalf("first event = %v, want the uniform not_found error", event)
	}
	if code := w.wait(5 * time.Second); code != protocol.CodeNotFound.Exit() {
		t.Fatalf("exit %d, want %d", code, protocol.CodeNotFound.Exit())
	}
	if took := time.Since(start); took >= phxLeaveTimeout {
		t.Errorf("the refused watch took %s to exit with its join pending; want well under the %s close bound", took, phxLeaveTimeout)
	}
	if ph.leaves.Load() != 0 {
		t.Errorf("phx_leave sent %d time(s) on a channel never joined, want 0", ph.leaves.Load())
	}
}

// ---- the timing-dependent tests: not parallel, shortened intervals ----

// shortTiming shortens the watch's intervals for one non-parallel test
// and restores them afterwards.
func shortTiming(t *testing.T) {
	t.Helper()
	saved := watchTiming
	watchTiming.drainLive = 200 * time.Millisecond
	watchTiming.drainPolling = 200 * time.Millisecond
	watchTiming.heartbeat = 150 * time.Millisecond
	watchTiming.outage = 1500 * time.Millisecond
	watchTiming.reconnect = []time.Duration{100 * time.Millisecond, 100 * time.Millisecond}
	t.Cleanup(func() { watchTiming = saved })
}

// drainTiming sets only the two drain timers for one non-parallel test
// and restores them afterwards; every other interval stays as shipped.
func drainTiming(t *testing.T, live, polling time.Duration) {
	t.Helper()
	saved := watchTiming
	watchTiming.drainLive = live
	watchTiming.drainPolling = polling
	t.Cleanup(func() { watchTiming = saved })
}

// settleTiming sets the settling drains' interval for one non-parallel
// test and restores it afterwards.
func settleTiming(t *testing.T, settle time.Duration) {
	t.Helper()
	saved := watchTiming
	watchTiming.settle = settle
	t.Cleanup(func() { watchTiming = saved })
}

// TestWatchSettleDrainFindsARevocationWhileTheJoinIsPending: C-08's other
// CI failure (run 33756168929, no error event within the 5 s budget). The
// join takes longer than the budget and NO broadcast reaches the watch,
// so only a timer can find the revocation; the polling timer is an hour
// here, and the settling drain armed at `ready` finds it inside 2 s.
// The negative arm proves the settling drain is what found it: with the
// settling interval at an hour too, nothing arrives within 2 s.
func TestWatchSettleDrainFindsARevocationWhileTheJoinIsPending(t *testing.T) {
	drainTiming(t, time.Hour, time.Hour)
	for _, arm := range []struct {
		name   string
		settle time.Duration
		found  bool
	}{
		{"settling drain 300ms finds it", 300 * time.Millisecond, true},
		{"negative control: settling drain 1h, nothing finds it", time.Hour, false},
	} {
		t.Run(arm.name, func(t *testing.T) {
			settleTiming(t, arm.settle)
			r, ph, in := watchRig(t)
			ph.joinDelay.Store(int64(10 * time.Second))
			w := startWatch(t, r, "message", "watch", "--session", watchSession)
			w.expectReady()
			in.refuseAll(http.StatusForbidden, "42501", "brigade:unauthorized")
			revoked := time.Now()
			if !arm.found {
				w.quiet(2 * time.Second)
				if got := r.be.calls(rpcPath + "fetch_inbox"); got != 1 {
					t.Errorf("fetch_inbox was called %d time(s), want 1: only the catch-up before the join completed", got)
				}
				return
			}
			event := w.expect(protocol.EventError, 2*time.Second)
			object, _ := event["error"].(map[string]any)
			if object["code"] != string(protocol.CodeUnauthorized) || object["retryable"] != false {
				t.Fatalf("event = %v, want the uniform unauthorized with retryable false", event)
			}
			if code := w.wait(2 * time.Second); code != protocol.CodeUnauthorized.Exit() {
				t.Fatalf("exit %d, want %d", code, protocol.CodeUnauthorized.Exit())
			}
			if got := r.be.calls(rpcPath + "fetch_inbox"); got != 2 {
				t.Errorf("fetch_inbox was called %d time(s), want 2: the catch-up and the settling drain that found the revocation", got)
			}
			t.Logf("the settling drain ended the watch %s after a revocation no broadcast announced, with the join still pending", time.Since(revoked))
		})
	}
}

// TestWatchSettleDrainAfterJoinFindsALostBroadcast: a message accepted
// right after the join whose broadcast never arrives (the fan-out to a
// fresh join is not warm at once; measured after a Realtime restart in
// CI run 33696302372) is found by the settling drains that follow the
// join; once those two have run, the live timer (an hour here) is the
// next drain, so a third hint-less message stays unseen — the control
// that pins the cadence's END as well as its start.
func TestWatchSettleDrainAfterJoinFindsALostBroadcast(t *testing.T) {
	drainTiming(t, time.Hour, time.Hour)
	settleTiming(t, 300*time.Millisecond)
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	in.settled(t, 2)

	lost := in.accept("broadcast lost right after the join")
	if got := messageID(t, w.expect(protocol.EventMessage, 2*time.Second)); got != lost {
		t.Fatalf("message %s, want the hint-less %s found by a settling drain", got, lost)
	}
	// Let the second settling drain run too, then the cadence is over.
	in.settled(t, 4)
	late := in.accept("accepted once settling is over")
	w.quiet(1500 * time.Millisecond)
	if got := in.fetched(); got != 4 {
		t.Errorf("fetch_inbox ran %d time(s), want 4: the catch-up, the post-join drain and the two settling drains, then nothing until the hour-long live timer", got)
	}
	_ = late
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d, want 0 on stdin EOF (C-38)", code)
	}
}

// TestWatchDrainTimerWhileLive: with the channel up, a message accepted
// with no hint (a lost broadcast) still arrives on the LIVE drain timer,
// and is not emitted again by the drains that follow. The polling
// interval is an hour here, so the timer that fires is the live one,
// re-armed on the join (the watch starts on the polling interval).
func TestWatchDrainTimerWhileLive(t *testing.T) {
	drainTiming(t, 250*time.Millisecond, time.Hour)
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	timed := in.accept("no hint")
	if got := messageID(t, w.expect(protocol.EventMessage, 3*time.Second)); got != timed {
		t.Fatalf("message %s, want the timed %s", got, timed)
	}
	w.quiet(1000 * time.Millisecond)
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d, want 0 on stdin EOF (C-38)", code)
	}
}

// TestWatchHeartbeatUnansweredReconnects: heartbeats go out on the
// cadence, and one that goes unanswered for a whole interval drops the
// socket and reconnects.
func TestWatchHeartbeatUnansweredReconnects(t *testing.T) {
	shortTiming(t)
	r, ph, _ := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	deadline := time.Now().Add(3 * time.Second)
	for ph.heartbeats.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ph.heartbeats.Load() < 2 {
		t.Fatalf("heartbeats = %d, want at least 2 on a %s cadence", ph.heartbeats.Load(), watchTiming.heartbeat)
	}
	ph.silent.Store(true)
	w.expectStatus(protocol.StatusStatePolling, linkReasonHeartbeat)
	ph.silent.Store(false)
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	if code := w.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestWatchOutageBudgetEndsTheWatch: consecutive drain failures past the
// budget end the watch with unavailable, exit 9, after the ONE retryable
// error event reported at the start of the outage.
func TestWatchOutageBudgetEndsTheWatch(t *testing.T) {
	shortTiming(t)
	r, ph, in := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	in.refuseAll(http.StatusBadGateway, "", "")
	first := w.expect(protocol.EventError, 5*time.Second)
	if object, _ := first["error"].(map[string]any); object["retryable"] != true {
		t.Fatalf("first event = %v, want retryable", first)
	}
	final := w.expect(protocol.EventError, 5*time.Second)
	object, _ := final["error"].(map[string]any)
	if object["retryable"] != false || object["code"] != string(protocol.CodeUnavailable) {
		t.Fatalf("final event = %v, want a fatal unavailable", final)
	}
	if code := w.wait(5 * time.Second); code != protocol.CodeUnavailable.Exit() {
		t.Fatalf("exit %d, want 9", code)
	}
}

// TestWatchExitsZeroOnSIGTERM covers C-38's second half in-process: the
// handler is installed before ready, so a SIGTERM sent on ready ends the
// watch with exit 0 within 5 s and the channel is left. Not parallel: the
// signal reaches every NotifyContext in the process.
func TestWatchExitsZeroOnSIGTERM(t *testing.T) {
	r, ph, _ := watchRig(t)
	w := startWatch(t, r, "message", "watch", "--session", watchSession)
	w.expectReady()
	ph.nextJoin()
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := w.wait(5 * time.Second); code != 0 {
		t.Fatalf("exit %d after SIGTERM, want 0", code)
	}
	if ph.leaves.Load() != 1 {
		t.Errorf("phx_leave sent %d times, want 1", ph.leaves.Load())
	}
}

// gatedStdout holds the first `message` line it is handed until gate is
// closed, so the adapter goroutine is provably still inside the catch-up
// when the test sends a signal.
type gatedStdout struct {
	w    io.Writer
	gate chan struct{}
	held atomic.Bool
}

func (g *gatedStdout) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"event":"message"`)) && g.held.CompareAndSwap(false, true) {
		<-g.gate
	}
	return g.w.Write(p)
}

// TestWatchSIGTERMHandlerPrecedesReady pins the ORDER 4.4.9 needs, not
// just the outcome: the SIGTERM handler is in place before `ready` is
// written. The fake inbox holds one message, so right after `ready` the
// adapter goroutine is held inside the catch-up write by a gated stdout,
// and the signal lands while it is held. With the handler installed first
// the signal is caught and the watch exits 0 once the gate opens; with the
// handler installed any later — after `ready`, after the catch-up (the fs
// adapter's measured CI failure) — the default disposition kills this
// test binary. Not parallel: the signal is process-wide.
func TestWatchSIGTERMHandlerPrecedesReady(t *testing.T) {
	r, _, in := watchRig(t)
	in.accept("held in the catch-up")
	gate := make(chan struct{})
	w := startWatchWith(t, r, func(w io.Writer) io.Writer { return &gatedStdout{w: w, gate: gate} },
		"message", "watch", "--session", watchSession)
	w.expectReady()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	w.expect(protocol.EventMessage, 5*time.Second)
	if code := w.wait(5 * time.Second); code != 0 {
		t.Fatalf("exit %d after a SIGTERM sent on ready, want 0", code)
	}
}

// ---- the wire ----

// TestPhoenixFraming pins both serializer versions: the 1.0.0 object, the
// 2.0.0 array, realtime-js's binary broadcast, and the ref rendering.
func TestPhoenixFraming(t *testing.T) {
	t.Parallel()
	v1 := []byte(`{"topic":"realtime:brigade:session:s","event":"phx_reply","payload":{"status":"ok","response":{}},"ref":"3","join_ref":"1"}`)
	f, err := decodePhxFrame(phxVersionV1, websocket.MessageText, v1)
	if err != nil || f.Ref != "3" || f.JoinRef != "1" || f.Event != phxEventReply || f.Topic != "realtime:brigade:session:s" {
		t.Fatalf("v1 frame = %+v, %v", f, err)
	}
	v2 := []byte(`[null,null,"realtime:brigade:session:s","broadcast",{"type":"broadcast","event":"message_accepted","payload":{"message_id":"m"}}]`)
	f, err = decodePhxFrame(phxVersionV2, websocket.MessageText, v2)
	if err != nil || f.Ref != "" || !hintFor(f, "realtime:brigade:session:s") {
		t.Fatalf("v2 frame = %+v, %v", f, err)
	}
	topic, event, payload := "realtime:brigade:session:s", "message_accepted", `{"message_id":"m","seq":7}`
	binary := append([]byte{4, byte(len(topic)), byte(len(event)), 0, 1}, topic...) //nolint:gosec // G115: both lengths are far below 256
	binary = append(binary, event...)
	binary = append(binary, payload...)
	f, err = decodePhxFrame(phxVersionV2, websocket.MessageBinary, binary)
	if err != nil || !hintFor(f, topic) {
		t.Fatalf("binary frame = %+v, %v", f, err)
	}
	revoked := []byte(`{"topic":"realtime:brigade:session:s","event":"broadcast","payload":{"type":"broadcast","event":"membership_revoked","payload":{"session_id":"s"}},"ref":null}`)
	f, err = decodePhxFrame(phxVersionV1, websocket.MessageText, revoked)
	if err != nil || !hintFor(f, "realtime:brigade:session:s") {
		t.Fatalf("a membership_revoked broadcast on the session's topic is not a hint: %+v, %v", f, err)
	}
	if hintFor(f, "realtime:brigade:session:other") {
		t.Fatal("a broadcast on another topic is a hint")
	}
	if _, err := decodePhxFrame(phxVersionV2, websocket.MessageBinary, []byte{9, 0, 0, 0, 1}); err == nil {
		t.Fatal("an unknown binary kind decoded")
	}
	if _, err := decodePhxFrame(phxVersionV2, websocket.MessageText, []byte(`["only","four","members","here"]`)); err == nil {
		t.Fatal("a four-element array decoded")
	}
	numbered := []byte(`{"topic":"phoenix","event":"phx_reply","payload":{},"ref":12}`)
	if f, err = decodePhxFrame(phxVersionV1, websocket.MessageText, numbered); err != nil || f.Ref != "12" {
		t.Fatalf("numeric ref = %+v, %v", f, err)
	}
}

// TestClassifyReason pins plan 5.6's reason table and the fixed tokens.
func TestClassifyReason(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]string{
		"Unauthorized: You do not have permissions to read from this Channel topic: brigade:session:x": linkReasonUnauthorized,
		"RlsPolicyError":                  linkReasonUnauthorized,
		"JoinsRateLimitReached":           linkReasonRateLimited,
		"ConnectionRateLimitReached":      linkReasonRateLimited,
		"InvalidJWTToken":                 linkReasonBadToken,
		"Token has expired 5 seconds ago": linkReasonBadToken,
		"MalformedJWT":                    linkReasonBadToken,
		"PrivateOnly":                     linkReasonPrivateOnly,
		"RealtimeDisabledForTenant":       linkReasonDisabled,
		"unmatched topic":                 linkReasonUnmatchedTopic,
		"something new":                   linkReasonChannelError,
	} {
		if got := classifyReason(text); got != want {
			t.Errorf("classifyReason(%q) = %s, want %s", text, got, want)
		}
	}
}
