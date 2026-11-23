//go:build !windows

// The two CI steps of scripts/release-prep.sh, and scripts/server-tag.sh's one (work item 399, ADR 0015).
//
// release_ci_check_test.go holds what the shared check does with each answer. This file holds that the two
// release scripts ask it at the right moments and obey it: the plugin release before anything is changed and
// again between the push of the release commit and the tag, the server release before its tag. Both scripts
// run for real, to their last step, in fixture repositories whose `origin` is a bare repository beside them,
// with the stand-in `gh` of that file: a tag in `origin` is a release that went out.
package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// stubGh puts the stand-in `gh` on the fixture's PATH and sets its answers. Its files live in the fixture's
// ignored home, so they never dirty the tree.
func (r *repo) stubGh(answers ...string) {
	r.t.Helper()
	r.writeAbs(filepath.Join(r.stubBin, "gh"), ghStubBody(r.home), 0o700)
	writeGhAnswers(r.t, r.home, answers...)
}

func (r *repo) ghLog() string { r.t.Helper(); return r.read(".home/gh.log") }

// withOrigin gives the fixture a bare `origin` with `master` pushed and tracked, which a real run pulls from
// and pushes to, and returns its path.
func (r *repo) withOrigin() string {
	r.t.Helper()
	origin := filepath.Join(r.t.TempDir(), "origin.git")
	r.git("init", "-q", "--bare", "-b", "master", origin)
	r.git("remote", "add", "origin", origin)
	r.git("push", "-q", "-u", "origin", "master")
	return origin
}

// stubMakeThatPushes is stubMake whose `make push message=…` does what the real chain ends with: stage
// everything, commit under the message and push. A real run needs it, because step 4b reads the run of the
// commit that push made.
func (r *repo) stubMakeThatPushes() {
	r.t.Helper()
	r.stubMake()
	body := r.read(".stub-bin/make") + `if [ "${1:-}" = push ]; then
  git add -A
  git commit -q -m "${2#message=}"
  git push -q origin HEAD
fi
`
	r.writeAbs(filepath.Join(r.stubBin, "make"), body, 0o700)
}

// originTags lists the tags of the bare repository at origin.
func originTags(t *testing.T, origin string) string {
	t.Helper()
	//nolint:gosec // G204: git with a fixed argument list on this test's own bare repository.
	out, err := exec.CommandContext(t.Context(), "git", "--git-dir", origin, "tag", "--list").CombinedOutput()
	if err != nil {
		t.Fatalf("listing origin's tags: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// originTagMessage is the message of an annotated tag of the bare repository at origin, as the release
// workflows' guard reads it from GitHub (work item 416): the override is a line of it.
func originTagMessage(t *testing.T, origin, tag string) string {
	t.Helper()
	//nolint:gosec // G204: git with a fixed argument list on this test's own bare repository.
	out, err := exec.CommandContext(t.Context(), "git", "--git-dir", origin, "tag", "--list", "--format=%(contents)", tag).CombinedOutput()
	if err != nil {
		t.Fatalf("reading the message of origin's tag %s: %v\n%s", tag, err, out)
	}
	return strings.TrimSpace(string(out))
}

// ciEnv keeps a real run from waiting: a run that is not there is missing at once, and a poll is one second.
var ciEnv = []string{"work_item=20", "RELEASE_CI_APPEAR_SECONDS=0", "RELEASE_CI_POLL_SECONDS=1"}

func TestReleasePrepDryRunReadsCIAndGoesOn(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.stubGh(ciRed)
	res := r.run([]string{"DRY_RUN=1", "work_item=20"}, "0.1.0")
	wantPass(t, res,
		"release-ci: ci.yml on "+r.gitOut("rev-parse", "HEAD")+": failure (run 7, ",
		"DRY_RUN=1: a real run waits for a run in progress, and stops here unless the run is green, with nothing changed",
		"0. read ci.yml's run on",
		"3. goreleaser reproduces",
		"4b. wait for ci.yml's run on the release commit, and stop over a red or a missing one",
		"5. git tag -a v0.1.0",
	)
}

func TestReleasePrepRefusesATaggedVersionBeforeAnythingChanges(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.git("tag", "v0.1.0")
	wantFail(t, r.run([]string{"DRY_RUN=1", "work_item=20"}, "0.1.0"), "the tag v0.1.0 exists already")
	if got := r.read("plugin/bin/VERSION"); got != "0.0.0\n" {
		t.Fatalf("the refusal came after the pins were bumped: VERSION = %q", got)
	}
}

func TestReleasePrepStopsBeforeAnythingChangesWhenItsStartIsRed(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	origin := r.withOrigin()
	r.stubMakeThatPushes()
	r.stubGh(ciRed)
	start := r.gitOut("rev-parse", "HEAD")
	res := r.run(ciEnv, "0.1.0")
	wantFail(t, res,
		"ci.yml on "+start+": failure (run 7, ",
		"release_over_ci=7",
		"ci.yml is not green on "+start+", the commit this release starts from: nothing was changed",
	)
	if got := r.read("plugin/bin/VERSION"); got != "0.0.0\n" {
		t.Fatalf("the pins were bumped on a red start: VERSION = %q", got)
	}
	if r.exists("make.log") && strings.Contains(r.read("make.log"), "cross") {
		t.Fatalf("the build ran on a red start:\n%s", r.read("make.log"))
	}
	if got := originTags(t, origin); got != "" {
		t.Fatalf("a tag reached origin: %q", got)
	}
}

func TestReleasePrepTagsOnlyAfterTheReleaseCommitsRunIsGreen(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	origin := r.withOrigin()
	r.stubMakeThatPushes()
	start := r.gitOut("rev-parse", "HEAD")
	r.stubGh(ciGreen, ciRunning, ciGreen) // the start; then the release commit, in progress and then green
	res := r.run(ciEnv, "0.1.0")
	wantPass(t, res,
		"0. read ci.yml's run on "+start,
		"4. committed plugin/bin/checksums.txt",
		"run 7 is in_progress; waiting for it to end",
		"4b. read ci.yml's run on the release commit",
		"5. pushed v0.1.0",
	)
	release := r.gitOut("rev-parse", "HEAD")
	if release == start {
		t.Fatalf("no release commit was made")
	}
	log := r.ghLog()
	if !strings.Contains(log, "--commit "+start+" ") || strings.Count(log, "--commit "+release+" ") != 2 {
		t.Fatalf("gh was not asked once for the start %s and twice for the release commit %s:\n%s", start, release, log)
	}
	if got := originTags(t, origin); got != "v0.1.0" {
		t.Fatalf("origin's tags = %q, want v0.1.0", got)
	}
	if got := r.gitOut("rev-parse", "v0.1.0^{commit}"); got != release {
		t.Fatalf("v0.1.0 points at %s, want the release commit %s", got, release)
	}
	// No override was given, so the tag's message carries none for the workflow's guard to read.
	if got := originTagMessage(t, origin, "v0.1.0"); got != "v0.1.0" {
		t.Fatalf("the tag's message = %q, want v0.1.0 and nothing else", got)
	}
}

// The stop of the ruling, and both ways on from it: the same command again, once the run is green, and the
// same command with the owner's override.
func TestReleasePrepStopsOverARedReleaseCommitAndGoesOnFromThere(t *testing.T) {
	t.Parallel()
	for _, way := range []struct {
		name    string
		answers []string
		env     []string
		prints  []string
	}{
		{"again once the run is green", []string{ciGreen}, nil, []string{": success (run 7, "}},
		{"with the override that names the run", []string{ciRed}, []string{"release_over_ci=7"}, []string{"releasing over the red run 7"}},
	} {
		t.Run(way.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t)
			origin := r.withOrigin()
			r.stubMakeThatPushes()
			r.stubGh(ciGreen, ciRed) // the start is green, the release commit red
			res := r.run(ciEnv, "0.1.0")
			release := r.gitOut("rev-parse", "HEAD")
			wantFail(t, res,
				"4. committed plugin/bin/checksums.txt",
				"ci.yml on "+release+": failure (run 7, ",
				"v0.1.0 is NOT tagged, and its release commit "+release+" is pushed",
				"it goes on from here",
			)
			if got := originTags(t, origin); got != "" {
				t.Fatalf("a tag reached origin over a red release commit: %q", got)
			}
			if got := r.gitOut("rev-parse", "origin/master"); got != release {
				t.Fatalf("the release commit is not origin's head: %s, want %s", got, release)
			}

			r.stubGh(way.answers...)
			res = r.run(append(append([]string{}, ciEnv...), way.env...), "0.1.0")
			wantPass(t, res, append([]string{
				"4. nothing to commit: HEAD pins 0.1.0 already, as origin/master does; going on from its CI run",
				"4b. read ci.yml's run on the release commit " + release,
				"5. pushed v0.1.0",
			}, way.prints...)...)
			if got := r.gitOut("rev-parse", "HEAD"); got != release {
				t.Fatalf("the second run made another commit: HEAD is %s, want %s", got, release)
			}
			if got := originTags(t, origin); got != "v0.1.0" {
				t.Fatalf("origin's tags = %q, want v0.1.0", got)
			}
			// The override reaches release.yml in the tag's message, a line of its own (work item 416).
			want := strings.Join(append([]string{"v0.1.0"}, way.env...), "\n\n")
			if got := originTagMessage(t, origin, "v0.1.0"); got != want {
				t.Fatalf("the tag's message = %q, want %q", got, want)
			}
		})
	}
}

// A word for the commit the release starts from is not a word for its release commit: the override names one
// commit, and step 4b reads another.
func TestReleasePrepsOverrideForItsStartDoesNotCoverTheReleaseCommit(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	origin := r.withOrigin()
	r.stubMakeThatPushes()
	r.stubGh("-") // no run, of either commit
	start := r.gitOut("rev-parse", "HEAD")
	res := r.run(append(append([]string{}, ciEnv...), "release_over_ci=missing:"+start), "0.1.0")
	release := r.gitOut("rev-parse", "HEAD")
	wantFail(t, res,
		"releasing over a missing run: release_over_ci=missing:"+start+" was given",
		"4. committed plugin/bin/checksums.txt",
		"does not name this commit's missing run",
		"release_over_ci=missing:"+release,
		"v0.1.0 is NOT tagged",
	)
	if got := originTags(t, origin); got != "" {
		t.Fatalf("a tag reached origin on the start's word: %q", got)
	}
}

// What only origin can say is asked before anything is changed: a tag another machine pushed, and a head that
// was never pushed, whose missing run would otherwise read as something to override.
func TestReleasePrepAsksOriginBeforeItReadsCI(t *testing.T) {
	t.Parallel()
	t.Run("a tag only origin has", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		origin := r.withOrigin()
		//nolint:gosec // G204: git with a fixed argument list on this test's own bare repository.
		if out, err := exec.CommandContext(t.Context(), "git", "--git-dir", origin, "tag", "v0.1.0", "master").CombinedOutput(); err != nil {
			t.Fatalf("tagging in origin: %v\n%s", err, out)
		}
		wantFail(t, r.run(ciEnv, "0.1.0"), "the tag v0.1.0 exists on origin")
		if log := r.ghLog(); log != "" {
			t.Errorf("gh was asked although the version is tagged on origin:\n%s", log)
		}
		if got := r.read("plugin/bin/VERSION"); got != "0.0.0\n" {
			t.Fatalf("the pins were bumped: VERSION = %q", got)
		}
	})
	t.Run("a head that was never pushed", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.withOrigin()
		r.write("local.txt", "x\n", 0o600)
		r.git("add", "local.txt")
		r.git("commit", "-qm", "a commit origin does not have")
		wantFail(t, r.run(ciEnv, "0.1.0"), "HEAD is not origin/master's head: push it first")
		if log := r.ghLog(); log != "" {
			t.Errorf("gh was asked about a commit origin does not have:\n%s", log)
		}
	})
}

func TestMakePassesTheOverrideToBothReleaseScripts(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"release", []string{"release", "version=9.9.9", "work_item=20", "release_over_ci=45; touch pwned"}, "release_over_ci='45; touch pwned' work_item='20' card='' scripts/release-prep.sh 9.9.9"},
		{"release, with none", []string{"release", "version=9.9.9", "work_item=20"}, "release_over_ci='' work_item='20' card='' scripts/release-prep.sh 9.9.9"},
		{"server-tag", []string{"server-tag", "release_over_ci=missing"}, "release_over_ci='missing' scripts/server-tag.sh"},
	} {
		//nolint:gosec // G204: make on the repository's own Makefile with this test's literal arguments
		cmd := exec.CommandContext(t.Context(), "make", append([]string{"-C", root, "-n"}, c.args...)...)
		// make takes a variable from the environment too, and an outer `make test` exports its flags.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), c.want) {
			t.Errorf("%s: make -n %s: err %v, want the recipe %q in\n%s", c.name, strings.Join(c.args, " "), err, c.want, out)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// scripts/server-tag.sh

// serverRepo is a fixture the server's tag script can run in to its end: the three places that name the
// version, a bare `origin`, and copies of the script and of the check it calls (the script works from its own
// directory's parent, so it has to live in the fixture).
type serverRepo struct {
	*repo
	origin string
}

func newServerRepo(t *testing.T) *serverRepo {
	t.Helper()
	root := t.TempDir()
	r := &repo{t: t, root: root, home: filepath.Join(root, ".home"), stubBin: filepath.Join(root, ".stub-bin")}
	r.mkdir(r.home)
	r.mkdir(r.stubBin)
	r.write(".gitignore", "/.home/\n/.stub-bin/\n", 0o600)
	r.write("server/VERSION", "1.2.3\n", 0o600)
	r.write("server/Cargo.toml", "[workspace.package]\nversion = \"1.2.3\"\n", 0o600)
	r.write("server/CHANGELOG.md", "# Changelog\n\n## [1.2.3]\n\n- The fixture.\n", 0o600)
	for _, name := range []string{"server-tag.sh", "release-ci-check.sh"} {
		//nolint:gosec // G304: the repository's own script.
		body, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "scripts", name))
		if err != nil {
			t.Fatalf("reading scripts/%s: %v", name, err)
		}
		r.write(filepath.Join("scripts", name), string(body), 0o600)
	}
	r.stubGh(ciGreen)
	r.git("init", "-q", "-b", "master")
	r.git("add", "-A")
	r.git("commit", "-qm", "fixture")
	return &serverRepo{repo: r, origin: r.withOrigin()}
}

func (s *serverRepo) tag(extraEnv ...string) result {
	s.t.Helper()
	env := s.env(append([]string{"RELEASE_CI_APPEAR_SECONDS=0", "RELEASE_CI_POLL_SECONDS=1"}, extraEnv...)...)
	return runScriptAt(s.t, filepath.Join(s.root, "scripts", "server-tag.sh"), s.root, env)
}

func TestServerTagTagsOnlyOverAGreenRun(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		answers []string
		env     []string
		tagged  bool
		prints  []string
	}{
		{"a green run", []string{ciGreen}, nil, true, []string{": success (run 7, ", "server-tag: pushed server-v1.2.3"}},
		{"a run in progress is waited for", []string{ciRunning, ciGreen}, nil, true, []string{"run 7 is in_progress; waiting for it to end", "pushed server-v1.2.3"}},
		{"a red run", []string{ciRed}, nil, false, []string{": failure (run 7, ", "release_over_ci=7", "server-v1.2.3 is NOT tagged: ci.yml is not green on"}},
		{"a red run and the override", []string{ciRed}, []string{"release_over_ci=7"}, true, []string{"releasing over the red run 7", "pushed server-v1.2.3"}},
		{"no run", []string{"-"}, nil, false, []string{": no run", "release_over_ci=missing:", "server-v1.2.3 is NOT tagged"}},
		{"no run and the override that names the head", []string{"-"}, []string{"release_over_ci=missing:HEAD"}, true, []string{"releasing over a missing run", "pushed server-v1.2.3"}},
		{"no run and an override for another commit", []string{"-"}, []string{"release_over_ci=missing:" + strings.Repeat("f", 40)}, false, []string{"does not name this commit's missing run", "server-v1.2.3 is NOT tagged"}},
		{"a dry run over a green run", []string{ciGreen}, []string{"DRY_RUN=1"}, false, []string{": success (run 7, ", "DRY_RUN=1: every check passed; a real run would now tag"}},
		{"a dry run over a red run", []string{ciRed}, []string{"DRY_RUN=true"}, false, []string{"server-v1.2.3 is NOT tagged"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newServerRepo(t)
			s.stubGh(c.answers...)
			head := s.gitOut("rev-parse", "HEAD")
			env := make([]string, len(c.env))
			for i, kv := range c.env {
				env[i] = strings.Replace(kv, "missing:HEAD", "missing:"+head, 1)
			}
			res := s.tag(env...)
			for _, want := range c.prints {
				if !strings.Contains(res.all(), want) {
					t.Errorf("the output does not carry %q\n%s", want, res.all())
				}
			}
			if !strings.Contains(s.ghLog(), "--commit "+head+" ") {
				t.Errorf("gh was not asked for the head %s:\n%s", head, s.ghLog())
			}
			got := originTags(t, s.origin)
			if c.tagged && (got != "server-v1.2.3" || res.code != 0) {
				t.Errorf("exit %d and origin's tags %q, want 0 and server-v1.2.3\n%s", res.code, got, res.all())
			}
			if c.tagged && s.gitOut("rev-parse", "server-v1.2.3^{commit}") != head {
				t.Errorf("the tag does not point at the head whose run was read, %s", head)
			}
			if c.tagged {
				// The override reaches server-release.yml in the tag's message, a line of its own (work item 416).
				paragraphs := []string{"Brigade server 1.2.3"}
				for _, kv := range env {
					if strings.HasPrefix(kv, "release_over_ci=") {
						paragraphs = append(paragraphs, kv)
					}
				}
				want := strings.Join(paragraphs, "\n\n")
				if got := originTagMessage(t, s.origin, "server-v1.2.3"); got != want {
					t.Errorf("the tag's message = %q, want %q", got, want)
				}
			}
			if !c.tagged && got != "" {
				t.Errorf("a tag reached origin: %q\n%s", got, res.all())
			}
			if !c.tagged && s.gitOut("tag", "--list") != "" {
				t.Errorf("a local tag was made: %q", s.gitOut("tag", "--list"))
			}
		})
	}
}

// The refusals the script had before work item 399 come before the CI check, which is the one that waits.
func TestServerTagRefusesBeforeItAsksAboutCI(t *testing.T) {
	t.Parallel()
	s := newServerRepo(t)
	s.write("stray.txt", "x\n", 0o600)
	wantFail(t, s.tag(), "the working tree has changes")
	s.remove("stray.txt")
	s.git("tag", "server-v1.2.3")
	wantFail(t, s.tag(), "the tag server-v1.2.3 exists already")
	s.git("tag", "-d", "server-v1.2.3")
	// Work that waits under `## [Unreleased]` is work the release would carry and its text would not say.
	s.write("server/CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n- A fix.\n\n## [1.2.3]\n\n- The fixture.\n", 0o600)
	s.git("commit", "-qam", "work that waits for a release")
	s.git("push", "-q", "origin", "master")
	wantFail(t, s.tag(), "still has a '## [Unreleased]' section")
	if log := s.ghLog(); log != "" {
		t.Errorf("gh was asked although an earlier check refused:\n%s", log)
	}
}

// ---------------------------------------------------------------------------------------------------------
// the tests above, shown able to fail
//
// Each release script is copied with its check taken out, and the copy is run over a red run: it must put a
// tag in `origin`, which is what the tests above forbid. A copy that did not would mean those tests look at
// nothing.

func TestAReleaseScriptThatSkipsTheCheckTags(t *testing.T) {
	t.Parallel()
	t.Run("release-prep.sh without step 4b", func(t *testing.T) {
		t.Parallel()
		original, ok := readText(t, testutil.RepoRoot(t), "scripts/release-prep.sh")
		if !ok {
			return
		}
		anchor := `ci_check "$release_commit" 180 || die`
		if !strings.Contains(original, anchor) {
			t.Fatalf("scripts/release-prep.sh no longer contains %q, so this mutation would be vacuous", anchor)
		}
		dir := t.TempDir()
		for name, body := range map[string]string{
			"release-prep.sh": strings.Replace(original, anchor, `ci_check "$release_commit" 180 || echo`, 1),
		} {
			// Mode 0600: handed to `sh` as an argument.
			//nolint:gosec // G703: under t.TempDir().
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatalf("writing the mutated %s: %v", name, err)
			}
		}
		check, ok := readText(t, testutil.RepoRoot(t), releaseCIScriptRel)
		if !ok {
			return
		}
		//nolint:gosec // G703: under t.TempDir().
		if err := os.WriteFile(filepath.Join(dir, "release-ci-check.sh"), []byte(check), 0o600); err != nil {
			t.Fatalf("copying the check: %v", err)
		}
		r := newRepo(t)
		origin := r.withOrigin()
		r.stubMakeThatPushes()
		r.stubGh(ciGreen, ciRed)
		res := runScriptAt(t, filepath.Join(dir, "release-prep.sh"), r.root, r.env(ciEnv...), "0.1.0")
		if got := originTags(t, origin); got != "v0.1.0" {
			t.Fatalf("the copy without step 4b did not tag over a red release commit (origin's tags %q), so the "+
				"test that forbids it looks at nothing\n%s", got, res.all())
		}
	})
	t.Run("server-tag.sh without its check", func(t *testing.T) {
		t.Parallel()
		s := newServerRepo(t)
		body := s.read("scripts/server-tag.sh")
		anchor := `sh scripts/release-ci-check.sh "$head" ||`
		if !strings.Contains(body, anchor) {
			t.Fatalf("scripts/server-tag.sh no longer contains %q, so this mutation would be vacuous", anchor)
		}
		s.write("scripts/server-tag.sh", strings.Replace(body, anchor, `true "$head" ||`, 1), 0o600)
		s.git("commit", "-qam", "the script without its check")
		s.git("push", "-q", "origin", "master")
		s.stubGh(ciRed)
		res := s.tag()
		if got := originTags(t, s.origin); got != "server-v1.2.3" {
			t.Fatalf("the copy without the check did not tag over a red run (origin's tags %q), so the test that "+
				"forbids it looks at nothing\n%s", got, res.all())
		}
	})
}
