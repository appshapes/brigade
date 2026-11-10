// askhold.go is the watcher's share of a held decision's ask (card 236,
// internal/harness/state/hold.go): a question `brigade decision` recorded
// for this session's own user is asked in the conversation, and this
// watcher — the asking session's, the only process that outlives the
// command — ends the hold. A prompt in the conversation since the ask
// ends it for good with nothing sent: the user saw the question there.
// Otherwise, once the member's wait has passed, the ask goes out by the
// road every ask takes: the first gateway of their recipients on the
// roster, else another session of theirs. The check reads local files
// only; the roster is read when an ask is due. None of this is the
// inbound `hold` policy, which keeps messages sent TO a session
// (hold_test.go, internal/harness/inbound).

package watch

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"time"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/notice"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// The cadence of a due ask that cannot go out yet — the roster or the
// subscribers' file unreadable, no road for it on the roster, the send
// refused: it is tried again after holdRetryAfter checks, and until
// DefaultHoldGiveUp after its wait ended; then the hold ends unsent.
const (
	holdRetryAfter    = 3
	DefaultHoldGiveUp = 15 * time.Minute
)

// holdCheck is one tick for this session's held asks: each is ended by a
// close or a prompt since it was recorded (stillHeld), left alone while
// its wait runs, given up when this watcher started after the wait had
// ended (the session was not running then, and whoever resumed it is in
// the conversation), or sent.
func (w *watcher) holdCheck(ctx context.Context) {
	store := state.Store{StateDir: w.rc.env.StateDir, Folder: w.stateFolder}
	held := store.HeldBy(w.sessionID)
	if len(held) == 0 {
		return
	}
	now := w.deps.Clock()
	var due []*state.Decision
	for _, h := range held {
		d, ok := w.stillHeld(store, h.ID, now)
		switch {
		case !ok:
		case now.Before(d.HeldUntil):
		case d.HeldUntil.Before(w.startedAt):
			w.endHold(store, d.ID, state.HoldMissed, now)
		default:
			if until, waiting := w.holdRetry[d.ID]; !waiting || !now.Before(until) {
				due = append(due, d)
			}
		}
	}
	if len(due) == 0 {
		return
	}
	roster, ok := w.digestRoster(ctx)
	reports, err := state.ReadReports(w.stateFolder)
	if errors.Is(err, fs.ErrNotExist) {
		reports, err = nil, nil // nobody subscribed: the ask goes to another session of the member
	}
	if !ok || err != nil {
		// Without the roster there is no road, and without the file the
		// member's recipients are unknown: neither is a reason to send the
		// ask somewhere else.
		if err != nil {
			w.log.Warn("decision: reports.json unreadable; the held ask waits", adlog.Err(err))
		}
		for _, d := range due {
			w.retryOrGiveUp(store, d, now)
		}
		return
	}
	for _, d := range due {
		w.releaseHold(ctx, store, d.ID, reports, roster)
	}
}

// stillHeld reads the decision again and answers it while its hold
// stands. A hold ends here, with nothing sent, when the decision was
// closed — in this machine's copy, or in the folder's by another session
// of the member on another machine, whose close is taken over rather than
// written over — and when a prompt arrived in the conversation since the
// ask.
func (w *watcher) stillHeld(store state.Store, id string, now time.Time) (*state.Decision, bool) {
	d, err := store.LoadDecision(id)
	if err != nil || !d.Held() {
		delete(w.holdRetry, id)
		return nil, false
	}
	if theirs, err := state.ReadDecision(w.stateFolder, id); err == nil && !theirs.Open() {
		d.ClosedAt, d.Outcome, d.ClosedBy, d.UpdatedAt = theirs.ClosedAt, theirs.Outcome, theirs.ClosedBy, theirs.UpdatedAt
		if err := store.SaveDecision(d); err != nil {
			w.log.Warn("decision: a close made elsewhere was not taken over", adlog.Err(err))
		} else {
			w.log.Info("decision: closed by another session of the member; the hold is over")
		}
		delete(w.holdRetry, id)
		return nil, false
	}
	if e, err := store.Load(w.sessionID); err == nil && e.LastPromptAt.After(d.CreatedAt) {
		w.endHold(store, id, state.HoldPrompt, now)
		return nil, false
	}
	return d, true
}

// releaseHold sends one due ask and records how it went. The decision and
// the conversation's last prompt are read again first: a close or a
// prompt that landed while the roster was read stands. With no road for
// it — no gateway of the recipients and no other session of the member
// on the roster — or a send that fails, it is tried again later
// (retryOrGiveUp).
func (w *watcher) releaseHold(ctx context.Context, store state.Store, id string, reports *state.Reports, roster []protocol.SessionRecord) {
	now := w.deps.Clock()
	d, ok := w.stillHeld(store, id, now)
	if !ok {
		return
	}
	var recipients []string
	if reports != nil {
		if sub := reports.Subscriber(d.To); sub != nil {
			recipients = sub.Recipients
		}
	}
	route, ok := digest.PickRoute(recipients, digest.OtherSessionOf(w.sessionID, roster), roster)
	if !ok {
		w.retryOrGiveUp(store, d, now)
		return
	}
	body, summary := digest.AskMessage(d, route)
	sctx, cancel := context.WithTimeout(ctx, adapterclient.DefaultTimeout)
	// The key is the hold's own time, so every attempt at this ask is one
	// message however many ticks it takes.
	resp, err := w.client.Send(sctx, &protocol.SendRequest{
		SenderSessionID: w.sessionID, RecipientSessionID: route.SessionID, Body: body, Summary: summary,
		IdempotencyKey: digest.IdempotencyKey(w.sessionID, route.SessionID, body, d.HeldUntil),
	})
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("decision: the held ask was not sent", adlog.Err(err), slog.String("how", route.How))
			w.retryOrGiveUp(store, d, now)
		}
		return
	}
	delete(w.holdRetry, id)
	// Read once more before the write: a close that landed while the send
	// was under way stands, with the fact that the ask went out beside it.
	cur, err := store.LoadDecision(id)
	if err != nil {
		w.log.Warn("decision: the sent ask was not recorded", adlog.Err(err))
		return
	}
	cur.Sent, cur.SentTo, cur.UpdatedAt = route.How, route.To, w.deps.Clock()
	if err := store.SaveDecision(cur); err != nil {
		w.log.Warn("decision: the sent ask was not recorded", adlog.Err(err))
	}
	line := "Brigade decision " + id + ": your user did not answer here within their wait, so it was sent to them through the " + route.How + " gateway; close it with the outcome when they answer."
	if route.How == state.SentSession {
		line = "Brigade decision " + id + ": your user did not answer here within their wait, so it was sent to another session of theirs as a team message; close it with the outcome when they answer."
	}
	w.holdNotice(id, line)
	w.log.Info("decision: held ask sent", slog.String("how", route.How), slog.String("message_id", resp.MessageID))
}

// retryOrGiveUp keeps a due ask that could not go out back for
// holdRetryAfter checks, and ends its hold unsent once HoldGiveUp has
// passed since its wait ended: the digest lists the decision, and the
// session is told.
func (w *watcher) retryOrGiveUp(store state.Store, d *state.Decision, now time.Time) {
	if now.Sub(d.HeldUntil) < w.deps.HoldGiveUp {
		if w.holdRetry == nil {
			w.holdRetry = map[string]time.Time{}
		}
		w.holdRetry[d.ID] = now.Add(holdRetryAfter * w.deps.StateCheckInterval)
		return
	}
	w.endHold(store, d.ID, state.HoldUnsent, now)
	w.holdNotice(d.ID, "Brigade decision "+d.ID+": your user did not answer here within their wait, and it could not be sent (no gateway of their recipients and no other session of theirs on the roster, or the send failed); the next digest lists it.")
}

// holdNotice records the line for one decision under a topic of its own,
// so the lines of two asks released between two prompts both reach the
// session.
func (w *watcher) holdNotice(id, line string) {
	w.writeNotice(notice.TopicDecision+":"+id, line)
}

// endHold records how a hold ended without a send, on the decision as it
// is now: one closed or sent meanwhile is left as it is.
func (w *watcher) endHold(store state.Store, id, how string, now time.Time) {
	delete(w.holdRetry, id)
	d, err := store.LoadDecision(id)
	if err != nil || !d.Held() {
		return
	}
	d.HoldEnded, d.UpdatedAt = how, now
	if err := store.SaveDecision(d); err != nil {
		w.log.Warn("decision: the end of the hold was not recorded", adlog.Err(err))
		return
	}
	w.log.Info("decision: hold ended with nothing sent", slog.String("how", how))
}
