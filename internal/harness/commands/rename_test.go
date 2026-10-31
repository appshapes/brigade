package commands

import (
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/protocol"
)

// renameStamp is the fixture session's pending-name file.
func (f *fixture) renameStamp() string { return config.RenameStamp(f.stateDir, fixturePID) }

// renameDoc decodes the --json envelope of `brigade rename`.
func renameDoc(t *testing.T, out string) renameResult {
	t.Helper()
	var env struct {
		OK     bool         `json:"ok"`
		Result renameResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || !env.OK {
		t.Fatalf("json %q: %v (ok %v)", out, err, env.OK)
	}
	return env.Result
}

// TestRenameRecordsThePendingName: a clean name is folded onto one line,
// written as `<claude_session_id> <name>` in a private file keyed by the
// session's pid, echoed on stdout, and sent to nobody — the command spawns
// nothing, not even the describe.
func TestRenameRecordsThePendingName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := Rename(f.inv(f.sessionEnv(), "  174-let-a-session\n\trename-itself \n"), RenameOptions{}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if want := "recorded: 174-let-a-session rename-itself" + RenameRecordedSuffix + "\n"; f.out.String() != want {
		t.Fatalf("stdout = %q, want %q", f.out.String(), want)
	}
	if f.rec.count() != 0 {
		t.Fatalf("%d children spawned for a rename", f.rec.count())
	}
	data, err := os.ReadFile(f.renameStamp())
	if err != nil {
		t.Fatalf("pending name: %v", err)
	}
	if string(data) != "native-1 174-let-a-session rename-itself\n" {
		t.Fatalf("pending name = %q", data)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(f.renameStamp())
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("pending name mode %o, want private", fi.Mode().Perm())
		}
	}
}

// TestRenameRefusesInputBeforeTouchingTheMap: every input refusal —
// empty, over the cap, not UTF-8, a credential shape, a local path,
// positional arguments, a terminal on stdin — is answered before the map
// is read, with no file written, nothing spawned and nothing on stdout.
func TestRenameRefusesInputBeforeTouchingTheMap(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the home-directory refusal compares unix paths")
	}
	for _, tc := range []struct {
		name   string
		stdin  string
		args   []string
		tty    bool
		code   protocol.Code
		reason string
	}{
		{"empty", "", nil, false, protocol.CodeInvalidInput, doing.ReasonEmpty},
		{"whitespace only", " \n\t\n", nil, false, protocol.CodeInvalidInput, doing.ReasonEmpty},
		{"one code point over the cap", strings.Repeat("é", config.MaxSessionNameChars+1), nil, false, protocol.CodeInvalidInput, doing.ReasonTooLong},
		{"not utf-8", "ok\xff\xfe", nil, false, protocol.CodeInvalidInput, doing.ReasonNotUTF8},
		{"a join secret", "brg1.team-7.deadbeefdeadbeefdeadbeefdeadbeef", nil, false, protocol.CodeInvalidInput, doing.ReasonSecretShaped},
		{"the messaging token", "named " + msgTok, nil, false, protocol.CodeInvalidInput, doing.ReasonSecretShaped},
		{"a local path", "/work/repo/main.go", nil, false, protocol.CodeInvalidInput, doing.ReasonLocalPath},
		{"the home directory", "in HOMEDIR now", nil, false, protocol.CodeInvalidInput, doing.ReasonLocalPath},
		{"a positional argument", "fine", []string{"174-the-card"}, false, protocol.CodeUsage, ""},
		{"a terminal on stdin", "typed", nil, true, protocol.CodeUsage, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			stdin := strings.ReplaceAll(tc.stdin, "HOMEDIR", f.dirs.Home)
			inv := f.inv(f.sessionEnv("CLAUDE_CODE_MESSAGING_TOKEN="+msgTok), stdin, tc.args...)
			if tc.tty {
				inv.Deps.IsTerminal = func(io.Reader) bool { return true }
			}
			err := Rename(inv, RenameOptions{})
			perr := wantCodeErr(t, err, tc.code, tc.reason)
			if tc.code == protocol.CodeInvalidInput {
				if perr.Details["field"] != renameField {
					t.Errorf("details %v", perr.Details)
				}
				if tc.reason == doing.ReasonTooLong && (perr.Details["limit"] != "200" || perr.Details["unit"] != "codepoints") {
					t.Errorf("too_long details %v", perr.Details)
				}
			} else {
				if tc.args != nil && perr.Message != RenameUsage {
					t.Errorf("usage = %q", perr.Message)
				}
				if tc.tty && !strings.Contains(perr.Message, "heredoc") {
					t.Errorf("usage = %q, want the heredoc hint", perr.Message)
				}
			}
			if strings.Contains(perr.Message, msgTok) || strings.Contains(perr.Message, f.dirs.Home) {
				t.Errorf("the message echoes the text: %q", perr.Message)
			}
			if _, err := os.Stat(f.renameStamp()); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a refused name left a pending file (%v)", err)
			}
			if f.rec.count() != 0 || f.out.Len() != 0 {
				t.Errorf("spawned %d, stdout %q on a refusal", f.rec.count(), f.out.String())
			}
		})
	}
}

// TestRenameClearDropsThePendingName: --clear removes a pending name,
// reads nothing from stdin, and is a no-op with the same line when none
// is pending.
func TestRenameClearDropsThePendingName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := Rename(f.inv(f.sessionEnv(), "174-the-card\n"), RenameOptions{}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "never read")
	inv.Deps.IsTerminal = func(io.Reader) bool { return true }
	for range 2 {
		if err := Rename(inv, RenameOptions{Clear: true}); err != nil {
			t.Fatalf("rename --clear: %v", err)
		}
		if f.out.String() != RenameClearedLine+"\n" {
			t.Fatalf("stdout = %q", f.out.String())
		}
		f.out.Reset()
		if _, err := os.Stat(f.renameStamp()); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("pending name still there (%v)", err)
		}
	}
	if f.rec.count() != 0 {
		t.Fatalf("%d children spawned for --clear", f.rec.count())
	}
}

// TestRenameOutsideASessionFails: a terminal has no session to name.
func TestRenameOutsideASessionFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	err := Rename(f.inv(f.terminalEnv(), "174-the-card\n"), RenameOptions{})
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code == protocol.CodeInternal {
		t.Fatalf("err = %v, want a classified refusal", err)
	}
	if _, err := os.Stat(f.renameStamp()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a terminal left a pending file (%v)", err)
	}
}

// TestRenameOnAnotherHostRecordsNothing: a Muse Code or Codex CLI map
// gets the fixed not-recorded line at exit 0 (and the other_host reason
// under --json), no file and nothing spawned; a map that names Claude
// Code, or none (written before the member existed), records.
func TestRenameOnAnotherHostRecordsNothing(t *testing.T) {
	t.Parallel()
	for _, host := range []string{string(config.HostMuse), string(config.HostCodex), string(config.HostGrok)} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			m := f.byPID()
			m.Harness = host
			f.writeMap(t, m)
			if err := Rename(f.inv(f.sessionEnv(), "174-the-card\n"), RenameOptions{}); err != nil {
				t.Fatalf("rename: %v", err)
			}
			if f.out.String() != RenameNotRecordedLine+"\n" {
				t.Fatalf("stdout = %q", f.out.String())
			}
			if _, err := os.Stat(f.renameStamp()); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("another host left a pending file (%v)", err)
			}
			f.out.Reset()
			inv := f.inv(f.sessionEnv(), "174-the-card\n")
			inv.JSON = true
			if err := Rename(inv, RenameOptions{}); err != nil {
				t.Fatalf("rename --json: %v", err)
			}
			got := renameDoc(t, f.out.String())
			if got.Recorded || got.Reason != RenameReasonOtherHost || got.Note != RenameNotRecordedLine || got.SelfSessionID != selfSessionID {
				t.Fatalf("result %+v", got)
			}
			if f.rec.count() != 0 {
				t.Fatalf("%d children spawned", f.rec.count())
			}
		})
	}
	for _, host := range []string{"", string(config.HostClaude)} {
		t.Run("records on "+host, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			m := f.byPID()
			m.Harness = host
			f.writeMap(t, m)
			if err := Rename(f.inv(f.sessionEnv(), "174-the-card\n"), RenameOptions{}); err != nil {
				t.Fatalf("rename: %v", err)
			}
			if _, err := os.Stat(f.renameStamp()); err != nil {
				t.Fatalf("no pending file: %v", err)
			}
		})
	}
}

// TestRenameJSON pins the --json result of a recorded name.
func TestRenameJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	inv := f.inv(f.sessionEnv(), "174-the-card\n")
	inv.JSON = true
	if err := Rename(inv, RenameOptions{}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got := renameDoc(t, f.out.String())
	if got.SelfSessionID != selfSessionID || got.Name != "174-the-card" || !got.Recorded || got.Cleared || got.Reason != "" || got.Note != RenameNote {
		t.Fatalf("result %+v", got)
	}
}
