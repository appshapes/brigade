//go:build !windows

package hook

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// fakeadapterResponse is the scripted fake adapter's response type.
type fakeadapterResponse = fakeadapter.Response

// codexDoc is a hook document as Codex CLI 0.163.0 writes it (measured,
// docs/experiments/codex-queue-participation.md §5): session_id, cwd,
// transcript_path (the rollout), hook_event_name, model, permission_mode;
// SessionStart adds source. The thread id is the document's; the hook's
// environment carries none.
func (f *fixture) codexDoc(event, source string) string {
	m := map[string]any{
		"session_id": f.nativeID, "cwd": f.cwd, "hook_event_name": event,
		"permission_mode": "danger-full-access", "model": "gpt-5.6-sol", "transcript_path": "/never/read/rollout.jsonl",
	}
	if source != "" {
		m["source"] = source
	}
	if event == "UserPromptSubmit" {
		m["prompt"], m["turn_id"] = "never read", "turn-1"
	}
	return f.doc(m)
}

// TestCodexSessionStart is the Codex CLI counterpart of TestMuseSessionStart
// (card 107): the session is keyed by the thread-derived key with the hook's
// parent kept as the liveness process (D2), registered as harness codex
// with the document's model, the by-host pointer is written for the
// shell's CODEX_THREAD_ID, the watcher is spawned in `--host codex` mode
// with no sink and no socket, the context line is followed by the Codex
// line naming the bootstrap, nothing under the Claude config directory is
// read or written, and no CODEX_* reaches a child.
func TestCodexSessionStart(t *testing.T) {
	t.Parallel()
	f := newCodexFixture(t)
	watcher := testutil.NewSleeper(t)
	f.spawner.watcherPID = watcher
	claudeReads := 0
	f.deps.ReadFile = func(path string) ([]byte, error) {
		if strings.HasPrefix(path, f.dirs.ClaudeConfig) {
			claudeReads++
		}
		return os.ReadFile(path)
	}
	f.deps.WriteFile = func(path string, _ []byte, _ os.FileMode) error {
		t.Errorf("a Codex session wrote %s", path)
		return nil
	}
	exit, out, errOut := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup"), "CODEX_HOME="+f.dirs.Home+"/.codex")
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	got := lines(out)
	if len(got) != 2 {
		t.Fatalf("stdout %q, want the context line and the Codex line", out)
	}
	if want := "Brigade: this session is \"checkout\" (brigade-sess-1) in team \"ops\"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`."; got[0] != want {
		t.Fatalf("line 1 %q\nwant   %q", got[0], want)
	}
	if !strings.HasPrefix(got[1], "Brigade: a team message reaches this session as a turn of its own") ||
		!strings.Contains(got[1], f.pluginBin+" send") || !strings.Contains(got[1], "The brigade command is "+f.pluginBin) ||
		strings.Contains(got[1], "Monitor") {
		t.Fatalf("codex line %q", got[1])
	}
	if claudeReads != 0 {
		t.Fatalf("a Codex session read %d file(s) under the Claude config directory", claudeReads)
	}
	if got := f.dumpVerbs(); strings.Join(got, ",") != "describe,session register" {
		t.Fatalf("adapter children %q", got)
	}
	for _, inv := range f.dump() {
		for name := range inv.Env {
			if strings.HasPrefix(name, "CODEX_") || name == "CLAUDE_PID" || strings.HasPrefix(name, "CLAUDE_CODE_MESSAGING_") {
				t.Errorf("adapter child %s %s received %s", inv.Group, inv.Verb, name)
			}
		}
	}
	m := f.mustMap()
	key := codexKey(f.nativeID)
	if key <= 1<<30 || key >= 1<<31 || key != codexKey(f.nativeID) || key == codexKey("another-thread") {
		t.Fatalf("codexKey %d is not a stable number above every real pid", key)
	}
	switch {
	case m.Harness != string(config.HostCodex), m.Model != "gpt-5.6-sol", m.ClaudePID != key, m.HostProcessPID != f.pid,
		m.LivenessPID() != f.pid, m.ClaudeSessionID != f.nativeID, m.BrigadeSessionID != "brigade-sess-1", m.SocketPath != "",
		m.TranscriptPath != "/never/read/rollout.jsonl", m.PermissionMode != "danger-full-access", m.Inbound != "accept",
		m.PluginBin != f.pluginBin, m.SessionName != "checkout", m.HarnessVersion != "unknown", m.Entrypoint != "", m.NonInteractive:
		t.Fatalf("map %+v", *m)
	}
	ptr, err := f.store().ReadByHost(string(config.HostCodex), f.nativeID)
	if err != nil || ptr.HostPID != key || ptr.BrigadeSessionID != "brigade-sess-1" {
		t.Fatalf("by-host pointer %+v %v", ptr, err)
	}
	if _, err := f.store().ReadByHost(string(config.HostMuse), f.nativeID); err == nil {
		t.Fatal("a Codex session wrote a Muse pointer")
	}
	if f.spawner.count() != 1 {
		t.Fatalf("watcher spawns %d, want 1", f.spawner.count())
	}
	spec := f.spawner.last(t)
	if len(spec.Args) != 3 || spec.Args[0] != "watch" || spec.Args[1] != "--host" || spec.Args[2] != "codex" {
		t.Fatalf("spec args %q", spec.Args)
	}
	env := strings.Join(spec.Env, "\n")
	for _, e := range spec.Env {
		if strings.HasPrefix(e, "CLAUDE_CODE_MESSAGING_") || strings.HasPrefix(e, "CODEX_THREAD_ID=") {
			t.Errorf("watcher env carries %s", e)
		}
	}
	if !strings.Contains(env, "BRIGADE_CLAUDE_PID="+strconv.Itoa(key)+"\n") {
		t.Fatalf("watcher env lacks the session's key: %v", spec.Env)
	}
	// The user's CODEX_HOME rides along, so `codex queue` finds the daemon
	// the TUI uses (card 107).
	if !strings.Contains(env+"\n", "CODEX_HOME="+f.dirs.Home+"/.codex\n") {
		t.Fatalf("watcher env lacks CODEX_HOME: %v", spec.Env)
	}

	// The Codex SessionEnd (D8): the watcher is signalled and every file of
	// the session is removed within the hook's second; no adapter call.
	exit, _, errOut = f.run(SubSessionEnd, f.codexDoc("SessionEnd", ""))
	if exit != 0 {
		t.Fatalf("session-end exit %d: %s", exit, errOut)
	}
	if got := f.dumpVerbs(); strings.Join(got, ",") != "describe,session register" {
		t.Fatalf("adapter children after session-end %q: a Codex session-end makes no adapter call", got)
	}
	if f.mapExists() {
		t.Fatal("the map survived session-end")
	}
	if _, err := f.store().ReadByHost(string(config.HostCodex), f.nativeID); err == nil {
		t.Fatal("the by-host pointer survived session-end")
	}
}

// TestCodexWatcherEnvWithoutCodexHome: a hook environment without
// CODEX_HOME passes none, and a relative one is not passed either.
func TestCodexWatcherEnvWithoutCodexHome(t *testing.T) {
	t.Parallel()
	for _, extra := range [][]string{nil, {"CODEX_HOME=relative/.codex"}} {
		f := newCodexFixture(t)
		f.spawner.watcherPID = testutil.NewSleeper(t)
		if exit, _, errOut := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup"), extra...); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		for _, e := range f.spawner.last(t).Env {
			if strings.HasPrefix(e, "CODEX_HOME=") {
				t.Fatalf("watcher env carries %s with extra %v", e, extra)
			}
		}
	}
}

// TestCodexNewThreadIsANewSession pins D2 and D3 (card 107): Codex's /new
// opens a new thread under the same parent (the TUI in embedded mode, the
// shared daemon in daemon mode), and its first prompt fires a SessionStart
// with a new session_id. Two threads are two keys: a second registration,
// a second map, a second pointer and a second watcher, and the first
// thread's state stands until Codex ends that thread (its SessionEnd) —
// nothing of the first session is reused or retired by the second.
func TestCodexNewThreadIsANewSession(t *testing.T) {
	t.Parallel()
	f := newCodexFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	first := f.nativeID
	if exit, _, errOut := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	firstMap := f.mustMap()
	f.nativeID = "019a2b3c-4d5e-7f80-9a1b-2c3d4e5f6071"
	f.script.Responses["session register"] = []fakeadapterResponse{okResp(registerDoc("brigade-sess-2", "work", false))}
	f.spawner.watcherPID = testutil.NewSleeper(t)
	exit, out, errOut := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup"))
	if exit != 0 {
		t.Fatalf("second exit %d: %s", exit, errOut)
	}
	// One describe (cached per script path across the process), two
	// registrations: the new thread is a new session, not a continuation.
	if got := f.dumpVerbs(); strings.Join(got, ",") != "describe,session register,session register" {
		t.Fatalf("adapter children %q: the new thread is a new session", got)
	}
	if !strings.Contains(out, "(brigade-sess-2)") {
		t.Fatalf("second start printed %q", out)
	}
	second := f.mustMap()
	if second.ClaudePID == firstMap.ClaudePID || second.ClaudeSessionID != f.nativeID || second.HostProcessPID != f.pid {
		t.Fatalf("second map %+v", *second)
	}
	for id, key := range map[string]int{first: firstMap.ClaudePID, f.nativeID: second.ClaudePID} {
		ptr, err := f.store().ReadByHost(string(config.HostCodex), id)
		if err != nil || ptr.HostPID != key {
			t.Fatalf("pointer of %s: %+v %v, want key %d", id, ptr, err, key)
		}
	}
	if f.spawner.count() != 2 {
		t.Fatalf("watcher spawns %d, want one per thread", f.spawner.count())
	}
}

// TestCodexHookNeedsAUsableThreadID: a document whose session_id cannot
// name a session (a path separator) keys nothing; the hook prints the
// not-connected line and spawns no watcher.
func TestCodexHookNeedsAUsableThreadID(t *testing.T) {
	t.Parallel()
	f := newCodexFixture(t)
	f.nativeID = "../" + evilThread
	exit, out, _ := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup"))
	if exit != 0 || !strings.HasPrefix(out, "Brigade: not connected (config)") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if strings.Contains(out, evilThread) {
		t.Fatalf("the id was echoed: %q", out)
	}
	if f.spawner.count() != 0 {
		t.Fatal("a watcher was spawned for an unkeyable session")
	}
	// The prompt and end hooks on the same document: exit 0, nothing done.
	for _, sub := range []string{SubPrompt, SubSessionEnd} {
		event := "UserPromptSubmit"
		if sub == SubSessionEnd {
			event = "SessionEnd"
		}
		if exit, out, _ := f.run(sub, f.codexDoc(event, "")); exit != 0 || out != "" {
			t.Fatalf("%s exit %d out %q", sub, exit, out)
		}
	}
}

const evilThread = "evil-thread-id-must-not-be-echoed"

// TestCodexPromptKeepsAccept pins D5 for Codex (card 107): no Claude
// settings scan, no parity rule, nothing written under the Claude config
// directory, the policy at accept, and the watcher kept alive by the
// prompt hook — which fires on queued turns too.
func TestCodexPromptKeepsAccept(t *testing.T) {
	t.Parallel()
	f := newCodexFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.deps.WriteFile = func(path string, _ []byte, _ os.FileMode) error {
		t.Errorf("a Codex session wrote %s", path)
		return nil
	}
	if exit, _, errOut := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	exit, out, errOut := f.run(SubPrompt, f.codexDoc("UserPromptSubmit", ""))
	if exit != 0 {
		t.Fatalf("prompt exit %d: %s", exit, errOut)
	}
	for _, l := range lines(out) {
		if strings.Contains(l, "refuse") || strings.Contains(l, "crossSessionInbound") {
			t.Fatalf("the prompt printed a parity line on Codex: %q", l)
		}
	}
	if m := f.mustMap(); m.Inbound != "accept" || m.PermissionMode != "danger-full-access" {
		t.Fatalf("map after prompt %+v", *m)
	}
	if f.spawner.count() != 1 {
		t.Fatalf("spawns %d: the live watcher stays", f.spawner.count())
	}
}

// TestCodexOptionsFileReachesTheHook pins D4 (card 107): a
// codex-options.json under HOME/.config/brigade sets the options, and the
// Muse file is not read for a Codex session.
func TestCodexOptionsFileReachesTheHook(t *testing.T) {
	t.Parallel()
	f := newCodexFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.noAdapterOption = true
	f.deps.ReadFile = os.ReadFile
	if err := os.MkdirAll(f.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	optionText, err := json.Marshal(f.adapterOption())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"adapter_command": ` + string(optionText) + `, "team_inbound": "hold"}`
	if err := os.WriteFile(filepath.Join(f.configDir, config.CodexOptionsFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// A Muse file beside it says refuse; a Codex session must not read it.
	if err := os.WriteFile(filepath.Join(f.configDir, config.MuseOptionsFile), []byte(`{"team_inbound": "refuse"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, out, errOut := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.Inbound != "hold" {
		t.Fatalf("the options file did not reach the hook: inbound %q (stdout %q)", m.Inbound, out)
	}
}

// TestCodexFlagNeverRelabelsAClaudeSession: with CLAUDE_PID in the
// environment the `--host codex` flag changes nothing — CLAUDE_PID wins in
// HostOf, the session registers as Claude Code, and no pointer is written.
func TestCodexFlagNeverRelabelsAClaudeSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.writeScript()
	var out, errOut bytes.Buffer
	args := []string{SubSessionStart, "--host", "codex", "--plugin-root", f.dirs.PluginRoot}
	exit := Run(args, cli.Streams{In: strings.NewReader(f.startDoc("startup")), Out: &out, Err: &errOut}, f.env(), f.deps)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut.String())
	}
	if m := f.mustMap(); m.Harness != "" {
		t.Fatalf("a Claude session registered as %q", m.Harness)
	}
	if _, err := f.store().ReadByHost(string(config.HostCodex), f.nativeID); err == nil {
		t.Fatal("a Claude session wrote a Codex pointer")
	}
}
