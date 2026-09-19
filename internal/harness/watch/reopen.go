package watch

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"

	"github.com/appshapes/brigade/internal/adapterkit"
	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/account"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/protocol"
)

// humanLabel resolves the member's default display label from the `label`
// option the by-pid map carries (card 24, part C). The option decides:
// `none` sends nothing, a literal is that text, and `account` — the
// default — is the Claude account email, read here at the point of use
// rather than carried in the map, so no file under the state directory
// ever holds a member's email. Best effort without exception: an
// unreadable account is a debug line WITHOUT the value and an empty
// label, never a failed registration.
func (w *watcher) humanLabel(option string) string {
	return config.LabelFor(config.ParseLabelOption(option), func() string {
		email, err := account.Email(w.environ)
		if err != nil {
			w.log.Debug("claude account email unavailable", adlog.Err(err))
		}
		return email
	})
}

// doingModeCarries reports whether the map's doing_mode lets a re-open
// carry the session's doing line (card 25, plan 5.3): off and unsupported
// carry nothing; every other word does, and so does an absent mode — a map
// written before the mode existed — because the verb publishes under it
// too, and a line the verb may publish is a line the re-open must keep.
func doingModeCarries(mode string) bool {
	return mode != doing.ModeOff && mode != doing.ModeUnsupported
}

// reasonNotListed is the watcher's own word for a read-back whose list
// came back without the session's record (a truncated list, plan 8); it
// sits beside doing.Clean's reasons on the `doing line not carried` line.
const reasonNotListed = "not_listed"

// ownDoingLine reads the session's doing line back for the re-open
// registration (plan 5.3): `session list --include-offline` under
// WatchRequestTimeout, the own record's session_description, then
// doing.Clean with the watcher's HOME (ChildEnv keeps it) and the messaging
// token, and NO cwd — none is recorded anywhere, and the watcher's differs
// from the verb's. What comes back is what the backend held a moment
// earlier, and only the owner can have written it: never a wrong value,
// never a teammate's. Anything short of a clean non-empty line is one
// fixed-text debug line WITHOUT the value and no member at all — never
// "", which the re-open would then send to be refused, and refused again
// on every heartbeat (reopen's default arm retries forever). A carry that
// was due and failed removes the prompt hook's nudge stamp (plan 5.3, 8),
// so the next eligible prompt tells the model its line is blank rather
// than letting a stale reminder say it still stands — and "failed" is
// every miss that can have blanked a sentence the backend held: the list
// refused or timing out, the own record missing from a truncated list,
// and a value Clean refuses (a stricter watcher after a plugin update, or
// a third-party writer's), since the resume registration then omits the
// member and both bundled adapters write that absence over the stored
// value. The one miss that keeps the stamp is a listed record holding
// nothing: nothing was held, so nothing is lost, and the reminder's
// state is as true after the re-open as before it.
func (w *watcher) ownDoingLine() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.WatchRequestTimeout)
	raw, listed, err := w.client.OwnDescription(ctx, w.sessionID)
	cancel()
	switch {
	case err != nil:
		w.log.Debug("doing line not read back; the re-open carries none", adlog.Err(err))
	case !listed:
		w.log.Debug("doing line not carried", slog.String("reason", reasonNotListed))
	default:
		text, reason := doing.Clean(raw, adapterkit.Getenv(w.environ, "HOME"), w.rc.token)
		if text != "" {
			return text, true
		}
		w.log.Debug("doing line not carried", slog.String("reason", reason))
		if reason == doing.ReasonEmpty {
			return "", false
		}
	}
	w.removeDoingNudge()
	return "", false
}

// removeDoingNudge removes the prompt hook's doing-nudge stamp, best
// effort: a stamp that is not there is the wanted state, and any other
// failure is a debug line — the reminder degrades to silence, never the
// re-open to a failure.
func (w *watcher) removeDoingNudge() {
	path := config.DoingNudgeStamp(w.rc.env.StateDir, w.rc.env.ClaudePID)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.log.Debug("doing nudge stamp not removed", adlog.Err(err))
	}
}

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
// the current name, activity, inbound policy and lease, the transcript
// facts, and — read back from the backend first, when the adapter
// announces session.description and the mode allows — the session's doing
// line, so a re-open the model never sees does not blank a line it
// published (card 25, plan 5.3). A resumed answer with the same id means
// the session is back and the next heartbeat is requested at once.
// Anything else stops the watcher with reason session_gone: a fresh id
// (the adapter declined the resume — closed first, best effort, so no
// stray session lingers), or not_found / conflict:session_live (gone for
// good, or held by another process); the next prompt hook respawns a
// watcher that tries again, and a new SessionStart registers afresh.
// Other failures are logged and the next heartbeat retries.

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
// and nothing is done once the exit path has begun. Either way it is a
// state-directory writer — the register opens the adapter log and spawns
// a child — so it runs under goWriter and the watcher's exit joins it
// (writers.go): the stopping() check above is a fast path, never the
// ordering guarantee.
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
	// A resume that omits the label clears it at the backend, so the
	// re-open carries the map's (P11-5).
	if snap.workspaceLabel != "" {
		label := snap.workspaceLabel
		reg.WorkspaceLabel = &label
	}
	// The member's default label rides every re-registration too (card 24,
	// part C), so a membership with an empty label is filled by whichever
	// registration reaches an adapter that announces session.human_label
	// first — the session start, or this re-open after a plugin version
	// change replaced the watcher. Adopting it is the adapter's decision
	// and it never overwrites a label that is already there, so sending it
	// again costs nothing.
	if label := w.humanLabel(snap.labelOption); label != "" {
		reg.HumanLabel = &label
	}
	// The doing line is carried the same way, read back first (card 25).
	// It happens here, inline, and not on a goroutine of its own: the
	// re-open is the writer the exit joins (writers.go), and a list that
	// outlived it would be the P14-6 bug again. The exit path may have
	// begun while the list ran, so the stopping check is repeated after it.
	carried := false
	if s.descriptionCapable && doingModeCarries(snap.doingMode) {
		if text, ok := w.ownDoingLine(); ok {
			reg.SessionDescription = &text
			carried = true
		}
		if w.stopping() || s.heartbeatsStopped.Load() {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.RegisterTimeout)
	res, err := w.client.Register(ctx, reg)
	cancel()
	switch {
	case err == nil && res.Resumed && res.SessionID == w.sessionID:
		w.log.Info("session re-opened", slog.String("session_id", w.sessionID), slog.Bool("described", carried))
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
