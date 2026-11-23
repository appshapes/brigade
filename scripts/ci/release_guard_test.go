//go:build !windows

// scripts/ci/release-guard.sh: the guard of release.yml and server-release.yml (work item 416). Before anything
// is published for a tag, the tag's commit is on master and ci.yml's run on it is green: the check `make release`
// and `make server-tag` make on the machine that releases, where a tag pushed by hand meets it too.
//
// An override the owner allowed is read from the tag's message, where the release scripts write it (ADR 0028).
//
// The script is run for real against the stand-in `gh` of release_ci_check_test.go, which answers one line a
// call: how master stands to the commit, the tag's ref, for an annotated tag the commit it names and its
// message's override lines, and then ci.yml's run.
package ci_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const releaseGuardRel = "scripts/ci/release-guard.sh"

const releaseGuardTag = "v1.2.3"

// What the stand-in answers for the tag: its ref (an annotated tag's object, or the commit itself), and for an
// annotated one the commit it names followed by the override lines of its message.
const (
	guardTagObject  = "tag 89abcdef0123456789abcdef0123456789abcdef"
	guardNoOverride = releaseCISha + " "
	guardOverRun7   = releaseCISha + " release_over_ci=7"
	guardOverNoRun  = releaseCISha + " release_over_ci=missing:" + releaseCISha
)

// guardRun runs the guard on the fixture's commit and tag with what GitHub answers, in a repository's name.
func guardRun(t *testing.T, script string, answers []string, env ...string) (result, *ghStub) {
	t.Helper()
	g := newGhStub(t)
	g.answers(answers...)
	env = append([]string{"GITHUB_REPOSITORY=example/brigade", "RELEASE_CI_APPEAR_SECONDS=0"}, env...)
	return g.run(script, env, releaseCISha, releaseGuardTag), g
}

func TestTheReleaseGuardLetsThroughATagOnMasterWithAGreenRunAndNothingElse(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), releaseGuardRel)
	otherCommit := strings.Repeat("f", 40)
	for _, tc := range []struct {
		name    string
		answers []string
		env     []string
		code    int
		prints  []string
		never   []string
		calls   int
	}{
		{
			"on master with a green run", []string{"ahead", guardTagObject, guardNoOverride, ciGreen}, nil, 0,
			[]string{releaseCISha + " is on master", "the message of v1.2.3 carries no override", ": success (run 7, ", "the release goes on"},
			[]string{"nothing is published"}, 4,
		},
		{
			"master's own head with a green run", []string{"identical", guardTagObject, guardNoOverride, ciGreen}, nil, 0,
			[]string{releaseCISha + " is on master", "the release goes on"}, nil, 4,
		},
		{
			"a commit master does not hold, ahead of it", []string{"behind", guardTagObject, guardNoOverride, ciGreen}, nil, 1,
			[]string{releaseCISha + " is not on master (master is 'behind' to it): nothing is published"},
			[]string{"the release goes on", "ci.yml"}, 1,
		},
		{
			"a commit of a branch that left master", []string{"diverged", guardTagObject, guardNoOverride, ciGreen}, nil, 1,
			[]string{"is not on master (master is 'diverged' to it)"}, []string{"the release goes on", "ci.yml"}, 1,
		},
		{
			"an answer that is no standing", []string{"-", guardTagObject, guardNoOverride, ciGreen}, nil, 1,
			[]string{"is not on master"}, []string{"the release goes on"}, 1,
		},
		{
			"GitHub cannot be asked about master", []string{"!fail", guardTagObject, guardNoOverride, ciGreen}, nil, 1,
			[]string{"could not ask GitHub how master stands", "nothing is published blind"}, []string{"the release goes on"}, 1,
		},
		{
			"on master with a red run", []string{"ahead", guardTagObject, guardNoOverride, ciRed}, nil, 1,
			[]string{": failure (run 7, ", "ci.yml is not green on " + releaseCISha, "nothing is published"},
			[]string{"the release goes on"}, 4,
		},
		{
			"on master with no run", []string{"ahead", guardTagObject, guardNoOverride, "-"}, nil, 1,
			[]string{": no run", "nothing is published"}, []string{"the release goes on"}, 0,
		},
		{
			"a run that does not end in the guard's time", []string{"ahead", guardTagObject, guardNoOverride, ciRunning}, []string{"RELEASE_CI_WAIT_SECONDS=1"}, 1,
			[]string{"is still in_progress after 1s", "nothing is published"}, []string{"the release goes on"}, 0,
		},
		// The override, read from the tag's message (ADR 0028).
		{
			"a red run and the message's override that names it", []string{"ahead", guardTagObject, guardOverRun7, ciRed}, nil, 0,
			[]string{"the message of v1.2.3 carries release_over_ci=7", "releasing over the red run 7", "the release goes on"},
			[]string{"nothing is published"}, 4,
		},
		{
			"a red run and an override for another run", []string{"ahead", guardTagObject, releaseCISha + " release_over_ci=8", ciRed}, nil, 1,
			[]string{"release_over_ci='8' does not name this run", "nothing is published"}, []string{"the release goes on"}, 4,
		},
		{
			"no run and the message's override that names the commit", []string{"ahead", guardTagObject, guardOverNoRun, "-"}, nil, 0,
			[]string{"releasing over a missing run", "the release goes on"}, []string{"nothing is published"}, 0,
		},
		{
			"no run and an override for another commit", []string{"ahead", guardTagObject, releaseCISha + " release_over_ci=missing:" + otherCommit, "-"}, nil, 1,
			[]string{"does not name this commit's missing run", "nothing is published"}, []string{"the release goes on"}, 0,
		},
		{
			"a green run and an override that was not needed", []string{"ahead", guardTagObject, guardOverRun7, ciGreen}, nil, 0,
			[]string{"was given and is not needed: the run is green", "the release goes on"}, nil, 4,
		},
		{
			"an override the check cannot read", []string{"ahead", guardTagObject, releaseCISha + " release_over_ci=yes", ciRed}, nil, 1,
			[]string{"is neither a run's id nor 'missing:<the commit's sha>'", "nothing is published"}, []string{"the release goes on"}, 3,
		},
		{
			"two override lines in one message", []string{"ahead", guardTagObject, releaseCISha + " release_over_ci=7 release_over_ci=8", ciRed}, nil, 1,
			[]string{"carries more than one release_over_ci line", "nothing is published"}, []string{"the release goes on", "ci.yml on"}, 3,
		},
		// A tag pushed by hand without a message is the commit itself: it has no override to give.
		{
			"a tag with no message, on a green run", []string{"ahead", "commit " + releaseCISha, ciGreen}, nil, 0,
			[]string{"v1.2.3 has no message: no override", "the release goes on"}, nil, 3,
		},
		{
			"a tag with no message, on a red run", []string{"ahead", "commit " + releaseCISha, ciRed}, nil, 1,
			[]string{"v1.2.3 has no message: no override", ": failure (run 7, ", "nothing is published"}, []string{"the release goes on"}, 3,
		},
		{
			"a tag whose message names another commit", []string{"ahead", guardTagObject, otherCommit + " release_over_ci=7", ciRed}, nil, 1,
			[]string{"the tag v1.2.3 names the commit '" + otherCommit + "', not " + releaseCISha, "nothing is published"},
			[]string{"the release goes on", "ci.yml on"}, 3,
		},
		{
			"a ref that is neither", []string{"ahead", "tree " + releaseCISha, ciGreen}, nil, 1,
			[]string{"which is neither the commit", "nothing is published"}, []string{"the release goes on"}, 2,
		},
		{
			"GitHub cannot be asked for the tag", []string{"ahead", "!fail", guardNoOverride, ciGreen}, nil, 1,
			[]string{"could not ask GitHub for the tag v1.2.3", "nothing is published blind"}, []string{"the release goes on"}, 2,
		},
		{
			"GitHub cannot be asked for the tag's message", []string{"ahead", guardTagObject, "!fail", ciGreen}, nil, 1,
			[]string{"could not ask GitHub for the message of the tag v1.2.3", "nothing is published blind"}, []string{"the release goes on"}, 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, g := guardRun(t, script, tc.answers, tc.env...)
			out := r.stdout + r.stderr
			if r.code != tc.code {
				t.Fatalf("exit %d, want %d:\n%s", r.code, tc.code, out)
			}
			for _, want := range tc.prints {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in:\n%s", want, out)
				}
			}
			for _, never := range tc.never {
				if strings.Contains(out, never) {
					t.Errorf("%q in:\n%s", never, out)
				}
			}
			if tc.calls != 0 && g.calls() != tc.calls {
				t.Errorf("gh was called %d times, want %d:\n%s", g.calls(), tc.calls, g.log())
			}
		})
	}
}

// What the guard asks GitHub, in the repository the workflow runs in: how master stands to the commit, what the
// tag's ref is, the tag's message, and then ci.yml's runs on that commit.
func TestTheReleaseGuardAsksAboutTheCommitAndTheTagInTheWorkflowsRepository(t *testing.T) {
	t.Parallel()
	r, g := guardRun(t, filepath.Join(testutil.RepoRoot(t), releaseGuardRel), []string{"ahead", guardTagObject, guardNoOverride, ciGreen})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(g.log()), "\n")
	if len(lines) != 4 {
		t.Fatalf("gh was called %d times:\n%s", len(lines), g.log())
	}
	if want := "gh api repos/example/brigade/compare/" + releaseCISha + "...master --jq .status"; lines[0] != want {
		t.Errorf("the first call is %q, want %q", lines[0], want)
	}
	if want := "gh api repos/example/brigade/git/ref/tags/" + releaseGuardTag + " --jq "; !strings.HasPrefix(lines[1], want) {
		t.Errorf("the second call is %q, want the tag's ref: %q…", lines[1], want)
	}
	if want := "gh api repos/example/brigade/git/tags/" + strings.TrimPrefix(guardTagObject, "tag ") + " --jq "; !strings.HasPrefix(lines[2], want) {
		t.Errorf("the third call is %q, want the tag's object: %q…", lines[2], want)
	}
	if !strings.Contains(lines[2], `startswith("release_over_ci=")`) {
		t.Errorf("the third call does not pick the override's lines out of the message: %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], "gh run list --workflow ci.yml --commit "+releaseCISha+" ") {
		t.Errorf("the fourth call is %q, want ci.yml's runs on the commit", lines[3])
	}
}

// The filter that reads the tag's message is run for real, by jq, on a tag object as GitHub answers it: the
// commit the tag names, then the override's lines and no other line of the message.
func TestTheReleaseGuardsFilterReadsTheOverrideOutOfATagsMessage(t *testing.T) {
	t.Parallel()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is not on PATH")
	}
	//nolint:gosec // G304: a fixed path under the repository's root.
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), releaseGuardRel))
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(data), "git/tags/${ref#tag }\" \\\n      --jq '")
	filter, _, ok2 := strings.Cut(after, "'); then")
	if !ok || !ok2 {
		t.Fatal("the guard's filter for a tag's message is not where this test looks for it")
	}
	for name, tc := range map[string]struct{ message, want string }{
		"a script's tag with no override":    {"v1.2.3\n", releaseCISha + " "},
		"a script's tag with the override":   {"v1.2.3\n\nrelease_over_ci=7\n", releaseCISha + " release_over_ci=7"},
		"the server's, over a missing run":   {"Brigade server 1.2.3\n\nrelease_over_ci=missing:" + releaseCISha + "\n", releaseCISha + " release_over_ci=missing:" + releaseCISha},
		"the words inside a line are not it": {"v1.2.3\n\nsee release_over_ci=7 in the log\n", releaseCISha + " "},
		"two lines":                          {"v1.2.3\nrelease_over_ci=7\nrelease_over_ci=8\n", releaseCISha + " release_over_ci=7 release_over_ci=8"},
	} {
		object, err := json.Marshal(map[string]any{"message": tc.message, "object": map[string]string{"sha": releaseCISha, "type": "commit"}})
		if err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // G204: jq, found on PATH, with the filter cut out of the repository's own script.
		cmd := exec.CommandContext(t.Context(), jq, "-r", filter)
		cmd.Stdin = strings.NewReader(string(object))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: jq: %v\n%s", name, err, out)
			continue
		}
		if got := strings.TrimRight(string(out), "\n"); got != tc.want {
			t.Errorf("%s: the filter printed %q, want %q", name, got, tc.want)
		}
	}
}

func TestTheReleaseGuardRefusesWhatItCannotCheck(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), releaseGuardRel)
	g := newGhStub(t)
	g.answers("ahead", guardTagObject, guardNoOverride, ciGreen)
	for name, tc := range map[string]struct {
		env  []string
		args []string
		want string
	}{
		"a short sha":        {[]string{"GITHUB_REPOSITORY=example/brigade"}, []string{releaseCISha[:12], releaseGuardTag}, "pass the full 40-character commit sha"},
		"no sha":             {[]string{"GITHUB_REPOSITORY=example/brigade"}, []string{"", releaseGuardTag}, "usage: scripts/ci/release-guard.sh"},
		"no tag":             {[]string{"GITHUB_REPOSITORY=example/brigade"}, []string{releaseCISha}, "got the tag ''"},
		"a tag with a slash": {[]string{"GITHUB_REPOSITORY=example/brigade"}, []string{releaseCISha, "refs/tags/v1.2.3"}, "got the tag 'refs/tags/v1.2.3'"},
		"no repository":      {nil, []string{releaseCISha, releaseGuardTag}, "GITHUB_REPOSITORY is not set"},
	} {
		r := g.run(script, tc.env, tc.args...)
		if r.code != 1 || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%s: exit %d, want 1 and %q:\n%s%s", name, r.code, tc.want, r.stdout, r.stderr)
		}
	}
	if g.calls() != 0 {
		t.Errorf("gh was asked before the arguments were read:\n%s", g.log())
	}
}

// TestTheReleaseWorkflowsRunTheGuardBeforeTheyBuild: each release workflow runs the guard on the commit it
// checked out, with a token that may read the runs, before the first step that builds or publishes.
func TestTheReleaseWorkflowsRunTheGuardBeforeTheyBuild(t *testing.T) {
	t.Parallel()
	const guard = `scripts/ci/release-guard.sh "$(git rev-parse HEAD)" "$GITHUB_REF_NAME"`
	for file, tc := range map[string]struct {
		before []string // what the guard comes before
		reads  string   // the permission that lets the job's token list the runs
	}{
		"release.yml":        {[]string{"      - uses: actions/setup-go@", "      - uses: goreleaser/goreleaser-action@", "      - name: Publish\n"}, "\n  actions: write "},
		"server-release.yml": {[]string{"\n  linux:\n", "\n  macos:\n", "\n  image:\n", "\n  publish:\n"}, "\n  actions: read "},
	} {
		//nolint:gosec // G304: a fixed path under the repository's root.
		data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), ".github", "workflows", file))
		if err != nil {
			t.Fatal(err)
		}
		workflow := string(data)
		if strings.Count(workflow, guard) != 1 {
			t.Errorf("%s runs the guard %d times, want once: %s", file, strings.Count(workflow, guard), guard)
			continue
		}
		at := strings.Index(workflow, guard)
		for _, later := range tc.before {
			if next := strings.Index(workflow, later); next < 0 || next < at {
				t.Errorf("%s: the guard does not come before %q", file, strings.TrimSpace(later))
			}
		}
		// The line itself, at the indentation of a `permissions:` entry: the words alone are in comments too.
		_, permissions, _ := strings.Cut(workflow, "\npermissions:")
		permissions, _, _ = strings.Cut(permissions, "\n\n")
		if !strings.Contains(permissions+" ", tc.reads) {
			t.Errorf("%s's permissions do not grant %q, which the guard's read of ci.yml's runs needs:%s", file, strings.TrimSpace(tc.reads), permissions)
		}
	}
	// Every job of the server's release waits on the job the guard is in.
	//nolint:gosec // G304: a fixed path under the repository's root.
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), ".github", "workflows", "server-release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"linux", "macos", "image"} {
		_, after, _ := strings.Cut(string(data), "\n  "+job+":\n")
		if head, _, _ := strings.Cut(after, "\n    steps:\n"); !strings.Contains(head, "needs: guard") {
			t.Errorf("server-release.yml: the job %s does not wait on guard", job)
		}
	}
}
