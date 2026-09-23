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

	res = call("detach", map[string]any{"state_dir": stateDir, "session_id": "real-1"})
	if res["stopped"] != true {
		t.Errorf("detach = %v", res)
	}
	if alive(pid, "") {
		t.Error("syncthing survived the last detach")
	}
}
