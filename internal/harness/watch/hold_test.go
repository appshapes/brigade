package watch_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// The acceptance of P5-9 (3.6, 6.8 step 5) with the fs adapter and the
// fake socket: `hold` writes pending and never posts or acks; a release
// file injects exactly the released ids once and acks them; a foreign
// release file is left alone; a release written while no watcher runs is
// applied at the next start; a hold→accept flip is not a release. The
// instruments are fakesock's connection count and the fs store's ack
// state, never a log line alone. Every wait is a hang catcher.

// holdFixture is an fs-adapter, socket-mode fixture under `hold` with n
// messages sent before the watcher starts (the fs adapter re-emits every
// unacknowledged message on a watch start).
func holdFixture(t *testing.T, bodies ...string) (*fixture, []string) {
	t.Helper()
	fx := newFixture(t, fixtureOptions{inbound: protocol.InboundHold})
	fx.useFS()
	fx.writeMap()
	ids := make([]string, 0, len(bodies))
	for _, b := range bodies {
		fx.forbidBody(b)
		ids = append(ids, fx.send(b))
	}
	return fx, ids
}

// waitHeld polls until the pending file holds exactly ids (unreleased).
func waitHeld(t *testing.T, fx *fixture, ids ...string) {
	t.Helper()
	want := slices.Clone(ids)
	slices.Sort(want)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		if _, err := os.Stat(fx.pendingPath()); err != nil {
			return false
		}
		got := fx.pendingIDs()
		slices.Sort(got)
		return slices.Equal(got, want)
	})
}

func TestHoldWritesPendingAndNeverPostsOrAcks(t *testing.T) {
	t.Parallel()
	fx, ids := holdFixture(t, "held under hold: never posted 5d1c")
	deps := fx.deps()
	deps.HeartbeatInterval = 200 * time.Millisecond
	r := fx.start(deps)
	fx.waitLog("message offered", map[string]any{"message_id": ids[0], "outcome": "held", "ack": false})
	waitHeld(t, fx, ids[0])
	first := fx.session().LastSeenAt
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return fx.session().LastSeenAt.After(first) })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := fx.sock.Accepted(); n != 0 {
		t.Errorf("connections = %d, want 0 under hold", n)
	}
	if got := fx.inboxIDs(); !slices.Equal(got, ids) {
		t.Errorf("inbox = %v, want %v (still unacknowledged on the store)", got, ids)
	}
	if got := fx.ackedIDs(); len(got) != 0 {
		t.Errorf("acked = %v, want none", got)
	}
	if fx.logHas("injection reported", nil) || fx.logHas("ack sent", nil) {
		t.Errorf("the log reports an injection or an ack under hold")
	}
	info, err := os.Stat(fx.pendingPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("pending file: %v %v", info, err)
	}
	e := fx.pendingEntries()
	if len(e) != 1 || e[0].SenderName != "sender" || e[0].SenderSessionID != fx.senderSessionID || e[0].Released() {
		t.Errorf("entries %+v", e)
	}
	if s := fx.session(); s.ClosedAt == nil || s.Inbound != "hold" {
		t.Errorf("session = %+v", s)
	}
}

func TestReleaseFileInjectsExactlyOnceAndAcks(t *testing.T) {
	t.Parallel()
	fx, ids := holdFixture(t, "first held 1a2b", "second held 3c4d", "third held 5e6f")
	r := fx.start(fx.deps())
	waitHeld(t, fx, ids...)
	fx.writeRelease(fx.sessionID, ids[1])
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], `message-id="`+ids[1]+`"`) || !strings.Contains(frames[0], "second held 3c4d") {
		t.Fatalf("frames = %q, want exactly the released id", frames)
	}
	for _, other := range []string{ids[0], ids[2]} {
		if strings.Contains(frames[0], other) {
			t.Fatalf("frame carries an unreleased id %s: %q", other, frames[0])
		}
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Equal(fx.ackedIDs(), []string{ids[1]}) })
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		_, err := os.Stat(fx.releasePath())
		return err != nil
	})
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		got := fx.pendingIDs()
		slices.Sort(got)
		want := []string{ids[0], ids[2]}
		slices.Sort(want)
		return slices.Equal(got, want)
	})
	// Nothing else moves while the watcher keeps running.
	fx.waitLog("ack sent", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(fx.pidfilePath()); err == nil {
		t.Error("pidfile survived the exit")
	}
	if n := len(fx.sock.Frames()); n != 1 {
		t.Errorf("frames = %d, want exactly one", n)
	}
	// The fs adapter MOVES an acknowledged message out of the inbox, so
	// the two unreleased ids are exactly what is left there.
	inbox := fx.inboxIDs()
	slices.Sort(inbox)
	want := []string{ids[0], ids[2]}
	slices.Sort(want)
	if !slices.Equal(inbox, want) || !slices.Equal(fx.ackedIDs(), []string{ids[1]}) {
		t.Errorf("inbox = %v acked = %v, want the two unreleased ids %v in the inbox and %s acknowledged", inbox, fx.ackedIDs(), want, ids[1])
	}
}

func TestReleaseOfAForeignSessionIsIgnored(t *testing.T) {
	t.Parallel()
	fx, ids := holdFixture(t, "held for someone else 7a8b")
	r := fx.start(fx.deps())
	waitHeld(t, fx, ids[0])
	fx.writeRelease("another-session", ids[0])
	fx.waitLog("release file names another session; left in place", nil)
	// Several more ticks: still nothing.
	first := fx.session().LastSeenAt
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return fx.session().LastSeenAt.After(first) })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := fx.sock.Accepted(); n != 0 {
		t.Errorf("connections = %d, want 0", n)
	}
	if got := fx.ackedIDs(); len(got) != 0 {
		t.Errorf("acked = %v", got)
	}
	if _, err := os.Stat(fx.releasePath()); err != nil {
		t.Errorf("the foreign release file was removed: %v", err)
	}
	if got := fx.pendingIDs(); !slices.Equal(got, ids) {
		t.Errorf("pending = %v, want %v unreleased", got, ids)
	}
	// A refused file is logged once, not on every tick.
	if n := fx.logCount("release file names another session; left in place", nil); n != 1 {
		t.Errorf("logged %d times, want once", n)
	}
}

func TestReleaseSurvivesAWatcherRestart(t *testing.T) {
	t.Parallel()
	fx, ids := holdFixture(t, "held before the restart 9c0d")
	r := fx.start(fx.deps())
	waitHeld(t, fx, ids[0])
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("first exit %d", code)
	}
	// The release is written while no watcher runs ("once at start").
	fx.writeRelease(fx.sessionID, ids[0])
	r = fx.start(fx.deps())
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], `message-id="`+ids[0]+`"`) {
		t.Fatalf("frames = %q", frames)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.ackedIDs()) == 1 })
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		_, err := os.Stat(fx.releasePath())
		return err != nil
	})
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.pendingEntries()) == 0 })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("second exit %d", code)
	}
	if n := len(fx.sock.Frames()); n != 1 {
		t.Errorf("frames = %d, want exactly one across both runs", n)
	}
	if got := fx.inboxIDs(); len(got) != 0 {
		t.Errorf("inbox = %v, want empty", got)
	}
}

// TestHoldPolicyFlipThroughTheMap records the decision that a policy flip
// is not a release: after the map is rewritten hold→accept, messages
// offered afterwards are injected and the ones already pending stay
// pending until a release names them.
func TestHoldPolicyFlipThroughTheMap(t *testing.T) {
	t.Parallel()
	fx, ids := holdFixture(t, "held before the flip b1c2")
	r := fx.start(fx.deps())
	waitHeld(t, fx, ids[0])

	fx.inbound = protocol.InboundAccept
	fx.writeMap()
	fx.waitLog("inbound policy changed", map[string]any{"inbound": "accept"})
	open := fx.send("after the flip d3e4")
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], "after the flip d3e4") {
		t.Fatalf("frames = %q", frames)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Equal(fx.ackedIDs(), []string{open}) })
	if got := fx.pendingIDs(); !slices.Equal(got, ids) {
		t.Fatalf("pending after the flip = %v, want %v still held", got, ids)
	}
	// The ack moves `open` out of the inbox (an acked copy first, then the
	// removal), so the inbox settles on the held id alone.
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Equal(fx.inboxIDs(), ids) })
	// The release, under accept, delivers the held one.
	fx.writeRelease(fx.sessionID, ids[0])
	frames = fx.sock.WaitFrames(2, waitShort)
	if len(frames) != 2 || !strings.Contains(frames[1], "held before the flip b1c2") {
		t.Fatalf("frames after the release = %q", frames)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.ackedIDs()) == 2 })
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return len(fx.pendingEntries()) == 0 })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestBodyGrepBites is the positive control of the cleanup grep over the
// state directory: a planted body is found; an untouched fixture has none.
func TestBodyGrepBites(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	const body = "a planted body 0f1e"
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/planted", []byte("x "+body+" y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := tokenHits(t, body, dir); len(hits) != 1 {
		t.Fatalf("hits = %v, want the planted file", hits)
	}
	if hits := tokenHits(t, body, fx.dirs.BrigadeState); len(hits) != 0 {
		t.Fatalf("hits in an untouched fixture = %v", hits)
	}
}
