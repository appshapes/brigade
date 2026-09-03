package e2e

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// sendResult is the --json result of `brigade send`.
type sendResult struct {
	MessageID          string `json:"message_id"`
	RecipientSessionID string `json:"recipient_session_id"`
	HopCount           int    `json:"hop_count"`
	Duplicate          bool   `json:"duplicate"`
}

// TestSessionLifecycleThroughTheRealBinary is E2E-01's fs precursor (plan
// 9.5; brief 2.4 steps 1–5 and 7): the whole sequence through the built
// bin/brigade and bin/brigade-adapter-fs as real processes.
//
//  1. `brigade hook session-start` for alice, with the hook stdin and the
//     session environment (options through CLAUDE_PLUGIN_OPTION_*, the
//     adapter as a JSON array with --root, a fake inbox socket and a
//     token): the context line names the team and the name; the by-pid
//     and by-native maps and the pidfile exist; the REAL watcher is
//     running detached and its pidfile is alive.
//  2. bob sends through `brigade send` from his OWN session (a second
//     sleeper, its own map): the frame arrives at the fake socket wrapped
//     in variant C with the right ids after one auth line carrying the
//     token, and the fs store has acknowledged it.
//  3. alice's reply through `send --reply-to` is hop 1 in the store.
//  4. `hook prompt` prints nothing extra; the watcher crashes (SIGKILL:
//     its pidfile stays, dead; the session stays open) → the next `hook
//     prompt` respawns it (a new pid in the pidfile, a second `watch
//     ready`), and the session is still open right before step 5.
//  5. `hook session-end` (reason other): the watcher exits, the pidfile
//     and the by-pid map are gone, the session is closed in the store.
//  7. U-25: the token is in no file under the XDG triple, the Claude
//     config dir and the fs store, and on no adapter child's argv or in
//     its environment (the wrapper recorded every child).
func TestSessionLifecycleThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()

	sock := fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", aliceName, sock.Path(), token)
	bob := r.newSession("bob", bobName, "", "", "")

	// --- 1. session-start: the maps, the pidfile, the live watcher -----
	res := r.hook(alice, "session-start", alice.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	m := r.mustMap(alice)
	if m.BrigadeSessionID == "" || m.TeamRef != r.teamRef || m.TeamName != teamName || m.SessionName != aliceName ||
		m.Profile != "alice" || m.SocketPath != sock.Path() || m.Inbound != "accept" || m.HarnessVersion != "2.1.259" ||
		m.PluginBin != r.pluginBin || m.ConfigDir != r.configDir || strings.Join(m.AdapterCommand, " ") != strings.Join(r.adapterCmd, " ") {
		t.Fatalf("alice's map: %+v", m)
	}
	wantLine := "Brigade: this session is \"" + aliceName + "\" (" + m.BrigadeSessionID + ") in team \"" + teamName +
		"\"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`; terminal commands: " + r.pluginBin + "\n"
	if res.stdout != wantLine {
		t.Errorf("session-start stdout:\n got %q\nwant %q", res.stdout, wantLine)
	}
	if bn, err := r.store().ReadByNative(alice.nativeID); err != nil || bn.BrigadeSessionID != m.BrigadeSessionID {
		t.Errorf("by-native map: %+v %v", bn, err)
	}
	entry := r.liveWatcher(alice)
	if entry.BrigadeSessionID != m.BrigadeSessionID || entry.SocketPath != sock.Path() || entry.TokenSHA256 != pidfile.TokenSHA256(token) {
		t.Fatalf("pidfile: %+v", entry)
	}
	first := entry.PID
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(alice, "watch ready") })
	if s := r.sessionFile(m.BrigadeSessionID); s.SessionName != aliceName || s.Inbound != "accept" || s.ClosedAt != nil {
		t.Errorf("store session: %+v", s)
	}

	// --- 2. bob's own session sends; the frame reaches the socket -------
	r.hook(bob, "session-start", bob.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	bobMap := r.mustMap(bob)
	if bobMap.SessionName != bobName || bobMap.Profile != "bob" || bobMap.SocketPath != "" {
		t.Fatalf("bob's map: %+v", bobMap)
	}
	if _, err := os.Lstat(r.pidfilePath(bob)); err == nil {
		t.Fatal("a watcher was started for a session without a socket")
	}
	body := "the tenant_id migration has landed\nsecond line"
	res = r.mustRun(r.env(bob), body, "send", m.BrigadeSessionID, "--summary", "migration completed", "--json")
	sent := jsonResult[sendResult](t, res.stdout)
	if sent.MessageID == "" || sent.RecipientSessionID != m.BrigadeSessionID || sent.HopCount != 0 || sent.Duplicate {
		t.Fatalf("send: %+v", sent)
	}
	frames := sock.WaitFrames(1, waitLong)
	p, err := frame.Parse(frames[0])
	if err != nil {
		t.Fatalf("the injected frame does not parse: %v\n%s", err, frames[0])
	}
	if !p.Wrapped || p.WrapperFromName != bobName || p.MessageID != sent.MessageID || p.ReplyToSessionID != bobMap.BrigadeSessionID ||
		p.Team != teamName || p.FromName != bobName || p.FromLabel != bobLabel+" (unverified)" || p.Hops != "0" ||
		p.Summary != "migration completed" || p.Body != body {
		t.Errorf("frame: %+v\n%s", p, frames[0])
	}
	conns := sock.Connections()
	if len(conns) != 1 || len(conns[0].Auth) != 1 || conns[0].Auth[0] != token || len(conns[0].Invalid) != 0 {
		t.Errorf("connections: %+v, want one with the token on its auth line", conns)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return len(r.storeIDs("acked", m.BrigadeSessionID)) == 1 && len(r.storeIDs("inbox", m.BrigadeSessionID)) == 0
	})
	if got := r.storeIDs("acked", m.BrigadeSessionID); got[0] != sent.MessageID {
		t.Errorf("acked ids = %v, want %s", got, sent.MessageID)
	}

	// --- 3. the reply is hop 1 in the store -------------------------------
	res = r.mustRun(r.env(alice), "on it", "send", bobMap.BrigadeSessionID, "--reply-to", sent.MessageID, "--json")
	reply := jsonResult[sendResult](t, res.stdout)
	if reply.HopCount != 1 {
		t.Errorf("reply hop_count = %d, want 1", reply.HopCount)
	}
	inbox := r.storeMessages("inbox", bobMap.BrigadeSessionID)
	if len(inbox) != 1 || inbox[0].MessageID != reply.MessageID || inbox[0].HopCount != 1 || inbox[0].ReplyTo == nil || *inbox[0].ReplyTo != sent.MessageID ||
		inbox[0].Sender.SessionID != m.BrigadeSessionID {
		t.Errorf("bob's inbox: %+v", inbox)
	}

	// --- 4. prompt prints nothing; a dead watcher is respawned -----------
	res = r.hook(alice, "prompt", alice.hookDoc("UserPromptSubmit", map[string]any{"permission_mode": "acceptEdits", "prompt": "never read"}))
	if res.stdout != "" {
		t.Errorf("prompt stdout = %q, want nothing", res.stdout)
	}
	if got := r.mustMap(alice).PermissionMode; got != "acceptEdits" {
		t.Errorf("permission_mode = %q after the prompt, want acceptEdits", got)
	}
	if v, err := pidfile.Check(r.pidfilePath(alice), procutil.Lookup); err != nil || v.Entry.PID != first {
		t.Fatalf("the prompt hook replaced a live watcher: %+v %v", v, err)
	}
	// The watcher crashes (SIGKILL): no exit path, so its pidfile stays
	// behind naming a dead pid and the session stays open in the store.
	crash(t, first)
	if v, err := pidfile.Check(r.pidfilePath(alice), procutil.Lookup); err != nil || !v.Found || v.Alive || v.Entry.PID != first {
		t.Fatalf("after the crash the pidfile should still name the dead watcher %d: %+v %v", first, v, err)
	}
	res = r.hook(alice, "prompt", alice.hookDoc("UserPromptSubmit", map[string]any{"permission_mode": "acceptEdits"}))
	if res.stdout != "" || !strings.Contains(res.stderr, "respawning") {
		t.Errorf("prompt after the watcher died: stdout %q stderr %q", res.stdout, res.stderr)
	}
	second := r.liveWatcher(alice)
	if second.PID == first || second.BrigadeSessionID != m.BrigadeSessionID || second.SocketPath != sock.Path() || second.TokenSHA256 != pidfile.TokenSHA256(token) {
		t.Fatalf("respawned pidfile: %+v (first %d)", second, first)
	}
	// The respawned watcher serves the session: a second `watch ready` in
	// the shared log, and the session is still OPEN in the store, so the
	// closed_at assertion after session-end below is load-bearing.
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logCount(alice, "watch ready") >= 2 })
	if s := r.sessionFile(m.BrigadeSessionID); s.ClosedAt != nil {
		t.Fatalf("the session was closed before session-end: %+v", s)
	}
	if !alive(second.PID) {
		t.Fatalf("the respawned watcher %d is not alive before session-end", second.PID)
	}

	// --- 5. session-end: the watcher exits, the state is gone ---------------
	sent5 := time.Now()
	res = r.hook(alice, "session-end", alice.hookDoc("SessionEnd", map[string]any{"reason": "other"}))
	if res.stdout != "" {
		t.Errorf("session-end stdout = %q", res.stdout)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(second.PID) })
	t.Logf("the watcher was gone %s after session-end", time.Since(sent5))
	if _, err := os.Lstat(r.pidfilePath(alice)); err == nil {
		t.Error("the pidfile survived session-end")
	}
	if r.mapExists(alice) {
		t.Error("the by-pid map survived session-end")
	}
	if bn, err := r.store().ReadByNative(alice.nativeID); err != nil || bn.BrigadeSessionID != m.BrigadeSessionID {
		t.Errorf("the by-native map must survive session-end: %+v %v", bn, err)
	}
	if s := r.sessionFile(m.BrigadeSessionID); s.ClosedAt == nil {
		t.Errorf("the session was not closed in the store: %+v", s)
	}
	r.hook(bob, "session-end", bob.hookDoc("SessionEnd", map[string]any{"reason": "other"}))

	// --- 7. U-25 -----------------------------------------------------------
	r.assertTokenNowhere(token)
}

// TestWatchSinkThroughTheRealBinary is brief 2.4 step 6: through the real
// binary the sink is an argv flag of `watch`, so the sink variant runs
// `bin/brigade watch --sink <file>` directly with the hook-built
// environment and no socket variable, and asserts the record. `--sink`
// with the socket variable set is refused with `usage` first.
func TestWatchSinkThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	carol := r.newSession("alice", "", aliceName, "", "")
	bob := r.newSession("bob", bobName, "", "", "")

	// A socketless session: the hook writes the map and starts nothing.
	res := r.hook(carol, "session-start", carol.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	m := r.mustMap(carol)
	if !strings.Contains(res.stdout, "("+m.BrigadeSessionID+")") || m.SocketPath != "" {
		t.Fatalf("session-start: %q map %+v", res.stdout, m)
	}
	if _, err := os.Lstat(r.pidfilePath(carol)); err == nil {
		t.Fatal("a watcher was started without a socket")
	}

	// The hook-built watcher environment (6.6), as the hook would build
	// it for this map, with the sink flag and no socket variable.
	adapter, err := config.DecodeAdapter(r.adapterJSON())
	if err != nil {
		t.Fatal(err)
	}
	vars, err := config.WatcherEnv{
		ClaudePID: carol.pid, Profile: "alice", ConfigDir: r.configDir, StateDir: r.stateDir,
		Adapter: adapter, TeamInbound: config.InboundAccept,
	}.Vars()
	if err != nil {
		t.Fatal(err)
	}
	env := append(r.baseEnv(), "BRIGADE_LOG_LEVEL=debug")
	env = append(env, vars...)
	sink := filepath.Join(r.dirs.Root, "sink.ndjson")

	// --sink with a socket variable is usage (exit 2), before anything runs.
	refused := r.run(append(env, watch.SocketVar+"="+filepath.Join(r.dirs.Root, "never.sock")), "", "watch", "--sink", sink)
	if refused.exit != 2 || !strings.HasPrefix(refused.stderr, "brigade watch failed (usage): --sink is refused") || refused.stdout != "" {
		t.Fatalf("watch --sink with the socket variable: %+v", refused)
	}
	if _, err := os.Lstat(sink); err == nil {
		t.Fatal("the refused watcher created the sink")
	}

	stdout := filepath.Join(r.dirs.Root, "watch.stdout")
	d := r.startDetached(env, stdout, filepath.Join(r.dirs.Root, "watch.stderr"), "watch", "--sink", sink)
	r.track(d.pid())
	entry := r.liveWatcher(carol)
	if entry.PID != d.pid() || entry.BrigadeSessionID != m.BrigadeSessionID || entry.SocketPath != "" || entry.TokenSHA256 != "" {
		t.Fatalf("pidfile: %+v (watcher %d)", entry, d.pid())
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(carol, "watch ready") })

	// bob sends from his own session; the record lands in the sink.
	r.hook(bob, "session-start", bob.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	bobMap := r.mustMap(bob)
	res = r.mustRun(r.env(bob), "to the sink", "send", m.BrigadeSessionID, "--json")
	sent := jsonResult[sendResult](t, res.stdout)
	var records []sinkRecord
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		records = readSink(t, sink)
		return len(records) >= 1
	})
	rec := records[0]
	if len(records) != 1 || rec.MessageID != sent.MessageID || rec.SenderSessionID != bobMap.BrigadeSessionID || rec.TS.IsZero() {
		t.Fatalf("sink records: %+v", records)
	}
	p, err := frame.Parse(rec.Frame)
	if err != nil || !p.Wrapped || p.MessageID != sent.MessageID || p.ReplyToSessionID != bobMap.BrigadeSessionID || p.Body != "to the sink" {
		t.Errorf("sink frame: %+v %v\n%s", p, err, rec.Frame)
	}
	if fi, err := os.Stat(sink); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("sink mode: %v %v", fi, err)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return len(r.storeIDs("acked", m.BrigadeSessionID)) == 1 })

	// SIGTERM ends it with exit 0, the pidfile gone and the session closed.
	if exit := d.stop(t); exit != 0 {
		t.Errorf("watcher exit = %d after SIGTERM (%v)", exit, d.err)
	}
	if _, err := os.Lstat(r.pidfilePath(carol)); err == nil {
		t.Error("the pidfile survived the watcher's exit")
	}
	if s := r.sessionFile(m.BrigadeSessionID); s.ClosedAt == nil {
		t.Errorf("the session was not closed: %+v", s)
	}
	if out, err := os.ReadFile(stdout); err != nil || len(out) != 0 {
		t.Errorf("the watcher wrote to stdout: %q (%v)", out, err)
	}
	if !r.logHas(carol, "watcher exiting") {
		t.Error("no `watcher exiting` line in the log")
	}
	r.hook(carol, "session-end", carol.hookDoc("SessionEnd", map[string]any{"reason": "other"}))
	r.hook(bob, "session-end", bob.hookDoc("SessionEnd", map[string]any{"reason": "other"}))
	if r.mapExists(carol) || r.mapExists(bob) {
		t.Error("a by-pid map survived session-end")
	}
	// No token in this variant; the recorder is still asserted live and
	// the CLAUDE_CODE_MESSAGING_* names absent from every adapter child.
	r.assertTokenNowhere("no-token-in-sink-mode-" + strconv.Itoa(carol.pid))
}

// sinkRecord is one line of the --sink file (6.6).
type sinkRecord struct {
	TS              time.Time `json:"ts"`
	Frame           string    `json:"frame"`
	MessageID       string    `json:"message_id"`
	SenderSessionID string    `json:"sender_session_id"`
}

// readSink parses the sink file; a missing file is empty.
func readSink(t *testing.T, path string) []sinkRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []sinkRecord
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec sinkRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("sink line: %v", err)
		}
		out = append(out, rec)
	}
	return out
}
