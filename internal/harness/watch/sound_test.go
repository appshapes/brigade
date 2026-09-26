package watch_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/sound"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// The message-arrival sound (card 35) with the fs adapter and the fake
// socket: one play for a burst, another only after sound.MinInterval by
// the watcher's clock; a held message plays and its release does not; off
// by default, and on within a tick when the map flips; nothing for a
// refused message. The player is a recorder — the machine's own players
// never decide a test — and every wait is a hang catcher.

// soundRecorder is the injected Deps.Sound: it counts plays and keeps the
// last argv and environment.
type soundRecorder struct {
	mu    sync.Mutex
	plays int
	argv  []string
	env   []string
}

func (r *soundRecorder) play(_ context.Context, argv, env []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plays++
	r.argv = slices.Clone(argv)
	r.env = slices.Clone(env)
	return nil
}

func (r *soundRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.plays
}

func (r *soundRecorder) last() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.argv), slices.Clone(r.env)
}

// fakeClock is a settable Deps.Clock: the interval between sounds is
// measured by it, so a test moves it rather than waiting.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var testPlayer = []string{"/nonexistent/brigade-test-player", "--quiet"}

func soundDeps(fx *fixture, rec *soundRecorder, clk *fakeClock) watch.Deps {
	deps := fx.deps()
	deps.Sound = rec.play
	deps.SoundCommand = testPlayer
	if clk != nil {
		deps.Clock = clk.Now
	}
	return deps
}

func soundOn(m *sessionmap.ByPID) { m.MessageSound = true }

func TestMessageSoundOncePerBurstThenAgainAfterTheInterval(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMapWith(soundOn)
	first := fx.send("first of a burst 7a1")
	rec := &soundRecorder{}
	clk := &fakeClock{now: time.Now()}
	r := fx.start(soundDeps(fx, rec, clk))
	fx.waitLog("message sound on", nil)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return rec.count() == 1 })
	// The second message is sent only once the first play has returned,
	// so the interval alone — not the one-player-at-a-time guard — is
	// what keeps it silent.
	fx.waitLog("message sound played", nil)
	second := fx.send("second of a burst 7a2")
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		got := fx.ackedIDs()
		return slices.Contains(got, first) && slices.Contains(got, second)
	})
	if n := rec.count(); n != 1 {
		t.Fatalf("plays = %d after a burst of two, want 1", n)
	}
	clk.Advance(sound.MinInterval + time.Second)
	third := fx.send("after the interval 7a3")
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return rec.count() == 2 })
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Contains(fx.ackedIDs(), third) })
	argv, env := rec.last()
	if !slices.Equal(argv, testPlayer) {
		t.Errorf("argv = %q, want the fixed player %q", argv, testPlayer)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "BRIGADE_") || strings.HasPrefix(e, "CLAUDE_CODE_MESSAGING") {
			t.Errorf("the player's environment carries %q", e)
		}
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestMessageSoundForAHeldMessageAndNotForItsRelease(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{inbound: protocol.InboundHold})
	fx.useFS()
	fx.writeMapWith(soundOn)
	const body = "held and announced 9b1"
	fx.forbidBody(body)
	id := fx.send(body)
	rec := &soundRecorder{}
	clk := &fakeClock{now: time.Now()}
	r := fx.start(soundDeps(fx, rec, clk))
	waitHeld(t, fx, id)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return rec.count() == 1 })
	// The release comes after the interval, so a second sound would not
	// be hidden by it.
	clk.Advance(sound.MinInterval + time.Second)
	fx.writeRelease(fx.sessionID, id)
	if frames := fx.sock.WaitFrames(1, waitShort); len(frames) != 1 || !strings.Contains(frames[0], body) {
		t.Fatalf("frames = %q, want the released message", frames)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Equal(fx.ackedIDs(), []string{id}) })
	if n := rec.count(); n != 1 {
		t.Fatalf("plays = %d after a hold and its release, want 1 (the release is the member's own act)", n)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestMessageSoundOffByDefaultAndOnWhenTheMapFlips(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	first := fx.send("quiet by default 3c1")
	rec := &soundRecorder{}
	r := fx.start(soundDeps(fx, rec, nil))
	if frames := fx.sock.WaitFrames(1, waitShort); len(frames) != 1 {
		t.Fatalf("frames = %d, want the message injected", len(frames))
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Equal(fx.ackedIDs(), []string{first}) })
	if n := rec.count(); n != 0 {
		t.Fatalf("plays = %d with the option off, want 0", n)
	}
	if fx.logHas("message sound on", nil) {
		t.Fatal("the log says the sound is on for a map that has it off")
	}
	fx.writeMapWith(soundOn)
	fx.waitLog("message sound on", nil)
	second := fx.send("audible now 3c2")
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return rec.count() == 1 })
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Contains(fx.ackedIDs(), second) })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestMessageSoundNeverForARefusedMessage(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{inbound: protocol.InboundRefuse})
	fx.useFS()
	fx.writeMapWith(soundOn)
	ids := []string{fx.send("refused 5d1"), fx.send("refused too 5d2")}
	rec := &soundRecorder{}
	r := fx.start(soundDeps(fx, rec, nil))
	for _, id := range ids {
		fx.waitLog("message offered", map[string]any{"message_id": id, "outcome": "refused"})
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("plays = %d for refused messages, want 0", n)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
