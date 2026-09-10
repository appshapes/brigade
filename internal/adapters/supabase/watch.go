package supabase

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

// `message watch` (4.4.9, plan 5.6, C-33..C-41, C-08): the realtime watch
// of plan task P2-10. Its stdout is NDJSON through ONE protocol.LineWriter
// (C-39), `ready` comes first with mode push (this adapter advertises
// message.watch.push), and every failure before the catch-up is ONE
// `error` event with retryable false and the code's exit status (C-37).
//
// The delivery path is the DRAIN — fetch_inbox over the session's
// accepted set, no cursor (5.7) — and the Phoenix channel is only a hint
// that makes a drain happen sooner: on join ok (a message committing
// before the join completes is never broadcast, E0-2 (f)), on EVERY
// broadcast on the session's own topic (message_accepted from a send,
// membership_revoked from leave_team, whatever a later migration adds),
// and on a periodic timer — 30 s while joined, 10 s while polling, with
// two 3 s "settling" drains after `ready` and after every join — so a
// lost hint costs latency, never a message. The RPC is the authority on
// ownership and membership every time it runs (owned_active_session),
// which is what ends the watch of a revoked member with the uniform
// `unauthorized` (C-08) and answers a foreign session with the uniform
// `not_found` (C-37) — the channel's own refusals are advisory: they
// trigger a drain and a fall-back to polling, never an exit of their own.
//
// The channel is dialled and joined CONCURRENTLY with the first fetch and
// the catch-up (dialEarly), not after them, so a hint broadcast the
// instant a harness sees `ready` — C-08's `team leave` — reaches a socket
// whose join has completed; `ready` itself stays where 4.4.9 puts it, at
// the catch-up's start, and the link says nothing before it.
//
// The stdin commands ack, heartbeat and close are the fs adapter's (each
// is its RPC); a rejected command is an `error` event with retryable true
// and the watch goes on; stdin EOF, `close` and SIGTERM end it with exit 0
// within 5 s (phx_leave and close_session best effort).

// drainPageSize is fetch_inbox's cap: one page holds the whole accepted
// set of any conforming inbox (max_unacked_per_recipient is 60). Without
// a cursor a fuller inbox surfaces as the harness acks (5.7).
const drainPageSize = 200

// watchTiming holds the watch's intervals. It is a variable so a test can
// shorten them; the tests that do are not parallel and restore it.
var watchTiming = struct {
	// drainLive is the periodic drain while the private channel is up. The
	// channel is the fast path — every broadcast on the session's topic,
	// message_accepted from a send and membership_revoked from leave_team,
	// is a hint that drains at once — so the timer only bounds what a lost
	// hint costs, at one RPC per 30 s per watcher (plan 5.6). The suite's
	// deadlines, a message within 5 s (C-35) and a revoked member's watch
	// ended within the same 5 s (C-08), are met by hints and never by this
	// timer; the tests' negative control pins that it does not fire early.
	drainLive time.Duration
	// drainPolling is the periodic drain while the channel is down and the
	// watch has reported `status polling`, and before the first join (plan
	// 5.6: 10 s — the latency a polling status announces).
	drainPolling time.Duration
	// settle is the interval of the settleDrains extra drains armed after
	// `ready` and after every join. Measured three times on 2026-09-02/03
	// (CI runs 33696302372 and 33756168929, a local run): a broadcast
	// issued in the first seconds after a socket joined its topic — the
	// membership_revoked of C-08's `team leave`, a message_accepted right
	// after a Realtime restart — reached no socket, and the next drain was
	// the 30 s live timer, past C-08's 5 s budget. The fan-out to a fresh
	// join is not warm the instant phx_reply says ok, and a slow join
	// leaves the pre-join window on the 10 s polling timer. Two drains 3 s
	// apart bound what either costs to ~3 s without touching the steady
	// cadence: at most four extra RPCs per start or rejoin.
	settle time.Duration
	// heartbeat is the phx heartbeat cadence (the 66 s server rule, E0-2).
	heartbeat time.Duration
	// joinTimeout bounds the wait for phx_join's reply; a refusal arrives
	// only after the server's fixed 5 s backoff.
	joinTimeout time.Duration
	// joinRetry is the wait before another join after a refusal that will
	// not fix itself soon (a closed session, a revoked topic).
	joinRetry time.Duration
	// reconnect is the backoff after a transport loss, realtime-js's
	// intervals capped at 30 s; the drain keeps delivering meanwhile.
	reconnect []time.Duration
	// outage is how long consecutive drain failures are tolerated before
	// the watch gives up with `unavailable`.
	outage time.Duration
	// shutdown bounds the wait for the channel's leave at exit.
	shutdown time.Duration
	// closeTimeout bounds close_session for a `close` command.
	closeTimeout time.Duration
}{
	drainLive:    30 * time.Second,
	drainPolling: 10 * time.Second,
	settle:       3 * time.Second,
	heartbeat:    25 * time.Second,
	joinTimeout:  15 * time.Second,
	joinRetry:    60 * time.Second,
	reconnect:    []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second},
	outage:       30 * time.Minute,
	shutdown:     2 * time.Second,
	closeTimeout: 4 * time.Second,
}

// The status details this adapter emits (free text by 4.4.9; fixed here).
// settleDrains is how many watchTiming.settle drains follow `ready` and
// every join before the timer returns to drainLive/drainPolling.
const settleDrains = 2

const (
	statusDetailJoined = "joined"
)

// watch implements `message watch`. The SIGTERM handler is installed
// before anything is written — before `ready` in particular — so a
// harness that signals the instant it sees `ready` (C-38) never hits the
// default disposition (the fs adapter's measured CI failure).
func (c *command) watch() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
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
	if !validUUID(id) {
		return c.watchFatal(events, errNotFound())
	}
	w := &watcher{
		c: c, ctx: ctx, events: events, id: id, topic: sessionTopic(id),
		seen: map[string]bool{}, linkEvents: make(chan linkEvent, 64),
	}
	return w.run()
}

// A watcher is one running watch: the loop's state, owned by one
// goroutine (run); the link goroutine and the stdin reader only send to
// it over channels.
type watcher struct {
	c      *command
	ctx    context.Context
	events *protocol.LineWriter
	id     string
	topic  string
	seen   map[string]bool // emitted at most once per process (C-36, the fs rule)

	commands   <-chan *protocol.WatchCommand
	linkEvents chan linkEvent
	link       *link
	gen        int
	attempts   int         // consecutive failed link attempts, for the backoff
	reconnect  *time.Timer // fires connect
	drainTimer *time.Timer // fires the periodic drain; re-armed after every drain
	settling   int         // settle-interval drains still owed after `ready` or a join (settleDrains)
	live       bool        // a `status live` is in force
	polling    bool        // a `status polling` was reported since the last live
	refreshed  bool        // the one forced refresh a bad-token refusal earns
	pushed     string      // the access token the channel was last given
	outage     time.Time   // the first of the current run of drain failures
}

// A link is the watcher's handle on one runLink goroutine.
type link struct {
	cancel context.CancelFunc
	tokens chan string
	done   chan struct{}
}

// run starts the channel's dial, runs the catch-up, then loops over stdin
// commands, the drain timer, the link's reports and the reconnect timer.
// It returns the exit status. The drain timer is one-shot and re-armed
// after EVERY drain, whatever ran it, so a hint or a join pushes the next
// timer-driven drain a whole interval away, and the interval follows the
// watch's state.
func (w *watcher) run() int {
	c := w.c
	w.reconnect = time.NewTimer(time.Hour)
	w.reconnect.Stop()
	w.dialEarly()
	// The first fetch is the ownership check of 4.5.7: a foreign or unknown
	// session is refused here, before `ready`, byte-identical to an id that
	// is not uuid-shaped (C-37). The link started above has said nothing —
	// it reports to the loop below, which has not started — and every exit
	// from here on goes through finish, which cancels it and waits for it.
	messages, err := w.fetch()
	if err != nil {
		if w.ctx.Err() != nil {
			return w.finish(protocol.ExitOK)
		}
		return w.finish(c.watchFatal(w.events, err))
	}
	if !w.emit(&protocol.WatchReady{
		Event: protocol.EventReady, ProtocolVersion: protocol.ProtocolVersion,
		SessionID: w.id, Mode: protocol.WatchModePush,
	}) {
		return w.finish(protocol.CodeInternal.Exit())
	}
	if !w.deliver(messages) {
		return w.finish(protocol.CodeInternal.Exit())
	}
	w.commands = c.readCommands(w.ctx)
	w.drainTimer = time.NewTimer(time.Hour)
	defer w.drainTimer.Stop()
	w.settling = settleDrains
	w.rearm()
	if w.link == nil {
		if code, done := w.connect(); done {
			return w.finish(code)
		}
	}
	for {
		select {
		case <-w.ctx.Done():
			return w.finish(protocol.ExitOK)
		case cmd, ok := <-w.commands:
			if !ok {
				return w.finish(protocol.ExitOK)
			}
			if code, done := w.handleCommand(cmd); done {
				return w.finish(code)
			}
		case <-w.drainTimer.C:
			if code, done := w.drain(); done {
				return w.finish(code)
			}
		case ev := <-w.linkEvents:
			if code, done := w.handleLink(ev); done {
				return w.finish(code)
			}
		case <-w.reconnect.C:
			if code, done := w.connect(); done {
				return w.finish(code)
			}
		}
	}
}

// finish leaves the channel (bounded) and returns code.
func (w *watcher) finish(code int) int {
	if w.link != nil {
		w.link.cancel()
		select {
		case <-w.link.done:
		case <-time.After(watchTiming.shutdown):
			w.c.log.Debug("realtime channel did not leave in time")
		}
		w.link = nil
	}
	return code
}

// emit writes one event; false means stdout is gone and the watch has
// nothing left to say (the caller exits `internal`).
func (w *watcher) emit(event any) bool {
	_, failed := w.c.emit(w.events, event)
	return !failed
}

// fetch is one fetch_inbox page: the session's accepted set, oldest first.
func (w *watcher) fetch() ([]protocol.MessageEnvelope, error) {
	var messages []protocol.MessageEnvelope
	err := w.c.rpc(w.ctx, "fetch_inbox", rpcArgs{"p_session_id": w.id, "p_limit": drainPageSize}, &messages)
	if err != nil {
		return nil, err
	}
	if len(messages) >= drainPageSize {
		w.c.log.Debug("inbox page is full; the rest surfaces as messages are acknowledged")
	}
	return messages, nil
}

// deliver emits every message not yet emitted by this process.
func (w *watcher) deliver(messages []protocol.MessageEnvelope) bool {
	for i := range messages {
		id := messages[i].MessageID
		if w.seen[id] {
			continue
		}
		if !w.emit(&protocol.WatchMessage{Event: protocol.EventMessage, Message: messages[i]}) {
			return false
		}
		w.seen[id] = true
	}
	return true
}

// drain is one fetch and delivery. A failure whose code is not retryable
// (not_found, unauthorized, unauthenticated, config, internal) is fatal;
// a retryable one (unavailable, rate_limited) is reported once, with
// retryable true, and tolerated for watchTiming.outage before the watch
// gives up. The channel keeps the loop informed meanwhile only through
// hints, which end in the same call.
func (w *watcher) drain() (int, bool) {
	defer w.rearm()
	messages, err := w.fetch()
	if err != nil {
		return w.drainFailed(err)
	}
	if !w.outage.IsZero() {
		w.c.log.Info("backend answering again")
		w.outage = time.Time{}
	}
	if !w.deliver(messages) {
		return protocol.CodeInternal.Exit(), true
	}
	w.syncToken()
	return protocol.ExitOK, false
}

func (w *watcher) drainFailed(err error) (int, bool) {
	if w.ctx.Err() != nil {
		return protocol.ExitOK, true
	}
	perr := asProtocolError(err)
	if !perr.Code.Retryable() {
		return w.c.watchFatal(w.events, err), true
	}
	now := time.Now()
	if w.outage.IsZero() {
		w.outage = now
		return w.c.watchRetryable(w.events, err), false
	}
	if now.Sub(w.outage) > watchTiming.outage {
		return w.c.watchFatal(w.events, errUnavailable("the backend has not answered for too long; the watch gives up", reasonBackendError)), true
	}
	w.c.log.Debug("drain failed again", slog.String("code", string(perr.Code)))
	return protocol.ExitOK, false
}

// drainInterval is the periodic drain's interval in the watch's state.
func (w *watcher) drainInterval() time.Duration {
	if w.live {
		return watchTiming.drainLive
	}
	return watchTiming.drainPolling
}

// rearm schedules the next timer-driven drain a whole interval from now —
// watchTiming.settle while settling drains are owed, the state's interval
// otherwise. Go's timers (go ≥ 1.23 semantics) discard an unreceived tick
// on Reset, so a drain that ran on a hint never doubles with a stale tick.
func (w *watcher) rearm() {
	if w.drainTimer == nil {
		return
	}
	d := w.drainInterval()
	if w.settling > 0 {
		w.settling--
		d = watchTiming.settle
	}
	w.drainTimer.Reset(d)
}

// syncToken pushes the access token to the channel when a refresh (which
// happens inside c.rpc, under the flock) changed it. The push re-runs the
// topic policy at the server (E0-2 (h)(2)); the channel needs the new JWT
// before the old one expires or the server closes it.
func (w *watcher) syncToken() {
	if w.link == nil || w.c.cred == nil || w.c.cred.AccessToken == w.pushed {
		return
	}
	w.pushed = w.c.cred.AccessToken
	select {
	case <-w.link.tokens:
	default:
	}
	select {
	case w.link.tokens <- w.pushed:
	default:
	}
}

// dialEarly starts the channel's dial and join before the first fetch —
// as soon as there is a token to join with — so the join has the whole
// ownership check and catch-up as a head start and is usually complete by
// the time `ready` is written. A harness that acts the instant it sees
// `ready` then acts on a joined socket: C-08's `team leave`, whose
// membership_revoked broadcast the server delivers only to sockets whose
// join has completed, found one still joining on a CI runner (run
// 33678110011: 2.24 s to the error event) and was seen only by the drain
// after the join. `ready` stays where 4.4.9 puts it, when the catch-up
// starts, and the drain on join ok stays: it is what finds a revocation
// or a message that landed between the first fetch and the join. Nothing
// the link learns before the loop starts is lost or spoken: it reports
// over linkEvents, which buffers and otherwise holds the link (never a
// frame), and the loop that reads it starts after the catch-up. A token
// that cannot be had now is not reported from here: the first fetch
// refreshes through the same state machine and answers before `ready` if
// the credential is dead, and connect reports a transient failure after
// the catch-up.
func (w *watcher) dialEarly() {
	token, err := w.c.accessToken(w.ctx)
	if err != nil {
		w.c.log.Debug("realtime dial waits for the catch-up", slog.String("code", string(asProtocolError(err).Code)))
		return
	}
	w.startLink(token)
}

// connect starts a link with a fresh access token. A credential that
// cannot be refreshed is fatal; a transient failure falls back to polling
// and retries.
func (w *watcher) connect() (int, bool) {
	token, err := w.c.accessToken(w.ctx)
	if err != nil {
		if w.ctx.Err() != nil {
			return protocol.ExitOK, true
		}
		if !asProtocolError(err).Code.Retryable() {
			return w.c.watchFatal(w.events, err), true
		}
		if !w.fallBack(linkReasonCredential) {
			return protocol.CodeInternal.Exit(), true
		}
		w.scheduleReconnect(w.backoff())
		return protocol.ExitOK, false
	}
	w.startLink(token)
	return protocol.ExitOK, false
}

// startLink starts the goroutine that owns one socket and joins with
// token, as the watch's current link.
func (w *watcher) startLink(token string) {
	w.gen++
	ctx, cancel := context.WithCancel(w.ctx)
	l := &link{cancel: cancel, tokens: make(chan string, 1), done: make(chan struct{})}
	w.link, w.pushed = l, token
	gen := w.gen
	go func() {
		defer close(l.done)
		w.c.runLink(ctx, gen, w.topic, token, l.tokens, w.linkEvents)
	}()
}

// dropLink forgets the current link (its goroutine has reported and is
// ending).
func (w *watcher) dropLink() {
	if w.link != nil {
		w.link.cancel()
		w.link = nil
	}
}

// backoff is the next reconnect delay and counts the attempt.
func (w *watcher) backoff() time.Duration {
	w.attempts++
	steps := watchTiming.reconnect
	return steps[min(w.attempts, len(steps))-1]
}

func (w *watcher) scheduleReconnect(d time.Duration) {
	w.reconnect.Reset(d)
}

// fallBack reports `status polling` once per loss of the channel; the
// drain timer, re-armed here at the polling interval, is what delivers
// meanwhile.
func (w *watcher) fallBack(reason string) bool {
	if !w.live && w.polling {
		w.c.log.Debug("realtime still down", slog.String("reason", reason))
		return true
	}
	w.live, w.polling = false, true
	w.rearm()
	return w.emit(&protocol.WatchStatus{Event: protocol.EventStatus, State: protocol.StatusStatePolling, Detail: reason})
}

// handleLink answers one report from the link goroutine.
func (w *watcher) handleLink(ev linkEvent) (int, bool) {
	if ev.gen != w.gen {
		return protocol.ExitOK, false
	}
	switch ev.kind {
	case linkJoined:
		w.attempts, w.refreshed = 0, false
		if !w.live {
			w.live, w.polling = true, false
			if !w.emit(&protocol.WatchStatus{Event: protocol.EventStatus, State: protocol.StatusStateLive, Detail: statusDetailJoined}) {
				return protocol.CodeInternal.Exit(), true
			}
		}
		// Drain on join ok is mandatory, not an optimisation (E0-2 (f)),
		// and the settling drains follow it: the fan-out to a fresh join
		// is not warm at once (watchTiming.settle).
		w.settling = settleDrains
		return w.drain()
	case linkHint:
		return w.onHint()
	case linkDown:
		w.dropLink()
		if !w.fallBack(ev.reason) {
			return protocol.CodeInternal.Exit(), true
		}
		w.scheduleReconnect(w.backoff())
		return protocol.ExitOK, false
	case linkRefused:
		return w.onRefused(ev.reason)
	}
	return protocol.ExitOK, false
}

// onHint drains, then coalesces the hints that queued up meanwhile into
// at most one follow-up drain (a burst of senders is one fetch, not
// fifty). A report of another kind found in the queue is handled in turn.
func (w *watcher) onHint() (int, bool) {
	if code, done := w.drain(); done {
		return code, true
	}
	extra := false
	for {
		select {
		case ev := <-w.linkEvents:
			if ev.kind == linkHint {
				extra = extra || ev.gen == w.gen
				continue
			}
			if extra {
				if code, done := w.drain(); done {
					return code, true
				}
			}
			return w.handleLink(ev)
		default:
			if extra {
				return w.drain()
			}
			return protocol.ExitOK, false
		}
	}
}

// onRefused answers a refused join. A JWT the server would not honour
// earns one forced refresh and a prompt rejoin. Anything else is decided
// by the RPC, not the channel: a drain right now ends the watch with the
// uniform `unauthorized` for a revoked member (C-08) or the uniform
// `not_found` for a session that is gone, and otherwise — a session
// closed by another process (owns_session_topic requires it open, but a
// closed owned session stays drainable, C-31), a realtime-only refusal —
// the watch keeps polling and retries the join later.
func (w *watcher) onRefused(reason string) (int, bool) {
	w.dropLink()
	if reason == linkReasonBadToken && !w.refreshed {
		w.refreshed = true
		if _, err := w.c.forceRefresh(w.ctx); err != nil {
			if w.ctx.Err() != nil {
				return protocol.ExitOK, true
			}
			if !asProtocolError(err).Code.Retryable() {
				return w.c.watchFatal(w.events, err), true
			}
		}
		w.scheduleReconnect(w.backoff())
		return protocol.ExitOK, false
	}
	if code, done := w.drain(); done {
		return code, true
	}
	if !w.fallBack(reason) {
		return protocol.CodeInternal.Exit(), true
	}
	w.attempts++
	if reason == linkReasonRateLimited || reason == linkReasonDisabled {
		w.scheduleReconnect(w.backoff())
	} else {
		w.scheduleReconnect(watchTiming.joinRetry)
	}
	return protocol.ExitOK, false
}

// readCommands reads NDJSON commands from stdin (capability
// message.watch.stdin_commands), the fs adapter's reader: an over-long
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
// concerns the command itself — invalid_input, conflict (a session closed
// elsewhere), a transient backend failure — is an `error` event with
// retryable true and the watch continues; a refusal that concerns the
// watch — the uniform unauthorized of a revoked member, not_found for a
// session that is gone, a dead credential — ends it with the code's exit.
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

// ack is `message ack` over stdin: an id that is not uuid-shaped is
// unknown locally and the RPC still runs with the rest (ownership of the
// session is checked there).
func (w *watcher) ack(ids []string) (int, bool) {
	known := make([]string, 0, len(ids))
	unknown := make([]string, 0, len(ids))
	for _, m := range ids {
		if validUUID(m) {
			known = append(known, m)
		} else {
			unknown = append(unknown, m)
		}
	}
	out := &protocol.AckResult{}
	if err := w.c.rpc(w.ctx, "ack_messages", rpcArgs{"p_session_id": w.id, "p_message_ids": known}, out); err != nil {
		return w.commandFailed(err)
	}
	if out.Acked == nil {
		out.Acked = []string{}
	}
	for _, id := range out.Acked {
		w.seen[id] = true
	}
	w.syncToken()
	if !w.emit(&protocol.WatchAcked{Event: protocol.EventAcked, MessageIDs: out.Acked, Unknown: append(unknown, out.Unknown...)}) {
		return protocol.CodeInternal.Exit(), true
	}
	return protocol.ExitOK, false
}

// heartbeat is `session heartbeat` over stdin (no session_description on
// this command, 4.4.9; model and context_used_tokens ride it like the RPC
// path's, C-44, null when absent so the stored values stand).
func (w *watcher) heartbeat(cmd *protocol.WatchCommand) (int, bool) {
	req := &protocol.HeartbeatRequest{
		Activity: cmd.Activity, SessionName: cmd.SessionName,
		Inbound: cmd.Inbound, LeaseSeconds: cmd.LeaseSeconds,
		Model: cmd.Model, ContextUsedTokens: cmd.ContextUsedTokens,
	}
	if err := req.Validate(); err != nil {
		return w.c.watchRetryable(w.events, err), false
	}
	if err := protocol.DefaultLease().CheckSeconds("lease_seconds", req.LeaseSeconds); err != nil {
		return w.c.watchRetryable(w.events, err), false
	}
	out := &protocol.HeartbeatResult{}
	err := w.c.rpcAppended(w.ctx, "session_heartbeat", rpcArgs{
		"p_session_id":          w.id,
		"p_activity":            req.Activity,
		"p_name":                req.SessionName,
		"p_description":         nil,
		"p_inbound":             req.Inbound,
		"p_lease_seconds":       req.LeaseSeconds,
		"p_model":               req.Model,
		"p_context_used_tokens": req.ContextUsedTokens,
	}, sessionAppendedParams, out)
	if err != nil {
		return w.commandFailed(err)
	}
	w.syncToken()
	if !w.emit(&protocol.WatchHeartbeatOK{
		Event: protocol.EventHeartbeatOK, SessionID: out.SessionID, State: out.State,
		LeaseUntil: out.LeaseUntil, ServerTime: out.ServerTime,
	}) {
		return protocol.CodeInternal.Exit(), true
	}
	return protocol.ExitOK, false
}

// close is the `close` command: close_session as `session close` would,
// bounded so the exit stays inside 5 s, then exit 0. A refused close is
// fatal with its code (the fs rule): the harness must learn the session
// is still open.
func (w *watcher) close() (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), watchTiming.closeTimeout)
	defer cancel()
	if err := w.c.rpc(ctx, "close_session", rpcArgs{"p_session_id": w.id}, nil); err != nil {
		return w.c.watchFatal(w.events, err), true
	}
	return protocol.ExitOK, true
}

// commandFailed classifies a command's RPC failure: retryable by code, or
// about the command's own content or the session's state, the watch goes
// on; otherwise the failure is the watch's and ends it.
func (w *watcher) commandFailed(err error) (int, bool) {
	if w.ctx.Err() != nil {
		return protocol.ExitOK, true
	}
	code := asProtocolError(err).Code
	if code.Retryable() || code == protocol.CodeInvalidInput || code == protocol.CodeConflict {
		return w.c.watchRetryable(w.events, err), false
	}
	return w.c.watchFatal(w.events, err), true
}
