package commands

import (
	"encoding/json/v2"
	"slices"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakesync"
)

// syncFixture is the fixture with file sync frozen into its map, as the
// SessionStart hook does; it returns the sync root.
func syncFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	root := t.TempDir()
	m := f.byPID()
	m.WorkspaceLabel = "brigade"
	m.SyncAdapter = "syncthing"
	m.SyncFolders = []string{"docs", ".context/plans"}
	m.SyncRoot = root
	f.writeMap(t, m)
	return f, root
}

// syncListResult is a roster in which bob's session publishes the peer
// PEER-A and nobody publishes XYZ1234567.
func syncListResult(t *testing.T) string {
	t.Helper()
	rec := func(id, label, peer string, self bool) map[string]any {
		r := map[string]any{
			"session_id": id, "session_name": "s", "human_label": label, "principal_ref": id[:8] + "-principal",
			"state": "idle", "activity": "idle", "inbound": "accept",
			"last_seen_at": fixtureNow, "lease_until": fixtureNow.Add(time.Minute), "created_at": fixtureNow, "is_self": self,
		}
		if peer != "" {
			r["sync_peer"] = peer
		}
		return r
	}
	b, err := json.Marshal(map[string]any{
		"team_ref": fixtureTeamRef, "team_name": fixtureTeamName, "server_time": fixtureNow, "truncated": false,
		"sessions": []any{
			rec("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "bob@example.com", "syncthing:PEER-A", false),
			rec("cccccccccccccccccccccccccccccccc", "carol@example.com", "other:XYZ1234567", false),
			rec(selfSessionID, "alice@example.com", "syncthing:"+fakesync.SelfPeer, true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// syncStatus is the engine's report: one of this project's folders, one
// of another project's on the same engine, a known peer and an unknown one.
func syncStatus(t *testing.T, root string) string {
	t.Helper()
	b, err := json.Marshal(foldersync.StatusResult{
		Running: true, Peer: fakesync.SelfPeer,
		Folders: []foldersync.FolderState{
			{ID: foldersync.FolderID(fixtureTeamRef, "docs"), Path: root + "/docs", State: "idle"},
			{ID: "brigade-other000-000000000000", Path: "/elsewhere", State: "syncing"},
		},
		Peers: []foldersync.PeerState{{Peer: "PEER-A", Connected: true}, {Peer: "XYZ1234567", Connected: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSyncStatusHuman pins the human layout: the engine line, this
// project's folders by label with their state (one the engine does not
// report yet says so; another project's folder is not listed), and every
// peer by the roster's label — or a short descriptor when the roster
// names none — with whether it is connected.
func TestSyncStatusHuman(t *testing.T) {
	t.Parallel()
	f, root := syncFixture(t)
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{Status: syncStatus(t, root)})
	f.rec.on("session list", okAnswer(syncListResult(t)))
	inv := f.inv(f.sessionEnv(), "", "status")
	inv.Deps.SyncCommand = fake.Argv
	if err := Sync(inv); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := "sync: syncthing, engine running, this machine's peer SELF-DEVICE-1\n" +
		"FOLDER                  STATE\n" +
		"brigade/docs            idle\n" +
		"brigade/.context/plans  not shared yet\n" +
		"PEER             CONNECTED\n" +
		"bob@example.com  yes\n" +
		"[XYZ1234]        no\n"
	if got := f.out.String(); got != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", got, want)
	}
	recs := fake.Records(t)
	if len(recs) != 1 || recs[0].Verb != "status" || string(recs[0].Request) != `{"state_dir":"`+f.stateDir+`"}` {
		t.Fatalf("sync adapter calls = %+v, want one status for the session's state directory", recs)
	}
}

// TestSyncStatusJSON: the adapter's status plus the adapter name, the
// folders as configured, and each peer's roster label.
func TestSyncStatusJSON(t *testing.T) {
	t.Parallel()
	f, root := syncFixture(t)
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{Status: syncStatus(t, root)})
	f.rec.on("session list", okAnswer(syncListResult(t)))
	inv := f.inv(f.sessionEnv(), "", "status")
	inv.JSON = true
	inv.Deps.SyncCommand = fake.Argv
	if err := Sync(inv); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var env struct {
		OK     bool             `json:"ok"`
		Result syncStatusResult `json:"result"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &env); err != nil {
		t.Fatalf("stdout %q: %v", f.out.String(), err)
	}
	r := env.Result
	if !env.OK || r.Sync != config.SyncOn || r.Adapter != "syncthing" || !r.Running || r.Peer != fakesync.SelfPeer || r.Note != SyncNote {
		t.Fatalf("result = %+v", r)
	}
	if len(r.Folders) != 2 || len(r.Peers) != 2 || r.Peers[0].Label != "bob@example.com" || r.Peers[1].Label != "" {
		t.Fatalf("folders/peers = %+v / %+v", r.Folders, r.Peers)
	}
	wantConfigured := foldersync.Folders(fixtureTeamRef, root, "brigade", []string{"docs", ".context/plans"})
	if !slices.Equal(r.Configured, wantConfigured) {
		t.Fatalf("configured = %+v, want %+v", r.Configured, wantConfigured)
	}
}

// TestSyncStatusWithoutARoster: a backend that cannot list still leaves
// the sync adapter's report standing, every peer by its short descriptor.
func TestSyncStatusWithoutARoster(t *testing.T) {
	t.Parallel()
	f, root := syncFixture(t)
	fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{Status: syncStatus(t, root)})
	f.rec.on("session list", failAnswer(protocol.CodeUnavailable, "backend down"))
	inv := f.inv(f.sessionEnv(), "", "status")
	inv.Deps.SyncCommand = fake.Argv
	if err := Sync(inv); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := f.out.String(); !containsLine(got, "[PEER-A]   yes") || !containsLine(got, "[XYZ1234]  no") {
		t.Fatalf("stdout = %q", got)
	}
}

// TestSyncStatusOffAndRefusals: a session that syncs nothing says so and
// spawns nothing; an adapter failure is the command's failure; outside a
// session it refuses as doing does; any other verb is usage.
func TestSyncStatusOffAndRefusals(t *testing.T) {
	t.Parallel()
	t.Run("off", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if err := Sync(f.inv(f.sessionEnv(), "", "status")); err != nil {
			t.Fatal(err)
		}
		if f.out.String() != SyncOffLine+"\n" || f.rec.count() != 0 {
			t.Fatalf("stdout %q, %d spawns", f.out.String(), f.rec.count())
		}
		f.out.Reset()
		inv := f.inv(f.sessionEnv(), "", "status")
		inv.JSON = true
		if err := Sync(inv); err != nil {
			t.Fatal(err)
		}
		ok, res := envelopeOf(t, f.out.String())
		if !ok || res["sync"] != config.SyncOff || res["running"] != false {
			t.Fatalf("--json = %q", f.out.String())
		}
	})
	t.Run("the adapter fails", func(t *testing.T) {
		t.Parallel()
		f, _ := syncFixture(t)
		fake := fakesync.Write(t, t.TempDir(), fakesync.Answers{ErrorOn: []string{"status"}, ErrorMessage: "syncthing is not on PATH"})
		inv := f.inv(f.sessionEnv(), "", "status")
		inv.Deps.SyncCommand = fake.Argv
		perr := wantCodeErr(t, Sync(inv), protocol.CodeUnavailable, "")
		if perr.Message != "syncthing is not on PATH" || f.out.String() != "" {
			t.Fatalf("error %+v, stdout %q", perr, f.out.String())
		}
	})
	t.Run("outside a session", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		wantCode(t, Sync(f.inv(f.terminalEnv(), "", "status")), protocol.CodeConfig, config.ReasonNotInSession)
		if f.rec.count() != 0 {
			t.Fatal("a refused sync status spawned a child")
		}
	})
	t.Run("usage", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		for _, args := range [][]string{nil, {"start"}, {"status", "extra"}} {
			wantCode(t, Sync(f.inv(f.sessionEnv(), "", args...)), protocol.CodeUsage, "")
		}
	})
	t.Run("an unusable map", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		m := f.byPID()
		m.SyncAdapter = "syncthing"
		// Written past Validate, as a planted file would be.
		writeRawMap(t, f, m)
		wantCode(t, Sync(f.inv(f.sessionEnv(), "", "status")), protocol.CodeConfig, sessionmap.ReasonMapInvalid)
	})
}

// containsLine reports whether out has exactly line as one of its lines.
func containsLine(out, line string) bool {
	return slices.Contains(splitLines(out), line)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// writeRawMap writes m as the by-pid file WITHOUT Validate — what a
// hand-edited or planted file is — for the reader's refusal to be seen.
func writeRawMap(t *testing.T, f *fixture, m *sessionmap.ByPID) {
	t.Helper()
	store := sessionmap.Store{StateDir: f.stateDir}
	p, err := store.ByPIDPath(m.ClaudePID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(p, data); err != nil {
		t.Fatal(err)
	}
}
