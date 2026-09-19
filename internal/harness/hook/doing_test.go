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
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The by-pid map's `doing_mode` (card 25, plan 5.2): resolved by
// SessionStart from the adapter's capabilities, the `share_doing` option
// and the permission rules Brigade can read, first match wins; on the
// continue path, where there is no describe, the capability verdict is
// inherited and the option and the rules are resolved again (plan 5.3),
// and that path's heartbeat is where a conversation switch and an
// opt-out blank the doing line. The seam's default describe
// (helpers_test.go) does NOT advertise `session.description`, so a test
// that wants any mode but `unsupported` asks for the capability
// explicitly.

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

// TestContinuePathReResolvesTheDoingMode: a SessionStart that finds its
// own live watcher (the /clear path) has no describe, so the capability
// half of the mode is the existing map's — an adapter recorded as
// unsupported stays so without a second describe, and a map without the
// member (written before the mode existed) stays without it, until the
// prompt hook's one-time resolution of P16-5 — while the option and the
// rules are resolved again, so an opt-out or a rule added since lands at
// this SessionStart rather than the next session (plan 5.3).
func TestContinuePathReResolvesTheDoingMode(t *testing.T) {
	t.Parallel()
	t.Run("the rules and the option are read again", func(t *testing.T) {
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
		// A deny lands under the running session: the next /clear sees it.
		f.deps.ReadFile = settingsReader(f, `{"permissions":{"deny":["Bash(brigade:*)"]}}`)
		f.nativeID = "d2c72366-0000-4000-8000-000000000002"
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := seam.verbs(); strings.Join(got, ",") != "describe,session register,session heartbeat" {
			t.Fatalf("calls %q (the continue path must not describe again)", got)
		}
		if got := f.mustMap().DoingMode; got != doing.ModeUnasked {
			t.Fatalf("after /clear under a new deny doing_mode %q, want unasked", got)
		}
		// The option turned off: a same-id re-fire is enough.
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), config.OptionShareDoing+"=false"); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := f.mustMap().DoingMode; got != doing.ModeOff {
			t.Fatalf("after the option was turned off doing_mode %q, want off", got)
		}
	})
	t.Run("the capability verdict is inherited, not described again", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.spawner.watcherPID = testutil.NewSleeper(t)
		seam := f.useSeam(map[string][]fakeadapter.Response{
			"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
			"session heartbeat": {okResp(heartbeatDoc())},
		})
		// The seam's default describe lacks the capability.
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := f.mustMap().DoingMode; got != doing.ModeUnsupported {
			t.Fatalf("after startup doing_mode %q, want unsupported", got)
		}
		f.deps.ReadFile = settingsReader(f, `{"permissions":{"allow":["Bash(brigade:*)"]}}`)
		f.nativeID = "d2c72366-0000-4000-8000-000000000002"
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := seam.verbs(); strings.Join(got, ",") != "describe,session register,session heartbeat" {
			t.Fatalf("calls %q (the continue path must not describe again)", got)
		}
		if got := f.mustMap().DoingMode; got != doing.ModeUnsupported {
			t.Fatalf("after /clear doing_mode %q, want the inherited unsupported", got)
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

// continueHeartbeat decodes the continue path's one `session heartbeat`
// document as a raw map, so a member's PRESENCE can be asserted and not
// only its value: the doing line is blanked by `""` and kept by absence.
func continueHeartbeat(t *testing.T, seam *adapterSeam) map[string]any {
	t.Helper()
	calls := seam.callsFor("session heartbeat")
	if len(calls) != 1 {
		t.Fatalf("%d heartbeat calls, want one", len(calls))
	}
	var members map[string]any
	if err := json.Unmarshal(calls[0].Stdin, &members); err != nil {
		t.Fatal(err)
	}
	return members
}

// TestSessionStartBlanksTheDoingLine is plan 5.3's continue-path table
// (the /clear row and the option-turned-off row): the heartbeat carries
// session_description "" — the wire form of none — on a real conversation
// switch (a new native session id: /clear, an in-process /resume),
// whatever the option says, and on a same-id re-fire only when the
// existing map's mode published and the option is now off. Otherwise the
// member is ABSENT: a same-id re-fire such as /reload-plugins keeps the
// line, an adapter without the capability (the seam's default describe)
// is never sent the member, and a map whose mode is absent has not been
// resolved yet.
func TestSessionStartBlanksTheDoingLine(t *testing.T) {
	t.Parallel()
	const secondID = "d2c72366-0000-4000-8000-000000000002"
	for _, tc := range []struct {
		name       string
		capable    bool
		firstEnv   []string // the startup's option
		absentMode bool     // erase the map's mode before the second start
		newID      bool     // the second start is a conversation switch
		secondEnv  []string // the second start's option
		wantBlank  bool     // "" on the wire; else the member must be absent
		wantMode   string
	}{
		{"no capability: a /clear heartbeat carries no member", false, nil, false, true, nil, false, doing.ModeUnsupported},
		{"a same-id re-fire keeps the line", true, nil, false, false, nil, false, doing.ModeQuiet},
		{"/clear with the capability blanks it", true, nil, false, true, nil, true, doing.ModeQuiet},
		{"the option flipped off on a same-id re-fire blanks it", true, nil, false, false, []string{config.OptionShareDoing + "=false"}, true, doing.ModeOff},
		{"the option off and a conversation switch blanks it", true, []string{config.OptionShareDoing + "=false"}, false, true, []string{config.OptionShareDoing + "=false"}, true, doing.ModeOff},
		{"off before and after on a same-id re-fire: nothing to retract", true, []string{config.OptionShareDoing + "=false"}, false, false, []string{config.OptionShareDoing + "=false"}, false, doing.ModeOff},
		{"an unresolved mode keeps the line even on /clear", true, nil, true, true, nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{
				"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
				"session heartbeat": {okResp(heartbeatDoc())},
			})
			if tc.capable {
				seam.describe = describeWithDescription()
			}
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), tc.firstEnv...); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if tc.absentMode {
				m := f.mustMap()
				m.DoingMode = ""
				if err := f.store().WriteByPID(m); err != nil {
					t.Fatal(err)
				}
			}
			source := "startup"
			if tc.newID {
				f.nativeID = secondID
				source = "clear"
			}
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc(source), tc.secondEnv...); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if got := seam.verbs(); strings.Join(got, ",") != "describe,session register,session heartbeat" {
				t.Fatalf("calls %q", got)
			}
			members := continueHeartbeat(t, seam)
			desc, has := members["session_description"]
			switch {
			case tc.wantBlank && (!has || desc != ""):
				t.Fatalf("heartbeat session_description = %v (present %v), want \"\"", desc, has)
			case !tc.wantBlank && has:
				t.Fatalf("heartbeat carries session_description %v, want the member absent", desc)
			}
			// The rest of the heartbeat is what it always was.
			for _, must := range []string{"activity", "session_name", "inbound"} {
				if _, ok := members[must]; !ok {
					t.Errorf("heartbeat lacks %q", must)
				}
			}
			if got := f.mustMap().DoingMode; got != tc.wantMode {
				t.Fatalf("doing_mode after the second start %q, want %q", got, tc.wantMode)
			}
		})
	}
}

// TestNoHookRegistrationCarriesADescription: the registration literal
// never carries session_description — not on a fresh start, not on a
// resume (a `claude --resume` in a new process, TestResumeHint's setup),
// not on the re-registration a heartbeat answered conflict or not_found
// forces (TestHeartbeatGoneReRegisters's) — even against an adapter that
// announces the capability. Both bundled adapters write the registration's
// absent value over the stored sentence, and that is intended (ruling 5):
// next morning's resume must not show yesterday's sentence in the present
// tense. Typed decode of every register document plus a raw byte check,
// so neither a typed nil nor a spelling slips through.
func TestNoHookRegistrationCarriesADescription(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		run       func(t *testing.T, f *fixture, seam *adapterSeam)
		wantCalls int
	}{
		{"a fresh registration", func(t *testing.T, f *fixture, _ *adapterSeam) {
			t.Helper()
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
		}, 1},
		{"a resume registration", func(t *testing.T, f *fixture, seam *adapterSeam) {
			t.Helper()
			seam.responses["session register"] = []fakeadapter.Response{okResp(registerDoc("old-sess", "payments-api", true))}
			if err := f.store().WriteByNative(f.nativeID, &sessionmap.ByNative{BrigadeSessionID: "old-sess", TeamRef: teamRef, SessionName: "payments-api"}); err != nil {
				t.Fatal(err)
			}
			if exit, out, errOut := f.run(SubSessionStart, f.startDoc("resume")); exit != 0 || !strings.Contains(out, "(old-sess)") {
				t.Fatalf("exit %d out %q err %q", exit, out, errOut)
			}
		}, 1},
		{"the re-registration after a heartbeat answered gone", func(t *testing.T, f *fixture, seam *adapterSeam) {
			t.Helper()
			seam.responses["session register"] = []fakeadapter.Response{okResp(registerDoc("brigade-sess-1", "payments-api", false)), okResp(registerDoc("brigade-sess-1", "payments-api", true))}
			seam.responses["session heartbeat"] = []fakeadapter.Response{errResp(protocol.CodeConflict, "")}
			f.spawner.watcherPID = testutil.NewSleeper(t)
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			f.spawner.watcherPID = testutil.NewSleeper(t)
			f.nativeID = "d2c72366-0000-4000-8000-000000000002"
			if exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 || !strings.Contains(out, "(brigade-sess-1)") {
				t.Fatalf("exit %d out %q err %q", exit, out, errOut)
			}
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{
				"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
				"session heartbeat": {okResp(heartbeatDoc())},
			})
			seam.describe = describeWithDescription()
			tc.run(t, f, seam)
			calls := seam.callsFor("session register")
			if len(calls) != tc.wantCalls {
				t.Fatalf("%d register calls, want %d", len(calls), tc.wantCalls)
			}
			for i, c := range calls {
				var reg protocol.SessionRegistration
				if err := json.Unmarshal(c.Stdin, &reg); err != nil {
					t.Fatal(err)
				}
				if reg.SessionDescription != nil {
					t.Errorf("register call %d carries session_description %q", i, *reg.SessionDescription)
				}
				if strings.Contains(string(c.Stdin), "session_description") {
					t.Errorf("register call %d carries the member's bytes: %s", i, c.Stdin)
				}
			}
		})
	}
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
