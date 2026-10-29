// state.go is the watcher's share of the digest's state folder (card 162,
// .context/plans/manager-digest.md §5.1 and §5.4; card 167, one digest
// per subscriber): the retention any watcher applies to the folder the
// map names — an entry past its term goes, whichever machine wrote it —
// and the producer: whichever session is active produces the digests,
// the watcher of the first online session of the repository by
// registration time first, each later one after a delay set by its rank,
// and a marker per subscriber in the folder stops the rest. No manager
// node: a quiet team reports nothing.

package watch

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"time"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/notice"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// The production cadence: the folder is looked at every
// DefaultStateCheckInterval (local files only; the roster is read only
// when a digest is due), a producer of rank n waits n of them before it
// produces, the folder is pruned DefaultStatePruneDelay after the start
// and every DefaultStatePruneInterval, and a subscriber whose digest
// could not be sent is tried again after produceRetryAfter checks.
const (
	DefaultStateCheckInterval = 20 * time.Second
	DefaultStatePruneDelay    = 2 * time.Minute
	DefaultStatePruneInterval = time.Hour
	produceRetryAfter         = 15
)

// A dueSubscriber is one subscriber whose digest a tick found due, with
// the marker it was judged by (nil for their first).
type dueSubscriber struct {
	sub    *state.Subscriber
	marker *digest.Marker
}

// runState is the state goroutine, started when the map names a state
// folder and ended by ctx with the session: the prune on its cadence,
// and the producer's check on every tick.
func (w *watcher) runState(ctx context.Context) {
	prune := time.NewTimer(w.deps.StatePruneDelay)
	defer prune.Stop()
	check := time.NewTicker(w.deps.StateCheckInterval)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			if n := state.PruneFolder(w.stateFolder, w.deps.Clock()); n > 0 {
				w.log.Info("state: expired entries pruned from the folder", slog.Int("removed", n))
			}
			prune.Reset(w.deps.StatePruneInterval)
		case <-check.C:
			w.produceCheck(ctx)
		}
	}
}

// produceCheck is one tick of the producer (§5.4, per subscriber): with
// anyone subscribed, a subscriber whose period has elapsed since their
// marker and something written since it is due; this session's rank,
// taken once for the batch, sets when it produces; at that tick each due
// subscriber's marker is read again and, when nobody produced theirs
// meanwhile, their digest is built, sent and marked. A failure at any
// step is logged and a later tick tries again.
func (w *watcher) produceCheck(ctx context.Context) {
	now := w.deps.Clock()
	reports, err := state.ReadReports(w.stateFolder)
	if err != nil || len(reports.Subscribers) == 0 {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			w.log.Debug("state: reports.json unreadable; no digest", adlog.Err(err))
		}
		w.produceAt = time.Time{}
		return
	}
	due := w.dueSubscribers(reports, now)
	if len(due) == 0 {
		w.produceAt = time.Time{}
		return
	}
	entries := state.ReadFolder(w.stateFolder)
	decisions := state.ReadDecisions(w.stateFolder)
	due = slices.DeleteFunc(due, func(d dueSubscriber) bool { return !digest.Changed(entries, decisions, d.marker) })
	if len(due) == 0 {
		w.produceAt = time.Time{}
		return
	}
	if w.produceAt.IsZero() {
		roster, ok := w.digestRoster(ctx)
		if !ok {
			return
		}
		rank := digest.Rank(w.sessionID, w.state.snapshot().workspaceLabel, roster)
		if rank < 0 {
			w.log.Debug("state: this session has no rank on the roster; another produces")
			return
		}
		w.produceAt = now.Add(time.Duration(rank) * w.deps.StateCheckInterval)
		w.produceRoster = roster
		w.log.Info("state: a digest is due", slog.Int("rank", rank), slog.Int("subscribers", len(due)))
	}
	if now.Before(w.produceAt) {
		return
	}
	// The last look before producing: a marker another session wrote for
	// a subscriber while this one waited stands this one down for them.
	due = slices.DeleteFunc(due, func(d dueSubscriber) bool {
		again, err := digest.MarkerOf(w.stateFolder, reports, d.sub)
		if err == nil && (d.marker == nil || again.ProducedAt.After(d.marker.ProducedAt)) {
			w.log.Info("state: another session produced the digest meanwhile", slog.String("producer", again.SessionName), slog.String("label", d.sub.Label))
			return true
		}
		return false
	})
	if len(due) > 0 {
		w.produce(ctx, due, entries, decisions, now)
	}
	w.produceAt = time.Time{}
}

// dueSubscribers is every subscriber whose digest is due at now by their
// marker, the ones under a retry hold left out.
func (w *watcher) dueSubscribers(reports *state.Reports, now time.Time) []dueSubscriber {
	var due []dueSubscriber
	for i := range reports.Subscribers {
		s := &reports.Subscribers[i]
		if until, held := w.produceRetry[strings.ToLower(s.Label)]; held && now.Before(until) {
			continue
		}
		marker, err := digest.MarkerOf(w.stateFolder, reports, s)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			w.log.Debug("state: a digest marker is unreadable; read as none", adlog.Err(err), slog.String("label", s.Label))
		}
		if digest.Due(s, marker, now) {
			due = append(due, dueSubscriber{sub: s, marker: marker})
		}
	}
	return due
}

// digestRoster reads the online roster for the rank and the delivery,
// once per due digest.
func (w *watcher) digestRoster(ctx context.Context) ([]protocol.SessionRecord, bool) {
	lctx, cancel := context.WithTimeout(ctx, adapterclient.DefaultTimeout)
	defer cancel()
	list, err := w.client.ListSessions(lctx, false)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("state: the roster could not be read; no digest this tick", adlog.Err(err))
		}
		return nil, false
	}
	return list.Sessions, true
}

// produce builds each due subscriber's digest — their period start,
// their filter — sends it through the first gateway of their recipients
// on the roster, else to their own best session, and writes their
// marker. A failure for one never stops the others: they are held for
// produceRetryAfter checks and the notice says who was not reached.
func (w *watcher) produce(ctx context.Context, due []dueSubscriber, entries []*state.Entry, decisions []*state.Decision, now time.Time) {
	snap := w.state.snapshot()
	var got, missed []string
	for _, d := range due {
		since := time.Time{}
		if d.marker != nil {
			since = d.marker.ProducedAt
		}
		out := digest.Build(digest.Input{
			Team: w.teamName, Repository: snap.workspaceLabel, Producer: snap.name, Now: now, Since: since,
			Entries: entries, Decisions: decisions, Roster: w.produceRoster, For: d.sub.Label, Only: d.sub.Only,
		})
		sctx, cancel := context.WithTimeout(ctx, adapterclient.DefaultTimeout)
		sent, err := digest.Deliver(sctx, w.client, w.sessionID, d.sub, w.produceRoster, out.Body, out.Summary, now)
		cancel()
		if err != nil {
			switch {
			case errors.Is(err, digest.ErrNoRecipient):
				w.log.Warn("state: no gateway of the recipients and no session of the subscriber on the roster; their digest waits", slog.String("label", d.sub.Label))
			case ctx.Err() == nil:
				w.log.Warn("state: the digest was not sent", adlog.Err(err), slog.String("label", d.sub.Label))
			}
			if w.produceRetry == nil {
				w.produceRetry = map[string]time.Time{}
			}
			w.produceRetry[strings.ToLower(d.sub.Label)] = now.Add(produceRetryAfter * w.deps.StateCheckInterval)
			missed = append(missed, d.sub.Label)
			continue
		}
		delete(w.produceRetry, strings.ToLower(d.sub.Label))
		if err := digest.WriteMarker(w.stateFolder, &digest.Marker{
			Label: d.sub.Label, ProducedAt: now, SessionID: w.sessionID, SessionName: snap.name,
			DecisionsOpen: out.DecisionsOpen, Commits: out.Commits, Active: out.Active,
		}); err != nil {
			w.log.Warn("state: the digest marker was not written", adlog.Err(err), slog.String("label", d.sub.Label))
		}
		got = append(got, d.sub.Label+" ("+sent.How+")")
		w.log.Info("state: digest produced", slog.String("label", d.sub.Label), slog.String("how", sent.How), slog.Int("decisions_open", out.DecisionsOpen),
			slog.Int("commits", out.Commits), slog.Int("active", out.Active), slog.Int("bytes", len(out.Body)))
	}
	// The notice before the summary line: a test that waits for the line
	// then reads the notice must find it.
	line := "Brigade digest: "
	if len(got) > 0 {
		line += "sent to " + strings.Join(got, ", ")
	}
	if len(missed) > 0 {
		if len(got) > 0 {
			line += "; "
		}
		line += "not sent to " + strings.Join(missed, ", ") + " (no gateway of their recipients and no session of theirs on the roster, or the send failed)"
	}
	w.writeNotice(notice.TopicState, line)
	w.log.Info("state: digests produced", slog.Int("sent", len(got)), slog.Int("missed", len(missed)))
}
