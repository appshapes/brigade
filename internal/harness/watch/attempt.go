package watch

import (
	"context"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/protocol"
)

// writerStopWait bounds how long an attempt waits for its command writer
// after the child has been reaped (the writer's blocked pipe write fails
// once Wait closed the pipe; this is only a hang catcher).
const writerStopWait = 10 * time.Second

// A session is one running watch child with the facts the loop needs.
// Commands to the child — acks, heartbeats, the close — are written by
// ONE writer goroutine (commandLoop) so the event loop never blocks on a
// child that is not reading its stdin (an adapter replaying a long
// catch-up reads commands only afterwards): a full pipe stalls the writer,
// never the events, the liveness tick or the exit path.
type session struct {
	watch         *adapterclient.Watch
	cancel        context.CancelFunc
	stdinCommands bool
	lease         *int

	hbReq    chan struct{} // a heartbeat is due (capacity 1)
	closeReq chan struct{} // the exit path asked for the close (capacity 1)
	done     chan struct{} // closed when the writer returns
	// heartbeatsStopped is set by the exit path BEFORE the close is
	// requested (U-21): a heartbeat already queued is dropped, not sent.
	heartbeatsStopped atomic.Bool
}

func newSession(wt *adapterclient.Watch, cancel context.CancelFunc, stdinCommands bool, lease *int) *session {
	return &session{
		watch: wt, cancel: cancel, stdinCommands: stdinCommands, lease: lease,
		hbReq: make(chan struct{}, 1), closeReq: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

// requestHeartbeat asks the writer for a heartbeat without blocking.
func (s *session) requestHeartbeat() {
	select {
	case s.hbReq <- struct{}{}:
	default:
	}
}

// requestClose stops heartbeats and asks the writer for the close.
func (s *session) requestClose() {
	s.heartbeatsStopped.Store(true)
	select {
	case s.closeReq <- struct{}{}:
	default:
	}
}

// attempt runs one watch child: describe (cached), StartWatch, then the
// event loop until the stream ends, the ready timeout fires, a fatal
// error event outlives its grace, or the watcher's own exit path runs.
// Watch.Wait is ALWAYS called (it reaps the child and closes the adapter
// log).
func (w *watcher) attempt() attemptResult {
	r := attemptResult{started: w.deps.Clock()}
	dctx, dcancel := context.WithTimeout(w.ctx, adapterclient.DescribeTimeout)
	desc, err := w.client.Describe(dctx)
	dcancel()
	if err != nil {
		r.startErr = err
		r.ended = w.deps.Clock()
		w.logAttemptEnd(r)
		return r
	}
	// The child's context is NOT the watcher's: on a stop the exit path
	// sends `close` first and cancels only afterwards.
	wctx, wcancel := context.WithCancel(context.WithoutCancel(w.ctx))
	wt, err := w.client.StartWatch(wctx, w.sessionID)
	if err != nil {
		wcancel()
		r.startErr = err
		r.ended = w.deps.Clock()
		w.logAttemptEnd(r)
		return r
	}
	s := newSession(wt, wcancel, slices.Contains(desc.Capabilities, StdinCommandsCapability), chooseLease(desc.Lease))
	w.log.Info("watch child started", slog.Bool("stdin_commands", s.stdinCommands))

	cctx, ccancel := context.WithCancel(context.Background())
	go w.commandLoop(cctx, s)
	w.injector.signalPendingAcks()

	w.eventLoop(s, &r)

	wcancel()
	r.exitCode, r.waitErr = wt.Wait()
	ccancel()
	select {
	case <-s.done:
	case <-time.After(writerStopWait):
		w.log.Warn("command writer did not stop after the child was reaped")
	}
	r.ended = w.deps.Clock()
	w.logAttemptEnd(r)
	return r
}

// chooseLease is the lease_seconds a heartbeat asks for: DefaultLeaseSeconds
// when the adapter's advertised range allows it, else nil (the adapter's
// own default; never a value outside its range).
func chooseLease(l protocol.Lease) *int {
	if l.MinSeconds > 0 && l.MaxSeconds > 0 && (DefaultLeaseSeconds < l.MinSeconds || DefaultLeaseSeconds > l.MaxSeconds) {
		return nil
	}
	v := DefaultLeaseSeconds
	return &v
}

// eventLoop is the attempt's select loop.
func (w *watcher) eventLoop(s *session, r *attemptResult) {
	readyTimer := time.NewTimer(w.deps.ReadyTimeout)
	defer readyTimer.Stop()
	liveTick := time.NewTicker(w.deps.PollInterval)
	defer liveTick.Stop()
	hbTick := time.NewTicker(w.deps.HeartbeatInterval)
	defer hbTick.Stop()
	var fatal <-chan time.Time

	for {
		select {
		case ev, ok := <-s.watch.Events():
			if !ok {
				return
			}
			if grace := w.handleEvent(s, r, ev); grace && fatal == nil {
				fatal = time.After(w.deps.FatalExitGrace)
			}
		case <-readyTimer.C:
			if r.ready.IsZero() {
				w.log.Warn("no ready event in time", slog.Duration("timeout", w.deps.ReadyTimeout))
				r.readyTimeout = true
				s.cancel()
				return
			}
		case <-fatal:
			w.log.Warn("child did not exit after a fatal error event; stopping it",
				slog.String("code", string(r.lastErrorCode)))
			r.fatalCode = r.lastErrorCode
			s.cancel()
			return
		case <-liveTick.C:
			if reason := w.checkLiveness(); reason != "" {
				w.stop(reason)
				w.shutdown(s, r)
				return
			}
			if w.state.takeFlip() {
				s.requestHeartbeat()
			}
			// The hold policy's release file rides the same tick (3.6):
			// no goroutine, no knob, no new deadline, and the same
			// "within 2 s" every other reaction already promises.
			w.applyRelease()
		case <-hbTick.C:
			s.requestHeartbeat()
		case <-w.ctx.Done():
			w.reasonOfStop()
			w.shutdown(s, r)
			return
		}
	}
}

// handleEvent applies one event. It returns true when a fatal-grace timer
// should start (an `error` flagged retryable: false, C-37).
func (w *watcher) handleEvent(s *session, r *attemptResult, ev adapterclient.Event) bool {
	switch ev.Kind {
	case adapterclient.KindReady:
		r.ready = w.deps.Clock()
		w.log.Info("watch ready", slog.String("mode", ev.Ready.Mode), slog.String("session_id", ev.Ready.SessionID))
		if ev.Ready.Mode == protocol.WatchModePolling {
			w.log.Info("adapter polls; drain latency is two poll intervals")
		}
		// The first heartbeat carries the current name and activity at
		// once, so a renamed or busy session is reported without waiting
		// a full interval; a release file written while no watcher ran is
		// applied at once for the same reason.
		s.requestHeartbeat()
		w.applyRelease()
	case adapterclient.KindMessage:
		w.offer(*ev.Message)
	case adapterclient.KindStatus:
		w.log.Info("watch status", slog.String("state", ev.Status.State), slog.String("detail", ev.Status.Detail))
	case adapterclient.KindAcked:
		w.log.Debug("acked", slog.Int("acked", len(ev.Acked.MessageIDs)), slog.Int("unknown", len(ev.Acked.Unknown)))
	case adapterclient.KindHeartbeatOK:
		w.log.Debug("heartbeat_ok", slog.String("state", ev.HeartbeatOK.State))
	case adapterclient.KindError:
		r.lastErrorCode = ev.Error.Code
		retryable := backoff.Retryable(ev.Error.Code)
		w.log.Warn("watch error event",
			slog.String("code", string(ev.Error.Code)),
			slog.Bool("wire_retryable", ev.Error.Retryable),
			slog.Bool("retryable", retryable),
			slog.String("message", ev.Error.Message))
		// A closed or vanished session is re-opened by the watcher itself
		// (reopen.go), off the event loop: the child stays, the next
		// heartbeat follows the re-open.
		if sessionGone(ev.Error.Code, ev.Error.Details) {
			w.log.Info("the session was closed under the watcher; re-opening", slog.String("code", string(ev.Error.Code)))
			go w.reopen(s)
		}
		// The wire flag says whether the CHILD will exit (4.4.9); the
		// grace timer only bounds an adapter that then does not. The
		// classification itself is by code (4.3), on the exit status.
		return !ev.Error.Retryable
	}
	return false
}

// offer runs one message through the pipeline and wakes the injector.
func (w *watcher) offer(m protocol.MessageEnvelope) {
	d := w.pipeline.Offer(m)
	w.log.Debug("message offered",
		slog.String("message_id", d.MessageID),
		slog.String("outcome", string(d.Outcome)),
		slog.String("reason", d.Reason),
		slog.Bool("ack", d.Ack),
		slog.String("dropped", d.Dropped),
		slog.String("sender_session_id", m.Sender.SessionID))
	if d.Ack {
		w.injector.queueAck(d.MessageID)
	}
	w.injector.kickNow()
}

// commandLoop is the session's command writer: acks as they become
// pending, heartbeats when requested, then the close, in one goroutine.
func (w *watcher) commandLoop(ctx context.Context, s *session) {
	defer close(s.done)
	for {
		// The close wins over anything else that is ready.
		select {
		case <-s.closeReq:
			w.sendClose(s)
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-s.closeReq:
			w.sendClose(s)
			return
		case <-w.injector.ackReady:
			w.sendAcks(s)
		case <-s.hbReq:
			if !s.heartbeatsStopped.Load() {
				w.heartbeat(s)
			}
		}
	}
}

// sendAcks sends every pending ack in ONE command: on the child's stdin
// when the adapter takes commands there, else one `message ack` call with
// the 3 s budget. A failure keeps the ids for a later batch (the next
// queued ack or the next attempt re-signals; no hot retry).
func (w *watcher) sendAcks(s *session) {
	ids := w.injector.takeAcks()
	if len(ids) == 0 {
		return
	}
	var err error
	if s.stdinCommands {
		err = s.watch.Ack(ids)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), adapterclient.WatchRequestTimeout)
		_, err = w.client.Ack(ctx, w.sessionID, &protocol.AckRequest{MessageIDs: ids})
		cancel()
	}
	if err != nil {
		w.log.Warn("ack failed; kept for a later batch", slog.Int("ids", len(ids)), adlog.Err(err))
		w.injector.requeueAcks(ids)
		return
	}
	w.log.Info("ack sent", slog.Int("ids", len(ids)), slog.Bool("stdin", s.stdinCommands))
}

// sendClose closes the Brigade session: a `close` command on the child's
// stdin (the child exits 0 by itself), else `session close` with the
// budget the exit path chose.
func (w *watcher) sendClose(s *session) {
	if s.stdinCommands {
		if err := s.watch.Close(); err != nil {
			w.log.Warn("close command not written", adlog.Err(err))
		}
		return
	}
	budget := w.deps.CloseWaitClean
	if w.reasonOfStop() == "claude_gone" {
		budget = w.deps.CloseWaitDeath
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if _, err := w.client.Close(ctx, w.sessionID); err != nil {
		w.log.Warn("session close failed", adlog.Err(err))
		return
	}
	w.log.Info("session closed")
}

// shutdown is the exit path of 6.6 as corrected by E0-5: heartbeats stop
// first (U-21), then `close` on the child's stdin (or `session close`),
// 1 s after a clean stop and 3 s when the Claude process died; then the
// child is closed (a `close` makes it exit 0 by itself; otherwise stdin
// EOF, SIGTERM and SIGKILL after 5 s through Wait).
func (w *watcher) shutdown(s *session, r *attemptResult) {
	r.stopped = true
	r.closed = true
	s.heartbeatsStopped.Store(true)
	reason := w.reasonOfStop()
	budget := w.deps.CloseWaitClean
	if reason == "claude_gone" {
		budget = w.deps.CloseWaitDeath
	}
	w.log.Info("closing the session", slog.String("reason", reason), slog.Duration("budget", budget))
	w.injector.stopDraining()
	s.requestClose()

	// Let the child end its stream on its own within the budget; events
	// that still arrive are dropped unacknowledged (the server redelivers).
	deadline := time.After(budget)
	for {
		select {
		case _, ok := <-s.watch.Events():
			if !ok {
				w.log.Info("watch child ended after close")
				return
			}
		case <-deadline:
			w.log.Warn("watch child did not end after close; stopping it")
			s.cancel()
			return
		}
	}
}

// stateForLog summarises the pipeline for the exit line.
func (w *watcher) stateForLog() []slog.Attr {
	st := w.pipeline.Stats()
	return []slog.Attr{
		slog.Int("queued", st.Queued), slog.Int("pending", st.Pending), slog.Int("senders", st.Senders),
		slog.Int("deferrals", st.Deferrals), slog.Int("seen", st.Seen), slog.Int("notices", st.Notices),
		slog.Int("held", st.Held),
	}
}

// itemKindForLog names an item for the log.
func itemKindForLog(it inbound.Item) string { return it.Kind.String() }
