package state_test

import (
	"os"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/state"
)

// TestHoldOfADecision (card 236): a held ask is held while it is open,
// unsent and no end is recorded, and holding until its time; a send, an
// end word and a close each end it; both copies carry the hold, a reader
// drops an end word it does not know, and HeldBy lists a session's own.
func TestHoldOfADecision(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	mk := func(id, session string) *state.Decision {
		return &state.Decision{Version: state.Version, ID: id, Question: "Ship it?", To: "rjae@example.com", SessionName: "payments", SessionID: session, CreatedAt: t0, UpdatedAt: t0}
	}
	until := t0.Add(10 * time.Minute)
	d := mk("d-20261029-aaaaaa", "session-1")
	if d.Held() || d.Holding(t0) {
		t.Fatal("a decision nobody held reads as held")
	}
	d.Hold(until)
	if !d.Held() || !d.Holding(t0) || !d.Holding(until.Add(-time.Second)) || d.Holding(until) {
		t.Fatalf("held %v, holding at the ask %v, at the end %v", d.Held(), d.Holding(t0), d.Holding(until))
	}
	if err := s.SaveDecision(d); err != nil {
		t.Fatal(err)
	}
	back, err := s.LoadDecision(d.ID)
	if err != nil || !back.Held() || !back.HeldUntil.Equal(until) {
		t.Fatalf("the canonical copy %+v %v", back, err)
	}
	theirs, err := state.ReadDecision(s.Folder, d.ID)
	if err != nil || !theirs.Held() || !theirs.HeldUntil.Equal(until) {
		t.Fatalf("the folder copy %+v %v", theirs, err)
	}

	// Each way a hold ends.
	sent, prompted, closed, other := mk("d-20261029-bbbbbb", "session-1"), mk("d-20261029-cccccc", "session-1"), mk("d-20261029-dddddd", "session-1"), mk("d-20261029-eeeeee", "session-2")
	for _, x := range []*state.Decision{sent, prompted, closed, other} {
		x.Hold(until)
	}
	sent.Sent, sent.SentTo = state.SentSlack, "@rjae"
	prompted.HoldEnded = state.HoldPrompt
	closed.ClosedAt, closed.Outcome = t0.Add(time.Minute), "ship"
	for _, x := range []*state.Decision{sent, prompted, closed} {
		if x.Held() || x.Holding(t0) {
			t.Errorf("%s reads as held: %+v", x.ID, *x)
		}
	}
	for _, x := range []*state.Decision{sent, prompted, closed, other} {
		if err := s.SaveDecision(x); err != nil {
			t.Fatal(err)
		}
	}
	if held := s.HeldBy("session-1"); len(held) != 1 || held[0].ID != d.ID {
		t.Fatalf("HeldBy(session-1) = %+v", held)
	}
	if held := s.HeldBy("session-2"); len(held) != 1 || held[0].ID != other.ID {
		t.Fatalf("HeldBy(session-2) = %+v", held)
	}
	if held := s.HeldBy("nobody"); len(held) != 0 {
		t.Fatalf("HeldBy(nobody) = %+v", held)
	}
	if held := (state.Store{StateDir: t.TempDir()}).HeldBy("session-1"); held != nil {
		t.Fatalf("HeldBy with no decisions directory = %+v", held)
	}

	// An end word of another build is dropped: the hold reads as it was.
	path := state.DecisionPath(s.Folder, d.ID)
	if err := os.WriteFile(path, []byte(`{"version":1,"id":"`+d.ID+`","question":"Ship it?","options":[],"session_name":"payments","session_id":"session-1","held_until":"2026-10-29T12:10:00Z","hold_ended":"whenever","created_at":"2026-10-29T12:00:00Z","updated_at":"2026-10-29T12:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := state.ReadDecision(s.Folder, d.ID); err != nil || got.HoldEnded != "" || !got.Held() {
		t.Fatalf("a foreign end word: %+v %v", got, err)
	}
	// A hold a folder copy stretches past the longest wait is cut to it, so
	// no copy keeps its decision out of the digests for good.
	far := mk("d-20261029-ffffff", "session-1")
	far.Hold(t0.Add(30 * 24 * time.Hour))
	far.Sanitize()
	if !far.HeldUntil.Equal(t0.Add(state.MaxWait)) || far.Holding(t0.Add(state.MaxWait)) {
		t.Errorf("a hold past the longest wait: held until %s", far.HeldUntil)
	}
	for _, word := range []string{state.HoldPrompt, state.HoldMissed, state.HoldUnsent} {
		x := mk("d-20261029-ffffff", "session-1")
		x.Hold(until)
		x.HoldEnded = word
		x.Sanitize()
		if x.HoldEnded != word || x.Held() {
			t.Errorf("end word %q after Sanitize: %+v", word, *x)
		}
	}
}

// TestSubscribersWait (card 236): the wait is the subscriber's own member
// of reports.json — a duration, zero for at once — and DefaultWait when it
// is absent, unreadable, negative or over MaxWait, and for a member who is
// not subscribed; it survives the file's round trip.
func TestSubscribersWait(t *testing.T) {
	t.Parallel()
	for wait, want := range map[string]time.Duration{
		"":      state.DefaultWait,
		"30m":   30 * time.Minute,
		"0s":    0,
		"90s":   90 * time.Second,
		"24h":   state.MaxWait,
		"25h":   state.DefaultWait,
		"-5m":   state.DefaultWait,
		"later": state.DefaultWait,
	} {
		if got := (&state.Subscriber{Wait: wait}).HoldFor(); got != want {
			t.Errorf("wait %q: HoldFor = %v, want %v", wait, got, want)
		}
	}
	if got := (*state.Subscriber)(nil).HoldFor(); got != state.DefaultWait {
		t.Errorf("nobody's entry: HoldFor = %v", got)
	}
	folder := t.TempDir()
	r := &state.Reports{Subscribers: []state.Subscriber{{Label: "rjae@example.com", Wait: " 15m0s\n"}, {Label: "bob@example.com"}}}
	if err := state.WriteReports(folder, r); err != nil {
		t.Fatal(err)
	}
	back, err := state.ReadReports(folder)
	if err != nil || len(back.Subscribers) != 2 || back.Subscribers[0].Wait != "15m0s" || back.Subscribers[0].HoldFor() != 15*time.Minute || back.Subscribers[1].Wait != "" || back.Subscribers[1].HoldFor() != state.DefaultWait {
		t.Fatalf("reports after the round trip: %+v %v", back, err)
	}
	if got := back.Subscriber("nobody@example.com").HoldFor(); got != state.DefaultWait {
		t.Errorf("a member who is not subscribed: HoldFor = %v", got)
	}
}
