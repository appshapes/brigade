package watch

import (
	"context"
	"log/slog"
	"time"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
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
// fails for a reason no restart fixes, or the give-up rule trips. It
// returns the exit status.
func (w *watcher) supervise() int {
	restart := w.deps.RestartSchedule()
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
			// give-up rule and the schedule.
			failures = failures[:0]
			restart.Reset()
		}
		if v.stop {
			w.log.Error("watcher stopping",
				slog.String("code", string(v.code)), slog.String("why", v.why), slog.Int("exit", r.exitCode))
			w.writeNotice(stopNotice(v.code))
			return v.code.Exit()
		}
		now := w.deps.Clock()
		failures = pruneFailures(append(failures, now), now.Add(-w.deps.GiveUpWindow))
		if len(failures) >= w.deps.GiveUpFailures {
			w.log.Error("watcher giving up",
				slog.Int("failures", len(failures)), slog.Duration("window", w.deps.GiveUpWindow))
			w.writeNotice(giveUpNotice(len(failures), w.deps.GiveUpWindow))
			return ExitGaveUp
		}
		delay := restart.Next()
		w.log.Warn("watch child failed; restarting",
			slog.String("code", string(v.code)), slog.String("why", v.why), slog.Int("exit", r.exitCode),
			slog.Duration("delay", delay), slog.Int("consecutive_failures", len(failures)))
		select {
		case <-w.ctx.Done():
			w.closeWithoutChild()
			return protocol.ExitOK
		case <-time.After(delay):
		}
	}
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
