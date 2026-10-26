package ci_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// museTree lays out the Muse Code package of card 88 in the throwaway tree:
// the real muse/brigade/ files (manifest, wrappers, skills) and the real
// bootstrap under both bin/ directories, with the pre-release pins, every
// executable recorded 100755 as the repository records it.
func (tr *tree) museTree() {
	tr.t.Helper()
	repo := testutil.RepoRoot(tr.t)
	copyFile := func(rel string) {
		body, err := os.ReadFile(filepath.Join(repo, rel))
		if err != nil {
			tr.t.Fatalf("reading %s: %v", rel, err)
		}
		tr.write(rel, string(body))
	}
	copyFile("plugin/bin/brigade")
	tr.write("plugin/bin/VERSION", "0.0.0\n")
	tr.write("plugin/bin/checksums.txt", "")
	copyFile("muse/brigade/bin/brigade")
	tr.write("muse/brigade/bin/VERSION", "0.0.0\n")
	tr.write("muse/brigade/bin/checksums.txt", "")
	for _, rel := range []string{
		"muse/brigade/hooks/session-start.sh", "muse/brigade/hooks/prompt.sh", "muse/brigade/hooks/session-end.sh",
		"muse/brigade/skills/team-messaging/SKILL.md", "muse/brigade/skills/sessions/SKILL.md",
		"muse/brigade/skills/setup/SKILL.md", "muse/brigade/skills/join/SKILL.md", "muse/brigade/skills/update/SKILL.md",
		"muse/brigade/skills/email/SKILL.md", "muse/brigade/skills/slack/SKILL.md", "muse/brigade/skills/quiet/SKILL.md",
		"muse/brigade/skills/migrate/SKILL.md", "muse/brigade/README.md",
	} {
		copyFile(rel)
	}
	manifest, err := os.ReadFile(filepath.Join(repo, "muse/brigade/.muse-plugin/plugin.json"))
	if err != nil {
		tr.t.Fatalf("reading the manifest: %v", err)
	}
	// The pre-release pin: the manifest's version must equal bin/VERSION.
	tr.write("muse/brigade/.muse-plugin/plugin.json", replaceVersion(string(manifest), "0.0.0"))
	// The Muse marketplace catalog (card 115), pinned like the package, carrying the digest the stub Muse
	// reports: check 11 then compares the two without running Muse.
	tr.write("marketplace.json", catalogJSON("0.0.0", digestA))
	stubMuse(tr.t, tr.stubBin(), digestA)
	tr.git("add", "plugin", "muse", "marketplace.json")
	for _, exe := range []string{"plugin/bin/brigade", "muse/brigade/bin/brigade",
		"muse/brigade/hooks/session-start.sh", "muse/brigade/hooks/prompt.sh", "muse/brigade/hooks/session-end.sh"} {
		tr.git("update-index", "--add", "--chmod=+x", exe)
		//nolint:gosec // G302: the fixtures must be executable for the check's own -x assertion
		if err := os.Chmod(filepath.Join(tr.root, exe), 0o700); err != nil {
			tr.t.Fatalf("chmod: %v", err)
		}
	}
}

// stubBin is the directory museEnv puts in front of PATH: the stub `muse` lives there, so check 11 compares the
// catalog's digest with what the stub reports instead of running Muse.
func (tr *tree) stubBin() string { return filepath.Join(tr.root, ".stub-bin") }

// museEnv is the fixture's environment with the stub Muse first on PATH.
func (tr *tree) museEnv() []string {
	env := tr.env()
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + tr.stubBin() + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	return env
}

// replaceVersion rewrites the manifest's one `"version": "…"` line.
func replaceVersion(manifest, version string) string {
	lines := strings.Split(manifest, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), `"version":`) {
			lines[i] = `  "version": "` + version + `",`
		}
	}
	return strings.Join(lines, "\n")
}

// replaceOnce replaces the first occurrence of old in s, and fails the
// caller's test through a panic-free zero change when old is absent — the
// assertion that follows then names what was expected.
func replaceOnce(s, old, replacement string) string { return strings.Replace(s, old, replacement, 1) }

func TestMusePluginCheck(t *testing.T) {
	t.Parallel()

	t.Run("the real repository passes", func(t *testing.T) {
		t.Parallel()
		root := testutil.RepoRoot(t)
		r := runScript(t, "muse-plugin-check.sh", root, baseEnv(t, t.TempDir()))
		wantPass(t, r, "all checks passed", "their own wrapper")
	})

	t.Run("the pre-release fixture passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantPass(t, r, "all checks passed")
	})

	t.Run("a stray file under muse/brigade/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/hooks/.DS_Store", "junk\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "not allow-listed under muse/brigade/")
	})

	t.Run("a bootstrap that differs from the Claude package's fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/bin/VERSION", "0.0.0\n# drift\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "differs from plugin/bin/VERSION")
	})

	t.Run("a manifest version that is not the pin fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		manifest, err := os.ReadFile(filepath.Join(tr.root, "muse/brigade/.muse-plugin/plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("muse/brigade/.muse-plugin/plugin.json", replaceVersion(string(manifest), "9.9.9"))
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "!= muse/brigade/bin/VERSION")
	})

	t.Run("a manifest carrying userConfig fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		manifest, err := os.ReadFile(filepath.Join(tr.root, "muse/brigade/.muse-plugin/plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("muse/brigade/.muse-plugin/plugin.json", replaceOnce(string(manifest), `"capabilities": {`, `"userConfig": {}, "capabilities": {`))
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "member Muse does not run")
	})

	t.Run("two hooks sharing one wrapper fail", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		manifest, err := os.ReadFile(filepath.Join(tr.root, "muse/brigade/.muse-plugin/plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("muse/brigade/.muse-plugin/plugin.json", replaceOnce(string(manifest), `["hooks/prompt.sh"]`, `["hooks/session-start.sh"]`))
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "share one source")
	})

	t.Run("a wrapper naming a subcommand the binary lacks fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/hooks/prompt.sh", "#!/bin/sh\nset -u\ncase $0 in */*) dir=${0%/*} ;; *) dir=. ;; esac\nexec \"$dir/../bin/brigade\" hook start \"$@\"\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "'start'", "does not implement")
	})

	t.Run("a symlink under muse/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		if err := os.Symlink("../../plugin/README.md", filepath.Join(tr.root, "muse/brigade/README.md")); err != nil {
			if removeErr := os.Remove(filepath.Join(tr.root, "muse/brigade/README.md")); removeErr != nil {
				t.Fatal(removeErr)
			}
			if err := os.Symlink("../../plugin/README.md", filepath.Join(tr.root, "muse/brigade/README.md")); err != nil {
				t.Fatal(err)
			}
		}
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "symlink")
	})

	t.Run("a skill declaring disable-model-invocation fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/skills/update/SKILL.md", "---\nname: update\ndisable-model-invocation: true\n---\n# x\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "disable-model-invocation")
	})

	// The marketplace catalog (card 115), checks 10 and 11.

	t.Run("a missing catalog fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		if err := os.Remove(filepath.Join(tr.root, "marketplace.json")); err != nil {
			t.Fatal(err)
		}
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "missing marketplace.json")
	})

	t.Run("a catalog version that is not the pin fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("marketplace.json", catalogJSON("9.9.9", digestA))
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "marketplace.json version '9.9.9' != muse/brigade/bin/VERSION")
	})

	t.Run("a catalog that does not point at muse/brigade fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("marketplace.json", replaceOnce(catalogJSON("0.0.0", digestA), `"source": "muse/brigade"`, `"source": "codex/brigade"`))
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "does not point at muse/brigade")
	})

	t.Run("a catalog digest that is not a sha256 fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("marketplace.json", catalogJSON("0.0.0", "sha256:tooshort"))
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, `carries no "digest"`)
	})

	t.Run("a catalog digest that is not what Muse computes fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		stubMuse(t, tr.stubBin(), digestB)
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.museEnv())
		wantFail(t, r, "differs from what Muse computes", "make muse-digest")
	})
}
