package testutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// TestQuietGitHomeStartsNothingInTheBackground holds what GitQuietConfig says: with a HOME from QuietGitHome,
// a commit, a push to a repository beside it and a fetch from that repository start no `git maintenance` and
// no `git gc`, the push's receiving end included. It is read from git's own trace.
func TestQuietGitHomeStartsNothingInTheBackground(t *testing.T) {
	t.Parallel()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	base := t.TempDir()
	work, origin, trace := filepath.Join(base, "work"), filepath.Join(base, "origin.git"), filepath.Join(base, "trace")
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + testutil.QuietGitHome(t, filepath.Join(base, "home")), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Me", "GIT_AUTHOR_EMAIL=me@example.invalid", "GIT_COMMITTER_NAME=Me", "GIT_COMMITTER_EMAIL=me@example.invalid",
		"GIT_TRACE2_EVENT=" + trace,
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), git, args...) //nolint:gosec // G204: fixed words and paths under t.TempDir()
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", work)
	run("-C", work, "commit", "-q", "--allow-empty", "-m", "a commit")
	run("init", "-q", "--bare", origin)
	run("-C", work, "push", "-q", origin, "HEAD:refs/heads/master")
	run("-C", work, "fetch", "-q", origin, "master")
	data, err := os.ReadFile(trace) //nolint:gosec // G304: a path of this test's own temp directory
	if err != nil {
		t.Fatalf("git wrote no trace: %v", err)
	}
	ran, maintenance := testutil.GitStarted(data)
	for _, word := range []string{"commit", "receive-pack", "fetch"} {
		if !strings.Contains(ran, word) {
			t.Fatalf("git's trace names no %s, so it does not show what the commit, the push and the fetch started:\n%s", word, data)
		}
	}
	for _, argv := range maintenance {
		t.Errorf("git started its own maintenance, which outlives the command and writes into the repository while the test ends: %v", argv)
	}
}

// TestGitStartedTellsMaintenanceApart holds the reading of a trace on fixed lines: what counts as git's own
// maintenance, however the program is named, and what does not.
func TestGitStartedTellsMaintenanceApart(t *testing.T) {
	t.Parallel()
	trace := strings.Join([]string{
		`{"event":"version","exe":"2.55.0"}`,
		`{"event":"start","argv":["git","commit","-q","-m","gc and maintenance in a message"]}`,
		`{"event":"child_start","argv":["git","maintenance","run","--auto","--quiet","--detach"]}`,
		`{"event":"start","argv":["/usr/libexec/git-core/git","gc","--auto"]}`,
		`{"event":"start","argv":["C:\\Program Files\\Git\\mingw64\\libexec\\git-core\\git-maintenance.exe","run","--auto"]}`,
		`{"event":"cmd_ancestry","ancestry":["gc","maintenance"]}`,
		`{"event":"start","argv":["git-receive-pack","origin.git"]}`,
		`not json`,
	}, "\n")
	ran, maintenance := testutil.GitStarted([]byte(trace))
	if len(maintenance) != 3 {
		t.Errorf("maintenance = %v, want the three lines that start git maintenance or git gc", maintenance)
	}
	for _, word := range []string{"commit", "receive-pack"} {
		if !strings.Contains(ran, word) {
			t.Errorf("ran does not name %s:\n%s", word, ran)
		}
	}
	if strings.Contains(ran, "ancestry") || strings.Contains(ran, "2.55.0") {
		t.Errorf("ran holds an event that starts nothing:\n%s", ran)
	}
}
