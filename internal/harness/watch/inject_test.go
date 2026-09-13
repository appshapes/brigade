package watch_test

import (
	"context"
	"encoding/json/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/socketpost"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestSameIDThreeTimesInjectsOnce is U-13's first half: the fake adapter
// replays one message id three times; the frame is posted once and
// acknowledged once, and neither repeat is injected. A repeat is judged at
// arrival: `duplicate` (acked) once the first copy's Done has moved the id
// into the seen set, `pending` (ignored, not acked) while the first copy
// is still being posted — Done runs after the socket post and the seen
// file's two fsyncs, measured 7–85 ms on an 18-CPU host at load average
// 17 (2026-09-11) against the script's 100 ms replay gap, which the macOS
// CI job lost twice that day by requiring two `duplicate` lines. Either
// verdict is correct and neither injects, so the test accepts both and
// pins what holds in every interleaving; the post-ack duplicate+ack path
// stays pinned deterministically by TestRestartBeforeAckInjectsOnceAndAcks
// (seen file loaded before any offer) and by the pipeline's own
// TestSameIDThreeTimesInjectsOnceAcksEachTime.
func TestSameIDThreeTimesInjectsOnce(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	m := fakeMessage("m1", "sender-a", "three times")
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{
		readyLine(t), messageLine(t, m, 0), messageLine(t, m, 100), messageLine(t, m, 100),
	}}})
	fx.writeMap()
	r := fx.start(fx.deps())
	fx.sock.WaitFrames(1, waitShort)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("message offered", map[string]any{"message_id": "m1"}) >= 3 &&
			fx.logCount("injection reported", map[string]any{"message_id": "m1", "outcome": "injected", "ack": true}) == 1
	})
	fx.waitLog("ack sent", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := len(fx.sock.Frames()); n != 1 {
		t.Fatalf("frames = %d, want 1", n)
	}
	queued, repeats := 0, 0
	for _, l := range offersOf(fx, "m1") {
		switch l.outcome {
		case "queued":
			queued++
		case "pending", "duplicate":
			repeats++
		default:
			t.Errorf("offer outcome %q", l.outcome)
		}
		if l.ack != (l.outcome == "duplicate") {
			t.Errorf("offer %q acked=%v: a duplicate is acked, nothing else is", l.outcome, l.ack)
		}
	}
	if queued != 1 || repeats != 2 {
		t.Errorf("offers of m1: %d queued, %d repeats, want 1 and 2", queued, repeats)
	}
}

// An offer is one `message offered` log line's verdict.
type offer struct {
	outcome string
	ack     bool
}

// offersOf lists the offers of id in log order.
func offersOf(fx *fixture, id string) []offer {
	var out []offer
	for _, l := range fx.logLines() {
		if l["msg"] != "message offered" || l["message_id"] != id {
			continue
		}
		outcome, _ := l["outcome"].(string)
		ack, _ := l["ack"].(bool)
		out = append(out, offer{outcome: outcome, ack: ack})
	}
	return out
}

// TestRestartBeforeAckInjectsOnceAndAcks is U-13's second half: a seen
// file left by a watcher that injected but died before its ack makes the
// next watcher acknowledge the redelivered id WITHOUT injecting it again
// (the fs adapter re-emits the unacked message); the control without the
// seen file injects it once and acks.
func TestRestartBeforeAckInjectsOnceAndAcks(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, preSeen bool) (*fixture, string) {
		t.Helper()
		fx := newFixture(t, fixtureOptions{})
		fx.useFS()
		fx.writeMap()
		id := fx.send("injected before the crash")
		if preSeen {
			store := inbound.FileSeenStore{Path: fx.seenPath()}
			if err := store.Save([]string{id}); err != nil {
				t.Fatal(err)
			}
		}
		r := fx.start(fx.deps())
		testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.ackedIDs()) == 1 })
		if code := r.stopAndWait(); code != 0 {
			t.Fatalf("exit %d", code)
		}
		return fx, id
	}
	fx, id := run(t, true)
	if n := fx.sock.Accepted(); n != 0 {
		t.Errorf("seen id was posted: %d connections", n)
	}
	if got := fx.ackedIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("acked = %v, want [%s]", got, id)
	}
	if !fx.logHas("message offered", map[string]any{"message_id": id, "outcome": "duplicate", "ack": true}) {
		t.Errorf("log: %v", fx.logLines())
	}
	fx, id = run(t, false)
	if frames := fx.sock.Frames(); len(frames) != 1 || !strings.Contains(frames[0], "injected before the crash") {
		t.Errorf("control frames = %q", frames)
	}
	if got := fx.ackedIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("control acked = %v", got)
	}
	// The seen file now holds the id, 0600, for the next restart.
	if info, err := os.Stat(fx.seenPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("seen file: %v %v", info, err)
	}
	ids, err := inbound.FileSeenStore{Path: fx.seenPath()}.Load()
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Errorf("seen ids = %v %v", ids, err)
	}
}

// TestRefuseNeverPostsOrAcks (D18, 6.8 item 4): under `refuse` the socket
// is never dialled, the message stays unacked in the store, and the
// watcher still heartbeats and closes cleanly. The liveness poll (which
// re-reads the map's policy) is pushed out of the test's window so the
// policy in force is the one the watcher took from the map at START — a
// watcher that started under `accept` and only corrected itself at its
// first poll (2 s in production) would inject; TestInboundPolicyFollowsTheMap
// covers the re-read.
func TestRefuseNeverPostsOrAcks(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{inbound: protocol.InboundRefuse})
	fx.useFS()
	fx.writeMap()
	deps := fx.deps()
	deps.HeartbeatInterval = 200 * time.Millisecond
	deps.PollInterval = time.Hour
	r := fx.start(deps)
	fx.waitLog("watch ready", nil)
	id := fx.send("refused")
	fx.waitLog("message offered", map[string]any{"message_id": id, "outcome": "refused"})
	first := fx.session().LastSeenAt
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return fx.session().LastSeenAt.After(first) })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := fx.sock.Accepted(); n != 0 {
		t.Errorf("connections = %d, want 0 under refuse", n)
	}
	if got := fx.inboxIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("inbox = %v, want [%s]", got, id)
	}
	if got := fx.ackedIDs(); len(got) != 0 {
		t.Errorf("acked = %v, want none", got)
	}
	if s := fx.session(); s.ClosedAt == nil || s.Inbound != "refuse" {
		t.Errorf("session = %+v", s)
	}
}

// TestInboundPolicyFollowsTheMap (6.6: the map is re-read every poll): a
// watcher started under `refuse` whose map is rewritten to `accept` starts
// injecting and acknowledging; rewritten back to `refuse`, the next message
// is refused again and stays unacked in the store.
func TestInboundPolicyFollowsTheMap(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{inbound: protocol.InboundRefuse})
	fx.useFS()
	fx.writeMap()
	r := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	held := fx.send("held under refuse")
	fx.waitLog("message offered", map[string]any{"message_id": held, "outcome": "refused"})

	fx.inbound = protocol.InboundAccept
	fx.writeMap()
	fx.waitLog("inbound policy changed", map[string]any{"inbound": "accept"})
	// The fs adapter re-emits an unacknowledged message only on a watch
	// restart, so a fresh message proves the flip; the held one stays.
	open := fx.send("after the flip")
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], "after the flip") {
		t.Fatalf("frames = %q", frames)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.ackedIDs()) == 1 })

	fx.inbound = protocol.InboundRefuse
	fx.writeMap()
	fx.waitLog("inbound policy changed", map[string]any{"inbound": "refuse"})
	again := fx.send("refused again")
	fx.waitLog("message offered", map[string]any{"message_id": again, "outcome": "refused"})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := len(fx.sock.Frames()); n != 1 {
		t.Errorf("frames = %d, want exactly the one sent under accept", n)
	}
	if got := fx.ackedIDs(); len(got) != 1 || got[0] != open {
		t.Errorf("acked = %v, want [%s]", got, open)
	}
	inbox := fx.inboxIDs()
	if len(inbox) != 2 || !slices.Contains(inbox, held) || !slices.Contains(inbox, again) {
		t.Errorf("inbox = %v, want the two refused ids %s and %s", inbox, held, again)
	}
}

// TestPerSenderRateLimit is U-14's watch half: twelve messages from one
// sender in a burst inject ten frames and one rate notice; the two held
// back are neither injected nor acknowledged.
func TestPerSenderRateLimit(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	lines := []fakeadapter.WatchLine{readyLine(t)}
	for i := range 12 {
		lines = append(lines, messageLine(t, fakeMessage("m"+strconv.Itoa(i), "sender-a", "burst "+strconv.Itoa(i)), 0))
	}
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	fx.writeMap()
	r := fx.start(fx.deps())
	fx.sock.WaitFrames(11, waitShort)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	frames := fx.sock.Frames()
	messages, notices := 0, 0
	for _, f := range frames {
		// The notice is rendered when the injector reaches it, with the
		// count held back so far: 1 or 2 depending on the interleaving.
		if (strings.HasPrefix(f, "Brigade: 1 message from ") || strings.HasPrefix(f, "Brigade: 2 messages from ")) &&
			strings.Contains(f, "held back for rate limiting") {
			notices++
		} else if strings.Contains(f, frame.OpenTag) {
			messages++
		}
	}
	if messages != 10 || notices != 1 || len(frames) != 11 {
		t.Fatalf("frames: %d messages, %d notices of %d: %q", messages, notices, len(frames), frames)
	}
	if n := fx.logCount("message offered", map[string]any{"outcome": "rate_limited"}); n != 2 {
		t.Errorf("rate_limited outcomes = %d, want 2", n)
	}
	if n := fx.logCount("injection reported", map[string]any{"kind": "message", "ack": true}); n != 10 {
		t.Errorf("acked injections = %d, want 10", n)
	}
}

// TestIdenticalBodyDeferredThenInjected (6.8 item 6): the same body from
// the same sender within 60 s is neither injected nor acknowledged; once
// the injected clock passes the window the redelivery is injected once,
// and no later redelivery is injected — each is `pending` (not acked)
// while that injection is in flight or `duplicate` (acked) after it, the
// same arrival-time verdict TestSameIDThreeTimesInjectsOnce explains, so
// the count of either is not asserted. Nor is the number of deferrals
// before the clock moves: the sender's rate window is checked first and
// each deferred redelivery spends one of its 10 accepts, so a test
// goroutine stalled ~1.4 s before the bump would see the tenth
// redelivery `rate_limited` (not acked) and a rate notice frame — both
// allowed, and the message frames are counted apart from notices.
func TestIdenticalBodyDeferredThenInjected(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	m1 := fakeMessage("m1", "sender-a", "same body")
	m2 := fakeMessage("m2", "sender-a", "same body")
	lines := []fakeadapter.WatchLine{readyLine(t), messageLine(t, m1, 0), messageLine(t, m2, 0)}
	for range 40 {
		lines = append(lines, messageLine(t, m2, 150))
	}
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	fx.writeMap()
	var offset atomic.Int64
	deps := fx.deps()
	deps.Clock = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	r := fx.start(deps)
	fx.sock.WaitFrames(1, waitShort)
	fx.waitLog("message offered", map[string]any{"message_id": "m2", "outcome": "deferred"})
	if n := len(messageFrames(fx.sock.Frames())); n != 1 {
		t.Fatalf("message frames before the window passed = %d, want 1", n)
	}
	if fx.logHas("injection reported", map[string]any{"message_id": "m2"}) {
		t.Fatalf("m2 was injected inside the window")
	}
	offset.Store(int64(inbound.DeferralWindow + time.Second))
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(messageFrames(fx.sock.Frames())) == 2 })
	if frames := messageFrames(fx.sock.Frames()); !strings.Contains(frames[1], "same body") {
		t.Fatalf("frames = %q", frames)
	}
	// Let the replay run out (m2 once, then its 40 redeliveries) before
	// judging it, so every redelivery is on record.
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("message offered", map[string]any{"message_id": "m2"}) >= 41
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := len(messageFrames(fx.sock.Frames())); n != 2 {
		t.Fatalf("message frames at the end = %d, want 2", n)
	}
	if n := fx.logCount("injection reported", map[string]any{"message_id": "m2"}); n != 1 {
		t.Errorf("injections of m2 = %d, want 1", n)
	}
	// In log order: deferred (or rate-limited) while the window held, the
	// one queued once the clock passed it, then only pending or duplicate —
	// acked exactly when duplicate.
	queued := false
	for i, l := range offersOf(fx, "m2") {
		switch {
		case (l.outcome == "deferred" || l.outcome == "rate_limited") && !queued:
		case l.outcome == "queued" && !queued:
			queued = true
		case (l.outcome == "pending" || l.outcome == "duplicate") && queued:
		default:
			t.Errorf("offer %d of m2: %q (queued yet: %v)", i, l.outcome, queued)
		}
		if l.ack != (l.outcome == "duplicate") {
			t.Errorf("offer %d of m2 %q acked=%v", i, l.outcome, l.ack)
		}
	}
	if !queued {
		t.Errorf("m2 was never queued: %v", offersOf(fx, "m2"))
	}
}

// messageFrames keeps the frames that carry a message (a notice has no
// open tag).
func messageFrames(frames []string) []string {
	var out []string
	for _, f := range frames {
		if strings.Contains(f, frame.OpenTag) {
			out = append(out, f)
		}
	}
	return out
}

// TestBurstStaysBoundedWithOneNotice is U-15's watch half: 10,000
// messages from 10,000 senders replayed at once; the queue drops the
// oldest beyond 50, exactly one drop notice is injected, the notice is
// never acknowledged, and the pipeline's sizes at exit are bounded.
func TestBurstStaysBoundedWithOneNotice(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	lines := make([]fakeadapter.WatchLine, 0, 10_001)
	lines = append(lines, readyLine(t))
	for i := range 10_000 {
		lines = append(lines, messageLine(t, fakeMessage("m"+strconv.Itoa(i), "s"+strconv.Itoa(i), "burst"), 0))
	}
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	fx.writeMap()
	r := fx.start(fx.deps(), fx.args()...)
	// The whole burst is offered while the injector drains at the seen
	// file's fsync pace (one atomic write per injection, P3-2's design),
	// so the test does not wait for all 10,000 offers: once the queue has
	// overflowed and the notice is out, the bound is what matters.
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		recs := fx.sinkRecords()
		for _, rec := range recs {
			if rec.MessageID == "" {
				return len(recs) >= 60
			}
		}
		return false
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	recs := fx.sinkRecords()
	notices, messages := 0, 0
	for _, rec := range recs {
		if rec.MessageID == "" {
			notices++
			if !strings.HasPrefix(rec.Frame, "Brigade: ") || !strings.Contains(rec.Frame, "dropped from the injection queue (limit 50)") {
				t.Errorf("notice frame = %q", rec.Frame)
			}
		} else {
			messages++
		}
	}
	if notices != 1 {
		t.Errorf("drop notices = %d, want exactly 1", notices)
	}
	if messages >= 10_000 || messages == 0 {
		t.Errorf("messages injected = %d, want some but not all", messages)
	}
	offered := fx.logCount("message offered", nil)
	dropped := fx.logCount("message offered", map[string]any{"outcome": "queued"}) - messages
	t.Logf("burst: %d offered, %d injected, %d dropped or still queued, %d notice(s)", offered, messages, dropped, notices)
	for _, l := range fx.logLines() {
		if l["msg"] != "watcher exiting" {
			continue
		}
		if q, _ := l["queued"].(float64); q > inbound.QueueCapacity {
			t.Errorf("queued at exit = %v, over the cap", l["queued"])
		}
		if s, _ := l["senders"].(float64); s > inbound.MaxSenders {
			t.Errorf("senders at exit = %v, over the cap", l["senders"])
		}
	}
	if fx.logHas("injection reported", map[string]any{"kind": "drop_notice", "ack": true}) {
		t.Errorf("a notice was acknowledged")
	}
}

// TestStalledSocketNoAck is U-20's stall row: a socket that accepts and
// never reads. A unix-socket write returns once the kernel buffered it
// (P3-2 measured 8 KiB on darwin, ~200 KiB on linux), so a frame never
// fills the buffer by itself; the write deadline is what a stall trips,
// and it is injected here at 1 ns so the row is deterministic on both
// platforms. The post fails, nothing is acknowledged, the message stays in
// the store; the control (a reading socket, the default deadline) is
// every other fs test.
func TestStalledSocketNoAck(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	fx.sock.Stall(true)
	deps := fx.deps()
	deps.PostOptions = socketpost.Options{WriteTimeout: time.Nanosecond}
	r := fx.start(deps)
	fx.waitLog("watch ready", nil)
	id := fx.send("nobody reads")
	fx.waitLog("injection failed; backing off", map[string]any{"message_id": id})
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return fx.logCount("heartbeat", nil) >= 2 })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if fx.sock.Accepted() < 1 {
		t.Errorf("the stalled socket was never dialled")
	}
	if n := len(fx.sock.Frames()); n != 0 {
		t.Errorf("frames = %d, want 0", n)
	}
	if got := fx.ackedIDs(); len(got) != 0 {
		t.Errorf("acked = %v, want none", got)
	}
	if got := fx.inboxIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("inbox = %v, want [%s]", got, id)
	}
	if !fx.logHas("injection reported", map[string]any{"message_id": id, "outcome": "not_injected", "ack": false}) {
		t.Errorf("log: %v", fx.logLines())
	}
}

// TestSocketGoneReReadsRegistry is U-20's ENOENT row: the hook-time
// socket path does not exist; the registry names the live socket; the
// watcher re-reads the registry and the frame reaches the new path.
func TestSocketGoneReReadsRegistry(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	m := fakeMessage("m1", "sender-a", "moved socket")
	lines := []fakeadapter.WatchLine{readyLine(t), messageLine(t, m, 0)}
	for range 30 {
		lines = append(lines, messageLine(t, m, 100))
	}
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	fx.writeMap()
	gone := fx.sock.Dir() + "/gone.sock"
	fx.writeRegistry("receiver", "idle", fx.sock.Path())
	fx.extraEnv = []string{"CLAUDE_CODE_MESSAGING_SOCKET=" + gone} // the last occurrence wins
	r := fx.start(fx.deps())
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], "moved socket") {
		t.Fatalf("frames = %q", frames)
	}
	if !fx.logHas("socket path updated from the registry", map[string]any{"socket_path": fx.sock.Path()}) {
		t.Errorf("log: %v", fx.logLines())
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestPrecheckRefusalNeverDials is U-19's watch half: a socket whose mode
// grants group or other access is refused before any dial; nothing is
// acknowledged.
func TestPrecheckRefusalNeverDials(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	if err := os.Chmod(fx.sock.Path(), 0o644); err != nil { //nolint:gosec // G302: the insecure mode IS the precondition
		t.Fatal(err)
	}
	r := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	id := fx.send("wrong mode")
	fx.waitLog("injection failed; backing off", map[string]any{"message_id": id})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := fx.sock.Accepted(); n != 0 {
		t.Errorf("connections = %d, want 0 after a pre-check refusal", n)
	}
	if got := fx.ackedIDs(); len(got) != 0 {
		t.Errorf("acked = %v", got)
	}
}

// TestSinkRecordAndFrameShape: sink mode writes one NDJSON record per
// injection with ts, the variant-C frame, message_id and
// sender_session_id; the frame parses with Brigade's own parser and names
// the sender's ids in full. The socket mode control checks the auth line
// and the wrapper on the fakesock.
func TestSinkRecordAndFrameShape(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	m := fakeMessage("m-abc", "sender-xyz", "the body\nsecond line")
	m.Summary = "A summary"
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t), messageLine(t, m, 0)}}})
	fx.writeMap()
	r := fx.start(fx.deps(), fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.sinkRecords()) == 1 })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	recs := fx.sinkRecords()
	rec := recs[0]
	if rec.MessageID != "m-abc" || rec.SenderSessionID != "sender-xyz" || rec.TS.IsZero() {
		t.Errorf("record = %+v", rec)
	}
	p, err := frame.Parse(rec.Frame)
	if err != nil {
		t.Fatalf("frame does not parse: %v\n%s", err, rec.Frame)
	}
	if !p.Wrapped || p.WrapperFromName != "peer sender-xyz" || p.MessageID != "m-abc" || p.ReplyToSessionID != "sender-xyz" || p.Team != "ops" {
		t.Errorf("parsed = %+v", p)
	}
	if p.Summary != "A summary" || !strings.Contains(p.Body, "second line") {
		t.Errorf("summary/body = %q / %q", p.Summary, p.Body)
	}
	if info, err := os.Stat(fx.sink); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("sink mode: %v %v", info, err)
	}
	// Every record is one physical line of JSON.
	data, _ := os.ReadFile(fx.sink)
	if strings.Count(string(data), "\n") != 1 {
		t.Errorf("sink has %d lines for one record", strings.Count(string(data), "\n"))
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 4 {
		t.Errorf("record members = %v (%v)", raw, err)
	}
}

// TestSocketFrameCarriesAuthAndWrapper: in socket mode each post is one
// connection with the auth line (the token) then the wrapped frame.
func TestSocketFrameCarriesAuthAndWrapper(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	r := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	fx.send("over the socket")
	frames := fx.sock.WaitFrames(1, waitShort)
	fx.sock.WaitDone(1, waitShort)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	conns := fx.sock.Connections()
	if len(conns) != 1 || len(conns[0].Auth) != 1 || conns[0].Auth[0] != fx.token || len(conns[0].Invalid) != 0 || conns[0].Lines != 2 {
		t.Fatalf("connection = %+v", conns)
	}
	p, err := frame.Parse(frames[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.Wrapped || p.WrapperFromName != "sender" || p.ReplyToSessionID != fx.senderSessionID || p.Team != fx.teamName {
		t.Errorf("parsed = %+v", p)
	}
	if strings.Contains(frames[0], fx.token) {
		t.Errorf("the token is inside the frame")
	}
}

// TestInjectionFailureIsRedactedInTheLog: an error text carrying the
// token (a hostile transport error) reaches the log redacted; the token
// grep at cleanup is the file-level assertion, this is the redactor's.
func TestInjectionFailureIsRedactedInTheLog(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{
		readyLine(t), messageLine(t, fakeMessage("m1", "sender-a", "fail me"), 0),
	}}})
	fx.writeMap()
	var calls atomic.Int32
	var mu sync.Mutex
	var seenTarget socketpost.Target
	deps := fx.deps()
	deps.Post = func(_ context.Context, target socketpost.Target, _ string, _ socketpost.Options) error {
		calls.Add(1)
		mu.Lock()
		seenTarget = target
		mu.Unlock()
		return &socketpost.Error{Kind: socketpost.KindWrite, Reason: "write", Err: errWithText("transport said " + target.Token)}
	}
	r := fx.start(deps)
	fx.waitLog("injection failed; backing off", map[string]any{"message_id": "m1"})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	mu.Lock()
	target := seenTarget
	mu.Unlock()
	if target.Token != fx.token || target.Path != fx.sock.Path() {
		t.Errorf("the poster got %v, want the socket and the token", target)
	}
	data, _ := os.ReadFile(fx.logPath())
	if strings.Contains(string(data), fx.token) {
		t.Errorf("the token reached the log")
	}
	if !strings.Contains(string(data), "transport said [redacted]") {
		t.Errorf("the redaction marker is missing from the log")
	}
	if calls.Load() < 1 {
		t.Errorf("the injected poster was never called")
	}
}

type errWithText string

func (e errWithText) Error() string { return string(e) }

// TestTokenGrepBites is the positive control of the cleanup grep: a file
// carrying the token under the fixture root is found.
func TestTokenGrepBites(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/planted", []byte("x "+fx.token+" y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := tokenHits(t, fx.token, dir); len(hits) != 1 {
		t.Fatalf("hits = %v, want the planted file", hits)
	}
	if hits := tokenHits(t, fx.token, fx.dirs.Root); len(hits) != 0 {
		t.Fatalf("hits in an untouched fixture = %v", hits)
	}
}
