//go:build !windows

// What an agent's checkout holds, and the lines of claude.yml that keep a run of it from vanishing (work item
// 398, 2026-11-22; brought to the form work item 433 left on 2026-11-24).
//
// Both were found on the issue-driven loop, which work item 433 removed. Three `labeled` runs of one work
// order met in claude.yml's concurrency group, which held one waiting run, and the `claude` one was cancelled
// by a newer one (2026-10-01, issue #50: no agent ran and nothing said so). And an agent ended in `success`
// with two pages edited and nothing committed, and its issue was closed as "finished without changes"
// (2026-11-22, issue #118).
//
// This file holds what stands against both, where each can still happen:
//   - scripts/ci/agent-leftover.sh, run for real in fixture checkouts: it says whether an agent's checkout
//     holds changed paths or unpushed commits, and names a `make` or `go` still running. The gate of the
//     scheduled jobs calls it now (scripts/ci/scheduled-job.sh; scheduled_job_test.go holds that call);
//   - the lines of claude.yml that a pull request's agent still needs: every run of the group waits its turn,
//     a pull request starts an agent by the `claude` label's own event, a long command stays in the
//     foreground, and nothing an issue does starts an agent.
//
// Both halves run twice, on the repository and on mutated copies, where silence is the bug.
package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const leftoverScriptRel = "scripts/ci/agent-leftover.sh"

// ---------------------------------------------------------------------------------------------------------
// the script, in a fixture checkout

// leftoverRepo is a checkout as the agent job has it: a clone with one pushed commit on `master`, on a local
// branch the action made and never pushed. PATH leads with a stand-in for `pgrep`, because the real one would
// find the `go` of `go test` and the `make` of `make test` on every run. The stand-in is a link to the
// machine's own `false` (nothing is running) or `true` (everything asked for is), never a script written here:
// a fresh file exec'd beside parallel forks is the ETXTBSY of invariants.md.
type leftoverRepo struct {
	t     *testing.T
	work  string
	env   []string
	shims string // the directory PATH leads with
}

func newLeftoverRepo(t *testing.T) *leftoverRepo {
	t.Helper()
	base := t.TempDir()
	shims := filepath.Join(base, "shims")
	r := &leftoverRepo{t: t, work: filepath.Join(base, "work"), shims: shims}
	for _, dir := range []string{shims, r.work, filepath.Join(base, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	r.pgrepAnswers("false")
	r.env = append(baseEnv(t, filepath.Join(base, "home")),
		"GIT_COMMITTER_NAME=ci-test", "GIT_COMMITTER_EMAIL=ci-test@example.invalid")
	for i, kv := range r.env {
		if strings.HasPrefix(kv, "PATH=") {
			r.env[i] = "PATH=" + shims + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
	}
	origin := filepath.Join(base, "origin.git")
	r.gitIn(base, "init", "--quiet", "--bare", "--initial-branch=master", origin)
	r.git("init", "--quiet", "--initial-branch=master")
	r.write(filepath.Join(r.work, ".gitignore"), "bin/\n")
	r.write(filepath.Join(r.work, "README.md"), "a page\n")
	r.git("add", ".gitignore", "README.md")
	r.git("commit", "--quiet", "-m", "first")
	r.git("remote", "add", "origin", origin)
	r.git("push", "--quiet", "--set-upstream", "origin", "master")
	r.git("checkout", "--quiet", "-b", "claude/issue-1-20261122-1037")
	return r
}

func (r *leftoverRepo) write(file, text string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		r.t.Fatalf("creating the directory of %s: %v", file, err)
	}
	//nolint:gosec // G703: every caller's path is under the fixture's own t.TempDir().
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		r.t.Fatalf("writing %s: %v", file, err)
	}
}

// pgrepAnswers makes the stand-in `pgrep` a link to the named program, `true` or `false`.
func (r *leftoverRepo) pgrepAnswers(program string) {
	r.t.Helper()
	target, err := exec.LookPath(program)
	if err != nil {
		r.t.Fatalf("this machine has no %s to stand in for pgrep: %v", program, err)
	}
	link := filepath.Join(r.shims, "pgrep")
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		r.t.Fatalf("removing %s: %v", link, err)
	}
	if err := os.Symlink(target, link); err != nil {
		r.t.Fatalf("linking %s to %s: %v", link, target, err)
	}
}

func (r *leftoverRepo) git(args ...string) { r.t.Helper(); r.gitIn(r.work, args...) }

func (r *leftoverRepo) gitIn(dir string, args ...string) {
	r.t.Helper()
	//nolint:gosec // G204: git with an argument array the test itself wrote, in a fixture directory.
	cmd := exec.CommandContext(r.t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = r.env
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// leftoverRun is what one run of the script gave: its output and the two values it wrote to $GITHUB_OUTPUT.
type leftoverRun struct {
	result
	left    string
	summary string
}

func (r *leftoverRepo) run(script string) leftoverRun {
	r.t.Helper()
	out := filepath.Join(r.t.TempDir(), "github-output")
	res := runScriptAt(r.t, script, r.work, append(r.env, "GITHUB_OUTPUT="+out))
	run := leftoverRun{result: res}
	//nolint:gosec // G304: out is under t.TempDir().
	data, err := os.ReadFile(out)
	if err != nil {
		return run // the script wrote no output file: the case reports it through `left`
	}
	text := string(data)
	if rest, ok := strings.CutPrefix(text, "left="); ok {
		run.left, _, _ = strings.Cut(rest, "\n")
	}
	if _, after, ok := strings.Cut(text, "summary<<AGENT_LEFTOVER_SUMMARY\n"); ok {
		run.summary, _, _ = strings.Cut(after, "AGENT_LEFTOVER_SUMMARY\n")
	}
	return run
}

// leftoverCases are the states of a checkout the script must tell apart. Each prepares the fixture, then
// says what `left` must be, what the summary must carry and what no output may carry.
var leftoverCases = []struct {
	name    string
	prepare func(r *leftoverRepo)
	left    string
	carries []string
	never   []string
}{
	{
		"an_untouched_checkout_left_nothing",
		func(*leftoverRepo) {},
		"false",
		[]string{"nothing: no path changed, no commit unpushed, nothing stashed, no make or go still running"},
		[]string{"::warning::"},
	},
	{
		"a_build_leaves_nothing",
		func(r *leftoverRepo) { r.write(filepath.Join(r.work, "bin", "brigade"), "built\n") },
		"false", []string{"nothing: no path changed"}, []string{"bin/brigade", "::warning::"},
	},
	{
		"an_edited_page_is_left",
		func(r *leftoverRepo) { r.write(filepath.Join(r.work, "README.md"), "a page, edited\n") },
		"true", []string{"1 changed or untracked path(s):", " M README.md", "::warning::"}, nil,
	},
	{
		"a_new_file_is_left",
		func(r *leftoverRepo) { r.write(filepath.Join(r.work, "docs", "new.md"), "new\n") },
		"true", []string{"1 changed or untracked path(s):", "?? docs/"}, nil,
	},
	{
		"a_commit_never_pushed_is_left",
		func(r *leftoverRepo) {
			r.write(filepath.Join(r.work, "README.md"), "a page, edited\n")
			r.git("commit", "--quiet", "-am", "a subject the summary must not carry")
		},
		"true", []string{"1 commit(s) that no remote branch carries: "}, []string{"a subject the summary must not carry"},
	},
	{
		"a_pushed_commit_is_not_left",
		func(r *leftoverRepo) {
			r.write(filepath.Join(r.work, "README.md"), "a page, edited\n")
			r.git("commit", "--quiet", "-am", "pushed")
			r.git("push", "--quiet", "origin", "claude/issue-1-20261122-1037")
		},
		"false", []string{"nothing: no path changed"}, []string{"::warning::"},
	},
	{
		"a_commit_on_a_branch_that_is_no_longer_checked_out_is_left",
		func(r *leftoverRepo) {
			r.write(filepath.Join(r.work, "README.md"), "a page, edited\n")
			r.git("commit", "--quiet", "-am", "on the agent's branch")
			r.git("checkout", "--quiet", "master")
		},
		"true", []string{"1 commit(s) that no remote branch carries: "}, []string{"nothing:"},
	},
	{
		"stashed_work_is_left",
		func(r *leftoverRepo) {
			r.write(filepath.Join(r.work, "README.md"), "a page, edited\n")
			r.git("stash", "push", "--quiet", "-m", "a message the summary must not carry")
		},
		"true", []string{"1 stash(es)", "::warning::"}, []string{"a message the summary must not carry", "nothing:"},
	},
	{
		"a_name_that_is_not_plain_is_counted_and_not_shown",
		func(r *leftoverRepo) {
			r.write(filepath.Join(r.work, "plain.md"), "plain\n")
			r.write(filepath.Join(r.work, "$(odd)`name`.md"), "odd\n")
		},
		"true", []string{"2 changed or untracked path(s):", "?? plain.md", "and 1 more, not shown"}, []string{"odd"},
	},
	{
		"more_than_twenty_paths_are_cut",
		func(r *leftoverRepo) {
			for i := range 23 {
				r.write(filepath.Join(r.work, "page-"+strconv.Itoa(100+i)+".md"), "page\n")
			}
		},
		"true", []string{"23 changed or untracked path(s):", "?? page-119.md", "and 3 more, not shown"}, []string{"page-120.md"},
	},
	{
		"a_make_still_running_is_named_and_is_not_work_left",
		func(r *leftoverRepo) { r.pgrepAnswers("true") },
		"false", []string{"still running when the session ended: make go", "::warning::"}, []string{"nothing:"},
	},
}

// checkLeftoverScript runs every case against one copy of the script and reports each that does not hold.
func checkLeftoverScript(t *testing.T, rep reporter, script string, only string) {
	t.Helper()
	rep.Helper()
	for _, c := range leftoverCases {
		if only != "" && c.name != only {
			continue
		}
		r := newLeftoverRepo(t)
		c.prepare(r)
		run := r.run(script)
		if run.code != 0 {
			rep.Errorf("%s: exit %d, and the script must never fail on what it finds\n%s", c.name, run.code, run.all())
			continue
		}
		if run.left != c.left {
			rep.Errorf("%s: left=%q in $GITHUB_OUTPUT, want %q\n%s", c.name, run.left, c.left, run.all())
		}
		for _, want := range c.carries {
			if !strings.Contains(run.stdout, want) {
				rep.Errorf("%s: the output does not carry %q\n%s", c.name, want, run.all())
			}
		}
		for _, bad := range c.never {
			if strings.Contains(run.all(), bad) || strings.Contains(run.summary, bad) {
				rep.Errorf("%s: the output carries %q, which it must not\n%s", c.name, bad, run.all())
			}
		}
		// The summary output is the lines printed, less the annotation: what open-pr puts in its comment.
		for _, line := range strings.Split(strings.TrimSpace(run.summary), "\n") {
			if !strings.Contains(run.stdout, line) || strings.HasPrefix(line, "::") {
				rep.Errorf("%s: the summary output carries a line the script did not print, or an annotation: %q", c.name, line)
			}
		}
	}
}

func TestAgentLeftoverSaysWhatACheckoutHolds(t *testing.T) {
	t.Parallel()
	checkLeftoverScript(t, t, filepath.Join(testutil.RepoRoot(t), leftoverScriptRel), "")
}

// The script without $GITHUB_OUTPUT prints and ends well: a person can run it in any checkout.
func TestAgentLeftoverRunsWithoutAnOutputFile(t *testing.T) {
	t.Parallel()
	r := newLeftoverRepo(t)
	r.write(filepath.Join(r.work, "README.md"), "a page, edited\n")
	res := runScriptAt(t, filepath.Join(testutil.RepoRoot(t), leftoverScriptRel), r.work, r.env)
	wantPass(t, res, " M README.md")
}

// leftoverScriptMutations: one edit to a copy of the script, and the case that must then fail.
var leftoverScriptMutations = []struct {
	name, old, replacement, caughtBy, because string
}{
	{
		"it_stops_counting_unpushed_commits",
		"unpushed=$(git rev-list --count HEAD --branches --not --remotes)", "unpushed=0",
		"a_commit_never_pushed_is_left", "a commit made and never pushed is work left behind",
	},
	{
		"it_counts_the_commits_of_head_alone",
		"unpushed=$(git rev-list --count HEAD --branches --not --remotes)", "unpushed=$(git rev-list --count HEAD --not --remotes)",
		"a_commit_on_a_branch_that_is_no_longer_checked_out_is_left", "an agent that committed and then checked out another branch",
	},
	{
		"it_stops_looking_at_the_stash",
		"stashed=$(git stash list | grep -c . || true)", "stashed=0",
		"stashed_work_is_left", "work put aside and never taken back is work left behind",
	},
	{
		"it_stops_seeing_new_files",
		"status=$(git status --porcelain)", "status=$(git status --porcelain --untracked-files=no)",
		"a_new_file_is_left", "a page the agent added is work left behind",
	},
	{
		"it_wants_both_a_change_and_a_commit",
		`[ "$changed" -gt 0 ] || [ "$unpushed" -gt 0 ] ||`, `[ "$changed" -gt 0 ] && [ "$unpushed" -gt 0 ] ||`,
		"an_edited_page_is_left", "the case of 2026-11-22: edits and no commit at all",
	},
	{
		"it_shows_every_name",
		`grep -E '^[ A-Z?!]{2} [A-Za-z0-9._/ >-]+$'`, "grep -E '.'",
		"a_name_that_is_not_plain_is_counted_and_not_shown", "the summary reaches a comment on a public issue",
	},
	{
		"it_shows_commit_subjects",
		"git rev-list --abbrev-commit --max-count=10 HEAD --branches --not --remotes", "git log --format=%s --max-count=10 HEAD --branches --not --remotes",
		"a_commit_never_pushed_is_left", "a commit's subject is text the agent wrote",
	},
	{
		"it_stops_looking_for_a_running_make",
		"for name in make go; do", "for name in go; do",
		"a_make_still_running_is_named_and_is_not_work_left", "a command the session did not wait for is the cause to name",
	},
}

func TestAgentLeftoverCasesFailOnAMutatedScript(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G304: the repository's own script.
	original, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), leftoverScriptRel))
	if err != nil {
		t.Fatalf("reading %s: %v", leftoverScriptRel, err)
	}
	for _, m := range leftoverScriptMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(string(original), m.old) {
				t.Fatalf("%s no longer contains the anchor %q, so this mutation would be vacuous", leftoverScriptRel, m.old)
			}
			script := filepath.Join(t.TempDir(), "agent-leftover.sh")
			// Mode 0600: runScriptAt hands the copy to `sh` as an argument, so it is read, never exec'd.
			//nolint:gosec // G703: script is filepath.Join(t.TempDir(), <a constant name>).
			if err := os.WriteFile(script, []byte(strings.ReplaceAll(string(original), m.old, m.replacement)), 0o600); err != nil {
				t.Fatalf("writing the mutated script: %v", err)
			}
			var rec recorder
			checkLeftoverScript(t, &rec, script, m.caughtBy)
			if len(rec.msgs) == 0 {
				t.Fatalf("case %s passed on the mutated script, so it does not hold what it says (%s)", m.caughtBy, m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}

// TestAgentLeftoverWritesItsCounts holds the four counts the gate of the scheduled jobs reads beside `left`
// (work item 433): a writer's own commits are unpushed by design there, so `left` alone cannot tell them
// from work that was never committed.
func TestAgentLeftoverWritesItsCounts(t *testing.T) {
	t.Parallel()
	r := newLeftoverRepo(t)
	r.write(filepath.Join(r.work, "README.md"), "a page, edited\n")
	r.write(filepath.Join(r.work, "docs", "new.md"), "a new page\n")
	r.git("add", "docs/new.md")
	r.git("commit", "--quiet", "-m", "a commit never pushed")
	r.write(filepath.Join(r.work, "docs", "new.md"), "a new page, edited\n")
	r.git("stash", "push", "--quiet", "--", "docs/new.md")
	out := filepath.Join(t.TempDir(), "github-output")
	res := runScriptAt(t, filepath.Join(testutil.RepoRoot(t), leftoverScriptRel), r.work, append(r.env, "GITHUB_OUTPUT="+out))
	wantPass(t, res)
	//nolint:gosec // G304: out is under t.TempDir().
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the script wrote no outputs: %v", err)
	}
	for _, want := range []string{"left=true\n", "\nchanged=1\n", "\nunpushed=1\n", "\nstashed=1\n", "\nrunning=\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the outputs do not carry %q:\n%s", want, data)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// the lines of claude.yml

// checkHubLines reports what claude.yml under root lacks of the lines work item 398 put there and work item
// 433 left, and what it carries of the issue-driven loop that work item 433 removed.
func checkHubLines(r reporter, root string) {
	r.Helper()
	text, ok := readText(r, root, loopHubRel)
	if !ok {
		return
	}
	for _, want := range []struct{ line, why string }{
		{
			"\nconcurrency:\n  group: claude-${{ github.event.pull_request.number || github.event.issue.number || github.run_id }}\n" +
				"  cancel-in-progress: false\n  queue: max\n",
			"every run of a pull request waits its turn: without `queue: max` a newer waiting run takes the place " +
				"of an older one, and that older one can be the one a person asked for",
		},
		{
			"  pull_request:\n    types: [labeled]\n",
			"a pull request starts an agent by the `claude` label's own event; `opened` is not listened for",
		},
		{
			"        (github.event_name == 'pull_request' && github.event.action == 'labeled' && github.event.label.name == 'claude')\n",
			"with every run waiting its turn, each event that matches runs an agent of its own: a pull request " +
				"labelled `claude` beside other labels must match once",
		},
		{
			"        (github.event_name == 'issue_comment' && github.event.issue.pull_request && contains(github.event.comment.body, '@claude')",
			"`issue_comment` is raised for an issue and for a pull request: only a comment on a pull request " +
				"starts an agent, since nothing turns the branch of any other into a pull request (work item 433)",
		},
		{
			"              \"env\": {\n                \"BASH_DEFAULT_TIMEOUT_MS\": \"900000\",\n                \"BASH_MAX_TIMEOUT_MS\": \"1800000\",\n" +
				"                \"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS\": \"1\"\n              },\n",
			"`make test` outlasts the Bash tool's two minutes on the runner, and a command moved to the background is one the session may not wait for",
		},
	} {
		if !strings.Contains(text, want.line) {
			r.Errorf("%s no longer carries\n%s\n(%s)", loopHubRel, want.line, want.why)
		}
	}
	// What an issue could start, comment lines apart: the trigger, a clause on the event, and the job that
	// opened a pull request for an issue's branch.
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, gone := range []string{"issues:", "github.event_name == 'issues'", "open-pr", "gh pr create", "gh issue"} {
			if strings.Contains(line, gone) && !strings.Contains(line, "issues: write") {
				r.Errorf("%s:%d carries %q: an issue starts no agent and this workflow opens no pull request "+
					"since work item 433 removed the issue-driven loop (ADR 0032)", loopHubRel, i+1, gone)
			}
		}
	}
}

func TestTheLinesOfTheHub(t *testing.T) {
	t.Parallel()
	checkHubLines(t, testutil.RepoRoot(t))
}

var hubLineMutations = []struct {
	name    string
	mutate  mutation
	because string
}{
	{
		"the_group_holds_one_waiting_run_again",
		replaceInFile(loopHubRel, "  cancel-in-progress: false\n  queue: max\n", "  cancel-in-progress: false\n"),
		"the race of 2026-10-01: a newer waiting run cancels the older",
	},
	{
		"any_label_on_a_claude_pull_request_starts_an_agent",
		replaceInFile(loopHubRel, "github.event.action == 'labeled' && github.event.label.name == 'claude')\n",
			"contains(join(github.event.pull_request.labels.*.name, ','), 'claude'))\n"),
		"the clause as it was: four matching events for a pull request opened with three labels, and now four agents",
	},
	{
		"the_hub_listens_for_opened_pull_requests_again",
		replaceInFile(loopHubRel, "  pull_request:\n    types: [labeled]\n", "  pull_request:\n    types: [opened, labeled]\n"),
		"an `opened` run can only be skipped now, and a skipped run still takes a turn in the group",
	},
	{
		"the_test_run_gets_two_minutes_again",
		replaceInFile(loopHubRel, "                \"BASH_DEFAULT_TIMEOUT_MS\": \"900000\",\n", ""),
		"two minutes is less than `make test` takes on the runner",
	},
	{
		"a_command_may_move_to_the_background",
		replaceInFile(loopHubRel, "\"BASH_MAX_TIMEOUT_MS\": \"1800000\",\n                \"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS\": \"1\"\n", "\"BASH_MAX_TIMEOUT_MS\": \"1800000\"\n"),
		"a command in the background is one the session may end without",
	},
	{
		"a_comment_on_an_issue_starts_an_agent_again",
		replaceInFile(loopHubRel, "github.event_name == 'issue_comment' && github.event.issue.pull_request && ", "github.event_name == 'issue_comment' && "),
		"the last door of the issue-driven loop: an agent whose branch nothing turns into a pull request",
	},
	{
		"an_issue_labelled_claude_is_a_work_order_again",
		replaceInFile(loopHubRel, "  pull_request:\n    types: [labeled]\n", "  issues:\n    types: [labeled]\n  pull_request:\n    types: [labeled]\n"),
		"the trigger work item 433 removed",
	},
	{
		"the_job_matches_an_issue_event",
		replaceInFile(loopHubRel, "        (github.event_name == 'pull_request' && github.event.action == 'labeled'",
			"        (github.event_name == 'issues' && github.event.label.name == 'claude') ||\n        (github.event_name == 'pull_request' && github.event.action == 'labeled'"),
		"the clause work item 433 removed",
	},
}

func TestHubLinesPassOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	var rec recorder
	checkHubLines(&rec, copyWorkflows(t))
	if len(rec.msgs) != 0 {
		t.Errorf("the check reported %d problem(s) on an unmutated copy: %s", len(rec.msgs), strings.Join(rec.msgs, "; "))
	}
}

func TestHubLinesFailOnAMutatedCopy(t *testing.T) {
	t.Parallel()
	for _, m := range hubLineMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			root := copyWorkflows(t)
			m.mutate(t, root)
			var rec recorder
			checkHubLines(&rec, root)
			if len(rec.msgs) == 0 {
				t.Fatalf("the mutation changed nothing the check can see, so the check is vacuous (%s)", m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}
