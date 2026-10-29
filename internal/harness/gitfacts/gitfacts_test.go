package gitfacts_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/gitfacts"
)

const (
	hashA = "0123456789abcdef0123456789abcdef01234567"
	hashB = "89abcdef0123456789abcdef0123456789abcdef"
)

// writeGit builds a hand-written `.git` under top: HEAD, loose refs,
// a packed-refs file, each as given.
func writeGit(t *testing.T, top string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(top, ".git", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil { //nolint:gosec // G301: a checkout tree
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestHeadReadsGitsOwnFiles: HEAD and the branch come from `.git/HEAD`,
// the loose ref, else packed-refs; a detached HEAD is the hash and no
// branch; a branch with no commit yet is the branch and no hash; a HEAD
// that points outside the common directory yields no hash; no `.git`
// at all yields nothing.
func TestHeadReadsGitsOwnFiles(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		files        map[string]string
		hash, branch string
	}{
		"a loose ref": {
			map[string]string{"HEAD": "ref: refs/heads/master\n", "refs/heads/master": hashA + "\n"}, hashA, "master",
		},
		"a packed ref": {
			map[string]string{
				"HEAD":        "ref: refs/heads/feature/x\n",
				"packed-refs": "# pack-refs with: peeled fully-peeled sorted\n" + hashB + " refs/heads/feature/x\n^" + hashA + "\n" + hashA + " refs/tags/v1\n",
			}, hashB, "feature/x",
		},
		"the loose ref wins over the packed one": {
			map[string]string{"HEAD": "ref: refs/heads/m\n", "refs/heads/m": hashA, "packed-refs": hashB + " refs/heads/m\n"}, hashA, "m",
		},
		"detached":                      {map[string]string{"HEAD": hashB + "\n"}, hashB, ""},
		"a branch with no commit yet":   {map[string]string{"HEAD": "ref: refs/heads/main\n"}, "", "main"},
		"a ref that is not a branch":    {map[string]string{"HEAD": "ref: refs/other/x\n", "refs/other/x": hashA}, hashA, ""},
		"a HEAD that escapes":           {map[string]string{"HEAD": "ref: ../../../etc/passwd\n"}, "", ""},
		"a HEAD that is not a hash":     {map[string]string{"HEAD": "garbage\n"}, "", ""},
		"a ref file that is not a hash": {map[string]string{"HEAD": "ref: refs/heads/m\n", "refs/heads/m": "nope\n"}, "", "m"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			top := t.TempDir()
			writeGit(t, top, tc.files)
			hash, branch := gitfacts.Head(top)
			if hash != tc.hash || branch != tc.branch {
				t.Fatalf("Head = %q, %q; want %q, %q", hash, branch, tc.hash, tc.branch)
			}
		})
	}
	t.Run("no .git", func(t *testing.T) {
		t.Parallel()
		if hash, branch := gitfacts.Head(t.TempDir()); hash != "" || branch != "" {
			t.Fatalf("Head = %q, %q; want nothing", hash, branch)
		}
	})
}

// TestHeadFollowsALinkedWorktree: in a linked worktree HEAD is the
// worktree's own and the refs are the main repository's (commondir).
func TestHeadFollowsALinkedWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	main := filepath.Join(root, "main")
	writeGit(t, main, map[string]string{
		"HEAD":                   "ref: refs/heads/master\n",
		"refs/heads/master":      hashA + "\n",
		"refs/heads/topic":       hashB + "\n",
		"worktrees/wt/HEAD":      "ref: refs/heads/topic\n",
		"worktrees/wt/commondir": "../..\n",
	})
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o750); err != nil { //nolint:gosec // G301: a checkout tree
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hash, branch := gitfacts.Head(wt); hash != hashB || branch != "topic" {
		t.Fatalf("Head = %q, %q; want the worktree's branch and the main repository's ref", hash, branch)
	}
	if hash, branch := gitfacts.Head(main); hash != hashA || branch != "master" {
		t.Fatalf("Head(main) = %q, %q", hash, branch)
	}
}

// gitOrSkip finds a real git for the spawning tests.
func gitOrSkip(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	return git
}

// gitRun runs git in dir for a fixture.
func gitRun(t *testing.T, git, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), git, append([]string{"-C", dir}, args...)...) //nolint:gosec // G204: a test fixture
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// environ is the environment of the spawning tests: the real PATH (for
// git) and a HOME of the test's own, so no global git config leaks in.
func environ(t *testing.T) []string {
	t.Helper()
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
}

// TestLogListsTheRangeByAuthor: the commits since a start head, newest
// first, with the card parsed from the subject; the author filter keeps
// another author's commit out; an empty since lists nothing; the hash
// Head reads agrees with git's own.
func TestLogListsTheRangeByAuthor(t *testing.T) {
	t.Parallel()
	git := gitOrSkip(t)
	env := environ(t)
	top := t.TempDir()
	gitRun(t, git, top, env, "init", "-q", "-b", "master")
	gitRun(t, git, top, env, "config", "user.email", "me@example.com")
	gitRun(t, git, top, env, "config", "user.name", "Me")
	commit := func(subject string, authorEnv ...string) string {
		t.Helper()
		gitRun(t, git, top, append(append([]string{}, env...), authorEnv...), "commit", "-q", "--allow-empty", "-m", subject)
		return gitRun(t, git, top, env, "rev-parse", "HEAD")
	}
	start := commit("0: before the session")
	if hash, branch := gitfacts.Head(top); hash != start || branch != "master" {
		t.Fatalf("Head = %q, %q; git says %q, master", hash, branch, start)
	}
	first := commit("162: Add the state folder")
	commit("merge from a teammate", "GIT_AUTHOR_EMAIL=them@example.com", "GIT_AUTHOR_NAME=Them")
	third := commit("no card here <tag> \u0007")
	r := gitfacts.Runner{Top: top, Environ: env, Timeout: 5 * time.Second}
	email, err := r.AuthorEmail(t.Context())
	if err != nil || email != "me@example.com" {
		t.Fatalf("AuthorEmail = %q, %v", email, err)
	}
	got, err := r.Log(t.Context(), start, email)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Hash != third || got[1].Hash != first {
		t.Fatalf("Log = %+v; want %s then %s", got, third, first)
	}
	if got[1].Card != 162 || got[1].Subject != "162: Add the state folder" || got[1].At.IsZero() || got[1].At.Location() != time.UTC {
		t.Fatalf("first commit = %+v", got[1])
	}
	if got[0].Card != 0 || got[0].Subject != "no card here <tag>" {
		t.Fatalf("third commit = %+v: the subject must be sanitised (the control character gone) and carry no card", got[0])
	}
	all, err := r.Log(t.Context(), start, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("Log without author = %d commits, %v; want 3", len(all), err)
	}
	if none, err := r.Log(t.Context(), "", email); err != nil || none != nil {
		t.Fatalf("Log with no since = %v, %v; want nothing", none, err)
	}
	if none, err := r.Log(t.Context(), "not-a-hash", email); err != nil || none != nil {
		t.Fatalf("Log with a bad since = %v, %v; want nothing", none, err)
	}
}

// TestIgnoredAsksGit: check-ignore's two answers, and the path after
// `--` so a path spelled like an option is still a path.
func TestIgnoredAsksGit(t *testing.T) {
	t.Parallel()
	git := gitOrSkip(t)
	env := environ(t)
	top := t.TempDir()
	gitRun(t, git, top, env, "init", "-q", "-b", "master")
	if err := os.WriteFile(filepath.Join(top, ".gitignore"), []byte(".brigade/state/\n--weird\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A pattern with a trailing slash matches a directory that exists, as
	// the state folder does once a hook has written into it.
	if err := os.MkdirAll(filepath.Join(top, ".brigade", "state", "sessions"), 0o750); err != nil { //nolint:gosec // G301: a checkout tree
		t.Fatal(err)
	}
	r := gitfacts.Runner{Top: top, Environ: env}
	for path, want := range map[string]bool{".brigade/state": true, ".brigade/state/sessions/x.json": true, "docs": false, "--weird": true} {
		got, err := r.Ignored(t.Context(), path)
		if err != nil || got != want {
			t.Fatalf("Ignored(%q) = %v, %v; want %v", path, got, err, want)
		}
	}
}

// TestRunnerWithoutGit: an environment whose PATH has no git answers
// ErrNoGit, and Head still works (it starts nothing).
func TestRunnerWithoutGit(t *testing.T) {
	t.Parallel()
	top := t.TempDir()
	writeGit(t, top, map[string]string{"HEAD": "ref: refs/heads/m\n", "refs/heads/m": hashA + "\n"})
	r := gitfacts.Runner{Top: top, Environ: []string{"PATH=" + t.TempDir()}}
	if _, err := r.Log(context.Background(), hashA, ""); err != gitfacts.ErrNoGit { //nolint:errorlint // the sentinel itself
		t.Fatalf("Log without git: %v", err)
	}
	if _, err := r.AuthorEmail(context.Background()); err != gitfacts.ErrNoGit { //nolint:errorlint // the sentinel itself
		t.Fatalf("AuthorEmail without git: %v", err)
	}
	if hash, _ := gitfacts.Head(top); hash != hashA {
		t.Fatalf("Head without git = %q", hash)
	}
}
