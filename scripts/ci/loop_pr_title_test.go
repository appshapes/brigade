//go:build !windows

// The title the agentic loop mints for a pull request (work item 292). claude.yml's `open-pr` job takes it from
// the issue's: a title that names its work item is kept, and any other is a maintenance work order and takes
// `15` (conventions.md). The squash merge lands that title as the commit's subject, so a title kept by mistake
// is a commit whose prefix is no work item's number: the pattern before this, `[0-9]*": "*`, kept
// `3 flaky tests: retry the watcher`.
//
// The lines are run for real: cut out of the workflow and handed to a shell with a title, once in `sh` and once
// in `bash`, which is what a runner runs them in.
package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// mintLines are the lines of claude.yml that set PR_TITLE from TITLE, without their indentation.
func mintLines(t *testing.T) string {
	t.Helper()
	//nolint:gosec // G304: a fixed path under the repository's root.
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), ".github", "workflows", "claude.yml"))
	if err != nil {
		t.Fatal(err)
	}
	const first, after = "          number=${TITLE%%:*}\n", "          # Labels are copied from the ISSUE"
	workflow := string(data)
	if strings.Count(workflow, first) != 1 || strings.Count(workflow, after) != 1 {
		t.Fatalf("claude.yml does not hold the lines that mint a pull request's title between %q and %q, once each", strings.TrimSpace(first), strings.TrimSpace(after))
	}
	block := workflow[strings.Index(workflow, first):strings.Index(workflow, after)]
	if !strings.Contains(block, "PR_TITLE=") || strings.Contains(block, "gh ") {
		t.Fatalf("the lines cut out of claude.yml are not the minting alone:\n%s", block)
	}
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	return strings.Join(lines, "\n")
}

func TestTheLoopKeepsATitleOnlyWhenItNamesAWorkItem(t *testing.T) {
	t.Parallel()
	script := filepath.Join(t.TempDir(), "mint.sh")
	body := "TITLE=$1\n" + mintLines(t) + "\nprintf '%s' \"$PR_TITLE\"\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ title, want string }{
		// A title that names its work item is kept.
		{"22: Add the thing", "22: Add the thing"},
		{"15: Refresh the pins", "15: Refresh the pins"},
		{"388: Fix a: b, and c", "388: Fix a: b, and c"},
		{"22: Add * things [to] the ?list", "22: Add * things [to] the ?list"},
		// The two of the work item: a digit first and a colon further on name no work item.
		{"3 flaky tests: retry the watcher", "15: 3 flaky tests: retry the watcher"},
		{"2026 refresh: update the pins", "15: 2026 refresh: update the pins"},
		// The scheduled callers' titles, and what else is no `<number>: <title>`.
		{"Refresh the documentation", "15: Refresh the documentation"},
		{"Refresh: the documentation", "15: Refresh: the documentation"},
		{"22:Add the thing", "15: 22:Add the thing"},
		{"22 : Add the thing", "15: 22 : Add the thing"},
		{"22", "15: 22"},
		{"22:", "15: 22:"},
		{": Add the thing", "15: : Add the thing"},
		{"v2: Add the thing", "15: v2: Add the thing"},
		{"2a: Add the thing", "15: 2a: Add the thing"},
		{"-1: Add the thing", "15: -1: Add the thing"},
		{"Add 22: the thing", "15: Add 22: the thing"},
	} {
		for _, shell := range []string{"sh", "bash"} {
			//nolint:gosec // G204: a shell by its fixed name, on a script under this test's t.TempDir().
			out, err := exec.CommandContext(t.Context(), shell, script, tc.title).CombinedOutput()
			if err != nil {
				t.Fatalf("%s on %q: %v\n%s", shell, tc.title, err, out)
			}
			if string(out) != tc.want {
				t.Errorf("%s mints %q for the issue %q, want %q", shell, out, tc.title, tc.want)
			}
		}
	}
}
