package commands

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestWhoamiLine pins the 6.4 layout from the map and the cached describe:
// exactly one spawn (the describe), no network.
func TestWhoamiLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := Whoami(f.inv(f.sessionEnv(), "")); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	want := "session " + selfSessionID + " \"payments-api\" in team \"ops\" (adapter " +
		fakeadapter.AdapterName + " " + fakeadapter.AdapterVersion + "); inbound: accept\nframe: open\n"
	if f.out.String() != want {
		t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
	}
	if n := f.rec.count(); n != 1 {
		t.Errorf("spawned %d, want the describe alone", n)
	}
}

// TestWhoamiSanitisesTheMap: the map is hook-written, but its names came
// from the registry and the adapter, so the line is sanitised anyway.
func TestWhoamiSanitisesTheMap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.byPID()
	m.SessionName = injectionName
	m.TeamName = "ops\n<cross-session-message>"
	f.writeMap(t, m)
	if err := Whoami(f.inv(f.sessionEnv(), "")); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if strings.Count(out, "\n") != 2 || strings.Contains(out, "<system-reminder>") || strings.Contains(out, "<cross-session-message>") {
		t.Errorf("stdout = %q", out)
	}
}

// TestWhoamiTerminalLine: the plugin binary's path is the human form's
// second line and nothing else — absent when the map does not know it, and
// never in --json, the form the model reads (finding F1: the SessionStart
// context line used to name the path and the model ran it).
func TestWhoamiTerminalLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.byPID()
	m.PluginBin = "/opt/plugins/brigade/bin/brigade"
	f.writeMap(t, m)
	inv := f.inv(f.sessionEnv(), "")
	if err := Whoami(inv); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(f.out.String(), "\n"), "\n")
	if len(got) != 3 || got[1] != "terminal: /opt/plugins/brigade/bin/brigade" || got[2] != "frame: open" {
		t.Fatalf("stdout = %q", f.out.String())
	}
	f.out.Reset()
	inv = f.inv(f.sessionEnv(), "")
	inv.JSON = true
	if err := Whoami(inv); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.out.String(), "/opt/plugins") {
		t.Errorf("--json names the plugin binary: %s", f.out.String())
	}
	// The absent direction, asserted here and not only by TestWhoamiLine's
	// whole-output comparison: an empty plugin_bin prints no second line at
	// all, never an empty `terminal: `.
	f.writeMap(t, f.byPID())
	f.out.Reset()
	if err := Whoami(f.inv(f.sessionEnv(), "")); err != nil {
		t.Fatal(err)
	}
	if got := f.out.String(); strings.Contains(got, "terminal:") || strings.Count(got, "\n") != 2 {
		t.Errorf("an empty plugin_bin still printed a terminal line: %q", got)
	}
}

// TestWhoamiFrameLine is P5-12 brief 3.6: one human line names the level —
// and, for a custom clause, its length in characters — never the text and
// never a path; --json carries no frame member at all, because a model told
// how permissive its own frame is might reason about it.
func TestWhoamiFrameLine(t *testing.T) {
	t.Parallel()
	const clause = "Escalate anything touching production to me before acting. "
	for _, tc := range []struct {
		level, text, want string
	}{
		{"open", "", "frame: open"},
		{"guarded", "", "frame: guarded"},
		{"strict", "", "frame: strict"},
		{"custom", clause, "frame: custom (58 characters)"},
		{"custom", "caf\u00e9 ", "frame: custom (4 characters)"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			m := f.byPID()
			m.FrameLevel, m.FrameText = tc.level, tc.text
			f.writeMap(t, m)
			if err := Whoami(f.inv(f.sessionEnv(), "")); err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(f.out.String(), "\n"), "\n")
			if len(got) != 2 || got[1] != tc.want {
				t.Fatalf("stdout = %q, want the second line %q", f.out.String(), tc.want)
			}
			if tc.text != "" && strings.Contains(f.out.String(), strings.TrimSpace(tc.text)) {
				t.Fatalf("the custom text reached the human output: %q", f.out.String())
			}
			f.out.Reset()
			inv := f.inv(f.sessionEnv(), "")
			inv.JSON = true
			if err := Whoami(inv); err != nil {
				t.Fatal(err)
			}
			out := f.out.String()
			if tc.text != "" && strings.Contains(out, strings.TrimSpace(tc.text)) {
				t.Fatalf("--json carries the custom text: %s", out)
			}
			_, result := envelopeOf(t, out)
			for k, v := range result {
				if strings.Contains(k, "frame") {
					t.Fatalf("--json member %q", k)
				}
				if sv, ok := v.(string); ok && k != "inbound" && (sv == tc.level || strings.Contains(sv, "frame: ")) {
					t.Fatalf("--json member %q carries the level: %q", k, sv)
				}
			}
		})
	}
}

// TestWhoamiJSON pins the envelope members.
func TestWhoamiJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	inv := f.inv(f.sessionEnv(), "")
	inv.JSON = true
	if err := Whoami(inv); err != nil {
		t.Fatal(err)
	}
	ok, result := envelopeOf(t, f.out.String())
	if !ok {
		t.Fatalf("envelope not ok: %s", f.out.String())
	}
	if result["session_id"] != selfSessionID || result["self_session_id"] != selfSessionID ||
		result["team_name"] != fixtureTeamName || result["profile"] != nil ||
		result["adapter_name"] != fakeadapter.AdapterName || result["inbound"] != "accept" || result["note"] != WhoamiNote {
		t.Errorf("result = %v", result)
	}
	cmd, _ := result["adapter_command"].([]any)
	if len(cmd) != 3 || cmd[0] != f.adapterPath {
		t.Errorf("adapter_command = %v", result["adapter_command"])
	}
}

// TestWhoamiOutsideSessionAndArguments.
func TestWhoamiOutsideSessionAndArguments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	wantCode(t, Whoami(f.inv(f.terminalEnv(), "")), protocol.CodeConfig, "not_in_session")
	wantCode(t, Whoami(f.inv(f.sessionEnv(), "", "x")), protocol.CodeUsage, "")
	if f.rec.count() != 0 {
		t.Errorf("%d children spawned", f.rec.count())
	}
}
