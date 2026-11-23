//go:build !windows

// scripts/release-pin.sh, the last step of a plugin release (work item 424, ADR 0030): once a release is
// published, one commit on `master` points the Codex CLI and the Grok Build package at the tag's commit.
//
// The script runs for real in the release fixture (release_prep_test.go), whose `origin` is a bare repository
// beside it, with the stand-in `gh` of release_ci_check_test.go answering whether the release is published and
// how release.yml's run stands, a `make` whose `push` commits and pushes, and a `go` that does what
// `brigade-packages pin <commit>` does to the two files (internal/packagecheck's tests hold the real writer).
// What is held: the pin moves only for a published release, a stop leaves both files as they were, each way a
// run can stop is finished by the same command again, and `make release` runs the step after its tag.
package ci_test

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// pinnedBefore is the commit the fixture's two marketplace files name before the step runs.
var pinnedBefore = strings.Repeat("0", 40)

const (
	fixtureCodexMarket = ".agents/plugins/marketplace.json"
	fixtureGrokMarket  = ".grok-plugin/marketplace.json"
	// What the stand-in `gh` answers `gh release view --jq .isDraft` with.
	releasePublished = "false"
	releaseIsADraft  = "true"
	pointerMessage   = "20: Point the Codex CLI and Grok Build packages at 0.1.0"
)

// pinFiles adds the two marketplace files to the fixture, committed, and the `go` that writes a commit into
// them.
func (r *repo) pinFiles() {
	r.t.Helper()
	for _, rel := range []string{fixtureCodexMarket, fixtureGrokMarket} {
		r.write(rel, "{\n  \"source\": {\n    \"sha\": \""+pinnedBefore+"\"\n  }\n}\n", 0o600)
	}
	r.git("add", "-A")
	r.git("commit", "-qm", "the two marketplace files")
	r.writeAbs(filepath.Join(r.stubBin, "go"), `#!/bin/sh
if [ "${1:-}" = run ] && [ "${2:-}" = ./cmd/brigade-packages ] && [ "${3:-}" = pin ] && [ -n "${4:-}" ]; then
  for f in `+fixtureCodexMarket+` `+fixtureGrokMarket+`; do
    sed -i.bak -E "s/(\"sha\": \")[0-9a-f]{40}/\1$4/" "$f" && rm -f "$f.bak"
  done
  exit 0
fi
printf '%s\n' 'go`+goLine+`'
`, 0o700)
}

// released makes the release commit of 0.1.0 (the pin of plugin/bin/VERSION), pushes it, tags it v0.1.0 with an
// annotated tag as `make release` does, pushes the tag and returns the commit.
func (r *repo) released() string {
	r.t.Helper()
	r.write("plugin/bin/VERSION", "0.1.0\n", 0o600)
	r.git("commit", "-qam", "20: Release 0.1.0")
	r.git("push", "-q", "origin", "master")
	r.git("tag", "-a", "-m", "v0.1.0", "v0.1.0")
	r.git("push", "-q", "origin", "v0.1.0")
	return r.gitOut("rev-parse", "HEAD")
}

// pinFixture is a fixture ready for the step: the two files, an origin, a `make` that pushes and a published
// v0.1.0 on its head.
func pinFixture(t *testing.T) (r *repo, origin, release string) {
	t.Helper()
	r = newRepo(t)
	r.pinFiles()
	origin = r.withOrigin()
	r.stubMakeThatPushes()
	release = r.released()
	r.stubGh(releasePublished)
	return r, origin, release
}

// runPin executes scripts/release-pin.sh, the real one, inside the fixture.
func (r *repo) runPin(extraEnv []string, args ...string) result {
	r.t.Helper()
	script := filepath.Join(testutil.RepoRoot(r.t), "scripts", "release-pin.sh")
	//nolint:gosec // G204: a fixed script path under the repository and this test's own fixture arguments
	cmd := exec.CommandContext(r.t.Context(), "sh", append([]string{script}, args...)...)
	cmd.Dir = r.root
	cmd.Env = r.env(extraEnv...)
	cmd.Stdin = strings.NewReader("")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := result{stdout: out.String(), stderr: errb.String()}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			r.t.Fatalf("running %s: %v", script, err)
		}
		res.code = ee.ExitCode()
	}
	return res
}

// pinEnv keeps a run from waiting: a poll is one second.
var pinEnv = []string{"work_item=20", "RELEASE_PIN_POLL_SECONDS=1"}

// originHead is the head of master in the bare repository at origin, and its message.
func originHead(t *testing.T, origin string) (sha, message string) {
	t.Helper()
	//nolint:gosec // G204: git with a fixed argument list on this test's own bare repository.
	out, err := exec.CommandContext(t.Context(), "git", "--git-dir", origin, "log", "-1", "--format=%H %s", "master").CombinedOutput()
	if err != nil {
		t.Fatalf("reading origin's head: %v\n%s", err, out)
	}
	sha, message, _ = strings.Cut(strings.TrimSpace(string(out)), " ")
	return sha, message
}

// originFile is a file of master's head in the bare repository at origin.
func originFile(t *testing.T, origin, rel string) string {
	t.Helper()
	//nolint:gosec // G204: git with a fixed argument list on this test's own bare repository.
	out, err := exec.CommandContext(t.Context(), "git", "--git-dir", origin, "show", "master:"+rel).CombinedOutput()
	if err != nil {
		t.Fatalf("reading origin's %s: %v\n%s", rel, err, out)
	}
	return string(out)
}

// wantPinnedAt holds that origin's two files name the commit, in a commit of the pointer's message whose
// parent is the release commit when the release is the fixture's head.
func wantPinnedAt(t *testing.T, origin, commit string) {
	t.Helper()
	for _, rel := range []string{fixtureCodexMarket, fixtureGrokMarket} {
		if got := originFile(t, origin, rel); !strings.Contains(got, `"sha": "`+commit+`"`) {
			t.Fatalf("origin's %s does not name %s:\n%s", rel, commit, got)
		}
	}
	if _, message := originHead(t, origin); message != pointerMessage {
		t.Fatalf("origin's head is %q, want %q", message, pointerMessage)
	}
}

// wantNotMoved holds that nothing reached origin and that the fixture's two files are as they were.
func wantNotMoved(t *testing.T, r *repo, origin, head string) {
	t.Helper()
	if sha, message := originHead(t, origin); sha != head {
		t.Fatalf("origin's head moved to %s (%q)", sha, message)
	}
	for _, rel := range []string{fixtureCodexMarket, fixtureGrokMarket} {
		if got := r.read(rel); !strings.Contains(got, pinnedBefore) {
			t.Fatalf("%s was changed:\n%s", rel, got)
		}
	}
}

func TestReleasePinMovesThePinOnceTheReleaseIsPublished(t *testing.T) {
	t.Parallel()

	t.Run("a published release: one commit on master names its commit in both files", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		res := r.runPin(pinEnv, "0.1.0")
		wantPass(t, res, "1. v0.1.0 is published", "2. wrote "+release, "3. pushed: the Codex CLI and Grok Build packages are at v0.1.0")
		wantPinnedAt(t, origin, release)
		if got := r.gitOut("rev-parse", "v0.1.0^{commit}"); got != release {
			t.Fatalf("the tag moved to %s", got)
		}
		if got := r.gitOut("rev-parse", "HEAD~1"); got != release {
			t.Fatalf("the pointer commit's parent is %s, want the release commit %s", got, release)
		}

		// The same command again: said, nothing asked of GitHub, nothing committed.
		head, _ := originHead(t, origin)
		r.stubGh(releaseIsADraft)
		again := r.runPin(pinEnv, "0.1.0")
		wantPass(t, again, "origin/master points the Codex CLI and Grok Build packages at v0.1.0 ("+release+") already")
		if sha, _ := originHead(t, origin); sha != head || r.ghLog() != "" {
			t.Fatalf("the second run moved origin to %s or asked gh:\n%s", sha, r.ghLog())
		}
	})

	t.Run("a draft is waited for, with release.yml still running", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		r.stubGh(releaseIsADraft, ciRunning, "-", ciRunning, releasePublished)
		res := r.runPin(pinEnv, "0.1.0")
		wantPass(t, res, "waiting up to 900s for release.yml to publish v0.1.0", "1. v0.1.0 is published")
		wantPinnedAt(t, origin, release)
		if log := r.ghLog(); !strings.Contains(log, "gh release view v0.1.0 ") || !strings.Contains(log, "--workflow release.yml --commit "+release+" ") {
			t.Fatalf("gh was not asked for the release and for release.yml's run on its commit:\n%s", log)
		}
	})

	t.Run("a release.yml that failed stops the wait at once, with nothing moved", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		r.stubGh("-", ciRed)
		res := r.runPin(pinEnv, "0.1.0")
		wantFail(t, res,
			"release.yml ended in failure on v0.1.0 (run 7, ",
			"the Codex CLI and Grok Build packages stay at the release before",
			"make release-pin version=0.1.0 work_item=20",
		)
		wantNotMoved(t, r, origin, release)
	})

	t.Run("a release that is never published stops at the limit, with nothing moved", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		r.stubGh(releaseIsADraft, "-")
		res := r.runPin(append([]string{"RELEASE_PIN_WAIT_SECONDS=2"}, pinEnv...), "0.1.0")
		wantFail(t, res, "v0.1.0 is not published after 2s", "stay at the release before")
		wantNotMoved(t, r, origin, release)
	})

	t.Run("a gh that cannot ask stops at once with its words, and nothing is moved", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		r.stubGh("!fail")
		res := r.runPin(pinEnv, "0.1.0")
		wantFail(t, res, "gh could not read the release v0.1.0 (gh: could not connect to github.test)", "stay at the release before")
		wantNotMoved(t, r, origin, release)
		if strings.Contains(res.all(), "waiting up to") {
			t.Fatalf("the script waited for a gh that cannot ask:\n%s", res.all())
		}
	})

	t.Run("a change that came into the tree during the wait is not swept into the pointer commit", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		// The tree is clean when the script starts; the stray file appears while it works, as another
		// session's edit would while it waits for the publication.
		r.writeAbs(filepath.Join(r.stubBin, "go"), strings.Replace(r.read(".stub-bin/go"), "  exit 0\n", "  echo x > stray.txt\n  exit 0\n", 1), 0o700)
		res := r.runPin(pinEnv, "0.1.0")
		wantFail(t, res, "stray.txt", "tree not clean", "The two marketplace files are written and not committed")
		if sha, _ := originHead(t, origin); sha != release {
			t.Fatalf("origin moved to %s", sha)
		}
		if r.exists("make.log") && strings.Contains(r.read("make.log"), "push") {
			t.Fatalf("make push ran over a tree that held another change:\n%s", r.read("make.log"))
		}
	})

	t.Run("a pre-release moves nothing and asks nothing", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		res := r.runPin(pinEnv, "0.2.0-rc1")
		wantPass(t, res, "v0.2.0-rc1 is a pre-release: the Codex CLI and Grok Build packages stay at the last release")
		wantNotMoved(t, r, origin, release)
		if r.ghLog() != "" {
			t.Fatalf("gh was asked:\n%s", r.ghLog())
		}
	})
}

func TestReleasePinFinishesARunThatStopped(t *testing.T) {
	t.Parallel()

	t.Run("a push chain that failed before it staged: the two files are written, and the next run commits them", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		good := r.read(".stub-bin/make")
		r.writeAbs(filepath.Join(r.stubBin, "make"), "#!/bin/sh\necho 'make: lint failed' >&2\nexit 2\n", 0o700)
		wantFail(t, r.runPin(pinEnv, "0.1.0"), "2. wrote "+release)
		if sha, _ := originHead(t, origin); sha != release {
			t.Fatalf("origin moved to %s", sha)
		}
		r.writeAbs(filepath.Join(r.stubBin, "make"), good, 0o700)
		wantPass(t, r.runPin(pinEnv, "0.1.0"), "3. pushed")
		wantPinnedAt(t, origin, release)
	})

	t.Run("a push that origin refused: the commit is made, and the next run pulls and pushes it", func(t *testing.T) {
		t.Parallel()
		r, origin, release := pinFixture(t)
		good := r.read(".stub-bin/make")
		r.writeAbs(filepath.Join(r.stubBin, "make"), "#!/bin/sh\ngit add -A\ngit commit -q -m \"${2#message=}\"\necho 'push rejected' >&2\nexit 1\n", 0o700)
		wantFail(t, r.runPin(pinEnv, "0.1.0"), "2. wrote "+release)
		if sha, _ := originHead(t, origin); sha != release {
			t.Fatalf("origin moved to %s", sha)
		}
		r.writeAbs(filepath.Join(r.stubBin, "make"), good, 0o700)
		wantPass(t, r.runPin(pinEnv, "0.1.0"), "3. pushed")
		wantPinnedAt(t, origin, release)
		if log := r.read("make.log"); !strings.Contains(log, "make pull\n") || strings.Contains(log, "make push") {
			t.Fatalf("the second run did not pull and push by git alone:\n%s", log)
		}
	})
}

func TestReleasePinRefusals(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		prepare func(r *repo)
		env     []string
		version string
		want    string
	}{
		{"no version", nil, pinEnv, "", "usage: make release-pin version=X.Y.Z work_item=<n>"},
		{"a version with a leading v", nil, pinEnv, "v0.1.0", "without the leading 'v'"},
		{"no work item", nil, []string{"RELEASE_PIN_POLL_SECONDS=1"}, "0.1.0", "work_item is the number of the release's work item"},
		{"a work item that is not a number", nil, []string{"work_item=x"}, "0.1.0", "work_item must be a work item's number"},
		{"a version that is not tagged", nil, pinEnv, "0.3.0", "there is no tag v0.3.0"},
		{"an older release than the one master pins", func(r *repo) {
			r.git("tag", "v0.0.9", "HEAD~1")
			r.git("push", "-q", "origin", "v0.0.9")
		}, pinEnv, "0.0.9", "origin/master's plugin/bin/VERSION pins '0.1.0', not 0.0.9"},
		{"another branch", func(r *repo) { r.git("checkout", "-q", "-b", "side") }, pinEnv, "0.1.0", "the pin is moved on master, not on 'side'"},
		{"a change that is not the step's own", func(r *repo) { r.write("stray.txt", "x\n", 0o600) }, pinEnv, "0.1.0", "tree not clean"},
		{"a tree that is not the repository's root", func(r *repo) { r.remove(fixtureGrokMarket) }, pinEnv, "0.1.0", "run this from the repository root"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, origin, release := pinFixture(t)
			if c.prepare != nil {
				c.prepare(r)
			}
			var args []string
			if c.version != "" {
				args = []string{c.version}
			}
			wantFail(t, r.runPin(c.env, args...), c.want)
			if sha, _ := originHead(t, origin); sha != release || r.ghLog() != "" {
				t.Fatalf("the refusal came after origin moved (%s) or gh was asked:\n%s", sha, r.ghLog())
			}
		})
	}
}

// `make release` runs the step after its tag, and a stop in it leaves the release out and says what to run.
func TestReleasePrepPointsThePackagesAtTheReleaseAfterItsTag(t *testing.T) {
	t.Parallel()

	t.Run("a release that is published: tag on the release commit, then the pointer commit", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.pinFiles()
		origin := r.withOrigin()
		r.stubMakeThatPushes()
		r.stubGh(ciGreen, ciGreen, releasePublished) // the start, the release commit, the release
		res := r.run(append([]string{"RELEASE_PIN_POLL_SECONDS=1"}, ciEnv...), "0.1.0")
		wantPass(t, res, "5. pushed v0.1.0", "release-pin: 1. v0.1.0 is published", "6. pointed the Codex CLI and Grok Build packages at v0.1.0")
		release := r.gitOut("rev-parse", "v0.1.0^{commit}")
		wantPinnedAt(t, origin, release)
		if got := r.gitOut("rev-parse", "HEAD~1"); got != release {
			t.Fatalf("the pointer commit's parent is %s, want the tagged release commit %s", got, release)
		}
		if got := r.gitOut("show", release+":"+fixtureCodexMarket); !strings.Contains(got, pinnedBefore) {
			t.Fatalf("the release commit itself moved the pin:\n%s", got)
		}
		if got := originTagMessage(t, origin, "v0.1.0"); got != "v0.1.0" {
			t.Fatalf("the tag's message = %q, want v0.1.0 and nothing else", got)
		}
	})

	t.Run("a release.yml that failed: the tag is out, the pin is not moved, and the script says so", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.pinFiles()
		origin := r.withOrigin()
		r.stubMakeThatPushes()
		r.stubGh(ciGreen, ciGreen, "-", ciRed)
		res := r.run(append([]string{"RELEASE_PIN_POLL_SECONDS=1"}, ciEnv...), "0.1.0")
		wantFail(t, res,
			"5. pushed v0.1.0",
			"release.yml ended in failure on v0.1.0",
			"v0.1.0 is tagged, and the Codex CLI and Grok Build packages are NOT moved to it: their members stay on the release before",
			"make release-pin version=0.1.0 work_item=20",
		)
		if got := originTags(t, origin); got != "v0.1.0" {
			t.Fatalf("origin's tags = %q, want v0.1.0", got)
		}
		if got := originFile(t, origin, fixtureCodexMarket); !strings.Contains(got, pinnedBefore) {
			t.Fatalf("origin's pin moved:\n%s", got)
		}
	})

	t.Run("a pre-release is tagged and its step 6 says that nothing moves", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.pinFiles()
		r.write("CHANGELOG.md", r.read("CHANGELOG.md")+"\n## [0.2.0-rc1] — 2026-01-02\n\n- A candidate.\n", 0o600)
		r.git("commit", "-qam", "the candidate's section")
		origin := r.withOrigin()
		r.stubMakeThatPushes()
		r.stubGh(ciGreen, ciGreen)
		res := r.run(ciEnv, "0.2.0-rc1")
		wantPass(t, res, "5. pushed v0.2.0-rc1", "6. v0.2.0-rc1 is a pre-release: the Codex CLI and Grok Build packages stay at the last release")
		if strings.Contains(res.all(), "6. pointed") || strings.Contains(res.all(), "release-pin:") {
			t.Fatalf("a pre-release ran the step or said it moved the pin:\n%s", res.all())
		}
		if got := originFile(t, origin, fixtureCodexMarket); !strings.Contains(got, pinnedBefore) {
			t.Fatalf("origin's pin moved:\n%s", got)
		}
	})

	t.Run("the dry run names the step and leaves the two files", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.pinFiles()
		res := r.run([]string{"DRY_RUN=1", "work_item=20"}, "0.1.0")
		wantPass(t, res, "5. git tag -a v0.1.0", `6. work_item=20 scripts/release-pin.sh 0.1.0`, `make push message="`+pointerMessage+`"`)
		if got := r.read(fixtureCodexMarket); !strings.Contains(got, pinnedBefore) {
			t.Fatalf("the dry run changed the pin:\n%s", got)
		}
	})

	t.Run("a fixture without the two files has no step 6", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		res := r.run([]string{"DRY_RUN=1", "work_item=20"}, "0.1.0")
		wantPass(t, res, "5. git tag -a v0.1.0")
		if strings.Contains(res.all(), "release-pin") {
			t.Fatalf("a tree with no marketplace file was given a step 6:\n%s", res.all())
		}
	})
}
