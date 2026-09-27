package watch

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/notify"
	"github.com/appshapes/brigade/internal/harness/socketpost"
)

// TestArrivedNow pins the arrival rule over every decision Offer returns
// (cards 35 and 36): a fresh queued message and a newly held one count; a
// released message a redelivery re-offers, a redelivered held id, a
// duplicate, a pending re-offer, a refusal, a rate limit, a deferral and a
// rejection do not.
func TestArrivedNow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		d    inbound.Decision
		want bool
	}{
		{"queued", inbound.Decision{Outcome: inbound.OutcomeQueued, MessageID: "m"}, true},
		{"queued with a drop", inbound.Decision{Outcome: inbound.OutcomeQueued, MessageID: "m", Dropped: "old"}, true},
		{"released, re-offered by a redelivery", inbound.Decision{Outcome: inbound.OutcomeQueued, MessageID: "m", Reason: "released"}, false},
		{"newly held", inbound.Decision{Outcome: inbound.OutcomeHeld, MessageID: "m", Reason: "policy_hold"}, true},
		{"held id redelivered", inbound.Decision{Outcome: inbound.OutcomeHeld, MessageID: "m", Reason: "already_held"}, false},
		{"duplicate", inbound.Decision{Outcome: inbound.OutcomeDuplicate, MessageID: "m", Ack: true, Reason: "seen"}, false},
		{"pending", inbound.Decision{Outcome: inbound.OutcomePending, MessageID: "m", Reason: "pending"}, false},
		{"refused", inbound.Decision{Outcome: inbound.OutcomeRefused, MessageID: "m", Reason: "policy_refuse"}, false},
		{"rate limited", inbound.Decision{Outcome: inbound.OutcomeRateLimited, MessageID: "m", Reason: "sender_rate"}, false},
		{"deferred", inbound.Decision{Outcome: inbound.OutcomeDeferred, MessageID: "m", Reason: "identical_body"}, false},
		{"rejected", inbound.Decision{Outcome: inbound.OutcomeRejected, Reason: "body_too_long"}, false},
	}
	for _, c := range cases {
		if got := arrivedNow(c.d); got != c.want {
			t.Errorf("%s: arrivedNow = %v, want %v", c.name, got, c.want)
		}
	}
}

// gatedProgram is a Deps.Announce that blocks until released (or its
// context ends), counting runs and keeping each run's context and argv.
type gatedProgram struct {
	mu    sync.Mutex
	runs  int
	gate  chan struct{}
	ctxs  []context.Context
	argvs [][]string
}

func (g *gatedProgram) run(ctx context.Context, argv, _ []string) error {
	g.mu.Lock()
	g.runs++
	g.ctxs = append(g.ctxs, ctx)
	g.argvs = append(g.argvs, slices.Clone(argv))
	g.mu.Unlock()
	select {
	case <-g.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gatedProgram) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.runs
}

func (g *gatedProgram) argv(i int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.argvs[i])
}

// settableClock is a Deps.Clock a test moves by hand.
type settableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// pastInterval moves the clock just past notify.DefaultInterval.
func (c *settableClock) pastInterval() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(notify.DefaultInterval + time.Second)
}

// testWatcher is the least watcher an announcer needs; its PATH holds no
// program, so a nil command seam resolves to nothing.
func testWatcher(g *gatedProgram, clk *settableClock, sound, banner []string) *watcher {
	return &watcher{
		deps:      Deps{Clock: clk.Now, Announce: g.run, SoundCommand: sound, BannerCommand: banner},
		log:       slog.New(slog.DiscardHandler),
		environ:   []string{"PATH=/nonexistent/bin"},
		sessionID: "sess-1",
		state:     newShared(socketpost.Target{}, "brigade-8d", "accept", "", "", "", ""),
	}
}

func eventuallyRuns(t *testing.T, g *gatedProgram, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for g.count() != n {
		if time.Now().After(deadline) {
			t.Fatalf("runs = %d, want %d", g.count(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAnnouncerCoalescesAndNeverOverlaps pins the two guards with the
// clock in hand (card 35): a second arrival inside notify.DefaultInterval
// runs nothing, an arrival while a run is still in progress runs nothing
// however far the clock moved, and an arrival after both — the interval
// passed, the run returned — runs again. Off, or on with no program on
// PATH, runs nothing at all.
func TestAnnouncerCoalescesAndNeverOverlaps(t *testing.T) {
	t.Parallel()
	g := &gatedProgram{gate: make(chan struct{})}
	clk := &settableClock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	w := testWatcher(g, clk, []string{"/nonexistent/player"}, nil)
	s := newSoundAnnouncer(w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.bind(ctx)

	s.arrived()
	if n := g.count(); n != 0 {
		t.Fatalf("runs = %d before the option is on, want 0", n)
	}
	s.apply(true, notify.DefaultInterval)
	s.arrived()
	eventuallyRuns(t, g, 1)
	s.arrived() // inside the interval, and the program is still running
	clk.pastInterval()
	s.arrived() // past the interval, but the program is still running
	if n := g.count(); n != 1 {
		t.Fatalf("runs = %d while the first run is in progress, want 1", n)
	}
	close(g.gate)
	s.join()
	s.arrived() // past the interval, run returned
	eventuallyRuns(t, g, 2)
	s.join()
	s.arrived() // inside the interval again
	if n := g.count(); n != 2 {
		t.Fatalf("runs = %d inside the second interval, want 2", n)
	}
	s.apply(false, notify.DefaultInterval)
	clk.pastInterval()
	s.arrived()
	if n := g.count(); n != 2 {
		t.Fatalf("runs = %d after the option went off, want 2", n)
	}

	// No program on PATH: on resolves to nothing and runs nothing.
	none := &gatedProgram{gate: make(chan struct{})}
	quiet := newSoundAnnouncer(testWatcher(none, clk, nil, nil))
	quiet.apply(true, notify.DefaultInterval)
	quiet.arrived()
	if n := none.count(); n != 0 || quiet.base != nil {
		t.Fatalf("runs = %d, base = %q with no program on PATH; want none", n, quiet.base)
	}
}

// TestBannerCarriesTheCountSinceTheLastOne (card 36): the banner's argv
// is completed per run with the session's own name and how many
// messages arrived since the last banner — the arrivals the interval
// kept silent, this one included — and never with a byte of a message.
func TestBannerCarriesTheCountSinceTheLastOne(t *testing.T) {
	t.Parallel()
	g := &gatedProgram{gate: make(chan struct{})}
	close(g.gate) // every run returns at once
	clk := &settableClock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	w := testWatcher(g, clk, nil, []string{"/nonexistent/brigade-test-notifier"})
	b := newBannerAnnouncer(w)
	b.apply(true, notify.DefaultInterval)
	b.arrived()
	eventuallyRuns(t, g, 1)
	b.join()
	if got := g.argv(0); !slices.Equal(got, []string{"/nonexistent/brigade-test-notifier", "--app-name=Brigade", "Brigade", "A message arrived for brigade-8d."}) {
		t.Fatalf("first banner argv = %q", got)
	}
	b.arrived() // silent: inside the interval
	b.arrived() // silent
	clk.pastInterval()
	b.arrived() // the third since the first banner
	eventuallyRuns(t, g, 2)
	b.join()
	if got := g.argv(1); !strings.HasSuffix(got[len(got)-1], "3 messages arrived for brigade-8d.") {
		t.Fatalf("second banner argv = %q, want the count of three", got)
	}
	// A renamed session is named as it is now.
	w.state.mu.Lock()
	w.state.name = "27-narrow-the-table"
	w.state.mu.Unlock()
	clk.pastInterval()
	b.arrived()
	eventuallyRuns(t, g, 3)
	b.join()
	if got := g.argv(2); got[len(got)-1] != "A message arrived for 27-narrow-the-table." {
		t.Fatalf("third banner argv = %q, want the new name and a count of one", got)
	}
	// Arrivals kept quiet before the option goes off are forgotten: the
	// first banner after it comes back counts from one.
	b.arrived()
	b.arrived()
	b.apply(false, notify.DefaultInterval)
	b.apply(true, notify.DefaultInterval)
	clk.pastInterval()
	b.arrived()
	eventuallyRuns(t, g, 4)
	b.join()
	if got := g.argv(3); got[len(got)-1] != "A message arrived for 27-narrow-the-table." {
		t.Fatalf("banner after an off-then-on flip = %q, want a count of one", got)
	}
	// The sound's argv never changes.
	s := newSoundAnnouncer(testWatcher(g, clk, []string{"/nonexistent/player", "-v", "0.25"}, nil))
	s.apply(true, notify.DefaultInterval)
	s.arrived()
	s.arrived()
	eventuallyRuns(t, g, 5)
	s.join()
	if got := g.argv(4); !slices.Equal(got, []string{"/nonexistent/player", "-v", "0.25"}) {
		t.Fatalf("sound argv = %q, want the fixed list", got)
	}
}

// TestAnnouncerIsCancelledAndJoinedAtExit: a program still running when
// the watcher exits is ended through its context and joined, so run's
// join returns without waiting for it to finish. The wait is a hang
// catcher.
func TestAnnouncerIsCancelledAndJoinedAtExit(t *testing.T) {
	t.Parallel()
	g := &gatedProgram{gate: make(chan struct{})} // never released: only the context ends it
	clk := &settableClock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	s := newSoundAnnouncer(testWatcher(g, clk, []string{"/nonexistent/player"}, nil))
	ctx, cancel := context.WithCancel(context.Background())
	s.bind(ctx)
	s.apply(true, notify.DefaultInterval)
	s.arrived()
	eventuallyRuns(t, g, 1)
	cancel()
	done := make(chan struct{})
	go func() { s.join(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("join did not return after the program's context was cancelled")
	}
	if err := g.ctxs[0].Err(); err == nil {
		t.Fatal("the program's context did not end")
	}
}

// TestAnnouncerHonoursTheConfiguredInterval (card 38): the interval is
// the map's, per announcer — five seconds lets a second arrival through
// six seconds later, an hour keeps it quiet — and a change through apply
// takes effect at the next arrival without touching the option.
func TestAnnouncerHonoursTheConfiguredInterval(t *testing.T) {
	t.Parallel()
	g := &gatedProgram{gate: make(chan struct{})}
	close(g.gate)
	clk := &settableClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	s := newSoundAnnouncer(testWatcher(g, clk, []string{"/nonexistent/player"}, nil))
	s.apply(true, notify.IntervalFloor)
	s.arrived()
	eventuallyRuns(t, g, 1)
	s.join()
	clk.mu.Lock()
	clk.now = clk.now.Add(notify.IntervalFloor + time.Second)
	clk.mu.Unlock()
	s.arrived()
	eventuallyRuns(t, g, 2)
	s.join()
	s.apply(true, notify.IntervalCeiling) // the option stays on; only the interval moves
	clk.pastInterval()                    // past the default, well inside an hour
	s.arrived()
	if n := g.count(); n != 2 {
		t.Fatalf("runs = %d inside an hour-long interval, want 2", n)
	}
	clk.mu.Lock()
	clk.now = clk.now.Add(notify.IntervalCeiling)
	clk.mu.Unlock()
	s.arrived()
	eventuallyRuns(t, g, 3)
	s.join()
}
