//go:build !windows

// scripts/release-ci-check.sh: what `ci.yml` made of a commit, for a release to stand on (work item 399,
// ADR 0014). Until 2026-11-22 neither release script looked, and two plugin releases went out over a red job.
//
// The script is run for real against a stand-in `gh` that answers from a file, one line per call, so a run
// can be in progress on the first call and ended on the third. Every case is written against `reporter` and
// runs twice: on the repository's script, where a report is a failure, and on mutated copies, where silence is
// the bug. The filter the script hands `gh --jq` is run through a real `jq` on a recorded answer, since the
// stand-in cannot evaluate it.
package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	releaseCIScriptRel = "scripts/release-ci-check.sh"
	releaseCISha       = "0123456789abcdef0123456789abcdef01234567"
	releaseCIRunURL    = "https://github.test/runs/7"
)

// ghStub is a stand-in `gh` on PATH. Each call appends its arguments to gh.log and prints the next line of
// `answers` (the last line again once they run out): the line the script's `--jq` filter would have printed,
// `-` for "no run", or `!fail` for a gh that cannot reach GitHub.
type ghStub struct {
	t   *testing.T
	dir string // holds bin/gh, answers, calls and gh.log
}

// ghStubBody is the stand-in's script, keeping its three files in dir.
func ghStubBody(dir string) string {
	return `#!/bin/sh
d=` + shellQuote(dir) + `
printf 'gh %s\n' "$*" >> "$d/gh.log"
n=$(cat "$d/calls" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$d/calls"
total=$(grep -c '' "$d/answers")
[ "$n" -le "$total" ] || n=$total
line=$(sed -n "${n}p" "$d/answers")
case $line in
  '!fail') echo "gh: could not connect to github.test" >&2; exit 1 ;;
  '-') exit 0 ;;
  *) printf '%s\n' "$line" ;;
esac
`
}

// writeGhAnswers sets what the stand-in in dir prints at its next calls and starts its count again.
func writeGhAnswers(t *testing.T, dir string, lines ...string) {
	t.Helper()
	for name, text := range map[string]string{"answers": strings.Join(lines, "\n") + "\n", "calls": "0\n", "gh.log": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatalf("writing the stub's %s: %v", name, err)
		}
	}
}

func newGhStub(t *testing.T) *ghStub {
	t.Helper()
	g := &ghStub{t: t, dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(g.dir, "bin"), 0o700); err != nil {
		t.Fatalf("creating the stub's directory: %v", err)
	}
	testutil.WriteExecutable(t, filepath.Join(g.dir, "bin", "gh"), []byte(ghStubBody(g.dir)))
	g.answers("7 completed success " + releaseCIRunURL)
	return g
}

// answers sets what the next calls print and starts the count again.
func (g *ghStub) answers(lines ...string) {
	g.t.Helper()
	writeGhAnswers(g.t, g.dir, lines...)
}

func (g *ghStub) binDir() string { return filepath.Join(g.dir, "bin") }

func (g *ghStub) log() string {
	g.t.Helper()
	//nolint:gosec // G304: under t.TempDir().
	data, err := os.ReadFile(filepath.Join(g.dir, "gh.log"))
	if err != nil {
		g.t.Fatalf("reading gh.log: %v", err)
	}
	return string(data)
}

func (g *ghStub) calls() int { return strings.Count(g.log(), "gh ") }

// run starts the script with the stand-in first on PATH, a poll of one second, and the given variables.
func (g *ghStub) run(script string, env []string, args ...string) result {
	g.t.Helper()
	full := append(baseEnv(g.t, g.dir), "RELEASE_CI_POLL_SECONDS=1")
	for i, kv := range full {
		if strings.HasPrefix(kv, "PATH=") {
			full[i] = "PATH=" + g.binDir() + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
	}
	return runScriptAt(g.t, script, g.dir, append(full, env...), args...)
}

const (
	ciRunning = "7 in_progress - " + releaseCIRunURL
	ciQueued  = "7 queued - " + releaseCIRunURL
	ciGreen   = "7 completed success " + releaseCIRunURL
	ciRed     = "7 completed failure " + releaseCIRunURL
)

// releaseCICases: what GitHub answers, what was given, and what the script must do with it.
var releaseCICases = []struct {
	name    string
	answers []string
	env     []string
	code    int
	prints  []string
	never   []string
	calls   int // gh calls expected; 0 means "do not count"
}{
	{
		"a_green_run_lets_the_release_go_on", []string{ciGreen}, nil, 0,
		[]string{"ci.yml on " + releaseCISha + ": success (run 7, " + releaseCIRunURL + ")"}, []string{"Not releasing"}, 1,
	},
	{
		"a_red_run_stops_it_and_says_how_to_go_on", []string{ciRed}, nil, 1,
		[]string{"ci.yml on " + releaseCISha + ": failure (run 7, ", "Not releasing", "the owner's to allow", "release_over_ci=7"}, nil, 1,
	},
	{
		"a_cancelled_run_is_red", []string{"7 completed cancelled " + releaseCIRunURL}, nil, 1,
		[]string{": cancelled (run 7, ", "release_over_ci=7"}, nil, 0,
	},
	{
		"the_override_that_names_the_red_run_releases", []string{ciRed}, []string{"release_over_ci=7"}, 0,
		[]string{": failure (run 7, ", "releasing over the red run 7: release_over_ci=7 was given"}, []string{"Not releasing"}, 0,
	},
	{
		"an_override_for_another_run_does_not", []string{ciRed}, []string{"release_over_ci=8"}, 1,
		[]string{"release_over_ci='8' does not name this run", "release_over_ci=7"}, []string{"releasing over"}, 0,
	},
	{
		"missing_does_not_override_a_red_run", []string{ciRed}, []string{"release_over_ci=missing:" + releaseCISha}, 1,
		[]string{"release_over_ci='missing:" + releaseCISha + "' does not name this run"}, []string{"releasing over"}, 0,
	},
	{
		"no_run_stops_it", []string{"-"}, []string{"RELEASE_CI_APPEAR_SECONDS=1"}, 1,
		[]string{"waiting up to 1s for one", "ci.yml on " + releaseCISha + ": no run", "release_over_ci=missing:" + releaseCISha}, nil, 0,
	},
	{
		"the_override_that_names_the_commit_releases_over_no_run", []string{"-"},
		[]string{"RELEASE_CI_APPEAR_SECONDS=0", "release_over_ci=missing:" + releaseCISha}, 0,
		[]string{": no run", "releasing over a missing run: release_over_ci=missing:" + releaseCISha + " was given"}, []string{"Not releasing"}, 1,
	},
	{
		"missing_for_another_commit_does_not_override", []string{"-"},
		[]string{"RELEASE_CI_APPEAR_SECONDS=0", "release_over_ci=missing:" + strings.Repeat("f", 40)}, 1,
		[]string{"does not name this commit's missing run", "release_over_ci=missing:" + releaseCISha}, []string{"releasing over"}, 0,
	},
	{
		"a_run_id_does_not_override_no_run", []string{"-"}, []string{"RELEASE_CI_APPEAR_SECONDS=0", "release_over_ci=7"}, 1,
		[]string{"release_over_ci='7' does not name this commit's missing run", "release_over_ci=missing:" + releaseCISha}, []string{"releasing over"}, 0,
	},
	{
		"a_run_in_progress_is_waited_for_and_then_read", []string{ciRunning, ciRunning, ciGreen}, nil, 0,
		[]string{"run 7 is in_progress; waiting for it to end", ": success (run 7, "}, []string{"Not releasing"}, 3,
	},
	{
		"a_queued_run_that_ends_red_stops_it", []string{ciQueued, ciRed}, nil, 1,
		[]string{"run 7 is queued; waiting for it to end", ": failure (run 7, ", "release_over_ci=7"}, nil, 2,
	},
	{
		"a_run_that_appears_late_is_waited_for", []string{"-", ciQueued, ciGreen}, []string{"RELEASE_CI_APPEAR_SECONDS=5"}, 0,
		[]string{"has no run on " + releaseCISha + " yet", ": success (run 7, "}, []string{": no run"}, 3,
	},
	{
		"a_run_that_does_not_end_stops_it", []string{ciRunning}, []string{"RELEASE_CI_WAIT_SECONDS=1"}, 1,
		[]string{"run 7 is still in_progress after 1s", "not releasing over a run that has not ended"}, []string{"release_over_ci="}, 0,
	},
	{
		"nothing_overrides_a_run_that_has_not_ended", []string{ciRunning}, []string{"RELEASE_CI_WAIT_SECONDS=1", "release_over_ci=7"}, 1,
		[]string{"not releasing over a run that has not ended"}, []string{"releasing over the red run"}, 0,
	},
	{
		"a_gh_that_cannot_ask_stops_it", []string{"!fail"}, []string{"release_over_ci=missing:" + releaseCISha, "RELEASE_CI_APPEAR_SECONDS=1"}, 1,
		[]string{"asking again in 1s", "could not ask GitHub for ci.yml's runs on " + releaseCISha + ", five times in a row", "not releasing blind"},
		[]string{"releasing over", ": no run"}, 5,
	},
	{
		"one_answer_that_fails_is_asked_again", []string{ciRunning, "!fail", ciGreen}, nil, 0,
		[]string{"gh could not list ci.yml's runs", ": success (run 7, "}, []string{"not releasing blind"}, 3,
	},
	{
		"an_override_over_a_green_run_is_said_and_not_needed", []string{ciGreen}, []string{"release_over_ci=7"}, 0,
		[]string{": success (run 7, ", "is not needed: the run is green"}, []string{"releasing over"}, 0,
	},
}

// checkReleaseCIScript runs the cases, or the one named, against one copy of the script.
func checkReleaseCIScript(t *testing.T, rep reporter, script, only string) {
	t.Helper()
	rep.Helper()
	for _, c := range releaseCICases {
		if only != "" && c.name != only {
			continue
		}
		g := newGhStub(t)
		g.answers(c.answers...)
		res := g.run(script, c.env, releaseCISha)
		if res.code != c.code {
			rep.Errorf("%s: exit %d, want %d\n%s", c.name, res.code, c.code, res.all())
		}
		for _, want := range c.prints {
			if !strings.Contains(res.all(), want) {
				rep.Errorf("%s: the output does not carry %q\n%s", c.name, want, res.all())
			}
		}
		for _, bad := range c.never {
			if strings.Contains(res.all(), bad) {
				rep.Errorf("%s: the output carries %q, which it must not\n%s", c.name, bad, res.all())
			}
		}
		if c.calls != 0 && g.calls() != c.calls {
			rep.Errorf("%s: gh was asked %d time(s), want %d\n%s", c.name, g.calls(), c.calls, g.log())
		}
	}
}

func TestReleaseCICheckReadsTheRunOfACommit(t *testing.T) {
	t.Parallel()
	checkReleaseCIScript(t, t, filepath.Join(testutil.RepoRoot(t), releaseCIScriptRel), "")
}

// What the script asks gh, and what it refuses before asking anything.
func TestReleaseCICheckAsksForTheWholeShaOfCiYml(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), releaseCIScriptRel)
	g := newGhStub(t)
	wantPass(t, g.run(script, nil, releaseCISha))
	if got := g.log(); !strings.Contains(got, "gh run list --workflow ci.yml --commit "+releaseCISha+" ") {
		t.Errorf("gh was not asked for ci.yml's runs on the whole sha:\n%s", got)
	}
	for _, c := range []struct {
		name string
		env  []string
		arg  string
		want string
	}{
		{"a short sha", nil, releaseCISha[:8], "pass the full 40-character commit sha"},
		{"no sha", nil, "", "usage: scripts/release-ci-check.sh <full commit sha>"},
		{"a branch name", nil, "master", "usage: scripts/release-ci-check.sh <full commit sha>"},
		{"an override that names nothing", []string{"release_over_ci=yes"}, releaseCISha, "is neither a run's id nor 'missing:<the commit's sha>'"},
		{"a bare missing, which names no commit", []string{"release_over_ci=missing"}, releaseCISha, "is neither a run's id nor 'missing:<the commit's sha>'"},
		{"a poll of no seconds", []string{"RELEASE_CI_POLL_SECONDS=0"}, releaseCISha, "RELEASE_CI_POLL_SECONDS is at least 1"},
		{"a wait that is not a number", []string{"RELEASE_CI_WAIT_SECONDS=soon"}, releaseCISha, "are whole numbers of seconds (got 'soon')"},
	} {
		g.answers(ciGreen)
		wantFail(t, g.run(script, c.env, c.arg), c.want)
		if g.calls() != 0 {
			t.Errorf("%s: gh was asked although the script refused its arguments:\n%s", c.name, g.log())
		}
	}
}

// The filter the script hands `gh --jq` picks the newest run of the commit and prints the one line the loop
// reads. gh evaluates it with its built-in jq; this runs the same text through `jq`, where there is one.
func TestReleaseCICheckFilterPicksTheNewestRun(t *testing.T) {
	t.Parallel()
	jq, err := exec.LookPath("jq")
	if err != nil {
		if runtime.GOOS == "linux" && os.Getenv("CI") != "" {
			t.Fatalf("jq is not on PATH: CI's runner has it, and this test is what reads the filter")
		}
		t.Skip("jq is not on PATH")
	}
	script, ok := readText(t, testutil.RepoRoot(t), releaseCIScriptRel)
	if !ok {
		return
	}
	m := regexp.MustCompile(`(?m)^\s+--jq '([^']+)'$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("%s no longer hands gh a --jq filter in single quotes on a line of its own", releaseCIScriptRel)
	}
	for _, c := range []struct{ name, in, want string }{
		{"no run", `[]`, ""},
		{
			"one ended run",
			`[{"databaseId":7,"status":"completed","conclusion":"success","url":"u7"}]`, "7 completed success u7\n",
		},
		{
			"a run that has not ended carries no conclusion",
			`[{"databaseId":7,"status":"in_progress","conclusion":"","url":"u7"}]`, "7 in_progress - u7\n",
		},
		{
			"the newest of several, whatever their order",
			`[{"databaseId":9,"status":"queued","conclusion":null,"url":"u9"},{"databaseId":7,"status":"completed","conclusion":"failure","url":"u7"}]`,
			"9 queued - u9\n",
		},
	} {
		//nolint:gosec // G204: jq with the filter read from the repository's own script.
		cmd := exec.CommandContext(t.Context(), jq, "-r", m[1])
		cmd.Stdin = strings.NewReader(c.in)
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != c.want {
			t.Errorf("%s: the filter printed %q (err %v), want %q", c.name, out, err, c.want)
		}
	}
}

// releaseCIMutations: one edit to a copy of the script, and the case that must then fail.
var releaseCIMutations = []struct {
	name, old, replacement, caughtBy, because string
}{
	{
		"only_a_failure_is_red",
		`if [ "$conclusion" = success ]; then`, `if [ "$conclusion" != failure ]; then`,
		"a_cancelled_run_is_red", "a run that was cancelled or timed out did not pass either",
	},
	{
		"any_override_releases_over_a_red_run",
		`if [ "$override" = "$id" ]; then`, `if [ -n "$override" ]; then`,
		"an_override_for_another_run_does_not", "a variable left in a shell must not carry one release's word to the next",
	},
	{
		"a_run_in_progress_is_read_at_once",
		`if [ "$status" = completed ]; then`, `if [ -n "$status" ]; then`,
		"a_run_in_progress_is_waited_for_and_then_read", "the race the release sequence ran until work item 399",
	},
	{
		"no_run_lets_the_release_go_on",
		"      how_to \"missing:$sha\"\n      exit 1\n", "      how_to \"missing:$sha\"\n      exit 0\n",
		"no_run_stops_it", "a commit CI never saw is not a commit CI passed",
	},
	{
		"a_gh_that_fails_reads_as_no_run",
		`if ! line=$(lookup); then`, `if ! line=$(lookup) && false; then`,
		"a_gh_that_cannot_ask_stops_it", "the override of a missing run would then release over a network fault",
	},
	{
		"a_word_for_one_commit_covers_every_missing_run",
		`if [ "$override" = "missing:$sha" ]; then`, `case $override in missing:*) true ;; *) false ;; esac && if true; then`,
		"missing_for_another_commit_does_not_override", "the word for the commit a release starts from is not a word for its release commit",
	},
	{
		"a_run_that_never_ends_is_overridden_by_its_id",
		`    die "not releasing over a run that has not ended: run the same command again when it has"`,
		`    [ "$override" = "$id" ] && exit 0; die "not releasing over a run that has not ended: run the same command again when it has"`,
		"nothing_overrides_a_run_that_has_not_ended", "the ruling waits for a run in progress in every case",
	},
}

func TestReleaseCICheckCasesFailOnAMutatedScript(t *testing.T) {
	t.Parallel()
	original, ok := readText(t, testutil.RepoRoot(t), releaseCIScriptRel)
	if !ok {
		return
	}
	for i, m := range releaseCIMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(original, m.old) {
				t.Fatalf("%s no longer contains the anchor %q, so this mutation would be vacuous", releaseCIScriptRel, m.old)
			}
			script := filepath.Join(t.TempDir(), "release-ci-check-"+strconv.Itoa(i)+".sh")
			// Mode 0600: the copy is handed to `sh` as an argument, so it is read, never exec'd.
			//nolint:gosec // G703: script is filepath.Join(t.TempDir(), <a name this test made>).
			if err := os.WriteFile(script, []byte(strings.Replace(original, m.old, m.replacement, 1)), 0o600); err != nil {
				t.Fatalf("writing the mutated script: %v", err)
			}
			var rec recorder
			checkReleaseCIScript(t, &rec, script, m.caughtBy)
			if len(rec.msgs) == 0 {
				t.Fatalf("case %s passed on the mutated script, so it does not hold what it says (%s)", m.caughtBy, m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}
