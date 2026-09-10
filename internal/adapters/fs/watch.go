package fs

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

// pollInterval is how often the watch drains the inbox. This adapter does
// not advertise message.watch.push, so its `ready` event says mode
// "polling" and a consumer expects a message within two intervals (4.7,
// C-33, C-35).
const pollInterval = 200 * time.Millisecond

// watch implements `message watch` (4.4.9, C-33..C-41). Its stdout is
// NDJSON, never a 4.3 envelope, and every event goes through ONE
// protocol.LineWriter: the codec escapes newlines, U+2028 and U+2029, so a
// hostile body is one physical line that parses back unchanged (C-39).
func (c *command) watch() int {
	// SIGTERM handling is installed before anything is written — before
	// `ready` in particular — so a harness that signals the instant it
	// sees `ready` (C-38) can never hit the default disposition. Measured
	// on a slow CI runner with the handler installed only inside watchLoop,
	// AFTER `ready` was written: a SIGTERM sent on `ready` killed the
	// process by signal, the one exit 4.4.9 forbids.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
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
	if err := c.teamScoped(); err != nil {
		return c.watchFatal(events, err)
	}
	if _, err := c.st.ownedSession(c.profile.TeamRef, c.cred.PrincipalRef, id); err != nil {
		return c.watchFatal(events, err)
	}
	c.finish()
	c.st = nil
	if err := events.WriteLine(&protocol.WatchReady{
		Event:           protocol.EventReady,
		ProtocolVersion: protocol.ProtocolVersion,
		SessionID:       id,
		Mode:            protocol.WatchModePolling,
	}); err != nil {
		return protocol.CodeInternal.Exit()
	}
	return c.watchLoop(ctx, events, id)
}

// watchFatal emits the ONE `error` event a pre-catch-up failure produces —
// `retryable` present and false — and returns the failure's 4.6 status.
// No `ready` is written, because the catch-up never started (C-37).
func (c *command) watchFatal(events *protocol.LineWriter, err error) int {
	perr := &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"}
	var typed *protocol.Error
	if errors.As(err, &typed) && typed != nil {
		perr = typed
	}
	object := perr.Object()
	object.Retryable = false
	_ = events.WriteLine(&protocol.WatchError{Event: protocol.EventError, Error: *object})
	c.log.Debug("watch refused", slog.String("code", string(perr.Code)))
	return perr.Code.Exit()
}

// watchLoop is the catch-up plus the poll loop plus the stdin command
// reader. It exits 0 on stdin EOF, on a `close` command and on SIGTERM
// (C-38, C-41).
func (c *command) watchLoop(ctx context.Context, events *protocol.LineWriter, id string) int {
	seen := map[string]bool{}
	if code, done := c.drain(events, id, seen); done {
		return code
	}
	commands := c.readCommands(ctx)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return protocol.ExitOK
		case cmd, ok := <-commands:
			if !ok {
				return protocol.ExitOK
			}
			if code, done := c.handleCommand(events, id, cmd, seen); done {
				return code
			}
		case <-ticker.C:
			if code, done := c.drain(events, id, seen); done {
				return code
			}
		}
	}
}

// drain emits every not-yet-emitted message of the session's inbox, oldest
// first. The store lock is taken for the read and released before the
// events are written, so the watch never holds it across a write or across
// the sleep between polls. An id is emitted at most once per process
// (4.5.2 permits a repeat; C-36 wants one per drain, and a restart is what
// re-emits an unacknowledged message).
//
// The 4.5.7 membership check is re-applied here, under the same lock,
// before anything is read: `unauthorized` is the answer for a principal
// whose membership was revoked on EVERY verb that touches the team, its
// sessions or their messages, and a watch touches all three for as long as
// it runs. A membership revoked by another process — `team leave`, an
// administrator, a deleted team — therefore ends the watch within one poll
// interval with the same fixed message every other command uses. A session
// merely CLOSED by another process does not: the spec makes a close the
// harness's own `close` command, not an end of stream.
func (c *command) drain(events *protocol.LineWriter, id string, seen map[string]bool) (int, bool) {
	st, err := c.lockStore()
	if err != nil {
		return c.watchFatal(events, err), true
	}
	if merr := st.requireMembership(c.profile.TeamRef, c.cred.PrincipalRef); merr != nil {
		st.close()
		return c.watchFatal(events, merr), true
	}
	pending, err := st.inboxMessages(c.profile.TeamRef, id)
	st.close()
	if err != nil {
		return c.watchFatal(events, err), true
	}
	for i := range pending {
		message := pending[i].MessageEnvelope
		if seen[message.MessageID] {
			continue
		}
		if werr := events.WriteLine(&protocol.WatchMessage{
			Event: protocol.EventMessage, Message: message,
		}); werr != nil {
			return protocol.CodeInternal.Exit(), true
		}
		seen[message.MessageID] = true
	}
	return protocol.ExitOK, false
}

// readCommands reads NDJSON commands from stdin (capability
// message.watch.stdin_commands). An over-long line, a line that does not
// decode and a command type this version does not know are all logged and
// skipped, never fatal (4.4.9, B-5, B-6). The channel closes on EOF, which
// is what ends the watch with exit 0 (C-38).
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

// handleCommand answers one stdin command (4.4.9, C-41). A failure that is
// not fatal is reported as an `error` event with `retryable: true` and the
// watch continues; only `close` ends it.
func (c *command) handleCommand(
	events *protocol.LineWriter, id string, cmd *protocol.WatchCommand, seen map[string]bool,
) (int, bool) {
	st, err := c.lockStore()
	if err != nil {
		return c.watchFatal(events, err), true
	}
	defer st.close()
	// The same 4.5.7 re-check as drain: an ack, heartbeat or close is a verb
	// that touches the team, and a revoked principal must not perform one
	// in the sub-interval between the revocation and the next poll. The
	// observable outcome is the one TestWatchStopsWhenMembershipIsRevoked
	// pins (one unauthorized event, exit 5); this closes the window in
	// which a command could slip through before the poll noticed.
	if merr := st.requireMembership(c.profile.TeamRef, c.cred.PrincipalRef); merr != nil {
		return c.watchFatal(events, merr), true
	}
	team, principal := c.profile.TeamRef, c.cred.PrincipalRef
	switch cmd.Type {
	case protocol.CommandAck:
		result, aerr := ackMessages(st, team, id, cmd.MessageIDs)
		if aerr != nil {
			return c.watchRetryable(events, aerr), false
		}
		for _, acked := range result.Acked {
			seen[acked] = true
		}
		return c.emit(events, &protocol.WatchAcked{
			Event: protocol.EventAcked, MessageIDs: result.Acked, Unknown: result.Unknown,
		})
	case protocol.CommandHeartbeat:
		req := &protocol.HeartbeatRequest{
			Activity: cmd.Activity, SessionName: cmd.SessionName,
			Inbound: cmd.Inbound, LeaseSeconds: cmd.LeaseSeconds,
			Model: cmd.Model, ContextUsedTokens: cmd.ContextUsedTokens,
		}
		if verr := req.Validate(); verr != nil {
			return c.watchRetryable(events, verr), false
		}
		if verr := adapterLease().CheckSeconds("lease_seconds", req.LeaseSeconds); verr != nil {
			return c.watchRetryable(events, verr), false
		}
		result, herr := st.heartbeat(team, principal, id, req)
		if herr != nil {
			return c.watchRetryable(events, herr), false
		}
		return c.emit(events, &protocol.WatchHeartbeatOK{
			Event: protocol.EventHeartbeatOK, SessionID: result.SessionID, State: result.State,
			LeaseUntil: result.LeaseUntil, ServerTime: result.ServerTime,
		})
	default:
		if cerr := st.closeSession(team, principal, id); cerr != nil {
			return c.watchFatal(events, cerr), true
		}
		return protocol.ExitOK, true
	}
}

// emit writes one event; a write failure ends the watch, because a watch
// whose stdout is gone has nothing left to say.
func (c *command) emit(events *protocol.LineWriter, event any) (int, bool) {
	if err := events.WriteLine(event); err != nil {
		return protocol.CodeInternal.Exit(), true
	}
	return protocol.ExitOK, false
}

// watchRetryable reports a non-fatal command failure. 4.4.9 keys the
// watch's survival on the flag, not on the code: `retryable: true` means
// informational, and the loop goes on.
func (c *command) watchRetryable(events *protocol.LineWriter, err error) int {
	perr := &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"}
	var typed *protocol.Error
	if errors.As(err, &typed) && typed != nil {
		perr = typed
	}
	object := perr.Object()
	object.Retryable = true
	_ = events.WriteLine(&protocol.WatchError{Event: protocol.EventError, Error: *object})
	return protocol.ExitOK
}
