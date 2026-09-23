package syncthing

import (
	"os"
	"path/filepath"
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

	res = call("status", map[string]any{"state_dir": stateDir})
	if res["running"] != true || res["peer"] != id {
		t.Errorf("status = %v", res)
	}
	if fs := res["folders"].([]any); len(fs) != 1 || fs[0].(map[string]any)["path"] != folder {
		t.Errorf("status folders = %v", fs)
	}
	if ps := res["peers"].([]any); len(ps) != 1 || ps[0].(map[string]any)["peer"] != realTestPeer {
		t.Errorf("status peers = %v", ps)
	}

	res = call("detach", map[string]any{"state_dir": stateDir, "session_id": "real-1"})
	if res["stopped"] != true {
		t.Errorf("detach = %v", res)
	}
	if alive(pid, "") {
		t.Error("syncthing survived the last detach")
	}
}
