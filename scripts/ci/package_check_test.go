//go:build !windows

package ci_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/appshapes/brigade/internal/docscheck"
	"github.com/appshapes/brigade/internal/packagecheck"
	"github.com/appshapes/brigade/internal/testutil"
)

// runPackageCheck runs the one check of the harness packages (work item 382, internal/packagecheck) for the
// harness id, on the tree at workdir, with an environment built from scratch: what `make plugin-check` and its
// three siblings run, in process. It is what runScript was for the four shell scripts the check replaced, and
// the cases that call it are the ones those scripts had: every refusal kept its words and its test.
func runPackageCheck(t *testing.T, id, workdir string, env []string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := packagecheck.Run(id, packagecheck.Options{Root: workdir, Environ: env, Args: args, Stdout: &out, Stderr: &errb})
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// packageChecksFollowVar is set by the one job that runs each package's make target after `make test`:
// ci.yml's `fast`.
const packageChecksFollowVar = "BRIGADE_PACKAGE_CHECKS_FOLLOW"

// onceInCI skips a test that runs a package's check on the real repository where a later step runs it: the
// `fast` job runs `make test` and then each package's make target as a step of its own, with CI set, which is
// the strict form (a missing shellcheck is a failure there). Here it would be the same check a second time.
// The job says so with a variable of its own. Being in CI is not enough: an agent of a scheduled job, or one
// on a pull request's branch, runs `make test` on a runner with no such step after it, and on a developer's
// machine nothing else runs the check before a push. In both the test is the check.
func onceInCI(t *testing.T, target string) {
	t.Helper()
	if os.Getenv(packageChecksFollowVar) != "" {
		t.Skipf("this job runs `%s` on the real repository as a step of its own; this would be its second run", target)
	}
}

// TestEveryHarnessOfTheRegistryHasAPackageCheck: docs/harnesses.json lists the harnesses Brigade ships a package
// for, and the package check has the rules of each under the harness's id. A harness added to the registry with
// no rules would ship a package nothing checks, and `brigade-packages check` refuses it; rules left behind for a
// harness the registry no longer lists are dead.
func TestEveryHarnessOfTheRegistryHasAPackageCheck(t *testing.T) {
	t.Parallel()
	reg, err := docscheck.LoadRegistry(testutil.RepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, h := range reg.Harnesses {
		listed = append(listed, h.ID)
	}
	slices.Sort(listed)
	if checked := packagecheck.IDs(); !slices.Equal(listed, checked) {
		t.Errorf("the registry lists %v and the package check has rules for %v", listed, checked)
	}
	var out, errb bytes.Buffer
	if code := packagecheck.Run("no-such-harness", packagecheck.Options{Root: t.TempDir(), Stdout: &out, Stderr: &errb}); code != 2 || out.Len() != 0 {
		t.Errorf("a harness with no check: exit %d, stdout %q, stderr %q; want exit 2 and a refusal on stderr", code, out.String(), errb.String())
	}
}

// TestGrokPackageCheckTakesSourceAlone: Grok Build's check takes `--source` for a package that has no release
// pins yet, and no other argument.
func TestGrokPackageCheckTakesSourceAlone(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	if r := runPackageCheck(t, "grok", root, baseEnv(t, t.TempDir()), "--source"); r.code != 0 || r.stdout != "grok-plugin-check: source checked; what a live Grok Build session shows is measured apart from this check\n" {
		t.Errorf("--source on the real repository: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	for _, args := range [][]string{{"--strict"}, {"--source", "--source"}} {
		if r := runPackageCheck(t, "grok", root, baseEnv(t, t.TempDir()), args...); r.code != 2 || r.stdout != "" {
			t.Errorf("%v: exit %d, stdout %q; want a usage error, exit 2", args, r.code, r.stdout)
		}
	}
	// Strict, the default: the three pins are plugin/bin's.
	tr := newTree(t)
	if r := runPackageCheck(t, "grok", tr.root, tr.env()); r.code != 1 {
		t.Errorf("a tree with no package: exit %d, stderr %q; want a refusal", r.code, r.stderr)
	}
}

// TestThePackageCheckIsAsStrictAsTheScriptsWere holds three refusals that the shell scripts made by the way
// they read a file, and that a port could lose without a case of its own (the review of work item 382 found
// each lost): a pin whose line ends the Windows way is no version token, as `head -n 1` kept the carriage
// return; a skill's name is matched as it is written, an underscore included; and every `exec … hook <sub>`
// line of a Muse Code wrapper counts, as `sed -n …p` printed every one.
func TestThePackageCheckIsAsStrictAsTheScriptsWere(t *testing.T) {
	t.Parallel()

	t.Run("a pin that ends its line with a carriage return is no version token", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "0.0.0\r\n")
		r := runPackageCheck(t, "claude-code", tr.root, tr.env())
		wantFail(t, r, "plugin/bin/VERSION is not a version token")
	})

	t.Run("a package's pin with a carriage return is not the manifest's version", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		tr.write("plugin/bin/VERSION", "0.0.0\r\n")
		tr.write("codex/brigade/bin/VERSION", "0.0.0\r\n")
		r := runPackageCheck(t, "codex", tr.root, tr.env())
		wantFail(t, r, "!= codex/brigade/bin/VERSION")
	})

	t.Run("a skill whose name has an underscore is named after its directory", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		tr.write("codex/brigade/skills/my_skill/SKILL.md", "---\nname: my_skill\ndescription: a skill\n---\n")
		tr.git("add", "codex")
		r := runPackageCheck(t, "codex", tr.root, tr.env())
		wantPass(t, r, "all checks passed", "16 skills")

		tr.write("codex/brigade/skills/my_skill/SKILL.md", "---\nname: my skill\ndescription: a skill\n---\n")
		r = runPackageCheck(t, "codex", tr.root, tr.env())
		wantFail(t, r, "is not named after its directory (my_skill)")
	})

	t.Run("a wrapper with two exec lines names no subcommand the binary has", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		wrapper := "muse/brigade/hooks/prompt.sh"
		body, err := os.ReadFile(filepath.Join(tr.root, wrapper))
		if err != nil {
			t.Fatal(err)
		}
		tr.write(wrapper, string(body)+"exec \"$dir/../bin/brigade\" hook session-end \"$@\"\n")
		//nolint:gosec // G302: the wrapper fixture must stay executable for the check's own -x assertion
		if err := os.Chmod(filepath.Join(tr.root, wrapper), 0o700); err != nil {
			t.Fatal(err)
		}
		r := runPackageCheck(t, "muse-code", tr.root, tr.env())
		wantFail(t, r, "which the binary does not implement")
	})

	t.Run("outside a git repository the refusal is the check's one line", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "plugin", "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		// GIT_CEILING_DIRECTORIES: the temp directory is not to be found inside some repository above it.
		r := runPackageCheck(t, "claude-code", root, baseEnv(t, root, "GIT_CEILING_DIRECTORIES="+filepath.Dir(root)))
		if r.code != 1 || r.stderr != "plugin-check: not a git repository: cannot check the recorded file modes\n" {
			t.Errorf("exit %d, stderr %q; want the check's own line and nothing of git's", r.code, r.stderr)
		}
	})
}
