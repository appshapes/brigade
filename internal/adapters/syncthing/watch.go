package syncthing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// pollInterval is how often the watch drains the inbox on this replica:
// one directory listing. This adapter does not advertise
// message.watch.push, so its `ready` event says mode "polling" (C-33,
// C-35). A message's own latency is Syncthing's: the sender's folder
// announces the change within a second, and the file is small.
const pollInterval = 500 * time.Millisecond

// watch implements `message watch` (4.4.9). Its stdout is NDJSON, never a
// 4.3 envelope. It is also where the engine lives: the watch attaches it
// at start, holds the reference while it runs, does an engine round
// (reconcile) every engineTick, and detaches at exit — so Syncthing runs
// while a session lives and stops with the last one.
func (c *command) watch() int {
	// SIGTERM handling is installed before anything is written — before
	// `ready` in particular — so a harness that signals the instant it
	// sees `ready` (C-38) can never hit the default disposition.
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
	// The engine: attached here, detached at exit. A machine where it
	// cannot start (syncthing not installed, a start that failed) still
	// watches its replica, so a session is never dead for that reason;
	// the warning is on stderr and the reason in the session log.
	a, err := c.attach(id)
	if err != nil {
		c.log.Warn("the syncthing engine is not available; watching this machine's replica alone", adapterlog.Err(err))
		a = nil
	}
	if a != nil {
		defer func() {
			if _, err := a.e.Detach(id); err != nil {
				c.log.Debug("detach", adapterlog.Err(err))
			}
		}()
	}
	if err := events.WriteLine(&protocol.WatchReady{
		Event:           protocol.EventReady,
		ProtocolVersion: protocol.ProtocolVersion,
		SessionID:       id,
		Mode:            protocol.WatchModePolling,
	}); err != nil {
		return protocol.CodeInternal.Exit()
	}
	return c.watchLoop(ctx, events, id, a)
}

// watchFatal emits the ONE `error` event a pre-catch-up failure produces —
// `retryable` present and false — and returns the failure's 4.6 status.
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
// reader plus the engine rounds. It exits 0 on stdin EOF, on a `close`
// command and on SIGTERM.
func (c *command) watchLoop(ctx context.Context, events *protocol.LineWriter, id string, a *attached) int {
	seen := map[string]bool{}
	if code, done := c.drain(events, id, seen); done {
		return code
	}
	c.engineRound(a)
	commands := c.readCommands(ctx)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	engineTicker := time.NewTicker(engineTick)
	defer engineTicker.Stop()
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
		case <-engineTicker.C:
			c.engineRound(a)
		}
	}
}

// engineRound is one reconcile for the profile's team, when the engine is
// there. A failed round is logged and the next tick tries again: the
// engine is how messages travel, but a round that fails costs nothing on
// this replica.
func (c *command) engineRound(a *attached) {
	if a == nil {
		return
	}
	if err := c.reconcile(a, c.profile.TeamRef, c.profile.Devices); err != nil {
		c.log.Warn("the engine round failed; the next tick tries again", adapterlog.Err(err))
	}
}

// drain emits every not-yet-emitted message of the session's inbox,
// oldest first. The store lock is taken for the read and released before
// the events are written. The 4.5.7 membership check is re-applied here,
// under the same lock, and a pending join completed, before anything is
// read: a membership revoked from another machine ends the watch within
// one poll of the marker's arrival.
func (c *command) drain(events *protocol.LineWriter, id string, seen map[string]bool) (int, bool) {
	st, err := c.lockStore()
	if err != nil {
		return c.watchFatal(events, err), true
	}
	c.st = st
	if jerr := c.completeJoin(); jerr != nil {
		st.close()
		c.st = nil
		return c.watchFatal(events, jerr), true
	}
	if merr := st.requireMembership(c.profile.TeamRef, c.cred.PrincipalRef); merr != nil {
		st.close()
		c.st = nil
		return c.watchFatal(events, merr), true
	}
	pending, err := st.inboxMessages(c.profile.TeamRef, id)
	st.close()
	c.st = nil
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
// message.watch.stdin_commands). The channel closes on EOF, which is what
// ends the watch with exit 0.
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

// handleCommand answers one stdin command (4.4.9). A failure that is not
// fatal is reported as an `error` event with `retryable: true` and the
// watch continues; only `close` ends it.
func (c *command) handleCommand(
	events *protocol.LineWriter, id string, cmd *protocol.WatchCommand, seen map[string]bool,
) (int, bool) {
	st, err := c.lockStore()
	if err != nil {
		return c.watchFatal(events, err), true
	}
	defer st.close()
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
			BrigadeVersion: cmd.BrigadeVersion, SyncPeer: cmd.SyncPeer,
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

// emit writes one event; a write failure ends the watch.
func (c *command) emit(events *protocol.LineWriter, event any) (int, bool) {
	if err := events.WriteLine(event); err != nil {
		return protocol.CodeInternal.Exit(), true
	}
	return protocol.ExitOK, false
}

// watchRetryable reports a non-fatal command failure: `retryable: true`
// means informational, and the loop goes on.
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
