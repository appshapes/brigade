package watch

import (
	"context"
	"log/slog"
	"time"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/protocol"
)

// An attemptResult is what one run of the watch child ended with.
type attemptResult struct {
	// started and ended bound the attempt; ready is when the ready event
	// arrived (zero when it never did).
	started, ended, ready time.Time
	// stopped is true when the watcher's own exit path ran (a signal, the
	// stop channel or a liveness verdict): the supervisor returns 0.
	stopped bool
	// startErr is a describe or StartWatch failure (no child ran).
	startErr error
	// readyTimeout is true when no ready event arrived in ReadyTimeout.
	readyTimeout bool
	// fatalCode is the code of an `error` event flagged retryable: false
	// whose child did not exit on its own within FatalExitGrace.
	fatalCode protocol.Code
	// lastErrorCode is the code of the last `error` event, whatever its
	// flag: it names the failure when the child then exits 0 or by signal.
	lastErrorCode protocol.Code
	// exitCode and waitErr are Watch.Wait's answer once a child ran.
	exitCode int
	waitErr  error
	// closed is true when the watcher itself asked the child to close.
	closed bool
}

// supervise runs watch attempts until the session is over, the child
// fails for a reason no restart fixes, or the give-up rule trips on a
// failure the slow schedule does not take. It returns the exit status.
//
// The give-up rule used to end the watcher whatever the failure was, and
// the only respawn is the next prompt (hook/prompt.go ensureWatcher): a
// session left idle for a hand-off stopped receiving after one network
// outage of a few minutes and stayed offline until a person typed. So a
// failure a restart can fix — `unavailable`, `rate_limited`
// (backoff.Retryable) — now moves the supervisor to the slow schedule
// instead (card 34): one notice, an attempt at least every
// backoff.WatchSlowRetry, and the fast schedule again once a child has
// been ready for HealthyAfter (recovered, which the event loop calls).
func (w *watcher) supervise() int {
	restart := w.deps.RestartSchedule()
	slow := w.deps.SlowSchedule()
	var failures []time.Time
	for {
		if w.stopping() {
			w.closeWithoutChild()
			return protocol.ExitOK
		}
		r := w.attempt()
		if r.stopped {
			return protocol.ExitOK
		}
		if w.stopping() {
			// The stop arrived while no child could take the close (during
			// describe or start, or just as the child ended by itself).
			w.closeWithoutChild()
			return protocol.ExitOK
		}
		v := classify(r)
		if !r.ready.IsZero() && r.ended.Sub(r.ready) >= w.deps.HealthyAfter {
			// A run that was up long enough is a fresh start for the
			// give-up rule and the schedule. The event loop has said so
			// already when its own timer fired first; this is the run that
			// ended on the very tick.
			failures = failures[:0]
			restart.Reset()
			w.recovered()
		}
		if v.stop {
			w.log.Error("watcher stopping",
				slog.String("code", string(v.code)), slog.String("why", v.why), slog.Int("exit", r.exitCode))
			w.writeNotice(stopNotice(v.code))
			return v.code.Exit()
		}
		now := w.deps.Clock()
		failures = pruneFailures(append(failures, now), now.Add(-w.deps.GiveUpWindow))
		if w.degradedSince.IsZero() && len(failures) >= w.deps.GiveUpFailures {
			if !backoff.Retryable(v.code) {
				w.log.Error("watcher giving up",
					slog.Int("failures", len(failures)), slog.Duration("window", w.deps.GiveUpWindow))
				w.writeNotice(giveUpNotice(len(failures), w.deps.GiveUpWindow))
				return ExitGaveUp
			}
			w.degradedSince = now
			w.log.Warn("watcher retrying slowly",
				slog.String("code", string(v.code)), slog.String("why", v.why),
				slog.Int("failures", len(failures)), slog.Duration("window", w.deps.GiveUpWindow),
				slog.Duration("every", slow.Max()))
			w.writeNotice(slowRetryNotice(v.code, slow.Max()))
		}
		delay := restart.Next()
		if !w.degradedSince.IsZero() {
			delay = slow.Next()
		}
		w.log.Warn("watch child failed; restarting",
			slog.String("code", string(v.code)), slog.String("why", v.why), slog.Int("exit", r.exitCode),
			slog.Duration("delay", delay), slog.Int("consecutive_failures", len(failures)),
			slog.Bool("slow", !w.degradedSince.IsZero()))
		if !w.waitRestart(delay) {
			w.closeWithoutChild()
			return protocol.ExitOK
		}
	}
}

// waitRestart waits out the delay before the next attempt and reports
// whether there is one: false means the watcher is stopping.
//
// A fast delay is at most the schedule's 30 s and is simply waited. A slow
// one is minutes, and no child runs an event loop meanwhile, so the wait
// keeps that loop's liveness tick itself: the watcher still ends within
// one tick of the Claude process or the by-pid map going, and a release
// file is still applied within one. The tick also reads the session's
// activity, and a change of it — a prompt, the end of a turn — cuts the
// wait short once ActivityRetryGap of it has passed: the watcher that used
// to exit was respawned by the next prompt, and the one that stays must
// not make a person at the keyboard wait out the rest of five minutes.
func (w *watcher) waitRestart(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if w.degradedSince.IsZero() {
		select {
		case <-w.ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	tick := time.NewTicker(w.deps.PollInterval)
	defer tick.Stop()
	began, active := time.Now(), false
	for {
		select {
		case <-w.ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-tick.C:
			if reason := w.checkLiveness(); reason != "" {
				w.stop(reason)
				return false
			}
			w.applyRelease()
			if w.state.takeFlip() {
				active = true
			}
			if active && time.Since(began) >= w.deps.ActivityRetryGap {
				w.log.Info("the session is active; trying the watch child now")
				return true
			}
		}
	}
}

// recovered ends the slow schedule: a child has been ready for
// HealthyAfter. It says so once, in the log and in the notice the next
// prompt prints, and does nothing for a watcher that was never slow.
func (w *watcher) recovered() {
	if w.degradedSince.IsZero() {
		return
	}
	away := w.deps.Clock().Sub(w.degradedSince)
	w.degradedSince = time.Time{}
	w.log.Info("watcher reconnected", slog.Duration("after", away.Truncate(time.Second)))
	w.writeNotice(reconnectedNotice(away))
}

// closeWithoutChild is the exit path when no watch child is running to
// take the `close` command (a stop during a restart delay, a describe or
// a start): one `session close` call with the exit path's budget, so the
// Brigade session does not wait for its lease to expire.
func (w *watcher) closeWithoutChild() {
	reason := w.reasonOfStop()
	budget := w.deps.CloseWaitClean
	if reason == "claude_gone" {
		budget = w.deps.CloseWaitDeath
	}
	w.log.Info("closing the session without a watch child", slog.String("reason", reason), slog.Duration("budget", budget))
	w.injector.stopDraining()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if _, err := w.client.Close(ctx, w.sessionID); err != nil {
		w.log.Warn("session close failed", adlog.Err(err))
		return
	}
	w.log.Info("session closed")
}

// logAttemptEnd writes the one line that summarises how a child ended.
func (w *watcher) logAttemptEnd(r attemptResult) {
	attrs := []slog.Attr{
		slog.Int("exit", r.exitCode),
		slog.Bool("ready", !r.ready.IsZero()),
		slog.Bool("closed", r.closed),
		slog.Duration("uptime", r.ended.Sub(r.started)),
	}
	if r.waitErr != nil {
		attrs = append(attrs, adlog.Err(r.waitErr))
	}
	if r.startErr != nil {
		attrs = append(attrs, adlog.Err(r.startErr))
	}
	if r.lastErrorCode != "" {
		attrs = append(attrs, slog.String("last_error_code", string(r.lastErrorCode)))
	}
	w.log.LogAttrs(w.ctx, slog.LevelInfo, "watch child ended", attrs...)
}
