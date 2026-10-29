//go:build !windows

package hook

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// stateMember is the team file's sync member with a state folder (card
// 162): the folder is listed, and `state` names it as listed.
const stateMember = `,"sync":{"folders":[".brigade/state","docs/shared"],"state":".brigade/state"}`

// stateStore is the fixture session's entry store, as the hooks build it
// from the map.
func (f *fixture) stateStore(t *testing.T) state.Store {
	t.Helper()
	m := f.mustMap()
	if m.StateFolder == "" {
		t.Fatal("the map names no state folder")
	}
	return state.Store{StateDir: f.stateDir, Folder: m.StateFolder}
}

// mustEntry loads the fixture session's canonical entry.
func (f *fixture) mustEntry(t *testing.T) *state.Entry {
	t.Helper()
	e, err := f.stateStore(t).Load("brigade-sess-1")
	if err != nil {
		t.Fatalf("canonical entry: %v", err)
	}
	return e
}

// folderEntries reads the folder's entries.
func (f *fixture) folderEntries(t *testing.T) []*state.Entry {
	t.Helper()
	return state.ReadFolder(f.stateStore(t).Folder)
}

// TestStateEntryFollowsTheSession: with a state folder in the team file,
// SessionStart freezes the folder's absolute path into the map and
// writes the entry's two copies — the session's facts, no transcript
// fact, no path — the prompt hook stamps the prompt, and SessionEnd
// records the end and keeps both copies; clear and resume end nothing.
func TestStateEntryFollowsTheSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.writeTeamFile(stateMember)
	registered(t, f, map[string][]fakeadapter.Response{"session close": {okResp(closeDoc())}})
	m := f.mustMap()
	if want := filepath.Join(realpath(t, f.cwd), ".brigade", "state"); m.StateFolder != want {
		t.Fatalf("state_folder = %q, want %q", m.StateFolder, want)
	}
	e := f.mustEntry(t)
	if e.SessionID != "brigade-sess-1" || e.SessionName != "payments-api" || e.Harness != "claude-code" || e.Inbound != "accept" ||
		e.Repository != "checkout" || !e.StartedAt.Equal(fixedTime) || !e.UpdatedAt.Equal(fixedTime) || !e.LastPromptAt.IsZero() || !e.EndedAt.IsZero() {
		t.Fatalf("entry after start: %+v", *e)
	}
	// The fixture's checkout has no git HEAD, no account email: those
	// facts are simply absent.
	if e.Member != "" || e.StartHead != "" || e.Branch != "" || len(e.Commits) != 0 {
		t.Fatalf("entry carries facts the fixture cannot know: %+v", *e)
	}
	raw, err := os.ReadFile(state.EntryPath(m.StateFolder, "brigade-sess-1"))
	if err != nil {
		t.Fatalf("folder copy: %v", err)
	}
	for _, forbidden := range []string{"transcript", "never/read", f.stateDir, msgTok, "socket"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the folder copy carries %q:\n%s", forbidden, raw)
		}
	}
	if got := f.folderEntries(t); len(got) != 1 || got[0].SessionName != "payments-api" {
		t.Fatalf("folder entries: %+v", got)
	}

	f.now = fixedTime.Add(time.Minute)
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
		t.Fatalf("prompt: exit %d out %q", exit, out)
	}
	e = f.mustEntry(t)
	if !e.LastPromptAt.Equal(f.now) || !e.UpdatedAt.Equal(f.now) || !e.StartedAt.Equal(fixedTime) {
		t.Fatalf("entry after the prompt: %+v", *e)
	}

	for _, reason := range []string{"clear", "resume"} {
		f.now = f.now.Add(time.Minute)
		if exit, _, _ := f.run(SubSessionEnd, f.endDoc(reason)); exit != 0 {
			t.Fatal(exit)
		}
		if e := f.mustEntry(t); !e.EndedAt.IsZero() || e.EndReason != "" {
			t.Fatalf("a %q end ended the entry: %+v", reason, *e)
		}
	}
	f.now = fixedTime.Add(10 * time.Minute)
	if exit, _, _ := f.run(SubSessionEnd, f.endDoc("prompt_input_exit")); exit != 0 {
		t.Fatal(exit)
	}
	s := state.Store{StateDir: f.stateDir, Folder: m.StateFolder}
	e, err = s.Load("brigade-sess-1")
	if err != nil {
		t.Fatalf("the canonical entry went with the session: %v", err)
	}
	if !e.EndedAt.Equal(f.now) || e.EndReason != "prompt_input_exit" || !e.LastPromptAt.Equal(fixedTime.Add(time.Minute)) {
		t.Fatalf("entry after the end: %+v", *e)
	}
	if got := state.ReadFolder(m.StateFolder); len(got) != 1 || !got[0].EndedAt.Equal(f.now) {
		t.Fatalf("folder entry after the end: %+v", got)
	}
}

// TestStateNothingWithoutAStateFolder: a project that syncs folders but
// names no state folder, one with sync off, and one with no sync member
// freeze no folder and write no entry anywhere.
func TestStateNothingWithoutAStateFolder(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		members string
		extra   []string
	}{
		"folders but no state": {members: `,"sync":{"folders":[".brigade/state"]}`},
		"a state folder with sync off": {
			members: stateMember, extra: []string{"CLAUDE_PLUGIN_OPTION_SYNC=off"},
		},
		"no sync member": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			f.writeTeamFile(tc.members)
			registered(t, f, nil, tc.extra...)
			if m := f.mustMap(); m.StateFolder != "" {
				t.Fatalf("state_folder = %q", m.StateFolder)
			}
			if exit, _, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 {
				t.Fatal(exit)
			}
			if _, err := os.Lstat(filepath.Join(f.stateDir, "state", "entries")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a canonical entries directory exists: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(f.cwd, ".brigade")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the folder was written: %v", err)
			}
		})
	}
}

// TestStateFolderCopyIsHealed: a folder entry a teammate's machine
// deleted comes back at the next hook, from the canonical copy; and a
// canonical copy that is gone (the project gained its state folder while
// the session ran, or a prune) is written afresh at the next prompt.
func TestStateFolderCopyIsHealed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.writeTeamFile(stateMember)
	registered(t, f, nil)
	s := f.stateStore(t)
	projected, _ := s.FolderPath("brigade-sess-1")
	if err := os.Remove(projected); err != nil {
		t.Fatal(err)
	}
	f.now = fixedTime.Add(time.Minute)
	if exit, _, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 {
		t.Fatal(exit)
	}
	if got := f.folderEntries(t); len(got) != 1 || !got[0].LastPromptAt.Equal(f.now) {
		t.Fatalf("the folder copy was not healed: %+v", got)
	}
	canonical, _ := s.CanonicalPath("brigade-sess-1")
	if err := os.Remove(canonical); err != nil {
		t.Fatal(err)
	}
	f.now = fixedTime.Add(2 * time.Minute)
	if exit, _, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 {
		t.Fatal(exit)
	}
	e := f.mustEntry(t)
	if !e.StartedAt.Equal(f.now) || !e.LastPromptAt.Equal(f.now) || e.SessionName != "payments-api" {
		t.Fatalf("the canonical copy was not written afresh: %+v", *e)
	}
}

// TestStateContinueRevivesAndPrunes: on the continue path (a /clear) the
// entry is kept — its end cleared, its name refreshed, its start kept —
// and this machine's expired entries go, both copies, while a recent
// ended one stays.
func TestStateContinueRevivesAndPrunes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.writeTeamFile(stateMember)
	registered(t, f, map[string][]fakeadapter.Response{"session heartbeat": {okResp(heartbeatDoc())}})
	s := f.stateStore(t)
	// The session ends, then resumes with the same Brigade session.
	e := f.mustEntry(t)
	e.EndedAt, e.EndReason = fixedTime.Add(time.Hour), "other"
	if err := s.Save(e); err != nil {
		t.Fatal(err)
	}
	old := state.New("old-sess", "gone", fixedTime.Add(-30*24*time.Hour))
	old.EndedAt = fixedTime.Add(-20 * 24 * time.Hour)
	recent := state.New("recent-sess", "stayed", fixedTime.Add(-3*24*time.Hour))
	recent.EndedAt, recent.UpdatedAt = fixedTime.Add(-2*24*time.Hour), fixedTime.Add(-2*24*time.Hour)
	for _, x := range []*state.Entry{old, recent} {
		if err := s.Save(x); err != nil {
			t.Fatal(err)
		}
	}
	f.now = fixedTime.Add(2 * time.Hour)
	f.registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, "renamed-session", "busy", f.socket)})
	f.deps.Registry = f.registry
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	e = f.mustEntry(t)
	if !e.EndedAt.IsZero() || e.EndReason != "" || e.SessionName != "renamed-session" || !e.StartedAt.Equal(fixedTime) || !e.UpdatedAt.Equal(f.now) {
		t.Fatalf("entry after the /clear: %+v", *e)
	}
	if _, err := s.Load("old-sess"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the expired entry survived: %v", err)
	}
	if _, err := s.Load("recent-sess"); err != nil {
		t.Fatalf("the recent ended entry went: %v", err)
	}
	ids := map[string]bool{}
	for _, x := range f.folderEntries(t) {
		ids[x.SessionID] = true
	}
	if ids["old-sess"] || !ids["recent-sess"] || !ids["brigade-sess-1"] {
		t.Fatalf("folder entries %v", ids)
	}
}

// gitPath is a PATH directory holding git alone, for the commit tests:
// the fixture's PATH is empty so the shadowing check sees no developer
// `brigade`, and git must be found on the hook's own PATH.
func gitPath(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := filepath.Join(t.TempDir(), "path-git")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// gitIn runs git in the fixture's checkout with a hermetic HOME.
func (f *fixture) gitIn(args ...string) string {
	f.t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		f.t.Skip("no git on PATH")
	}
	cmd := exec.CommandContext(f.t.Context(), git, append([]string{"-C", f.cwd}, args...)...) //nolint:gosec // G204: a test fixture
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.dirs.Home, "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestStateCommitsSinceStart: with a real repository, SessionStart
// records the start head and the branch; a prompt after commits lists
// the session author's commits since the start, newest first, with the
// card parsed, and not a teammate's; a prompt with HEAD unmoved starts
// no git and keeps the list; SessionEnd lists the last commit too.
func TestStateCommitsSinceStart(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.writeTeamFile(stateMember)
	if err := os.RemoveAll(filepath.Join(f.cwd, ".git")); err != nil {
		t.Fatal(err)
	}
	f.gitIn("init", "-q", "-b", "master")
	f.gitIn("config", "user.email", "me@example.com")
	f.gitIn("config", "user.name", "Me")
	f.gitIn("commit", "-q", "--allow-empty", "-m", "0: before")
	start := f.gitIn("rev-parse", "HEAD")
	path := "PATH=" + gitPath(t)
	registered(t, f, map[string][]fakeadapter.Response{"session close": {okResp(closeDoc())}}, path)
	e := f.mustEntry(t)
	if e.StartHead != start || e.Head != start || e.Branch != "master" || len(e.Commits) != 0 {
		t.Fatalf("entry after start: %+v", *e)
	}
	f.gitIn("commit", "-q", "--allow-empty", "-m", "162: Add the state folder")
	first := f.gitIn("rev-parse", "HEAD")
	f.gitIn("-c", "user.email=them@example.com", "-c", "user.name=Them", "commit", "-q", "--allow-empty", "-m", "7: a teammate's, pulled")
	f.gitIn("commit", "-q", "--allow-empty", "-m", "Merge branch 'x'")
	second := f.gitIn("rev-parse", "HEAD")
	f.now = fixedTime.Add(time.Minute)
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc("default"), path); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	e = f.mustEntry(t)
	if e.Head != second || len(e.Commits) != 2 || e.Commits[0].Hash != second || e.Commits[1].Hash != first ||
		e.Commits[1].Card != 162 || e.Commits[1].Subject != "162: Add the state folder" || e.Commits[0].Card != 0 || e.Commits[0].At.IsZero() {
		t.Fatalf("entry after the prompt: %+v", *e)
	}
	// HEAD unmoved: no git on PATH is no loss, the list stays.
	f.now = fixedTime.Add(2 * time.Minute)
	if exit, _, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 {
		t.Fatal(exit)
	}
	if e = f.mustEntry(t); len(e.Commits) != 2 || !e.LastPromptAt.Equal(f.now) {
		t.Fatalf("entry after the quiet prompt: %+v", *e)
	}
	f.gitIn("commit", "-q", "--allow-empty", "-m", "162: Record the end")
	third := f.gitIn("rev-parse", "HEAD")
	f.now = fixedTime.Add(3 * time.Minute)
	if exit, _, _ := f.run(SubSessionEnd, f.endDoc("other"), path); exit != 0 {
		t.Fatal(exit)
	}
	e, err := (state.Store{StateDir: f.stateDir}).Load("brigade-sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Head != third || len(e.Commits) != 3 || e.Commits[0].Hash != third || !e.EndedAt.Equal(f.now) {
		t.Fatalf("entry after the end: %+v", *e)
	}
}
