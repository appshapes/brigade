package hook

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// startDocMode is a SessionStart document carrying a permission_mode.
func (f *fixture) startDocMode(source, mode string) string {
	return f.doc(map[string]any{
		"session_id": f.nativeID, "cwd": f.cwd, "hook_event_name": "SessionStart",
		"source": source, "session_title": "titled-session", "transcript_path": "/never/read.jsonl",
		"permission_mode": mode,
	})
}

// userSettingsFile is the fixture's user settings file, the one the
// parity rule reads.
func (f *fixture) userSettingsFile() string {
	return filepath.Join(f.dirs.ClaudeConfig, "settings.json")
}

// writeOff is the claude_inbound_setting option turned off: the tests of
// the fallback (refuse with the parity warning) run under it, because with
// the option on the hook would write the accept and the warning would
// never come.
var writeOff = config.OptionClaudeInboundSetting + "=false"

// TestSessionStartParityRule is card 50's fallback at SessionStart, the
// write turned off: a session in bypassPermissions whose user settings file
// carries no crossSessionInbound accept gets inbound refuse, the parity
// warning as its own line, refuse in the map, the registration and the
// watcher's environment; a repository accept changes only the warning's
// text; the user file's accept — the fixture's default — gives accept with
// no warning; and a default-mode session without any settings file stays
// accept.
func TestSessionStartParityRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		mode     string
		readFile func(f *fixture) func(string) ([]byte, error)
		want     string
		warning  func(f *fixture) string
	}{
		{"bypass, no settings file", "bypassPermissions", func(*fixture) func(string) ([]byte, error) { return noSettings }, "refuse",
			func(f *fixture) string { return policy.Scan{UserFile: f.userSettingsFile()}.ParityWarning() }},
		{"bypass, repository accept only", "bypassPermissions", func(f *fixture) func(string) ([]byte, error) {
			repo := filepath.Join(f.cwd, ".claude", "settings.json")
			return func(p string) ([]byte, error) {
				if p == repo {
					return []byte(`{"crossSessionInbound": "accept"}`), nil
				}
				return nil, os.ErrNotExist
			}
		}, "refuse", func(f *fixture) string {
			return policy.Scan{UserFile: f.userSettingsFile(), RepoAccept: filepath.Join(f.cwd, ".claude", "settings.json")}.ParityWarning()
		}},
		{"bypass, user accept", "bypassPermissions", nil, "accept", nil},
		{"default, no settings file", "default", func(*fixture) func(string) ([]byte, error) { return noSettings }, "accept", nil},
		{"plan, no settings file", "plan", func(*fixture) func(string) ([]byte, error) { return noSettings }, "accept", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.readFile != nil {
				f.deps.ReadFile = tc.readFile(f)
			}
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			exit, out, errOut := f.run(SubSessionStart, f.startDocMode("startup", tc.mode), writeOff)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			got := lines(out)
			if !strings.Contains(got[0], "; inbound: "+tc.want+";") {
				t.Fatalf("line %q lacks inbound %s", got[0], tc.want)
			}
			switch {
			case tc.warning == nil && len(got) != 1:
				t.Fatalf("%d lines, want the start line alone: %q", len(got), got)
			case tc.warning != nil && (len(got) != 2 || got[1] != tc.warning(f)):
				t.Fatalf("lines %q, want the parity warning %q second", got, tc.warning(f))
			}
			if m := f.mustMap(); m.Inbound != tc.want || m.PermissionMode != tc.mode {
				t.Fatalf("map inbound %q mode %q", m.Inbound, m.PermissionMode)
			}
			var reg protocol.SessionRegistration
			if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
				t.Fatal(err)
			}
			if reg.Inbound != tc.want {
				t.Fatalf("registered inbound %q", reg.Inbound)
			}
			if spec := f.spawner.last(t); envValue(spec.Env, "BRIGADE_TEAM_INBOUND") != tc.want {
				t.Fatalf("watcher BRIGADE_TEAM_INBOUND=%q", envValue(spec.Env, "BRIGADE_TEAM_INBOUND"))
			}
			if tc.warning != nil && !strings.Contains(got[1], f.userSettingsFile()) {
				t.Fatalf("the warning does not name the user settings file %s: %q", f.userSettingsFile(), got[1])
			}
		})
	}
}

// TestPromptRedecidesThePolicy is card 50's fallback at the prompt, the
// write turned off: a session registered under accept (a SessionStart
// document with no mode) whose first bypassPermissions prompt finds no
// accept in the user file flips to refuse — the parity warning is the
// prompt's whole output, the map says refuse, and the live watcher is
// replaced by one started under refuse; the user adds the accept, the next
// prompt flips back — one fixed line, the map says accept, another
// replacement under accept; and a prompt that changes nothing prints
// nothing and replaces nothing. The hold option takes the same road and
// lands on hold.
func TestPromptRedecidesThePolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		option []string
		back   string
	}{
		{"accept option", []string{writeOff}, "accept"},
		{"hold option", []string{writeOff, config.OptionTeamInbound + "=hold"}, "hold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			registered(t, f, nil, tc.option...)
			if m := f.mustMap(); m.Inbound != tc.back {
				t.Fatalf("after start: inbound %q, want %s", m.Inbound, tc.back)
			}
			f.deps.ReadFile = noSettings
			f.spawner.watcherPID = testutil.NewSleeper(t)
			exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"), tc.option...)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			want := policy.Scan{UserFile: f.userSettingsFile()}.ParityWarning() + "\n"
			if out != want {
				t.Fatalf("first bypass prompt printed:\n%q\nwant the parity warning alone:\n%q", out, want)
			}
			if m := f.mustMap(); m.Inbound != "refuse" || m.PermissionMode != "bypassPermissions" {
				t.Fatalf("map after the flip %+v", *m)
			}
			if n := f.spawner.count(); n != 2 {
				t.Fatalf("spawns after the flip %d, want 2 (the watcher replaced)", n)
			}
			if v := envValue(f.spawner.last(t).Env, "BRIGADE_TEAM_INBOUND"); v != "refuse" {
				t.Fatalf("replacement watcher BRIGADE_TEAM_INBOUND=%q", v)
			}
			// Nothing changed: nothing printed, nothing replaced.
			if exit, out, _ := f.run(SubPrompt, f.promptDoc("bypassPermissions"), tc.option...); exit != 0 || out != "" {
				t.Fatalf("unchanged prompt: exit %d out %q", exit, out)
			}
			if n := f.spawner.count(); n != 2 {
				t.Fatalf("spawns after an unchanged prompt %d, want 2", n)
			}
			// The user adds the accept to the user file.
			f.deps.ReadFile = userAcceptSettings(f.dirs.ClaudeConfig)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			exit, out, errOut = f.run(SubPrompt, f.promptDoc("bypassPermissions"), tc.option...)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if want := policyNowLine(policy.Policy(tc.back)) + "\n"; out != want {
				t.Fatalf("after the accept was added:\n%q\nwant:\n%q", out, want)
			}
			if m := f.mustMap(); m.Inbound != tc.back {
				t.Fatalf("map after the accept %+v", *m)
			}
			if n := f.spawner.count(); n != 3 {
				t.Fatalf("spawns after the accept %d, want 3", n)
			}
			if v := envValue(f.spawner.last(t).Env, "BRIGADE_TEAM_INBOUND"); v != tc.back {
				t.Fatalf("replacement watcher BRIGADE_TEAM_INBOUND=%q, want %s", v, tc.back)
			}
			// Back to a prompting mode without any settings file: still
			// the option's policy, with no line (nothing changed).
			f.deps.ReadFile = noSettings
			if exit, out, _ := f.run(SubPrompt, f.promptDoc("default"), tc.option...); exit != 0 || out != "" {
				t.Fatalf("default prompt: exit %d out %q", exit, out)
			}
			if m := f.mustMap(); m.Inbound != tc.back || m.PermissionMode != "default" {
				t.Fatalf("map after the default prompt %+v", *m)
			}
		})
	}
}

// TestPolicyNowLine pins the accept-side line of the prompt hook.
func TestPolicyNowLine(t *testing.T) {
	t.Parallel()
	if got := policyNowLine(policy.Accept); got != "Brigade: this session's inbound policy is now accept; team messages are delivered as they arrive." {
		t.Fatalf("accept: %q", got)
	}
	if got := policyNowLine(policy.Hold); got != "Brigade: this session's inbound policy is now hold; team messages are held for `brigade inbox release`." {
		t.Fatalf("hold: %q", got)
	}
}

// realSettings points the fixture's reads at the real filesystem, so the
// write under test and the scan that follows it see one world; the user
// settings file lives under the fixture's own Claude config directory.
func realSettings(f *fixture) { f.deps.ReadFile = os.ReadFile }

// TestSessionStartWritesTheUserAccept is card 50's write at SessionStart,
// the option on by default: a bypassPermissions session whose user
// settings file lacks crossSessionInbound gets the member added (or the
// file created), the line saying so right after the context line, and
// inbound accept everywhere — map, registration, watcher; a file carrying
// hold is left alone and the hold warning applies; a file that is not a
// JSON object is left alone, the skip line names the reason, and the
// parity warning follows; a default-mode session writes nothing.
func TestSessionStartWritesTheUserAccept(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mode    string
		before  *string
		want    string
		after   string
		lines   func(f *fixture) []string
		wantLen int
	}{
		{"bypass, member added", "bypassPermissions", str("{\n  \"theme\": \"dark\"\n}\n"), "accept",
			"{\n  \"crossSessionInbound\": \"accept\",\n  \"theme\": \"dark\"\n}\n",
			func(f *fixture) []string {
				return []string{policy.EnsureResult{Outcome: policy.EnsureAdded, File: f.userSettingsFile()}.Line()}
			}, 2},
		{"bypass, file created", "bypassPermissions", nil, "accept",
			"{\n  \"crossSessionInbound\": \"accept\"\n}\n",
			func(f *fixture) []string {
				return []string{policy.EnsureResult{Outcome: policy.EnsureCreated, File: f.userSettingsFile()}.Line()}
			}, 2},
		{"bypass, hold present is left alone", "bypassPermissions", str(`{"crossSessionInbound": "hold"}`), "refuse",
			`{"crossSessionInbound": "hold"}`,
			func(f *fixture) []string {
				return []string{policy.Scan{Found: true, Value: "hold", File: f.userSettingsFile(), UserFile: f.userSettingsFile()}.Warning()}
			}, 2},
		{"bypass, not an object", "bypassPermissions", str(`[1, 2]`), "refuse", `[1, 2]`,
			func(f *fixture) []string {
				return []string{
					policy.EnsureResult{Outcome: policy.EnsureSkipped, Reason: policy.ReasonNotAnObject, File: f.userSettingsFile()}.Line(),
					policy.Scan{UserFile: f.userSettingsFile()}.ParityWarning(),
				}
			}, 3},
		{"default mode writes nothing", "default", str("{\n  \"theme\": \"dark\"\n}\n"), "accept", "{\n  \"theme\": \"dark\"\n}\n", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			realSettings(f)
			if tc.before != nil {
				if err := os.WriteFile(f.userSettingsFile(), []byte(*tc.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			exit, out, errOut := f.run(SubSessionStart, f.startDocMode("startup", tc.mode))
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			got := lines(out)
			if len(got) != tc.wantLen || !strings.Contains(got[0], "; inbound: "+tc.want+";") {
				t.Fatalf("lines %q, want %d with inbound %s first", got, tc.wantLen, tc.want)
			}
			if tc.lines != nil {
				for i, want := range tc.lines(f) {
					if got[i+1] != want {
						t.Fatalf("line %d:\n got %q\nwant %q", i+1, got[i+1], want)
					}
				}
			}
			data, err := os.ReadFile(f.userSettingsFile())
			if err != nil || string(data) != tc.after {
				t.Fatalf("user settings file:\n%q (err %v)\nwant:\n%q", data, err, tc.after)
			}
			if m := f.mustMap(); m.Inbound != tc.want {
				t.Fatalf("map inbound %q", m.Inbound)
			}
			var reg protocol.SessionRegistration
			if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
				t.Fatal(err)
			}
			if reg.Inbound != tc.want {
				t.Fatalf("registered inbound %q", reg.Inbound)
			}
			if spec := f.spawner.last(t); envValue(spec.Env, "BRIGADE_TEAM_INBOUND") != tc.want {
				t.Fatalf("watcher BRIGADE_TEAM_INBOUND=%q", envValue(spec.Env, "BRIGADE_TEAM_INBOUND"))
			}
		})
	}
}

// TestPromptWritesTheUserAccept is the write at the prompt: a session
// registered under accept in default mode whose user file lacks the member
// gets it written at the first bypassPermissions prompt — the write's line
// is the prompt's whole output, the policy stays accept, no watcher is
// replaced — and the next prompt prints nothing. When the map held refuse
// (a fallback decided earlier), the same prompt writes, flips to accept
// with the write's line alone, and replaces the watcher.
func TestPromptWritesTheUserAccept(t *testing.T) {
	t.Parallel()
	t.Run("from accept", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		realSettings(f)
		if err := os.WriteFile(f.userSettingsFile(), []byte("{\n  \"theme\": \"dark\"\n}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.spawner.watcherPID = testutil.NewSleeper(t)
		registered(t, f, nil)
		exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"))
		if exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		want := policy.EnsureResult{Outcome: policy.EnsureAdded, File: f.userSettingsFile()}.Line() + "\n"
		if out != want {
			t.Fatalf("printed:\n%q\nwant:\n%q", out, want)
		}
		data, _ := os.ReadFile(f.userSettingsFile())
		if string(data) != "{\n  \"crossSessionInbound\": \"accept\",\n  \"theme\": \"dark\"\n}\n" {
			t.Fatalf("file %q", data)
		}
		if m := f.mustMap(); m.Inbound != "accept" || m.PermissionMode != "bypassPermissions" {
			t.Fatalf("map %+v", *m)
		}
		if n := f.spawner.count(); n != 1 {
			t.Fatalf("spawns %d, want the one from SessionStart", n)
		}
		if exit, out, _ := f.run(SubPrompt, f.promptDoc("bypassPermissions")); exit != 0 || out != "" {
			t.Fatalf("second prompt: exit %d out %q", exit, out)
		}
	})
	t.Run("from refuse", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		realSettings(f)
		f.spawner.watcherPID = testutil.NewSleeper(t)
		registered(t, f, nil, writeOff)
		// The fallback decided refuse at an earlier bypass prompt.
		if exit, _, _ := f.run(SubPrompt, f.promptDoc("bypassPermissions"), writeOff); exit != 0 {
			t.Fatal(exit)
		}
		if m := f.mustMap(); m.Inbound != "refuse" {
			t.Fatalf("precondition: map inbound %q, want refuse", m.Inbound)
		}
		f.spawner.watcherPID = testutil.NewSleeper(t)
		exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"))
		if exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		want := policy.EnsureResult{Outcome: policy.EnsureCreated, File: f.userSettingsFile()}.Line() + "\n"
		if out != want {
			t.Fatalf("printed:\n%q\nwant the write's line alone:\n%q", out, want)
		}
		if m := f.mustMap(); m.Inbound != "accept" {
			t.Fatalf("map inbound %q, want accept", m.Inbound)
		}
		if n := f.spawner.count(); n != 3 {
			t.Fatalf("spawns %d, want 3 (start, the refuse replacement, the accept replacement)", n)
		}
		if v := envValue(f.spawner.last(t).Env, "BRIGADE_TEAM_INBOUND"); v != "accept" {
			t.Fatalf("replacement watcher BRIGADE_TEAM_INBOUND=%q", v)
		}
	})
}

func str(s string) *string { return &s }
