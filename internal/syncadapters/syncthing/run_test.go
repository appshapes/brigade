package syncthing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
)

// testKey is the API key the fixtures write into config.xml. Every test
// that runs a verb asserts it never reaches stdout or stderr.
const testKey = "fixture-api-key-7f3a"

// testID is the device id the fake REST API answers as myID.
const testID = "SELFSEL-FSELFSE-LFSELFS-ELFSELF-SELFSEL-FSELFSE-LFSELFS-ELFSELF"

// fakeAPI is an httptest stand-in for Syncthing's REST API: the endpoints
// the adapter calls, keyed by X-API-Key, keeping what apply posts.
type fakeAPI struct {
	t *testing.T

	mu          sync.Mutex
	devices     []deviceConfig
	devicePosts int
	folders     []folderConfig
	folderPosts int
	pauses      int                // PATCHes that paused a folder
	extra       []configuredFolder // folders "another project" holds
	connected   map[string]bool
	listen      []string // options.listenAddresses
	listenSets  int
	shutdowns   int
	failStop    bool
	onShutdown  func()
	badKey      int
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	t.Helper()
	f := &fakeAPI{t: t, connected: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-API-Key") != testKey {
		f.badKey++
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	reply := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			f.t.Errorf("fake API: marshal: %v", err)
		}
		_, _ = w.Write(raw)
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /rest/system/status":
		reply(map[string]string{"myID": testID})
	case "POST /rest/system/shutdown":
		f.shutdowns++
		if f.failStop {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		if f.onShutdown != nil {
			f.onShutdown()
		}
		reply(map[string]string{"ok": "shutting down"})
	case "GET /rest/system/connections":
		conns := map[string]map[string]bool{}
		for _, d := range f.devices {
			conns[d.DeviceID] = map[string]bool{"connected": f.connected[d.DeviceID]}
		}
		reply(map[string]any{"connections": conns})
	case "GET /rest/config/devices":
		out := []deviceConfig{{DeviceID: testID, Name: "self"}}
		reply(append(out, f.devices...))
	case "POST /rest/config/devices":
		var d deviceConfig
		if err := json.UnmarshalRead(r.Body, &d); err != nil || strings.HasPrefix(d.DeviceID, "BAD") {
			http.Error(w, "invalid device", http.StatusBadRequest)
			return
		}
		f.devicePosts++
		f.devices = slices.DeleteFunc(f.devices, func(e deviceConfig) bool { return e.DeviceID == d.DeviceID })
		f.devices = append(f.devices, d)
	case "GET /rest/config/folders":
		out := slices.Clone(f.extra)
		for _, c := range f.folders {
			out = append(out, configuredFolder{ID: c.ID, Label: c.Label, Path: c.Path, Paused: c.Paused, Devices: c.Devices})
		}
		reply(out)
	case "POST /rest/config/folders":
		var c folderConfig
		if err := json.UnmarshalRead(r.Body, &c); err != nil {
			http.Error(w, "invalid folder", http.StatusBadRequest)
			return
		}
		f.folderPosts++
		f.folders = slices.DeleteFunc(f.folders, func(e folderConfig) bool { return e.ID == c.ID })
		f.folders = append(f.folders, c)
	case "GET /rest/config/options":
		// Syncthing's whole options object; the adapter reads one member.
		reply(map[string]any{"listenAddresses": f.listen, "globalAnnounceEnabled": true, "relaysEnabled": true})
	case "PATCH /rest/config/options":
		var o map[string]any
		if err := json.UnmarshalRead(r.Body, &o); err != nil || len(o) != 1 {
			http.Error(w, "the patch must name listenAddresses alone", http.StatusBadRequest)
			return
		}
		var lo listenOptions
		raw, _ := json.Marshal(o)
		if err := json.Unmarshal(raw, &lo); err != nil || lo.ListenAddresses == nil {
			http.Error(w, "invalid options", http.StatusBadRequest)
			return
		}
		f.listen = lo.ListenAddresses
		f.listenSets++
	case "GET /rest/db/status":
		id := r.URL.Query().Get("folder")
		for _, c := range f.folders {
			if c.ID == id {
				reply(map[string]string{"state": "idle"})
				return
			}
		}
		http.Error(w, "no such folder", http.StatusNotFound)
	default:
		id, ok := strings.CutPrefix(r.URL.Path, "/rest/config/folders/")
		if r.Method != http.MethodPatch || !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var patch map[string]any
		if err := json.UnmarshalRead(r.Body, &patch); err != nil || len(patch) != 1 || patch["paused"] != true {
			http.Error(w, "the patch must set paused alone", http.StatusBadRequest)
			return
		}
		for i := range f.folders {
			if f.folders[i].ID == id {
				f.folders[i].Paused = true
				f.pauses++
				return
			}
		}
		http.Error(w, "no such folder", http.StatusNotFound)
	}
}

// testDeps points the adapter at srv and at the fake syncthing script (run
// as /bin/sh <script>, the repository's fixture rule), with short waits.
func testDeps(srv *httptest.Server, script string) deps {
	return deps{
		syncthing: func() ([]string, error) {
			if script == "" {
				return nil, exec.ErrNotFound
			}
			return []string{"/bin/sh", script}, nil
		},
		baseURL:   func(int) string { return srv.URL },
		startWait: 5 * time.Second,
		stopWait:  2 * time.Second,
	}
}

type outcome struct {
	exit   int
	stdout string
	stderr string
	env    struct {
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
}

// invoke runs one verb in-process with req on stdin.
func invoke(t *testing.T, d deps, verb string, req any) outcome {
	t.Helper()
	return invokeEnv(t, d, []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}, verb, req)
}

func invokeEnv(t *testing.T, d deps, environ []string, verb string, req any) outcome {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	var o outcome
	o.exit = run([]string{verb}, bytes.NewReader(raw), &out, &errb, environ, d)
	o.stdout, o.stderr = out.String(), errb.String()
	if strings.Count(o.stdout, "\n") != 1 {
		t.Fatalf("%s: stdout is not exactly one envelope line: %q", verb, o.stdout)
	}
	if err := json.Unmarshal([]byte(o.stdout), &o.env); err != nil {
		t.Fatalf("%s: stdout is not an envelope: %v (%q)", verb, err, o.stdout)
	}
	if strings.Contains(o.stdout+o.stderr, testKey) {
		t.Errorf("%s: the API key reached stdout or stderr", verb)
	}
	return o
}

func mustOK(t *testing.T, o outcome, verb string) map[string]any {
	t.Helper()
	if o.exit != 0 || !o.env.OK {
		t.Fatalf("%s: exit %d, stdout %q, stderr %q", verb, o.exit, o.stdout, o.stderr)
	}
	return o.env.Result
}

// runningHome prepares a home whose daemon is "running": config.xml with
// the test key, a kept port, and daemon.pid naming a live sleeper (with
// its real start token). It returns the state dir and the sleeper's pid.
func runningHome(t *testing.T) (string, int) {
	t.Helper()
	stateDir := t.TempDir()
	home := filepath.Join(stateDir, "sync", "syncthing")
	if err := os.MkdirAll(filepath.Join(home, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "config.xml"), `<configuration version="37"><gui enabled="true"><address>127.0.0.1:1</address><apikey>`+testKey+`</apikey></gui></configuration>`)
	writeFile(t, filepath.Join(home, "port"), "1\n")
	pid := testutil.NewSleeper(t)
	info, err := procutil.Lookup(pid)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "daemon.pid"), strconv.Itoa(pid)+" "+info.StartToken+"\n")
	return stateDir, pid
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	_, srv := newFakeAPI(t)
	res := mustOK(t, invoke(t, testDeps(srv, ""), "describe", map[string]any{}), "describe")
	if res["name"] != "syncthing" || res["protocol_version"] != "sync/1" || res["version"] == "" {
		t.Errorf("describe = %v", res)
	}
}

func TestUsageAndInputRefusals(t *testing.T) {
	t.Parallel()
	_, srv := newFakeAPI(t)
	d := testDeps(srv, "")
	for _, args := range [][]string{nil, {"bogus"}, {"attach", "extra"}} {
		var out, errb bytes.Buffer
		if exit := run(args, strings.NewReader("{}"), &out, &errb, nil, d); exit != 2 || !strings.Contains(out.String(), `"code":"usage"`) {
			t.Errorf("%v: exit %d, stdout %q", args, exit, out.String())
		}
	}
	for _, id := range []string{"", "..", "a/b"} {
		o := invoke(t, d, "attach", map[string]any{"state_dir": t.TempDir(), "session_id": id})
		if o.exit != 3 || o.env.Error.Code != "invalid_input" {
			t.Errorf("session_id %q: exit %d, stdout %q", id, o.exit, o.stdout)
		}
	}
	o := invoke(t, d, "status", map[string]any{"state_dir": "relative/dir"})
	if o.exit != 3 {
		t.Errorf("relative state_dir: exit %d, stdout %q", o.exit, o.stdout)
	}
}

func TestStatusWithNoDaemon(t *testing.T) {
	t.Parallel()
	_, srv := newFakeAPI(t)
	res := mustOK(t, invoke(t, testDeps(srv, ""), "status", map[string]any{"state_dir": t.TempDir()}), "status")
	if res["running"] != false || res["peer"] != "" || len(res["folders"].([]any)) != 0 || len(res["peers"].([]any)) != 0 {
		t.Errorf("status = %v, want not running with empty lists", res)
	}
}

func TestApplyIntroducesPeersAndSharesFolders(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	d := testDeps(srv, "")
	stateDir, _ := runningHome(t)
	project := t.TempDir()
	taken := filepath.Join(project, "taken")
	fake.extra = []configuredFolder{{ID: "someone-else", Path: taken}}
	fake.connected["PEERONE"] = true
	req := map[string]any{
		"state_dir":  stateDir,
		"session_id": "s1",
		"folders": []map[string]string{
			{"id": "brigade-aaaa-1111", "path": filepath.Join(project, "docs", "shared"), "label": "repo/docs/shared"},
			{"id": "brigade-aaaa-2222", "path": taken, "label": "repo/taken"},
		},
		"peers": []map[string]string{
			{"peer": "PEERONE", "label": "alice"},
			{"peer": "BADPEER", "label": "broken"},
			{"peer": "PEERTWO", "label": "bob"},
		},
	}
	for range 2 { // apply is idempotent: the second run changes nothing
		res := mustOK(t, invoke(t, d, "apply", req), "apply")
		folders := res["folders"].([]any)
		if len(folders) != 2 ||
			folders[0].(map[string]any)["state"] != "idle" ||
			folders[1].(map[string]any)["state"] != "conflict_path" {
			t.Errorf("apply folders = %v", folders)
		}
		peers := res["peers"].([]any)
		if len(peers) != 3 || peers[0].(map[string]any)["connected"] != true || peers[2].(map[string]any)["connected"] != false {
			t.Errorf("apply peers = %v", peers)
		}
	}
	if fi, err := os.Stat(filepath.Join(project, "docs", "shared")); err != nil || !fi.IsDir() {
		t.Errorf("the folder was not created: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.devices) != 2 || fake.devices[0].Name != "alice" || fake.devices[0].Addresses[0] != "dynamic" {
		t.Errorf("devices = %+v", fake.devices)
	}
	if len(fake.folders) != 1 {
		t.Fatalf("folders = %+v, want only the unconflicted one", fake.folders)
	}
	f := fake.folders[0]
	want := []deviceRef{{DeviceID: "PEERONE"}, {DeviceID: "PEERTWO"}}
	if f.ID != "brigade-aaaa-1111" || f.Label != "repo/docs/shared" || f.Type != "sendreceive" ||
		!slices.Equal(f.Devices, want) || f.Versioning.Type != "trashcan" || f.Versioning.Params["cleanoutDays"] != "14" ||
		!f.FSWatcherEnabled || f.RescanIntervalS != 60 {
		t.Errorf("folder object = %+v", f)
	}
	if fake.badKey != 0 {
		t.Errorf("%d calls carried the wrong API key", fake.badKey)
	}
}

// TestApplyKeepsWhatItDidNotAdd: a device a person added to the instance
// by hand — an always-on server sharing the same folder id — stays in the
// instance exactly as it was added, and stays in the folder's device list
// (with Syncthing's own members of its entry), round after round; a
// roster peer the instance already knows is not re-posted.
func TestApplyKeepsWhatItDidNotAdd(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	d := testDeps(srv, "")
	stateDir, _ := runningHome(t)
	path := filepath.Join(t.TempDir(), "shared")
	server := deviceConfig{DeviceID: "SERVER", Name: "the server", Addresses: []string{"tcp://server.example:22000"}}
	fake.devices = []deviceConfig{server, {DeviceID: "PEERTWO", Name: "bob by hand", Addresses: []string{"tcp://bob.example:22000"}}}
	fake.folders = []folderConfig{{
		ID: "brigade-aaaa-1111", Path: path,
		Devices: []deviceRef{{DeviceID: testID}, {DeviceID: "SERVER", EncryptionPassword: "server-folder-password"}},
	}}
	req := map[string]any{
		"state_dir": stateDir,
		"folders":   []map[string]string{{"id": "brigade-aaaa-1111", "path": path, "label": "repo/shared"}},
		"peers":     []map[string]string{{"peer": "PEERONE", "label": "alice"}, {"peer": "PEERTWO", "label": "bob"}},
	}
	for round := range 2 {
		res := mustOK(t, invoke(t, d, "apply", req), "apply")
		if fs := res["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["state"] != "idle" {
			t.Fatalf("round %d: apply folders = %v", round, fs)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	want := []deviceRef{{DeviceID: testID}, {DeviceID: "SERVER", EncryptionPassword: "server-folder-password"}, {DeviceID: "PEERONE"}, {DeviceID: "PEERTWO"}}
	if len(fake.folders) != 1 || !slices.Equal(fake.folders[0].Devices, want) {
		t.Fatalf("folder devices = %+v, want %+v (the hand-added server kept, the peers added)", fake.folders, want)
	}
	if fake.devicePosts != 1 {
		t.Errorf("%d device posts, want 1: only PEERONE was unknown, and only in the first round", fake.devicePosts)
	}
	byID := map[string]deviceConfig{}
	for _, dc := range fake.devices {
		byID[dc.DeviceID] = dc
	}
	if got := byID["SERVER"]; got.Name != server.Name || !slices.Equal(got.Addresses, server.Addresses) {
		t.Errorf("the hand-added server device changed: %+v", got)
	}
	if got := byID["PEERTWO"]; got.Name != "bob by hand" || got.Addresses[0] != "tcp://bob.example:22000" {
		t.Errorf("a device the instance already knew was re-posted: %+v", got)
	}
	if got := byID["PEERONE"]; got.Name != "alice" || !slices.Equal(got.Addresses, []string{"dynamic"}) {
		t.Errorf("the new peer = %+v", got)
	}
}

// TestApplyLeavesAFolderHeldAtAnotherPath: the folder id is already held
// at another path — the same folder of a second clone of the repository
// on this machine — so this checkout's apply reports conflict_path and
// leaves it exactly where it is, round after round, and creates nothing.
// The same path spelled through a symlink is not a conflict.
func TestApplyLeavesAFolderHeldAtAnotherPath(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	d := testDeps(srv, "")
	stateDir, _ := runningHome(t)
	first := filepath.Join(t.TempDir(), "clone-1", "shared")
	second := filepath.Join(t.TempDir(), "clone-2", "shared")
	fake.folders = []folderConfig{{ID: "brigade-aaaa-1111", Path: first, Devices: []deviceRef{{DeviceID: testID}}}}
	req := map[string]any{
		"state_dir": stateDir,
		"folders":   []map[string]string{{"id": "brigade-aaaa-1111", "path": second, "label": "repo/shared"}},
		"peers":     []map[string]string{{"peer": "PEERONE", "label": "alice"}},
	}
	for range 2 {
		res := mustOK(t, invoke(t, d, "apply", req), "apply")
		if fs := res["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["state"] != "conflict_path" {
			t.Fatalf("apply folders = %v, want conflict_path", fs)
		}
	}
	fake.mu.Lock()
	if fake.folderPosts != 0 || fake.folders[0].Path != first {
		t.Errorf("the held folder was re-posted (%d posts) or moved to %s", fake.folderPosts, fake.folders[0].Path)
	}
	fake.mu.Unlock()
	if _, err := os.Stat(second); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the conflicting checkout's folder was created: %v", err)
	}

	// The held path, reached through a symlink, is the same folder.
	if err := os.MkdirAll(first, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(first), link); err != nil {
		t.Fatal(err)
	}
	req["folders"] = []map[string]string{{"id": "brigade-aaaa-1111", "path": filepath.Join(link, "shared"), "label": "repo/shared"}}
	res := mustOK(t, invoke(t, d, "apply", req), "apply")
	if fs := res["folders"].([]any); fs[0].(map[string]any)["state"] != "idle" {
		t.Errorf("the same path through a symlink: folders = %v, want idle", fs)
	}
}

// testFolderID is foldersync.FolderID, the id every checkout derives
// (docs/sync-adapters.md, "Folder ids").
func testFolderID(ref, folder string) string {
	sum := sha256.Sum256([]byte(folder))
	return "brigade-" + ref + "-" + hex.EncodeToString(sum[:])[:12]
}

// TestApplyPausesAFolderTheProjectNoLongerLists: a folder this checkout
// shared and its project then drops is paused — PATCHed once, reported
// `paused` every round, never deleted — ".." folders included; listing
// it again un-pauses it through the ordinary post. Another repository's
// folder of the same team, a folder under this team's prefix whose label
// proves no name, and another team's folder are never touched.
func TestApplyPausesAFolderTheProjectNoLongerLists(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	d := testDeps(srv, "")
	stateDir, _ := runningHome(t)
	work := t.TempDir()
	root := filepath.Join(work, "repo")
	folder := func(name string) map[string]string {
		return map[string]string{"id": testFolderID("6f0f2b41", name), "path": filepath.Join(root, name), "label": "repo/" + name}
	}
	untouched := []folderConfig{
		// Another repository of the same team, beside this one.
		{ID: testFolderID("6f0f2b41", "notes"), Label: "other/notes", Path: filepath.Join(work, "other", "notes")},
		// Under this team's prefix, but its label proves no folder name.
		{ID: testFolderID("6f0f2b41", "kept"), Label: "the server's copy", Path: filepath.Join(root, "kept")},
		// Another team's folder at a path this checkout would use.
		{ID: testFolderID("0a0b0c0d", "gone"), Label: "repo/gone", Path: filepath.Join(root, "gone")},
	}
	fake.folders = slices.Clone(untouched)
	all := []map[string]string{folder("docs"), folder("shared/deep"), folder("../beside")}
	req := map[string]any{"state_dir": stateDir, "folders": all, "peers": []map[string]string{}}
	states := func(res map[string]any) map[string]string {
		out := map[string]string{}
		for _, f := range res["folders"].([]any) {
			m := f.(map[string]any)
			out[m["id"].(string)] = m["state"].(string)
		}
		return out
	}
	mustOK(t, invoke(t, d, "apply", req), "apply")

	req["folders"] = []map[string]string{folder("docs")}
	for round := range 2 {
		got := states(mustOK(t, invoke(t, d, "apply", req), "apply"))
		want := map[string]string{all[0]["id"]: "idle", all[1]["id"]: "paused", all[2]["id"]: "paused"}
		if !maps.Equal(got, want) {
			t.Fatalf("round %d: apply folders = %v, want %v", round, got, want)
		}
	}
	fake.mu.Lock()
	if fake.pauses != 2 {
		t.Errorf("%d pauses, want 2: each dropped folder is paused once", fake.pauses)
	}
	for _, c := range fake.folders {
		if slices.ContainsFunc(untouched, func(u folderConfig) bool { return u.ID == c.ID }) && c.Paused {
			t.Errorf("folder %s (%s) is not this checkout's and was paused", c.ID, c.Label)
		}
	}
	if len(fake.folders) != len(untouched)+3 {
		t.Errorf("folders = %+v: a dropped folder was deleted", fake.folders)
	}
	fake.mu.Unlock()
	for _, p := range []string{filepath.Join(root, "shared", "deep"), filepath.Join(work, "beside")} {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("a paused folder's directory is gone: %v", err)
		}
	}

	req["folders"] = all
	got := states(mustOK(t, invoke(t, d, "apply", req), "apply"))
	for _, f := range all {
		if got[f["id"]] != "idle" {
			t.Errorf("listed again: %s = %q, want idle", f["label"], got[f["id"]])
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, c := range fake.folders {
		if c.Paused {
			t.Errorf("folder %s is still paused after the project listed it again", c.Label)
		}
	}
}

// TestApplyRejectsAFolderItCannotCreate: a folder whose directory cannot
// be made is `rejected`, and the rest of the apply goes on.
func TestApplyRejectsAFolderItCannotCreate(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	d := testDeps(srv, "")
	stateDir, _ := runningHome(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a-file"), "not a directory")
	req := map[string]any{
		"state_dir": stateDir,
		"folders": []map[string]string{
			{"id": "brigade-aaaa-1111", "path": filepath.Join(root, "a-file", "sub"), "label": "repo/a-file/sub"},
			{"id": "brigade-aaaa-2222", "path": filepath.Join(root, "docs"), "label": "repo/docs"},
		},
		"peers": []map[string]string{},
	}
	res := mustOK(t, invoke(t, d, "apply", req), "apply")
	fs := res["folders"].([]any)
	if len(fs) != 2 || fs[0].(map[string]any)["state"] != "rejected" || fs[1].(map[string]any)["state"] != "idle" {
		t.Errorf("apply folders = %v, want rejected then idle", fs)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.folders) != 1 || fake.folders[0].ID != "brigade-aaaa-2222" {
		t.Errorf("folders = %+v, want the creatable one alone", fake.folders)
	}
}

// TestAttachRecordsItsHolderAndPrunesDeadRefs: attach keeps the request's
// pid (with its start token) in refs/<session_id>; every attach and
// detach prunes a ref whose holder is gone or is another incarnation of
// its pid, and keeps a ref with no pid, an unreadable one, and a live
// holder's. The last live ref's detach then stops the instance.
func TestAttachRecordsItsHolderAndPrunesDeadRefs(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	stateDir, daemon := runningHome(t)
	fake.onShutdown = func() { _ = syscall.Kill(daemon, syscall.SIGTERM) }
	refs := filepath.Join(stateDir, "sync", "syncthing", "refs")
	dead := exec.CommandContext(t.Context(), "/usr/bin/true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	live := testutil.NewSleeper(t)
	liveInfo, err := procutil.Lookup(live)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(refs, "dead"), strconv.Itoa(dead.Process.Pid)+"\n")
	writeFile(t, filepath.Join(refs, "reused"), strconv.Itoa(testutil.NewSleeper(t))+" 1.000000\n")
	writeFile(t, filepath.Join(refs, "live"), strconv.Itoa(live)+" "+liveInfo.StartToken+"\n")
	writeFile(t, filepath.Join(refs, "nopid"), "")
	writeFile(t, filepath.Join(refs, "garbage"), "not a pid\n")
	d := testDeps(srv, "")

	o := invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s1", "pid": -4})
	if o.exit != 3 || o.env.Error.Details["field"] != "pid" {
		t.Fatalf("a negative pid: exit %d, stdout %q", o.exit, o.stdout)
	}
	mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s1", "pid": os.Getpid()}), "attach")
	self, err := procutil.Lookup(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(refs, "s1")); got != strconv.Itoa(os.Getpid())+" "+self.StartToken+"\n" {
		t.Errorf("refs/s1 = %q, want the holder's pid and start token", got)
	}
	names := func() []string {
		t.Helper()
		entries, err := os.ReadDir(refs)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"garbage", "live", "nopid", "s1"}) {
		t.Fatalf("refs after attach = %v, want the dead and reused holders pruned", got)
	}

	// The live holder dies without detaching; s1's detach prunes it.
	if err := syscall.Kill(live, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool { return !alive(live, "") })
	if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "s1", "pid": os.Getpid()}), "detach"); res["stopped"] != false {
		t.Fatalf("detach with pid-less refs left: %v", res)
	}
	if got := names(); !slices.Equal(got, []string{"garbage", "nopid"}) {
		t.Fatalf("refs after detach = %v, want only the refs no pid can prune", got)
	}
	mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "garbage"}), "detach")
	if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "nopid"}), "detach"); res["stopped"] != true {
		t.Fatalf("the last detach: %v", res)
	}
}

// TestAttachGivesTheInstanceItsOwnListenPort: attach picks a free port,
// keeps it in listen-port, and sets options.listenAddresses to TCP and
// QUIC on it plus the dynamic relay pool — through one PATCH, and none
// when they already match; a kept port survives the next attach.
func TestAttachGivesTheInstanceItsOwnListenPort(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	stateDir, _ := runningHome(t)
	fake.listen = []string{"default"}
	d := testDeps(srv, "")
	mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "attach")
	kept := strings.TrimSpace(readFile(t, filepath.Join(stateDir, "sync", "syncthing", "listen-port")))
	port, err := strconv.Atoi(kept)
	if err != nil || port <= 0 || port >= 65536 || port == 22000 {
		t.Fatalf("listen-port = %q, want a port of the instance's own", kept)
	}
	want := []string{"tcp://0.0.0.0:" + kept, "quic://0.0.0.0:" + kept, "dynamic+https://relays.syncthing.net/endpoint"}
	check := func(sets int) {
		t.Helper()
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if !slices.Equal(fake.listen, want) || fake.listenSets != sets {
			t.Fatalf("listenAddresses = %v after %d PATCHes, want %v after %d", fake.listen, fake.listenSets, want, sets)
		}
	}
	check(1)
	mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s2"}), "attach")
	check(1)
	if got := strings.TrimSpace(readFile(t, filepath.Join(stateDir, "sync", "syncthing", "listen-port"))); got != kept {
		t.Errorf("listen-port moved from %s to %s while the instance ran", kept, got)
	}
	// Someone changed it by hand: the next attach sets it back.
	fake.mu.Lock()
	fake.listen = []string{"default"}
	fake.mu.Unlock()
	mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "attach")
	check(2)
}

func TestApplyAndStatusNeedARunningDaemon(t *testing.T) {
	t.Parallel()
	_, srv := newFakeAPI(t)
	o := invoke(t, testDeps(srv, ""), "apply", map[string]any{"state_dir": t.TempDir(), "folders": []any{}, "peers": []any{}})
	if o.exit != 9 || o.env.Error.Details["reason"] != "not_running" {
		t.Errorf("apply with no daemon: exit %d, stdout %q", o.exit, o.stdout)
	}
}

func TestStatusReportsFoldersAndPeers(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	stateDir, _ := runningHome(t)
	fake.devices = []deviceConfig{{DeviceID: "PEERONE"}}
	fake.connected["PEERONE"] = true
	fake.folders = []folderConfig{{ID: "brigade-x", Path: "/somewhere"}}
	res := mustOK(t, invoke(t, testDeps(srv, ""), "status", map[string]any{"state_dir": stateDir}), "status")
	if res["running"] != true || res["peer"] != testID {
		t.Errorf("status = %v", res)
	}
	folders, peers := res["folders"].([]any), res["peers"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["path"] != "/somewhere" || folders[0].(map[string]any)["state"] != "idle" {
		t.Errorf("status folders = %v", folders)
	}
	if len(peers) != 1 || peers[0].(map[string]any)["peer"] != "PEERONE" || peers[0].(map[string]any)["connected"] != true {
		t.Errorf("status peers = %v (the instance's own device must not be listed)", peers)
	}
}

func TestDetachStopsOnlyWithTheLastSession(t *testing.T) {
	t.Parallel()
	for _, failStop := range []bool{false, true} {
		fake, srv := newFakeAPI(t)
		stateDir, pid := runningHome(t)
		home := filepath.Join(stateDir, "sync", "syncthing")
		writeFile(t, filepath.Join(home, "refs", "s1"), "")
		writeFile(t, filepath.Join(home, "refs", "s2"), "")
		fake.failStop = failStop
		fake.onShutdown = func() { _ = syscall.Kill(pid, syscall.SIGTERM) }
		d := testDeps(srv, "")
		if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "detach"); res["stopped"] != false {
			t.Errorf("detach with a session left: %v", res)
		}
		if !alive(pid, "") {
			t.Fatal("the daemon died while a session still referenced it")
		}
		if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "s2"}), "detach"); res["stopped"] != true {
			t.Errorf("last detach: %v", res)
		}
		testutil.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool { return !alive(pid, "") })
		if _, err := os.Stat(filepath.Join(home, "daemon.pid")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("daemon.pid survived the last detach: %v", err)
		}
		fake.mu.Lock()
		if fake.shutdowns != 1 {
			t.Errorf("failStop=%v: %d shutdown calls, want 1", failStop, fake.shutdowns)
		}
		fake.mu.Unlock()
		// Detaching again is harmless (idempotent).
		if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "s2"}), "detach"); res["stopped"] != true {
			t.Errorf("repeat detach: %v", res)
		}
	}
}

// fakeSyncthing is the fixture standing in for the real binary: it parses
// --home and --gui-address as `syncthing serve` would, counts its starts,
// writes a line to its output, writes config.xml after a delay (so the
// adapter's wait is exercised), then becomes a long sleep (exec keeps the
// pid and its start token, as the pidfile expects). mode "noconfig" never
// writes config.xml; "exit" dies at once; "child" also runs a background
// child in its process group, as `syncthing serve`'s monitor does, and
// records its pid in <home>/child.pid.
func fakeSyncthing(t *testing.T, mode string) string {
	t.Helper()
	script := `home=""; gui=""
while [ $# -gt 0 ]; do
  case "$1" in
    --home) home=$2; shift ;;
    --gui-address) gui=$2; shift ;;
  esac
  shift
done
echo start >> "$home/starts"
printf '%s\n' "$gui" > "$home/gui-address"
echo "fake syncthing starting"
[ "` + mode + `" = exit ] && exit 3
if [ "` + mode + `" = child ]; then
  sleep 300 &
  echo $! > "$home/child.pid"
fi
sleep 0.3
if [ "` + mode + `" != noconfig ]; then
  printf '<configuration version="37"><gui enabled="true"><address>%s</address><apikey>%s</apikey></gui></configuration>\n' "$gui" "` + testKey + `" > "$home/config.xml.tmp"
  mv "$home/config.xml.tmp" "$home/config.xml"
fi
exec sleep 300
`
	path := filepath.Join(t.TempDir(), "fake-syncthing.sh")
	writeFile(t, path, script)
	return path
}

// killDaemon is a cleanup: whatever daemon.pid names dies with the test.
func killDaemon(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(home, "daemon.pid"))
		if err != nil {
			return
		}
		pidText, _, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
		if pid, err := strconv.Atoi(pidText); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

func daemonPID(t *testing.T, home string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "daemon.pid"))
	if err != nil {
		t.Fatalf("daemon.pid: %v", err)
	}
	pidText, _, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestAttachStartsOneDaemonAndTheLastDetachStopsIt(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	stateDir := t.TempDir()
	home := filepath.Join(stateDir, "sync", "syncthing")
	killDaemon(t, home)
	fake.onShutdown = func() { _ = syscall.Kill(daemonPID(t, home), syscall.SIGTERM) }
	d := testDeps(srv, fakeSyncthing(t, ""))

	res := mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "attach")
	if res["peer"] != testID {
		t.Errorf("attach peer = %v, want the fake myID", res["peer"])
	}
	first := daemonPID(t, home)
	if !alive(first, "") {
		t.Fatal("the daemon is not running after attach")
	}
	fi, err := os.Stat(home)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("home mode = %v (%v), want 0700", fi.Mode().Perm(), err)
	}
	port := strings.TrimSpace(readFile(t, filepath.Join(home, "port")))
	if got := strings.TrimSpace(readFile(t, filepath.Join(home, "gui-address"))); got != "127.0.0.1:"+port {
		t.Errorf("--gui-address = %q, want 127.0.0.1:%s (the kept port, loopback only)", got, port)
	}
	if !strings.Contains(readFile(t, filepath.Join(home, "syncthing.log")), "fake syncthing starting") {
		t.Error("the daemon's output did not reach syncthing.log")
	}

	// A second session joins the running daemon; nothing new starts.
	mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s2"}), "attach")
	if daemonPID(t, home) != first || strings.Count(readFile(t, filepath.Join(home, "starts")), "start") != 1 {
		t.Error("a second attach started a second daemon")
	}
	if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "detach"); res["stopped"] != false || !alive(first, "") {
		t.Errorf("first detach: %v, daemon alive %v", res, alive(first, ""))
	}
	if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "s2"}), "detach"); res["stopped"] != true {
		t.Errorf("last detach: %v", res)
	}
	if alive(first, "") {
		t.Error("the daemon survived the last detach")
	}

	// The next attach starts a fresh daemon on the SAME kept port.
	mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s3"}), "attach")
	if got := strings.TrimSpace(readFile(t, filepath.Join(home, "port"))); got != port {
		t.Errorf("port = %s after a restart, want the kept %s", got, port)
	}
	if daemonPID(t, home) == first {
		t.Error("no new daemon after the restart")
	}
}

// TestAFailedShutdownSignalsTheDaemonsProcessGroup: when the REST
// shutdown fails, the last detach signals the daemon's whole process
// group, so a child holding the sockets stops with the monitor.
func TestAFailedShutdownSignalsTheDaemonsProcessGroup(t *testing.T) {
	t.Parallel()
	fake, srv := newFakeAPI(t)
	fake.failStop = true
	stateDir := t.TempDir()
	home := filepath.Join(stateDir, "sync", "syncthing")
	killDaemon(t, home)
	d := testDeps(srv, fakeSyncthing(t, "child"))
	mustOK(t, invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "attach")
	daemon := daemonPID(t, home)
	child, err := strconv.Atoi(strings.TrimSpace(readFile(t, filepath.Join(home, "child.pid"))))
	if err != nil || !alive(child, "") {
		t.Fatalf("the fake's child is not running (%v)", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	// The daemon was started by this process: reap it when it dies, as
	// init does for a real one, so its group empties.
	go func() {
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(daemon, &ws, 0, nil)
	}()
	if res := mustOK(t, invoke(t, d, "detach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "detach"); res["stopped"] != true {
		t.Errorf("last detach: %v", res)
	}
	testutil.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool { return !alive(daemon, "") && !alive(child, "") })
}

func TestAttachReplacesAStalePidfile(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]func(t *testing.T) string{
		// No such process.
		"dead": func(t *testing.T) string {
			t.Helper()
			cmd := exec.CommandContext(t.Context(), "/usr/bin/true")
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			return strconv.Itoa(cmd.Process.Pid) + " 1.000000\n"
		},
		// A live process, but not the incarnation that was recorded: the
		// kernel reused the pid.
		"reused": func(t *testing.T) string {
			t.Helper()
			return strconv.Itoa(testutil.NewSleeper(t)) + " 1.000000\n"
		},
		"garbage": func(*testing.T) string { return "not a pid\n" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, srv := newFakeAPI(t)
			stateDir := t.TempDir()
			home := filepath.Join(stateDir, "sync", "syncthing")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(home, "daemon.pid"), content(t))
			killDaemon(t, home)
			mustOK(t, invoke(t, testDeps(srv, fakeSyncthing(t, "")), "attach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "attach")
			if !alive(daemonPID(t, home), "") || strings.Count(readFile(t, filepath.Join(home, "starts")), "start") != 1 {
				t.Error("a stale pidfile did not lead to exactly one fresh start")
			}
		})
	}
}

func TestAttachFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode, reason string
	}{
		{"missing", "syncthing_not_found"},
		{"exit", "syncthing_exited"},
		{"noconfig", "start_timeout"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			_, srv := newFakeAPI(t)
			stateDir := t.TempDir()
			home := filepath.Join(stateDir, "sync", "syncthing")
			killDaemon(t, home)
			script := ""
			if tc.mode != "missing" {
				script = fakeSyncthing(t, tc.mode)
			}
			d := testDeps(srv, script)
			d.startWait = time.Second
			o := invoke(t, d, "attach", map[string]any{"state_dir": stateDir, "session_id": "s1"})
			if o.exit != 9 || o.env.Error.Code != "unavailable" || o.env.Error.Details["reason"] != tc.reason {
				t.Fatalf("exit %d, stdout %q, want unavailable/%s", o.exit, o.stdout, tc.reason)
			}
			if _, err := os.Stat(filepath.Join(home, "refs", "s1")); !errors.Is(err, os.ErrNotExist) {
				t.Error("a failed attach left its ref behind")
			}
			if tc.mode == "noconfig" {
				pid := daemonPID(t, home)
				testutil.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool { return !alive(pid, "") })
			}
		})
	}
}

func TestAStartTruncatesAnOversizedLog(t *testing.T) {
	t.Parallel()
	_, srv := newFakeAPI(t)
	stateDir := t.TempDir()
	home := filepath.Join(stateDir, "sync", "syncthing")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "syncthing.log"), strings.Repeat("x", maxLogBytes+1))
	killDaemon(t, home)
	mustOK(t, invoke(t, testDeps(srv, fakeSyncthing(t, "")), "attach", map[string]any{"state_dir": stateDir, "session_id": "s1"}), "attach")
	if got := readFile(t, filepath.Join(home, "syncthing.log")); len(got) > 1024 || !strings.Contains(got, "fake syncthing starting") {
		t.Errorf("syncthing.log is %d bytes after the start, want only the new output", len(got))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
