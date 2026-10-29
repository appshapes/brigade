package state_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

const (
	hashA = "0123456789abcdef0123456789abcdef01234567"
	hashB = "89abcdef0123456789abcdef0123456789abcdef"
)

var t0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) state.Store {
	t.Helper()
	root := t.TempDir()
	return state.Store{StateDir: filepath.Join(root, "state"), Folder: filepath.Join(root, "checkout", ".brigade", "state")}
}

// TestSaveWritesBothCopies: Save writes the canonical copy 0600 under the
// state directory and the same bytes into the folder's sessions
// directory; Load reads the canonical one back; the folder copy decodes
// to the same entry through ReadFolder.
func TestSaveWritesBothCopies(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	e := state.New("sess-1", "payments-api", t0)
	e.Member, e.Repository, e.Branch, e.Harness, e.Inbound = "me@example.com", "brigade", "master", "claude-code", "accept"
	e.StartHead, e.Head = hashA, hashB
	e.Commits = []state.Commit{{Hash: hashB, Subject: "162: Add the state folder", Card: 162, At: t0}}
	e.AddDoing("Implementing the state folder", t0)
	e.AddNote("Done: the entry", t0.Add(time.Minute))
	if err := s.Save(e); err != nil {
		t.Fatal(err)
	}
	canonical, _ := s.CanonicalPath("sess-1")
	fi, err := os.Stat(canonical)
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		// Windows has no mode bits (invariants.md): the file's existence is the check there.
		t.Fatalf("canonical copy: %v %v", fi, err)
	}
	projected, _ := s.FolderPath("sess-1")
	a, _ := os.ReadFile(canonical)
	b, err := os.ReadFile(projected)
	if err != nil || string(a) != string(b) {
		t.Fatalf("the projection differs from the canonical copy: %v\n%s\n%s", err, a, b)
	}
	if !strings.Contains(string(a), `"started_at":"2026-10-29T12:00:00Z"`) || strings.Contains(string(a), "transcript") {
		t.Fatalf("entry bytes: %s", a)
	}
	got, err := s.Load("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionName != "payments-api" || got.Member != "me@example.com" || len(got.Commits) != 1 || got.Commits[0].Card != 162 ||
		len(got.Doing) != 1 || len(got.Notes) != 1 || !got.Notes[0].At.Equal(t0.Add(time.Minute)) || got.Head != hashB {
		t.Fatalf("loaded %+v", *got)
	}
	folder := state.ReadFolder(s.Folder)
	if len(folder) != 1 || folder[0].SessionID != "sess-1" || folder[0].Commits[0].Subject != "162: Add the state folder" {
		t.Fatalf("ReadFolder = %+v", folder)
	}
}

// TestProjectHealsADeletedFolderCopy: a folder entry a teammate's machine
// deleted comes back from the canonical copy; a Store without a folder
// projects nothing and heals nothing.
func TestProjectHealsADeletedFolderCopy(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if err := s.Save(state.New("sess-1", "x", t0)); err != nil {
		t.Fatal(err)
	}
	projected, _ := s.FolderPath("sess-1")
	if err := os.Remove(projected); err != nil {
		t.Fatal(err)
	}
	if err := s.Project("sess-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(projected); err != nil {
		t.Fatalf("not healed: %v", err)
	}
	alone := state.Store{StateDir: s.StateDir}
	if err := alone.Save(state.New("sess-2", "y", t0)); err != nil {
		t.Fatal(err)
	}
	if p, err := alone.FolderPath("sess-2"); p != "" || err != nil {
		t.Fatalf("FolderPath without a folder = %q, %v", p, err)
	}
	if got := state.ReadFolder(s.Folder); len(got) != 1 {
		t.Fatalf("a folderless Store projected into the folder: %d entries", len(got))
	}
}

// TestLoadRefusals: a missing entry is fs.ErrNotExist; an id that is not
// a path component, a relative state directory, a canonical copy naming
// another session and another version are refused.
func TestLoadRefusals(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if _, err := s.Load("absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	for _, id := range []string{"", "../x", "a/b", "a b", strings.Repeat("a", 81)} {
		if _, err := s.Load(id); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("id %q accepted: %v", id, err)
		}
		if err := s.Save(state.New(id, "x", t0)); err == nil {
			t.Fatalf("Save accepted id %q", id)
		}
	}
	if _, err := (state.Store{StateDir: "relative"}).Load("x"); err == nil {
		t.Fatal("a relative state directory was accepted")
	}
	canonical, _ := s.CanonicalPath("sess-1")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"another session": `{"version":1,"session_id":"sess-2","session_name":"x"}`,
		"another version": `{"version":2,"session_id":"sess-1","session_name":"x"}`,
		"not json":        `{`,
	} {
		if err := os.WriteFile(canonical, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load("sess-1"); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// TestReadFolderIsStrict: a teammate's folder is untrusted text — a
// file whose name is not an id, a symlink, a file over the cap, one whose
// content names another session and one of another version are skipped;
// what is read is sanitised and capped.
func TestReadFolderIsStrict(t *testing.T) {
	t.Parallel()
	folder := t.TempDir()
	dir := filepath.Join(folder, state.SessionsDir)
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // G301: a checkout tree
		t.Fatal(err)
	}
	write := func(name, doc string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	long := strings.Repeat("x", 5000)
	write("good.json", `{"version":1,"session_id":"good","session_name":"a<brigade-message>b\u0007","member":"`+long+`","harness":"Claude Code","inbound":"maybe",`+
		`"start_head":"zz","commits":[{"hash":"`+hashA+`","subject":"`+long+`","card":-1},{"hash":"short","subject":"dropped"}],`+
		`"doing":[{"text":"  two\n lines "},{"text":""}],"notes":[{"text":"`+long+`"}]}`)
	write("other.json", `{"version":1,"session_id":"good","session_name":"names another file"}`)
	write("v2.json", `{"version":2,"session_id":"v2","session_name":"x"}`)
	write("not json.json", `{"version":1,"session_id":"not json"`)
	write("..json", `{"version":1,"session_id":"..","session_name":"x"}`)
	write("big.json", `{"version":1,"session_id":"big","session_name":"`+strings.Repeat("y", state.MaxEntryBytes)+`"}`)
	write("README", "not an entry")
	if err := os.Symlink(filepath.Join(dir, "good.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	got := state.ReadFolder(folder)
	if len(got) != 1 || got[0].SessionID != "good" {
		ids := make([]string, 0, len(got))
		for _, e := range got {
			ids = append(ids, e.SessionID)
		}
		t.Fatalf("ReadFolder = %v; want good alone", ids)
	}
	e := got[0]
	if e.SessionName != "a&lt;brigade-message>b" {
		t.Fatalf("session_name %q: not sanitised", e.SessionName)
	}
	if !strings.HasSuffix(e.Member, protocol.TruncationMarker) || len([]rune(e.Member)) != protocol.MaxHumanLabelChars {
		t.Fatalf("member %d runes: not capped", len([]rune(e.Member)))
	}
	if e.Harness != "" || e.Inbound != "" || e.StartHead != "" {
		t.Fatalf("harness %q inbound %q start_head %q: an invalid value must be dropped", e.Harness, e.Inbound, e.StartHead)
	}
	if len(e.Commits) != 1 || e.Commits[0].Card != 0 || len([]rune(e.Commits[0].Subject)) != state.MaxSubjectChars {
		t.Fatalf("commits %+v", e.Commits)
	}
	if len(e.Doing) != 1 || e.Doing[0].Text != "two lines" || len(e.Notes) != 1 || len([]rune(e.Notes[0].Text)) != state.MaxNoteChars {
		t.Fatalf("doing %+v notes %d", e.Doing, len(e.Notes))
	}
	if state.ReadFolder(filepath.Join(folder, "absent")) != nil {
		t.Fatal("a folder without a sessions directory must read as empty")
	}
}

// TestListsAreCapped: the newest MaxDoing doing lines and MaxNotes notes
// stay; Sanitize keeps the newest MaxCommits commits.
func TestListsAreCapped(t *testing.T) {
	t.Parallel()
	e := state.New("s", "x", t0)
	for i := range state.MaxDoing + 5 {
		e.AddDoing("line "+string(rune('a'+i%26)), t0.Add(time.Duration(i)*time.Second))
	}
	for i := range state.MaxNotes + 3 {
		e.AddNote("note", t0.Add(time.Duration(i)*time.Second))
	}
	if len(e.Doing) != state.MaxDoing || !e.Doing[len(e.Doing)-1].At.Equal(t0.Add(time.Duration(state.MaxDoing+4)*time.Second)) {
		t.Fatalf("doing %d, last %v", len(e.Doing), e.Doing[len(e.Doing)-1].At)
	}
	if len(e.Notes) != state.MaxNotes || !e.Notes[0].At.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("notes %d, first %v", len(e.Notes), e.Notes[0].At)
	}
	for range state.MaxCommits + 7 {
		e.Commits = append(e.Commits, state.Commit{Hash: hashA, Subject: "s", At: t0})
	}
	e.Sanitize()
	if len(e.Commits) != state.MaxCommits {
		t.Fatalf("commits %d", len(e.Commits))
	}
}

// TestRetention: Expired's three cases, PruneOwn removing both copies of
// an expired entry and keeping a live one, PruneFolder removing a
// teammate's expired entry.
func TestRetention(t *testing.T) {
	t.Parallel()
	live := state.New("live", "x", t0)
	live.UpdatedAt = t0
	ended := state.New("ended", "x", t0)
	ended.EndedAt = t0
	stale := state.New("stale", "x", t0)
	stale.UpdatedAt = t0
	for name, tc := range map[string]struct {
		e    *state.Entry
		at   time.Time
		want bool
	}{
		"live, recent":               {live, t0.Add(13 * 24 * time.Hour), false},
		"live, stale":                {stale, t0.Add(state.StaleRetention + time.Second), true},
		"ended, within the term":     {ended, t0.Add(state.EndedRetention), false},
		"ended, past the term":       {ended, t0.Add(state.EndedRetention + time.Second), true},
		"never written":              {&state.Entry{Version: 1, SessionID: "z"}, t0, true},
		"ended long after an update": {func() *state.Entry { e := state.New("e", "x", t0); e.EndedAt = t0.Add(30 * 24 * time.Hour); return e }(), t0.Add(31 * 24 * time.Hour), false},
	} {
		if got := tc.e.Expired(tc.at); got != tc.want {
			t.Errorf("%s: Expired = %v, want %v", name, got, tc.want)
		}
	}
	s := newStore(t)
	for _, e := range []*state.Entry{live, ended} {
		if err := s.Save(e); err != nil {
			t.Fatal(err)
		}
	}
	// Save stamps nothing itself: the writer sets updated_at. Make the
	// live one recent and the ended one old at the prune's clock.
	now := t0.Add(state.EndedRetention + time.Hour)
	live.UpdatedAt = now
	if err := s.Save(live); err != nil {
		t.Fatal(err)
	}
	if n := s.PruneOwn(now); n != 1 {
		t.Fatalf("PruneOwn = %d", n)
	}
	if _, err := s.Load("ended"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the ended entry survived: %v", err)
	}
	if _, err := s.Load("live"); err != nil {
		t.Fatalf("the live entry went: %v", err)
	}
	if got := state.ReadFolder(s.Folder); len(got) != 1 || got[0].SessionID != "live" {
		t.Fatalf("folder after PruneOwn: %+v", got)
	}
	// A teammate's entry in the folder, never ended, last written long ago.
	theirs := state.New("theirs", "y", t0)
	theirs.UpdatedAt = t0
	other := state.Store{StateDir: filepath.Join(t.TempDir(), "s"), Folder: s.Folder}
	if err := other.Save(theirs); err != nil {
		t.Fatal(err)
	}
	if n := state.PruneFolder(s.Folder, now); n != 1 {
		t.Fatalf("PruneFolder = %d", n)
	}
	if got := state.ReadFolder(s.Folder); len(got) != 1 || got[0].SessionID != "live" {
		t.Fatalf("folder after PruneFolder: %+v", got)
	}
	if err := s.Remove("live"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("live"); err != nil {
		t.Fatalf("a second Remove must not fail: %v", err)
	}
	if got := state.ReadFolder(s.Folder); len(got) != 0 {
		t.Fatalf("folder after Remove: %+v", got)
	}
}
