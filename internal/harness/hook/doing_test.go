package hook

import (
	"encoding/json"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The by-pid map's `doing_mode` (card 25, plan 5.2): resolved once by
// SessionStart from the adapter's capabilities, the `share_doing` option
// and the permission rules Brigade can read, first match wins; inherited
// unchanged on the continue path, where there is no describe. The seam's
// default describe (helpers_test.go) does NOT advertise
// `session.description`, so a test that wants any mode but `unsupported`
// asks for the capability explicitly.

// settingsMarker is planted in every settings fixture: a rule is read to
// decide one word, and no byte of it may reach the model's context or
// the log.
const settingsMarker = "EVILMARKER-settings"

// describeWithDescription is the fixture's describe plus the
// session.description capability.
func describeWithDescription() jsontext.Value {
	var d protocol.DescribeResult
	if err := json.Unmarshal(describeDoc(protocol.ProtocolVersion, teamName), &d); err != nil {
		panic(err)
	}
	d.Capabilities = append(d.Capabilities, "session.description")
	return mustJSON(&d)
}

// settingsReader answers one content for the user settings file and
// "missing" for every other path.
func settingsReader(f *fixture, content string) func(string) ([]byte, error) {
	user := filepath.Join(f.dirs.ClaudeConfig, "settings.json")
	return func(p string) ([]byte, error) {
		if p == user {
			return []byte(content), nil
		}
		return nil, os.ErrNotExist
	}
}

func TestSessionStartResolvesTheDoingMode(t *testing.T) {
	t.Parallel()
	deny := `{"note":"` + settingsMarker + `","permissions":{"deny":["Bash(brigade doing:*)"]}}`
	allow := `{"permissions":{"allow":["Bash(brigade:*)"]}}`
	for _, tc := range []struct {
		name       string
		capable    bool
		env        []string
		settings   string // "" means no settings file anywhere
		want       string
		wantMarker bool
	}{
		{"no capability is unsupported", false, nil, "", doing.ModeUnsupported, false},
		{"no capability wins over the option and the rules", false, []string{config.OptionShareDoing + "=false"}, deny, doing.ModeUnsupported, true},
		{"the option off", true, []string{config.OptionShareDoing + "=false"}, "", doing.ModeOff, false},
		{"the option off wins over a deny", true, []string{config.OptionShareDoing + "=off"}, deny, doing.ModeOff, true},
		{"a deny on the verb is unasked", true, nil, deny, doing.ModeUnasked, true},
		{"an unreadable settings file is unasked", true, nil, `{"permissions": {` + settingsMarker, doing.ModeUnasked, true},
		{"an exact allow is allowed", true, nil, allow, doing.ModeAllowed, false},
		{"nothing either way is quiet", true, nil, "", doing.ModeQuiet, false},
		{"the send gate alone is quiet (ruling 4)", true, nil, `{"permissions":{"ask":["Bash(brigade send*)"]}}`, doing.ModeQuiet, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.settings != "" {
				f.deps.ReadFile = settingsReader(f, tc.settings)
			}
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			if tc.capable {
				seam.describe = describeWithDescription()
			}
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), tc.env...)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if got := f.mustMap().DoingMode; got != tc.want {
				t.Fatalf("doing_mode %q, want %q", got, tc.want)
			}
			if tc.wantMarker && (strings.Contains(out, settingsMarker) || strings.Contains(errOut, settingsMarker)) {
				t.Fatalf("a settings byte reached the session:\nstdout %q\nstderr %q", out, errOut)
			}
			// The mode is the hook's business only: no describe is spawned
			// beyond the one every registration costs, and nothing about
			// the mode is on the context line.
			if got := seam.verbs(); strings.Join(got, ",") != "describe,session register" {
				t.Fatalf("calls %q", got)
			}
			if strings.Contains(out, "doing") {
				t.Fatalf("the context line mentions the mode: %q", out)
			}
		})
	}
}

// TestSessionStartKeepsTheMapFreeOfSettingsBytes is the disk half of the
// same promise: the map carries one of five words, and a rule that
// matched — marker and all — is not in the file.
func TestSessionStartKeepsTheMapFreeOfSettingsBytes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.deps.ReadFile = settingsReader(f, `{"permissions":{"deny":["Bash(brigade:*) `+settingsMarker+`","Bash(brigade:*)"]}}`)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
	seam.describe = describeWithDescription()
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	p, err := f.store().ByPIDPath(f.pid)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p) // #nosec G304 -- a path this test just built under its own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"doing_mode":"unasked"`) {
		t.Fatalf("the map does not carry the mode:\n%s", raw)
	}
	if strings.Contains(string(raw), settingsMarker) || strings.Contains(string(raw), "Bash(") {
		t.Fatalf("the map carries settings bytes:\n%s", raw)
	}
}

// TestContinuePathInheritsTheDoingMode: a SessionStart that finds its own
// live watcher (the /clear path) has no describe, so it carries the
// existing map's mode forward whatever the rules now say — and a map
// without the member (written before the mode existed) stays without it,
// until the prompt hook's one-time resolution of P16-5.
func TestContinuePathInheritsTheDoingMode(t *testing.T) {
	t.Parallel()
	t.Run("the first resolution stands", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.spawner.watcherPID = testutil.NewSleeper(t)
		seam := f.useSeam(map[string][]fakeadapter.Response{
			"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
			"session heartbeat": {okResp(heartbeatDoc())},
		})
		seam.describe = describeWithDescription()
		f.deps.ReadFile = settingsReader(f, `{"permissions":{"allow":["Bash(brigade:*)"]}}`)
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := f.mustMap().DoingMode; got != doing.ModeAllowed {
			t.Fatalf("after startup doing_mode %q, want allowed", got)
		}
		// The rules change under the running session; the continue path
		// does not re-read them.
		f.deps.ReadFile = settingsReader(f, `{"permissions":{"deny":["Bash(brigade:*)"]}}`)
		f.nativeID = "d2c72366-0000-4000-8000-000000000002"
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := seam.verbs(); strings.Join(got, ",") != "describe,session register,session heartbeat" {
			t.Fatalf("calls %q (the continue path must not describe again)", got)
		}
		if got := f.mustMap().DoingMode; got != doing.ModeAllowed {
			t.Fatalf("after /clear doing_mode %q, want the inherited allowed", got)
		}
	})
	t.Run("absent stays absent", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.spawner.watcherPID = testutil.NewSleeper(t)
		seam := f.useSeam(map[string][]fakeadapter.Response{
			"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
			"session heartbeat": {okResp(heartbeatDoc())},
		})
		seam.describe = describeWithDescription()
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		// A map written by a plugin from before the member existed.
		m := f.mustMap()
		m.DoingMode = ""
		if err := f.store().WriteByPID(m); err != nil {
			t.Fatal(err)
		}
		f.nativeID = "d2c72366-0000-4000-8000-000000000003"
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := f.mustMap().DoingMode; got != "" {
			t.Fatalf("after /clear doing_mode %q, want the member still absent", got)
		}
	})
}

// TestDoingScanDirsReadCLAUDE_PROJECT_DIR: the scan reads the project
// files under CLAUDE_PROJECT_DIR when it is absolute (Claude Code's own
// project root) as well as under the document's cwd, and a relative value
// is ignored.
func TestDoingScanDirsReadCLAUDE_PROJECT_DIR(t *testing.T) {
	t.Parallel()
	in := input{Cwd: "/work/repo/sub"}
	if got := doingScanDirs([]string{"CLAUDE_PROJECT_DIR=/work/repo"}, in); strings.Join(got, "|") != "/work/repo|/work/repo/sub" {
		t.Fatalf("dirs = %q", got)
	}
	if got := doingScanDirs([]string{"CLAUDE_PROJECT_DIR=rel/repo"}, in); strings.Join(got, "|") != "/work/repo/sub" {
		t.Fatalf("relative CLAUDE_PROJECT_DIR: dirs = %q", got)
	}
	if got := doingScanDirs(nil, in); strings.Join(got, "|") != "/work/repo/sub" {
		t.Fatalf("no CLAUDE_PROJECT_DIR: dirs = %q", got)
	}
	// Through the hook: a deny that lives only under CLAUDE_PROJECT_DIR
	// still closes the feature.
	f := newFixture(t)
	project := filepath.Join(t.TempDir(), "project")
	f.deps.ReadFile = func(p string) ([]byte, error) {
		if p == filepath.Join(project, ".claude", "settings.json") {
			return []byte(`{"permissions":{"deny":["Bash(brigade doing*)"]}}`), nil
		}
		return nil, os.ErrNotExist
	}
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
	seam.describe = describeWithDescription()
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), "CLAUDE_PROJECT_DIR="+project); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if got := f.mustMap().DoingMode; got != doing.ModeUnasked {
		t.Fatalf("doing_mode %q, want unasked from the CLAUDE_PROJECT_DIR deny", got)
	}
}
