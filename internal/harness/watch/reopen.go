package watch

import (
	"context"
	"errors"
	"log/slog"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/protocol"
)

// A watcher whose Brigade session is closed under it re-opens it (P10-7).
//
// The case that made this necessary: a watcher replaced by the hook — at
// a prompt after a plugin update (0.5.1), or on /clear when the socket or
// token changed — is SIGTERMed, and its exit path closes the session
// (6.6: the close is what a stopped watcher owes the team). The
// replacement then heartbeats a closed session and is answered
// `conflict`, details.reason session_closed (4.5.8, C-15), on every beat
// until the Claude session ends; to the team it is offline. The
// SessionStart hook papered over this by heartbeating after the respawn
// and re-registering on conflict; the prompt hook has no budget for that
// (4.5 s), and measured on 2026-09-11 it left the session offline.
//
// So the watcher does it itself, wherever the closure came from: one
// `session register` with resume.session_id = its own id (4.4.2, C-19: an
// owned, closed session re-opens with its id and its pending messages),
// the current name, activity, inbound policy and lease, and the transcript
// facts. A resumed answer with the same id means the session is back and
// the next heartbeat is requested at once. Anything else stops the
// watcher with reason session_gone: a fresh id (the adapter declined the
// resume — closed first, best effort, so no stray session lingers), or
// not_found / conflict:session_live (gone for good, or held by another
// process); the next prompt hook respawns a watcher that tries again, and
// a new SessionStart registers afresh. Other failures are logged and the
// next heartbeat retries.

// harnessName is the `harness` member of a registration (6.3) — the
// hook's own constant, kept in step by hand.
const harnessName = "claude-code"

// reasonSessionClosed is the details.reason both bundled adapters give a
// heartbeat or an ack on a closed session (4.5.8, C-15).
const reasonSessionClosed = "session_closed"

// sessionGone reports whether a refused command says the Brigade session
// is closed or gone: `not_found`, or `conflict` with reason
// session_closed. A `conflict` with another reason (session_live) is not
// ours to fix.
func sessionGone(code protocol.Code, details map[string]string) bool {
	if code == protocol.CodeNotFound {
		return true
	}
	return code == protocol.CodeConflict && details["reason"] == reasonSessionClosed
}

// errorParts returns the code and details of a *protocol.Error, or
// `internal` and nil for anything else.
func errorParts(err error) (protocol.Code, map[string]string) {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		return perr.Code, perr.Details
	}
	return protocol.CodeInternal, nil
}

// reopen re-registers the session with a resume hint (see the file
// comment). It runs on the command writer (the RPC path) or on its own
// goroutine (an error event); reopening keeps two triggers from racing,
// and nothing is done once the exit path has begun.
func (w *watcher) reopen(s *session) {
	if !w.reopening.CompareAndSwap(false, true) {
		return
	}
	defer w.reopening.Store(false)
	if w.stopping() || s.heartbeatsStopped.Load() {
		return
	}
	snap := w.state.snapshot()
	name := snap.name
	if name == "" {
		name = harnessName
	}
	model, tokens := w.transcriptFacts(snap.transcriptPath)
	reg := &protocol.SessionRegistration{
		Harness:           harnessName,
		HarnessVersion:    w.harnessVersion,
		SessionName:       name,
		Activity:          snap.activity,
		Inbound:           snap.inbound,
		LeaseSeconds:      s.lease,
		Model:             model,
		ContextUsedTokens: tokens,
		Resume:            &protocol.ResumeRef{SessionID: w.sessionID},
	}
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.RegisterTimeout)
	res, err := w.client.Register(ctx, reg)
	cancel()
	switch {
	case err == nil && res.Resumed && res.SessionID == w.sessionID:
		w.log.Info("session re-opened", slog.String("session_id", w.sessionID))
		s.requestHeartbeat()
	case err == nil:
		cctx, ccancel := context.WithTimeout(context.Background(), adapterclient.WatchRequestTimeout)
		if _, cerr := w.client.Close(cctx, res.SessionID); cerr != nil {
			w.log.Debug("stray session not closed", adlog.Err(cerr))
		}
		ccancel()
		w.log.Warn("re-open answered another session; stopping", slog.String("session_id", w.sessionID))
		w.stop("session_gone")
	case codeOf(err) == protocol.CodeNotFound || codeOf(err) == protocol.CodeConflict:
		w.log.Warn("the session cannot be re-opened; stopping", slog.String("code", string(codeOf(err))), adlog.Err(err))
		w.stop("session_gone")
	default:
		w.log.Warn("re-open failed; the next heartbeat tries again", adlog.Err(err))
	}
}
