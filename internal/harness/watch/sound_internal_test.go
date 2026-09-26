package watch

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sound"
)

// TestArrivedNow pins the arrival rule over every decision Offer returns
// (card 35): a fresh queued message and a newly held one ring; a
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

// gatedPlayer is a Deps.Sound that blocks until released (or its context
// ends), counting plays and keeping each play's context.
type gatedPlayer struct {
	mu    sync.Mutex
	plays int
	gate  chan struct{}
	ctxs  []context.Context
}

func (g *gatedPlayer) play(ctx context.Context, _, _ []string) error {
	g.mu.Lock()
	g.plays++
	g.ctxs = append(g.ctxs, ctx)
	g.mu.Unlock()
	select {
	case <-g.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gatedPlayer) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.plays
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

func (c *settableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testSounder(g *gatedPlayer, clk *settableClock, command []string, pathVar string) *sounder {
	w := &watcher{
		deps:    Deps{Clock: clk.Now, Sound: g.play, SoundCommand: command},
		log:     slog.New(slog.DiscardHandler),
		environ: []string{"PATH=" + pathVar},
	}
	return newSounder(w)
}

func eventuallyPlays(t *testing.T, g *gatedPlayer, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for g.count() != n {
		if time.Now().After(deadline) {
			t.Fatalf("plays = %d, want %d", g.count(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSounderCoalescesAndNeverOverlaps pins the two guards with the clock
// in hand (card 35): a second arrival inside sound.MinInterval plays
// nothing, an arrival while a player is still running plays nothing
// however far the clock moved, and an arrival after both — the interval
// passed, the player returned — plays again. Off, or on with no player on
// PATH, plays nothing at all.
func TestSounderCoalescesAndNeverOverlaps(t *testing.T) {
	t.Parallel()
	g := &gatedPlayer{gate: make(chan struct{})}
	clk := &settableClock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	s := testSounder(g, clk, []string{"/nonexistent/player"}, "/nonexistent/bin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.bind(ctx)

	s.arrived()
	if n := g.count(); n != 0 {
		t.Fatalf("plays = %d before the option is on, want 0", n)
	}
	s.apply(true)
	s.arrived()
	eventuallyPlays(t, g, 1)
	s.arrived() // inside the interval, and the player is still running
	clk.advance(sound.MinInterval + time.Second)
	s.arrived() // past the interval, but the player is still running
	if n := g.count(); n != 1 {
		t.Fatalf("plays = %d while the first player runs, want 1", n)
	}
	close(g.gate)
	s.join()
	s.arrived() // past the interval, player returned
	eventuallyPlays(t, g, 2)
	s.join()
	s.arrived() // inside the interval again
	if n := g.count(); n != 2 {
		t.Fatalf("plays = %d inside the second interval, want 2", n)
	}
	s.apply(false)
	clk.advance(sound.MinInterval + time.Second)
	s.arrived()
	if n := g.count(); n != 2 {
		t.Fatalf("plays = %d after the option went off, want 2", n)
	}

	// No player on PATH: on resolves to nothing and plays nothing.
	none := &gatedPlayer{gate: make(chan struct{})}
	quiet := testSounder(none, clk, nil, "/nonexistent/bin")
	quiet.apply(true)
	quiet.arrived()
	if n := none.count(); n != 0 || quiet.argv != nil {
		t.Fatalf("plays = %d, argv = %q with no player on PATH; want none", n, quiet.argv)
	}
}

// TestSounderIsCancelledAndJoinedAtExit: a player still running when the
// watcher exits is ended through its context and joined, so run's join
// returns without waiting for the sound to finish. The wait is a hang
// catcher.
func TestSounderIsCancelledAndJoinedAtExit(t *testing.T) {
	t.Parallel()
	g := &gatedPlayer{gate: make(chan struct{})} // never released: only the context ends it
	clk := &settableClock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	s := testSounder(g, clk, []string{"/nonexistent/player"}, "/nonexistent/bin")
	ctx, cancel := context.WithCancel(context.Background())
	s.bind(ctx)
	s.apply(true)
	s.arrived()
	eventuallyPlays(t, g, 1)
	cancel()
	done := make(chan struct{})
	go func() { s.join(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("join did not return after the player's context was cancelled")
	}
	if err := g.ctxs[0].Err(); err == nil {
		t.Fatal("the player's context did not end")
	}
}
