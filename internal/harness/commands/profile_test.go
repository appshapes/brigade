package commands

import (
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// errorsAs is errors.As under a name the team test shares.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// TestWriteDefaultAdapterForms pins the three `--adapter` forms (brief
// section 3): `<name>=<command>` registers and writes the sidecar naming
// the name; a bare name must already be registered (or be supabase); a
// path or array writes the sidecar itself.
func TestWriteDefaultAdapterForms(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "brigade-adapter-x")
	readSidecar := func(t *testing.T, configDir, profile string) string {
		t.Helper()
		path, err := config.SidecarPath(configDir, profile)
		if err != nil {
			t.Fatal(err)
		}
		data, err := adapterkit.ReadStrict(path)
		if err != nil {
			t.Fatalf("sidecar: %v", err)
		}
		return strings.TrimSpace(string(data))
	}
	readRegistry := func(t *testing.T, configDir string) map[string]any {
		t.Helper()
		data, err := adapterkit.ReadStrict(config.RegistryPath(configDir))
		if err != nil {
			t.Fatalf("registry: %v", err)
		}
		var reg map[string]any
		if err := json.Unmarshal(data, &reg); err != nil {
			t.Fatal(err)
		}
		return reg
	}

	t.Run("name=path registers", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a, err := writeDefaultAdapter(dir, "p", "x="+bin)
		if err != nil {
			t.Fatalf("writeDefaultAdapter: %v", err)
		}
		if !slices.Equal(a.Argv, []string{bin}) || a.Source != config.SourceSidecar {
			t.Errorf("adapter = %+v", a)
		}
		if got := readSidecar(t, dir, "p"); got != "x" {
			t.Errorf("sidecar = %q, want the name", got)
		}
		if reg := readRegistry(t, dir); reg["x"] == nil {
			t.Errorf("registry = %v, want x", reg)
		}
		// A second profile can now use the bare name.
		if b, err := writeDefaultAdapter(dir, "q", "x"); err != nil || !slices.Equal(b.Argv, []string{bin}) {
			t.Errorf("bare registered name: %+v %v", b, err)
		}
	})
	t.Run("name=array registers fixed arguments", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a, err := writeDefaultAdapter(dir, "p", `fs=["`+bin+`", "--root", "/srv/x"]`)
		if err != nil {
			t.Fatalf("writeDefaultAdapter: %v", err)
		}
		if !slices.Equal(a.Argv, []string{bin, "--root", "/srv/x"}) {
			t.Errorf("argv = %v", a.Argv)
		}
		if got := readSidecar(t, dir, "p"); got != "fs" {
			t.Errorf("sidecar = %q", got)
		}
		// The binding's NAME resolves the same thing through the registry
		// (P7-6: the chain reads names, not profiles).
		r, err := config.ResolveAdapter(config.Options{}, dir, "fs")
		if err != nil || !slices.Equal(r.Argv, a.Argv) {
			t.Errorf("ResolveAdapter after registration = %+v %v", r, err)
		}
	})
	t.Run("bare path and array write the sidecar only", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if a, err := writeDefaultAdapter(dir, "p", bin); err != nil || !slices.Equal(a.Argv, []string{bin}) {
			t.Errorf("path form: %+v %v", a, err)
		}
		if got := readSidecar(t, dir, "p"); got != bin {
			t.Errorf("sidecar = %q", got)
		}
		if a, err := writeDefaultAdapter(dir, "q", `["`+bin+`","--root","/y"]`); err != nil || len(a.Argv) != 3 {
			t.Errorf("array form: %+v %v", a, err)
		}
		if _, err := os.Stat(config.RegistryPath(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a registry was written for a nameless form: %v", err)
		}
	})
	t.Run("bare name must be registered", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := writeDefaultAdapter(dir, "p", "ghost")
		wantCode(t, err, protocol.CodeConfig, config.ReasonAdapterUnregistered)
		if _, err := os.Stat(filepath.Join(dir, "teams", "p", "adapter")); !errors.Is(err, os.ErrNotExist) {
			t.Error("a sidecar the next SessionStart would refuse was written")
		}
		a, err := writeDefaultAdapter(dir, "p", "supabase")
		if err != nil || !a.Bundled {
			t.Errorf("supabase = %+v %v, want bundled", a, err)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for name, spec := range map[string]string{
			"relative path":     "x=relative/adapter",
			"relative array":    `x=["relative","--root","/x"]`,
			"empty array":       "x=[]",
			"command line":      "x=/opt/adapter --root /x",
			"bundled name":      "supabase=" + bin,
			"junk":              "x={}",
			"empty":             "",
			"name with a space": "my adapter=" + bin,
		} {
			if _, err := writeDefaultAdapter(dir, "p", spec); err == nil {
				t.Errorf("%s (%q) was accepted", name, spec)
			} else {
				wantCode(t, err, protocol.CodeConfig, "")
			}
		}
	})
}

// TestSplitRegistration pins the `<name>=<command>` recogniser.
func TestSplitRegistration(t *testing.T) {
	t.Parallel()
	for spec, want := range map[string][3]string{
		"fs=/opt/fs":        {"fs", "/opt/fs", "yes"},
		"fs=[\"/a\",\"b\"]": {"fs", "[\"/a\",\"b\"]", "yes"},
		"a-b_1=/x":          {"a-b_1", "/x", "yes"},
		"/opt/fs":           {"", "", ""},
		"[\"/a\"]":          {"", "", ""},
		"=x":                {"", "", ""},
		"fs=":               {"", "", ""},
		"bad name!=/x":      {"", "", ""},
		"fs":                {"", "", ""},
	} {
		name, cmd, ok := splitRegistration(spec)
		if ok != (want[2] == "yes") || name != want[0] || cmd != want[1] {
			t.Errorf("splitRegistration(%q) = %q %q %v, want %v", spec, name, cmd, ok, want)
		}
	}
}

// TestProfileInitWithAdapterSpawnsThatAdapter: the sidecar is written
// FIRST and then the named adapter's own `profile init` runs with the
// remaining arguments.
func TestProfileInitWithAdapterSpawnsThatAdapter(t *testing.T) {
	t.Parallel()
	f, dump := passThroughFixture(t, "unused", fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"profile init": {{Result: []byte(`{"name":"newp","state":"not_member"}`)}},
		},
	})
	scriptPath := filepath.Join(f.dirs.Root, "script.json")
	spec := `fake=["` + fakeAdapterBin + `","--script","` + scriptPath + `"]`
	if err := Profile(f.inv(f.terminalEnv(), "", "init", "--profile", "newp", "--adapter", spec, "--force")); err != nil {
		t.Fatalf("profile init --adapter: %v", err)
	}
	invs := readDump(t, dump)
	if len(invs) != 1 || invs[0].Group != "profile" || invs[0].Verb != "init" || !slices.Equal(invs[0].Args, []string{"--force"}) {
		t.Fatalf("dump = %+v", invs)
	}
	if invs[0].Env["BRIGADE_PROFILE"] != "newp" {
		t.Errorf("BRIGADE_PROFILE = %q", invs[0].Env["BRIGADE_PROFILE"])
	}
	r, err := config.ResolveAdapter(config.Options{}, f.dirs.BrigadeConfig, "fake")
	if err != nil || r.Source != config.SourceProfile || r.Argv[0] != fakeAdapterBin {
		t.Errorf("the registered name does not resolve to the adapter: %+v %v", r, err)
	}
	if !strings.Contains(f.out.String(), `"name":"newp"`) {
		t.Errorf("stdout = %q", f.out.String())
	}
}

// TestProfileStatusLine pins the harness line for each D36 source and the
// in-session override, and that it precedes the adapter's own output.
func TestProfileStatusLine(t *testing.T) {
	t.Parallel()
	t.Run("sidecar in a terminal", func(t *testing.T) {
		t.Parallel()
		f, _ := passThroughFixture(t, "bob", fakeadapter.Script{
			Responses: map[string][]fakeadapter.Response{"profile status": {{Result: []byte(`{"name":"bob","state":"joined"}`)}}},
		})
		if err := Profile(f.inv(f.terminalEnv(), "", "status", "--profile", "bob")); err != nil {
			t.Fatalf("profile status: %v", err)
		}
		lines := strings.SplitN(f.out.String(), "\n", 2)
		// P7-6: the default comes from the binding's NAME through the
		// registry, and the line names the dialect, never the command.
		want := `profile bob: default adapter fake (from profile)`
		if lines[0] != want {
			t.Errorf("line:\n got %q\nwant %q", lines[0], want)
		}
		if !strings.Contains(lines[1], `"state":"joined"`) {
			t.Errorf("the adapter's own envelope did not follow: %q", lines[1])
		}
	})
	t.Run("bundled, profile member and override", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		bundled := config.Adapter{Bundled: true, Source: config.SourceBundled}
		tg := &target{profile: "fresh", configDir: f.dirs.BrigadeConfig, adapter: bundled, defaultAdapter: bundled}
		if got := statusLine(tg); got != "profile fresh: default adapter supabase (from bundled)" {
			t.Errorf("bundled line = %q", got)
		}
		// profile member: a registered name in team.json.
		if err := config.RegisterAdapter(f.dirs.BrigadeConfig, "fs", []string{f.adapterPath}); err != nil {
			t.Fatal(err)
		}
		if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, "viaprofile", &adapterkit.Profile{Version: 1, Adapter: "fs"}); err != nil {
			t.Fatal(err)
		}
		a, err := config.ResolveAdapter(config.Options{}, f.dirs.BrigadeConfig, "fs")
		if err != nil {
			t.Fatal(err)
		}
		tg = &target{profile: "viaprofile", configDir: f.dirs.BrigadeConfig, adapter: a, defaultAdapter: a}
		if got := statusLine(tg); got != "profile viaprofile: default adapter fs (from profile)" {
			t.Errorf("profile line = %q", got)
		}
		// In a session whose map resolved a DIFFERENT command for the same
		// profile, the session runs on that command and the line says so.
		m := f.byPID()
		m.TeamKey = "viaprofile"
		tg.session = m
		tg.adapter = config.Adapter{Argv: []string{f.adapterPath, "--override"}, Source: config.SourceMap}
		want := `profile viaprofile: default adapter fs (from profile); this session overrides it with ["` + f.adapterPath + `","--override"]`
		if got := statusLine(tg); got != want {
			t.Errorf("override line:\n got %q\nwant %q", got, want)
		}
		// The same command is no override.
		tg.adapter = config.Adapter{Argv: []string{f.adapterPath}, Source: config.SourceMap}
		if got := statusLine(tg); strings.Contains(got, "overrides") {
			t.Errorf("an identical command was reported as an override: %q", got)
		}
		// Another profile's map is not consulted.
		m.TeamKey = "other"
		tg.adapter = config.Adapter{Argv: []string{"/elsewhere"}, Source: config.SourceMap}
		if got := statusLine(tg); strings.Contains(got, "overrides") {
			t.Errorf("another profile's map was reported as an override: %q", got)
		}
		// A default the chain cannot resolve is said so, with the reason.
		tg = &target{profile: "viaprofile", configDir: f.dirs.BrigadeConfig, session: f.byPID(),
			adapter:    config.Adapter{Argv: []string{f.adapterPath}, Source: config.SourceMap},
			defaultErr: &protocol.Error{Code: protocol.CodeConfig, Details: map[string]string{"reason": config.ReasonAdapterUnregistered}}}
		tg.session.TeamKey = "viaprofile"
		want = `profile viaprofile: default adapter unresolvable (config: adapter_unregistered); this session overrides it with ["` + f.adapterPath + `"]`
		if got := statusLine(tg); got != want {
			t.Errorf("unresolvable line:\n got %q\nwant %q", got, want)
		}
	})
}

// TestProfileVerbsAndRefusals: the verb rule, --adapter only on init, and
// a registration that fails writes nothing and spawns nothing.
func TestProfileVerbsAndRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	wantCode(t, Profile(f.inv(f.terminalEnv(), "")), protocol.CodeUsage, "")
	wantCode(t, Profile(f.inv(f.terminalEnv(), "", "nuke")), protocol.CodeUsage, "")
	// --adapter on `status` is an adapter flag, forwarded — and the bundled
	// adapter is this test binary, so the spawn cannot succeed; assert only
	// that the harness did not treat it as its own by refusing early.
	err := Profile(f.inv(f.terminalEnv(), "", "init", "--adapter", "ghost"))
	wantCode(t, err, protocol.CodeConfig, config.ReasonAdapterUnregistered)
	if _, serr := os.Stat(filepath.Join(f.dirs.BrigadeConfig, "teams", "default", "adapter")); !errors.Is(serr, os.ErrNotExist) {
		t.Error("a sidecar was written for an unregistered name")
	}
	// A profile name that fails the path rule never becomes a path.
	wantCode(t, Profile(f.inv(f.terminalEnv(), "", "status", "--profile", "../x")), protocol.CodeConfig, "invalid_profile_name")
}

// TestProfileStatusInSessionUsesTheMapsProfile: without --profile the
// session's own profile and config dir are the ones described.
func TestProfileStatusInSessionUsesTheMapsProfile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The map's adapter path does not exist, so the pass-through fails at
	// spawn; the harness line was already written before that.
	err := Profile(f.inv(f.sessionEnv("BRIGADE_PROFILE=evil", "BRIGADE_CONFIG_DIR=/evil"), "", "status"))
	wantCode(t, err, protocol.CodeUnavailable, "adapter_not_found")
	line := strings.SplitN(f.out.String(), "\n", 2)[0]
	if !strings.HasPrefix(line, "profile "+fixtureProfile+": default adapter supabase (from bundled); this session overrides it with [") {
		t.Errorf("line = %q", line)
	}
}
