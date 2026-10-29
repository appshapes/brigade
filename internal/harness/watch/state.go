// state.go is the watcher's share of the manager digest's state folder
// (card 162, .context/plans/manager-digest.md §5.1): the retention any
// watcher applies to the folder the map names — an entry past its term
// goes, whichever machine wrote it, so a teammate's machine that never
// came back to end or prune its own session leaves nothing behind.

package watch

import (
	"context"
	"log/slog"
	"time"

	"github.com/appshapes/brigade/internal/harness/state"
)

// The production cadence of the folder prune: a first pass shortly
// after the watcher starts, then once an hour. Nothing here reads the
// backend or starts a process; a pass is one directory listing.
const (
	DefaultStatePruneDelay    = 2 * time.Minute
	DefaultStatePruneInterval = time.Hour
)

// runState is the state goroutine, started when the map names a state
// folder and ended by ctx with the session: it prunes the folder's
// expired entries on its cadence.
func (w *watcher) runState(ctx context.Context) {
	timer := time.NewTimer(w.deps.StatePruneDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if n := state.PruneFolder(w.stateFolder, w.deps.Clock()); n > 0 {
			w.log.Info("state: expired entries pruned from the folder", slog.Int("removed", n))
		}
		timer.Reset(w.deps.StatePruneInterval)
	}
}
