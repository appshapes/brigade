// state.go is the watcher's share of the manager digest's state folder
// (card 162, .context/plans/manager-digest.md §5.1 and §5.4): the
// retention any watcher applies to the folder the map names — an entry
// past its term goes, whichever machine wrote it — and the producer:
// whichever session is active produces the digest, the watcher of the
// first online session of the repository in roster order first, each
// later one after a delay set by its rank, and a marker in the folder
// stops the rest. No manager node: a quiet team reports nothing.

package watch

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
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
// produces, and the folder is pruned DefaultStatePruneDelay after the
// start and every DefaultStatePruneInterval.
const (
	DefaultStateCheckInterval = 20 * time.Second
	DefaultStatePruneDelay    = 2 * time.Minute
	DefaultStatePruneInterval = time.Hour
)

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

// produceCheck is one tick of the producer (§5.4): with a manager
// configured, the period elapsed since the marker and something written
// since it, this session's rank sets when it produces; at that tick the
// marker is read again and, when nobody produced meanwhile, the digest is
// built, sent and marked. A failure at any step is logged and the next
// tick tries again.
func (w *watcher) produceCheck(ctx context.Context) {
	now := w.deps.Clock()
	reports, err := state.ReadReports(w.stateFolder)
	if err != nil || reports.Manager == "" {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			w.log.Debug("state: reports.json unreadable; no digest", adlog.Err(err))
		}
		w.produceAt = time.Time{}
		return
	}
	marker, err := digest.ReadMarker(w.stateFolder)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.log.Debug("state: the digest marker is unreadable; read as none", adlog.Err(err))
	}
	if !digest.Due(reports, marker, now) {
		w.produceAt = time.Time{}
		return
	}
	entries := state.ReadFolder(w.stateFolder)
	decisions := state.ReadDecisions(w.stateFolder)
	if !digest.Changed(entries, decisions, marker) {
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
		w.log.Info("state: a digest is due", slog.Int("rank", rank))
	}
	if now.Before(w.produceAt) {
		return
	}
	// The last look before producing: a marker another session wrote
	// while this one waited stands this one down.
	if again, err := digest.ReadMarker(w.stateFolder); err == nil && (marker == nil || again.ProducedAt.After(marker.ProducedAt)) {
		w.log.Info("state: another session produced the digest meanwhile", slog.String("producer", again.SessionName))
		w.produceAt = time.Time{}
		return
	}
	w.produce(ctx, reports, marker, entries, decisions, now)
	w.produceAt = time.Time{}
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

// produce builds the digest, sends it through the first gateway of the
// recipients on the roster — else to the manager's own best session —
// and writes the marker.
func (w *watcher) produce(ctx context.Context, reports *state.Reports, marker *digest.Marker, entries []*state.Entry, decisions []*state.Decision, now time.Time) {
	snap := w.state.snapshot()
	since := time.Time{}
	if marker != nil {
		since = marker.ProducedAt
	}
	out := digest.Build(digest.Input{
		Team: w.teamName, Repository: snap.workspaceLabel, Producer: snap.name, Now: now, Since: since,
		Entries: entries, Decisions: decisions, Roster: w.produceRoster,
	})
	sctx, cancel := context.WithTimeout(ctx, adapterclient.DefaultTimeout)
	defer cancel()
	sent, err := digest.Deliver(sctx, w.client, w.sessionID, reports, w.produceRoster, out.Body, out.Summary, now)
	if err != nil {
		switch {
		case errors.Is(err, digest.ErrNoRecipient):
			w.log.Warn("state: no gateway of the recipients and no session of the manager on the roster; the digest waits")
		case ctx.Err() == nil:
			w.log.Warn("state: the digest was not sent", adlog.Err(err))
		}
		return
	}
	how := sent.How
	if err := digest.WriteMarker(w.stateFolder, &digest.Marker{
		ProducedAt: now, SessionID: w.sessionID, SessionName: snap.name,
		DecisionsOpen: out.DecisionsOpen, Commits: out.Commits, Active: out.Active,
	}); err != nil {
		w.log.Warn("state: the digest marker was not written", adlog.Err(err))
	}
	// The notice before the log line: a test that waits for the line
	// then reads the notice must find it.
	w.writeNotice(notice.TopicState, "Brigade digest: sent to the manager, "+strings.TrimPrefix(out.Summary, "Brigade digest "+snap.workspaceLabel+": "))
	w.log.Info("state: digest produced", slog.String("how", how), slog.Int("decisions_open", out.DecisionsOpen),
		slog.Int("commits", out.Commits), slog.Int("active", out.Active), slog.Int("bytes", len(out.Body)))
}
