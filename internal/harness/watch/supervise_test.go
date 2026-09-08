package watch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// dumpCount counts the fake adapter's recorded invocations of a verb.
func dumpCount(t *testing.T, path, group, verb string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.Contains(line, `"group":"`+group+`"`) && strings.Contains(line, `"verb":"`+verb+`"`) {
			n++
		}
	}
	return n
}

// TestReadyTimeoutRestartsWithBackoff: a child that never says ready
// within ReadyTimeout is stopped and restarted on the schedule; the
// fake's dump file counts the restarts, the log names the reason.
func TestReadyTimeoutRestartsWithBackoff(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	late := readyLine(t)
	late.DelayMS = 60_000 // never within the injected ReadyTimeout
	fx.useFake(fakeadapter.Script{
		DumpFile: dump,
		Responses: map[string][]fakeadapter.Response{
			"session close": {{Result: []byte(`{"session_id":"s1","state":"offline"}`)}},
		},
		Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{late}},
	})
	fx.writeMap()
	deps := fx.deps()
	deps.ReadyTimeout = 150 * time.Millisecond
	deps.GiveUpFailures = 1000
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return dumpCount(t, dump, "message", "watch") >= 3
	})
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("watch child failed; restarting", map[string]any{"why": "ready_timeout", "code": "unavailable"}) >= 2
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d, want 0 after a stop", code)
	}
	if _, err := os.Stat(fx.noticePath()); err == nil {
		t.Errorf("a restart wrote a notice; only a stop or the give-up does")
	}
}

// TestStopDuringRestartDelayClosesTheSession: a stop that lands while no
// watch child is running (the restart delay) still closes the Brigade
// session, through one one-shot `session close`, and exits 0.
func TestStopDuringRestartDelayClosesTheSession(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	fx.useFake(fakeadapter.Script{
		DumpFile: dump,
		Responses: map[string][]fakeadapter.Response{
			"session close": {{Result: []byte(`{"session_id":"s1","state":"offline"}`)}},
		},
		// No ready line: every attempt times out.
		Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{{Raw: rawLine(t, &protocol.WatchStatus{Event: protocol.EventStatus, State: protocol.StatusStatePolling}), DelayMS: 60_000}}},
	})
	fx.writeMap()
	deps := fx.deps()
	deps.ReadyTimeout = 100 * time.Millisecond
	// A long restart delay: the stop lands inside it, with no child.
	deps.RestartSchedule = func() *backoff.Schedule { return backoff.New(time.Hour, time.Hour, seeded(3)) }
	r := fx.start(deps, fx.args()...)
	fx.waitLog("watch child failed; restarting", map[string]any{"why": "ready_timeout"})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !fx.logHas("closing the session without a watch child", map[string]any{"reason": "stop"}) || !fx.logHas("session closed", nil) {
		t.Errorf("log: %v", fx.logLines())
	}
	if n := dumpCount(t, dump, "session", "close"); n != 1 {
		t.Errorf("session close children = %d, want 1", n)
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present: %v", err)
	}
}

// TestGiveUpAfterTenFailures: an adapter whose watch exits 9 at once is
// restarted with backoff and, after DefaultGiveUpFailures consecutive
// failures inside the window, the watcher exits ExitGaveUp (3) with the
// notice and its pidfile removed.
func TestGiveUpAfterTenFailures(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useHelper("exit=9")
	fx.writeMap()
	r := fx.start(fx.deps(), fx.args()...)
	if code := r.wait(); code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if n := fx.logCount("watch child failed; restarting", map[string]any{"code": "unavailable", "why": "child_exit", "exit": 9}); n != 9 {
		t.Errorf("restart lines = %d, want 9 (ten failures, nine restarts): %v", n, fx.logLines())
	}
	if !fx.logHas("watcher giving up", map[string]any{"failures": 10}) {
		t.Errorf("no give-up line: %v", fx.logLines())
	}
	notice, err := os.ReadFile(fx.noticePath())
	if err != nil {
		t.Fatalf("notice: %v", err)
	}
	if string(notice) != "Brigade: watcher gave up after 10 failures within 5m0s; it will be restarted at your next prompt\n" {
		t.Errorf("notice = %q", notice)
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present: %v", err)
	}
}

// TestHealthyRunResetsTheFailureCount: a child that was ready for at
// least HealthyAfter before ending starts a fresh failure sequence, so the
// give-up rule never trips on a watch that keeps recovering; the control
// arm (HealthyAfter unreachable) gives up.
func TestHealthyRunResetsTheFailureCount(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, healthyAfter time.Duration) (*fixture, int, bool) {
		t.Helper()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useHelper("ready-exit=9")
		fx.writeMap()
		deps := fx.deps()
		deps.GiveUpFailures = 3
		deps.HealthyAfter = healthyAfter
		r := fx.start(deps, fx.args()...)
		testutil.Eventually(t, waitLong, pollEvery, func() bool {
			return r.exited() || fx.logCount("watch child failed; restarting", nil) >= 6
		})
		if r.exited() {
			return fx, r.wait(), true
		}
		return fx, r.stopAndWait(), false
	}
	fx, code, gaveUp := run(t, time.Nanosecond)
	if gaveUp || code != 0 {
		t.Fatalf("with HealthyAfter=1ns the watcher gave up (exit %d): %v", code, fx.logLines())
	}
	fx, code, gaveUp = run(t, time.Hour)
	if !gaveUp || code != 3 {
		t.Fatalf("control: with HealthyAfter=1h the watcher did not give up (exit %d, exited %v)", code, gaveUp)
	}
	if !fx.logHas("watcher giving up", map[string]any{"failures": 3}) {
		t.Errorf("control log: %v", fx.logLines())
	}
}

// TestUnauthorizedStopsWithNotice: a revoked member's watch ends with one
// `unauthorized` event and exit 5 (the fs adapter re-checks membership
// every poll); the watcher stops with exit 5, the notice names `brigade
// team join`, the pidfile is gone. Nothing is restarted.
func TestUnauthorizedStopsWithNotice(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMap()
	r := fx.start(fx.deps())
	fx.waitLog("watch ready", nil)
	ctx, cancel := context.WithTimeout(t.Context(), waitShort)
	defer cancel()
	if _, err := fx.owner.Call(ctx, "team", "leave", nil, nil); err != nil {
		t.Fatalf("team leave: %v", err)
	}
	if code := r.wait(); code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if !fx.logHas("watch error event", map[string]any{"code": "unauthorized"}) {
		t.Errorf("no unauthorized event in the log: %v", fx.logLines())
	}
	if !fx.logHas("watcher stopping", map[string]any{"code": "unauthorized", "exit": 5}) {
		t.Errorf("no stopping line: %v", fx.logLines())
	}
	if fx.logHas("watch child failed; restarting", nil) {
		t.Errorf("a non-retryable exit was restarted")
	}
	notice, err := os.ReadFile(fx.noticePath())
	if err != nil {
		t.Fatalf("notice: %v", err)
	}
	if string(notice) != "Brigade: watcher stopped: unauthorized; run `brigade team join` again\n" {
		t.Errorf("notice = %q", notice)
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present: %v", err)
	}
}

// TestProtocolMismatchStops: a describe with protocol_version "2" is
// protocol_mismatch (exit 10) before any watch child runs, with its own
// notice wording.
func TestProtocolMismatchStops(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	fx.useFake(fakeadapter.Script{Describe: fakeadapter.DescribeJSON("2"), DumpFile: dump})
	fx.writeMap()
	r := fx.start(fx.deps(), fx.args()...)
	if code := r.wait(); code != 10 {
		t.Fatalf("exit %d, want 10", code)
	}
	if dumpCount(t, dump, "message", "watch") != 0 {
		t.Errorf("a watch child ran despite the protocol mismatch")
	}
	notice, _ := os.ReadFile(fx.noticePath())
	if !strings.HasPrefix(string(notice), "Brigade: watcher stopped: protocol_mismatch; ") {
		t.Errorf("notice = %q", notice)
	}
}

// TestRetryableErrorEventContinues: an `error` event with a retryable
// code is logged and the watch goes on — the message after it is still
// injected and the child is not restarted.
func TestRetryableErrorEventContinues(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	fx.useFake(fakeadapter.Script{DumpFile: dump, Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{
		readyLine(t),
		{Raw: rawLine(t, &protocol.WatchError{Event: protocol.EventError, Error: protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "blip", Retryable: true}})},
		// A rejected stdin command's shape: invalid_input flagged retryable.
		{Raw: rawLine(t, &protocol.WatchError{Event: protocol.EventError, Error: protocol.ErrorObject{Code: protocol.CodeInvalidInput, Message: "bad lease", Retryable: true}})},
		messageLine(t, fakeMessage("m1", "sender-a", "after the blip"), 50),
	}}})
	fx.writeMap()
	r := fx.start(fx.deps())
	frames := fx.sock.WaitFrames(1, waitShort)
	if len(frames) != 1 || !strings.Contains(frames[0], "after the blip") {
		t.Fatalf("frames = %q", frames)
	}
	if n := fx.logCount("watch error event", nil); n != 2 {
		t.Errorf("error event lines = %d, want 2", n)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if dumpCount(t, dump, "message", "watch") != 1 {
		t.Errorf("watch children = %d, want 1 (no restart)", dumpCount(t, dump, "message", "watch"))
	}
}

// TestFatalErrorEventWithoutExitIsStopped: an `error` flagged
// retryable: false whose child does not exit within FatalExitGrace
// (C-37) is stopped by the watcher and classified by its code — a
// non-retryable unauthenticated stops the watcher with exit 4 and the
// team-join notice.
func TestFatalErrorEventWithoutExitIsStopped(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{
		readyLine(t),
		{Raw: rawLine(t, &protocol.WatchError{Event: protocol.EventError, Error: protocol.ErrorObject{Code: protocol.CodeUnauthenticated, Message: "revoked", Retryable: false}})},
	}}})
	fx.writeMap()
	deps := fx.deps()
	deps.FatalExitGrace = 200 * time.Millisecond
	r := fx.start(deps, fx.args()...)
	if code := r.wait(); code != 4 {
		t.Fatalf("exit %d, want 4", code)
	}
	if !fx.logHas("child did not exit after a fatal error event; stopping it", map[string]any{"code": "unauthenticated"}) {
		t.Errorf("log: %v", fx.logLines())
	}
	notice, _ := os.ReadFile(fx.noticePath())
	if string(notice) != "Brigade: watcher stopped: unauthenticated; run `brigade team join` again\n" {
		t.Errorf("notice = %q", notice)
	}
}

// TestRateLimitedExitRestarts: exit 8 is restarted (with the code named),
// the positive control of the stop rows above.
func TestRateLimitedExitRestarts(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useHelper("ready-exit=8")
	fx.writeMap()
	deps := fx.deps()
	deps.GiveUpFailures = 1000
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return fx.logCount("watch child failed; restarting", map[string]any{"code": "rate_limited", "exit": 8}) >= 3
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
