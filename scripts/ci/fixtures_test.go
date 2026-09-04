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
