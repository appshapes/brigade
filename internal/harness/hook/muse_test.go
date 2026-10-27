//go:build !windows

package hook

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/testutil"
)

// museStartDoc is a SessionStart document as Muse Code 1.4.10 writes it
// (measured, docs/experiments/muse-participation.md §5): no session_title,
// transcript_path null, a model and a permission_mode.
func (f *fixture) museStartDoc(mode string) string {
	return f.doc(map[string]any{
		"session_id": f.nativeID, "cwd": f.cwd, "hook_event_name": "SessionStart", "source": "startup",
		"permission_mode": mode, "model": "muse-wonder-full-1.0", "transcript_path": nil,
	})
}

// TestMuseSessionStart is the Muse Code counterpart of
// TestSessionStartRegistersSession (card 88): the session is keyed by the
// hook's parent pid, registered as harness muse with the document's model,
// the by-host pointer is written for the shell's MUSE_SESSION_ID, the
// watcher is spawned in sink mode on a fresh per-watcher sink, the context
// line is followed by the arming line naming the bootstrap, and nothing
// under the Claude config directory is read or written.
func TestMuseSessionStart(t *testing.T) {
	t.Parallel()
	f := newMuseFixture(t)
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
		t.Errorf("a Muse session wrote %s", path)
		return nil
	}
	exit, out, errOut := f.run(SubSessionStart, f.museStartDoc("bypassPermissions"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	got := lines(out)
	if len(got) != 2 {
		t.Fatalf("stdout %q, want the context line and the arming line", out)
	}
	// No registry and no session_title on Muse: the name is the checkout's
	// directory name, which the fixture's cwd ends in.
	if want := "Brigade: this session is \"checkout\" (brigade-sess-1) in team \"ops\"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`."; got[0] != want {
		t.Fatalf("line 1 %q\nwant   %q", got[0], want)
	}
	if !strings.HasPrefix(got[1], "Brigade: team messages reach this session through your Monitor tool.") ||
		!strings.Contains(got[1], "persistent, wake_delay_ms 0") ||
		!strings.Contains(got[1], f.pluginBin+" inbox follow") || !strings.Contains(got[1], f.pluginBin+" send") ||
		!strings.Contains(got[1], "never stop the monitor") {
		t.Fatalf("arming line %q", got[1])
	}
	if claudeReads != 0 {
		t.Fatalf("a Muse session read %d file(s) under the Claude config directory", claudeReads)
	}
	if got := f.dumpVerbs(); strings.Join(got, ",") != "describe,session register" {
		t.Fatalf("adapter children %q", got)
	}
	for _, inv := range f.dump() {
		for name := range inv.Env {
			if strings.HasPrefix(name, "MUSE_") || name == "CLAUDE_PID" || strings.HasPrefix(name, "CLAUDE_CODE_MESSAGING_") {
				t.Errorf("adapter child %s %s received %s", inv.Group, inv.Verb, name)
			}
		}
	}
	m := f.mustMap()
	switch {
	case m.Harness != string(config.HostMuse), m.Model != "muse-wonder-full-1.0", m.ClaudePID != f.pid,
		m.ClaudeSessionID != f.nativeID, m.BrigadeSessionID != "brigade-sess-1", m.SocketPath != "",
		m.TranscriptPath != "", m.PermissionMode != "bypassPermissions", m.Inbound != "accept",
		m.PluginBin != f.pluginBin, m.SessionName != "checkout", m.HarnessVersion != "unknown", m.Entrypoint != "", m.NonInteractive:
		t.Fatalf("map %+v", *m)
	}
	ptr, err := f.store().ReadByHost(string(config.HostMuse), f.nativeID)
	if err != nil || ptr.HostPID != f.pid || ptr.BrigadeSessionID != "brigade-sess-1" {
		t.Fatalf("by-host pointer %+v %v", ptr, err)
	}
	if f.spawner.count() != 1 {
		t.Fatalf("watcher spawns %d, want 1", f.spawner.count())
	}
	spec := f.spawner.last(t)
	if len(spec.Args) != 3 || spec.Args[0] != "watch" || spec.Args[1] != "--sink" {
		t.Fatalf("spec args %q", spec.Args)
	}
	sink := spec.Args[2]
	if filepath.Dir(sink) != inbound.SinkDir(f.stateDir, "brigade-sess-1") || !strings.HasSuffix(sink, ".ndjson") {
		t.Fatalf("sink %q is not a per-watcher file under the session's inbox directory", sink)
	}
	for _, e := range spec.Env {
		if strings.HasPrefix(e, "CLAUDE_CODE_MESSAGING_") || strings.HasPrefix(e, "MUSE_") {
			t.Errorf("watcher env carries %s", e)
		}
	}
	if !strings.Contains(strings.Join(spec.Env, "\n"), "BRIGADE_CLAUDE_PID="+strconv.Itoa(f.pid)+"\n") {
		t.Fatalf("watcher env lacks the host pid: %v", spec.Env)
	}

	// The Muse SessionEnd: the watcher is signalled and every file of the
	// session is removed within the hook's few hundred milliseconds, and
	// no adapter is spawned for a close the host would kill anyway.
	exit, _, errOut = f.run(SubSessionEnd, f.endDoc("other"))
	if exit != 0 {
		t.Fatalf("session-end exit %d: %s", exit, errOut)
	}
	if got := f.dumpVerbs(); strings.Join(got, ",") != "describe,session register" {
		t.Fatalf("adapter children after session-end %q: a Muse session-end makes no adapter call", got)
	}
	if f.mapExists() {
		t.Fatal("the map survived session-end")
	}
	if _, err := f.store().ReadByHost(string(config.HostMuse), f.nativeID); err == nil {
		t.Fatal("the by-host pointer survived session-end")
	}
}

// TestMuseSessionStartNeedsAParent pins the one failure the Muse facts
// add: a hook whose parent is init (or none) cannot key a session.
func TestMuseSessionStartNeedsAParent(t *testing.T) {
	t.Parallel()
	f := newMuseFixture(t)
	f.deps.Getppid = func() int { return 1 }
	exit, out, _ := f.run(SubSessionStart, f.museStartDoc("default"))
	if exit != 0 || !strings.HasPrefix(out, "Brigade: not connected (config)") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if f.spawner.count() != 0 {
		t.Fatal("a watcher was spawned for an unkeyable session")
	}
}

// TestMusePromptKeepsAccept pins decision D5 (card 88): under Muse the
// card-50 parity rule does not apply, so a bypassPermissions prompt leaves
// the policy at accept and writes nothing under the Claude config
// directory; and the prompt hook keeps the sink watcher alive.
func TestMusePromptKeepsAccept(t *testing.T) {
	t.Parallel()
	f := newMuseFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.deps.WriteFile = func(path string, _ []byte, _ os.FileMode) error {
		t.Errorf("a Muse session wrote %s", path)
		return nil
	}
	if exit, _, errOut := f.run(SubSessionStart, f.museStartDoc("bypassPermissions")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	exit, out, errOut := f.run(SubPrompt, f.doc(map[string]any{
		"session_id": f.nativeID, "cwd": f.cwd, "hook_event_name": "UserPromptSubmit",
		"permission_mode": "bypassPermissions", "prompt": "never read", "model": "muse-wonder-full-1.0", "transcript_path": nil,
	}))
	if exit != 0 {
		t.Fatalf("prompt exit %d: %s", exit, errOut)
	}
	for _, l := range lines(out) {
		if strings.Contains(l, "refuse") || strings.Contains(l, "crossSessionInbound") {
			t.Fatalf("the prompt printed a parity line on Muse: %q", l)
		}
	}
	if m := f.mustMap(); m.Inbound != "accept" || m.PermissionMode != "bypassPermissions" {
		t.Fatalf("map after prompt %+v", *m)
	}
}

// TestMuseByteCap pins the hook's stdout byte cap on Muse (card 88): a
// line that would carry the output over museByteCap does not fit, and the
// cap counts bytes, not code points.
func TestMuseByteCap(t *testing.T) {
	t.Parallel()
	r := &run{byteCap: 100}
	if !r.fits(strings.Repeat("a", 99)) {
		t.Fatal("99 bytes plus a newline should fit under 100")
	}
	if r.fits(strings.Repeat("a", 100)) {
		t.Fatal("100 bytes plus a newline should not fit under 100")
	}
	// Twenty three-byte characters: 20 code points, 60 bytes; four of them
	// are 240 bytes, over the cap, while well under OutputCap's points.
	r = &run{byteCap: 100}
	if r.fits(strings.Repeat("€", 80)) {
		t.Fatal("240 bytes of 80 code points should not fit under a 100-byte cap")
	}
	claude := &run{}
	if !claude.fits(strings.Repeat("€", 3000)) {
		t.Fatal("without a byte cap only OutputCap applies")
	}
}

// TestMuseOptionsFileReachesTheHook pins D4 (card 88): a muse-options.json
// under the config directory the hook resolves from HOME sets the options
// a Claude session would get from CLAUDE_PLUGIN_OPTION_*, and a file that
// is not an object earns one line and the defaults.
func TestMuseOptionsFileReachesTheHook(t *testing.T) {
	t.Parallel()
	f := newMuseFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.noAdapterOption = true
	// The fixture's default reader answers only Claude's settings path; a
	// Muse hook reads the options file for real.
	f.deps.ReadFile = os.ReadFile
	path := filepath.Join(f.configDir, config.MuseOptionsFile)
	if err := os.MkdirAll(f.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The adapter option moves into the file — as the TEXT a Claude option
	// would carry, a JSON array rendered as a string — and the inbound
	// policy with it.
	optionText, err := json.Marshal(f.adapterOption())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"adapter_command": ` + string(optionText) + `, "team_inbound": "hold"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, out, errOut := f.run(SubSessionStart, f.museStartDoc("default"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.Inbound != "hold" {
		t.Fatalf("the options file did not reach the hook: inbound %q (stdout %q)", m.Inbound, out)
	}
	if strings.Contains(out, config.WarnOptionsUnusable(path)) {
		t.Fatalf("a usable file earned the warning: %q", out)
	}
}
