package watch_test

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesync"
)

// syncRepo is the workspace label the tests' synced sessions share.
const syncRepo = "brigade"

// registerPeer registers a session of the fixture's peer principal with
// a workspace label and a sync peer (either may be nil), optionally
// closing it again so it is listed offline.
func (fx *fixture) registerPeer(label, peer *string, closeIt bool) {
	fx.t.Helper()
	ctx, cancel := context.WithTimeout(fx.t.Context(), waitShort)
	defer cancel()
	reg, err := fx.peer.Register(ctx, &protocol.SessionRegistration{
		Harness: "claude-code", HarnessVersion: "test", SessionName: "teammate",
		Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
		WorkspaceLabel: label, SyncPeer: peer,
	})
	if err != nil {
		fx.t.Fatalf("register teammate: %v", err)
	}
	if closeIt {
		if _, err := fx.peer.Close(ctx, reg.SessionID); err != nil {
			fx.t.Fatalf("close teammate: %v", err)
		}
	}
}

// ownSyncPeer is the watched session's sync_peer as the backend holds it.
func (fx *fixture) ownSyncPeer() string {
	fx.t.Helper()
	ctx, cancel := context.WithTimeout(fx.t.Context(), waitShort)
	defer cancel()
	list, err := fx.peer.ListSessions(ctx, true)
	if err != nil {
		fx.t.Fatalf("list: %v", err)
	}
	for _, s := range list.Sessions {
		if s.SessionID == fx.sessionID && s.SyncPeer != nil {
			return *s.SyncPeer
		}
	}
	return ""
}

func ptr(s string) *string { return &s }

// syncMap freezes a sync configuration into the fixture's map, as the
// SessionStart hook does (hook/start.go resolveSync).
func (fx *fixture) syncMap(adapter string, root string, folders ...string) {
	fx.t.Helper()
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.WorkspaceLabel = syncRepo
		m.SyncAdapter = adapter
		m.SyncFolders = folders
		m.SyncRoot = root
	})
}

// readNotice is the notice file's one line, "" when there is none.
func (fx *fixture) readNotice() string {
	b, err := os.ReadFile(fx.noticePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// TestSyncDrivesTheAdapter is the watcher half of folder-sync plan §4.3
// end to end against the fs backend and the fake sync adapter: describe,
// attach — whose descriptor reaches the roster as this session's
// sync_peer through a heartbeat at the next liveness tick, not the next
// interval — then apply with the project's folders (id, path, label as
// derived) and exactly the teammates' peers this adapter can use (not
// this session, same repository, this adapter's prefix stripped, offline
// included, each descriptor once and never this machine's own), the
// summary notice, and detach once the watcher stops.
func TestSyncDrivesTheAdapter(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	repo, other := ptr(syncRepo), ptr("another-repo")
	fx.registerPeer(repo, ptr("syncthing:PEER-A"), false)
	fx.registerPeer(repo, ptr("syncthing:PEER-A"), false) // the same machine twice: offered once
	fx.registerPeer(repo, ptr("syncthing:PEER-D"), true)  // offline: still introduced
	fx.registerPeer(repo, ptr("other:PEER-B"), false)     // another adapter's peer
	fx.registerPeer(other, ptr("syncthing:PEER-C"), false)
	fx.registerPeer(repo, ptr("syncthing:"+fakesync.SelfPeer), false) // another session on this machine
	fx.registerPeer(repo, nil, false)                                 // a session without sync
	fx.registerPeer(nil, ptr("syncthing:PEER-E"), false)              // no repository shared

	root := t.TempDir()
	fx.syncMap("syncthing", root, ".context/plans", "docs")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{
		Apply: `{"folders":[{"id":"a","state":"idle"},{"id":"b","state":"scanning"}],"peers":[{"peer":"PEER-A","connected":true},{"peer":"PEER-D","connected":false}]}`,
	})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	// One interval longer than the test: every heartbeat after ready is
	// the one the attach asked for, and exactly one apply runs.
	deps.HeartbeatInterval = time.Hour
	deps.SyncInterval = time.Hour
	r := fx.start(deps, fx.args()...)

	testutil.Eventually(t, waitShort, pollEvery, func() bool { return slices.Contains(fake.Verbs(t), "apply") })
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.ownSyncPeer() == "syncthing:"+fakesync.SelfPeer
	})
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.readNotice() == "Brigade sync: 2 folders, 1 of 2 peers connected"
	})

	recs := fake.Records(t)
	if got := fake.Verbs(t); !slices.Equal(got, []string{"describe", "attach", "apply"}) {
		t.Fatalf("verbs before the stop = %v", got)
	}
	var apply foldersync.ApplyRequest
	if err := json.Unmarshal(recs[2].Request, &apply); err != nil {
		t.Fatal(err)
	}
	wantFolders := []foldersync.Folder{
		{ID: foldersync.FolderID(fx.teamRef, ".context/plans"), Path: filepath.Join(root, ".context/plans"), Label: syncRepo + "/.context/plans"},
		{ID: foldersync.FolderID(fx.teamRef, "docs"), Path: filepath.Join(root, "docs"), Label: syncRepo + "/docs"},
	}
	if apply.SessionID != fx.sessionID || apply.StateDir != fx.dirs.BrigadeState || !slices.Equal(apply.Folders, wantFolders) {
		t.Fatalf("apply request = %+v\nwant folders %+v", apply, wantFolders)
	}
	slices.SortFunc(apply.Peers, func(a, b foldersync.Peer) int { return strings.Compare(a.Peer, b.Peer) })
	wantPeers := []foldersync.Peer{{Peer: "PEER-A", Label: "peer@example.com"}, {Peer: "PEER-D", Label: "peer@example.com"}}
	if !slices.Equal(apply.Peers, wantPeers) {
		t.Fatalf("apply peers = %+v, want %+v", apply.Peers, wantPeers)
	}
	for _, rec := range recs {
		if rec.StateDir != fx.dirs.BrigadeState {
			t.Errorf("%s ran with BRIGADE_STATE_DIR %q", rec.Verb, rec.StateDir)
		}
	}

	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	recs = fake.Records(t)
	if last := recs[len(recs)-1]; last.Verb != "detach" || !strings.Contains(string(last.Request), `"session_id":"`+fx.sessionID+`"`) {
		t.Fatalf("the last call is %s %s, want this session's detach", last.Verb, last.Request)
	}
	// Scalars only: no request or result body reaches the watcher's log.
	if data, err := os.ReadFile(fx.logPath()); err != nil || strings.Contains(string(data), "PEER-A") || strings.Contains(string(data), root) {
		t.Fatalf("the watcher log carries adapter data (err %v)", err)
	}
}

// TestSyncReattachesEveryRound: `attach` precedes every apply after the
// first (it is idempotent), so a reference a replaced watcher's late
// detach dropped comes back within one interval.
func TestSyncReattachesEveryRound(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	fx.syncMap("syncthing", t.TempDir(), "docs")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	deps.SyncInterval = 50 * time.Millisecond
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		n := 0
		for _, v := range fake.Verbs(t) {
			if v == "apply" {
				n++
			}
		}
		return n >= 3
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	verbs := fake.Verbs(t)
	if !slices.Equal(verbs[:5], []string{"describe", "attach", "apply", "attach", "apply"}) || verbs[len(verbs)-1] != "detach" {
		t.Fatalf("verbs = %v", verbs)
	}
}

// TestSyncAdapterUnavailableIsOneNotice: an adapter that cannot be
// spawned, or answers describe with an error, earns one notice line; the
// goroutine ends there — no attach, no detach — and the session goes on.
func TestSyncAdapterUnavailableIsOneNotice(t *testing.T) {
	t.Parallel()
	t.Run("an external adapter not on PATH", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useFS()
		fx.syncMap("nosuch", t.TempDir(), "docs")
		r := fx.start(fx.deps(), fx.args()...)
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return fx.readNotice() == "Brigade sync: brigade-sync-nosuch is not on PATH; file sync is off for this session"
		})
		fx.waitLog("watch ready", nil)
		if code := r.stopAndWait(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("describe answers an error", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useFS()
		fx.syncMap("syncthing", t.TempDir(), "docs")
		fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{ErrorOn: []string{"describe"}, ErrorMessage: "syncthing is not on PATH"})
		deps := fx.deps()
		deps.SyncCommand = fake.Argv
		r := fx.start(deps, fx.args()...)
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return fx.readNotice() == "Brigade sync: the syncthing sync adapter is not usable (unavailable: syncthing is not on PATH); file sync is off for this session"
		})
		if code := r.stopAndWait(); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if got := fake.Verbs(t); !slices.Equal(got, []string{"describe"}) {
			t.Fatalf("verbs = %v, want describe alone", got)
		}
		if fx.ownSyncPeer() != "" {
			t.Fatal("a session whose adapter failed published a sync peer")
		}
	})
}

// TestNoSyncWithoutTheMapMembers: a map without sync members runs no
// sync goroutine at all, even with an adapter at hand.
func TestNoSyncWithoutTheMapMembers(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	fx.writeMap()
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	r := fx.start(deps, fx.args()...)
	fx.waitLog("watch ready", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := fake.Verbs(t); len(got) != 0 {
		t.Fatalf("verbs = %v, want none", got)
	}
}
