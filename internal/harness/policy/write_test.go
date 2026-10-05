package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture is a real user settings file under a temp config dir, or
// its absence.
func writeFixture(t *testing.T, content *string, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "claude-config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(dir, "settings.json")
	if content != nil {
		if err := os.WriteFile(user, []byte(*content), mode); err != nil {
			t.Fatal(err)
		}
	}
	return user
}

func str(s string) *string { return &s }

// TestEnsureUserAcceptWrites is the write in its allowed shapes: a missing
// file is created with the one member at 0600; an object gains the member
// first, by text, every other byte and the file's mode as they were.
func TestEnsureUserAcceptWrites(t *testing.T) {
	t.Parallel()
	const member = "\"crossSessionInbound\": \"accept\""
	for _, tc := range []struct {
		name    string
		before  *string
		mode    os.FileMode
		outcome string
		after   string
	}{
		{"missing file", nil, 0, EnsureCreated, "{\n  " + member + "\n}\n"},
		{"two-space object", str("{\n  \"attribution\": {\n    \"commit\": \"\"\n  },\n  \"theme\": \"dark\"\n}\n"), 0o644, EnsureAdded,
			"{\n  " + member + ",\n  \"attribution\": {\n    \"commit\": \"\"\n  },\n  \"theme\": \"dark\"\n}\n"},
		{"tabs and no final newline", str("{\n\t\"theme\": \"dark\"\n}"), 0o600, EnsureAdded, "{\n  " + member + ",\n\t\"theme\": \"dark\"\n}"},
		{"one line", str(`{"theme":"dark","x":[1,2,{"y":null}]}`), 0o640, EnsureAdded, "{\n  " + member + `,"theme":"dark","x":[1,2,{"y":null}]}`},
		{"empty object", str("{}"), 0o644, EnsureAdded, "{\n  " + member + "\n}"},
		{"empty object with newline inside", str("{\n}\n"), 0o644, EnsureAdded, "{\n  " + member + "\n\n}\n"},
		{"leading whitespace", str("\n  {\"theme\": \"dark\"}\n"), 0o644, EnsureAdded, "\n  {\n  " + member + ",\"theme\": \"dark\"}\n"},
		{"nested mention is not the member", str(`{"permissions": {"crossSessionInbound": "refuse"}}`), 0o644, EnsureAdded,
			"{\n  " + member + `,"permissions": {"crossSessionInbound": "refuse"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			user := writeFixture(t, tc.before, tc.mode)
			res := EnsureUserAccept(user, EnsureIO{})
			if res.Outcome != tc.outcome || res.Reason != "" || res.File != user {
				t.Fatalf("result %+v, want %s", res, tc.outcome)
			}
			got, err := os.ReadFile(user)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.after {
				t.Fatalf("file:\n%q\nwant:\n%q", got, tc.after)
			}
			fi, err := os.Stat(user)
			if err != nil {
				t.Fatal(err)
			}
			wantMode := tc.mode
			if tc.before == nil {
				wantMode = 0o600
			}
			if fi.Mode().Perm() != wantMode {
				t.Fatalf("mode %v, want %v", fi.Mode().Perm(), wantMode)
			}
			// The scan now sees the user accept, and nothing else.
			s := ScanNative(filepath.Dir(user), "", nil)
			if !s.UserAccept || s.Found {
				t.Fatalf("scan after the write: %+v", s)
			}
			// Idempotent: a second call finds it present and writes nothing.
			before, _ := os.Stat(user)
			if res := EnsureUserAccept(user, EnsureIO{}); res.Outcome != EnsurePresent {
				t.Fatalf("second call %+v, want present", res)
			}
			if after, _ := os.Stat(user); !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("a present member rewrote the file")
			}
		})
	}
}

// TestEnsureUserAcceptLeavesTheFileAlone: a file that already has the
// member — with any value — a file that is not one JSON object, a symlink,
// an over-large file, an unreadable or unwritable one, and a missing
// configuration directory are left byte for byte as they are, with the
// fixed reason.
func TestEnsureUserAcceptLeavesTheFileAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		before  string
		io      func(user string) EnsureIO
		outcome string
		reason  string
	}{
		{"accept present", `{"crossSessionInbound": "accept"}`, nil, EnsurePresent, ""},
		{"hold present", `{"theme": "dark", "crossSessionInbound": "hold"}`, nil, EnsurePresent, ""},
		{"refuse present", `{"crossSessionInbound": "refuse"}`, nil, EnsurePresent, ""},
		{"odd value present", `{"crossSessionInbound": true}`, nil, EnsurePresent, ""},
		{"array", `[{"theme": "dark"}]`, nil, EnsureSkipped, ReasonNotAnObject},
		{"scalar", `"dark"`, nil, EnsureSkipped, ReasonNotAnObject},
		{"malformed", `{"theme": "dark"`, nil, EnsureSkipped, ReasonNotAnObject},
		{"duplicate member", `{"a": 1, "a": 2}`, nil, EnsureSkipped, ReasonNotAnObject},
		{"empty file", ``, nil, EnsureSkipped, ReasonNotAnObject},
		{"byte order mark", "\xEF\xBB\xBF{}", nil, EnsureSkipped, ReasonNotAnObject},
		{"too large", `{"pad": "` + strings.Repeat("x", MaxSettingsBytes) + `"}`, nil, EnsureSkipped, ReasonTooLarge},
		{"unreadable", `{"theme": "dark"}`, func(string) EnsureIO {
			return EnsureIO{ReadFile: func(string) ([]byte, error) { return nil, errors.New("permission denied") }}
		}, EnsureSkipped, ReasonUnreadable},
		{"unwritable", `{"theme": "dark"}`, func(string) EnsureIO {
			return EnsureIO{WriteFile: func(string, []byte, os.FileMode) error { return errors.New("read-only") }}
		}, EnsureSkipped, ReasonUnwritable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			user := writeFixture(t, &tc.before, 0o644)
			io := EnsureIO{}
			if tc.io != nil {
				io = tc.io(user)
			}
			res := EnsureUserAccept(user, io)
			if res.Outcome != tc.outcome || res.Reason != tc.reason {
				t.Fatalf("result %+v, want %s/%s", res, tc.outcome, tc.reason)
			}
			got, err := os.ReadFile(user)
			if err != nil || string(got) != tc.before {
				t.Fatalf("file changed: %q (err %v)", got, err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		target := writeFixture(t, str(`{"theme": "dark"}`), 0o644)
		link := filepath.Join(t.TempDir(), "settings.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if res := EnsureUserAccept(link, EnsureIO{}); res.Outcome != EnsureSkipped || res.Reason != ReasonSymlink {
			t.Fatalf("symlink: %+v", res)
		}
		if got, _ := os.ReadFile(target); string(got) != `{"theme": "dark"}` {
			t.Fatalf("target changed: %q", got)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()
		user := filepath.Join(t.TempDir(), "no-such-config", "settings.json")
		if res := EnsureUserAccept(user, EnsureIO{}); res.Outcome != EnsureSkipped || res.Reason != ReasonNoConfigDir {
			t.Fatalf("missing dir: %+v", res)
		}
		if _, err := os.Stat(filepath.Dir(user)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the directory was created")
		}
	})
	t.Run("no config dir known", func(t *testing.T) {
		t.Parallel()
		if res := EnsureUserAccept("", EnsureIO{}); res.Outcome != EnsureSkipped || res.Reason != ReasonNoConfigDir {
			t.Fatalf("no dir: %+v", res)
		}
	})
}

// TestEnsureResultLine pins the three lines and the silence of present.
func TestEnsureResultLine(t *testing.T) {
	t.Parallel()
	const undo = ` Remove the line, or set the plugin option claude_inbound_setting to false, to undo.`
	added := EnsureResult{Outcome: EnsureAdded, File: "/home/u/.claude-work/settings.json"}.Line()
	if added != `Brigade: added "crossSessionInbound": "accept" to /home/u/.claude-work/settings.json, so Claude Code delivers team messages to this session, which bypasses permission prompts (it would hold each one for a dialog otherwise); Claude Code picks the change up within the session.`+undo {
		t.Fatalf("added: %q", added)
	}
	created := EnsureResult{Outcome: EnsureCreated, File: "/home/u/.claude-work/settings.json"}.Line()
	if created != `Brigade: wrote /home/u/.claude-work/settings.json with "crossSessionInbound": "accept", so Claude Code delivers team messages to this session, which bypasses permission prompts (it would hold each one for a dialog otherwise); a new settings file is read at your next session start.`+undo {
		t.Fatalf("created: %q", created)
	}
	skipped := EnsureResult{Outcome: EnsureSkipped, Reason: ReasonNotAnObject, File: "/f/settings.json"}.Line()
	if skipped != `Brigade: could not add "crossSessionInbound": "accept" to /f/settings.json (not_a_json_object); add it yourself to receive team messages in this session.` {
		t.Fatalf("skipped: %q", skipped)
	}
	if got := (EnsureResult{Outcome: EnsureSkipped, Reason: ReasonNoConfigDir}).Line(); !strings.Contains(got, "to your Claude Code user settings.json (no_config_dir)") {
		t.Fatalf("no file: %q", got)
	}
	if (EnsureResult{Outcome: EnsurePresent, File: "/f"}).Line() != "" {
		t.Fatal("present must say nothing")
	}
	for _, l := range []string{added, created, skipped} {
		if strings.Contains(l, "\n") {
			t.Errorf("not one line: %q", l)
		}
	}
}
