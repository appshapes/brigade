//go:build !windows

// The chain of `make push` (work item 423; the owner's ruling of 2026-11-23). `make push` is `make commit` and a
// push, and `make commit` is its prerequisites and then the staging: until this work item they were typecheck,
// pull, build and test, and twice a push that had passed them turned `master` red in CI's `lint` job (77579966,
// a test helper from a file tagged `!windows`; e3e07811, a test's comparison of an error with itself).
//
// What is held here is the Makefile's line and the places that say the chain in words, so that one does not
// move without the others: lint is in the chain, after the pull (the merged tree is what is pushed) and before
// the build and the tests (a fault stops the push in seconds), and nothing is staged by a prerequisite.
package ci_test

import (
	"os"
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
	if len(recipe) == 0 || !strings.HasPrefix(recipe[0], "git add ") {
		t.Errorf("make commit's recipe no longer begins with the staging: %q", recipe)
	}

	prerequisites, recipe, ok = makeRule(makefile, "push")
	if !ok {
		t.Fatal("the Makefile has no push target")
	}
	if !slices.Equal(prerequisites, []string{"commit"}) {
		t.Errorf("make push runs %v before it pushes; it is make commit, and nothing else", prerequisites)
	}
	if !slices.ContainsFunc(recipe, func(line string) bool { return strings.HasPrefix(line, "git push") }) {
		t.Errorf("make push's recipe does not push: %q", recipe)
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
