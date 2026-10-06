package watch_test

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/harness/notice"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakesync"
)

// syncRepo is the workspace label the tests' synced sessions share.
const syncRepo = "brigade"

// syncScope is the repository name the hook froze for the folder ids. It
// differs from the label on purpose: a member may set the label to
// anything, and the ids must not follow it.
const syncScope = "brigade-repository"

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
		m.SyncScope = syncScope
	})
}

// readNotice is the notice file's one line, "" when there is none.
func (fx *fixture) readNotice() string {
	fx.t.Helper()
	// Every line a sync test reads is folder sync's own: written under
	// another topic it would replace, and be replaced by, the watcher's.
	notices, _ := notice.Read(fx.noticePath())
	for _, n := range notices {
		if n.Topic != notice.TopicSync {
			fx.t.Errorf("a sync notice was written under the topic %q: %q", n.Topic, n.Text)
		}
	}
	return strings.TrimSpace(fx.notice())
}

// TestSyncDrivesTheAdapter is the watcher half of folder-sync plan §4.3
// end to end against the fs backend and the fake sync adapter: describe,
// attach — whose descriptor reaches the roster as this session's
// sync_peer through a heartbeat at the next liveness tick, not the next
// interval — then apply with the project's folders (id, path, label and
// earlier id, derived from the repository's name and not from the
// session's label) and exactly the teammates' peers this adapter can use (not
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
	deps.SyncListInterval = time.Hour
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
	// attach carries the watcher's own pid (in process here: this test's),
	// so the adapter can prune the reference of a watcher that died.
	var attach foldersync.SessionRequest
	if err := json.Unmarshal(recs[1].Request, &attach); err != nil {
		t.Fatal(err)
	}
	if attach.SessionID != fx.sessionID || attach.PID != os.Getpid() {
		t.Fatalf("attach request = %s, want this session and pid %d", recs[1].Request, os.Getpid())
	}
	var apply foldersync.ApplyRequest
	if err := json.Unmarshal(recs[2].Request, &apply); err != nil {
		t.Fatal(err)
	}
	wantFolders := []foldersync.Folder{
		{
			ID: foldersync.FolderID(fx.teamRef, syncScope, ".context/plans"), Path: filepath.Join(root, ".context/plans"),
			Label: syncScope + "/.context/plans", Replaces: foldersync.LegacyFolderID(fx.teamRef, ".context/plans"),
		},
		{
			ID: foldersync.FolderID(fx.teamRef, syncScope, "docs"), Path: filepath.Join(root, "docs"),
			Label: syncScope + "/docs", Replaces: foldersync.LegacyFolderID(fx.teamRef, "docs"),
		},
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
	wantDetach := `{"state_dir":"` + fx.dirs.BrigadeState + `","session_id":"` + fx.sessionID + `","pid":` + strconv.Itoa(os.Getpid()) + `}`
	if last := recs[len(recs)-1]; last.Verb != "detach" || string(last.Request) != wantDetach {
		t.Fatalf("the last call is %s %s, want %s", last.Verb, last.Request, wantDetach)
	}
	// Scalars only: no request or result body reaches the watcher's log.
	if data, err := os.ReadFile(fx.logPath()); err != nil || strings.Contains(string(data), "PEER-A") || strings.Contains(string(data), root) {
		t.Fatalf("the watcher log carries adapter data (err %v)", err)
	}
}

// TestSyncReattachesEveryRound: with the peers unchanged, an apply still
// runs every SyncInterval, and `attach` precedes every apply
// after the first (it is idempotent), so a reference a replaced watcher's
// late detach dropped comes back within one interval.
func TestSyncReattachesEveryRound(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	fx.syncMap("syncthing", t.TempDir(), "docs")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	deps.SyncInterval = 50 * time.Millisecond
	deps.SyncListInterval = 20 * time.Millisecond
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

// applies counts the apply calls recorded so far.
func applies(t *testing.T, fake *fakesync.Fake) int {
	t.Helper()
	n := 0
	for _, v := range fake.Verbs(t) {
		if v == "apply" {
			n++
		}
	}
	return n
}

// TestSyncAppliesWhenThePeersChange: the roster is read every
// SyncListInterval and an apply follows as soon as the teammates' peers
// changed — long before SyncInterval — carrying the new peer; one more
// apply follows at the next round (the peer just introduced has connected
// by then), and the notice line is refreshed from that apply's result,
// naming a folder another checkout holds (conflict_path) in its one fixed
// sentence. While the peers stay the same, no further apply runs before
// SyncInterval.
func TestSyncAppliesWhenThePeersChange(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	fx.syncMap("syncthing", t.TempDir(), "docs", "notes")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{ApplySequence: []string{
		`{"folders":[{"id":"a","state":"idle"},{"id":"b","state":"idle"}],"peers":[]}`,
		`{"folders":[{"id":"a","state":"idle"},{"id":"b","state":"conflict_path"}],"peers":[{"peer":"PEER-NEW","connected":false}]}`,
		`{"folders":[{"id":"a","state":"idle"},{"id":"b","state":"conflict_path"}],"peers":[{"peer":"PEER-NEW","connected":true}]}`,
	}})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	deps.SyncInterval = time.Hour
	deps.SyncListInterval = 50 * time.Millisecond
	r := fx.start(deps, fx.args()...)

	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.readNotice() == "Brigade sync: 2 folders, 0 of 0 peers connected"
	})
	fx.registerPeer(ptr(syncRepo), ptr("syncthing:PEER-NEW"), false)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.readNotice() == "Brigade sync: 2 folders, 1 of 1 peers connected; 1 folder is held by another checkout"
	})
	recs := fake.Records(t)
	if got := fake.Verbs(t); !slices.Equal(got, []string{"describe", "attach", "apply", "attach", "apply", "attach", "apply"}) {
		t.Fatalf("verbs = %v", got)
	}
	for _, i := range []int{4, 6} {
		var apply foldersync.ApplyRequest
		if err := json.Unmarshal(recs[i].Request, &apply); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(apply.Peers, []foldersync.Peer{{Peer: "PEER-NEW", Label: "peer@example.com"}}) {
			t.Fatalf("call %d: apply peers = %+v", i, apply.Peers)
		}
	}
	// Unchanged peers: several roster reads later, still three applies.
	time.Sleep(10 * deps.SyncListInterval)
	if n := applies(t, fake); n != 3 {
		t.Fatalf("%d applies with the peers unchanged and SyncInterval an hour away, want 3", n)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// syncRosterList is a `session list` result holding one teammate's
// session of the synced repository with the given sync_peer.
func syncRosterList(t *testing.T, peer string) []byte {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	rec := protocol.SessionRecord{
		SessionID: "s2", SessionName: "teammate", PrincipalRef: "p2", HumanLabel: "peer@example.com",
		State: protocol.SessionStateIdle, Activity: protocol.ActivityIdle, Inbound: protocol.InboundAccept,
		LastSeenAt: now, LeaseUntil: now.Add(time.Minute), CreatedAt: now,
		WorkspaceLabel: ptr(syncRepo), SyncPeer: ptr(peer),
	}
	out, err := json.Marshal(adapterclient.ListResult{TeamRef: "team-1", TeamName: "ops", ServerTime: now, Sessions: []protocol.SessionRecord{rec}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSyncReadsTheRosterOnItsOwnTicker: the roster — a backend read — is
// read every SyncListInterval (an hour here), and apply — a local call —
// runs every SyncInterval with the peers of the last read, never reading
// the roster again for it (card 53). TestSyncFailedFirstReadWaitsForTheListTick
// and TestSyncKeepsTheLastPeersWhenARereadFails have the failed reads.
func TestSyncReadsTheRosterOnItsOwnTicker(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	fx.useFake(fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{"session list": {{Result: syncRosterList(t, "syncthing:PEER-A")}}},
		Watch:     &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}},
		DumpFile:  dump,
	})
	fx.syncMap("syncthing", t.TempDir(), "docs")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	deps.SyncInterval = 50 * time.Millisecond
	deps.SyncListInterval = time.Hour
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return applies(t, fake) >= 3 })
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := dumpCount(t, dump, "session", "list"); n != 1 {
		t.Fatalf("session list calls = %d, want the one read at the start", n)
	}
	for i, rec := range fake.Records(t) {
		if rec.Verb != "apply" {
			continue
		}
		var apply foldersync.ApplyRequest
		if err := json.Unmarshal(rec.Request, &apply); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(apply.Peers, []foldersync.Peer{{Peer: "PEER-A", Label: "peer@example.com"}}) {
			t.Fatalf("call %d: apply peers = %+v, want the peer of the one read", i, apply.Peers)
		}
	}
}

// TestSyncFailedFirstReadWaitsForTheListTick: a session that starts while
// the backend answers nothing (an outage, a plan's limit: the 402 case)
// reads the roster once and then waits for the next SyncListInterval (an
// hour here) — not every SyncInterval, whose apply ticks run on — and
// applies nothing, having no peers to apply (card 53).
func TestSyncFailedFirstReadWaitsForTheListTick(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	fx.useFake(fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{"session list": {
			{Error: &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "backend down", Retryable: true}},
		}},
		Watch:    &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}},
		DumpFile: dump,
	})
	fx.syncMap("syncthing", t.TempDir(), "docs")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	deps.SyncInterval = 20 * time.Millisecond
	deps.SyncListInterval = time.Hour
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logHas("sync: the roster could not be read; the last peers stand", nil)
	})
	time.Sleep(15 * deps.SyncInterval) // fifteen apply ticks
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := dumpCount(t, dump, "session", "list"); n != 1 {
		t.Fatalf("session list calls = %d over fifteen apply ticks, want the one failed read: it waits for the list tick", n)
	}
	if n := applies(t, fake); n != 0 {
		t.Fatalf("%d applies with no roster read yet, want none", n)
	}
}

// TestSyncKeepsTheLastPeersWhenARereadFails: once a read has succeeded, a
// failed one leaves its peers standing — applies go on with them every
// SyncInterval — and the roster is not read again before the next
// SyncListInterval, so a backend that answers nothing (an outage, a
// plan's limit) is asked no more often than when it answers (card 53).
func TestSyncKeepsTheLastPeersWhenARereadFails(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	fx.useFake(fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{"session list": {
			{Result: syncRosterList(t, "syncthing:PEER-A")},
			{Error: &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "backend down", Retryable: true}},
		}},
		Watch:    &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}},
		DumpFile: dump,
	})
	fx.syncMap("syncthing", t.TempDir(), "docs")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	deps.SyncInterval = 20 * time.Millisecond
	deps.SyncListInterval = 200 * time.Millisecond
	r := fx.start(deps, fx.args()...)
	// The first read succeeds, the second (at the first list tick) fails;
	// applies go on meanwhile with PEER-A.
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logHas("sync: the roster could not be read; the last peers stand", nil)
	})
	failedAt := time.Now()
	reads := dumpCount(t, dump, "session", "list")
	n := applies(t, fake)
	testutil.Eventually(t, waitShort, pollEvery, func() bool { return applies(t, fake) >= n+3 })
	// Several apply ticks later, no read beyond one per list tick.
	ticks := int(time.Since(failedAt)/deps.SyncListInterval) + 1
	if got := dumpCount(t, dump, "session", "list"); got > reads+ticks {
		t.Fatalf("session list calls = %d after the failure, want at most %d (one per list tick, not per apply tick)", got, reads+ticks)
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for i, rec := range fake.Records(t) {
		if rec.Verb != "apply" {
			continue
		}
		var apply foldersync.ApplyRequest
		if err := json.Unmarshal(rec.Request, &apply); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(apply.Peers, []foldersync.Peer{{Peer: "PEER-A", Label: "peer@example.com"}}) {
			t.Fatalf("call %d: apply peers = %+v, want the last read's PEER-A", i, apply.Peers)
		}
	}
}

// TestSyncAdapterUnavailableIsOneNotice: an adapter that cannot be
// spawned, or answers describe or attach with an error, earns one notice
// line; the goroutine ends there — after a failed describe with no attach
// and no detach, after a failed attach with its detach — and the session
// goes on.
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
	t.Run("the bundled adapter's binary cannot be run", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useFS()
		fx.writeMapWith(func(m *sessionmap.ByPID) {
			m.WorkspaceLabel = syncRepo
			m.SyncAdapter = "syncthing"
			m.SyncFolders = []string{"docs"}
			m.SyncRoot = t.TempDir()
			m.PluginBin = filepath.Join(t.TempDir(), "gone", "brigade")
		})
		r := fx.start(fx.deps(), fx.args()...)
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return fx.readNotice() == "Brigade sync: the plugin's brigade binary could not be run; file sync is off for this session"
		})
		if code := r.stopAndWait(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("attach answers an error", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useFS()
		fx.syncMap("syncthing", t.TempDir(), "docs")
		fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{ErrorOn: []string{"attach"}, ErrorMessage: "syncthing exited during start"})
		deps := fx.deps()
		deps.SyncCommand = fake.Argv
		r := fx.start(deps, fx.args()...)
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return fx.readNotice() == "Brigade sync: the syncthing sync adapter is not usable (unavailable: syncthing exited during start); file sync is off for this session"
		})
		// The detach is registered before the attach: a failed attach
		// that counted the session in leaves no reference behind.
		testutil.Eventually(t, waitShort, pollEvery, func() bool {
			return slices.Equal(fake.Verbs(t), []string{"describe", "attach", "detach"})
		})
		if code := r.stopAndWait(); code != 0 {
			t.Fatalf("exit %d", code)
		}
	})
}

// TestSyncNoticeCountsAPausedFolderApart: a folder the adapter paused
// because the project no longer lists it is not one of the session's
// folders; the notice names it in its own clause.
func TestSyncNoticeCountsAPausedFolderApart(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFS()
	fx.syncMap("syncthing", t.TempDir(), "docs")
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{
		Apply: `{"folders":[{"id":"a","state":"idle"},{"id":"gone","state":"paused"},{"id":"old","state":"paused"}],"peers":[]}`,
	})
	deps := fx.deps()
	deps.SyncCommand = fake.Argv
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.readNotice() == "Brigade sync: 1 folders, 0 of 0 peers connected; 2 folders no longer listed are paused"
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
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
