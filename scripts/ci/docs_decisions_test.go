//go:build !windows

// The half of the documentation gate's `decisions` check that reads git (ADR 0001, work item 309): an ADR that
// has landed keeps its text. docs_check_test.go plants its violations in a plain copy of the tree, which has no
// history; this file makes the copy a repository, commits a planted ADR in it, and holds every kind of edit
// against the gate: the ones it must refuse, and the ones it must let through, which are the controls.
package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/docscheck"
)

// The planted ADR of these tests, and the texts the cases are built from.
const (
	plantedName      = "9999-a-planted-decision.md"
	plantedRel       = docscheck.DecisionsDir + "/" + plantedName
	plantedFact      = "counted 4 planted"
	plantedDecision  = "We plant one ADR."
	plantedCorrected = "- **Corrected:** 2026-11-14: the count was given as 4; it is 3\n"
)

// docsGit runs git in the copy with an environment built from scratch (baseEnv): its own identity, a home of its
// own, no signing and no hooks, and nothing inherited that could point git at another repository.
func docsGit(t *testing.T, root, home string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", root, "-c", "user.name=Gate Test", "-c", "user.email=gate@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-c", "gc.auto=0"}, args...)
	cmd := exec.CommandContext(t.Context(), "git", argv...) //nolint:gosec // G204: fixed words and a path under t.TempDir()
	cmd.Env = baseEnv(t, home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// decisionsRepo is a copy of the tree that is a repository: the decisions as the tree has them in its first
// commit, and the planted ADR, listed in the index, in its second. It returns the two commits.
func decisionsRepo(t *testing.T) (root, home, before, planted string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is needed to prove the rule that reads it: %v", err)
	}
	root, home = copyDocsTree(t), t.TempDir()
	docsGit(t, root, home, "init", "-q", "-b", "master")
	docsGit(t, root, home, "add", "--", docscheck.DecisionsDir)
	docsGit(t, root, home, "commit", "-q", "-m", "the decisions")
	before = docsGit(t, root, home, "rev-parse", "HEAD")
	plantADR(t, root, plantedName, plantedADR("9999", "Accepted"))
	docsGit(t, root, home, "add", "--", docscheck.DecisionsDir)
	docsGit(t, root, home, "commit", "-q", "-m", "plant a decision")
	planted = docsGit(t, root, home, "rev-parse", "HEAD")
	return root, home, before, planted
}

// decisionFindings runs the gate on the copy and returns what the `decisions` check found.
func decisionFindings(t *testing.T, root string) []string {
	t.Helper()
	findings, _, err := docscheck.Run(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range findings {
		if f.Check == "decisions" {
			out = append(out, f.String())
		}
	}
	return out
}

// checkDecisionFinding reports a case whose findings are not what it wants: none, or one that says want.
func checkDecisionFinding(t *testing.T, name string, got []string, want string) {
	t.Helper()
	if want == "" {
		if len(got) != 0 {
			t.Errorf("%s: the gate refused a change it must allow: %v", name, got)
		} else {
			t.Logf("%s: allowed", name)
		}
		return
	}
	for _, f := range got {
		if strings.Contains(f, want) {
			t.Logf("%s: caught: %s", name, f)
			return
		}
	}
	t.Errorf("%s: the gate reported %v; want a finding that says %q", name, got, want)
}

// mustChange is strings.Replace that fails when it replaced nothing: a case that changes nothing proves nothing.
func mustChange(t *testing.T, text, old, replacement string) string {
	t.Helper()
	out := strings.Replace(text, old, replacement, 1)
	if out == text {
		t.Fatalf("%q is not in the text, so the case would change nothing", old)
	}
	return out
}

// TestDocsGateHoldsACommittedADR: in a repository with no remote branch, what is committed has landed. The gate
// lets a landed ADR's Status line change, a fact be corrected under a dated Corrected entry and a link be
// repointed, and refuses every other change to it while that change is still in the working tree.
func TestDocsGateHoldsACommittedADR(t *testing.T) {
	t.Parallel()
	root, home, _, _ := decisionsRepo(t)
	committed := docsRead(t, root, plantedRel)
	index := docsRead(t, root, docscheck.DecisionsIndex)
	if got := decisionFindings(t, root); len(got) != 0 {
		t.Fatalf("the gate found fault with the ADRs as committed: %v", got)
	}
	// The same repository through a link: the directory is the one git names, whatever the path is spelt.
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	checkDecisionFinding(t, "the tree reached through a link", decisionFindings(t, link), "")

	corrected := mustChange(t, mustChange(t, committed, plantedFact, "counted 3 planted"),
		"- **Supersedes:** none\n", "- **Supersedes:** none\n"+plantedCorrected)
	cases := []struct {
		name string
		text string // the ADR's new text; "" removes the file
		want string // part of the finding the gate must report; "" when it must report none
	}{
		{"the Status line alone", mustChange(t, committed, "- **Status:** Accepted", "- **Status:** Rejected"), ""},
		{"a fact corrected under a Corrected entry", corrected, ""},
		{"a link in the Decision repointed", mustChange(t, committed, "(README.md)", "(0000-template.md)"), ""},
		{"a titled link repointed", mustChange(t, committed, "(0000-template.md \"the template\")", "(README.md \"the template\")"), ""},
		{"a reference link repointed", mustChange(t, committed, "[idx]: README.md", "[idx]: 0000-template.md"), ""},
		{"what looks like a link inside inline code", mustChange(t, committed, "`[words](target.md)`", "`[words](other.md)`"), "beyond the Status line"},
		{"a titled link's title", mustChange(t, committed, "\"the template\")", "\"a template\")"), "beyond the Status line"},
		{"a fact changed with no Corrected entry", mustChange(t, committed, plantedFact, "counted 3 planted"), "beyond the Status line"},
		{"a section added", committed + "\n## Afterthought\n\nOne more thing.\n", "beyond the Status line"},
		{"the Decision section", mustChange(t, committed, plantedDecision, "We plant two ADRs."), "Decision section changed"},
		{"the Decision section, even with a Corrected entry", mustChange(t, corrected, plantedDecision, "We plant two ADRs."), "Decision section changed"},
		{"a link's words in the Decision", mustChange(t, committed, "[The index]", "[The list]"), "Decision section changed"},
		{"the title", mustChange(t, committed, "Plant a decision for", "Plant a ruling for"), "title changed"},
		{"another header line", mustChange(t, committed, "- **Date:** 2026-11-13", "- **Date:** 2026-11-14"), "header line other than Status"},
		{"the file removed", "", "gone from the tree"},
	}
	// The cases share the one repository, so they run one after another, each undone before the next.
	for _, tc := range cases {
		if tc.text == "" {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(plantedRel))); err != nil {
				t.Fatal(err)
			}
		} else {
			docsWrite(t, root, plantedRel, tc.text)
		}
		checkDecisionFinding(t, tc.name, decisionFindings(t, root), tc.want)
		docsWrite(t, root, plantedRel, committed)
	}

	// A renamed ADR is a removed one and a new one: the old name is gone from the tree.
	const renamed = "9999-a-renamed-decision.md"
	from, to := filepath.Join(root, filepath.FromSlash(plantedRel)), filepath.Join(root, filepath.FromSlash(docscheck.DecisionsDir+"/"+renamed))
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	docsWrite(t, root, docscheck.DecisionsIndex, mustChange(t, index, plantedName, renamed))
	checkDecisionFinding(t, "the file renamed", decisionFindings(t, root), "gone from the tree")
	if err := os.Remove(to); err != nil {
		t.Fatal(err)
	}
	docsWrite(t, root, plantedRel, committed)
	docsWrite(t, root, docscheck.DecisionsIndex, index)

	// A second correction is held against the first: commit the corrected ADR, then change it again.
	docsWrite(t, root, plantedRel, corrected)
	docsGit(t, root, home, "commit", "-q", "-am", "correct the count")
	if got := decisionFindings(t, root); len(got) != 0 {
		t.Fatalf("the gate found fault with the corrected ADR as committed: %v", got)
	}
	again := mustChange(t, corrected, "Lost: the rule would be unproven.", "Lost: the rule would be untested.")
	second := mustChange(t, again, plantedCorrected, plantedCorrected+"- **Corrected:** 2026-11-15: the alternative said unproven; it says untested\n")
	later := []struct {
		name string
		text string
		want string
	}{
		{"a second change with no entry of its own", again, "beyond the Status line"},
		{"a second change with its own entry", second, ""},
		{"the first entry rewritten", mustChange(t, second, "the count was given as 4", "the count was given as 5"), "changed or removed"},
		{"the first entry removed", mustChange(t, second, plantedCorrected, ""), "changed or removed"},
	}
	for _, tc := range later {
		docsWrite(t, root, plantedRel, tc.text)
		checkDecisionFinding(t, tc.name, decisionFindings(t, root), tc.want)
		docsWrite(t, root, plantedRel, corrected)
	}
}

// TestDocsGateHoldsWhatLanded: with a remote default branch, an ADR is held against what that branch has. One
// that a branch added and the default branch has not can be revised, commit after commit, as a pull request's
// is; one that landed is held even after the edit to it is committed, which is how a pull request's CI and a
// session with commits of its own see it; and putting the landed text back satisfies the gate again.
func TestDocsGateHoldsWhatLanded(t *testing.T) {
	t.Parallel()
	root, home, before, planted := decisionsRepo(t)
	committed := docsRead(t, root, plantedRel)
	index := docsRead(t, root, docscheck.DecisionsIndex)
	commit := func(message string) {
		t.Helper()
		docsGit(t, root, home, "add", "-A", "--", docscheck.DecisionsDir)
		docsGit(t, root, home, "commit", "-q", "-m", message)
	}
	landAt := func(commit string) {
		t.Helper()
		docsGit(t, root, home, "update-ref", "refs/remotes/origin/master", commit)
	}
	revised := mustChange(t, committed, plantedDecision, "We plant two ADRs.")

	// 1. The default branch does not have the ADR yet: the branch may revise it.
	landAt(before)
	docsWrite(t, root, plantedRel, revised)
	checkDecisionFinding(t, "a branch's own ADR, its Decision revised in the working tree", decisionFindings(t, root), "")
	commit("revise the decision")
	checkDecisionFinding(t, "a branch's own ADR, its Decision revised in a second commit", decisionFindings(t, root), "")

	// 2. The default branch has the ADR as it was added: the same revision, committed, is now an edit to a
	// landed ADR, though the working tree is clean.
	landAt(planted)
	checkDecisionFinding(t, "a landed ADR whose Decision a commit changed", decisionFindings(t, root), "Decision section changed")
	docsWrite(t, root, plantedRel, committed)
	commit("put the decision back")
	checkDecisionFinding(t, "the landed text put back", decisionFindings(t, root), "")

	// 3. A landed ADR removed by a commit, with the index following it.
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(plantedRel))); err != nil {
		t.Fatal(err)
	}
	docsWrite(t, root, docscheck.DecisionsIndex, mustChange(t, index, plantedRow(plantedName), ""))
	commit("remove the decision")
	checkDecisionFinding(t, "a landed ADR a commit removed", decisionFindings(t, root), "gone from the tree")
	docsWrite(t, root, plantedRel, committed)
	docsWrite(t, root, docscheck.DecisionsIndex, index)
	commit("put the decision's file back")
	checkDecisionFinding(t, "the removed ADR put back", decisionFindings(t, root), "")

	// 4. A correction lands; a later commit changes the ADR again with no entry of its own.
	corrected := mustChange(t, mustChange(t, committed, plantedFact, "counted 3 planted"),
		"- **Supersedes:** none\n", "- **Supersedes:** none\n"+plantedCorrected)
	docsWrite(t, root, plantedRel, corrected)
	commit("correct the count")
	landAt(docsGit(t, root, home, "rev-parse", "HEAD"))
	checkDecisionFinding(t, "a correction, landed", decisionFindings(t, root), "")
	docsWrite(t, root, plantedRel, mustChange(t, corrected, "Lost: the rule would be unproven.", "Lost: the rule would be untested."))
	commit("change it again")
	checkDecisionFinding(t, "a landed, corrected ADR a commit changed with no new entry", decisionFindings(t, root), "beyond the Status line")
}

// TestDocsGateLetsANumberClashBeRepaired: two ADRs that landed with one number are the one case in which a
// landed ADR is renamed. The gate refuses the clash, lets one of the two take a number of its own with nothing
// else changed, and still refuses a rename that changes anything else or that no clash calls for.
func TestDocsGateLetsANumberClashBeRepaired(t *testing.T) {
	t.Parallel()
	root, home, _, _ := decisionsRepo(t)
	const twin = "9999-a-second-planted-decision.md"
	twinRel := docscheck.DecisionsDir + "/" + twin
	twinText := mustChange(t, plantedADR("9999", "Accepted"), "Plant a decision for", "Plant a second decision for")
	plantADR(t, root, twin, twinText)
	docsGit(t, root, home, "add", "-A", "--", docscheck.DecisionsDir)
	docsGit(t, root, home, "commit", "-q", "-m", "a second decision under the same number")
	index := docsRead(t, root, docscheck.DecisionsIndex)
	checkDecisionFinding(t, "two landed ADRs with one number", decisionFindings(t, root), "reuses the number 9999")

	move := func(name, text string) {
		t.Helper()
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(twinRel))); err != nil {
			t.Fatal(err)
		}
		docsWrite(t, root, docscheck.DecisionsDir+"/"+name, text)
		docsWrite(t, root, docscheck.DecisionsIndex, mustChange(t, index, plantedRow(twin), plantedRow(name)))
	}
	undo := func(name string) {
		t.Helper()
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(docscheck.DecisionsDir+"/"+name))); err != nil {
			t.Fatal(err)
		}
		docsWrite(t, root, twinRel, twinText)
		docsWrite(t, root, docscheck.DecisionsIndex, index)
	}
	const renumbered = "9998-a-second-planted-decision.md"
	move(renumbered, mustChange(t, twinText, "# 9999. ", "# 9998. "))
	checkDecisionFinding(t, "one of the two given a number of its own", decisionFindings(t, root), "")
	undo(renumbered)
	move(renumbered, mustChange(t, mustChange(t, twinText, "# 9999. ", "# 9998. "), plantedDecision, "We plant two ADRs."))
	checkDecisionFinding(t, "renumbered, and its Decision changed on the way", decisionFindings(t, root), "gone from the tree")
	undo(renumbered)

	// With the clash repaired and landed, there is no clash left to excuse a rename.
	move(renumbered, mustChange(t, twinText, "# 9999. ", "# 9998. "))
	docsGit(t, root, home, "add", "-A", "--", docscheck.DecisionsDir)
	docsGit(t, root, home, "commit", "-q", "-m", "give the second decision its own number")
	checkDecisionFinding(t, "the repair, landed", decisionFindings(t, root), "")
	const again = "9997-a-second-planted-decision.md"
	renumberedText := docsRead(t, root, docscheck.DecisionsDir+"/"+renumbered)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(docscheck.DecisionsDir+"/"+renumbered))); err != nil {
		t.Fatal(err)
	}
	docsWrite(t, root, docscheck.DecisionsDir+"/"+again, mustChange(t, renumberedText, "# 9998. ", "# 9997. "))
	docsWrite(t, root, docscheck.DecisionsIndex, mustChange(t, docsRead(t, root, docscheck.DecisionsIndex), plantedRow(renumbered), plantedRow(again)))
	checkDecisionFinding(t, "renumbered with no clash to repair", decisionFindings(t, root), "gone from the tree")
}

// TestDocsGateTakesAnADRsOwnWords: what looks like a heading or a Corrected entry inside a fenced block, and a
// sentence that starts with the word, are an ADR's own words and no fault.
func TestDocsGateTakesAnADRsOwnWords(t *testing.T) {
	t.Parallel()
	root := copyDocsTree(t)
	text := plantedADR("9999", "Accepted") +
		"- Corrected counts were taken again on 2026-11-20.\n\n" +
		"```markdown\n## Decision\n\n- **Corrected:** YYYY-MM-DD: <what was wrong>\n```\n"
	plantADR(t, root, plantedName, text)
	checkDecisionFinding(t, "a fenced example and a sentence that starts with Corrected", decisionFindings(t, root), "")
}

// TestDocsGateSkipsHistoryInAPlainTree: a copy with no `.git` is no repository, and the gate says nothing about
// history there: the tree's own rules still run.
func TestDocsGateSkipsHistoryInAPlainTree(t *testing.T) {
	t.Parallel()
	root := copyDocsTree(t)
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		t.Fatal("the copy has a .git, so it is not the plain tree this test is about")
	}
	if got := decisionFindings(t, root); len(got) != 0 {
		t.Fatalf("the gate found fault with a plain copy: %v", got)
	}
}
