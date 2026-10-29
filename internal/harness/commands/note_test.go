package commands

import (
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// The state-folder fixtures of card 162: a map that names a state folder
// under a checkout of the fixture's own, and the session's entry in it.

// withStateFolder rewrites the fixture's map with the sync members and a
// state folder, and returns the folder.
func (f *fixture) withStateFolder(t *testing.T) string {
	t.Helper()
	root := filepath.Join(f.dirs.Root, "checkout")
	folder := filepath.Join(root, ".brigade", "state")
	m := f.byPID()
	m.SyncAdapter, m.SyncFolders, m.SyncRoot, m.SyncScope = "syncthing", []string{".brigade/state"}, root, "payments"
	m.StateFolder = folder
	f.writeMap(t, m)
	return folder
}

// stateStore is the fixture session's entry store.
func (f *fixture) stateStore(folder string) state.Store {
	return state.Store{StateDir: f.stateDir, Folder: folder}
}

// seedEntry writes the session's entry as the hooks would have.
func (f *fixture) seedEntry(t *testing.T, folder string) {
	t.Helper()
	e := state.New(selfSessionID, selfSessionName, fixtureNow.Add(-time.Hour))
	e.Member, e.Repository, e.Harness, e.Inbound = "alice@example.com", "payments", "claude-code", protocol.InboundAccept
	if err := f.stateStore(folder).Save(e); err != nil {
		t.Fatal(err)
	}
}

// mustEntry loads the session's canonical entry.
func (f *fixture) mustEntry(t *testing.T, folder string) *state.Entry {
	t.Helper()
	e, err := f.stateStore(folder).Load(selfSessionID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestNoteRecordsInTheEntry: a note is cleaned, appended to the session's
// entry in both copies with the clock's time, echoed on stdout, and sent
// to nobody — the command spawns nothing, not even the describe.
func TestNoteRecordsInTheEntry(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	if err := Note(f.inv(f.sessionEnv(), "  Done:  the entry\n  and the facts layer\n")); err != nil {
		t.Fatalf("note: %v", err)
	}
	if f.out.String() != "recorded: Done: the entry and the facts layer\n" {
		t.Fatalf("stdout = %q", f.out.String())
	}
	if f.rec.count() != 0 {
		t.Fatalf("%d children spawned for a note", f.rec.count())
	}
	e := f.mustEntry(t, folder)
	if len(e.Notes) != 1 || e.Notes[0].Text != "Done: the entry and the facts layer" || !e.Notes[0].At.Equal(fixtureNow) || !e.UpdatedAt.Equal(fixtureNow) || e.Member != "alice@example.com" {
		t.Fatalf("entry %+v", *e)
	}
	if theirs := state.ReadFolder(folder); len(theirs) != 1 || len(theirs[0].Notes) != 1 {
		t.Fatalf("folder copy %+v", theirs)
	}
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "Next: the Stop hook")
	inv.JSON = true
	if err := Note(inv); err != nil {
		t.Fatal(err)
	}
	ok, result := envelopeOf(t, f.out.String())
	if !ok || result["self_session_id"] != selfSessionID || result["text"] != "Next: the Stop hook" || result["recorded"] != true || result["note"] != NoteNote {
		t.Fatalf("result %v", result)
	}
	if e := f.mustEntry(t, folder); len(e.Notes) != 2 {
		t.Fatalf("entry after the second note: %d notes", len(e.Notes))
	}
}

// TestNoteRefusals: every refusal — the text's (before anything is
// resolved), a terminal, an argument, no session, no state folder, no
// entry yet — spawns nothing and writes nothing, with its code and reason.
func TestNoteRefusals(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for _, tc := range []struct {
		name   string
		stdin  string
		args   []string
		tty    bool
		setup  func(t *testing.T, f *fixture) []string // the environment, after the fixture's state is laid out
		code   protocol.Code
		reason string
	}{
		{"empty", "\n", nil, false, nil, protocol.CodeInvalidInput, doing.ReasonEmpty},
		{"over the cap", strings.Repeat("x", state.MaxNoteChars+1), nil, false, nil, protocol.CodeInvalidInput, doing.ReasonTooLong},
		{"over the read limit", strings.Repeat("x", doingReadLimit+1), nil, false, nil, protocol.CodeInvalidInput, doing.ReasonTooLong},
		{"not utf-8", "ok\xff", nil, false, nil, protocol.CodeInvalidInput, doing.ReasonNotUTF8},
		{"a credential", "Blocked: need sbp_0123456789abcdef", nil, false, nil, protocol.CodeInvalidInput, doing.ReasonSecretShaped},
		{"a local path", "Done: edited " + home + "/x.go", nil, false, nil, protocol.CodeInvalidInput, doing.ReasonLocalPath},
		{"an argument", "fine", []string{"Done: x"}, false, nil, protocol.CodeUsage, ""},
		{"a terminal", "typed", nil, true, nil, protocol.CodeUsage, ""},
		{"outside a session", "Done: x", nil, false, func(t *testing.T, f *fixture) []string { t.Helper(); return f.terminalEnv() }, protocol.CodeConfig, "not_in_session"},
		{"no state folder", "Done: x", nil, false, nil, protocol.CodeConfig, ReasonNoStateFolder},
		{"no entry yet", "Done: x", nil, false, func(t *testing.T, f *fixture) []string { t.Helper(); f.withStateFolder(t); return f.sessionEnv() }, protocol.CodeConfig, ReasonNoStateEntry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.reason == doing.ReasonLocalPath && runtime.GOOS == "windows" {
				t.Skip("the local-path refusal compares unix paths (as the doing tests skip it)")
			}
			f := newFixture(t)
			env := f.sessionEnv("HOME=" + home)
			if tc.setup != nil {
				env = tc.setup(t, f)
			}
			inv := f.inv(env, tc.stdin, tc.args...)
			if tc.tty {
				inv.Deps.IsTerminal = func(io.Reader) bool { return true }
			}
			perr := wantCodeErr(t, Note(inv), tc.code, tc.reason)
			if tc.reason == doing.ReasonTooLong && (perr.Details["limit"] != "1000" || perr.Details["unit"] != "codepoints") {
				t.Errorf("too_long details %v", perr.Details)
			}
			if strings.Contains(perr.Message, home) || strings.Contains(perr.Message, "sbp_") {
				t.Errorf("the message echoes the text: %q", perr.Message)
			}
			if f.rec.count() != 0 || f.out.Len() != 0 {
				t.Errorf("spawns %d stdout %q on a refusal", f.rec.count(), f.out.String())
			}
		})
	}
}

// TestDoingRecordsTheHistory: a published doing line is appended to the
// entry's history when the project shares a state folder and the entry
// is there; a --clear records nothing; a session without a state folder
// records nothing and the publish is as before.
func TestDoingRecordsTheHistory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withStateFolder(t)
	f.seedEntry(t, folder)
	f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
	if err := Doing(f.inv(f.sessionEnv(), "implementing the digest\n"), DoingOptions{}); err != nil {
		t.Fatal(err)
	}
	e := f.mustEntry(t, folder)
	if len(e.Doing) != 1 || e.Doing[0].Text != "implementing the digest" || !e.Doing[0].At.Equal(fixtureNow) {
		t.Fatalf("history %+v", e.Doing)
	}
	if err := Doing(f.inv(f.sessionEnv(), ""), DoingOptions{Clear: true}); err != nil {
		t.Fatal(err)
	}
	if e := f.mustEntry(t, folder); len(e.Doing) != 1 {
		t.Fatalf("a --clear changed the history: %+v", e.Doing)
	}
	// No state folder: the publish goes, nothing is recorded anywhere.
	g := newFixture(t)
	g.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
	if err := Doing(g.inv(g.sessionEnv(), "x\n"), DoingOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(g.stateDir, "state", "entries")); err == nil {
		t.Fatal("a session without a state folder wrote an entry")
	}
	var m sessionmap.ByPID
	data, _ := os.ReadFile(filepath.Join(g.stateDir, "sessions", "by-pid", "4242.json"))
	if err := json.Unmarshal(data, &m); err != nil || m.StateFolder != "" {
		t.Fatalf("map %+v %v", m, err)
	}
}
