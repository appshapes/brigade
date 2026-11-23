//go:build !windows

// The pin of the Codex CLI and Grok Build packages, held against git (work item 424, ADR 0030).
//
// Both harnesses read their marketplace file from `master`, and each file names its package at one commit: the
// commit of the last published release. internal/packagecheck's own tests hold the form of the two files and
// the writer; here the Codex CLI check runs on fixture repositories that have release tags, and holds what
// only git can say: that the commit is a release's, that the release has both packages, and where the pin
// stands when a newer release is tagged and its last step has not run.
package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/packagecheck"
)

// pinTree is the Codex CLI fixture with a history of four commits, and returns them:
//
//	early     tagged v0.1.0: a release from before Grok Build's package was there
//	released  tagged v9.9.8, an annotated tag as `make release` makes them: a release with both packages
//	newer     tagged v0.0.0, a tag without an object as a checkout of a tag ref leaves it: the release the
//	          fixture's plugin/bin/VERSION pins
//	later     no tag: a push to master
type pinTree struct {
	*tree
	early, released, newer, later string
}

func newPinTree(t *testing.T) *pinTree {
	t.Helper()
	p := &pinTree{tree: newTree(t)}
	p.codexTree()
	commit := func(message string) string {
		p.git("add", "-A")
		p.git("-c", "user.name=ci-test", "-c", "user.email=ci-test@example.invalid", "commit", "-q", "--allow-empty", "-m", message)
		return p.rev("HEAD")
	}
	p.write(".gitignore", "/.home/\n")
	p.early = commit("a release before Grok Build's package")
	p.git("tag", "v0.1.0")
	p.write("grok/brigade/.grok-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"0.0.0\"\n}\n")
	p.released = commit("a release with both packages")
	p.git("-c", "user.name=ci-test", "-c", "user.email=ci-test@example.invalid", "tag", "-a", "-m", "v9.9.8", "v9.9.8")
	p.newer = commit("the release plugin/bin/VERSION pins")
	p.git("tag", "v0.0.0")
	p.later = commit("a push to master")
	return p
}

// rev is the full sha a name of the fixture's repository stands for.
func (tr *tree) rev(name string) string {
	tr.t.Helper()
	//nolint:gosec // G204: git with a fixed argument list, inside this test's own temp directory
	cmd := exec.CommandContext(tr.t.Context(), "git", "rev-parse", name)
	cmd.Dir = tr.root
	cmd.Env = baseEnv(tr.t, tr.home)
	out, err := cmd.Output()
	if err != nil {
		tr.t.Fatalf("git rev-parse %s: %v", name, err)
	}
	return strings.TrimSpace(string(out))
}

func (tr *tree) read(rel string) string {
	tr.t.Helper()
	//nolint:gosec // G304: a path of this test's own temp directory
	data, err := os.ReadFile(filepath.Join(tr.root, filepath.FromSlash(rel)))
	if err != nil {
		tr.t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// pinTo writes the commit into both marketplace files, as the release sequence does.
func (p *pinTree) pinTo(commit string) {
	p.t.Helper()
	if err := packagecheck.WritePin(p.root, commit); err != nil {
		p.t.Fatal(err)
	}
}

func TestThePinIsARelease(t *testing.T) {
	t.Parallel()

	t.Run("at the release VERSION pins: held, and named", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		p.pinTo(p.newer)
		r := runPackageCheck(t, "codex", p.root, p.env())
		wantPass(t, r, "ok: the Codex CLI and Grok Build packages are pinned to v0.0.0 ("+p.newer+")", "all checks passed")
	})

	t.Run("at the release before, with the newer one tagged: said, and no failure", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		p.pinTo(p.released)
		r := runPackageCheck(t, "codex", p.root, p.env())
		wantPass(t, r,
			"behind: the Codex CLI and Grok Build packages are still at v9.9.8 ("+p.released+"), and v0.0.0 is tagged",
			"`make release-pin version=0.0.0 work_item=<n>`",
			"all checks passed",
		)
	})

	t.Run("at a commit that is no release's: refused", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		p.pinTo(p.later)
		r := runPackageCheck(t, "codex", p.root, p.env())
		wantFail(t, r, "name the commit "+p.later+", which is the commit of no release tag (v*)")
	})

	t.Run("at a release that has no Grok Build package: refused", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		p.pinTo(p.early)
		r := runPackageCheck(t, "codex", p.root, p.env())
		wantFail(t, r, "the release v0.1.0 ("+p.early+") has no grok/brigade/.grok-plugin/plugin.json")
	})

	t.Run("two files that name two releases: refused", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		p.pinTo(p.newer)
		p.write(packagecheck.GrokMarketplace, strings.Replace(p.read(packagecheck.GrokMarketplace), p.newer, p.released, 1))
		r := runPackageCheck(t, "codex", p.root, p.env())
		wantFail(t, r, "one release is one commit")
	})

	t.Run("the package's directory on the default branch, as it was until work item 424: refused", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		market := p.read(packagecheck.CodexMarketplace)
		from := strings.Index(market, `"source": {`)
		to := from + strings.Index(market[from:], "}")
		p.write(packagecheck.CodexMarketplace, market[:from]+`"source": {"source": "local", "path": "./codex/brigade"`+market[to:])
		r := runPackageCheck(t, "codex", p.root, p.env())
		wantFail(t, r, ".agents/plugins/marketplace.json does not name the package at a release's commit")
	})

	t.Run("a tree with no release tag: the form is held and the release is not read", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		r := runPackageCheck(t, "codex", tr.root, tr.env())
		wantPass(t, r, "ok: the Codex CLI and Grok Build packages are pinned to the commit ", "(the release tags are not here to read it against: `git fetch --tags`)")
	})

	t.Run("a clone whose release tags do not reach the release VERSION pins: the release is not read", func(t *testing.T) {
		t.Parallel()
		// A fork whose branch was synced has the commits and the older tags, and not the tag of the release
		// its tree pins: which release the pinned commit is cannot be told there.
		p := newPinTree(t)
		p.git("tag", "-d", "v0.0.0")
		p.pinTo(p.newer)
		r := runPackageCheck(t, "codex", p.root, p.env())
		wantPass(t, r, "pinned to the commit "+p.newer+" (the release tags are not here to read it against")
	})

	t.Run("a pre-release that is tagged is not what the packages are behind", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		p.write("plugin/bin/VERSION", "0.0.0-rc.1\n")
		p.git("tag", "v0.0.0-rc.1", p.later)
		p.pinTo(p.released)
		pin, err := packagecheck.ReadPin(packagecheck.Options{Root: p.root, Environ: p.env()})
		if err != nil || pin.Release != "v9.9.8" || pin.Ahead != "" {
			t.Fatalf("ReadPin = %+v, %v; want the pin at v9.9.8 and behind nothing", pin, err)
		}
	})

	t.Run("a commit that breaks the rules is refused with both files as they were", func(t *testing.T) {
		t.Parallel()
		p := newPinTree(t)
		p.pinTo(p.released)
		codex, grok := p.read(packagecheck.CodexMarketplace), p.read(packagecheck.GrokMarketplace)
		for commit, want := range map[string]string{p.later: "the commit of no release tag", p.early: "has no grok/brigade/.grok-plugin/plugin.json"} {
			if _, err := packagecheck.MovePin(packagecheck.Options{Root: p.root, Environ: p.env()}, commit); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("MovePin(%s) = %v, want an error naming %q", commit, err, want)
			}
			if p.read(packagecheck.CodexMarketplace) != codex || p.read(packagecheck.GrokMarketplace) != grok {
				t.Fatalf("the refusal of %s left the two files changed", commit)
			}
		}
		pin, err := packagecheck.MovePin(packagecheck.Options{Root: p.root, Environ: p.env()}, p.newer)
		if err != nil || pin.Release != "v0.0.0" || !strings.Contains(p.read(packagecheck.GrokMarketplace), p.newer) {
			t.Fatalf("MovePin to a release = %+v, %v", pin, err)
		}
	})
}
