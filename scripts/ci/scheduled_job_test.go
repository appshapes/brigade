//go:build !windows

// The scheduled jobs (work item 433, ADR 0032): scripts/ci/scheduled-job.sh and the three workflows that are
// built on it, update-documentation.yml, update-dependencies.yml and review-repository.yml.
//
// Each of the three is one workflow whose steps are the personas in turn, and the script is every step that is
// not a model: it says what a writer's checkout holds, gates the change, reads a reviewer's verdict, decides,
// looks for a token in what leaves the job, and lands the change as a pull request from a job that runs no
// agent. None of it can be tried on GitHub without a run that costs a model's session and opens a real pull
// request. So this file holds:
//   - every verb of the script, run for real: in fixture repositories with an origin beside them, with a
//     `make` that answers as it is told and a `gh` that answers from files and records every call;
//   - the three workflows: one text from the line that says so on, each job's own values above it, no token
//     that can write in the job that runs the agents, the owner's token in the job that runs none, and the
//     script read from the commit the run started on.
//
// Both halves run twice, on the repository and on mutated copies, where silence is the bug.
package ci_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	scheduledScriptRel = "scripts/ci/scheduled-job.sh"
	scheduledRepo      = "example-owner/example-repo"
	scheduledJobName   = "update-documentation"
	scheduledPullURL   = "https://github.com/" + scheduledRepo + "/pull/7"
	// scheduledHead stands for the commit the landing job pushed: the merge is asked for it and no other.
	scheduledHead = "0123456789abcdef0123456789abcdef01234567"
)

// scheduledWorkflows are the three jobs, by file name without `.yml`: the name each gives SCHEDULED_JOB.
var scheduledWorkflows = []string{"update-documentation", "update-dependencies", "review-repository"}

// ---------------------------------------------------------------------------------------------------------
// the fixture

// jobRepo is a checkout as the job `work` has it: a clone of an origin with one pushed commit on `master`,
// which is the commit the run started on (GITHUB_SHA) and carries the repository's own agent-leftover.sh and
// pr-guard.sh, because the script reads both from that commit. PATH leads with stand-ins for `pgrep` (see
// leftoverRepo), `make` and `gh`; dir is the job's directory outside the checkout.
type jobRepo struct {
	*leftoverRepo
	base   string // the fixture's root
	origin string
	dir    string // SCHEDULED_DIR
	sha    string // GITHUB_SHA
	script string // the copy of scheduled-job.sh under test
}

func newJobRepo(t *testing.T, script string) *jobRepo {
	t.Helper()
	base := t.TempDir()
	j := &jobRepo{
		leftoverRepo: &leftoverRepo{t: t, work: filepath.Join(base, "work"), shims: filepath.Join(base, "shims")},
		base:         base,
		origin:       filepath.Join(base, "origin.git"),
		dir:          filepath.Join(base, "job"),
		script:       script,
	}
	for _, dir := range []string{j.shims, j.work, j.dir, filepath.Join(base, "home"), filepath.Join(base, "tmp"), filepath.Join(base, "gh")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	j.pgrepAnswers("false")
	// `make` answers as FAKE_MAKE_FAILS tells it and records what it was asked for.
	writeExec(t, filepath.Join(j.shims, "make"), `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DIR/make.log"
case " ${FAKE_MAKE_FAILS:-} " in
  *" $1 "*) echo "make: *** [$1] Error 1 (the stand-in was told to fail)"; exit 2 ;;
esac
echo "stand-in make: $1"
`)
	// `gh` answers the two listings of `sweep` from files and records every call, one a line.
	writeExec(t, filepath.Join(j.shims, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_DIR/gh.log"
if [ -n "${FAKE_GH_FAILS:-}" ]; then echo "gh: HTTP 502" >&2; exit 1; fi
case "$1 $2" in
  "api --paginate") cat "$FAKE_DIR/refs.json"; exit 0 ;;
  "api -X") exit 0 ;;
  "pr create")
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --body-file ]; then cp "$2" "$FAKE_DIR/body.md"; fi
      shift
    done
    echo "`+scheduledPullURL+`"; exit 0 ;;
  "pr merge")
    case "$*" in *--auto*) if [ -n "${FAKE_GH_AUTO_FAILS:-}" ]; then echo "gh: Pull request is in clean status" >&2; exit 1; fi ;; esac
    exit 0 ;;
  "pr checks") exit "${FAKE_GH_CHECKS:-0}" ;;
esac
if [ "$1" = api ]; then
  branch=${2#*head=example-owner:}; branch=${branch%%&*}
  file="$FAKE_DIR/pulls-$(printf '%s' "$branch" | tr '/' '_').json"
  if [ -f "$file" ]; then cat "$file"; else echo '[]'; fi
  exit 0
fi
echo "gh: the stand-in does not know: $*" >&2; exit 1
`)
	j.env = append(baseEnv(t, filepath.Join(base, "home")),
		"GIT_COMMITTER_NAME=ci-test", "GIT_COMMITTER_EMAIL=ci-test@example.invalid",
		"RUNNER_TEMP="+filepath.Join(base, "tmp"), "FAKE_DIR="+filepath.Join(base, "gh"),
		"SCHEDULED_JOB="+scheduledJobName, "SCHEDULED_DIR="+j.dir, "GITHUB_REPOSITORY="+scheduledRepo, "GITHUB_REF=refs/heads/master",
		"SCHEDULED_GATE=docs-check test", "SCHEDULED_MAKE_VARS=go_test_p=4 go_test_timeout=5m")
	for i, kv := range j.env {
		if strings.HasPrefix(kv, "PATH=") {
			j.env[i] = "PATH=" + j.shims + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
	}
	j.gitIn(base, "init", "--quiet", "--bare", "--initial-branch=master", j.origin)
	j.git("init", "--quiet", "--initial-branch=master")
	root := testutil.RepoRoot(t)
	for _, rel := range []string{"scripts/ci/agent-leftover.sh", "scripts/ci/pr-guard.sh"} {
		//nolint:gosec // G304: a script of the repository under test.
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		j.write(filepath.Join(j.work, rel), string(data))
	}
	j.file(".gitignore", "/bin/\n")
	j.file("README.md", "a page\n")
	j.file("go.mod", "module example.invalid/fixture\n\ngo 1.27.0\n")
	j.file("plugin/bin/VERSION", "0.60.0\n")
	j.git("add", ".")
	j.git("commit", "--quiet", "-m", "first")
	j.git("remote", "add", "origin", j.origin)
	j.git("push", "--quiet", "--set-upstream", "origin", "master")
	j.sha = j.rev(j.work, "HEAD")
	j.env = append(j.env, "GITHUB_SHA="+j.sha)
	return j
}

// file writes a file of the checkout, by its path in it.
func (j *jobRepo) file(rel, text string) {
	j.t.Helper()
	j.write(filepath.Join(j.work, filepath.FromSlash(rel)), text)
}

// commit writes the files, pairs of a path and its text, and commits exactly them.
func (j *jobRepo) commit(message string, files ...string) {
	j.t.Helper()
	for i := 0; i+1 < len(files); i += 2 {
		j.file(files[i], files[i+1])
		j.git("add", files[i])
	}
	j.git("commit", "--quiet", "-m", message)
}

func (j *jobRepo) rev(dir, name string) string {
	j.t.Helper()
	//nolint:gosec // G204: git with a fixed argument list, in a fixture directory.
	cmd := exec.CommandContext(j.t.Context(), "git", "rev-parse", "--verify", "--quiet", name)
	cmd.Dir = dir
	cmd.Env = j.env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// jobRun is what one run of a verb gave: its output and what it wrote to $GITHUB_OUTPUT.
type jobRun struct {
	result
	outputs map[string]string
}

// run runs a verb in the checkout; runIn runs it in another directory, the landing job's own checkout.
func (j *jobRepo) run(verb string, extra ...string) jobRun { return j.runIn(j.work, verb, extra...) }

func (j *jobRepo) runIn(dir, verb string, extra ...string) jobRun {
	j.t.Helper()
	out := filepath.Join(j.t.TempDir(), "github-output")
	env := append(append([]string{}, j.env...), "GITHUB_OUTPUT="+out)
	run := jobRun{result: runScriptAt(j.t, j.script, dir, append(env, extra...), verb), outputs: map[string]string{}}
	//nolint:gosec // G304: out is under t.TempDir().
	data, err := os.ReadFile(out)
	if err != nil {
		return run
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			run.outputs[name] = value
		}
	}
	return run
}

// read is a file of the job's directory or of the stand-ins' own, empty when it is not there.
func (j *jobRepo) read(path string) string {
	j.t.Helper()
	//nolint:gosec // G304: a path of this test's own temp directory.
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func (j *jobRepo) gate() string    { return j.read(filepath.Join(j.dir, "gate.txt")) }
func (j *jobRepo) makeLog() string { return j.read(filepath.Join(j.base, "gh", "make.log")) }
func (j *jobRepo) ghLog() string   { return j.read(filepath.Join(j.base, "gh", "gh.log")) }

// landing is the landing job's checkout: a second clone of the origin, at the commit the run started on.
func (j *jobRepo) landing() string {
	j.t.Helper()
	dir := filepath.Join(j.base, "landing")
	j.gitIn(j.base, "clone", "--quiet", j.origin, dir)
	return dir
}

// tokenShaped is a string of a token's shape, put together here so that no file of the repository carries
// one: the scan this file tests reads every line of a scheduled job's commits.
func tokenShaped(kind string) string {
	switch kind {
	case "model":
		return "sk-" + "ant-" + strings.Repeat("a1B2", 8)
	default:
		return "gh" + "p_" + strings.Repeat("A1b2", 9)
	}
}

// ---------------------------------------------------------------------------------------------------------
// the cases

// problems collects what a case found wrong.
type problems []string

func (p *problems) want(ok bool, format string, args ...any) {
	if !ok {
		*p = append(*p, fmt.Sprintf(format, args...))
	}
}

// scheduledCases are what each verb must do, one fixture a case. A case returns what it found wrong.
var scheduledCases = []struct {
	name string
	run  func(j *jobRepo) problems
}{
	// ---- gate ----
	{"gate/an untouched checkout is nothing to change", func(j *jobRepo) (p problems) {
		r := j.run("gate")
		p.want(r.code == 0, "exit %d, want 0: %s", r.code, r.all())
		p.want(r.outputs["changed"] == "false", "changed=%q, want false", r.outputs["changed"])
		p.want(strings.Contains(j.gate(), "nothing to change"), "gate.txt does not say nothing to change: %s", j.gate())
		p.want(j.makeLog() == "", "make ran with nothing to gate: %s", j.makeLog())
		return p
	}},
	{"gate/a commit is gated by every target", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		r := j.run("gate")
		p.want(r.code == 0, "exit %d, want 0: %s", r.code, r.all())
		p.want(r.outputs["changed"] == "true", "changed=%q, want true", r.outputs["changed"])
		p.want(r.outputs["head"] == j.rev(j.work, "HEAD"), "head=%q, want the checkout's", r.outputs["head"])
		p.want(r.outputs["state"] != "", "no state was written for the verdict to compare")
		p.want(j.makeLog() == "docs-check go_test_p=4 go_test_timeout=5m\ntest go_test_p=4 go_test_timeout=5m\n", "make was asked for: %q", j.makeLog())
		p.want(strings.Contains(j.gate(), "ok: make docs-check") && strings.Contains(j.gate(), "ok: make test"), "gate.txt: %s", j.gate())
		p.want(!strings.Contains(j.gate(), "FAIL:"), "a finding on a clean change: %s", j.gate())
		return p
	}},
	{"gate/work that is not committed is a finding", func(j *jobRepo) (p problems) {
		j.file("README.md", "a page, edited and never committed\n")
		r := j.run("gate")
		p.want(r.code == 1, "exit %d, want 1: %s", r.code, r.all())
		p.want(r.outputs["changed"] == "true", "changed=%q, want true: an edit is not nothing", r.outputs["changed"])
		p.want(strings.Contains(j.gate(), "FAIL: the checkout holds work that is not committed"), "gate.txt: %s", j.gate())
		p.want(strings.Contains(j.gate(), "README.md"), "gate.txt does not name the path: %s", j.gate())
		return p
	}},
	{"gate/a stash is a finding", func(j *jobRepo) (p problems) {
		j.file("README.md", "a page, edited\n")
		j.git("stash", "push", "--quiet")
		r := j.run("gate")
		p.want(r.code == 1 && strings.Contains(j.gate(), "1 stash(es)"), "exit %d, gate.txt: %s", r.code, j.gate())
		return p
	}},
	{"gate/an untracked file beside a commit is a finding, and the gate still runs", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.file("docs/forgotten.md", "never added\n")
		r := j.run("gate")
		p.want(r.code == 1 && strings.Contains(j.gate(), "FAIL: the checkout holds work that is not committed"), "exit %d, gate.txt: %s", r.code, j.gate())
		p.want(strings.Contains(j.makeLog(), "docs-check"), "make did not run: %q", j.makeLog())
		return p
	}},
	{"gate/a commit on another branch is a finding", func(j *jobRepo) (p problems) {
		j.git("checkout", "--quiet", "-b", "aside")
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.git("checkout", "--quiet", "master")
		r := j.run("gate")
		p.want(r.code == 1 && strings.Contains(j.gate(), "a local branch that HEAD does not carry"), "exit %d, gate.txt: %s", r.code, j.gate())
		return p
	}},
	{"gate/a rewritten history is a finding", func(j *jobRepo) (p problems) {
		j.git("commit", "--quiet", "--amend", "-m", "first, said again")
		r := j.run("gate")
		p.want(r.code == 1 && strings.Contains(j.gate(), "no longer descends"), "exit %d, gate.txt: %s", r.code, j.gate())
		return p
	}},
	{"gate/a release pin is held by the guard of the starting commit", func(j *jobRepo) (p problems) {
		// The writer bumps the pin and rewrites the guard in its own commit: the guard that runs is master's.
		j.commit("5: Bump", "plugin/bin/VERSION", "9.9.9\n", "scripts/ci/pr-guard.sh", "#!/bin/sh\nexit 0\n")
		r := j.run("gate")
		p.want(r.code == 1 && strings.Contains(j.gate(), "FAIL: This PR changes a release pin"), "exit %d, gate.txt: %s", r.code, j.gate())
		return p
	}},
	{"gate/a workflow file is a path no scheduled job changes", func(j *jobRepo) (p problems) {
		j.commit("5: Move a pin", ".github/workflows/ci.yml", "name: ci\n")
		r := j.run("gate")
		p.want(r.code == 1 && strings.Contains(j.gate(), "a path no scheduled job changes") && strings.Contains(j.gate(), ".github/workflows/ci.yml"), "exit %d, gate.txt: %s", r.code, j.gate())
		return p
	}},
	{"gate/a failing target is a finding, and the others still run", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		r := j.run("gate", "FAKE_MAKE_FAILS=docs-check")
		p.want(r.code == 1, "exit %d, want 1", r.code)
		p.want(strings.Contains(j.gate(), "FAIL: make docs-check failed (exit 2)"), "gate.txt: %s", j.gate())
		p.want(strings.Contains(j.gate(), "the stand-in was told to fail"), "gate.txt does not carry the target's output: %s", j.gate())
		p.want(strings.Contains(j.gate(), "ok: make test"), "the target after the failing one did not run: %s", j.gate())
		return p
	}},
	{"gate/a target that is no target's name is refused", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		r := j.run("gate", "SCHEDULED_GATE=test;id")
		p.want(r.code == 2 && j.makeLog() == "", "exit %d, make log %q", r.code, j.makeLog())
		return p
	}},
	{"gate/a gate that is ended among its targets has said what the checkout was", func(j *jobRepo) (p problems) {
		// The stand-in for a gate ended at its limit: a target the script stops at, after the outputs.
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		r := j.run("gate", "SCHEDULED_GATE=docs-check test;id")
		p.want(r.code == 2, "exit %d, want 2", r.code)
		p.want(r.outputs["changed"] == "true" && r.outputs["head"] == j.rev(j.work, "HEAD") && r.outputs["state"] != "",
			"the outputs written before the targets: %v", r.outputs)
		return p
	}},
	{"gate/a review left by an earlier session is no verdict", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.write(filepath.Join(j.dir, "review.md"), "APPROVED\n")
		g := j.run("gate")
		r := j.run("verdict", "SCHEDULED_ROUND=2", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.outputs["verdict"] == "CHANGES_REQUESTED", "the writer's own review.md was read as the reviewer's: %v", r.outputs)
		return p
	}},

	// ---- verdict ----
	{"verdict/an approval after a clean gate needs no fix", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		g := j.run("gate")
		j.write(filepath.Join(j.dir, "review.md"), "APPROVED\n")
		r := j.run("verdict", "SCHEDULED_ROUND=1", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.code == 0 && r.outputs["verdict"] == "APPROVED" && r.outputs["fix"] == "false", "exit %d, outputs %v: %s", r.code, r.outputs, r.all())
		return p
	}},
	{"verdict/a request for changes starts the fix cycle and keeps round 1", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		g := j.run("gate")
		j.write(filepath.Join(j.dir, "review.md"), "CHANGES_REQUESTED\nREADME.md: says too much\n")
		r := j.run("verdict", "SCHEDULED_ROUND=1", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.code == 0 && r.outputs["verdict"] == "CHANGES_REQUESTED" && r.outputs["fix"] == "true", "exit %d, outputs %v", r.code, r.outputs)
		p.want(strings.Contains(j.read(filepath.Join(j.dir, "review-round1.md")), "says too much"), "round 1's review was not kept")
		p.want(j.read(filepath.Join(j.dir, "gate-round1.txt")) != "", "round 1's gate was not kept")
		p.want(j.read(filepath.Join(j.dir, "review.md")) == "", "review.md is still there: a second reviewer who wrote nothing would have approved")
		return p
	}},
	{"verdict/an approval over a failed gate still starts the fix cycle", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		g := j.run("gate", "FAKE_MAKE_FAILS=test")
		j.write(filepath.Join(j.dir, "review.md"), "APPROVED\n")
		r := j.run("verdict", "SCHEDULED_ROUND=1", "SCHEDULED_GATE_OUTCOME=failure", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.outputs["verdict"] == "APPROVED" && r.outputs["fix"] == "true", "outputs %v", r.outputs)
		return p
	}},
	{"verdict/no review is no approval", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		g := j.run("gate")
		r := j.run("verdict", "SCHEDULED_ROUND=1", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.code == 0 && r.outputs["verdict"] == "CHANGES_REQUESTED" && r.outputs["fix"] == "true", "exit %d, outputs %v", r.code, r.outputs)
		return p
	}},
	{"verdict/a first line that is neither word is no approval", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		g := j.run("gate")
		j.write(filepath.Join(j.dir, "review.md"), "Looks good to me\nAPPROVED\n")
		r := j.run("verdict", "SCHEDULED_ROUND=2", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.outputs["verdict"] == "CHANGES_REQUESTED", "outputs %v", r.outputs)
		return p
	}},
	{"verdict/a refusal that carries the word is no approval", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		for _, first := range []string{"NOT APPROVED: the bump pulls a new module into the binary", "APPROVED once the CHANGES_REQUESTED below are made", "APPROVED."} {
			g := j.run("gate")
			j.write(filepath.Join(j.dir, "review.md"), first+"\n")
			r := j.run("verdict", "SCHEDULED_ROUND=2", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
			p.want(r.outputs["verdict"] == "CHANGES_REQUESTED", "%q was read as %q", first, r.outputs["verdict"])
		}
		g := j.run("gate")
		j.write(filepath.Join(j.dir, "review.md"), "  APPROVED \r\n")
		r := j.run("verdict", "SCHEDULED_ROUND=2", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.outputs["verdict"] == "APPROVED", "the word alone, with spaces and a carriage return about it, was read as %q", r.outputs["verdict"])
		return p
	}},
	{"verdict/a reviewer that changed the checkout fails the job", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		g := j.run("gate")
		j.write(filepath.Join(j.dir, "review.md"), "APPROVED\n")
		j.file("README.md", "a page, edited by the reviewer\n")
		r := j.run("verdict", "SCHEDULED_ROUND=1", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.code != 0 && strings.Contains(r.all(), "changed the checkout"), "exit %d: %s", r.code, r.all())
		p.want(r.outputs["verdict"] == "", "a verdict was written all the same: %v", r.outputs)
		return p
	}},
	{"verdict/a reviewer that committed fails the job", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		g := j.run("gate")
		j.write(filepath.Join(j.dir, "review.md"), "APPROVED\n")
		j.commit("5: The reviewer's own idea", "README.md", "a page, edited twice\n")
		r := j.run("verdict", "SCHEDULED_ROUND=2", "SCHEDULED_GATE_OUTCOME=success", "SCHEDULED_EXPECT_STATE="+g.outputs["state"])
		p.want(r.code != 0, "exit %d: %s", r.code, r.all())
		return p
	}},

	// ---- decide ----
	{"decide/every way a run ends", func(j *jobRepo) (p problems) {
		for _, c := range []struct {
			name     string
			env      []string
			code     int
			land     string
			mentions string
		}{
			{"nothing to change", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=false"}, 0, "false", "nothing to change"},
			{"the writer died with nothing written", []string{"SCHEDULED_WRITER_OUTCOME=failure", "SCHEDULED_CHANGED=false"}, 1, "false", "not a run with nothing to change"},
			{"the gate never looked", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED="}, 1, "false", "not a run with nothing to change"},
			{"approved in round 1", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=skipped", "SCHEDULED_GATE1=success", "SCHEDULED_VERDICT1=APPROVED"}, 0, "true", "approved after 1 round"},
			{"approved in round 1 by a writer that reached its ceiling", []string{"SCHEDULED_WRITER_OUTCOME=failure", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=skipped", "SCHEDULED_GATE1=success", "SCHEDULED_VERDICT1=APPROVED"}, 0, "true", "approved after 1 round"},
			{"approved in round 2", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=success", "SCHEDULED_GATE1=failure", "SCHEDULED_VERDICT1=CHANGES_REQUESTED", "SCHEDULED_GATE2=success", "SCHEDULED_VERDICT2=APPROVED"}, 0, "true", "approved after 2 round"},
			{"refused in round 2", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=success", "SCHEDULED_GATE1=success", "SCHEDULED_VERDICT1=CHANGES_REQUESTED", "SCHEDULED_GATE2=success", "SCHEDULED_VERDICT2=CHANGES_REQUESTED"}, 1, "false", "not approved after 2 round"},
			{"round 2's gate failed under an approval", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=failure", "SCHEDULED_GATE1=success", "SCHEDULED_VERDICT1=CHANGES_REQUESTED", "SCHEDULED_GATE2=failure", "SCHEDULED_VERDICT2=APPROVED"}, 1, "false", "not approved after 2 round"},
			{"the fix cycle left nothing at all", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=success", "SCHEDULED_GATE1=failure", "SCHEDULED_VERDICT1=CHANGES_REQUESTED", "SCHEDULED_GATE2=success", "SCHEDULED_CHANGED2=false", "SCHEDULED_VERDICT2="}, 0, "false", "nothing to change"},
			{"round 2's gate says nothing changed and failed all the same", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=success", "SCHEDULED_GATE1=failure", "SCHEDULED_VERDICT1=CHANGES_REQUESTED", "SCHEDULED_GATE2=failure", "SCHEDULED_CHANGED2=false", "SCHEDULED_VERDICT2="}, 1, "false", "not approved after 2 round"},
			{"round 1's approval does not stand for round 2", []string{"SCHEDULED_WRITER_OUTCOME=success", "SCHEDULED_CHANGED=true", "SCHEDULED_FIX_OUTCOME=success", "SCHEDULED_GATE1=failure", "SCHEDULED_VERDICT1=APPROVED", "SCHEDULED_GATE2=success", "SCHEDULED_VERDICT2="}, 1, "false", "not approved after 2 round"},
		} {
			r := j.run("decide", c.env...)
			p.want(r.code == c.code && r.outputs["land"] == c.land && strings.Contains(r.all(), c.mentions),
				"%s: exit %d and land=%q, want %d and %q, saying %q: %s", c.name, r.code, r.outputs["land"], c.code, c.land, c.mentions, r.all())
		}
		return p
	}},

	// ---- scan ----
	{"scan/a clean change and a clean report pass", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.write(filepath.Join(j.dir, "report.md"), "Edited the page.\n")
		r := j.run("scan", "SCHEDULED_SECRET_MODEL=an-opaque-value-of-no-known-shape-0123456789", "SCHEDULED_SECRET_GITHUB="+tokenShaped("github"))
		p.want(r.code == 0 && strings.Contains(r.all(), "2 place(s) read, no token"), "exit %d: %s", r.code, r.all())
		return p
	}},
	{"scan/a token the job held, whole, in a commit", func(j *jobRepo) (p problems) {
		const held = "an-opaque-value-of-no-known-shape-0123456789"
		j.commit("5: Edit the page", "README.md", "a page\nexport TOKEN="+held+"\n")
		r := j.run("scan", "SCHEDULED_SECRET_MODEL="+held)
		p.want(r.code == 1 && strings.Contains(r.all(), "the commits carries, whole, a token this job held"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(r.all(), held), "the scan printed the token it found")
		return p
	}},
	{"scan/a token the job held, whole, in a commit's message", func(j *jobRepo) (p problems) {
		const held = "an-opaque-value-of-no-known-shape-0123456789"
		j.commit("5: Edit the page with "+held, "README.md", "a page, edited\n")
		r := j.run("scan", "SCHEDULED_SECRET_GITHUB="+held)
		p.want(r.code == 1, "exit %d: %s", r.code, r.all())
		return p
	}},
	{"scan/a token the job held, whole, in the report", func(j *jobRepo) (p problems) {
		const held = "an-opaque-value-of-no-known-shape-0123456789"
		j.write(filepath.Join(j.dir, "report.md"), "origin\thttps://x-access-token:"+held+"@github.com/x/y.git (fetch)\n")
		r := j.run("scan", "SCHEDULED_SECRET_GITHUB="+held)
		p.want(r.code == 1 && strings.Contains(r.all(), "report.md carries, whole, a token this job held"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(r.all(), held), "the scan printed the token it found")
		return p
	}},
	{"scan/a model token's shape in a commit", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page\n"+tokenShaped("model")+"\n")
		r := j.run("scan")
		p.want(r.code == 1 && strings.Contains(r.all(), "the commits carries something of a token's shape"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(r.all(), tokenShaped("model")), "the scan printed what it found")
		return p
	}},
	{"scan/a GitHub token's shape in an execution log", func(j *jobRepo) (p problems) {
		j.write(filepath.Join(j.dir, "write-execution.json"), `{"output":"`+tokenShaped("github")+`"}`+"\n")
		r := j.run("scan")
		p.want(r.code == 1 && strings.Contains(r.all(), "write-execution.json carries something of a token's shape at line(s) 1"), "exit %d: %s", r.code, r.all())
		return p
	}},
	{"scan/a file in a directory under the job's directory is read too", func(j *jobRepo) (p problems) {
		j.write(filepath.Join(j.dir, "logs", "deep", "remote.txt"), "origin https://x-access-token:"+tokenShaped("github")+"@github.com/x/y.git\n")
		r := j.run("scan")
		p.want(r.code == 1 && strings.Contains(r.all(), "logs/deep/remote.txt carries something of a token's shape"), "exit %d: %s", r.code, r.all())
		return p
	}},
	{"scan/a short value is compared with nothing", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		r := j.run("scan", "SCHEDULED_SECRET_MODEL=a", "SCHEDULED_SECRET_GITHUB=")
		p.want(r.code == 0, "exit %d: a value of one letter is in every file: %s", r.code, r.all())
		return p
	}},

	// ---- bundle and land ----
	{"land/the bundle's commits become the job's branch, and the landing checkout stays where it was", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.commit("5: Edit it again", "README.md", "a page, edited twice\n")
		b := j.run("bundle")
		p.want(b.code == 0 && strings.Contains(b.all(), "2 commit(s)"), "bundle: exit %d: %s", b.code, b.all())
		landing := j.landing()
		r := j.runIn(landing, "land")
		branch := "scheduled/" + scheduledJobName + "-" + time.Now().UTC().Format("20060102")
		p.want(r.code == 0, "land: exit %d: %s", r.code, r.all())
		// The date is the run's: a run across midnight UTC names the next day.
		p.want(regexp.MustCompile(`^scheduled/`+scheduledJobName+`-[0-9]{8}$`).MatchString(r.outputs["branch"]), "branch=%q, want %s", r.outputs["branch"], branch)
		p.want(r.outputs["head"] == j.rev(j.work, "HEAD"), "head=%q, want the writer's head: the merge is asked for that commit", r.outputs["head"])
		p.want(j.rev(j.origin, "refs/heads/"+r.outputs["branch"]) == j.rev(j.work, "HEAD"), "the origin's %s is not the writer's head", r.outputs["branch"])
		p.want(j.rev(landing, "HEAD") == j.sha, "the landing checkout moved off the commit the run started on")
		p.want(j.read(filepath.Join(landing, "README.md")) == "a page\n", "the landing checkout holds the bundle's files: nothing of the bundle may be checked out")
		p.want(j.rev(j.origin, "refs/heads/master") == j.sha, "master moved")
		return p
	}},
	{"land/a run on another branch pushes nothing", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.run("bundle")
		r := j.runIn(j.landing(), "land", "GITHUB_REF=refs/heads/feature-x")
		p.want(r.code == 1 && strings.Contains(r.all(), "runs on master and nowhere else"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(j.refs(), "scheduled/"), "a branch was pushed: %s", j.refs())
		return p
	}},
	{"land/no bundle, nothing pushed", func(j *jobRepo) (p problems) {
		r := j.runIn(j.landing(), "land")
		p.want(r.code == 1 && strings.Contains(r.all(), "no change.bundle"), "exit %d: %s", r.code, r.all())
		return p
	}},
	{"land/a release pin in the bundle is refused by the landing job's own guard", func(j *jobRepo) (p problems) {
		j.commit("5: Bump", "plugin/bin/VERSION", "9.9.9\n", "scripts/ci/pr-guard.sh", "#!/bin/sh\nexit 0\n")
		j.run("bundle")
		r := j.runIn(j.landing(), "land")
		p.want(r.code == 1 && strings.Contains(r.all(), "trips pr-guard.sh"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(j.refs(), "scheduled/"), "a branch was pushed: %s", j.refs())
		return p
	}},
	{"land/a workflow file in the bundle is refused", func(j *jobRepo) (p problems) {
		j.commit("5: Move a pin", ".github/workflows/ci.yml", "name: ci\n")
		j.run("bundle")
		r := j.runIn(j.landing(), "land")
		p.want(r.code == 1 && strings.Contains(r.all(), "a path no scheduled job changes"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(j.refs(), "scheduled/"), "a branch was pushed: %s", j.refs())
		return p
	}},
	{"land/commits that do not sit on the starting commit are refused", func(j *jobRepo) (p problems) {
		// The bundle sits on the first commit, and the landing job's run started on a later one: the two
		// share history, so nothing but the look at the ancestry tells them apart.
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.run("bundle")
		landing := j.landing()
		j.gitIn(landing, "commit", "--quiet", "--allow-empty", "-m", "master moved on")
		r := j.runIn(landing, "land", "GITHUB_SHA="+j.rev(landing, "HEAD"))
		p.want(r.code == 1 && strings.Contains(r.all(), "do not sit on the commit the run started on"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(j.refs(), "scheduled/"), "a branch was pushed: %s", j.refs())
		return p
	}},
	{"land/a token's shape in the bundle is refused by the landing job's own look", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page\n"+tokenShaped("github")+"\n")
		j.run("bundle")
		r := j.runIn(j.landing(), "land")
		p.want(r.code == 1 && strings.Contains(r.all(), "a token's shape"), "exit %d: %s", r.code, r.all())
		p.want(!strings.Contains(j.refs(), "scheduled/"), "a branch was pushed: %s", j.refs())
		return p
	}},
	{"land/a token's shape in the report is refused", func(j *jobRepo) (p problems) {
		j.commit("5: Edit the page", "README.md", "a page, edited\n")
		j.run("bundle")
		j.write(filepath.Join(j.dir, "report.md"), "Edited. "+tokenShaped("model")+"\n")
		r := j.runIn(j.landing(), "land")
		p.want(r.code == 1 && !strings.Contains(j.refs(), "scheduled/"), "exit %d, refs %s", r.code, j.refs())
		return p
	}},

	// ---- open ----
	{"open/a when-green job opens the pull request under its work item and arms the merge", func(j *jobRepo) (p problems) {
		j.write(filepath.Join(j.dir, "report.md"), "Refreshed two paths in the developer's pages.\n")
		r := j.run("open", "STANDING_WORK_ITEM=434", "SCHEDULED_TITLE=Refresh the structural documentation", "SCHEDULED_MERGE=when-green",
			"SCHEDULED_BRANCH=scheduled/update-documentation-20261201", "SCHEDULED_HEAD="+scheduledHead, "SCHEDULED_RUN_URL=https://github.com/x/y/actions/runs/1")
		p.want(r.code == 0, "exit %d: %s", r.code, r.all())
		log := j.ghLog()
		p.want(strings.Contains(log, "pr create --repo "+scheduledRepo+" --base master --head scheduled/update-documentation-20261201 --title 434: Refresh the structural documentation --body-file "), "the pull request was opened as: %s", log)
		p.want(strings.Contains(log, " --label maintenance\n"), "no maintenance label: %s", log)
		p.want(strings.Contains(log, "pr merge "+scheduledPullURL+" --squash --auto --match-head-commit "+scheduledHead+" --repo "+scheduledRepo+"\n"), "the merge was not armed for the commit the run pushed: %s", log)
		body := j.read(filepath.Join(j.base, "gh", "body.md"))
		p.want(strings.HasPrefix(body, "Refreshed two paths in the developer's pages.\n"), "the body does not begin with the report: %q", body)
		p.want(strings.Contains(body, "Scheduled job `update-documentation`") && strings.Contains(body, "https://github.com/x/y/actions/runs/1") && strings.Contains(body, "merges by itself"), "the body does not say where it came from: %q", body)
		return p
	}},
	{"open/a by-hand job opens the pull request and arms nothing", func(j *jobRepo) (p problems) {
		r := j.run("open", "STANDING_WORK_ITEM=436", "SCHEDULED_TITLE=Review the repository and apply minimal fixes", "SCHEDULED_MERGE=by-hand",
			"SCHEDULED_BRANCH=scheduled/review-repository-20261201", "SCHEDULED_HEAD="+scheduledHead)
		p.want(r.code == 0, "exit %d: %s", r.code, r.all())
		p.want(strings.Contains(j.ghLog(), "pr create ") && !strings.Contains(j.ghLog(), "pr merge"), "gh was asked for: %s", j.ghLog())
		p.want(strings.Contains(j.read(filepath.Join(j.base, "gh", "body.md")), "waits for the owner's merge"), "the body does not say that it waits")
		return p
	}},
	{"open/a pull request already green is merged only after its required checks are read", func(j *jobRepo) (p problems) {
		r := j.run("open", "STANDING_WORK_ITEM=434", "SCHEDULED_TITLE=Refresh the structural documentation", "SCHEDULED_MERGE=when-green",
			"SCHEDULED_BRANCH=scheduled/update-documentation-20261201", "SCHEDULED_HEAD="+scheduledHead, "FAKE_GH_AUTO_FAILS=1")
		p.want(r.code == 0, "exit %d: %s", r.code, r.all())
		log := j.ghLog()
		checks := strings.Index(log, "pr checks "+scheduledPullURL+" --required")
		merge := strings.Index(log, "pr merge "+scheduledPullURL+" --squash --match-head-commit "+scheduledHead+" --repo")
		p.want(checks >= 0 && merge > checks, "the plain merge must follow a green `pr checks --required`: %s", log)
		return p
	}},
	{"open/a pull request whose checks are not green is never merged outright", func(j *jobRepo) (p problems) {
		r := j.run("open", "STANDING_WORK_ITEM=434", "SCHEDULED_TITLE=Refresh the structural documentation", "SCHEDULED_MERGE=when-green",
			"SCHEDULED_BRANCH=scheduled/update-documentation-20261201", "SCHEDULED_HEAD="+scheduledHead, "FAKE_GH_AUTO_FAILS=1", "FAKE_GH_CHECKS=1")
		p.want(r.code != 0, "exit 0 with the merge neither armed nor made: %s", r.all())
		p.want(!strings.Contains(j.ghLog(), "--squash --match-head-commit"), "a merge past red checks: %s", j.ghLog())
		return p
	}},
	{"open/a run on another branch opens nothing", func(j *jobRepo) (p problems) {
		r := j.run("open", "STANDING_WORK_ITEM=434", "SCHEDULED_TITLE=Refresh the structural documentation", "SCHEDULED_MERGE=when-green",
			"SCHEDULED_BRANCH=scheduled/update-documentation-20261201", "SCHEDULED_HEAD="+scheduledHead, "GITHUB_REF=refs/heads/feature-x")
		p.want(r.code == 1 && j.ghLog() == "", "exit %d, gh log %q", r.code, j.ghLog())
		return p
	}},
	{"open/without the standing work item's number nothing is opened", func(j *jobRepo) (p problems) {
		for _, number := range []string{"", "15a", "four"} {
			r := j.run("open", "STANDING_WORK_ITEM="+number, "SCHEDULED_TITLE=Refresh the structural documentation", "SCHEDULED_MERGE=when-green",
				"SCHEDULED_BRANCH=scheduled/update-documentation-20261201", "SCHEDULED_HEAD="+scheduledHead)
			p.want(r.code == 1 && strings.Contains(r.all(), "not a work item's number"), "number %q: exit %d: %s", number, r.code, r.all())
		}
		p.want(j.ghLog() == "", "gh was called: %s", j.ghLog())
		return p
	}},

	// ---- sweep ----
	{"sweep/the branches of closed pull requests go, an open one stops the run", func(j *jobRepo) (p problems) {
		ref := func(name string) string { return `{"ref":"refs/heads/` + name + `"}` }
		pre := "scheduled/" + scheduledJobName + "-"
		j.write(filepath.Join(j.base, "gh", "refs.json"), "["+ref(pre+"20261001")+","+ref(pre+"20261101")+"]\n["+ref(pre+"20261102")+","+ref(pre+"notes")+"]\n")
		j.write(filepath.Join(j.base, "gh", "pulls-scheduled_"+scheduledJobName+"-20261001.json"), `[{"number":3,"state":"closed"}]`)
		j.write(filepath.Join(j.base, "gh", "pulls-scheduled_"+scheduledJobName+"-20261101.json"), `[{"number":2,"state":"closed"},{"number":9,"state":"open"}]`)
		r := j.run("sweep", "STANDING_WORK_ITEM=434")
		p.want(r.code == 0 && r.outputs["open_pr"] == "9", "exit %d, open_pr=%q: %s", r.code, r.outputs["open_pr"], r.all())
		log := j.ghLog()
		p.want(strings.Contains(log, "api --paginate repos/"+scheduledRepo+"/git/matching-refs/heads/"+pre+"\n"), "the listing asked for: %s", log)
		p.want(strings.Contains(log, "api -X DELETE repos/"+scheduledRepo+"/git/refs/heads/"+pre+"20261001\n"), "the merged pull request's branch stayed: %s", log)
		p.want(strings.Contains(log, "api -X DELETE repos/"+scheduledRepo+"/git/refs/heads/"+pre+"20261102\n"), "the branch with no pull request stayed: %s", log)
		p.want(!strings.Contains(log, "DELETE repos/"+scheduledRepo+"/git/refs/heads/"+pre+"20261101"), "the open pull request's branch was removed: %s", log)
		p.want(!strings.Contains(log, "DELETE repos/"+scheduledRepo+"/git/refs/heads/"+pre+"notes"), "a branch this job never named was removed: %s", log)
		return p
	}},
	{"sweep/nothing left and nothing open", func(j *jobRepo) (p problems) {
		j.write(filepath.Join(j.base, "gh", "refs.json"), "[]\n")
		r := j.run("sweep", "STANDING_WORK_ITEM=434")
		open, written := r.outputs["open_pr"]
		p.want(r.code == 0 && written && open == "", "exit %d, open_pr=%q (written: %v): %s", r.code, open, written, r.all())
		return p
	}},
	{"sweep/a listing that fails stops the run and removes nothing", func(j *jobRepo) (p problems) {
		r := j.run("sweep", "STANDING_WORK_ITEM=434", "FAKE_GH_FAILS=1")
		p.want(r.code != 0 && !strings.Contains(j.ghLog(), "DELETE"), "exit %d: %s", r.code, j.ghLog())
		return p
	}},
	{"sweep/a run started on another branch stops before any agent", func(j *jobRepo) (p problems) {
		j.write(filepath.Join(j.base, "gh", "refs.json"), "[]\n")
		for _, ref := range []string{"refs/heads/feature-x", "refs/tags/v0.60.0", ""} {
			r := j.run("sweep", "STANDING_WORK_ITEM=434", "GITHUB_REF="+ref)
			p.want(r.code == 1 && strings.Contains(r.all(), "runs on master and nowhere else"), "ref %q: exit %d: %s", ref, r.code, r.all())
		}
		p.want(j.ghLog() == "", "gh was called: %s", j.ghLog())
		return p
	}},
	{"sweep/without the standing work item's number the run stops before any agent", func(j *jobRepo) (p problems) {
		r := j.run("sweep", "STANDING_WORK_ITEM=")
		p.want(r.code == 1 && j.ghLog() == "", "exit %d, gh log %q", r.code, j.ghLog())
		return p
	}},
}

// refs is every ref of the origin, one a line.
func (j *jobRepo) refs() string {
	j.t.Helper()
	//nolint:gosec // G204: git with a fixed argument list, in a fixture directory.
	cmd := exec.CommandContext(j.t.Context(), "git", "for-each-ref", "--format=%(refname)")
	cmd.Dir = j.origin
	cmd.Env = j.env
	out, err := cmd.Output()
	if err != nil {
		j.t.Fatalf("listing the origin's refs: %v", err)
	}
	return string(out)
}

// checkScheduledScript runs every case, or the one named, against one copy of the script and reports each
// that does not hold.
func checkScheduledScript(t *testing.T, rep reporter, script, only string) {
	t.Helper()
	ran := 0
	for _, c := range scheduledCases {
		if only != "" && c.name != only {
			continue
		}
		ran++
		for _, problem := range c.run(newJobRepo(t, script)) {
			rep.Errorf("%s: %s", c.name, problem)
		}
	}
	if ran == 0 {
		t.Fatalf("no case is named %q", only)
	}
}

func TestScheduledJobScript(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), scheduledScriptRel)
	for _, c := range scheduledCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			checkScheduledScript(t, t, script, c.name)
		})
	}
}

// scheduledScriptMutations: one edit to a copy of the script, and the case that must then fail.
var scheduledScriptMutations = []struct {
	name, old, replacement, fails string
}{
	{"the_gate_takes_uncommitted_work_for_nothing", `if [ "$changed" -gt 0 ] || [ "$stashed" -gt 0 ]; then`, `if false; then`,
		"gate/work that is not committed is a finding"},
	{"the_gate_runs_the_working_trees_guard", `if ! sh "$work/pr-guard.sh" "$base" "$head"`, `if ! sh scripts/ci/pr-guard.sh "$base" "$head"`,
		"gate/a release pin is held by the guard of the starting commit"},
	{"the_gate_lets_a_workflow_file_through", `if [ -n "$touched" ]; then`, `if false; then`,
		"gate/a workflow file is a path no scheduled job changes"},
	{"the_gate_stops_at_the_first_failing_target", `      if [ "$status" -ne 0 ]; then`, "      if [ \"$status\" -ne 0 ]; then\n        finding \"stopped\"; break",
		"gate/a failing target is a finding, and the others still run"},
	{"no_review_is_an_approval", `    verdict=CHANGES_REQUESTED
  fi
  out verdict`, `    verdict=APPROVED
  fi
  out verdict`, "verdict/no review is no approval"},
	{"the_verdict_is_read_from_the_last_line", `case $(head -n 1 "$review"`, `case $(tail -n 1 "$review"`,
		"verdict/a first line that is neither word is no approval"},
	{"the_verdict_is_a_word_anywhere_in_the_line", `      APPROVED) verdict=APPROVED ;;`, `      *APPROVED*) verdict=APPROVED ;;`,
		"verdict/a refusal that carries the word is no approval"},
	{"the_reviewer_inherits_a_review", `  rm -f "$SCHEDULED_DIR/review.md"`, `  :`,
		"gate/a review left by an earlier session is no verdict"},
	{"the_gate_says_what_the_checkout_was_only_at_its_end", `  out changed true
  # Before the targets as well as after them: a gate that is ended at its limit has still said what the
  # checkout was, and the verdict has something to compare.
  out head "$(git rev-parse HEAD)"
  out state "$(state)"`, `  out changed true`,
		"gate/a gate that is ended among its targets has said what the checkout was"},
	{"a_fix_cycle_that_left_nothing_is_approved", `[ "$round" = 2 ] && [ "${SCHEDULED_CHANGED2:-}" = false ] && [ "$gate" = success ]`, `false`,
		"decide/every way a run ends"},
	{"a_reviewer_may_change_the_checkout", `  if [ "$(state)" != "$expect" ]; then`, `  if false; then`,
		"verdict/a reviewer that changed the checkout fails the job"},
	{"round_1_stays_as_review_md", `      rm -f "$review"`, `      :`,
		"verdict/a request for changes starts the fix cycle and keeps round 1"},
	{"a_failed_gate_under_an_approval_needs_no_fix", `if [ "${SCHEDULED_GATE_OUTCOME:-}" = success ] && [ "$verdict" = APPROVED ]; then`, `if [ "$verdict" = APPROVED ]; then`,
		"verdict/an approval over a failed gate still starts the fix cycle"},
	{"a_change_lands_on_the_gate_alone", `  if [ "$gate" = success ] && [ "$verdict" = APPROVED ]; then`, `  if [ "$gate" = success ]; then`,
		"decide/every way a run ends"},
	{"a_dead_writer_had_nothing_to_change", `[ "${SCHEDULED_CHANGED:-}" = false ] && [ "${SCHEDULED_WRITER_OUTCOME:-}" = success ]`, `[ "${SCHEDULED_CHANGED:-}" = false ]`,
		"decide/every way a run ends"},
	{"the_scan_compares_no_token_whole", `[ "${#value}" -ge 20 ] && holds_value "$value" "$1"`, `false`,
		"scan/a token the job held, whole, in the report"},
	{"the_scan_compares_a_short_value", `[ "${#value}" -ge 20 ] && holds_value`, `holds_value`,
		"scan/a short value is compared with nothing"},
	{"the_scan_reads_only_the_commits", `    scan_file "$file" "${file#"$SCHEDULED_DIR"/}"`, `    :`,
		"scan/a GitHub token's shape in an execution log"},
	{"the_scan_stops_at_the_top_of_the_directory", `find "$SCHEDULED_DIR" -type f >`, `find "$SCHEDULED_DIR" -maxdepth 1 -type f >`,
		"scan/a file in a directory under the job's directory is read too"},
	{"the_landing_checks_the_bundle_out", `"HEAD:refs/scheduled-job/incoming"`, `"HEAD:refs/scheduled-job/incoming" && git checkout --quiet refs/scheduled-job/incoming`,
		"land/the bundle's commits become the job's branch, and the landing checkout stays where it was"},
	{"the_landing_trusts_the_other_jobs_guard", `  sh "$work/pr-guard.sh" "$base" "$tip" || refuse`, `  : || refuse`,
		"land/a release pin in the bundle is refused by the landing job's own guard"},
	{"the_landing_takes_commits_off_the_starting_commit", `git merge-base --is-ancestor "$base" "$tip" || refuse`, `: || refuse`,
		"land/commits that do not sit on the starting commit are refused"},
	{"the_landing_looks_for_no_token", `  [ "$hits" -eq 0 ] || refuse`, `  : || refuse`,
		"land/a token's shape in the bundle is refused by the landing job's own look"},
	{"a_by_hand_job_arms_the_merge", `  if [ "$SCHEDULED_MERGE" = when-green ]; then`, `  if true; then`,
		"open/a by-hand job opens the pull request and arms nothing"},
	{"the_merge_is_made_past_the_checks", `      gh pr checks "$url" --required --repo "$GITHUB_REPOSITORY" &&
`, ``,
		"open/a pull request whose checks are not green is never merged outright"},
	{"the_merge_is_armed_for_any_head", `--squash --auto --match-head-commit "$SCHEDULED_HEAD" --repo`, `--squash --auto --repo`,
		"open/a when-green job opens the pull request under its work item and arms the merge"},
	{"the_plain_merge_takes_any_head", `gh pr merge "$url" --squash --match-head-commit "$SCHEDULED_HEAD" --repo`, `gh pr merge "$url" --squash --repo`,
		"open/a pull request already green is merged only after its required checks are read"},
	{"a_job_runs_on_any_branch", "sweep() {\n  need_number\n  need_master\n", "sweep() {\n  need_number\n",
		"sweep/a run started on another branch stops before any agent"},
	{"the_landing_pushes_from_any_branch", "land() {\n  need_master\n", "land() {\n",
		"land/a run on another branch pushes nothing"},
	{"a_pull_request_opens_from_any_branch", "open() {\n  need_number\n  need_master\n", "open() {\n  need_number\n",
		"open/a run on another branch opens nothing"},
	{"the_title_has_no_number", `--title "$STANDING_WORK_ITEM: $SCHEDULED_TITLE"`, `--title "$SCHEDULED_TITLE"`,
		"open/a when-green job opens the pull request under its work item and arms the merge"},
	{"a_pull_request_opens_without_a_number", "open() {\n  need_number\n  need_master\n", "open() {\n  need_master\n",
		"open/without the standing work item's number nothing is opened"},
	{"the_sweep_removes_an_open_pull_requests_branch", `      continue
    fi
    gh api -X DELETE`, `    fi
    gh api -X DELETE`, "sweep/the branches of closed pull requests go, an open one stops the run"},
	{"the_sweep_removes_any_branch_under_the_prefix", `      *) echo "left alone: $branch is not a name this job gives a branch"; continue ;;`, `      *) ;;`,
		"sweep/the branches of closed pull requests go, an open one stops the run"},
}

func TestScheduledJobCasesFailOnAMutatedScript(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G304: the repository's own script.
	original, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), scheduledScriptRel))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range scheduledScriptMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			if strings.Count(string(original), m.old) != 1 {
				t.Fatalf("the script carries the anchor %q %d time(s), so this mutation would be vacuous or ambiguous", m.old, strings.Count(string(original), m.old))
			}
			script := filepath.Join(t.TempDir(), "scheduled-job.sh")
			//nolint:gosec // G703: the target is under t.TempDir().
			if err := os.WriteFile(script, []byte(strings.Replace(string(original), m.old, m.replacement, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			var rec recorder
			checkScheduledScript(t, &rec, script, m.fails)
			if len(rec.msgs) == 0 {
				t.Fatalf("the case %q still holds on the mutated script, so it does not test what the mutation broke", m.fails)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}

// ---------------------------------------------------------------------------------------------------------
// the three workflows

// scheduledCommonMarker begins the text the three workflows share, to the end of the file.
const scheduledCommonMarker = "# ---- from here on the three scheduled jobs are one text (scripts/ci/scheduled_job_test.go holds it) ----------\n"

var (
	scheduledNumberLine = regexp.MustCompile(`(?m)^  STANDING_WORK_ITEM: "([^"]*)"$`)
	scheduledMergeLine  = regexp.MustCompile(`(?m)^  SCHEDULED_MERGE: (\S+)$`)
	// scheduledWritePermission is a line of a job's `permissions:` block that grants a write.
	scheduledWritePermission = regexp.MustCompile(`(?m)^      [a-z-]+: write\b`)
)

// uncommented is a workflow's text without its comment lines, which may say what is not done.
func uncommented(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// checkScheduledWorkflows reports what the three workflows under root, and the pull-request reviewer beside
// them, lack of the form work item 433 gave them.
func checkScheduledWorkflows(r reporter, root string) {
	r.Helper()
	var common string
	numbers := map[string]string{}
	for i, job := range scheduledWorkflows {
		rel := workflowsDirRel + "/" + job + ".yml"
		text, ok := readText(r, root, rel)
		if !ok {
			continue
		}
		head, rest, found := strings.Cut(text, scheduledCommonMarker)
		if !found {
			r.Errorf("%s has no line that begins the text the three scheduled jobs share", rel)
			continue
		}
		if i == 0 {
			common = rest
		} else if rest != common {
			r.Errorf("%s differs from %s.yml after the line that says the three are one text: a change to one job's steps is a change to all three", rel, scheduledWorkflows[0])
		}
		// What is the job's own.
		if !strings.Contains(head, "\n  SCHEDULED_JOB: "+job+"\n") {
			r.Errorf("%s does not name itself: SCHEDULED_JOB is the file's name, and the job's branches are named by it", rel)
		}
		if m := scheduledNumberLine.FindStringSubmatch(head); m == nil || !regexp.MustCompile(`^[0-9]+$`).MatchString(m[1]) {
			r.Errorf("%s carries no standing work item's number in digits (STANDING_WORK_ITEM): its pull requests are titled with it, and no other number stands in for one (work item 433)", rel)
		} else if other, taken := numbers[m[1]]; taken {
			r.Errorf("%s and %s name the same standing work item, %s: each job has its own", rel, other, m[1])
		} else {
			numbers[m[1]] = rel
		}
		merge := scheduledMergeLine.FindStringSubmatch(head)
		switch {
		case merge == nil || (merge[1] != "when-green" && merge[1] != "by-hand"):
			r.Errorf("%s does not say how its pull request merges (SCHEDULED_MERGE: when-green or by-hand)", rel)
		case job == "review-repository" && merge[1] != "by-hand":
			r.Errorf("%s arms the merge of a whole-tree review: that pull request waits for the owner (his standing decision 5)", rel)
		}
		for _, name := range []string{"SCHEDULED_TITLE: ", "SCHEDULED_GATE: ", "SCHEDULED_BRIEF: |\n"} {
			if !strings.Contains(head, "\n  "+name) {
				r.Errorf("%s has no %s", rel, strings.TrimSpace(name))
			}
		}
		for _, secret := range []string{"secrets.", "github.token"} {
			if strings.Contains(uncommented(head), secret) {
				r.Errorf("%s reads %s above the shared text: what each job may reach is said once, in the shared text", rel, secret)
			}
		}
	}
	if common == "" {
		return
	}
	shared := workflowsDirRel + "/" + scheduledWorkflows[0] + ".yml (and the two beside it)"
	code := uncommented(common)
	// Each job's text begins with the line break before its first key, so that a line can be looked for whole.
	_, afterSweep, okSweep := strings.Cut(code, "\n  sweep:")
	sweep, afterWork, okWork := strings.Cut(afterSweep, "\n  work:")
	work, land, okLand := strings.Cut(afterWork, "\n  land:")
	if !okSweep || !okWork || !okLand {
		r.Errorf("%s does not have the three jobs sweep, work and land, in that order", shared)
		return
	}
	const action = "uses: anthropics/claude-code-action@"
	// The job that runs the agents can write nothing.
	if !strings.Contains(work, "\n    permissions:\n      contents: read") {
		r.Errorf("%s: the job `work` does not open its permissions with `contents: read`: nothing in the job that runs the agents may write to the repository", shared)
	}
	for _, never := range []*regexp.Regexp{scheduledWritePermission, regexp.MustCompile(`GH_ACTIONS_TOKEN`), regexp.MustCompile(`id-token`)} {
		if found := never.FindString(work); found != "" {
			r.Errorf("%s: the job `work` carries %q: no permission to write, no token of the owner's and no identity to exchange for the Claude App's token belongs in the job that runs the agents", shared, strings.TrimSpace(found))
		}
	}
	if agents, handed := strings.Count(work, action), strings.Count(work, "          github_token: ${{ github.token }}"); agents != 4 || handed != agents {
		r.Errorf("%s: the job `work` has %d agent step(s) and %d of them are handed the job's own token, want 4 and 4: "+
			"an agent step without `github_token` is given a token of the Claude App, which the job's permissions do not bound", shared, agents, handed)
	}
	// The shell steps beside the agents run the script of the commit the run started on.
	show := `git show "$GITHUB_SHA:` + scheduledScriptRel + `" > "$RUNNER_TEMP/scheduled-job.sh"`
	if runs, shows, named := strings.Count(work, `sh "$RUNNER_TEMP/scheduled-job.sh" `), strings.Count(work, show), strings.Count(work, scheduledScriptRel); runs == 0 || runs != shows || named != shows {
		r.Errorf("%s: the job `work` runs the script %d time(s), takes it from the commit the run started on %d time(s) and names "+
			"its path %d time(s): every run is of that commit's copy, never the working tree's, which an agent can edit", shared, runs, shows, named)
	}
	for _, verb := range []string{"gate", "verdict", "decide", "scan", "bundle"} {
		if !strings.Contains(work, `sh "$RUNNER_TEMP/scheduled-job.sh" `+verb+"\n") {
			r.Errorf("%s: the job `work` never runs the script's `%s`", shared, verb)
		}
	}
	// The two tokens are compared whole in one step, and the artifact and the bundle wait for it.
	if strings.Count(work, "SCHEDULED_SECRET_MODEL: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}") != 1 || strings.Count(work, "SCHEDULED_SECRET_GITHUB: ${{ github.token }}") != 1 {
		r.Errorf("%s: the scan step is not handed the two tokens the job holds, once each", shared)
	}
	for _, line := range []string{
		"        if: steps.decide.outputs.land == 'true' && steps.scan.outcome == 'success'\n",
		"        if: ${{ !cancelled() && steps.scan.outcome == 'success' }}\n        uses: actions/upload-artifact@",
	} {
		if !strings.Contains(work, line) {
			r.Errorf("%s: the job `work` no longer carries\n%s\nnothing leaves the job, as a bundle or as an artifact, before the look for a token passed", shared, line)
		}
	}
	// The jobs that run no agent.
	for name, section := range map[string]string{"sweep": sweep, "land": land} {
		if strings.Contains(section, action) || strings.Contains(section, "CLAUDE_CODE_OAUTH_TOKEN") {
			r.Errorf("%s: the job `%s` runs an agent or holds the model's token: it can write to the repository, so it runs no model", shared, name)
		}
	}
	if strings.Contains(sweep, "GH_ACTIONS_TOKEN") {
		r.Errorf("%s: the job `sweep` holds the owner's token; the job's own deletes a branch", shared)
	}
	if strings.Count(land, "secrets.GH_ACTIONS_TOKEN") != 1 || !strings.Contains(land, "          GH_TOKEN: ${{ secrets.GH_ACTIONS_TOKEN }}\n          SCHEDULED_BRANCH: ${{ steps.land.outputs.branch }}\n") {
		r.Errorf("%s: the owner's token belongs to one step of `land`, the one that opens the pull request", shared)
	}
	if !strings.Contains(land, "\n    needs: work\n    if: needs.work.outputs.land == 'true'\n") {
		r.Errorf("%s: the job `land` does not wait for `work` to say that the change was approved", shared)
	}
	if !strings.Contains(work, "\n    needs: sweep\n    if: needs.sweep.outputs.open_pr == ''\n") {
		r.Errorf("%s: the job `work` does not stop while an earlier run's pull request is open", shared)
	}
	// The pull-request reviewer leaves a scheduled job's pull request alone, and the script names its
	// branches by the prefix that clause reads.
	const skip = "!startsWith(github.event.pull_request.head.ref, 'scheduled/')"
	if review, ok := readText(r, root, loopReviewRel); ok && !strings.Contains(uncommented(review), skip) {
		r.Errorf("%s no longer skips a branch that begins `scheduled/` (%s): a scheduled job's pull request was reviewed in the run that opened it", loopReviewRel, skip)
	}
}

func TestTheScheduledWorkflows(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	checkScheduledWorkflows(t, root)
	// The script and the reviewer's clause agree on the prefix. The script is not under the workflows'
	// directory, so the copies of the mutation cases do not carry it: it is read here.
	if script, ok := readText(t, root, scheduledScriptRel); ok && !strings.Contains(script, `branch="scheduled/$SCHEDULED_JOB-$(date -u +%Y%m%d)"`) {
		t.Errorf("%s no longer names a job's branch `scheduled/<job>-<date>`: review-pull-request.yml skips that prefix, and `sweep` removes those names", scheduledScriptRel)
	}
	// `sweep` and `land` run the script by its path, so it has to be a file that can be run.
	if info, err := os.Stat(filepath.Join(root, scheduledScriptRel)); err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable (%v): the jobs `sweep` and `land` run it by its path", scheduledScriptRel, err)
	}
}

// TestNothingNamesFifteenAsAWorkItem holds work item 433's second done criterion: no workflow, agent file or
// rule file names `15` as the number of a work order that names none. It was the number of the build-out card
// of the Trello days, minted into the title of every pull request of the issue-driven loop; a scheduled job's
// pull request carries the number of its standing work item now.
func TestNothingNamesFifteenAsAWorkItem(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	// Not a number that ends in 15: a work item 415 is no stand-in.
	standIn := regexp.MustCompile("`15`|`15:|\"15: |(^|[^0-9])15: <")
	for _, pattern := range []string{".github/workflows/*.yml", ".claude/agents/*.md", "docs/agent/repository/*.md", "docs/developer/contribute/*.md", "AGENTS.md", "CLAUDE.md"} {
		files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil || len(files) == 0 {
			t.Fatalf("no file matches %s (%v)", pattern, err)
		}
		for _, file := range files {
			//nolint:gosec // G304: a file of the repository under test.
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(data), "\n") {
				if standIn.MatchString(line) {
					rel, _ := filepath.Rel(root, file)
					t.Errorf("%s:%d names 15 as a work item's number: %s", rel, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}

var scheduledWorkflowMutations = []struct {
	name    string
	mutate  mutation
	because string
}{
	{
		"one_job_changes_its_steps_alone",
		replaceInFile(workflowsDirRel+"/update-dependencies.yml", "        timeout-minutes: 60\n", "        timeout-minutes: 90\n"),
		"the three are one text: a change made to one is a job that differs from what the tests and the pages describe",
	},
	{
		"a_job_has_no_number",
		replaceInFile(workflowsDirRel+"/review-repository.yml", "  STANDING_WORK_ITEM: \"", "  STANDING_WORK_ITEM: \"x"),
		"its pull request would have no work item to be titled with",
	},
	{
		"the_repository_review_merges_by_itself",
		replaceInFile(workflowsDirRel+"/review-repository.yml", "  SCHEDULED_MERGE: by-hand\n", "  SCHEDULED_MERGE: when-green\n"),
		"a whole-tree review waits for the owner",
	},
	{
		"an_agent_step_gets_the_apps_token",
		replaceInAll("          github_token: ${{ github.token }}       # the job's read-only token, so the action mints none (the header)\n", ""),
		"without `github_token` the action exchanges the job's identity for a token of the Claude App",
	},
	{
		"the_agents_job_can_write",
		replaceInAll("      contents: read              # the boundary: nothing in this job, agent or shell, can write to the repository\n",
			"      contents: write\n"),
		"the writer could push, and the reviewer too",
	},
	{
		"the_agents_job_holds_the_owners_token",
		replaceInAll("          SCHEDULED_SECRET_GITHUB: ${{ github.token }}\n", "          SCHEDULED_SECRET_GITHUB: ${{ secrets.GH_ACTIONS_TOKEN }}\n"),
		"a classic token of the owner's account in the job that runs a model",
	},
	{
		"a_shell_step_runs_the_working_trees_script",
		replaceInAll("          git show \"$GITHUB_SHA:scripts/ci/scheduled-job.sh\" > \"$RUNNER_TEMP/scheduled-job.sh\"\n          sh \"$RUNNER_TEMP/scheduled-job.sh\" decide\n",
			"          sh scripts/ci/scheduled-job.sh decide\n"),
		"the agents can edit that copy",
	},
	{
		"the_bundle_leaves_before_the_look_for_a_token",
		replaceInAll("        if: steps.decide.outputs.land == 'true' && steps.scan.outcome == 'success'\n", "        if: steps.decide.outputs.land == 'true'\n"),
		"the branch is public the moment the landing job pushes it",
	},
	{
		"the_landing_job_runs_an_agent",
		replaceInAll("      - name: Push the change as a branch\n", "      - uses: anthropics/claude-code-action@c0949c50a78c1ec8ce2647db36629b870e715d0c\n      - name: Push the change as a branch\n"),
		"the job that holds the owner's token runs no model",
	},
	{
		"the_landing_does_not_wait_for_the_verdict",
		replaceInAll("    needs: work\n    if: needs.work.outputs.land == 'true'\n", "    needs: work\n"),
		"a change that was refused would be pushed",
	},
	{
		"a_run_starts_over_an_open_pull_request",
		replaceInAll("    needs: sweep\n    if: needs.sweep.outputs.open_pr == ''\n", "    needs: sweep\n"),
		"a second pull request of one job on the first one's lines",
	},
	{
		"the_reviewer_reviews_a_scheduled_pull_request_again",
		replaceInFile(loopReviewRel, " &&\n      !startsWith(github.event.pull_request.head.ref, 'scheduled/')\n", "\n"),
		"a second review, and a request for changes there starts the fixer on a change its own run closed",
	},
}

// replaceInAll makes one replacement in each of the three scheduled workflows, so that they stay one text
// and the mutation is seen for what it is.
func replaceInAll(old, replacement string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		for _, job := range scheduledWorkflows {
			replaceInFile(workflowsDirRel+"/"+job+".yml", old, replacement)(t, root)
		}
	}
}

func TestScheduledWorkflowsPassOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	var rec recorder
	checkScheduledWorkflows(&rec, copyWorkflows(t))
	if len(rec.msgs) != 0 {
		t.Errorf("the check reported %d problem(s) on an unmutated copy: %s", len(rec.msgs), strings.Join(rec.msgs, "; "))
	}
}

func TestScheduledWorkflowsFailOnAMutatedCopy(t *testing.T) {
	t.Parallel()
	for _, m := range scheduledWorkflowMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			root := copyWorkflows(t)
			m.mutate(t, root)
			var rec recorder
			checkScheduledWorkflows(&rec, root)
			if len(rec.msgs) == 0 {
				t.Fatalf("the mutation changed nothing the check can see, so the check is vacuous (%s)", m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}
