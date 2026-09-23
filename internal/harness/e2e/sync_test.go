package e2e

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// syncthingVar opts in to the one e2e test that runs the real `syncthing`
// on PATH. `make test` stays hermetic without it: no daemon, no network,
// no port 22000.
const syncthingVar = "BRIGADE_TEST_SYNCTHING"

// Hang catchers for the smoke, never performance bounds (plan 7.3):
// waitSync covers the watcher's roster read (every 15 s — a peer
// published after a session's last apply is introduced at the next read,
// and at the latest by the 60 s re-apply) plus Syncthing's discovery;
// waitFile covers a transfer, which Syncthing starts after its filesystem
// watcher's 10 s delay.
const (
	waitSync  = 180 * time.Second
	waitFile  = 120 * time.Second
	pollSync  = 500 * time.Millisecond
	syncLabel = "smoke"
	syncDir   = "shared"
)

// syncStatus is the --json result of `brigade sync status`, the members
// this file reads.
type syncStatus struct {
	Sync       string                   `json:"sync"`
	Running    bool                     `json:"running"`
	Peer       string                   `json:"peer"`
	Folders    []foldersync.FolderState `json:"folders"`
	Peers      []syncStatusPeer         `json:"peers"`
	Configured []foldersync.Folder      `json:"configured"`
}

type syncStatusPeer struct {
	Peer      string `json:"peer"`
	Connected bool   `json:"connected"`
}

// TestTwoSessionsSyncAFolderThroughSyncthing is the real-binary smoke of
// the folder-sync plan (§5.2): two personas on this machine, each with its
// own config dir, state dir and checkout of one repository whose
// .brigade.json lists a `sync` folder, the fs adapter as the backend, the
// hooks as real processes and the REAL detached watchers driving the
// bundled Syncthing adapter — so two dedicated Syncthing instances, one
// per state dir.
//
//  1. SessionStart for both says file sync is on; the roster shows both
//     sessions carrying a `syncthing:` sync_peer; each session's `brigade
//     sync status` reports its engine running with the folder configured
//     and the other instance's device connected.
//  2. A file alice writes appears at bob's, and one bob writes at alice's.
//  3. One file edited on both sides within a second leaves a
//     .sync-conflict-* copy on at least one side.
//  4. SessionEnd for alice stops her instance (no other session of hers
//     references it: its pidfile and process are gone) while bob's runs
//     on; SessionEnd for bob stops his.
//  5. U-25: neither instance's API key is on any argv while they run, nor
//     in any file under the rig but the instance's own config.xml.
//
// Every duration measured is logged; every wait is a hang catcher.
func TestTwoSessionsSyncAFolderThroughSyncthing(t *testing.T) {
	if os.Getenv(syncthingVar) != "1" {
		t.Skip("set " + syncthingVar + "=1 to run against the syncthing on PATH")
	}
	syncthing, err := exec.LookPath("syncthing")
	if err != nil {
		t.Fatalf("%s=1 but syncthing is not on PATH: %v", syncthingVar, err)
	}
	r := newRig(t)
	r.createTeam()

	// The bundled adapter is [plugin_bin, "sync-adapter", "syncthing"]: the
	// plugin root's bin/brigade becomes a symlink to the built binary (the
	// hook resolves plugin_bin through it). The sessions' PATH holds only
	// syncthing, so the adapter finds it and nothing else.
	bootstrap := filepath.Join(r.dirs.PluginRoot, "bin", "brigade")
	if err := os.Remove(bootstrap); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.brigade, bootstrap); err != nil {
		t.Fatal(err)
	}
	r.pluginBin = realpath(t, bootstrap)
	pathDir := filepath.Join(r.dirs.Root, "path-syncthing")
	if err := os.MkdirAll(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(syncthing, filepath.Join(pathDir, "syncthing")); err != nil {
		t.Fatal(err)
	}

	// The team file gains the sync member; bob's checkout is a second
	// directory holding the identical file, pinned in his own store.
	teamFile := filepath.Join(r.checkout, teamfile.FileName)
	raw, err := os.ReadFile(teamFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["sync"] = map[string]any{"folders": []string{syncDir}}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	bobCheckout := filepath.Join(r.dirs.Root, "checkout-bob")
	if err := os.MkdirAll(filepath.Join(bobCheckout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{r.checkout, bobCheckout} {
		if err := os.WriteFile(filepath.Join(dir, teamfile.FileName), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	canonB, err := teamfile.Canonicalize(bobCheckout)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(r.configDirB, canonB, teamstore.Pin{
		Adapter: "fs", URL: "http://127.0.0.1:1", PublishableKey: "placeholder", TeamRef: r.teamRef,
	}); err != nil {
		t.Fatal(err)
	}

	// Two sessions with sockets (a watcher runs only for one), bob's with
	// his own state dir and checkout, both with one workspace label — the
	// checkouts' directory names differ, and only sessions of the same
	// label share folders.
	sockA, sockB := fakesock.New(t), fakesock.New(t)
	tokenA := "cc-messaging-secret-A-MUST-NOT-LEAK-" + testutil.RunID()
	tokenB := "cc-messaging-secret-B-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", aliceName, sockA.Path(), tokenA)
	bob := r.newSession("bob", "", bobName, sockB.Path(), tokenB)
	bob.cwd = bobCheckout
	r.ownStateHome(&bob, "xdg-state-bob")
	for _, s := range []*session{&alice, &bob} {
		s.extraEnv = []string{"PATH=" + r.emptyPath + string(os.PathListSeparator) + pathDir, config.OptionWorkspaceLabel + "=" + syncLabel}
	}
	homes := map[string]string{
		"alice": filepath.Join(r.stateDirOf(alice), "sync", "syncthing"),
		"bob":   filepath.Join(r.stateDirOf(bob), "sync", "syncthing"),
	}
	// Runs before the rig's own reaping (Cleanup is LIFO): the watchers
	// first, whose exit detaches, then any instance a failed run left.
	t.Cleanup(func() {
		r.reapWatchers()
		for _, home := range homes {
			stopDaemon(t, home)
		}
	})

	// --- 1. start, publish, configure, connect ------------------------------
	start := time.Now()
	for _, s := range []session{alice, bob} {
		res := r.hook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
		if !strings.Contains(res.stdout, "Brigade: file sync on: 1 folder(s) through syncthing.") {
			t.Fatalf("session-start for %s did not turn file sync on:\nstdout: %s\nstderr: %s", s.profile, res.stdout, res.stderr)
		}
		if m := r.mustMap(s); m.SyncAdapter != "syncthing" || strings.Join(m.SyncFolders, ",") != syncDir || m.PluginBin != r.pluginBin {
			t.Fatalf("%s's map: sync %q %v plugin_bin %q", s.profile, m.SyncAdapter, m.SyncFolders, m.PluginBin)
		}
		r.liveWatcher(s)
	}
	mA, mB := r.mustMap(alice), r.mustMap(bob)

	peers := map[string]string{} // session id → descriptor
	testutil.Eventually(t, waitLong, pollSync, func() bool {
		roster := jsonResult[rosterResult](t, r.mustRun(r.env(alice), "", "sessions", "--json").stdout)
		for _, rec := range roster.Sessions {
			if rec.SyncPeer != nil {
				if d, ok := strings.CutPrefix(*rec.SyncPeer, "syncthing:"); ok {
					peers[rec.SessionID] = d
				}
			}
		}
		return peers[mA.BrigadeSessionID] != "" && peers[mB.BrigadeSessionID] != ""
	})
	t.Logf("both sessions carried a syncthing: sync_peer %s after the first SessionStart", time.Since(start).Round(time.Millisecond))
	devA, devB := peers[mA.BrigadeSessionID], peers[mB.BrigadeSessionID]
	if devA == devB {
		t.Fatalf("both sessions published one device id %s: one instance, not two", devA)
	}

	status := func(s session) syncStatus {
		t.Helper()
		return jsonResult[syncStatus](t, r.mustRun(r.env(s), "", "sync", "status", "--json").stdout)
	}
	folderID := foldersync.FolderID(r.teamRef, syncDir)
	configured := func(st syncStatus, root string) bool {
		for _, f := range st.Folders {
			if f.ID == folderID && samePath(f.Path, filepath.Join(root, syncDir)) {
				return true
			}
		}
		return false
	}
	peerState := func(st syncStatus, dev string) (listed, connected bool) {
		for _, p := range st.Peers {
			if p.Peer == dev {
				return true, p.Connected
			}
		}
		return false, false
	}
	var introduced, connected time.Duration
	testutil.Eventually(t, waitSync, pollSync, func() bool {
		sa, sb := status(alice), status(bob)
		if !sa.Running || !sb.Running || sa.Peer != devA || sb.Peer != devB {
			t.Fatalf("sync status: alice running=%v peer=%s, bob running=%v peer=%s", sa.Running, sa.Peer, sb.Running, sb.Peer)
		}
		la, ca := peerState(sa, devB)
		lb, cb := peerState(sb, devA)
		if introduced == 0 && la && lb {
			introduced = time.Since(start)
		}
		if !configured(sa, r.checkout) || !configured(sb, bobCheckout) || !ca || !cb {
			return false
		}
		connected = time.Since(start)
		return true
	})
	t.Logf("each instance listed the other's device %s after the first SessionStart; both reported it connected at %s (%s after both were introduced)",
		introduced.Round(time.Millisecond), connected.Round(time.Millisecond), (connected - introduced).Round(time.Millisecond))
	// Each instance listens on a port of its own (listen-port), never
	// Syncthing's default 22000, so neither shares a port with the other
	// or with a Syncthing the person runs; how they connected, in
	// Syncthing's own words, is worth recording.
	listen := map[string]string{}
	for name, home := range homes {
		raw, err := os.ReadFile(filepath.Join(home, "listen-port")) //nolint:gosec // G304: the instance's own file under the test's tree
		if err != nil {
			t.Fatalf("%s's instance has no listen-port: %v", name, err)
		}
		listen[name] = strings.TrimSpace(string(raw))
		if listen[name] == "22000" || listen[name] == "" {
			t.Fatalf("%s's instance listens on %q, want a port of its own", name, listen[name])
		}
	}
	if listen["alice"] == listen["bob"] {
		t.Fatalf("both instances kept listen port %s", listen["alice"])
	}
	t.Logf("listen ports: alice %s, bob %s", listen["alice"], listen["bob"])
	// A fresh instance starts on Syncthing's default (22000) before attach
	// patches its listen addresses; once patched it must hold no 22000
	// listener. Measured with lsof where the machine has it, by process
	// group: daemon.pid names Syncthing's monitor process (v2.1.5 runs one
	// even with --no-restart), and its child — in the group the Setsid
	// start made — holds the sockets.
	if lsof, err := exec.LookPath("lsof"); err == nil {
		for name, home := range homes {
			pid := strconv.Itoa(daemonPID(t, home))
			var ports string
			for deadline := time.Now().Add(waitLong); ; time.Sleep(pollSync) {
				out, _ := exec.CommandContext(t.Context(), lsof, "-nP", "-a", "-g", pid, "-iTCP", "-sTCP:LISTEN").Output() //nolint:gosec // G204: lsof found on PATH, an argv array, and a pid this test read
				ports = string(out)
				if strings.Contains(ports, ":"+listen[name]+" ") && !strings.Contains(ports, ":22000 ") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s's instance (pid %s) TCP listeners after %s, want %s and not 22000:\n%s", name, pid, waitLong, listen[name], ports)
				}
			}
			t.Logf("%s's instance listens (TCP) on its own port %s and not on 22000", name, listen[name])
		}
	} else {
		t.Log("no lsof on this machine: the instances' listeners were not inspected")
	}
	for _, name := range []string{"alice", "bob"} {
		for _, line := range engineLog(t, homes[name], "listener starting", "Failed to listen", "Established secure connection") {
			t.Logf("%s's syncthing.log: %s", name, line)
		}
	}
	for name, home := range homes {
		if _, err := os.Stat(filepath.Join(home, "daemon.pid")); err != nil {
			t.Fatalf("%s's instance has no daemon.pid: %v", name, err)
		}
	}
	// The notice follows each apply; the apply after the one that
	// introduced the other device (at the next roster read) reports it
	// connected. A prompt prints the notice only when it changed.
	var notices []string
	testutil.Eventually(t, waitSync, pollSync, func() bool {
		res := r.hook(alice, "prompt", alice.hookDoc("UserPromptSubmit", map[string]any{"permission_mode": "default", "prompt": "never read"}))
		if out := strings.TrimSpace(res.stdout); out != "" {
			notices = append(notices, out)
		}
		return strings.Contains(res.stdout, "Brigade sync: 1 folders, 1 of 1 peers connected")
	})
	t.Logf("alice's prompts printed %q; the last %s after the first SessionStart", notices, time.Since(start).Round(time.Millisecond))

	// --- 5 (while running). U-25: no API key on any argv ------------------------
	keys := map[string]string{"alice": apiKey(t, homes["alice"]), "bob": apiKey(t, homes["bob"])}
	ps := psArgs(t)
	for name, home := range homes {
		if !strings.Contains(ps, home) {
			t.Fatalf("ps shows no process naming %s's instance home: the argv check is not live\n%s", name, ps)
		}
		if strings.Contains(ps, keys[name]) {
			t.Errorf("%s's instance API key is on a process's argv", name)
		}
	}

	// --- 2. a file each way ---------------------------------------------------
	aliceDir, bobDir := filepath.Join(r.checkout, syncDir), filepath.Join(bobCheckout, syncDir)
	took := syncFile(t, aliceDir, bobDir, "from-alice.txt", "written in alice's checkout\n")
	t.Logf("alice's file appeared in bob's checkout %s after it was written", took.Round(time.Millisecond))
	took = syncFile(t, bobDir, aliceDir, "from-bob.txt", "written in bob's checkout\n")
	t.Logf("bob's file appeared in alice's checkout %s after it was written", took.Round(time.Millisecond))

	// --- 3. a conflict ----------------------------------------------------------
	took = syncFile(t, aliceDir, bobDir, "both.txt", "the base both sides start from\n")
	t.Logf("the conflict's base reached bob %s after it was written", took.Round(time.Millisecond))
	edited := time.Now()
	writeFile(t, filepath.Join(aliceDir, "both.txt"), "alice's edit, the longer of the two\n")
	writeFile(t, filepath.Join(bobDir, "both.txt"), "bob's edit\n")
	gap := time.Since(edited)
	if gap >= time.Second {
		t.Fatalf("the two edits were %s apart, not within a second", gap)
	}
	var copies []string
	testutil.Eventually(t, waitFile, pollSync, func() bool {
		copies = nil
		for _, dir := range []string{aliceDir, bobDir} {
			m, _ := filepath.Glob(filepath.Join(dir, "both.sync-conflict-*"))
			copies = append(copies, m...)
		}
		return len(copies) > 0
	})
	t.Logf("edits %s apart; a conflict copy appeared %s later: %v", gap.Round(time.Microsecond), time.Since(edited).Round(time.Millisecond), relTo(r.dirs.Root, copies))

	// --- 4. the engines stop with their last session ------------------------------
	pidA, pidB := daemonPID(t, homes["alice"]), daemonPID(t, homes["bob"])
	ended := time.Now()
	r.hook(alice, "session-end", alice.hookDoc("SessionEnd", map[string]any{"reason": "other"}))
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		_, err := os.Lstat(filepath.Join(homes["alice"], "daemon.pid"))
		return !alive(pidA) && os.IsNotExist(err)
	})
	t.Logf("alice's instance (pid %d) and its pidfile were gone %s after her session-end", pidA, time.Since(ended).Round(time.Millisecond))
	if refs, err := os.ReadDir(filepath.Join(homes["alice"], "refs")); err != nil || len(refs) != 0 {
		t.Errorf("alice's refs after her session-end: %v %v", refs, err)
	}
	if !alive(pidB) {
		t.Fatal("bob's instance stopped with alice's session")
	}
	ended = time.Now()
	r.hook(bob, "session-end", bob.hookDoc("SessionEnd", map[string]any{"reason": "other"}))
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		_, err := os.Lstat(filepath.Join(homes["bob"], "daemon.pid"))
		return !alive(pidB) && os.IsNotExist(err)
	})
	t.Logf("bob's instance (pid %d) and its pidfile were gone %s after his session-end", pidB, time.Since(ended).Round(time.Millisecond))

	// --- 5. U-25: the keys in no file but their own config ----------------------
	// Syncthing keeps the key in config.xml and, measured with v2.1.5, in
	// the config.xml.v0 it archives when it first migrates a fresh config:
	// both are its own files in the instance's home. Anywhere else is a
	// leak.
	for name, key := range keys {
		found := false
		for _, hit := range tokenHits(t, key, r.dirs.Root) {
			if filepath.Dir(hit) == homes[name] && strings.HasPrefix(filepath.Base(hit), "config.xml") {
				if hit == filepath.Join(homes[name], "config.xml") {
					found = true
				}
				continue
			}
			t.Errorf("%s's instance API key appears in %s", name, hit)
		}
		if !found {
			t.Errorf("the key walk did not find %s's key in its own config.xml: the walk is not live", name)
		}
	}
	r.assertTokenNowhere(tokenA)
	r.assertTokenNowhere(tokenB)
}

// syncFile writes name under from and waits for it to appear under to
// with the same content, returning how long that took.
func syncFile(t *testing.T, from, to, name, content string) time.Duration {
	t.Helper()
	written := time.Now()
	writeFile(t, filepath.Join(from, name), content)
	testutil.Eventually(t, waitFile, pollSync, func() bool {
		got, err := os.ReadFile(filepath.Join(to, name)) //nolint:gosec // G304: the test's own checkout
		return err == nil && string(got) == content
	})
	return time.Since(written)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// apiKey reads an instance's API key from its config.xml, for the U-25
// checks only; the test never puts it anywhere.
func apiKey(t *testing.T, home string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "config.xml")) //nolint:gosec // G304: the instance's own config under the test's tree
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := bytes.Cut(raw, []byte("<apikey>"))
	key, _, ok2 := bytes.Cut(rest, []byte("</apikey>"))
	if !ok || !ok2 || len(key) < 16 {
		t.Fatalf("no API key in %s/config.xml", home)
	}
	return string(key)
}

// psArgs is every process's full argv on this machine, as ps prints it.
func psArgs(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ps", "-axww", "-o", "args=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	return string(out)
}

// daemonPID is the pid an instance's daemon.pid names ("<pid> <token>").
func daemonPID(t *testing.T, home string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "daemon.pid")) //nolint:gosec // G304: the instance's own pidfile under the test's tree
	if err != nil {
		t.Fatalf("daemon.pid: %v", err)
	}
	pidText, _, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		t.Fatalf("daemon.pid %q", raw)
	}
	return pid
}

// stopDaemon is the cleanup of an instance a failed run left: the pid its
// daemon.pid names, when it is still this user's live process, is
// SIGTERMed and then SIGKILLed.
func stopDaemon(t *testing.T, home string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "daemon.pid")) //nolint:gosec // G304: the instance's own pidfile under the test's tree
	if err != nil {
		return
	}
	pidText, _, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return
	}
	if info, err := procutil.Lookup(pid); err != nil || !info.Exists || info.Zombie || info.Foreign {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(10 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("syncthing %d under %s survived SIGTERM; SIGKILLed", pid, home)
			return
		}
		time.Sleep(pollEvery)
	}
}

// samePath reports whether a and b name one path once symlinks are
// resolved (the adapter reports the canonical /private/var form of the
// rig's /var paths on macOS).
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// engineLog is the distinct lines of an instance's syncthing.log that
// contain any of needles, for the measurements the test logs.
func engineLog(t *testing.T, home string, needles ...string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "syncthing.log")) //nolint:gosec // G304: the instance's own log under the test's tree
	if err != nil {
		t.Fatalf("syncthing.log: %v", err)
	}
	seen := map[string]bool{}
	var out []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		for _, n := range needles {
			if strings.Contains(line, n) && !seen[line] {
				seen[line] = true
				out = append(out, line)
			}
		}
	}
	return out
}

// relTo shortens paths under root for a log line.
func relTo(root string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if rel, err := filepath.Rel(root, p); err == nil {
			p = rel
		}
		out = append(out, p)
	}
	return out
}
