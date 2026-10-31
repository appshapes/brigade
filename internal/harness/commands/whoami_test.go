package commands

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// noWatcherLine is the delivery line of the plain fixture: a session with
// no inbox socket, nothing delivered and nothing held.
const noWatcherLine = "delivery: no watcher: this session has no inbox socket; no delivery recorded; 0 held"

// shortIDLine is the second line of every fixture's human form (card 157):
// the last five characters of selfSessionID, as the roster's SESSION cell
// shows them.
const shortIDLine = "short id: aaaaa"

// TestWhoamiLine pins the 6.4 layout from the map and the cached describe:
// exactly one spawn (the describe), no network.
func TestWhoamiLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := Whoami(f.inv(f.sessionEnv(), "")); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	want := "session " + selfSessionID + " \"payments-api\" in team \"ops\" (adapter " +
		fakeadapter.AdapterName + " " + fakeadapter.AdapterVersion + "); inbound: accept\n" + shortIDLine + "\nframe: open\n" + noWatcherLine + "\n"
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
	if strings.Count(out, "\n") != 4 || strings.Contains(out, "<system-reminder>") || strings.Contains(out, "<cross-session-message>") {
		t.Errorf("stdout = %q", out)
	}
}

// TestWhoamiTerminalLine: the plugin binary's path is the human form's
// line after the short id and nothing else — absent when the map does not know it, and
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
	if len(got) != 5 || got[1] != shortIDLine || got[2] != "terminal: /opt/plugins/brigade/bin/brigade" || got[3] != "frame: open" || got[4] != noWatcherLine {
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
	// whole-output comparison: an empty plugin_bin prints no terminal line at
	// all, never an empty `terminal: `.
	f.writeMap(t, f.byPID())
	f.out.Reset()
	if err := Whoami(f.inv(f.sessionEnv(), "")); err != nil {
		t.Fatal(err)
	}
	if got := f.out.String(); strings.Contains(got, "terminal:") || strings.Count(got, "\n") != 4 {
		t.Errorf("an empty plugin_bin still printed a terminal line: %q", got)
	}
}

// TestWhoamiShortIDIsATail (card 157): the short id is the tail of the id
// as the same form prints it — the breakers dropped first, so a hostile id
// cannot spend its five characters on them — the id whole when shorter,
// and NO line (and "" in --json) for an id that sanitises to nothing:
// never the roster's "?", which `brigade send` would refuse as an address.
func TestWhoamiShortIDIsATail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ id, wantLine, wantJSON string }{
		{"9f3c1a20-5d4e-4a7b-8c11-aa0000000001", "short id: 00001", "00001"},
		{"a\"<>bcdefghij", "short id: fghij", "fghij"},
		{"\"xyz", "short id: xyz", "xyz"},
		{"\"<>", "", ""},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			m := f.byPID()
			m.BrigadeSessionID = tc.id
			f.writeMap(t, m)
			if err := Whoami(f.inv(f.sessionEnv(), "")); err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(f.out.String(), "\n"), "\n")
			var lines []string
			for _, l := range got {
				if strings.HasPrefix(l, "short id:") {
					lines = append(lines, l)
				}
			}
			switch {
			case tc.wantLine == "" && len(lines) != 0:
				t.Fatalf("an id that sanitises to nothing printed %q", lines)
			case tc.wantLine != "" && (len(lines) != 1 || lines[0] != tc.wantLine || got[1] != tc.wantLine):
				t.Fatalf("stdout = %q, want the second line %q", f.out.String(), tc.wantLine)
			}
			if strings.Contains(f.out.String(), "?") {
				t.Fatalf("the roster's placeholder reached whoami: %q", f.out.String())
			}
			f.out.Reset()
			inv := f.inv(f.sessionEnv(), "")
			inv.JSON = true
			if err := Whoami(inv); err != nil {
				t.Fatal(err)
			}
			_, result := envelopeOf(t, f.out.String())
			full, _ := result["session_id"].(string)
			short, _ := result["short_session_id"].(string)
			if short != tc.wantJSON || !strings.HasSuffix(full, short) {
				t.Fatalf("session_id %q, short_session_id %q, want %q and a suffix", full, short, tc.wantJSON)
			}
		})
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
			if len(got) != 4 || got[1] != shortIDLine || got[2] != tc.want || got[3] != noWatcherLine {
				t.Fatalf("stdout = %q, want the third line %q", f.out.String(), tc.want)
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
	if result["session_id"] != selfSessionID || result["short_session_id"] != "aaaaa" || result["self_session_id"] != selfSessionID ||
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
