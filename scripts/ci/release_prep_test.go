// This file is the test of scripts/release-prep.sh, the release sequence of plan 7.7. It lives beside
// checks_test.go and borrows its vocabulary (result, baseEnv, wantPass, wantFail): the script is shell, and the
// only property a release script really has is that each of its refusals FIRES.
//
// Every case builds a throwaway repository under t.TempDir() -- its own `git init`, its own go.mod, its own
// plugin/ pins -- and puts STUBS for `go`, `make` and `bin/goreleaser` in front of it on PATH. The stubs are what
// make the whole DRY_RUN=1 path testable offline in milliseconds: they record the environment and arguments the
// script hands them (so the GOTOOLCHAIN and GORELEASER_CURRENT_TAG contract is asserted, not assumed) and write
// the two checksums.txt files the script compares. The real four-target build is not this file's job -- the
// P2-12 rehearsal ran that end to end against the real toolchain and the real goreleaser.
package ci_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// ---------------------------------------------------------------------------------------------------------
// the fixture repository

// hashes are stand-in sha256 columns for the four assets, one repeated hex digit each, so a mismatch injected
// by the goreleaser stub is visible at a glance in a failure message.
var hashes = [4]string{
	strings.Repeat("1", 64),
	strings.Repeat("2", 64),
	strings.Repeat("3", 64),
	strings.Repeat("4", 64),
}

// goLine is the go.mod language version every fixture declares. It is deliberately NOT the repository's own:
// nothing here runs a real toolchain, and a fixed string keeps the toolchain assertions readable.
const goLine = "1.27.0"

const manifest = `{
  "name": "brigade",
  "version": "0.0.0"
}
`

type repo struct {
	t       *testing.T
	root    string
	home    string
	stubBin string
}

// newRepo lays out a releasable fixture on branch `master`: go.mod, the plugin pins, a manifest, an executable
// bin/goreleaser stub and PATH stubs for `go` and `make`, all committed so the tree starts clean.
func newRepo(t *testing.T) *repo {
	t.Helper()
	root := t.TempDir()
	r := &repo{t: t, root: root, home: filepath.Join(root, ".home"), stubBin: filepath.Join(root, ".stub-bin")}
	r.mkdir(r.home)
	r.mkdir(r.stubBin)

	r.write("go.mod", "module example.invalid/fixture\n\ngo "+goLine+"\n", 0o600)
	r.write("plugin/bin/VERSION", "0.0.0\n", 0o600)
	r.write("plugin/bin/checksums.txt", "", 0o600)
	r.write("plugin/.claude-plugin/plugin.json", manifest, 0o600)
	// The release's changelog section: the card-110 precondition refuses a version with none.
	r.write("CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [0.1.0] — 2026-01-01\n\n### Added\n\n- The fixture.\n", 0o600)
	// The machinery is untracked, so swapping a stub mid-test does not itself make the tree dirty and trip the
	// clean-tree precondition the test is not looking at.
	r.write(".gitignore", "/.home/\n/.stub-bin/\n/bin/\n/dist/\n/dist-cross/\n/make.log\n/goreleaser.log\n", 0o600)
	r.stubGo("go" + goLine)
	r.stubMake()
	r.stubGoreleaser(false)

	r.git("init", "-q", "-b", "master")
	r.git("add", "-A")
	r.git("commit", "-qm", "fixture")
	return r
}

func (r *repo) mkdir(abs string) {
	r.t.Helper()
	if err := os.MkdirAll(abs, 0o700); err != nil {
		r.t.Fatalf("mkdir %s: %v", abs, err)
	}
}

func (r *repo) write(rel, body string, mode os.FileMode) {
	r.t.Helper()
	r.writeAbs(filepath.Join(r.root, rel), body, mode)
}

func (r *repo) writeAbs(p, body string, mode os.FileMode) {
	r.t.Helper()
	r.mkdir(filepath.Dir(p))
	if mode&0o100 != 0 {
		// The go, make and goreleaser stubs the script under test execs:
		// always 0700, written under syscall.ForkLock
		// (testutil.WriteExecutable has the mechanism and the numbers).
		testutil.WriteExecutable(r.t, p, []byte(body))
		return
	}
	//nolint:gosec // G306: the mode is this test's own choice for a data file (0600 or 0644) and every path is
	// joined onto this test's t.TempDir().
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		r.t.Fatalf("writing %s: %v", p, err)
	}
}

func (r *repo) read(rel string) string {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(r.root, rel))
	if err != nil {
		r.t.Fatalf("reading %s: %v", rel, err)
	}
	return string(body)
}

func (r *repo) remove(rel string) {
	r.t.Helper()
	if err := os.Remove(filepath.Join(r.root, rel)); err != nil {
		r.t.Fatalf("removing %s: %v", rel, err)
	}
}

func (r *repo) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(r.root, rel))
	return err == nil
}

func (r *repo) git(args ...string) {
	r.t.Helper()
	//nolint:gosec // G204: git with a fixed argument list inside this test's own temp directory
	cmd := exec.CommandContext(r.t.Context(), "git", args...)
	cmd.Dir = r.root
	cmd.Env = r.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *repo) gitOut(args ...string) string {
	r.t.Helper()
	//nolint:gosec // G204: git with a fixed argument list inside this test's own temp directory
	cmd := exec.CommandContext(r.t.Context(), "git", args...)
	cmd.Dir = r.root
	cmd.Env = r.env()
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// env is baseEnv plus the committer identity (baseEnv sets only the author, and these fixtures commit), the stub
// PATH in front of the real one, and no inherited GOTOOLCHAIN.
func (r *repo) env(extra ...string) []string {
	r.t.Helper()
	e := baseEnv(r.t, r.home,
		"GIT_COMMITTER_NAME=ci-test",
		"GIT_COMMITTER_EMAIL=ci-test@example.invalid",
	)
	for i, kv := range e {
		if strings.HasPrefix(kv, "PATH=") {
			e[i] = "PATH=" + r.stubBin + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	return append(e, extra...)
}

// stubGo answers `go env GOVERSION` with reports, whatever GOTOOLCHAIN says. Passing the fixture's own
// go<line> makes the toolchain precondition pass; passing anything else makes it fail without a download.
func (r *repo) stubGo(reports string) {
	r.t.Helper()
	r.writeAbs(filepath.Join(r.stubBin, "go"), "#!/bin/sh\nprintf '%s\\n' "+shellQuote(reports)+"\n", 0o700)
}

// stubMake stands in for `make cross version=<v>` (writing the four-line dist-cross/checksums.txt the script
// then compares) and for `make push`, recording every invocation in make.log so a test can prove that step 4
// was NOT reached.
func (r *repo) stubMake() {
	r.t.Helper()
	body := `#!/bin/sh
printf 'make %s\n' "$*" >> make.log
if [ "${1:-}" = cross ]; then
  v=${2#version=}
  mkdir -p dist-cross
  {
    printf '%s  brigade_%s_darwin_amd64\n' ` + hashes[0] + ` "$v"
    printf '%s  brigade_%s_darwin_arm64\n' ` + hashes[1] + ` "$v"
    printf '%s  brigade_%s_linux_amd64\n' ` + hashes[2] + ` "$v"
    printf '%s  brigade_%s_linux_arm64\n' ` + hashes[3] + ` "$v"
  } > dist-cross/checksums.txt
fi
`
	r.writeAbs(filepath.Join(r.stubBin, "make"), body, 0o700)
}

// stubGoreleaser records the arguments and the two environment variables the script must set, then produces
// dist/checksums.txt -- equal to make cross's, or (mismatch) with one hash column changed, which is exactly the
// Makefile/.goreleaser.yaml drift step 3 exists to catch.
func (r *repo) stubGoreleaser(mismatch bool) {
	r.t.Helper()
	transform := "cat dist-cross/checksums.txt > dist/checksums.txt"
	if mismatch {
		transform = "sed 's/^" + hashes[0][:8] + "/deadbeef/' dist-cross/checksums.txt > dist/checksums.txt"
	}
	body := `#!/bin/sh
{
  printf 'args: %s\n' "$*"
  printf 'GORELEASER_CURRENT_TAG=%s\n' "${GORELEASER_CURRENT_TAG:-}"
  printf 'GOTOOLCHAIN=%s\n' "${GOTOOLCHAIN:-}"
} > goreleaser.log
mkdir -p dist
` + transform + `
`
	r.write("bin/goreleaser", body, 0o700)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// museFiles adds the Muse Code package's pins and the marketplace catalog (card 115) to the fixture, committed.
// With them present the script requires `muse` on PATH and step 4 rewrites the catalog's digest with it.
func (r *repo) museFiles() {
	r.t.Helper()
	r.write("plugin/bin/brigade", "#!/bin/sh\nexit 0\n", 0o700)
	r.write("muse/brigade/.muse-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"0.0.0\"\n}\n", 0o600)
	r.write("muse/brigade/bin/VERSION", "0.0.0\n", 0o600)
	r.write("muse/brigade/bin/brigade", "#!/bin/sh\nexit 0\n", 0o700)
	r.write("muse/brigade/bin/checksums.txt", "", 0o600)
	r.write("marketplace.json", catalogJSON("0.0.0", digestZero), 0o600)
	r.git("add", "-A")
	r.git("commit", "-qm", "the Muse package and its catalog")
}

// run executes scripts/release-prep.sh -- the real one, from the repository -- inside the fixture.
func (r *repo) run(extraEnv []string, args ...string) result {
	r.t.Helper()
	script := filepath.Join(testutil.RepoRoot(r.t), "scripts", "release-prep.sh")
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

// ---------------------------------------------------------------------------------------------------------
// the refusals

func TestReleasePrepRefusals(t *testing.T) {
	t.Parallel()

	t.Run("a version with a leading v is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		wantFail(t, r.run(nil, "v0.1.0"), "without the leading 'v'")
	})

	t.Run("a version that is not MAJOR.MINOR.PATCH is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		wantFail(t, r.run(nil, "0.1"), "not a MAJOR.MINOR.PATCH version")
	})

	t.Run("a version carrying shell metacharacters is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		wantFail(t, r.run(nil, "0.1.0; touch pwned"), "not a version token")
		if r.exists("pwned") {
			t.Fatal("the version argument reached a shell")
		}
	})

	t.Run("no version at all is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		wantFail(t, r.run(nil), "usage: scripts/release-prep.sh")
	})

	t.Run("the pre-release sentinel 0.0.0 is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		// `make release` with no version= does not reach the Makefile's `test -n "$(version)"` guard --
		// `version ?= $(plugin_version)` has already defaulted it to the pinned 0.0.0 -- and checksums-check.sh
		// requires plugin/bin/checksums.txt to be EMPTY at 0.0.0, so a released 0.0.0 reddens every later commit.
		wantFail(t, r.run(nil, "0.0.0"), "pre-release sentinel", "version=X.Y.Z")
	})

	t.Run("no card is refused: nothing pins the commit's card", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		wantFail(t, r.run(nil, "0.1.0"), "card=<n>", "Trello card the release belongs to")
	})

	t.Run("a card that is not a number is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		wantFail(t, r.run([]string{"card=15; touch pwned"}, "0.1.0"), "card must be a Trello card number")
		if r.exists("pwned") {
			t.Fatal("the card reached a shell")
		}
	})

	t.Run("a dirty tree is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("plugin/bin/VERSION", "0.0.0\nstray\n", 0o600)
		res := r.run([]string{"card=15"}, "0.1.0")
		wantFail(t, res, "tree not clean")
		if got := r.read("plugin/bin/VERSION"); got != "0.0.0\nstray\n" {
			t.Fatalf("a refused run still wrote VERSION: %q", got)
		}
	})

	t.Run("a staged change is refused too", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("staged.txt", "x\n", 0o600)
		r.git("add", "staged.txt")
		wantFail(t, r.run([]string{"card=15"}, "0.1.0"), "tree not clean")
	})

	t.Run("the wrong branch is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.git("switch", "-q", "-c", "feature/x")
		wantFail(t, r.run([]string{"card=15"}, "0.1.0"), "release from master, not 'feature/x'")
	})

	t.Run("a branch argument that does not match is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		wantFail(t, r.run([]string{"card=15"}, "0.1.0", "rehearsal/other"), "release from rehearsal/other, not 'master'")
	})

	t.Run("an absent plugin manifest is refused before anything is written", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.git("rm", "-q", "plugin/.claude-plugin/plugin.json")
		r.git("commit", "-qm", "drop the manifest")
		res := r.run([]string{"card=15"}, "0.1.0")
		wantFail(t, res, "plugin/.claude-plugin/plugin.json does not exist", "P3-1")
		if got := r.read("plugin/bin/VERSION"); got != "0.0.0\n" {
			t.Fatalf("VERSION was bumped despite the refusal: %q", got)
		}
	})

	t.Run("an absent goreleaser binary is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.remove("bin/goreleaser")
		wantFail(t, r.run([]string{"card=15"}, "0.1.0"), "make setup-goreleaser")
	})

	t.Run("a version with no changelog section is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.write("CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [0.0.9] — 2026-01-01\n", 0o600)
		r.git("commit", "-qam", "drop the section")
		wantFail(t, r.run([]string{"card=15"}, "0.1.0"), "CHANGELOG.md has no '## [0.1.0]' section")
		if r.exists("dist-cross/checksums.txt") {
			t.Fatal("the build ran although the changelog section is missing")
		}
	})

	t.Run("a failing documentation gate is refused before anything is mutated", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.writeAbs(filepath.Join(r.stubBin, "make"), "#!/bin/sh\nprintf 'make %s\\n' \"$*\" >> make.log\n[ \"${1:-}\" = docs-check ] && exit 1\nexit 0\n", 0o700)
		wantFail(t, r.run([]string{"card=15"}, "0.1.0"), "the documentation gate failed")
		if strings.Contains(r.read("plugin/bin/VERSION"), "0.1.0") {
			t.Fatal("the pins were bumped although the documentation gate failed")
		}
	})

	t.Run("a Muse package with no muse on PATH is refused before anything is mutated", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.museFiles()
		// The developer's own PATH may carry a real Muse, so this run sees only the stubs and the tools the
		// script needs before the refusal.
		path := r.stubBin + string(os.PathListSeparator) + restrictedPath(t, r.home, "git", "sed", "grep", "head", "cp", "rm", "diff", "env", "mktemp") // under the ignored .home/, so the tree stays clean
		res := r.run([]string{"DRY_RUN=1", "card=15", "PATH=" + path}, "0.1.0")
		wantFail(t, res, "muse is not on PATH", "owner ruling 2026-10-17")
		if got := r.read("plugin/bin/VERSION"); got != "0.0.0\n" {
			t.Fatalf("the pins were bumped although muse is missing: %q", got)
		}
		if r.exists("dist-cross/checksums.txt") {
			t.Fatal("the build ran although muse is missing")
		}
	})

	t.Run("the dry run announces the catalog write and leaves the catalog alone", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.museFiles()
		stubMuse(t, r.stubBin, digestA)
		res := r.run([]string{"DRY_RUN=1", "card=15"}, "0.1.0")
		wantPass(t, res, "4. MUSE_DIGEST_REQUIRE=1 sh scripts/muse-digest.sh write", "stopping before the commit")
		if got := r.read("muse/brigade/bin/VERSION"); got != "0.1.0\n" {
			t.Fatalf("the Muse package's pin was not bumped: %q", got)
		}
		if got := r.read("marketplace.json"); got != catalogJSON("0.0.0", digestZero) {
			t.Fatalf("the dry run rewrote the catalog:\n%s", got)
		}
	})

	t.Run("a toolchain that cannot be selected is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.stubGo("go1.0.0") // whatever GOTOOLCHAIN asked for, the toolchain that answers is a different one
		wantFail(t, r.run([]string{"card=15"}, "0.1.0"), "cannot select toolchain go"+goLine, "go1.0.0")
	})

	t.Run("running outside the repository root is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		sub := filepath.Join(r.root, "sub")
		r.mkdir(sub)
		script := filepath.Join(testutil.RepoRoot(t), "scripts", "release-prep.sh")
		//nolint:gosec // G204: a fixed script path and a literal argument
		cmd := exec.CommandContext(t.Context(), "sh", script, "0.1.0")
		cmd.Dir = sub
		cmd.Env = r.env("card=15")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("exit 0, want non-zero\n%s", out)
		}
		if !strings.Contains(string(out), "run this from the repository root") {
			t.Fatalf("output does not name the repository root:\n%s", out)
		}
	})

	t.Run("a manifest whose version does not start its own line is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		// release.yml and checksums-check.sh read the manifest with the same anchored sed (execution-log
		// correction 4): a single-line manifest reads as "declares no version", so the bump must not pass.
		r.write("plugin/.claude-plugin/plugin.json", `{"name": "brigade", "version": "0.0.0"}`+"\n", 0o600)
		r.git("commit", "-qam", "single-line manifest")
		wantFail(t, r.run([]string{"DRY_RUN=1", "card=15"}, "0.1.0"), "the bump did not take", "must start its own line")
		// The manifest is bumped and verified BEFORE VERSION is written, so the refusal must not strand a
		// bumped VERSION beside a stale manifest -- git must see the tree exactly as the run found it.
		if got := r.read("plugin/bin/VERSION"); got != "0.0.0\n" {
			t.Fatalf("the refused bump still wrote VERSION: %q", got)
		}
		if got := r.gitOut("status", "--porcelain"); got != "" {
			t.Fatalf("the refused bump left the tree dirty:\n%s", got)
		}
	})

	t.Run("an untracked file is refused too", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		// `git diff`/`git diff --cached` do not see untracked files, but step 4's `make push` runs
		// `git add --verbose :/ .`, which would sweep this into the release commit the tag points at.
		r.write("scratch-note.txt", "not reviewed\n", 0o600)
		res := r.run([]string{"DRY_RUN=1", "card=15"}, "0.1.0")
		wantFail(t, res, "tree not clean", "scratch-note.txt")
		if got := r.read("plugin/bin/VERSION"); got != "0.0.0\n" {
			t.Fatalf("a refused run still wrote VERSION: %q", got)
		}
	})

	t.Run("a misspelt DRY_RUN is a dry run, not a real release", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		// DRY_RUN is a switch: `DRY_RUN=true` must fail SAFE. If it fell through to the real path the script
		// would cp the checksums, run `make push` and `git tag -a`/`git push origin` for real.
		res := r.run([]string{"DRY_RUN=true", "card=15"}, "0.1.0")
		wantPass(t, res, "stopping before the commit")
		if strings.Contains(r.read("make.log"), "push") {
			t.Fatalf("DRY_RUN=true ran make push:\n%s", r.read("make.log"))
		}
		if got := r.read("plugin/bin/checksums.txt"); got != "" {
			t.Fatalf("DRY_RUN=true copied the checksums into the commit-bound file: %q", got)
		}
		if got := r.gitOut("tag", "--list"); got != "" {
			t.Fatalf("DRY_RUN=true created a tag: %q", got)
		}
		if got := r.gitOut("rev-list", "--count", "HEAD"); got != "1" {
			t.Fatalf("DRY_RUN=true created a commit: HEAD is %s commits deep, want 1", got)
		}
	})

	t.Run("goreleaser disagreeing with make cross fails before the commit", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t)
		r.stubGoreleaser(true)
		res := r.run([]string{"DRY_RUN=1", "card=15"}, "0.1.0")
		wantFail(t, res, "drifted apart")
		if strings.Contains(r.read("make.log"), "push") {
			t.Fatalf("make push ran after the drift was detected:\n%s", r.read("make.log"))
		}
	})
}

// ---------------------------------------------------------------------------------------------------------
// the dry run

func TestReleasePrepDryRun(t *testing.T) {
	t.Parallel()

	r := newRepo(t)
	r.git("switch", "-q", "-c", "rehearsal/test")
	res := r.run([]string{"DRY_RUN=1", "card=20"}, "0.1.0", "rehearsal/test") // card 20, not the pinned 15
	wantPass(t, res,
		"preparing v0.1.0 on rehearsal/test with GOTOOLCHAIN=go"+goLine,
		"1. pinned plugin/bin/VERSION",
		"2. built dist-cross/",
		"3. goreleaser reproduces",
		"stopping before the commit",
	)

	// step 1 really happened
	if got := r.read("plugin/bin/VERSION"); got != "0.1.0\n" {
		t.Fatalf("VERSION = %q, want \"0.1.0\\n\"", got)
	}
	if got := r.read("plugin/.claude-plugin/plugin.json"); !strings.Contains(got, `"version": "0.1.0"`) {
		t.Fatalf("the manifest was not bumped:\n%s", got)
	}

	// steps 2 and 3 were handed the contract they must be handed
	if got := r.read("make.log"); !strings.Contains(got, "make cross version=0.1.0") {
		t.Fatalf("make.log does not record the cross build:\n%s", got)
	}
	grl := r.read("goreleaser.log")
	for _, want := range []string{
		"args: release --clean --skip=publish,validate,announce",
		"GORELEASER_CURRENT_TAG=v0.1.0",
		"GOTOOLCHAIN=go" + goLine,
	} {
		if !strings.Contains(grl, want) {
			t.Fatalf("goreleaser.log does not contain %q:\n%s", want, grl)
		}
	}

	// steps 4 and 5 were PRINTED, not run
	for _, want := range []string{
		`4. cp dist/checksums.txt plugin/bin/checksums.txt`,
		`4. make push message="20: Release 0.1.0"`, // the card passed in, not a pinned one
		`5. git tag -a v0.1.0 -m v0.1.0 && git push origin v0.1.0`,
	} {
		if !strings.Contains(res.all(), want) {
			t.Fatalf("the dry run did not announce %q:\n%s", want, res.all())
		}
	}
	if strings.Contains(r.read("make.log"), "push") {
		t.Fatalf("DRY_RUN=1 ran make push:\n%s", r.read("make.log"))
	}
	if got := r.read("plugin/bin/checksums.txt"); got != "" {
		t.Fatalf("DRY_RUN=1 copied the checksums into the commit-bound file: %q", got)
	}
	if got := r.gitOut("rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("DRY_RUN=1 created a commit: HEAD is %s commits deep, want 1", got)
	}
	if got := r.gitOut("tag", "--list"); got != "" {
		t.Fatalf("DRY_RUN=1 created a tag: %q", got)
	}
}
