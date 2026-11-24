//go:build !windows

// The reviewer agent's login in the workflows that act on a pull request (work item 397, 2026-11-22).
//
// auto-merge.yml merges a labelled pull request on an approving review of the reviewer agent, claude.yml arms
// the fixer on its reviews and counts them against the fix cap, and review-pull-request.yml takes its review
// as the job's verdict. Each tells the agent's review from anyone else's by the review's login, and until this
// work item each did so with a prefix test, `startsWith(…, 'claude')` in an `if:` and `startswith("claude")`
// in a jq filter: thirteen places that an account registered under any login beginning with `claude` would
// have satisfied. The agent's login is `claude[bot]`, the Claude GitHub App, and no person can register one
// that ends in `[bot]`.
//
// checkLoopLogins reads every workflow, comment lines apart, and holds three things:
//   - a login is compared whole and on one line, `<…>.login == 'claude[bot]'` or `!=`;
//   - each workflow carries the `==` and the `!=` comparisons the table names, so a clause cannot be dropped or
//     turned round quietly (an auto-merge.yml without its login clause arms a merge on anyone's approval, and
//     one with `!=` on everyone's but the agent's);
//   - every other read of a login or of the actor, however it is spelled, is a line the second table names:
//     a read that matches nothing, such as a user name handed to a command.
//
// Like the other gates of this package the check runs twice: on the repository, where a report is a failure,
// and on mutated copies, where silence is the bug.
package ci_test

import (
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	workflowsDirRel = ".github/workflows"
	// loopReviewerLogin is the login the reviewer agent's `gh pr review` runs as: measured on 2026-11-22 over
	// every review of this repository's pull requests, 79 of them the agent's, all `claude[bot]`, type Bot.
	loopReviewerLogin = "claude[bot]"
)

// loopLoginCount is how often a workflow compares a login with loopReviewerLogin, by operator.
type loopLoginCount struct{ is, isNot int }

// loopLoginComparisons names each workflow's comparisons. A workflow the table does not name makes none. A new
// comparison is added here on purpose, with its reason.
var loopLoginComparisons = map[string]loopLoginCount{
	// the job's `if:`: whose approval arms the merge
	"auto-merge.yml": {is: 1},
	// is: the fix gate's `if:`, the jq filter that counts the fix cap, and the two verifiers;
	// isNot: three clauses of the agent job's `if:` and the four steps the gate holds back
	"claude.yml": {is: 4, isNot: 7},
	// the jq filter of "Verify a review was submitted"
	"review-pull-request.yml": {is: 1},
}

// loopOtherIdentityReads names, by text the line contains, every line of a workflow that reads a login or the
// actor without comparing a login whole: a read that matches no part of it. An entry no line carries is stale.
var loopOtherIdentityReads = map[string][]string{
	// the registry login of the two image jobs: the actor as a user name, compared with nothing
	"server-release.yml": {`docker login ghcr.io --username "${{ github.actor }}"`},
}

var (
	// loopIdentityRef finds a login or the actor wherever a workflow reads one, however it is spelled:
	// `github.event.review.user.login` in an expression and `.user.login` in a jq filter, the indexed forms
	// `user['login']` and `.user["login"]`, `github.actor` and `github.triggering_actor`, and the runner's
	// `GITHUB_ACTOR` and `GITHUB_TRIGGERING_ACTOR`. `docker login` has no dot before it and is not one.
	loopIdentityRef = regexp.MustCompile(
		`(?i)\.login\b|\[\\?['"]login\\?['"]\]|\bgithub\.(?:triggering_)?actor\b|\bGITHUB_(?:TRIGGERING_)?ACTOR\b`)
	// loopWholeComparison is what follows a `.login` that is compared whole: `==` or `!=`, then the quoted
	// login. The quotes are escaped where the jq filter sits inside a double-quoted shell string.
	loopWholeComparison = regexp.MustCompile(`^\s*([=!])=\s*\\?['"]` + regexp.QuoteMeta(loopReviewerLogin) + `\\?['"]`)
)

// checkLoopLogins reports every line of a workflow under root that reads a login or the actor other than in a
// whole comparison with loopReviewerLogin or on a line loopOtherIdentityReads names, every workflow whose
// comparisons are not the table's, and every entry of either table that names nothing.
func checkLoopLogins(r reporter, root string) {
	r.Helper()
	entries, err := os.ReadDir(filepath.Join(root, workflowsDirRel))
	if err != nil {
		r.Errorf("reading %s: %v", workflowsDirRel, err)
		return
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		rel := path.Join(workflowsDirRel, name)
		text, ok := readText(r, root, rel)
		if !ok {
			continue
		}
		seen[name] = true
		var got loopLoginCount
		allowed := loopOtherIdentityReads[name]
		used := make([]bool, len(allowed))
		for i, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue // a comment may say what the prefix test was
			}
			for _, at := range loopIdentityRef.FindAllStringIndex(line, -1) {
				if strings.EqualFold(line[at[0]:at[1]], ".login") {
					if m := loopWholeComparison.FindStringSubmatch(line[at[1]:]); m != nil {
						if m[1] == "=" {
							got.is++
						} else {
							got.isNot++
						}
						continue
					}
				}
				named := false
				for j, want := range allowed {
					if strings.Contains(line, want) {
						named, used[j] = true, true
					}
				}
				if !named {
					r.Errorf("%s:%d reads a login or the actor (%s) without comparing a login whole with %q on "+
						"that line (`<…>.login == '%s'`, or `!=`): a prefix or any other part of a login is one "+
						"that anyone can register (work item 397). A read that matches nothing, such as a log "+
						"line or a user name handed to a command, is named in loopOtherIdentityReads",
						rel, i+1, line[at[0]:at[1]], loopReviewerLogin, loopReviewerLogin)
				}
			}
		}
		if want := loopLoginComparisons[name]; got != want {
			r.Errorf("%s compares a login with %q %d time(s) with `==` and %d with `!=`, and "+
				"loopLoginComparisons names %d and %d: a clause that was dropped or turned round lets other "+
				"logins through, and a new one is added to the table with its reason",
				rel, loopReviewerLogin, got.is, got.isNot, want.is, want.isNot)
		}
		for j, want := range allowed {
			if !used[j] {
				r.Errorf("loopOtherIdentityReads names a line of %s that carries %q, and no line does: drop "+
					"the entry", rel, want)
			}
		}
	}
	named := slices.Collect(maps.Keys(loopLoginComparisons))
	named = append(named, slices.Collect(maps.Keys(loopOtherIdentityReads))...)
	slices.Sort(named)
	for _, name := range slices.Compact(named) {
		if !seen[name] {
			r.Errorf("a table of loop_login_test.go names %s, which is not a workflow under %s", name, workflowsDirRel)
		}
	}
}

func TestNoWorkflowTakesALoginByItsPrefix(t *testing.T) {
	t.Parallel()
	checkLoopLogins(t, testutil.RepoRoot(t))
}

// ---------------------------------------------------------------------------------------------------------
// mutations: the check, shown failing

// copyWorkflows copies the repository's workflows into a fresh temporary root.
func copyWorkflows(t *testing.T) string {
	t.Helper()
	src := filepath.Join(testutil.RepoRoot(t), workflowsDirRel)
	root := t.TempDir()
	dst := filepath.Join(root, workflowsDirRel)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatalf("creating %s: %v", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("reading %s: %v", src, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		//nolint:gosec // G304: the source is the repository's own workflow directory.
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		//nolint:gosec // G703: the target is under t.TempDir().
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", entry.Name(), err)
		}
	}
	return root
}

const (
	loopAutoMergeRel     = workflowsDirRel + "/auto-merge.yml"
	loopHubRel           = workflowsDirRel + "/claude.yml"
	loopReviewRel        = workflowsDirRel + "/review-pull-request.yml"
	loopServerReleaseRel = workflowsDirRel + "/server-release.yml"
	loopOtherRel         = workflowsDirRel + "/keepalive.yml" // a workflow neither table names
)

var loopLoginMutations = []struct {
	name    string
	mutate  mutation
	because string
}{
	{
		"auto_merge_takes_a_prefix",
		replaceInFile(loopAutoMergeRel, "github.event.review.user.login == 'claude[bot]'",
			"startsWith(github.event.review.user.login, 'claude')"),
		"the form work item 397 removed: any login that begins with `claude` would arm the merge",
	},
	{
		"auto_merge_drops_its_login_clause",
		replaceInFile(loopAutoMergeRel, "      github.event.review.user.login == 'claude[bot]' &&\n", ""),
		"without the clause anyone's approval of a labelled pull request arms the merge",
	},
	{
		"auto_merge_turns_its_comparison_round",
		replaceInFile(loopAutoMergeRel, "user.login == 'claude[bot]'", "user.login != 'claude[bot]'"),
		"still a whole comparison, and now every approval but the agent's arms the merge",
	},
	{
		"auto_merge_names_another_login",
		replaceInFile(loopAutoMergeRel, "user.login == 'claude[bot]'", "user.login == 'claude'"),
		"`claude` is a login a person can hold; the agent's is the App's",
	},
	{
		"auto_merge_reads_the_login_by_index",
		replaceInFile(loopAutoMergeRel, "github.event.review.user.login == 'claude[bot]'",
			"startsWith(github.event.review.user['login'], 'claude')"),
		"the same prefix test with the login spelled as an index",
	},
	{
		"the_hub_excludes_comments_by_prefix",
		replaceInFile(loopHubRel, "github.event.comment.user.login != 'claude[bot]'",
			"!startsWith(github.event.comment.user.login, 'claude')"),
		"the negated prefix test of the agent job's `if:`",
	},
	{
		"the_hub_matches_a_part_of_the_login",
		replaceInFile(loopHubRel, "github.event.review.user.login == 'claude[bot]'",
			"contains(github.event.review.user.login, 'claude')"),
		"a part anywhere in the login is no better than a prefix",
	},
	{
		"the_fix_cap_counts_reviews_by_prefix",
		replaceInFile(loopHubRel, `select(.user.login == "claude[bot]" and`,
			`select((.user.login // "" | startswith("claude")) and`),
		"the jq form: a stranger's CHANGES_REQUESTED reviews would spend the fix cap",
	},
	{
		"the_reviewer_verifier_takes_a_prefix",
		replaceInFile(loopReviewRel, `select(.user.login == \"claude[bot]\" and`,
			`select((.user.login // \"\" | startswith(\"claude\")) and`),
		"the jq form inside a double-quoted shell string: a stranger's review would pass for the agent's verdict",
	},
	{
		"a_workflow_matches_the_actor_by_prefix",
		appendToFile(loopOtherRel, "\n    if: startsWith(github.actor, 'claude')\n"),
		"the actor is a login too",
	},
	{
		"a_workflow_looks_the_actor_up_in_a_list",
		appendToFile(loopOtherRel, "\n    if: contains('claude[bot],claude-x', github.actor)\n"),
		"the actor in the second argument: `contains` on a string matches a part of it",
	},
	{
		"a_step_matches_the_actor_in_its_shell",
		appendToFile(loopOtherRel, "\n          case \"$GITHUB_ACTOR\" in claude*) exit 0 ;; esac\n"),
		"the runner's variable and a shell glob are the prefix test again",
	},
	{
		"a_workflow_outside_the_table_compares_a_login",
		appendToFile(loopOtherRel, "\n    if: github.event.sender.login == 'claude[bot]'\n"),
		"a new comparison is added to the table on purpose, never met by surprise",
	},
	{
		"a_step_reads_a_login_no_table_names",
		appendToFile(loopOtherRel, "\n          gh api \"repos/$GITHUB_REPOSITORY/pulls/1/reviews\" --jq '.[].user.login'\n"),
		"a read that matches nothing is allowed by naming its line, so that each one is looked at once",
	},
	{
		"a_named_read_is_gone",
		replaceAllInFile(loopServerReleaseRel, `--username "${{ github.actor }}"`, "--username brigade"),
		"an entry of loopOtherIdentityReads that no line carries would allow the next line that does",
	},
}

// TestLoopLoginCheckPassesOnAnUnmutatedCopy is the positive control of the mutation cases: were the copy
// wrong, each of them would report for the wrong reason.
func TestLoopLoginCheckPassesOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	var rec recorder
	checkLoopLogins(&rec, copyWorkflows(t))
	if len(rec.msgs) != 0 {
		t.Errorf("the check reported %d problem(s) on an unmutated copy: %s", len(rec.msgs), strings.Join(rec.msgs, "; "))
	}
}

func TestLoopLoginCheckFailsOnAMutatedCopy(t *testing.T) {
	t.Parallel()
	for _, m := range loopLoginMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			root := copyWorkflows(t)
			m.mutate(t, root)
			var rec recorder
			checkLoopLogins(&rec, root)
			if len(rec.msgs) == 0 {
				t.Fatalf("the mutation changed nothing the check can see, so the check is vacuous (%s)", m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}
