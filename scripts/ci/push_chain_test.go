//go:build !windows

// The chain of `make push` (work item 423; the owner's ruling of 2026-11-23). `make push` is `make commit` and a
// push, and `make commit` is its prerequisites and then the staging: until this work item they were typecheck,
// pull, build and test, and twice a push that had passed them turned `master` red in CI's `lint` job (77579966,
// a test helper from a file tagged `!windows`; e3e07811, a test's comparison of an error with itself).
//
// What is held here is the Makefile's line and the places that say the chain in words, so that one does not
// move without the others: lint is in the chain, after the pull (the merged tree is what is pushed) and before
// the build and the tests (a fault stops the push in seconds), and nothing is staged by a prerequisite.
//
// The chain's two last steps are `scripts/commit-push.sh` (work item 443), run here for real in a throwaway
// repository with a throwaway origin: with something to commit it commits and pushes; with nothing to commit
// it goes on and pushes what is ahead of origin, a merge a session concluded for one, where `make push` used
// to stop at git's "nothing to commit"; with neither it says so and ends well.
package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// pushChain is the chain as the Makefile has it, in order.
var pushChain = []string{"typecheck", "pull", "lint", "build", "test"}

// readRepoFile reads one file of the repository.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	//nolint:gosec // G304: the repository's own file
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// makeRule returns the prerequisites and the recipe of one target of the Makefile, and whether it has one.
func makeRule(makefile, target string) (prerequisites, recipe []string, ok bool) {
	lines := strings.Split(makefile, "\n")
	for i, line := range lines {
		rest, found := strings.CutPrefix(line, target+":")
		if !found || strings.HasPrefix(rest, "=") {
			continue
		}
		rest, _, _ = strings.Cut(rest, "##")
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "\t") {
				break
			}
			recipe = append(recipe, strings.TrimSpace(next))
		}
		return strings.Fields(rest), recipe, true
	}
	return nil, nil, false
}

// movesTheTree says whether a recipe line stages, commits or pushes, in the forms a Makefile writes them.
var movesTheTree = regexp.MustCompile(`\bgit\b[^|;&]*\b(add|commit|push|stash)\b`)

func TestThePushChainLintsTheMergedTreeBeforeAnythingIsStaged(t *testing.T) {
	t.Parallel()
	makefile := readRepoFile(t, "Makefile")

	prerequisites, recipe, ok := makeRule(makefile, "commit")
	if !ok {
		t.Fatal("the Makefile has no commit target")
	}
	if !slices.Equal(prerequisites, pushChain) {
		t.Errorf("make commit runs %v before it stages; the chain is %v: lint after the pull, before the build and the tests",
			prerequisites, pushChain)
	}
	if !slices.Equal(recipe, []string{`scripts/commit-push.sh commit "$(message)"`}) {
		t.Errorf("make commit's recipe is not the script's commit step alone, which stages and commits: %q", recipe)
	}

	prerequisites, recipe, ok = makeRule(makefile, "push")
	if !ok {
		t.Fatal("the Makefile has no push target")
	}
	if !slices.Equal(prerequisites, []string{"commit"}) {
		t.Errorf("make push runs %v before it pushes; it is make commit, and nothing else", prerequisites)
	}
	if !slices.Equal(recipe, []string{"scripts/commit-push.sh push"}) {
		t.Errorf("make push's recipe is not the script's push step alone: %q", recipe)
	}
	// The recipes run the script by its path, so it has to be executable: the tests below start it with `sh`
	// and would pass on a file that is not.
	info, err := os.Stat(filepath.Join(testutil.RepoRoot(t), "scripts", "commit-push.sh"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("scripts/commit-push.sh is not executable (%v, %v): every make push would end at it", info, err)
	}

	// No step of the chain stages, commits or pushes, or a step that failed after it would leave a staged tree.
	// Read: the recipe of each step and of every target it depends on, as the Makefile writes them. Not read:
	// what a script or a `go run` that a recipe starts does.
	seen := map[string]bool{}
	var walk func(target string, via []string)
	walk = func(target string, via []string) {
		if seen[target] {
			return
		}
		seen[target] = true
		prerequisites, recipe, ok := makeRule(makefile, target)
		if !ok {
			return // a file or a variable, not a target of this Makefile
		}
		for _, line := range recipe {
			if movesTheTree.MatchString(line) {
				t.Errorf("make %s (reached through %v) runs %q: a step of the chain stages, commits or pushes", target, via, line)
			}
		}
		for _, p := range prerequisites {
			walk(p, append(via, target))
		}
	}
	for _, step := range pushChain {
		walk(step, nil)
	}
	for _, step := range pushChain {
		if _, _, ok := makeRule(makefile, step); !ok {
			t.Errorf("the Makefile has no %s target, which the chain names", step)
		}
	}
}

// TestThePagesThatListThePushChainListLint holds each place that says the chain in words against the Makefile.
// Every word of a step that the place has (typecheck, pull, lint, build, test, in any of their forms), read in
// order with a word said twice running counted once, is the chain: no step missing, none added, none moved.
func TestThePagesThatListThePushChainListLint(t *testing.T) {
	t.Parallel()
	step := regexp.MustCompile(`(?i)\b(typecheck|pull|lint|build|test)s?\b`)
	for _, c := range []struct {
		file, from, to string
	}{
		{"Makefile", "\ncommit: typecheck pull lint build test ## ", "\n"},
		{"docs/developer/reference/make-targets.md", "\n| `commit` | ", "\n"},
		{"docs/agent/repository/conventions.md", "Commit and push through `make push", "commit, push)"},
		{"docs/developer/build-and-test/README.md", "2. `make push` runs", "\n"},
		{"docs/developer/contribute/commits.md", "\n`make commit` ", "`make push` adds"},
		{"docs/developer/release/cut-a-release.md", "then `make push message=", "commit, push)"},
		{".claude/skills/commit/SKILL.md", "`make commit` alone", "does NOT push"},
		{".claude/skills/commit/SKILL.md", "```\n\nThis ", "\n"},
		{".github/workflows/ci.yml", "has already passed `make push`", "before it reaches"},
		{"scripts/release-prep.sh", "make push message=\"$work_item: Release $v\"", "\n"},
		{"scripts/release-pin.sh", "  make push message=\"$work_item: Point the Codex CLI and Grok Build packages at $v\"", "\n"},
	} {
		text := readRepoFile(t, c.file)
		_, after, ok := strings.Cut(text, c.from)
		if !ok {
			t.Errorf("%s: no place beginning %q: the wording moved, name it here again", c.file, c.from)
			continue
		}
		said, _, ok := strings.Cut(after, c.to)
		if !ok {
			t.Errorf("%s: the place beginning %q does not reach %q", c.file, c.from, c.to)
			continue
		}
		var names []string
		for _, m := range step.FindAllStringSubmatch(said, -1) {
			if name := strings.ToLower(m[1]); len(names) == 0 || names[len(names)-1] != name {
				names = append(names, name)
			}
		}
		if !slices.Equal(names, pushChain) {
			t.Errorf("%s: the place beginning %q names the steps %v; the chain is %v", c.file, strings.TrimSpace(c.from), names, pushChain)
		}
	}
}

// pushFixture is a throwaway repository whose `origin` is a bare repository beside it, with `master` pushed
// and tracked, and a second clone of that origin through which origin moves. Nothing of it is the
// repository's own: the script is run in it, never against a real remote.
type pushFixture struct {
	t                   *testing.T
	env                 []string
	origin, work, other string
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	base := t.TempDir()
	f := &pushFixture{t: t, origin: filepath.Join(base, "origin.git"), work: filepath.Join(base, "work"), other: filepath.Join(base, "other")}
	f.env = baseEnv(t, filepath.Join(base, "home"), "GIT_COMMITTER_NAME=ci-test", "GIT_COMMITTER_EMAIL=ci-test@example.invalid")
	for _, dir := range []string{f.work, filepath.Join(base, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.git(base, "init", "-q", "--bare", "-b", "master", f.origin)
	f.git(f.work, "init", "-q", "-b", "master")
	f.write(f.work, "page.md", "a page\n")
	f.git(f.work, "add", "page.md")
	f.git(f.work, "commit", "-q", "-m", "1: Start")
	f.git(f.work, "remote", "add", "origin", f.origin)
	f.git(f.work, "push", "-q", "-u", "origin", "master")
	f.git(base, "clone", "-q", f.origin, f.other)
	return f
}

// git runs git in dir and returns what it printed, trimmed; a failure fails the test.
func (f *pushFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.CommandContext(f.t.Context(), "git", args...) //nolint:gosec // G204: git, with this test's own arguments
	cmd.Dir, cmd.Env = dir, f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v in %s: %v\n%s", args, filepath.Base(dir), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *pushFixture) write(dir, name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// step runs one step of the script in the working repository, as the Makefile's recipe does.
func (f *pushFixture) step(args ...string) result {
	f.t.Helper()
	return runScriptAt(f.t, filepath.Join(testutil.RepoRoot(f.t), "scripts", "commit-push.sh"), f.work, f.env, args...)
}

// atOrigin is the commit origin's master names.
func (f *pushFixture) atOrigin() string { f.t.Helper(); return f.git(f.origin, "rev-parse", "master") }

// TestTheChainCommitsAndPushesWhatThereIsToCommit: the first of the three cases, and what `make push` always
// did. Every change of the tree is committed under the message, an untracked file included, and reaches origin.
func TestTheChainCommitsAndPushesWhatThereIsToCommit(t *testing.T) {
	t.Parallel()
	f := newPushFixture(t)
	f.write(f.work, "page.md", "a page, edited\n")
	f.write(f.work, "new.md", "a new page\n")
	if r := f.step("commit", "7: Edit the page"); r.code != 0 {
		t.Fatalf("commit: exit %d\n%s", r.code, r.all())
	}
	if left := f.git(f.work, "status", "--porcelain"); left != "" {
		t.Fatalf("the commit left changes behind:\n%s", left)
	}
	if got := f.git(f.work, "log", "-1", "--format=%s"); got != "7: Edit the page" {
		t.Fatalf("the commit's subject is %q", got)
	}
	if got := f.git(f.work, "show", "--name-only", "--format=", "HEAD"); got != "new.md\npage.md" {
		t.Fatalf("the commit holds %q, want the edited page and the new one", got)
	}
	r := f.step("push")
	if r.code != 0 || !strings.Contains(r.stdout, "make push: pushing 1 commit(s) that origin/master does not have") {
		t.Fatalf("push: exit %d\n%s", r.code, r.all())
	}
	if head := f.git(f.work, "rev-parse", "HEAD"); f.atOrigin() != head {
		t.Fatalf("origin's master is %s, the commit is %s", f.atOrigin(), head)
	}
}

// TestTheChainPushesAConcludedMergeWithNothingToCommit: the second case, the one work item 443 is for. Origin
// moved, the pull merged it and the session concluded the merge: the tree is clean and the branch is ahead.
// The commit step has nothing to commit, says so and ends well, with no message and nothing staged, and the
// push step takes the merge to origin. Until then `make push` ended at git's "nothing to commit" there, exit 1.
func TestTheChainPushesAConcludedMergeWithNothingToCommit(t *testing.T) {
	t.Parallel()
	f := newPushFixture(t)
	f.write(f.other, "other.md", "another session's page\n")
	f.git(f.other, "add", "other.md")
	f.git(f.other, "commit", "-q", "-m", "8: Another session's commit")
	f.git(f.other, "push", "-q", "origin", "master")
	f.write(f.work, "mine.md", "this session's page\n")
	f.git(f.work, "add", "mine.md")
	f.git(f.work, "commit", "-q", "-m", "9: This session's commit")
	// The chain's pull, a plain merge; said outright here, since the fixture's git has no configuration to say it.
	f.git(f.work, "pull", "-q", "--no-rebase", "--no-edit")
	merge := f.git(f.work, "rev-parse", "HEAD")
	if parents := strings.Fields(f.git(f.work, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("the fixture's pull made no merge commit: parents %v", parents)
	}

	r := f.step("commit", "")
	if r.code != 0 || !strings.Contains(r.stdout, "make commit: nothing to commit: the working tree is clean") {
		t.Fatalf("commit on a clean tree: exit %d\n%s", r.code, r.all())
	}
	if head := f.git(f.work, "rev-parse", "HEAD"); head != merge {
		t.Fatalf("the commit step moved HEAD from %s to %s", merge, head)
	}
	if before := f.atOrigin(); before == merge {
		t.Fatal("origin had the merge before the push: the fixture proves nothing")
	}
	r = f.step("push")
	if r.code != 0 || !strings.Contains(r.stdout, "make push: pushing 2 commit(s) that origin/master does not have") {
		t.Fatalf("push of the merge: exit %d\n%s", r.code, r.all())
	}
	if f.atOrigin() != merge {
		t.Fatalf("origin's master is %s, the merge is %s", f.atOrigin(), merge)
	}
}

// TestTheChainSaysSoWhenThereIsNothingToCommitAndNothingToPush: the third case. Both steps say what they
// found and end well, git is asked to push nothing, and origin stays where it was.
func TestTheChainSaysSoWhenThereIsNothingToCommitAndNothingToPush(t *testing.T) {
	t.Parallel()
	f := newPushFixture(t)
	before := f.atOrigin()
	if r := f.step("commit", ""); r.code != 0 || !strings.Contains(r.stdout, "make commit: nothing to commit") {
		t.Fatalf("commit: exit %d\n%s", r.code, r.all())
	}
	r := f.step("push")
	if r.code != 0 || !strings.Contains(r.stdout, "make push: nothing to push: origin/master has every commit of this branch") {
		t.Fatalf("push: exit %d\n%s", r.code, r.all())
	}
	if strings.Contains(r.all(), "Everything up-to-date") || strings.Contains(r.all(), "To ") {
		t.Fatalf("git was asked to push though nothing is ahead:\n%s", r.all())
	}
	if f.atOrigin() != before {
		t.Fatalf("origin moved from %s to %s", before, f.atOrigin())
	}
}

// TestTheChainStillAsksForAMessageWhenThereIsSomethingToCommit: a message is required as before, and it is
// asked for before anything is staged, so a `make push` without one leaves the tree as it found it.
func TestTheChainStillAsksForAMessageWhenThereIsSomethingToCommit(t *testing.T) {
	t.Parallel()
	f := newPushFixture(t)
	f.write(f.work, "page.md", "a page, edited\n")
	head := f.git(f.work, "rev-parse", "HEAD")
	r := f.step("commit", "")
	if r.code == 0 || !strings.Contains(r.stderr, "there are changes to commit and no message") {
		t.Fatalf("commit with changes and no message: exit %d\n%s", r.code, r.all())
	}
	if staged := f.git(f.work, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("the refusal staged %q", staged)
	}
	if got := f.git(f.work, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the refusal moved HEAD to %s", got)
	}
	// A message of blanks is no message: git would abort on it after everything was staged.
	if r := f.step("commit", " \t "); r.code != 2 || f.git(f.work, "diff", "--cached", "--name-only") != "" {
		t.Fatalf("commit with a message of blanks: exit %d\n%s", r.code, r.all())
	}
	// A status git cannot give is not a clean tree: the step stops, and says nothing of nothing to commit.
	broken := t.TempDir()
	if r := runScriptAt(t, filepath.Join(testutil.RepoRoot(t), "scripts", "commit-push.sh"), broken, f.env, "commit", ""); r.code == 0 || strings.Contains(r.stdout, "nothing to commit") {
		t.Fatalf("commit where git status fails: exit %d\n%s", r.code, r.all())
	}
	// A branch with no upstream is pushed as git pushes it, which refuses: never a success with nothing sent.
	f.git(f.work, "stash", "-q")
	f.git(f.work, "checkout", "-q", "-b", "no-upstream")
	if r := f.step("push"); r.code == 0 || strings.Contains(r.stdout, "nothing to push") {
		t.Fatalf("push of a branch with no upstream: exit %d\n%s", r.code, r.all())
	}
	if r := f.step("nonsense"); r.code != 2 {
		t.Fatalf("an unknown step: exit %d\n%s", r.code, r.all())
	}
}
