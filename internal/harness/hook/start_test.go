package hook

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// TestSessionStartRegistersSession is the happy path through the REAL
// fake-adapter binary: describe then register as real children with the
// from-scratch environment, both maps written with the resolved values
// (and never the token), the watcher spawned with the hook-built
// environment, the pidfile awaited, and the one context line printed.
func TestSessionStartRegistersSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	watcher := testutil.NewSleeper(t)
	f.spawner.watcherPID = watcher
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	want := "Brigade: this session is \"payments-api\" (brigade-sess-1) in team \"ops\"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`; terminal commands: " + f.pluginBin
	if got := lines(out); len(got) != 1 || got[0] != want {
		t.Fatalf("stdout %q\nwant  %q", out, want)
	}
	if got := f.dumpVerbs(); strings.Join(got, ",") != "describe,session register" {
		t.Fatalf("adapter children %q", got)
	}
	for _, inv := range f.dump() {
		for name := range inv.Env {
			if strings.HasPrefix(name, "CLAUDE_CODE_MESSAGING_") || name == "CLAUDE_PID" || strings.HasPrefix(name, "CLAUDE_PLUGIN_OPTION_") {
				t.Errorf("adapter child %s %s received %s", inv.Group, inv.Verb, name)
			}
		}
		if inv.Env["BRIGADE_PROFILE"] != "default" || inv.Env["BRIGADE_STATE_DIR"] != f.stateDir || inv.Env["BRIGADE_CONFIG_DIR"] != f.configDir {
			t.Errorf("adapter child env %v", inv.Env)
		}
	}
	m := f.mustMap()
	wantArgv := []string{fakeAdapterBin, "--script", f.scriptPath}
	switch {
	case m.ClaudePID != f.pid, m.ClaudeSessionID != f.nativeID, m.BrigadeSessionID != "brigade-sess-1",
		m.TeamRef != teamRef, m.TeamName != teamName, m.SessionName != "payments-api", m.PermissionMode != "",
		m.NonInteractive, m.Inbound != "accept", m.SocketPath != f.socket, m.Profile != "default",
		m.ConfigDir != f.configDir, strings.Join(m.AdapterCommand, "\x00") != strings.Join(wantArgv, "\x00"),
		m.PluginBin != f.pluginBin, m.HarnessVersion != "2.1.259", !m.RegisteredAt.Equal(fixedTime), !m.UpdatedAt.Equal(fixedTime):
		t.Fatalf("map %+v", *m)
	}
	mapPath, _ := f.store().ByPIDPath(f.pid)
	if fi, err := os.Stat(mapPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("map mode: %v %v", fi, err)
	}
	bn, err := f.store().ReadByNative(f.nativeID)
	if err != nil || bn.BrigadeSessionID != "brigade-sess-1" || bn.TeamRef != teamRef || bn.SessionName != "payments-api" {
		t.Fatalf("by-native %+v %v", bn, err)
	}
	if f.spawner.count() != 1 {
		t.Fatalf("watcher spawns %d, want 1", f.spawner.count())
	}
	spec := f.spawner.last(t)
	if strings.Join(spec.Args, " ") != "watch" || spec.Dir != f.dirs.Home || spec.LogPath != filepath.Join(f.stateDir, "logs", "watcher-"+strconv.Itoa(f.pid)+".log") {
		t.Fatalf("spec %+v", spec)
	}
	assertWatcherEnv(t, spec.Env, f)
	v, err := pidfile.Check(f.pidfilePath(), f.deps.Lookup)
	if err != nil || !v.Found || !v.Alive || v.Entry.PID != watcher || v.Entry.BrigadeSessionID != "brigade-sess-1" {
		t.Fatalf("pidfile %+v %v", v, err)
	}
}

// assertWatcherEnv checks the hook-built watcher environment (6.6): the
// six BRIGADE_* of the hook, the log level, the socket and the token each
// exactly once; the allow-listed inherited names; nothing of the session.
func assertWatcherEnv(t *testing.T, env []string, f *fixture) {
	t.Helper()
	want := map[string]string{
		"BRIGADE_CLAUDE_PID":           strconv.Itoa(f.pid),
		"BRIGADE_PROFILE":              "default",
		"BRIGADE_CONFIG_DIR":           f.configDir,
		"BRIGADE_STATE_DIR":            f.stateDir,
		"BRIGADE_ADAPTER_COMMAND":      f.adapterOption(),
		"BRIGADE_TEAM_INBOUND":         "accept",
		"BRIGADE_LOG_LEVEL":            "info",
		"CLAUDE_CODE_MESSAGING_SOCKET": f.socket,
		"CLAUDE_CODE_MESSAGING_TOKEN":  msgTok,
		"HOME":                         f.dirs.Home,
		"CLAUDE_CONFIG_DIR":            f.dirs.ClaudeConfig,
		"XDG_STATE_HOME":               f.dirs.XDGState,
	}
	for name, value := range want {
		if n := countEnv(env, name); n != 1 {
			t.Errorf("watcher env: %d %s entries, want 1", n, name)
		}
		if got := envValue(env, name); got != value {
			t.Errorf("watcher env: %s=%q, want %q", name, got, value)
		}
	}
	for _, forbidden := range []string{"CLAUDE_PID", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_PLUGIN_ROOT", "CLAUDE_PLUGIN_OPTION_ADAPTER_COMMAND", "BRIGADE_FS_ROOT"} {
		if countEnv(env, forbidden) != 0 {
			t.Errorf("watcher env carries %s", forbidden)
		}
	}
	if _, err := config.FromWatcherEnv(env); err != nil {
		t.Errorf("the watcher cannot read its own environment: %v", err)
	}
}

// TestRegistrationCarriesNoLocalFacts is U-22 and I-33's harness half:
// the SessionRegistration document has exactly the 4.4.2 members — no cwd,
// hostname, username, native id or transcript path — and the workspace
// label only when share_workspace_label is on.
func TestRegistrationCarriesNoLocalFacts(t *testing.T) {
	t.Parallel()
	hostname, _ := os.Hostname()
	for _, tc := range []struct {
		name      string
		extra     []string
		wantLabel string
		wantKey   bool
	}{
		{"default: no label", nil, "", false},
		{"label without opt-in is not sent", []string{config.OptionWorkspaceLabel + "=repo brigade"}, "", false},
		{"label with opt-in is sent", []string{config.OptionShareWorkspaceLabel + "=true", config.OptionWorkspaceLabel + "=repo brigade"}, "repo brigade", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), tc.extra...); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			calls := seam.callsFor("session register")
			if len(calls) != 1 {
				t.Fatalf("%d register calls", len(calls))
			}
			raw := calls[0].Stdin
			var members map[string]any
			if err := json.Unmarshal(raw, &members); err != nil {
				t.Fatal(err)
			}
			allowed := map[string]bool{"harness": true, "harness_version": true, "session_name": true, "activity": true, "inbound": true, "workspace_label": true}
			for k := range members {
				if !allowed[k] {
					t.Errorf("registration carries member %q", k)
				}
			}
			for _, must := range []string{"harness", "harness_version", "session_name", "activity", "inbound"} {
				if _, ok := members[must]; !ok {
					t.Errorf("registration lacks %q", must)
				}
			}
			for _, secret := range []string{"/work/project", f.nativeID, "/never/read.jsonl", msgTok, "transcript"} {
				if strings.Contains(string(raw), secret) {
					t.Errorf("registration carries %q: %s", secret, raw)
				}
			}
			if hostname != "" && strings.Contains(string(raw), hostname) {
				t.Errorf("registration carries the hostname: %s", raw)
			}
			if user := os.Getenv("USER"); user != "" && strings.Contains(string(raw), user) {
				t.Errorf("registration carries the username: %s", raw)
			}
			label, has := members["workspace_label"]
			if has != tc.wantKey || (has && label != tc.wantLabel) {
				t.Errorf("workspace_label %v %v, want %v %q", has, label, tc.wantKey, tc.wantLabel)
			}
		})
	}
}

// TestCompactRefreshesMapOnly: `source = compact` re-resolves the name
// and rewrites updated_at, with no adapter call, no spawn and no output.
func TestCompactRefreshesMapOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatal(exit)
	}
	before := f.mustMap()
	calls, spawns := len(seam.calls), f.spawner.count()
	f.registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, "renamed", "busy", f.socket)})
	f.deps.Registry = f.registry
	f.now = fixedTime.Add(time.Hour)
	exit, out, _ := f.run(SubSessionStart, f.startDoc("compact"))
	if exit != 0 || out != "" {
		t.Fatalf("exit %d out %q", exit, out)
	}
	after := f.mustMap()
	if after.SessionName != "renamed" || !after.UpdatedAt.Equal(f.now) || !after.RegisteredAt.Equal(before.RegisteredAt) || after.BrigadeSessionID != before.BrigadeSessionID {
		t.Fatalf("map after compact %+v", *after)
	}
	if len(seam.calls) != calls || f.spawner.count() != spawns {
		t.Fatalf("compact made %d adapter calls and %d spawns", len(seam.calls)-calls, f.spawner.count()-spawns)
	}
	// Without a map there is nothing to refresh; still exit 0, silent.
	if err := f.store().DeleteByPID(f.pid); err != nil {
		t.Fatal(err)
	}
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("compact")); exit != 0 || out != "" || f.mapExists() {
		t.Fatalf("compact without a map: exit %d out %q map %v", exit, out, f.mapExists())
	}
}

// TestClearKeepsSessionAndHeartbeats is the idempotence of 6.3 (E0-8:
// SessionStart re-fires on /clear): a live watcher for this pid serving
// the map's session, with the same socket and token hash, means ONE
// heartbeat, no registration, no respawn; the map keeps its Brigade id and
// gains the new native id, and both native ids resolve to it.
func TestClearKeepsSessionAndHeartbeats(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	watcher := testutil.NewSleeper(t)
	f.spawner.watcherPID = watcher
	seam := f.useSeam(map[string][]fakeadapter.Response{
		"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"session heartbeat": {okResp(heartbeatDoc("brigade-sess-1"))},
	})
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatal(exit)
	}
	first := f.nativeID
	f.nativeID = "d2c72366-0000-4000-8000-000000000002"
	f.now = fixedTime.Add(time.Minute)
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if got := seam.verbs(); strings.Join(got, ",") != "describe,session register,session heartbeat" {
		t.Fatalf("calls %q", got)
	}
	var hb protocol.HeartbeatRequest
	if err := json.Unmarshal(seam.callsFor("session heartbeat")[0].Stdin, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.SessionName == nil || *hb.SessionName != "payments-api" || hb.Activity == nil || *hb.Activity != "busy" || hb.Inbound == nil || *hb.Inbound != "accept" {
		t.Fatalf("heartbeat %+v", hb)
	}
	m := f.mustMap()
	if m.BrigadeSessionID != "brigade-sess-1" || m.ClaudeSessionID != f.nativeID || !m.RegisteredAt.Equal(fixedTime) || !m.UpdatedAt.Equal(f.now) {
		t.Fatalf("map after clear %+v", *m)
	}
	for _, id := range []string{first, f.nativeID} {
		bn, err := f.store().ReadByNative(id)
		if err != nil || bn.BrigadeSessionID != "brigade-sess-1" {
			t.Fatalf("by-native %s: %+v %v", id, bn, err)
		}
	}
	if f.spawner.count() != 1 || !alive(watcher) {
		t.Fatalf("the watcher was respawned (%d) or killed (%v) on /clear", f.spawner.count(), alive(watcher))
	}
	if !strings.Contains(out, "(brigade-sess-1)") {
		t.Fatalf("stdout %q", out)
	}
}

// TestClearRespawnsWhenCoordinatesChange is D9's respawn branch (cold in
// practice, E0-5 (c)): the same session with a rotated token hash or a
// new socket path SIGTERMs the watcher and spawns a new one, then
// heartbeats. The unchanged case above is the control.
func TestClearRespawnsWhenCoordinatesChange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(f *fixture, e *pidfile.Entry)
	}{
		{"rotated token hash", func(_ *fixture, e *pidfile.Entry) { e.TokenSHA256 = forge(e.TokenSHA256) }},
		{"new socket path", func(f *fixture, _ *pidfile.Entry) { f.socket = filepath.Join(f.dirs.Root, "moved.sock") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			old := testutil.NewSleeper(t)
			f.spawner.watcherPID = old
			seam := f.useSeam(map[string][]fakeadapter.Response{
				"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
				"session heartbeat": {okResp(heartbeatDoc("brigade-sess-1"))},
			})
			if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatal(exit)
			}
			e, err := pidfile.Read(f.pidfilePath())
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(f, &e)
			if err := os.WriteFile(f.pidfilePath(), pidfile.Encode(e), 0o600); err != nil {
				t.Fatal(err)
			}
			replacement := testutil.NewSleeper(t)
			f.spawner.watcherPID = replacement
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(old) })
			if f.spawner.count() != 2 {
				t.Fatalf("spawns %d, want 2", f.spawner.count())
			}
			if got := seam.verbs(); strings.Join(got, ",") != "describe,session register,session heartbeat" {
				t.Fatalf("calls %q (no second registration expected)", got)
			}
			v, err := pidfile.Check(f.pidfilePath(), f.deps.Lookup)
			if err != nil || !v.Alive || v.Entry.PID != replacement || v.Entry.SocketPath != f.socket || v.Entry.TokenSHA256 != pidfile.TokenSHA256(msgTok) {
				t.Fatalf("new pidfile %+v %v", v, err)
			}
			if m := f.mustMap(); m.SocketPath != f.socket || m.BrigadeSessionID != "brigade-sess-1" {
				t.Fatalf("map %+v", *m)
			}
		})
	}
}

// TestHeartbeatGoneReRegisters: a heartbeat answered with conflict or
// not_found means the server no longer has the session; the hook retires
// the watcher, registers again with the map's id as the hint, and
// respawns.
func TestHeartbeatGoneReRegisters(t *testing.T) {
	t.Parallel()
	for _, code := range []protocol.Code{protocol.CodeConflict, protocol.CodeNotFound} {
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			old := testutil.NewSleeper(t)
			f.spawner.watcherPID = old
			seam := f.useSeam(map[string][]fakeadapter.Response{
				"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false)), okResp(registerDoc("brigade-sess-1", "payments-api", true))},
				"session heartbeat": {errResp(code, "")},
			})
			if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatal(exit)
			}
			f.spawner.watcherPID = testutil.NewSleeper(t)
			if exit, out, _ := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 || !strings.Contains(out, "(brigade-sess-1)") {
				t.Fatalf("exit %d out %q", exit, out)
			}
			if got := seam.verbs(); strings.Join(got, ",") != "describe,session register,session heartbeat,session register" {
				t.Fatalf("calls %q", got)
			}
			var reg protocol.SessionRegistration
			if err := json.Unmarshal(seam.callsFor("session register")[1].Stdin, &reg); err != nil {
				t.Fatal(err)
			}
			if reg.Resume == nil || reg.Resume.SessionID != "brigade-sess-1" {
				t.Fatalf("second registration resume %+v, want the map's id", reg.Resume)
			}
			testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(old) })
			if f.spawner.count() != 2 {
				t.Fatalf("spawns %d, want 2", f.spawner.count())
			}
		})
	}
}

// TestResumeHint is the 3.7 / E0-5 (f) table: the by-native hint is sent
// for any source, a not_found or conflict answer falls back to a fresh
// registration (and rewrites by-native), and the hint is skipped when a
// live pidfile of another pid names the same Brigade session.
func TestResumeHint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		source      string
		hint        bool
		otherLive   bool
		responses   []fakeadapter.Response
		wantCalls   int
		wantResume  []string // per register call: the resume id or ""
		wantSession string
	}{
		{"hint honoured on resume", "resume", true, false, []fakeadapter.Response{okResp(registerDoc("old-sess", "payments-api", true))}, 1, []string{"old-sess"}, "old-sess"},
		{"hint honoured on startup too", "startup", true, false, []fakeadapter.Response{okResp(registerDoc("old-sess", "payments-api", true))}, 1, []string{"old-sess"}, "old-sess"},
		{"not_found falls back", "resume", true, false, []fakeadapter.Response{errResp(protocol.CodeNotFound, ""), okResp(registerDoc("new-sess", "payments-api", false))}, 2, []string{"old-sess", ""}, "new-sess"},
		{"conflict session_live falls back", "resume", true, false, []fakeadapter.Response{errResp(protocol.CodeConflict, "session_live"), okResp(registerDoc("new-sess", "payments-api", false))}, 2, []string{"old-sess", ""}, "new-sess"},
		{"hint skipped for another live watcher", "resume", true, true, []fakeadapter.Response{okResp(registerDoc("new-sess", "payments-api", false))}, 1, []string{""}, "new-sess"},
		{"no hint without by-native", "resume", false, false, []fakeadapter.Response{okResp(registerDoc("new-sess", "payments-api", false))}, 1, []string{""}, "new-sess"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": tc.responses})
			if tc.hint {
				if err := f.store().WriteByNative(f.nativeID, &sessionmap.ByNative{BrigadeSessionID: "old-sess", TeamRef: teamRef, SessionName: "payments-api"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.otherLive {
				other := testutil.NewSleeper(t)
				e := liveEntry(t, other, "old-sess", f.socket, pidfile.TokenSHA256("other"))
				if err := pidfile.Create(pidfile.Path(f.stateDir, 99999), e); err != nil {
					t.Fatal(err)
				}
			}
			if exit, out, errOut := f.run(SubSessionStart, f.startDoc(tc.source)); exit != 0 || !strings.Contains(out, "("+tc.wantSession+")") {
				t.Fatalf("exit %d out %q err %q", exit, out, errOut)
			}
			calls := seam.callsFor("session register")
			if len(calls) != tc.wantCalls {
				t.Fatalf("%d register calls, want %d", len(calls), tc.wantCalls)
			}
			for i, c := range calls {
				var reg protocol.SessionRegistration
				if err := json.Unmarshal(c.Stdin, &reg); err != nil {
					t.Fatal(err)
				}
				got := ""
				if reg.Resume != nil {
					got = reg.Resume.SessionID
				}
				if got != tc.wantResume[i] {
					t.Errorf("register call %d resume %q, want %q", i, got, tc.wantResume[i])
				}
			}
			if m := f.mustMap(); m.BrigadeSessionID != tc.wantSession {
				t.Fatalf("map session %q, want %q", m.BrigadeSessionID, tc.wantSession)
			}
			bn, err := f.store().ReadByNative(f.nativeID)
			if err != nil || bn.BrigadeSessionID != tc.wantSession {
				t.Fatalf("by-native %+v %v, want %q", bn, err, tc.wantSession)
			}
		})
	}
}

// TestAdapterResolutionFailureLine is D36's context line: an adapter that
// cannot be resolved names the source (never the value), spawns nothing
// and writes no map. The option path of every other test is the control.
func TestAdapterResolutionFailureLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		setup      func(f *fixture) []string
		wantSource string
	}{
		{"relative option", func(*fixture) []string { return []string{config.OptionAdapterCommand + "=adapters/fake"} }, "option"},
		{"unregistered sidecar name", func(f *fixture) []string {
			f.noAdapterOption = true
			dir := filepath.Join(f.configDir, "profiles", "default")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				f.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "adapter"), []byte("fs\n"), 0o600); err != nil {
				f.t.Fatal(err)
			}
			return nil
		}, "sidecar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(nil)
			extra := tc.setup(f)
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), extra...)
			if exit != 0 {
				t.Fatal(exit)
			}
			want := "Brigade: not connected (config): the adapter for profile \"default\" could not be resolved from " + tc.wantSource + "; run `brigade profile status` in a terminal"
			if got := lines(out); len(got) != 1 || got[0] != want {
				t.Fatalf("stdout %q\nwant  %q", out, want)
			}
			if strings.Contains(out, "adapters/fake") || strings.Contains(errOut, "adapters/fake") {
				t.Fatal("the value was echoed")
			}
			if len(seam.calls) != 0 || f.spawner.count() != 0 || f.mapExists() {
				t.Fatalf("calls %d spawns %d map %v after a resolution failure", len(seam.calls), f.spawner.count(), f.mapExists())
			}
		})
	}
}

// TestPolicyWarnings is 6.8/6.10 in the hook: the effective policy lands
// in the map, the registration and the context line, and each way of
// arriving at refuse prints its fixed warning as a line of its own.
func TestPolicyWarnings(t *testing.T) {
	t.Parallel()
	settings := "/work/project/.claude/settings.json"
	for _, tc := range []struct {
		name        string
		extra       []string
		readFile    func(string) ([]byte, error)
		wantPolicy  string
		wantWarning string
	}{
		{"default accept", nil, nil, "accept", ""},
		{"option refuse", []string{config.OptionTeamInbound + "=refuse"}, nil, "refuse", ""},
		{"option hold is refuse with a warning", []string{config.OptionTeamInbound + "=hold"}, nil, "refuse", config.WarnInboundHold},
		{"option junk is refuse with a warning", []string{config.OptionTeamInbound + "=sometimes"}, nil, "refuse", config.WarnInboundInvalid},
		{"native refuse in the project file", nil, func(p string) ([]byte, error) {
			if p == settings {
				return []byte(`{"crossSessionInbound": "refuse"}`), nil
			}
			return nil, os.ErrNotExist
		}, "refuse", policy.Scan{Found: true, Value: "refuse", File: settings}.Warning()},
		{"native hold in the user file", nil, func(p string) ([]byte, error) {
			if strings.HasSuffix(p, "/settings.json") && !strings.HasPrefix(p, "/work") {
				return []byte(`{"crossSessionInbound": "hold"}`), nil
			}
			return nil, os.ErrNotExist
		}, "refuse", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.readFile != nil {
				f.deps.ReadFile = tc.readFile
			}
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"), tc.extra...)
			if exit != 0 {
				t.Fatal(exit)
			}
			got := lines(out)
			if !strings.Contains(got[0], "; inbound: "+tc.wantPolicy+";") {
				t.Fatalf("line %q lacks inbound %s", got[0], tc.wantPolicy)
			}
			switch {
			case tc.wantWarning == "" && tc.name != "native hold in the user file" && len(got) != 1:
				t.Fatalf("%d lines, want 1: %q", len(got), got)
			case tc.wantWarning != "" && (len(got) != 2 || got[1] != tc.wantWarning):
				t.Fatalf("lines %q, want the warning %q second", got, tc.wantWarning)
			case tc.name == "native hold in the user file" && (len(got) != 2 || !strings.Contains(got[1], `"crossSessionInbound": "hold"`) || !strings.Contains(got[1], f.dirs.ClaudeConfig)):
				t.Fatalf("lines %q, want a warning naming hold and the user file", got)
			}
			if m := f.mustMap(); m.Inbound != tc.wantPolicy {
				t.Fatalf("map inbound %q", m.Inbound)
			}
			var reg protocol.SessionRegistration
			if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
				t.Fatal(err)
			}
			if reg.Inbound != tc.wantPolicy {
				t.Fatalf("registered inbound %q", reg.Inbound)
			}
			if spec := f.spawner.last(t); envValue(spec.Env, "BRIGADE_TEAM_INBOUND") != tc.wantPolicy {
				t.Fatalf("watcher BRIGADE_TEAM_INBOUND=%q", envValue(spec.Env, "BRIGADE_TEAM_INBOUND"))
			}
		})
	}
}

// TestShadowingWarning is E0-8 (e): any `brigade` on the hook's PATH
// other than the plugin's own bootstrap earns the warning; none, or a
// symlink to the plugin's, does not.
func TestShadowingWarning(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind string // "shadow", "symlink", "none", "nonexec"
		warn bool
	}{
		{"another brigade first on PATH", "shadow", true},
		{"a symlink to the plugin's bootstrap", "symlink", false},
		{"nothing on PATH", "none", false},
		{"a non-executable file is not a shadow", "nonexec", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			dir := filepath.Join(f.dirs.Root, "path-bin")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			decoy := filepath.Join(dir, "brigade")
			switch tc.kind {
			case "shadow":
				if err := os.WriteFile(decoy, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // G306: an executable fixture
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(f.pluginBin, decoy); err != nil {
					t.Fatal(err)
				}
			case "nonexec":
				if err := os.WriteFile(decoy, []byte("not a program"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"), "PATH="+dir+string(os.PathListSeparator)+f.emptyPath())
			if exit != 0 {
				t.Fatal(exit)
			}
			got := lines(out)
			if tc.warn {
				want := shadowLine(decoy)
				if len(got) != 2 || got[1] != want {
					t.Fatalf("lines %q, want the warning %q", got, want)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("lines %q, want no warning", got)
			}
		})
	}
}

// TestNoSocketNoWatcher: a host without an inbox socket registers, records
// an empty socket_path and starts no watcher (poll_on_prompt is the
// fallback); the socket case above is the control.
func TestNoSocketNoWatcher(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.noSocket = true
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 || !strings.Contains(out, "(brigade-sess-1)") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if f.spawner.count() != 0 || !strings.Contains(errOut, "watcher is not started") {
		t.Fatalf("spawns %d stderr %q", f.spawner.count(), errOut)
	}
	if m := f.mustMap(); m.SocketPath != "" {
		t.Fatalf("socket_path %q", m.SocketPath)
	}
}

// TestPermissionModeRecorded: the SessionStart document's permission_mode
// lands in the map when present and is diagnostics only (the policy above
// never depends on it).
func TestPermissionModeRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	doc := f.doc(map[string]any{"session_id": f.nativeID, "cwd": "/work/project", "hook_event_name": "SessionStart", "source": "startup", "permission_mode": "bypassPermissions"})
	if exit, out, _ := f.run(SubSessionStart, doc); exit != 0 || !strings.Contains(out, "inbound: accept") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if m := f.mustMap(); m.PermissionMode != "bypassPermissions" {
		t.Fatalf("permission_mode %q", m.PermissionMode)
	}
}

// TestPlantedMapIsReplaced: a by-pid map that fails the privacy check is
// not trusted and is overwritten by the registration (3.2: a planted map
// cannot survive to the first Bash call).
func TestPlantedMapIsReplaced(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	path, _ := f.store().ByPIDPath(f.pid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	planted := `{"claude_pid":` + strconv.Itoa(f.pid) + `,"brigade_session_id":"planted","profile":"evil","config_dir":"/evil","inbound":"accept","adapter_command":["/evil/adapter"]}`
	if err := os.WriteFile(path, []byte(planted), 0o644); err != nil { //nolint:gosec // G306: the planted, world-readable map is the PRECONDITION
		t.Fatal(err)
	}
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !strings.Contains(out, "(brigade-sess-1)") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	m := f.mustMap()
	if m.BrigadeSessionID != "brigade-sess-1" || m.Profile != "default" {
		t.Fatalf("map %+v", *m)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("map mode after rewrite: %v %v", fi, err)
	}
}

// TestStopWatcherIgnoresGonePid: the SIGTERM path tolerates a pid that
// vanished between the check and the signal.
func TestStopWatcherIgnoresGonePid(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := newRun(streamsOf(t), f.env(), f.deps.withDefaults(), "info")
	dead := testutil.NewSleeper(t)
	if err := syscall.Kill(dead, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(dead) })
	r.stopWatcher(pidfile.Entry{PID: dead, StartToken: "x"}, facts{pid: f.pid, stateDir: f.stateDir})
}
