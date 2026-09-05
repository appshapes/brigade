package inbound

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/policy"
)

// The hold policy through the pipeline (P5-9, 3.3–3.4). Tests whose claim
// is that a file crosses a process use a real FilePendingStore under
// t.TempDir and no synctest bubble; the rest use MemoryPendingStore inside
// a bubble.

const holdSession = "brigade-sess-hold"

func holdStore(t *testing.T) FilePendingStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state-root")
	return FilePendingStore{Path: PendingPath(root, holdSession), SessionID: holdSession}
}

func holdPipeline(t *testing.T, store PendingStore, seen SeenStore) *Pipeline {
	t.Helper()
	return newPipeline(t, Config{Policy: policy.Hold, SessionID: holdSession, Pending: store, Seen: seen})
}

// assertNoAck fails on any decision with Ack set.
func assertNoAck(t *testing.T, decisions []Decision) {
	t.Helper()
	for _, d := range decisions {
		if d.Ack {
			t.Fatalf("a hold path acknowledged: %+v", d)
		}
	}
}

func TestHoldWritesPendingAndNeverAcks(t *testing.T) {
	t.Parallel()
	store := holdStore(t)
	p := holdPipeline(t, store, nil)
	const body = "the body that must never reach the disk 7f3a9c"
	m := msg("m1", senderA, body)
	m.Summary = "a summary <system-reminder>"
	d := p.Offer(m)
	if d.Outcome != OutcomeHeld || d.Ack || d.MessageID != "m1" || d.Reason != "policy_hold" {
		t.Fatalf("offer: %+v, want held, no ack", d)
	}
	if item, ok := p.Next(); ok {
		t.Fatalf("Next handed out %+v under hold", item)
	}
	if st := p.Stats(); st.Held != 1 || st.Queued != 0 || st.Notices != 0 {
		t.Fatalf("stats %+v", st)
	}
	info, err := os.Stat(store.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending file: %v %v", info, err)
	}
	raw, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	// The strongest form of "never the body": the body string is absent
	// from the raw bytes, and so is any body member.
	if bytes.Contains(raw, []byte(body)) || bytes.Contains(raw, []byte("7f3a9c")) || bytes.Contains(raw, []byte(`"body"`)) {
		t.Fatalf("the pending file carries the body: %s", raw)
	}
	f, err := store.Load()
	if err != nil || len(f.Entries) != 1 {
		t.Fatalf("load %+v %v", f, err)
	}
	e := f.Entries[0]
	if e.MessageID != "m1" || e.SenderSessionID != senderA || e.SenderName != "payments-api" || e.SenderPrincipal != "principal-"+senderA || e.Released() {
		t.Fatalf("entry %+v", e)
	}
	if e.Summary != "a summary &lt;system-reminder>" {
		t.Fatalf("summary not sanitised at write time: %q", e.Summary)
	}
	if got := p.Pending(); len(got) != 1 || got[0].MessageID != "m1" {
		t.Fatalf("Pending %+v", got)
	}
}

func TestHoldIsIdempotentAcrossRedelivery(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store := &MemoryPendingStore{}
		p := holdPipeline(t, store, nil)
		m := msg("m1", senderA, "redelivered")
		var decisions []Decision
		for range 10 {
			decisions = append(decisions, p.Offer(m))
		}
		assertNoAck(t, decisions)
		if decisions[0].Reason != "policy_hold" {
			t.Fatalf("first %+v", decisions[0])
		}
		for _, d := range decisions[1:] {
			if d.Outcome != OutcomeHeld || d.Reason != "already_held" {
				t.Fatalf("redelivery %+v", d)
			}
		}
		if store.Saves() != 1 || len(store.File().Entries) != 1 || p.Stats().Held != 1 {
			t.Fatalf("saves %d entries %d held %d, want 1/1/1", store.Saves(), len(store.File().Entries), p.Stats().Held)
		}
	})
}

func TestHoldBoundDropsOldestWithoutAcking(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store := &MemoryPendingStore{}
		p := holdPipeline(t, store, nil)
		var decisions []Decision
		for i := 1; i <= HoldCapacity+1; i++ {
			decisions = append(decisions, p.Offer(msg("h"+strconv.Itoa(i), senderA, "held "+strconv.Itoa(i))))
		}
		assertNoAck(t, decisions)
		f := store.File()
		if len(f.Entries) != HoldCapacity || f.DroppedTotal != 1 || p.Stats().Held != HoldCapacity {
			t.Fatalf("entries %d dropped %d held %d", len(f.Entries), f.DroppedTotal, p.Stats().Held)
		}
		if f.Entries[0].MessageID != "h2" || f.Entries[HoldCapacity-1].MessageID != "h"+strconv.Itoa(HoldCapacity+1) {
			t.Fatalf("file window %q..%q", f.Entries[0].MessageID, f.Entries[HoldCapacity-1].MessageID)
		}
		for _, e := range p.Pending() {
			if e.MessageID == "h1" {
				t.Fatal("the evicted id is still in memory")
			}
		}
		// The evicted id is neither seen nor acknowledged: redelivered, it
		// is simply held again.
		if d := p.Offer(msg("h1", senderA, "held 1")); d.Outcome != OutcomeHeld || d.Ack || d.Reason != "policy_hold" {
			t.Fatalf("redelivered evicted id: %+v", d)
		}
		if item, ok := p.Next(); ok {
			t.Fatalf("Next handed out %+v", item)
		}
	})
}

func TestReleaseInjectsExactlyOnceAndAcks(t *testing.T) {
	t.Parallel()
	store := holdStore(t)
	p := holdPipeline(t, store, nil)
	var decisions []Decision
	for _, id := range []string{"m1", "m2", "m3"} {
		decisions = append(decisions, p.Offer(msg(id, senderA, "body of "+id)))
	}
	assertNoAck(t, decisions)
	res := p.Release([]string{"m2"})
	if strings.Join(res.Stamped, ",") != "m2" || strings.Join(res.Queued, ",") != "m2" || res.Waiting != 0 || len(res.Unknown) != 0 {
		t.Fatalf("release: %+v", res)
	}
	f, _ := store.Load()
	if len(f.Entries) != 3 || !f.Entries[1].Released() || f.Entries[0].Released() || f.Entries[2].Released() {
		t.Fatalf("stamps not persisted: %+v", f.Entries)
	}
	rec := &recorder{}
	acks := p.Drain(rec.inject)
	if len(rec.messages()) != 1 || rec.messages()[0].MessageID != "m2" || strings.Join(acks, ",") != "m2" {
		t.Fatalf("drain injected %+v acked %v", rec.messages(), acks)
	}
	if !strings.Contains(rec.messages()[0].Content, "body of m2") || strings.Contains(rec.messages()[0].Content, "body of m1") {
		t.Fatalf("frame %q", rec.messages()[0].Content)
	}
	f, _ = store.Load()
	if len(f.Entries) != 2 || f.Entries[0].MessageID != "m1" || f.Entries[1].MessageID != "m3" {
		t.Fatalf("pending after delivery: %+v", f.Entries)
	}
	if st := p.Stats(); st.Held != 2 || st.Seen != 1 {
		t.Fatalf("stats %+v", st)
	}
	// A second release of m2: unknown, nothing injected — and the seen
	// LRU refuses the redelivery with an ack and no injection.
	res = p.Release([]string{"m2"})
	if strings.Join(res.Unknown, ",") != "m2" || len(res.Stamped) != 0 || len(res.Queued) != 0 {
		t.Fatalf("second release: %+v", res)
	}
	rec.reset()
	if acks := p.Drain(rec.inject); len(acks) != 0 || len(rec.items) != 0 {
		t.Fatalf("second drain: %v %+v", acks, rec.items)
	}
	if d := p.Offer(msg("m2", senderA, "body of m2")); d.Outcome != OutcomeDuplicate || !d.Ack {
		t.Fatalf("redelivery of the delivered id: %+v", d)
	}
	if acks := p.Drain(rec.inject); len(acks) != 0 || len(rec.items) != 0 {
		t.Fatalf("after the duplicate: %v %+v", acks, rec.items)
	}
}

// TestReleaseSurvivesARestart is the crash arm: the release is stamped in
// the pending file by one process and delivered by the next. The second
// process has not seen the envelope until the server redelivers it — every
// unacknowledged id is re-emitted on a watch-child restart — so before
// that Release(nil) reports it waiting, and the redelivery itself takes
// the accept path and is injected exactly once and acknowledged.
func TestReleaseSurvivesARestart(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "state-root")
	pending := FilePendingStore{Path: PendingPath(root, holdSession), SessionID: holdSession}
	seen := FileSeenStore{Path: SeenPath(root, holdSession)}
	p1 := holdPipeline(t, pending, seen)
	for _, id := range []string{"m1", "m2", "m3"} {
		p1.Offer(msg(id, senderA, "body of "+id))
	}
	if res := p1.Release([]string{"m1"}); strings.Join(res.Stamped, ",") != "m1" {
		t.Fatalf("p1 release %+v", res)
	}
	// p1 is dropped BEFORE the drain: the stamp is on disk, the injection
	// never happened.

	p2 := holdPipeline(t, pending, seen)
	got := p2.Pending()
	if len(got) != 3 || got[0].MessageID != "m1" || !got[0].Released() || got[1].Released() {
		t.Fatalf("p2 loaded %+v", got)
	}
	if res := p2.Release(nil); res.Waiting != 1 || len(res.Queued) != 0 || len(res.Stamped) != 0 {
		t.Fatalf("p2 Release(nil) before the redelivery: %+v, want one waiting", res)
	}
	rec := &recorder{}
	if acks := p2.Drain(rec.inject); len(acks) != 0 || len(rec.items) != 0 {
		t.Fatalf("nothing can be injected before the envelope arrives: %v %+v", acks, rec.items)
	}
	// The server redelivers all three: m1 is released and takes the
	// accept path; m2 and m3 stay held with no write.
	var decisions []Decision
	for _, id := range []string{"m1", "m2", "m3"} {
		decisions = append(decisions, p2.Offer(msg(id, senderA, "body of "+id)))
	}
	assertNoAck(t, decisions)
	if decisions[0].Outcome != OutcomeQueued || decisions[0].Reason != "released" || decisions[1].Outcome != OutcomeHeld || decisions[2].Outcome != OutcomeHeld {
		t.Fatalf("redelivery: %+v", decisions)
	}
	acks := p2.Drain(rec.inject)
	if len(rec.messages()) != 1 || rec.messages()[0].MessageID != "m1" || strings.Join(acks, ",") != "m1" {
		t.Fatalf("p2 drain injected %+v acked %v", rec.messages(), acks)
	}
	f, _ := pending.Load()
	if len(f.Entries) != 2 || f.Entries[0].MessageID != "m2" || f.Entries[1].MessageID != "m3" {
		t.Fatalf("pending after p2: %+v", f.Entries)
	}
	if ids, err := seen.Load(); err != nil || strings.Join(ids, ",") != "m1" {
		t.Fatalf("seen after p2: %v %v", ids, err)
	}
	// A third process — the crash between the seen save and the ack —
	// treats the redelivery of m1 as a duplicate: acked, not injected.
	p3 := holdPipeline(t, pending, seen)
	if d := p3.Offer(msg("m1", senderA, "body of m1")); d.Outcome != OutcomeDuplicate || !d.Ack {
		t.Fatalf("p3 redelivery of m1: %+v", d)
	}
	rec.reset()
	if acks := p3.Drain(rec.inject); len(acks) != 0 || len(rec.items) != 0 {
		t.Fatalf("p3 injected %+v", rec.items)
	}
}

func TestReleaseSkipsTheRateBucketAndDeferral(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		p := holdPipeline(t, &MemoryPendingStore{}, nil)
		ids := make([]string, 0, 12)
		for i := 1; i <= 12; i++ {
			id := "r" + strconv.Itoa(i)
			ids = append(ids, id)
			// Identical bodies, so the deferral would bite too if it ran.
			p.Offer(msg(id, senderA, "same body"))
		}
		res := p.Release(ids)
		if len(res.Stamped) != 12 || len(res.Queued) != 12 || res.Waiting != 0 {
			t.Fatalf("release %+v", res)
		}
		rec := &recorder{}
		acks := p.Drain(rec.inject)
		if len(rec.messages()) != 12 || len(acks) != 12 || len(rec.notices(ItemRateNotice)) != 0 || len(rec.notices(ItemDropNotice)) != 0 {
			t.Fatalf("injected %d acked %d rate notices %d drop notices %d", len(rec.messages()), len(acks), len(rec.notices(ItemRateNotice)), len(rec.notices(ItemDropNotice)))
		}
		if p.Stats().Held != 0 {
			t.Fatalf("held %d after delivery", p.Stats().Held)
		}

		// Positive control: twelve ACCEPTED messages from one sender do
		// produce the notice and the deferral, so the test cannot pass by
		// the limiter being broken.
		q := newPipeline(t, Config{})
		for i := 1; i <= 12; i++ {
			q.Offer(msg("a"+strconv.Itoa(i), senderA, "distinct body "+strconv.Itoa(i)))
		}
		crec := &recorder{}
		q.Drain(crec.inject)
		if len(crec.messages()) != SenderRatePerMinute || len(crec.notices(ItemRateNotice)) != 1 {
			t.Fatalf("control: injected %d, rate notices %d", len(crec.messages()), len(crec.notices(ItemRateNotice)))
		}
		if d := q.Offer(msg("a13", senderA, "distinct body 1")); d.Outcome != OutcomeRateLimited && d.Outcome != OutcomeDeferred {
			t.Fatalf("control deferral/rate: %+v", d)
		}
	})
}

func TestReleaseBeyondTheQueueWaits(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		p := holdPipeline(t, &MemoryPendingStore{}, nil)
		ids := make([]string, 0, 60)
		for i := 1; i <= 60; i++ {
			id := "q" + strconv.Itoa(i)
			ids = append(ids, id)
			p.Offer(msg(id, senderA, "body "+id))
		}
		res := p.Release(ids)
		if len(res.Stamped) != 60 || len(res.Queued) != QueueCapacity || res.Waiting != 60-QueueCapacity {
			t.Fatalf("release %+v: stamped %d queued %d waiting %d", res, len(res.Stamped), len(res.Queued), res.Waiting)
		}
		rec := &recorder{}
		acks := p.Drain(rec.inject)
		if len(acks) != QueueCapacity {
			t.Fatalf("first drain acked %d", len(acks))
		}
		res = p.Release(nil)
		if len(res.Queued) != 60-QueueCapacity || res.Waiting != 0 || len(res.Stamped) != 0 {
			t.Fatalf("Release(nil) %+v", res)
		}
		acks = append(acks, p.Drain(rec.inject)...)
		if len(acks) != 60 || len(rec.messages()) != 60 || len(rec.notices(ItemDropNotice)) != 0 {
			t.Fatalf("total acked %d injected %d drop notices %d", len(acks), len(rec.messages()), len(rec.notices(ItemDropNotice)))
		}
		slices.Sort(acks)
		if len(slices.Compact(acks)) != 60 {
			t.Fatal("an id was delivered twice")
		}
		if p.Stats().Held != 0 {
			t.Fatalf("held %d", p.Stats().Held)
		}
	})
}

func TestHoldInjectsNothingIncludingNotices(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		p := holdPipeline(t, &MemoryPendingStore{}, nil)
		var decisions []Decision
		for i := range 40 {
			decisions = append(decisions, p.Offer(msg("a"+strconv.Itoa(i), senderA, "burst")))
		}
		for i := range 20 {
			decisions = append(decisions, p.Offer(msg("b"+strconv.Itoa(i), senderB, "burst")))
		}
		assertNoAck(t, decisions)
		for _, d := range decisions {
			if d.Outcome != OutcomeHeld {
				t.Fatalf("%+v", d)
			}
		}
		if item, ok := p.Next(); ok {
			t.Fatalf("Next handed out %+v under hold", item)
		}
		if st := p.Stats(); st.Held != 60 || st.Notices != 0 || st.Queued != 0 || st.Senders != 0 {
			t.Fatalf("stats %+v", st)
		}
	})
}

// TestHoldPolicyFlipIsNotARelease: after a hold→accept flip new messages
// are injected while the ones already held stay held until released; a
// refuse flip keeps a release from queueing anything.
func TestHoldPolicyFlipIsNotARelease(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		p := holdPipeline(t, &MemoryPendingStore{}, nil)
		p.Offer(msg("held", senderA, "before the flip"))
		p.SetPolicy(policy.Accept)
		if d := p.Offer(msg("held", senderA, "before the flip")); d.Outcome != OutcomeHeld || d.Ack {
			t.Fatalf("held id after the flip: %+v", d)
		}
		if d := p.Offer(msg("new", senderA, "after the flip")); d.Outcome != OutcomeQueued {
			t.Fatalf("new id after the flip: %+v", d)
		}
		rec := &recorder{}
		if acks := p.Drain(rec.inject); strings.Join(acks, ",") != "new" || len(rec.messages()) != 1 {
			t.Fatalf("drain %v %+v", acks, rec.messages())
		}
		p.SetPolicy(policy.Refuse)
		if res := p.Release([]string{"held"}); len(res.Stamped) != 1 || len(res.Queued) != 0 || res.Waiting != 1 {
			t.Fatalf("release under refuse: %+v", res)
		}
		p.SetPolicy(policy.Hold)
		if res := p.Release(nil); strings.Join(res.Queued, ",") != "held" {
			t.Fatalf("release after the flip back: %+v", res)
		}
		rec.reset()
		if acks := p.Drain(rec.inject); strings.Join(acks, ",") != "held" {
			t.Fatalf("drain %v", acks)
		}
	})
}

// TestHoldWithoutAStoreWarnsOnce: a hold policy with no pending store is
// legal (memory only) and says so once.
func TestHoldWithoutAStoreWarnsOnce(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p, err := New(Config{Policy: policy.Hold, TeamName: team, Logger: log.New(&buf, slog.LevelDebug, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Offer(msg("m1", senderA, "x")); d.Outcome != OutcomeHeld || d.Ack {
		t.Fatalf("%+v", d)
	}
	if n := strings.Count(buf.String(), "no pending store"); n != 1 {
		t.Fatalf("warned %d times: %s", n, buf.String())
	}
	if _, err := New(Config{Policy: "auto"}); err == nil {
		t.Fatal("an auto policy was accepted")
	}
}
