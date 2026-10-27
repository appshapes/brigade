//go:build !windows

package hook

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

func grokFixture(t *testing.T) *fixture {
	t.Helper()
	f := buildFixture(t, config.HostGrok)
	f.deps.ReadFile = os.ReadFile
	base := f.deps.Lookup
	f.deps.Lookup = func(pid int) (procutil.Info, error) {
		info, err := base(pid)
		if pid == f.pid {
			info.Command = "grok"
			if runtime.GOOS == "darwin" {
				info.Command = "grok-macos-aarch"
			}
		}
		return info, err
	}
	f.spawner.watcherPID = testutil.NewSleeper(t)
	body, _ := json.Marshal(map[string]string{"adapter_command": f.adapterOption(), "sync": "off"})
	if err := os.MkdirAll(f.configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.configDir, config.GrokOptionsFile), body, 0600); err != nil {
		t.Fatal(err)
	}
	return f
}
func grokDoc(f *fixture, event, source string, extra map[string]any) string {
	d := map[string]any{"sessionId": f.nativeID, "cwd": f.cwd, "workspaceRoot": f.cwd, "hookEventName": event, "source": source, "permissionMode": "default"}
	for k, v := range extra {
		d[k] = v
	}
	b, _ := json.Marshal(d)
	return string(b)
}
func grokRun(f *fixture, sub, doc string, extra ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	env := []string{"HOME=" + f.dirs.Home, "PATH=/usr/bin:/bin", "GROK_SESSION_ID=" + f.nativeID, "GROK_WORKSPACE_ROOT=" + f.cwd, "GROK_PLUGIN_ROOT=" + f.dirs.PluginRoot}
	env = append(env, extra...)
	f.writeScript()
	code := Run([]string{sub, "--host", "grok", "--plugin-root", f.dirs.PluginRoot}, cli.Streams{In: strings.NewReader(doc), Out: &out, Err: &errOut}, env, f.deps)
	return code, out.String(), errOut.String()
}
func TestGrokLifecycleAndBoundedStop(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	code, out, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	if code != 0 {
		t.Fatalf("hook exit %d", code)
	}
	var ui map[string]string
	if json.Unmarshal([]byte(out), &ui) != nil || !strings.Contains(ui["systemMessage"], "UI-only") {
		t.Fatalf("startup stdout %q stderr %q", out, errOut)
	}
	m := f.mustMap()
	if !m.ValidGrokReceiver() || m.SocketPath != "" || m.TranscriptPath != "" || m.Model != "" || m.Grok.MonitorState != "unarmed" {
		t.Fatalf("map %+v", m)
	}
	if got := f.spawner.last(t).Args; len(got) != 3 || got[1] != "--sink" {
		t.Fatalf("watcher args %v", got)
	}
	stop := grokDoc(f, "stop", "", map[string]any{"stopReason": "end_turn"})
	_, out, _ = grokRun(f, "stop", stop)
	var block map[string]string
	if json.Unmarshal([]byte(out), &block) != nil || block["decision"] != "block" || !strings.Contains(block["reason"], m.MonitorCommand()) {
		t.Fatalf("Stop %q", out)
	}
	for _, want := range []string{
		"installed /brigade:team-messaging skill",
		"every page and every record until caught_up:true",
		"including after frame_complete:true",
		"cannot verify the command or readiness",
		"one bounded inbox read per tool call",
		"entire unfiltered response, including every frame_chunk, in model context before requesting the next page",
		"Do not use shell/script loops, batching, piping/filtering, metadata-only summaries or temporary-file/grep/regex shortcuts",
		"Subprocess retrieval alone is not model reading",
		"handle a frame only after reading all its pages",
	} {
		if !strings.Contains(block["reason"], want) {
			t.Fatalf("Stop omitted %q: %q", want, out)
		}
	}
	for _, obsolete := range []string{"wake_delay_ms 0", "opaque wake token"} {
		if strings.Contains(block["reason"], obsolete) {
			t.Fatalf("Stop retained obsolete %q: %q", obsolete, out)
		}
	}
	for i := 0; i < 3; i++ {
		_, out, _ = grokRun(f, "stop", stop)
		if out != "" {
			t.Fatalf("repeated automatic attempt %q", out)
		}
	}
	paused := f.mustMap()
	if paused.Grok.MonitorState != "unverified" {
		t.Fatalf("denial state %s", paused.Grok.MonitorState)
	}
	task := grokTask{ID: "own-task", Type: "monitor", Status: "running", Command: m.MonitorCommand(), Description: m.MonitorDescription()}
	_, out, _ = grokRun(f, "stop", grokDoc(f, "stop", "", map[string]any{"backgroundTasks": []grokTask{task}}))
	if out != "" || f.mustMap().Grok.MonitorState != "armed" {
		t.Fatalf("native monitor not observed: %q", out)
	}
	_, out, _ = grokRun(f, SubPrompt, grokDoc(f, "user_prompt_submit", "", nil))
	if out != "" {
		t.Fatalf("prompt leaked stdout %q", out)
	}
	// An actual load gets a fresh incarnation and exactly one new opportunity.
	// Unread sink files of the same Brigade session move with it.
	f.spawner.watcherPID = testutil.NewSleeper(t)
	oldDir := inbound.GrokSinkDir(f.stateDir, m.BrigadeSessionID, m.Grok.Incarnation)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kept := []byte("{\"message_id\":\"kept\"}\n")
	if err := os.WriteFile(filepath.Join(oldDir, "00000000000000000001.ndjson"), kept, 0o600); err != nil {
		t.Fatal(err)
	}
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "load", nil))
	next := f.mustMap()
	if next.Grok.Incarnation == m.Grok.Incarnation {
		t.Fatal("load kept old incarnation")
	}
	if next.BrigadeSessionID != m.BrigadeSessionID {
		t.Fatalf("load session %s, want %s", next.BrigadeSessionID, m.BrigadeSessionID)
	}
	moved, err := os.ReadFile(filepath.Join(inbound.GrokSinkDir(f.stateDir, next.BrigadeSessionID, next.Grok.Incarnation), "00000000000000000001.ndjson"))
	if err != nil || string(moved) != string(kept) {
		t.Fatalf("backlog %q %v", moved, err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("old incarnation directory remained")
	}
	_, out, _ = grokRun(f, "stop", stop)
	if !strings.Contains(out, `"decision":"block"`) {
		t.Fatalf("resume did not allow setup %q", out)
	}
	_, out, _ = grokRun(f, SubSessionEnd, grokDoc(f, "session_end", "", nil))
	if out != "" || f.mapExists() {
		t.Fatalf("end stdout %q map %v", out, f.mapExists())
	}
}

// A move that fails partway must not strand the files it already renamed
// under an incarnation no reader resolves: they go back, and the previous
// map comes back with them.
func TestGrokFailedBacklogAdoptionRestoresTheOldDirectory(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	if code, _, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil)); code != 0 {
		t.Fatalf("hook exit %d: %s", code, errOut)
	}
	m := f.mustMap()
	oldDir := inbound.GrokSinkDir(f.stateDir, m.BrigadeSessionID, m.Grok.Incarnation)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kept := []byte("{\"message_id\":\"kept\"}\n")
	// The first entry moves; the second cannot, so the adoption fails with
	// one file already sitting under the new incarnation.
	if err := os.WriteFile(filepath.Join(oldDir, "00000000000000000001.ndjson"), kept, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(oldDir, "00000000000000000002.ndjson")); err != nil {
		t.Fatal(err)
	}
	f.spawner.watcherPID = testutil.NewSleeper(t)
	_, out, _ := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "load", nil))
	if !strings.Contains(out, "not connected (config)") {
		t.Fatalf("failed adoption stdout %q", out)
	}
	back, err := os.ReadFile(filepath.Join(oldDir, "00000000000000000001.ndjson"))
	if err != nil || string(back) != string(kept) {
		t.Fatalf("restored backlog %q %v", back, err)
	}
	still := f.mustMap()
	if !still.ValidGrokReceiver() || still.Grok.Incarnation != m.Grok.Incarnation {
		t.Fatalf("map %+v, want incarnation %s", still, m.Grok.Incarnation)
	}
	entries, err := os.ReadDir(inbound.SinkDir(f.stateDir, m.BrigadeSessionID))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "grok-"+m.Grok.Incarnation {
			t.Fatalf("directory %q of an unpublished incarnation remained", e.Name())
		}
	}
}

func TestGrokClaimDoesNotStealOnLookupError(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	m := f.mustMap()
	other := *m
	other.ClaudePID = m.ClaudePID + 1
	if other.HostProcessPID != 0 {
		other.HostProcessPID = other.ClaudePID
	}
	g := *m.Grok
	other.Grok = &g
	if err := f.store().WriteByPID(&other); err != nil {
		t.Fatal(err)
	}
	if err := f.store().WriteByHost("grok", m.ClaudeSessionID, &sessionmap.ByHost{HostPID: other.ClaudePID, BrigadeSessionID: m.BrigadeSessionID}); err != nil {
		t.Fatal(err)
	}
	base := f.deps.Lookup
	f.deps.Lookup = func(pid int) (procutil.Info, error) {
		if pid == other.ClaudePID {
			return procutil.Info{}, errors.New("lookup failed")
		}
		return base(pid)
	}
	_, out, _ := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	if !strings.Contains(out, "could not be checked") {
		t.Fatalf("stdout %q", out)
	}
	still, err := f.store().ReadByPID(other.ClaudePID)
	if err != nil || still.Grok.Incarnation != other.Grok.Incarnation {
		t.Fatal("lookup error let a new TUI steal the UUID")
	}
}

func TestGrokAcceptsVersionedInstallerKernelName(t *testing.T) {
	t.Parallel()
	name := "grok-1.0.67-lin"
	if runtime.GOOS == "darwin" {
		name = "grok-1.0.67-maco"
	}
	f := grokFixture(t)
	base := f.deps.Lookup
	f.deps.Lookup = func(pid int) (procutil.Info, error) {
		info, err := base(pid)
		if pid == f.pid {
			info.Command = name
		}
		return info, err
	}
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	if !f.mapExists() {
		t.Fatal("versioned kernel name did not register")
	}
}

func TestGrokRefusesUnboundInputs(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"leader", "collision", "unknown source", "malformed", "alias conflict", "bad native", "subagent", "cwd mismatch", "unknown parent"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := grokFixture(t)
			extra := []string{}
			doc := grokDoc(f, "session_start", "new", nil)
			switch name {
			case "leader":
				extra = append(extra, "GROK_LEADER_SOCKET=/synthetic/leader.sock")
			case "collision":
				extra = append(extra, "CLAUDE_PID=17")
			case "unknown source":
				doc = grokDoc(f, "session_start", "compact", nil)
			case "malformed":
				doc = "{"
			case "alias conflict":
				doc = grokDoc(f, "session_start", "new", map[string]any{"session_id": "other"})
			case "bad native":
				f.nativeID = "not-a-uuid"
				doc = grokDoc(f, "session_start", "new", nil)
			case "subagent":
				doc = grokDoc(f, "session_start", "new", map[string]any{"subagentType": "explore"})
			case "cwd mismatch":
				doc = grokDoc(f, "session_start", "new", map[string]any{"cwd": t.TempDir()})
			case "unknown parent":
				f.deps.Lookup = func(int) (procutil.Info, error) {
					return procutil.Info{Exists: true, Command: "sh", StartToken: "1"}, nil
				}
			}
			_, _, _ = grokRun(f, SubSessionStart, doc, extra...)
			if f.mapExists() || f.spawner.count() != 0 {
				t.Fatal("unbound input registered/spawned")
			}
		})
	}
}

// TestGrokIDNeverRelabelsAClaudeSession: a Grok terminal tool exports
// GROK_SESSION_ID, so a `claude` started from one inherits it (card 120).
// CLAUDE_PID still wins in config.HostOf, so the hooks never take the Grok
// path: the session registers as Claude Code with no receiver, and no Grok
// pointer is written. The nesting is the reason Grok is LAST in the host
// precedence — relabelled, this session would register nothing and receive
// nothing, with only a stderr line to say why.
func TestGrokIDNeverRelabelsAClaudeSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	leaked := "02000000-0000-7000-8000-00000000000a"
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), "GROK_SESSION_ID="+leaked)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	m := f.mustMap()
	if m.Harness != "" || m.Grok != nil {
		t.Fatalf("a Claude session registered as %q with receiver %+v", m.Harness, m.Grok)
	}
	if _, err := f.store().ReadByHost(string(config.HostGrok), leaked); err == nil {
		t.Fatal("a Claude session wrote a Grok pointer")
	}
	if !strings.Contains(out, "Brigade: this session is") {
		t.Fatalf("the Claude session did not connect: %q (stderr %q)", out, errOut)
	}
}

func TestGrokStopHonorsRecursionAndOtherTasks(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	_, out, _ := grokRun(f, "stop", grokDoc(f, "stop", "", map[string]any{"stopHookActive": true}))
	if out != "" {
		t.Fatal(out)
	}
	_, out, _ = grokRun(f, "stop", grokDoc(f, "stop", "", map[string]any{"stopReason": "cancelled"}))
	if out != "" {
		t.Fatal(out)
	}
	// An unrelated running monitor does not suppress this receiver's setup.
	_, out, _ = grokRun(f, "stop", grokDoc(f, "stop", "", map[string]any{"backgroundTasks": []grokTask{{ID: "other", Type: "monitor", Status: "running", Command: "echo other"}}}))
	if !strings.Contains(out, `"decision":"block"`) {
		t.Fatal(out)
	}
}

func TestGrokNativeAliasesAndFailedTaskSettle(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	body, _ := json.Marshal(map[string]any{"session_id": f.nativeID, "cwd": f.cwd, "workspace_root": f.cwd, "hook_event_name": "SessionStart", "source": "new", "permission_mode": "default"})
	grokRun(f, SubSessionStart, string(body))
	m := f.mustMap()
	for _, status := range []string{"running", "failed"} {
		_, out, _ := grokRun(f, "stop", grokDoc(f, "stop", "", map[string]any{"backgroundTasks": []grokTask{{ID: "native-task", Type: "monitor", Status: status, Description: "[monitor] " + m.MonitorDescription()}}}))
		if out != "" {
			t.Fatal("correlated task triggered duplicate setup", out)
		}
		want := "unverified"
		if status == "failed" {
			want = "paused"
		}
		if got := f.mustMap().Grok.MonitorState; got != want {
			t.Fatalf("description-only %s observation became %s", status, got)
		}
	}
	_, out, _ := grokRun(f, "stop", grokDoc(f, "stop", "", nil))
	if out != "" {
		t.Fatal("failed task disappearance triggered an automatic retry", out)
	}
}

func TestGrokExplicitPluginRoot(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	f.writeScript()
	var out, errOut bytes.Buffer
	env := []string{"HOME=" + f.dirs.Home, "PATH=/usr/bin:/bin", "GROK_SESSION_ID=" + f.nativeID, "GROK_WORKSPACE_ROOT=" + f.cwd, "GROK_PLUGIN_ROOT=/wrong-root"}
	code := Run([]string{SubSessionStart, "--host", "grok", "--plugin-root", f.dirs.PluginRoot}, cli.Streams{In: strings.NewReader(grokDoc(f, "session_start", "new", nil)), Out: &out, Err: &errOut}, env, f.deps)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	m := f.mustMap()
	root, err := filepath.EvalSymlinks(f.dirs.PluginRoot)
	if err != nil {
		t.Fatal(err)
	}
	if m.PluginBin != filepath.Join(root, "bin", "brigade") {
		t.Fatalf("plugin binary %q", m.PluginBin)
	}
}

func TestGrokRegistersSummaryTitle(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	enc := watch.GrokEncodedCwd(f.cwd)
	if enc == "" {
		t.Fatal("fixture cwd did not encode")
	}
	dir := filepath.Join(f.dirs.Home, ".grok", "sessions", enc, f.nativeID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"info":{"id":"` + f.nativeID + `"},"generated_title":"payments-api","session_summary":"not the name","last_recap":"not the name"}`
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	m := f.mustMap()
	if m.Grok == nil {
		t.Fatalf("no receiver: %+v", m)
	}
	if m.SessionName != "payments-api" || m.Grok.LaunchCwd != f.cwd {
		t.Fatalf("name %q launch %q document %q", m.SessionName, m.Grok.LaunchCwd, f.cwd)
	}
}

func TestGrokRegistersSummaryModel(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	enc := watch.GrokEncodedCwd(f.cwd)
	if enc == "" {
		t.Fatal("fixture cwd did not encode")
	}
	dir := filepath.Join(f.dirs.Home, ".grok", "sessions", enc, f.nativeID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"info":{"id":"` + f.nativeID + `"},"generated_title":"payments-api","current_model_id":"grok-4.7","session_summary":"not the model"}`
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	seam := f.useSeam(map[string][]fakeadapter.Response{
		"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"session heartbeat": {okResp(heartbeatDoc())},
	})
	code, _, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	calls := seam.callsFor("session register")
	if len(calls) != 1 {
		t.Fatalf("registrations %d", len(calls))
	}
	var reg struct {
		Model             *string `json:"model"`
		ContextUsedTokens *int    `json:"context_used_tokens"`
	}
	if err := json.Unmarshal(calls[0].Stdin, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Model == nil || *reg.Model != "grok-4.7" || reg.ContextUsedTokens != nil {
		t.Fatalf("registration model %+v context %+v", reg.Model, reg.ContextUsedTokens)
	}
	if m := f.mustMap(); m.Model != "" || m.SessionName != "payments-api" {
		t.Fatalf("map model %q name %q", m.Model, m.SessionName)
	}
}

func TestGrokNameStaysGrokWithoutTitle(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	code, _, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	m := f.mustMap()
	if m.SessionName != "grok" {
		t.Fatalf("name %q", m.SessionName)
	}
}

func TestGrokWatcherEnvCarriesAbsoluteGrokHome(t *testing.T) {
	t.Parallel()
	w := config.WatcherEnv{ClaudePID: 4064, Profile: "default", ConfigDir: "/home/u/.config/brigade", StateDir: "/home/u/.local/state/brigade",
		Adapter: config.Adapter{Argv: []string{"/opt/adapter"}}, TeamInbound: config.InboundAccept}
	base := []string{"PATH=/usr/bin", "HOME=/home/u"}
	env, err := watcherEnviron(append(append([]string{}, base...), "GROK_HOME=/opt/grok"), w, "", "", "info", false, true)
	if err != nil || envValue(env, "GROK_HOME") != "/opt/grok" {
		t.Fatalf("%v %v", env, err)
	}
	env, err = watcherEnviron(append(append([]string{}, base...), "GROK_HOME=relative"), w, "", "", "info", false, true)
	if err != nil || countEnv(env, "GROK_HOME") != 0 {
		t.Fatalf("relative: %v %v", env, err)
	}
	env, err = watcherEnviron(append(append([]string{}, base...), "GROK_HOME=/opt/grok"), w, "", "", "info", false, false)
	if err != nil || countEnv(env, "GROK_HOME") != 0 {
		t.Fatalf("other host: %v %v", env, err)
	}
}
