package syncthing

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil"
)

// realSyncthingVar opts in to the one test that runs the real `syncthing`
// on PATH. `make test` stays hermetic without it: no daemon, no network,
// no port 22000.
const realSyncthingVar = "BRIGADE_TEST_SYNCTHING"

// realTestPeer is a syntactically valid Syncthing device id (the example
// id of Syncthing's own documentation) that no instance of this test runs.
const realTestPeer = "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"

// realServerPeer is a second valid device id (another example of
// Syncthing's documentation) standing for an always-on server a person
// adds by hand.
const realServerPeer = "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2"

// TestRealSyncthingIntegration drives the installed binary through the
// whole lifecycle (plan 4.4): attach starts a dedicated instance on a
// fresh home and answers its device id, apply shares a folder that
// Syncthing then marks with .stfolder, status sees it, and the last detach
// stops the daemon.
func TestRealSyncthingIntegration(t *testing.T) {
	if os.Getenv(realSyncthingVar) != "1" {
		t.Skip("set " + realSyncthingVar + "=1 to run against the syncthing on PATH")
	}
	stateDir := t.TempDir()
	home := filepath.Join(stateDir, "sync", "syncthing")
	killDaemon(t, home)
	d := realDeps()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	call := func(verb string, req any) map[string]any {
		t.Helper()
		return mustOK(t, invokeEnv(t, d, env, verb, req), verb)
	}

	res := call("attach", map[string]any{"state_dir": stateDir, "session_id": "real-1"})
	id, _ := res["peer"].(string)
	if len(id) < 50 {
		t.Fatalf("attach peer = %q, want a Syncthing device id", id)
	}
	pid := daemonPID(t, home)

	folder := filepath.Join(t.TempDir(), "shared")
	res = call("apply", map[string]any{
		"state_dir":  stateDir,
		"session_id": "real-1",
		"folders":    []map[string]string{{"id": "brigade-test0000-000000000000", "path": folder, "label": "test/shared"}},
		// A well-formed device id nobody runs: Syncthing accepts it, shares
		// the folder with it and reports it not connected.
		"peers": []map[string]string{{"peer": realTestPeer, "label": "nobody"}},
	})
	if fs := res["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["state"] == "rejected" {
		t.Errorf("apply folders = %v", fs)
	}
	if ps := res["peers"].([]any); len(ps) != 1 || ps[0].(map[string]any)["connected"] != false {
		t.Errorf("apply peers = %v", ps)
	}
	testutil.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool {
		_, err := os.Stat(filepath.Join(folder, ".stfolder"))
		return err == nil
	})

	// The instance listens on its own port, not 22000.
	c, err := newInstance(stateDir, &adapter{d: d, http: &http.Client{Timeout: restTimeout}, log: slog.New(slog.DiscardHandler)}).api()
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(readFile(t, filepath.Join(home, "listen-port"))))
	if err != nil || port == 22000 {
		t.Fatalf("listen-port = %d (%v), want a port of the instance's own", port, err)
	}
	var opts listenOptions
	if err := c.do(http.MethodGet, "/rest/config/options", nil, nil, &opts); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(opts.ListenAddresses, listenAddresses(port)) {
		t.Errorf("listenAddresses = %v, want %v", opts.ListenAddresses, listenAddresses(port))
	}
	t.Logf("the instance listens on %v", opts.ListenAddresses)

	// A server a person adds by hand — to the instance and to the folder —
	// survives the next apply, with its own addresses.
	const folderID = "brigade-test0000-000000000000"
	server := deviceConfig{DeviceID: realServerPeer, Name: "server by hand", Addresses: []string{"tcp://192.0.2.1:22000"}}
	if err := c.do(http.MethodPost, "/rest/config/devices", nil, server, nil); err != nil {
		t.Fatalf("add the server device by hand: %v", err)
	}
	var fc map[string]any
	if err := c.do(http.MethodGet, "/rest/config/folders/"+folderID, nil, nil, &fc); err != nil {
		t.Fatal(err)
	}
	fc["devices"] = append(fc["devices"].([]any), map[string]any{"deviceID": realServerPeer})
	if err := c.do(http.MethodPut, "/rest/config/folders/"+folderID, nil, fc, nil); err != nil {
		t.Fatalf("share the folder with the server by hand: %v", err)
	}
	applyReq := map[string]any{
		"state_dir":  stateDir,
		"session_id": "real-1",
		"folders":    []map[string]string{{"id": folderID, "path": folder, "label": "test/shared"}},
		"peers":      []map[string]string{{"peer": realTestPeer, "label": "nobody"}},
	}
	call("apply", applyReq)
	var after configuredFolder
	if err := c.do(http.MethodGet, "/rest/config/folders/"+folderID, nil, nil, &after); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, dr := range after.Devices {
		ids[dr.DeviceID] = true
	}
	if !ids[realServerPeer] || !ids[realTestPeer] || !ids[id] {
		t.Errorf("folder devices after apply = %+v, want the server, the peer and this device", after.Devices)
	}
	var dev deviceConfig
	if err := c.do(http.MethodGet, "/rest/config/devices/"+realServerPeer, nil, nil, &dev); err != nil {
		t.Fatal(err)
	}
	if dev.Name != server.Name || !slices.Equal(dev.Addresses, server.Addresses) {
		t.Errorf("the hand-added device after apply = %+v, want it unchanged", dev)
	}

	// A second checkout of the same folder id: conflict_path, and the folder
	// stays where the first checkout put it.
	other := filepath.Join(t.TempDir(), "clone-2", "shared")
	applyReq["folders"] = []map[string]string{{"id": folderID, "path": other, "label": "test/shared"}}
	res = call("apply", applyReq)
	if fs := res["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["state"] != "conflict_path" {
		t.Errorf("apply from a second checkout: folders = %v, want conflict_path", fs)
	}
	if err := c.do(http.MethodGet, "/rest/config/folders/"+folderID, nil, nil, &after); err != nil || after.Path != folder {
		t.Errorf("the folder moved to %q (%v), want it held at %q", after.Path, err, folder)
	}

	res = call("status", map[string]any{"state_dir": stateDir})
	if res["running"] != true || res["peer"] != id {
		t.Errorf("status = %v", res)
	}
	if fs := res["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["path"] != folder {
		t.Errorf("status folders = %v", fs)
	}
	listed := map[any]bool{}
	for _, p := range res["peers"].([]any) {
		listed[p.(map[string]any)["peer"]] = true
	}
	if len(listed) != 2 || !listed[realTestPeer] || !listed[realServerPeer] {
		t.Errorf("status peers = %v, want the peer and the hand-added server", res["peers"])
	}

	// A folder the project drops is paused — its directory stays — while
	// the folder above, whose label proves no name for its id, is left
	// alone; listing it again un-pauses it.
	repo := filepath.Join(t.TempDir(), "repo")
	docs := map[string]string{"id": testFolderID("test0000", "repo", "docs"), "path": filepath.Join(repo, "docs"), "label": "repo/docs"}
	notes := map[string]string{"id": testFolderID("test0000", "repo", "../notes"), "path": filepath.Join(filepath.Dir(repo), "notes"), "label": "repo/../notes"}
	paused := func(id string) bool {
		t.Helper()
		var fc configuredFolder
		if err := c.do(http.MethodGet, "/rest/config/folders/"+id, nil, nil, &fc); err != nil {
			t.Fatal(err)
		}
		return fc.Paused
	}
	applyReq["folders"] = []map[string]string{docs, notes}
	call("apply", applyReq)
	applyReq["folders"] = []map[string]string{docs}
	res = call("apply", applyReq)
	states := map[string]any{}
	for _, f := range res["folders"].([]any) {
		states[f.(map[string]any)["id"].(string)] = f.(map[string]any)["state"]
	}
	if states[notes["id"]] != "paused" || states[docs["id"]] == "rejected" || len(states) != 2 {
		t.Errorf("apply with a folder dropped: folders = %v, want the dropped one paused", states)
	}
	if !paused(notes["id"]) || paused(docs["id"]) || paused(folderID) {
		t.Errorf("paused: notes %v, docs %v, the unproven folder %v; want notes alone", paused(notes["id"]), paused(docs["id"]), paused(folderID))
	}
	if _, err := os.Stat(notes["path"]); err != nil {
		t.Errorf("the paused folder's directory: %v", err)
	}
	applyReq["folders"] = []map[string]string{docs, notes}
	call("apply", applyReq)
	if paused(notes["id"]) {
		t.Error("the folder listed again is still paused")
	}

	// The upgrade of card 42: a folder the instance holds under the id it
	// had before 0.16.0 is moved to its new id at the same path, with the
	// server a person added to it; its file stays, and Syncthing puts its
	// marker back. The earlier id of another folder, held at a second
	// clone's path, stays where it is.
	web := filepath.Join(t.TempDir(), "web")
	clone := filepath.Join(t.TempDir(), "web-second-clone")
	legacy := map[string]string{"id": testLegacyID("test0000", ".brigade"), "path": filepath.Join(web, ".brigade"), "label": "web/.brigade"}
	legacyDocs := map[string]string{"id": testLegacyID("test0000", "docs"), "path": filepath.Join(clone, "docs"), "label": "web/docs"}
	applyReq["folders"] = []map[string]string{legacy}
	call("apply", applyReq)
	applyReq["folders"] = []map[string]string{legacyDocs}
	call("apply", applyReq)
	marker := filepath.Join(legacy["path"], ".stfolder")
	testutil.Eventually(t, 15*time.Second, 100*time.Millisecond, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	writeFile(t, filepath.Join(legacy["path"], "kept.md"), "a file the folder already holds\n")
	if err := c.do(http.MethodGet, "/rest/config/folders/"+legacy["id"], nil, nil, &fc); err != nil {
		t.Fatal(err)
	}
	fc["devices"] = append(fc["devices"].([]any), map[string]any{"deviceID": realServerPeer})
	fc["paused"] = false // the apply for the second clone's folder paused nothing here, and must not have
	if err := c.do(http.MethodPut, "/rest/config/folders/"+legacy["id"], nil, fc, nil); err != nil {
		t.Fatalf("share the earlier folder with the server by hand: %v", err)
	}
	scoped := map[string]string{"id": testFolderID("test0000", "web", ".brigade"), "replaces": legacy["id"], "path": legacy["path"], "label": "web/.brigade"}
	scopedDocs := map[string]string{"id": testFolderID("test0000", "web", "docs"), "replaces": legacyDocs["id"], "path": filepath.Join(web, "docs"), "label": "web/docs"}
	applyReq["folders"] = []map[string]string{scoped, scopedDocs}
	for round := range 2 {
		res = call("apply", applyReq)
		if fs := res["folders"].([]any); len(fs) != 2 || fs[0].(map[string]any)["state"] == "conflict_path" || fs[1].(map[string]any)["state"] == "conflict_path" {
			t.Fatalf("round %d: apply with the new ids: folders = %v", round, fs)
		}
	}
	var held []configuredFolder
	if err := c.do(http.MethodGet, "/rest/config/folders", nil, nil, &held); err != nil {
		t.Fatal(err)
	}
	at := map[string]configuredFolder{}
	for _, h := range held {
		at[h.ID] = h
	}
	if _, still := at[legacy["id"]]; still {
		t.Error("the earlier id is still configured at the folder's path")
	}
	moved := at[scoped["id"]]
	ids = map[string]bool{}
	for _, dr := range moved.Devices {
		ids[dr.DeviceID] = true
	}
	if !samePath(moved.Path, legacy["path"]) || moved.Paused || !ids[realServerPeer] || !ids[realTestPeer] {
		t.Errorf("the moved folder = %+v, want it at %s with the server and the peer", moved, legacy["path"])
	}
	if kept := at[legacyDocs["id"]]; !samePath(kept.Path, legacyDocs["path"]) {
		t.Errorf("the earlier id at the second clone's path = %+v, want it where it was", kept)
	}
	if got := readFile(t, filepath.Join(legacy["path"], "kept.md")); got != "a file the folder already holds\n" {
		t.Errorf("the folder's file after the move = %q", got)
	}
	testutil.Eventually(t, 30*time.Second, 100*time.Millisecond, func() bool {
		_, err := os.Stat(marker)
		return err == nil && c.folderState(scoped["id"]) == "idle"
	})
	t.Log("the moved folder is idle under its new id, with its marker and its file")

	res = call("detach", map[string]any{"state_dir": stateDir, "session_id": "real-1"})
	if res["stopped"] != true {
		t.Errorf("detach = %v", res)
	}
	if alive(pid, "") {
		t.Error("syncthing survived the last detach")
	}
}

// realWait is a hang catcher for two instances finding each other and
// exchanging a file, never a performance bound: discovery, then
// Syncthing's 10 s filesystem-watcher delay before a transfer.
const realWait = 240 * time.Second

// A realSide is one machine of TestRealSyncthingMovesASharedFolder: a
// state directory with its own instance, and a checkout's folder.
type realSide struct {
	name     string
	stateDir string
	folder   string
	device   string
	listen   string // the instance's own sync port (listen-port)
	api      *api
}

// knows adds other to s's instance by hand, at other's loopback address,
// before any apply — which keeps a device the instance already holds as
// it is. The two instances then reach each other without discovery:
// another Syncthing on the machine (a developer's own Brigade instance)
// holds the local discovery port, and the test must neither depend on it
// nor on the public discovery servers.
func (s *realSide) knows(t *testing.T, other *realSide) {
	t.Helper()
	dev := deviceConfig{DeviceID: other.device, Name: other.name, Addresses: []string{"tcp://127.0.0.1:" + other.listen}}
	if err := s.api.do(http.MethodPost, "/rest/config/devices", nil, dev, nil); err != nil {
		t.Fatalf("%s: add %s by hand: %v", s.name, other.name, err)
	}
}

// TestRealSyncthingMovesASharedFolder measures the upgrade of card 42
// between two machines, with the syncthing on PATH: two instances share
// one folder under the id of 0.11.0 to 0.15.0; one is upgraded and then
// the other, as a team's members update one after another. It records
// what docs/sync.md says of the time in between and after:
//
//   - while one side is upgraded and the other is not, the two hold the
//     folder under different ids, so they exchange nothing;
//   - once both are, the folder is in step again. A file both sides
//     already had alike is left alone — no conflict copy. A file written
//     on either side in between reaches the other. A file changed on one
//     side in between wins over the older copy, which is kept beside it
//     as a conflict copy. A file deleted on one side in between comes
//     back from the other.
func TestRealSyncthingMovesASharedFolder(t *testing.T) {
	if os.Getenv(realSyncthingVar) != "1" {
		t.Skip("set " + realSyncthingVar + "=1 to run against the syncthing on PATH")
	}
	d := realDeps()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	call := func(verb string, req any) map[string]any {
		t.Helper()
		return mustOK(t, invokeEnv(t, d, env, verb, req), verb)
	}
	side := func(name string) *realSide {
		t.Helper()
		s := &realSide{name: name, stateDir: t.TempDir(), folder: filepath.Join(t.TempDir(), "web", ".brigade")}
		killDaemon(t, filepath.Join(s.stateDir, "sync", "syncthing"))
		s.device, _ = call("attach", map[string]any{"state_dir": s.stateDir, "session_id": "real-" + name})["peer"].(string)
		if len(s.device) < 50 {
			t.Fatalf("%s: attach peer = %q, want a Syncthing device id", name, s.device)
		}
		c, err := newInstance(s.stateDir, &adapter{d: d, http: &http.Client{Timeout: restTimeout}, log: slog.New(slog.DiscardHandler)}).api()
		if err != nil {
			t.Fatal(err)
		}
		s.api = c
		s.listen = strings.TrimSpace(readFile(t, filepath.Join(s.stateDir, "sync", "syncthing", "listen-port")))
		return s
	}
	a, b := side("a"), side("b")
	a.knows(t, b)
	b.knows(t, a)
	// What the engines said, when the test fails: the last lines of each
	// instance's own log (throwaway instances, under the test's tree).
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, s := range []*realSide{a, b} {
			raw, err := os.ReadFile(filepath.Join(s.stateDir, "sync", "syncthing", "syncthing.log")) //nolint:gosec // G304: the instance's own log under the test's tree
			if err != nil {
				t.Logf("%s: syncthing.log: %v", s.name, err)
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) > 60 {
				lines = lines[len(lines)-60:]
			}
			t.Logf("%s (device %s), the end of syncthing.log:\n%s", s.name, s.device, strings.Join(lines, "\n"))
		}
	})
	legacy, scoped := testLegacyID("test0000", ".brigade"), testFolderID("test0000", "web", ".brigade")
	apply := func(s, other *realSide, upgraded bool) {
		t.Helper()
		f := map[string]string{"id": legacy, "path": s.folder, "label": "web/.brigade"}
		if upgraded {
			f = map[string]string{"id": scoped, "replaces": legacy, "path": s.folder, "label": "web/.brigade"}
		}
		res := call("apply", map[string]any{
			"state_dir": s.stateDir, "session_id": "real-" + s.name,
			"folders": []map[string]string{f},
			"peers":   []map[string]string{{"peer": other.device, "label": other.name}},
		})
		if fs := res["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["id"] != f["id"] ||
			fs[0].(map[string]any)["state"] == "conflict_path" || fs[0].(map[string]any)["state"] == "rejected" {
			t.Fatalf("%s: apply folders = %v", s.name, fs)
		}
	}
	holds := func(s *realSide) []string {
		t.Helper()
		fs, err := s.api.folders()
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range fs {
			ids = append(ids, f.ID)
		}
		return ids
	}
	arrives := func(at *realSide, name, content string) time.Duration {
		t.Helper()
		start := time.Now()
		testutil.Eventually(t, realWait, 250*time.Millisecond, func() bool {
			got, err := os.ReadFile(filepath.Join(at.folder, name)) //nolint:gosec // G304: the test's own folder
			return err == nil && string(got) == content
		})
		return time.Since(start)
	}
	conflicts := func(s *realSide) []string {
		t.Helper()
		m, err := filepath.Glob(filepath.Join(s.folder, "*.sync-conflict-*"))
		if err != nil {
			t.Fatal(err)
		}
		for i := range m {
			m[i] = filepath.Base(m[i])
		}
		return m
	}

	// --- both on the earlier id: three files reach the other side -------------
	start := time.Now()
	apply(a, b, false)
	apply(b, a, false)
	for name, content := range map[string]string{"same.md": "alike on both sides\n", "edited.md": "before the edit\n", "deleted.md": "to be deleted on one side\n"} {
		writeFile(t, filepath.Join(a.folder, name), content)
	}
	for name, content := range map[string]string{"same.md": "alike on both sides\n", "edited.md": "before the edit\n", "deleted.md": "to be deleted on one side\n"} {
		arrives(b, name, content)
	}
	t.Logf("under the earlier id, a's three files were at b %s after the first apply", time.Since(start).Round(time.Millisecond))

	// --- a is upgraded, b is not: different ids, nothing to exchange ----------
	apply(a, b, true)
	apply(b, a, false)
	if got := holds(a); !slices.Equal(got, []string{scoped}) {
		t.Fatalf("a holds %v, want the new id alone", got)
	}
	if got := holds(b); !slices.Equal(got, []string{legacy}) {
		t.Fatalf("b holds %v, want the earlier id alone", got)
	}
	writeFile(t, filepath.Join(a.folder, "from-a.md"), "written on a in between\n")
	writeFile(t, filepath.Join(b.folder, "from-b.md"), "written on b in between\n")
	// b's edit is the later of the two copies of edited.md.
	writeFile(t, filepath.Join(b.folder, "edited.md"), "after b's edit, in between\n")
	if err := os.Remove(filepath.Join(b.folder, "deleted.md")); err != nil {
		t.Fatal(err)
	}

	// --- b is upgraded too: the folder is in step again -------------------------
	upgraded := time.Now()
	apply(b, a, true)
	apply(a, b, true)
	if got := holds(b); !slices.Equal(got, []string{scoped}) {
		t.Fatalf("b holds %v, want the new id alone", got)
	}
	t.Logf("from-a.md reached b %s after b's upgrade", arrives(b, "from-a.md", "written on a in between\n").Round(time.Millisecond))
	t.Logf("from-b.md reached a %s after b's upgrade", arrives(a, "from-b.md", "written on b in between\n").Round(time.Millisecond))
	arrives(a, "edited.md", "after b's edit, in between\n")
	arrives(b, "deleted.md", "to be deleted on one side\n")
	for _, s := range []*realSide{a, b} {
		if got := readFile(t, filepath.Join(s.folder, "same.md")); got != "alike on both sides\n" {
			t.Errorf("%s: same.md = %q", s.name, got)
		}
		if got := readFile(t, filepath.Join(s.folder, "edited.md")); got != "after b's edit, in between\n" {
			t.Errorf("%s: edited.md = %q, want b's edit", s.name, got)
		}
	}
	// The older copy of edited.md is kept as a conflict copy, which then
	// syncs like any file; no other file has one.
	testutil.Eventually(t, realWait, 250*time.Millisecond, func() bool {
		ca, cb := conflicts(a), conflicts(b)
		return len(ca) == 1 && slices.Equal(ca, cb)
	})
	copies := conflicts(a)
	if !strings.HasPrefix(copies[0], "edited.sync-conflict-") {
		t.Errorf("conflict copies = %v, want one, of edited.md", copies)
	}
	if got := readFile(t, filepath.Join(a.folder, copies[0])); got != "before the edit\n" {
		t.Errorf("the conflict copy holds %q, want the older copy", got)
	}
	t.Logf("in step again %s after b's upgrade; conflict copies: %v", time.Since(upgraded).Round(time.Millisecond), copies)

	for _, s := range []*realSide{a, b} {
		if res := call("detach", map[string]any{"state_dir": s.stateDir, "session_id": "real-" + s.name}); res["stopped"] != true {
			t.Errorf("%s: detach = %v", s.name, res)
		}
	}
}
