package watch_test

import (
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// The slow schedule of card 34: a watcher whose restarts trip the give-up
// rule on a failure a restart can fix stays, retries slowly, and says when
// it has reconnected. The instruments are the watcher's own log, its
// notice file, its pidfile and the fake socket. Every wait is a hang
// catcher.

// The two notices, as the next prompt prints them.
const (
	slowNoticePrefix = "Brigade: this session is not receiving team messages (unavailable). Brigade tries again at least every "
	slowNoticeSuffix = ". Messages wait on the server until it reconnects.\n"
	backNoticePrefix = "Brigade: this session is receiving team messages again, after about "
)

// notice reads the fixture's notice file, "" when there is none.
func (fx *fixture) notice() string {
	data, err := os.ReadFile(fx.noticePath())
	if err != nil {
		return ""
	}
	return string(data)
}

// restarts counts the restart lines of one pace.
func (fx *fixture) restarts(slow bool) int {
	return fx.logCount("watch child failed; restarting", map[string]any{"slow": slow})
}

// TestUnreachableBackendRetriesSlowly: an adapter whose watch exits 9 at
// once trips the give-up rule after ten failures, and the watcher STAYS.
// It writes one notice, keeps its pidfile, and goes on trying at the slow
// pace — every delay from the slow schedule, none from the fast one —
// until it is stopped.
func TestUnreachableBackendRetriesSlowly(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useHelper("exit=9")
	fx.writeMap()
	deps := fx.deps()
	// Far above the fast schedule's 40 ms cap, so a delay says which
	// schedule it came from.
	const every = 400 * time.Millisecond
	deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(every, every, seeded(4)) }
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return fx.restarts(true) >= 3 })
	if r.exited() {
		t.Fatalf("the watcher exited: %v", fx.logLines())
	}

	if n := fx.logCount("watcher retrying slowly", map[string]any{"code": "unavailable", "why": "child_exit", "failures": 10}); n != 1 {
		t.Errorf("slow lines = %d, want 1: %v", n, fx.logLines())
	}
	if fx.logHas("watcher giving up", nil) {
		t.Errorf("the watcher gave up on an unreachable backend")
	}
	if n := fx.restarts(false); n != 9 {
		t.Errorf("fast restart lines = %d, want 9 (the tenth failure is the first slow one)", n)
	}
	for _, l := range fx.logLines() {
		if l["msg"] != "watch child failed; restarting" {
			continue
		}
		delay, _ := l["delay"].(float64)
		if slow, _ := l["slow"].(bool); slow != (time.Duration(delay) >= every/2) {
			t.Errorf("a restart with slow=%v waits %v (the slow schedule starts at %v)", slow, time.Duration(delay), every/2)
		}
	}
	if got, want := fx.notice(), slowNoticePrefix+"400ms"+slowNoticeSuffix; got != want {
		t.Errorf("notice:\n got %q\nwant %q", got, want)
	}
	// One notice for the whole outage, not one per attempt.
	if n := fx.logCount("notice written", nil); n != 1 {
		t.Errorf("notices written = %d, want 1", n)
	}
	if _, err := os.Stat(fx.pidfilePath()); err != nil {
		t.Errorf("the pidfile is gone while the watcher runs: %v", err)
	}

	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d, want 0 after a stop", code)
	}
	if !fx.logHas("closing the session without a watch child", map[string]any{"reason": "stop"}) {
		t.Errorf("log: %v", fx.logLines())
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present: %v", err)
	}
}

// TestRateLimitedBackendRetriesSlowly: `rate_limited` is the other code a
// restart can fix, and slowing down is what it asks for.
func TestRateLimitedBackendRetriesSlowly(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useHelper("exit=8")
	fx.writeMap()
	deps := fx.deps()
	deps.GiveUpFailures = 3
	deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(time.Hour, time.Hour, seeded(5)) }
	r := fx.start(deps, fx.args()...)
	fx.waitLog("watcher retrying slowly", map[string]any{"code": "rate_limited", "failures": 3})
	// The restart line follows the notice; the slow line precedes it.
	fx.waitLog("watch child failed; restarting", map[string]any{"slow": true})
	if !strings.Contains(fx.notice(), "(rate_limited)") {
		t.Errorf("notice = %q", fx.notice())
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestSlowRetryReconnects: the backend comes back. The next slow attempt
// is ready, and once it has been ready for HealthyAfter the watcher says
// it has reconnected — in the log and in the notice, which replaces the
// outage's. The pace is the fast one again afterwards: the backend goes
// away a second time and the first restarts after it are fast ones, until
// the rule trips again.
func TestSlowRetryReconnects(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	gate := t.TempDir() + "/gate"
	fx.useHelper("gate=" + gate)
	fx.writeMap()
	deps := fx.deps()
	deps.GiveUpFailures = 3
	deps.HealthyAfter = 300 * time.Millisecond
	deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(60*time.Millisecond, 60*time.Millisecond, seeded(6)) }
	r := fx.start(deps, fx.args()...)

	fx.waitLog("watcher retrying slowly", map[string]any{"code": "unavailable", "failures": 3})
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return strings.HasPrefix(fx.notice(), slowNoticePrefix) })
	if fx.logHas("watch ready", nil) || fx.logHas("watcher reconnected", nil) {
		t.Fatalf("ready or reconnected before the backend was back: %v", fx.logLines())
	}
	fastBefore := fx.restarts(false)

	openGate(t, gate)
	fx.waitLog("watch ready", nil)
	fx.waitLog("watcher reconnected", nil)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return strings.HasPrefix(fx.notice(), backNoticePrefix) })
	if got := fx.notice(); !strings.HasSuffix(got, " without.\n") || strings.Count(got, "\n") != 1 {
		t.Errorf("notice = %q", got)
	}
	// That reconnected waits for HealthyAfter, and is not said at ready, is
	// TestShortRecoveryStaysSlow's to show: a comparison of two log
	// timestamps here would be a bound on the wall clock.
	if n := fx.logCount("watcher reconnected", nil); n != 1 {
		t.Errorf("reconnected lines = %d, want 1", n)
	}

	closeGate(t, gate)
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return fx.logCount("watcher retrying slowly", nil) == 2 })
	if n := fx.restarts(false) - fastBefore; n != deps.GiveUpFailures-1 {
		t.Errorf("fast restarts after the reconnect = %d, want %d: the pace did not return to the fast one", n, deps.GiveUpFailures-1)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestShortRecoveryStaysSlow: a child that is ready and then gone before
// HealthyAfter has not reconnected. The watcher says nothing and keeps the
// slow pace.
func TestShortRecoveryStaysSlow(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useHelper("ready-exit=9")
	fx.writeMap()
	deps := fx.deps()
	deps.GiveUpFailures = 3
	deps.HealthyAfter = time.Hour
	deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(60*time.Millisecond, 60*time.Millisecond, seeded(7)) }
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return fx.restarts(true) >= 4 })
	if fx.logHas("watcher reconnected", nil) || strings.HasPrefix(fx.notice(), backNoticePrefix) {
		t.Errorf("reconnected after a run shorter than HealthyAfter: %v", fx.logLines())
	}
	if n := fx.logCount("watcher retrying slowly", nil); n != 1 {
		t.Errorf("slow lines = %d, want 1", n)
	}
	if n := fx.restarts(false); n != deps.GiveUpFailures-1 {
		t.Errorf("fast restarts = %d, want %d", n, deps.GiveUpFailures-1)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestSlowWaitEndsWithTheSession: a slow wait is minutes and runs no event
// loop, so it keeps the liveness tick itself. The Claude process dying, or
// the by-pid map going, ends the watcher within a tick — not at the end of
// the wait, which here is an hour.
func TestSlowWaitEndsWithTheSession(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		end    func(fx *fixture)
		reason string
	}{
		"the claude process dies": {func(fx *fixture) {
			if err := syscall.Kill(fx.claudePID, syscall.SIGTERM); err != nil {
				fx.t.Fatal(err)
			}
		}, "claude_gone"},
		"the by-pid map goes": {func(fx *fixture) {
			if err := fx.store().DeleteByPID(fx.claudePID); err != nil {
				fx.t.Fatal(err)
			}
		}, "map_gone"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{sink: true})
			fx.useHelper("exit=9")
			fx.writeMap()
			deps := fx.deps()
			deps.GiveUpFailures = 3
			deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(time.Hour, time.Hour, seeded(8)) }
			r := fx.start(deps, fx.args()...)
			fx.waitLog("watch child failed; restarting", map[string]any{"slow": true})
			tc.end(fx)
			if code := r.wait(); code != 0 {
				t.Fatalf("exit %d, want 0", code)
			}
			if !fx.logHas("closing the session without a watch child", map[string]any{"reason": tc.reason}) {
				t.Errorf("log: %v", fx.logLines())
			}
			if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
				t.Errorf("pidfile still present: %v", err)
			}
		})
	}
}

// twoTicks returns once two liveness ticks have run in full: the by-pid
// map's inbound policy is flipped and flipped back, and the watcher logs
// each change on the tick that reads it. Whatever the first of those ticks
// did after reading the map is in the log by the time the second is.
func (fx *fixture) twoTicks() {
	fx.t.Helper()
	fx.writeMapWith(func(m *sessionmap.ByPID) { m.Inbound = protocol.InboundHold })
	fx.waitLog("inbound policy changed", map[string]any{"inbound": "hold"})
	fx.writeMap()
	fx.waitLog("inbound policy changed", map[string]any{"inbound": "accept"})
}

// TestActivityCutsASlowWaitShort: the watcher that used to exit was
// respawned by the next prompt. The one that stays tries again when the
// session becomes active, once ActivityRetryGap of the wait has passed —
// and only then: the gap passing with no activity cuts nothing short, and
// the control arm, whose gap is an hour, sees the same activity and waits.
func TestActivityCutsASlowWaitShort(t *testing.T) {
	t.Parallel()
	const cut = "the session is active; trying the watch child now"
	run := func(t *testing.T, gap time.Duration) (*fixture, *running, int) {
		t.Helper()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useHelper("exit=9")
		fx.writeMap()
		deps := fx.deps()
		deps.GiveUpFailures = 3
		deps.ActivityRetryGap = gap
		deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(time.Hour, time.Hour, seeded(9)) }
		r := fx.start(deps, fx.args()...)
		fx.waitLog("watch child failed; restarting", map[string]any{"slow": true})
		before := fx.logCount("watch child ended", nil)
		// Two ticks of the wait with no activity: past the short gap, and
		// nothing is cut short.
		fx.twoTicks()
		if fx.logHas(cut, nil) || fx.logCount("watch child ended", nil) != before {
			t.Fatalf("the wait was cut short with no activity: %v", fx.logLines())
		}
		fx.writeRegistry("receiver", "busy", "")
		fx.waitLog("activity changed", map[string]any{"activity": "busy"})
		return fx, r, before
	}

	t.Run("after the gap", func(t *testing.T) {
		t.Parallel()
		fx, r, before := run(t, 20*time.Millisecond)
		fx.waitLog(cut, nil)
		testutil.Eventually(t, waitShort, pollEvery, func() bool { return fx.logCount("watch child ended", nil) > before })
		if code := r.stopAndWait(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("control: inside the gap", func(t *testing.T) {
		t.Parallel()
		fx, r, before := run(t, time.Hour)
		// A second change of activity proves a later tick ran: had the first
		// one cut the wait short, the attempt would be in the log by now.
		fx.writeRegistry("receiver", "idle", "")
		fx.waitLog("activity changed", map[string]any{"activity": "idle"})
		if fx.logHas(cut, nil) || fx.logCount("watch child ended", nil) != before {
			t.Errorf("the wait was cut short inside the gap: %v", fx.logLines())
		}
		if code := r.stopAndWait(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
}

// TestSlowWaitAppliesARelease: under `hold` a message the watcher received
// before the backend went away is still the member's to release while it
// is away. The slow wait applies the release file on its tick, the frame
// reaches the session with no watch child running, and the acknowledgement
// waits for the child that follows the outage.
func TestSlowWaitAppliesARelease(t *testing.T) {
	t.Parallel()
	const body = "held before the outage 7f3a"
	fx := newFixture(t, fixtureOptions{inbound: protocol.InboundHold})
	gate := t.TempDir() + "/gate"
	fx.useHelper("gate=" + gate)
	fx.writeMap()
	fx.forbidBody(body)
	openGate(t, gate, rawLine(t, &protocol.WatchMessage{Event: protocol.EventMessage, Message: fakeMessage("m1", "sender-a", body)}))
	deps := fx.deps()
	deps.GiveUpFailures = 3
	deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(time.Hour, time.Hour, seeded(10)) }
	deps.ActivityRetryGap = 20 * time.Millisecond
	r := fx.start(deps)
	waitHeld(t, fx, "m1")

	closeGate(t, gate)
	fx.waitLog("watch child failed; restarting", map[string]any{"slow": true})
	ended := fx.logCount("watch child ended", nil)
	fx.writeRelease(fx.sessionID, "m1")
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], `message-id="m1"`) || !strings.Contains(frames[0], body) {
		t.Fatalf("frames = %q, want the released message", frames)
	}
	if n := fx.logCount("watch child ended", nil); n != ended {
		t.Errorf("a watch child ran during the wait (%d attempts, want %d): the release did not come from the wait", n, ended)
	}
	if fx.logHas("ack sent", nil) {
		t.Errorf("an ack was sent with no watch child running")
	}

	// The backend is back; the session's activity cuts the hour short, and
	// the child that starts takes the acknowledgement that waited.
	openGate(t, gate)
	fx.writeRegistry("receiver", "busy", fx.sock.Path())
	fx.waitLog("ack sent", map[string]any{"ids": 1})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := len(fx.sock.WaitFrames(1, waitShort)); n != 1 {
		t.Errorf("frames = %d, want 1: the released message was injected again", n)
	}
}

// TestHealthyRunThatEndedEndsTheSlowSchedule: the event loop says
// reconnected when its timer fires, and the supervisor says it for the run
// that ended before the timer was served — a run that was ready for
// HealthyAfter by the clock resets the give-up rule either way, and a
// watcher whose rule was reset must not stay on the slow pace. The clock
// here jumps an hour a reading once the watcher is slow, so the next run
// is healthy by the clock while the loop's own timer, half an hour of real
// time, never fires.
func TestHealthyRunThatEndedEndsTheSlowSchedule(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useHelper("ready-exit=9")
	fx.writeMap()
	deps := fx.deps()
	deps.GiveUpFailures = 3
	deps.HealthyAfter = 30 * time.Minute
	deps.SlowSchedule = func() *backoff.Schedule { return backoff.New(60*time.Millisecond, 60*time.Millisecond, seeded(11)) }
	var (
		mu    sync.Mutex
		now   = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
		jumps bool
	)
	deps.Clock = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if jumps {
			now = now.Add(time.Hour)
		} else {
			now = now.Add(time.Millisecond)
		}
		return now
	}
	r := fx.start(deps, fx.args()...)
	fx.waitLog("watch child failed; restarting", map[string]any{"slow": true})
	fastBefore := fx.restarts(false)
	mu.Lock()
	jumps = true
	mu.Unlock()

	fx.waitLog("watcher reconnected", nil)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return strings.HasPrefix(fx.notice(), backNoticePrefix) })
	// The pace is the fast one again: the run that reconnected ended too,
	// and its restart is a fast one.
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return fx.restarts(false) > fastBefore })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
