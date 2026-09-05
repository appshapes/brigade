package ci_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// TestFixturesAreNotGitignored fails when a file under scripts/ci/testdata
// exists on disk but is ignored by .gitignore. The P4-3 commit (4713afb)
// carried 17 stdin-writes.log fixtures that `*.log` swallowed at `git add`
// time: every local run passed against the files on disk, and CI failed
// on a clean checkout that never had them (run 33915532579). An untracked
// fixture is fine — it is about to be added; an ignored one is the bug,
// because nothing downstream will ever see it. On a clean checkout the
// list is empty by construction, so this bites where it must: in `make
// test` before a push.
func TestFixturesAreNotGitignored(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	//nolint:gosec // G204: fixed argv, the repository root is the test's own
	cmd := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "--others", "--ignored", "--exclude-standard", "--", "scripts/ci/testdata")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	if ignored := strings.TrimSpace(string(out)); ignored != "" {
		t.Fatalf("fixtures on disk that .gitignore hides from git — add a scoped negation in .gitignore or rename them:\n%s", ignored)
	}
}

// TestNoCompiledPythonTracked is the mirror of the test above: that one
// catches a fixture the ignore rules hide, this one catches a build artefact
// the ignore rules failed to hide. 25 __pycache__/*.pyc files under
// scripts/experiments/E0-4, E0-5 and E0-7 were swept into the index by the
// canonical `git add --verbose :/ .` chain (the `commit` target's stage step)
// while .gitignore carried no __pycache__ or *.pyc rule at all; Phase 6 added
// the rule, removed the 25 from the index and left them on disk, because the
// experiment drivers still run. The scope is compiled Python in the TRACKED
// set only — a blanket "no binary is tracked" rule would fight
// plugin/bin/brigade and the fixture trees. Like its sibling it bites in
// `make test`, before a push.
func TestNoCompiledPythonTracked(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	//nolint:gosec // G204: fixed argv, the repository root is the test's own
	cmd := exec.CommandContext(t.Context(), "git", "-C", root, "ls-files", "--", "*.pyc", "*.pyo", "*/__pycache__/*")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	if tracked := strings.TrimSpace(string(out)); tracked != "" {
		t.Fatalf("compiled Python is tracked — `git rm --cached` these and keep them on disk (.gitignore already ignores them):\n%s", tracked)
	}
}
