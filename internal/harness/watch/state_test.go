//go:build !windows

package watch_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/testutil"
)

// TestStatePrunesTheFolder (card 162, state.go): a watcher whose map
// names a state folder prunes the folder's expired entries — a
// teammate's session that ended long ago, one whose machine never came
// back — on its cadence, and keeps the live and the recently ended ones;
// a watcher whose map names none touches no folder.
func TestStatePrunesTheFolder(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	root := t.TempDir()
	folder := filepath.Join(root, ".brigade", "state")
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.WorkspaceLabel = syncRepo
		m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, syncScope
		m.StateFolder = folder
	})
	now := time.Now().UTC()
	theirs := state.Store{StateDir: filepath.Join(t.TempDir(), "s"), Folder: folder}
	expired := state.New("ended-long-ago", "a", now.Add(-40*24*time.Hour))
	expired.EndedAt = now.Add(-30 * 24 * time.Hour)
	gone := state.New("machine-never-back", "b", now.Add(-40*24*time.Hour))
	gone.UpdatedAt = now.Add(-20 * 24 * time.Hour)
	live := state.New("live", "c", now)
	live.UpdatedAt = now
	recent := state.New("ended-recently", "d", now.Add(-5*24*time.Hour))
	recent.EndedAt = now.Add(-2 * 24 * time.Hour)
	for _, e := range []*state.Entry{expired, gone, live, recent} {
		if err := theirs.Save(e); err != nil {
			t.Fatal(err)
		}
	}
	deps := fx.deps()
	// No sync engine in this test: an adapter that is not there ends the
	// sync goroutine at its describe, and the state goroutine runs alone.
	deps.SyncCommand = []string{filepath.Join(t.TempDir(), "no-such-sync-adapter")}
	deps.StatePruneDelay = 50 * time.Millisecond
	deps.StatePruneInterval = time.Hour
	fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logHas("state: expired entries pruned from the folder", map[string]any{"removed": float64(2)})
	})
	ids := map[string]bool{}
	for _, e := range state.ReadFolder(folder) {
		ids[e.SessionID] = true
	}
	if len(ids) != 2 || !ids["live"] || !ids["ended-recently"] {
		t.Fatalf("folder after the prune: %v", ids)
	}
}
