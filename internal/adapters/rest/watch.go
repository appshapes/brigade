package rest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// `message watch` (4.4.9, C-33..C-41, C-08) as the loop of the contract's
// section 4 over /v1/message/receive:
//
//   - the catch-up: one receive without `after`. It is also the ownership
//     check of 4.5.7, so a foreign or unknown session is refused before
//     `ready` with the server's uniform `not_found` (C-37);
//   - long polls: receives with `after` (the cursor of the last answer)
//     and `wait_seconds`. The server holds each until a message is accepted
//     for the session and answers within a second of it, which is why this
//     adapter advertises message.watch.push and says mode push in `ready`;
//   - a periodic drain: a receive without `after` every timing.drain, after
//     which every message that is still unacknowledged is emitted again,
//     once (4.4.9). Between two drains an id is emitted once.
//
// One request is in flight at a time and it runs beside the loop, so a
// stdin command is answered while a long poll waits. The stdin commands
// ack, heartbeat and close are each their own request; a rejected command
// is an `error` event with retryable true and the watch goes on; stdin
// EOF, `close` and SIGTERM end it with exit 0 within 5 s, cancelling the
// request in flight.

// timing holds the watch's intervals. It is a value the tests replace
// through options; defaultTiming is production's.
type timing struct {
	// wait is how long a long poll asks the server to hold a request: the
	// contract allows 30 s, and 25 s stays under the 30 s at which common
	// proxies and load balancers cut an idle request. It is sent as whole
	// seconds, rounded up.
	wait time.Duration
	// drain is the periodic drain's interval: 30 s, the Supabase adapter's
	// interval while its channel is up. The long polls are the fast path,
	// so this timer only bounds how long a message a server failed to hand
	// to a waiting request stays unseen, and how often an unacknowledged
	// one is emitted again. It is the adapter's policy, not the protocol's.
	drain time.Duration
	// slack is what a long poll's own deadline adds to its wait, for the
	// connection and the answer.
	slack time.Duration
	// pace is the least time between the starts of two long polls when the
	// first brought nothing new. A server that keeps the contract answers
	// an empty long poll only after the wait, so this never delays one; it
	// is what keeps a server that answers at once, with nothing or with
	// what was already emitted, from being asked in a tight loop.
	pace time.Duration
	// backoff is the wait before the next request after a retryable
	// failure, by the count of consecutive failures.
	backoff []time.Duration
	// outage is how long consecutive failures are tolerated before the
	// watch gives up with `unavailable`.
	outage time.Duration
	// closeTimeout bounds /v1/session/close for a `close` command, so the
	// exit stays inside 5 s.
	closeTimeout time.Duration
	// shutdown bounds the wait for the request in flight to end at exit.
	shutdown time.Duration
}

func defaultTiming() timing {
	return timing{
		wait:         25 * time.Second,
		drain:        30 * time.Second,
		slack:        15 * time.Second,
		pace:         time.Second,
		backoff:      []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second},
		outage:       30 * time.Minute,
		closeTimeout: 4 * time.Second,
		shutdown:     2 * time.Second,
	}
}

// maxRetryAfter caps the wait a `rate_limited` answer can ask of the watch.
const maxRetryAfter = time.Minute

// watch implements `message watch`. The SIGTERM handler is installed
// before anything is written, before `ready` in particular, so a harness
// that signals the instant it sees `ready` (C-38) never hits the default
// disposition (the fs adapter's measured CI failure).
func (c *command) watch() int {
	ctx, stop := signal.NotifyContext(c.ctx, syscall.SIGTERM)
	defer stop()
	c.ctx = ctx
	events := protocol.NewLineWriter(c.stdout)
	fs := newFlags()
	session := fs.String("session", "", "the session to watch")
	if err := c.parse(fs); err != nil {
		return c.watchFatal(events, err)
	}
	id, err := c.requireSessionFlag(*session)
	if err != nil {
		return c.watchFatal(events, err)
	}
	if err := c.authenticate(true); err != nil {
		return c.watchFatal(events, err)
	}
	w := &watcher{
		c: c, ctx: ctx, events: events, id: id,
		emitted: map[string]bool{}, acked: map[string]bool{},
		answers: make(chan pollAnswer, 1),
	}
	return w.run()
}

// A watcher is one running watch: the loop's state, owned by one goroutine
// (run). The request in flight runs on its own goroutine and only sends
// its answer back.
type watcher struct {
	c      *command
	ctx    context.Context
	events *protocol.LineWriter
	id     string

	// cursor is the last answer's: "everything accepted for this session up
	// to then".
	cursor string
	// emitted holds the ids emitted since the last drain; a long poll's
	// answer never repeats one of them.
	emitted map[string]bool
	// acked holds the ids this watch acknowledged that a drain may still
	// answer with, because it was asked before the acknowledgement: 4.5.3
	// forbids emitting an acknowledged message again.
	acked map[string]bool

	answers  chan pollAnswer    // the request in flight answers here
	cancel   context.CancelFunc // of the request in flight; nil when there is none
	started  time.Time          // when the request in flight, or the last one, was started
	drainAt  time.Time          // when the next periodic drain is due
	retry    *time.Timer        // fires the next request after a failure
	failures int                // consecutive retryable failures
	outage   time.Time          // the first of them
}

// A pollAnswer is the outcome of one receive of the loop.
type pollAnswer struct {
	drain  bool // the request carried no `after`
	answer *receiveAnswer
	err    error
}

// run does the catch-up, then loops over stdin commands, the answers of
// the requests it starts and the retry timer. It returns the exit status.
func (w *watcher) run() int {
	c := w.c
	// The first receive is the ownership check of 4.5.7: a foreign or
	// unknown session is refused here, before `ready`, with the server's own
	// answer (C-37).
	ctx, cancel := context.WithTimeout(w.ctx, requestTimeout)
	first, err := c.receive(ctx, &receiveBody{SessionID: w.id, Limit: receiveMaxLimit})
	cancel()
	if err != nil {
		if w.ctx.Err() != nil {
			return protocol.ExitOK
		}
		return c.watchFatal(w.events, err)
	}
	if !w.emit(&protocol.WatchReady{
		Event: protocol.EventReady, ProtocolVersion: protocol.ProtocolVersion,
		SessionID: w.id, Mode: protocol.WatchModePush,
	}) {
		return protocol.CodeInternal.Exit()
	}
	if !w.drained(first) {
		return protocol.CodeInternal.Exit()
	}
	commands := c.readCommands(w.ctx)
	w.retry = time.NewTimer(time.Hour)
	w.retry.Stop()
	defer w.retry.Stop()
	w.next()
	for {
		select {
		case <-w.ctx.Done():
			return w.finish(protocol.ExitOK)
		case cmd, ok := <-commands:
			if !ok {
				return w.finish(protocol.ExitOK)
			}
			if code, done := w.handleCommand(cmd); done {
				return w.finish(code)
			}
		case a := <-w.answers:
			if code, done := w.handleAnswer(a); done {
				return w.finish(code)
			}
		case <-w.retry.C:
			w.next()
		}
	}
}

// finish cancels the request in flight, waits for it to end (bounded) and
// returns code.
func (w *watcher) finish(code int) int {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
		select {
		case <-w.answers:
		case <-time.After(w.c.timing.shutdown):
			w.c.log.Debug("the request in flight did not end in time")
		}
	}
	return code
}

// emit writes one event; false means stdout is gone and the watch has
// nothing left to say (the caller exits `internal`).
func (w *watcher) emit(event any) bool {
	if err := w.events.WriteLine(event); err != nil {
		w.c.log.Debug("watch stdout write failed")
		return false
	}
	return true
}

// next starts the loop's next request: the periodic drain when it is due,
// else a long poll behind the cursor that waits no longer than the time
// left before the drain.
func (w *watcher) next() {
	now := time.Now()
	drain := !now.Before(w.drainAt)
	body := &receiveBody{SessionID: w.id, Limit: receiveMaxLimit}
	budget := requestTimeout
	if !drain {
		wait := min(w.c.timing.wait, w.drainAt.Sub(now))
		seconds := int((wait + time.Second - 1) / time.Second)
		body.After, body.WaitSeconds = w.cursor, seconds
		budget = time.Duration(seconds)*time.Second + w.c.timing.slack
	}
	ctx, cancel := context.WithTimeout(w.ctx, budget)
	w.cancel, w.started = cancel, now
	go func() {
		answer, err := w.c.receive(ctx, body)
		w.answers <- pollAnswer{drain: drain, answer: answer, err: err}
	}()
}

// handleAnswer takes the answer of the request in flight, delivers what
// it holds and starts the next one.
func (w *watcher) handleAnswer(a pollAnswer) (int, bool) {
	w.cancel()
	w.cancel = nil
	if a.err != nil {
		return w.failed(a.err)
	}
	if !w.outage.IsZero() {
		w.c.log.Info("backend answering again")
		w.outage, w.failures = time.Time{}, 0
	}
	if a.drain {
		if !w.drained(a.answer) {
			return protocol.CodeInternal.Exit(), true
		}
		w.next()
		return protocol.ExitOK, false
	}
	fresh, written := w.deliver(a.answer)
	if !written {
		return protocol.CodeInternal.Exit(), true
	}
	if rest := w.c.timing.pace - time.Since(w.started); fresh == 0 && rest > 0 {
		w.retry.Reset(rest)
		return protocol.ExitOK, false
	}
	w.next()
	return protocol.ExitOK, false
}

// drained takes the answer of a receive without `after`, the catch-up and
// every periodic drain: the whole unacknowledged set. Each message in it
// is emitted, again if it was emitted before (4.4.9: at most once per
// periodic drain), except one this watch has acknowledged since the drain
// was asked. The emitted and acknowledged sets start over from this
// answer, so neither grows with the life of the watch.
func (w *watcher) drained(answer *receiveAnswer) bool {
	emitted := make(map[string]bool, len(answer.Messages))
	acked := map[string]bool{}
	for i := range answer.Messages {
		id := answer.Messages[i].MessageID
		if w.acked[id] {
			acked[id] = true
			continue
		}
		if emitted[id] {
			continue
		}
		if !w.emit(&protocol.WatchMessage{Event: protocol.EventMessage, Message: answer.Messages[i]}) {
			return false
		}
		emitted[id] = true
	}
	w.emitted, w.acked, w.cursor = emitted, acked, answer.Cursor
	w.drainAt = time.Now().Add(w.c.timing.drain)
	return true
}

// deliver takes the answer of a long poll: the messages accepted since
// the cursor. An id already emitted since the last drain is not emitted
// again, which is also what keeps a server that does not recognise the
// cursor, and so answers the whole set, from flooding the harness. It
// returns how many messages it emitted, and false when stdout is gone.
func (w *watcher) deliver(answer *receiveAnswer) (int, bool) {
	fresh := 0
	for i := range answer.Messages {
		id := answer.Messages[i].MessageID
		if w.acked[id] || w.emitted[id] {
			continue
		}
		if !w.emit(&protocol.WatchMessage{Event: protocol.EventMessage, Message: answer.Messages[i]}) {
			return fresh, false
		}
		w.emitted[id] = true
		fresh++
	}
	w.cursor = answer.Cursor
	return fresh, true
}

// failed answers a receive that failed. A failure whose code is not
// retryable (not_found, unauthorized, unauthenticated, internal) is fatal:
// the uniform `unauthorized` of a member who left ends the watch here
// (C-08). A retryable one (unavailable, rate_limited) is reported once,
// with retryable true, and the request is tried again after a backoff for
// as long as timing.outage allows.
func (w *watcher) failed(err error) (int, bool) {
	if w.ctx.Err() != nil {
		return protocol.ExitOK, true
	}
	perr := asProtocolError(err)
	if !perr.Code.Retryable() {
		return w.c.watchFatal(w.events, err), true
	}
	now := time.Now()
	switch {
	case w.outage.IsZero():
		w.outage = now
		w.c.watchRetryable(w.events, err)
	case now.Sub(w.outage) > w.c.timing.outage:
		gaveUp := errUnavailable("the backend has not answered for too long; the watch gives up", reasonBackendError)
		return w.c.watchFatal(w.events, gaveUp), true
	default:
		w.c.log.Debug("receive failed again", slog.String("code", string(perr.Code)))
	}
	w.retry.Reset(w.backoff(perr))
	return protocol.ExitOK, false
}

// backoff is the wait before the next request and counts the failure. A
// `rate_limited` answer's own retry_after_ms is honoured up to a minute.
func (w *watcher) backoff(perr *protocol.Error) time.Duration {
	w.failures++
	steps := w.c.timing.backoff
	d := steps[min(w.failures, len(steps))-1]
	if after := time.Duration(perr.RetryAfterMS) * time.Millisecond; after > d {
		d = min(after, maxRetryAfter)
	}
	return d
}

// readCommands reads NDJSON commands from stdin (capability
// message.watch.stdin_commands), the other adapters' reader: an over-long
// line, a line that does not decode and a command type this version does
// not know are logged and skipped, never fatal (4.4.9, B-5, B-6). The
// channel closes on EOF, which ends the watch with exit 0 (C-38).
func (c *command) readCommands(ctx context.Context) <-chan *protocol.WatchCommand {
	out := make(chan *protocol.WatchCommand)
	go func() {
		defer close(out)
		reader := protocol.NewLineReader(c.stdin)
		for {
			line, err := reader.Next()
			switch {
			case errors.Is(err, protocol.ErrLineTooLong):
				c.log.Warn("watch stdin line dropped", slog.String("reason", "line_too_long"))
				continue
			case err != nil:
				return
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			cmd := &protocol.WatchCommand{}
			if derr := protocol.Decode(line, cmd); derr != nil {
				c.log.Warn("watch stdin line ignored", slog.String("reason", "malformed"))
				continue
			}
			if !cmd.Known() {
				c.log.Warn("watch stdin command ignored", slog.String("reason", "unknown_type"))
				continue
			}
			select {
			case out <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// handleCommand answers one stdin command (4.4.9, C-41). A refusal that
// concerns the command itself (invalid_input, conflict for a session
// closed elsewhere, a transient failure) is an `error` event with
// retryable true and the watch continues; a refusal that concerns the
// watch (the uniform unauthorized of a revoked member, not_found for a
// session that is gone, a credential the server no longer knows) ends it
// with the code's exit.
func (w *watcher) handleCommand(cmd *protocol.WatchCommand) (int, bool) {
	switch cmd.Type {
	case protocol.CommandAck:
		return w.ack(cmd.MessageIDs)
	case protocol.CommandHeartbeat:
		return w.heartbeat(cmd)
	default:
		return w.close()
	}
}

// ack is `message ack` over stdin.
func (w *watcher) ack(ids []string) (int, bool) {
	out := &ackAnswer{}
	if err := w.c.call(pathMessageAck, &ackBody{SessionID: w.id, MessageIDs: ids}, out); err != nil {
		return w.commandFailed(err)
	}
	for _, id := range out.Acked {
		w.acked[id] = true
	}
	if !w.emit(&protocol.WatchAcked{Event: protocol.EventAcked, MessageIDs: out.Acked, Unknown: out.Unknown}) {
		return protocol.CodeInternal.Exit(), true
	}
	return protocol.ExitOK, false
}

// heartbeat is `session heartbeat` over stdin (no session_description on
// this command, 4.4.9; an absent member is absent from the request too, so
// the stored value stands).
func (w *watcher) heartbeat(cmd *protocol.WatchCommand) (int, bool) {
	req := &protocol.HeartbeatRequest{
		Activity: cmd.Activity, SessionName: cmd.SessionName,
		Inbound: cmd.Inbound, LeaseSeconds: cmd.LeaseSeconds,
		Model: cmd.Model, ContextUsedTokens: cmd.ContextUsedTokens,
		BrigadeVersion: cmd.BrigadeVersion, SyncPeer: cmd.SyncPeer,
	}
	if err := req.Validate(); err != nil {
		w.c.watchRetryable(w.events, err)
		return protocol.ExitOK, false
	}
	if err := protocol.DefaultLease().CheckSeconds("lease_seconds", req.LeaseSeconds); err != nil {
		w.c.watchRetryable(w.events, err)
		return protocol.ExitOK, false
	}
	out := &protocol.HeartbeatResult{}
	if err := w.c.call(pathSessionHeartbeat, &heartbeatBody{SessionID: w.id, Heartbeat: req}, out); err != nil {
		return w.commandFailed(err)
	}
	if !w.emit(&protocol.WatchHeartbeatOK{
		Event: protocol.EventHeartbeatOK, SessionID: out.SessionID, State: out.State,
		LeaseUntil: out.LeaseUntil, ServerTime: out.ServerTime,
	}) {
		return protocol.CodeInternal.Exit(), true
	}
	return protocol.ExitOK, false
}

// close is the `close` command: /v1/session/close as `session close`
// would, bounded so the exit stays inside 5 s, then exit 0. A refused
// close is fatal with its code: the harness must learn the session is
// still open.
func (w *watcher) close() (int, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(w.ctx), w.c.timing.closeTimeout)
	defer cancel()
	req := &request{path: pathSessionClose, in: &sessionBody{SessionID: w.id}, out: &sessionCloseResult{}}
	if err := w.c.do(ctx, req); err != nil {
		return w.c.watchFatal(w.events, err), true
	}
	return protocol.ExitOK, true
}

// commandFailed classifies a command's failure: retryable by code, or
// about the command's own content or the session's state, the watch goes
// on; otherwise the failure is the watch's and ends it.
func (w *watcher) commandFailed(err error) (int, bool) {
	if w.ctx.Err() != nil {
		return protocol.ExitOK, true
	}
	code := asProtocolError(err).Code
	if code.Retryable() || code == protocol.CodeInvalidInput || code == protocol.CodeConflict {
		w.c.watchRetryable(w.events, err)
		return protocol.ExitOK, false
	}
	return w.c.watchFatal(w.events, err), true
}
