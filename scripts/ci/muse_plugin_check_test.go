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
		"muse/brigade/skills/email/SKILL.md", "muse/brigade/skills/slack/SKILL.md", "muse/brigade/README.md",
	} {
		copyFile(rel)
	}
	manifest, err := os.ReadFile(filepath.Join(repo, "muse/brigade/.muse-plugin/plugin.json"))
	if err != nil {
		tr.t.Fatalf("reading the manifest: %v", err)
	}
	// The pre-release pin: the manifest's version must equal bin/VERSION.
	tr.write("muse/brigade/.muse-plugin/plugin.json", replaceVersion(string(manifest), "0.0.0"))
	tr.git("add", "plugin", "muse")
	for _, exe := range []string{"plugin/bin/brigade", "muse/brigade/bin/brigade",
		"muse/brigade/hooks/session-start.sh", "muse/brigade/hooks/prompt.sh", "muse/brigade/hooks/session-end.sh"} {
		tr.git("update-index", "--add", "--chmod=+x", exe)
		//nolint:gosec // G302: the fixtures must be executable for the check's own -x assertion
		if err := os.Chmod(filepath.Join(tr.root, exe), 0o700); err != nil {
			tr.t.Fatalf("chmod: %v", err)
		}
	}
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
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "all checks passed")
	})

	t.Run("a stray file under muse/brigade/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/hooks/.DS_Store", "junk\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "not allow-listed under muse/brigade/")
	})

	t.Run("a bootstrap that differs from the Claude package's fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/bin/VERSION", "0.0.0\n# drift\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
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
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
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
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
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
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "share one source")
	})

	t.Run("a wrapper naming a subcommand the binary lacks fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/hooks/prompt.sh", "#!/bin/sh\nset -u\ncase $0 in */*) dir=${0%/*} ;; *) dir=. ;; esac\nexec \"$dir/../bin/brigade\" hook start \"$@\"\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
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
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "symlink")
	})

	t.Run("a skill declaring disable-model-invocation fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.museTree()
		tr.write("muse/brigade/skills/update/SKILL.md", "---\nname: update\ndisable-model-invocation: true\n---\n# x\n")
		r := runScript(t, "muse-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "disable-model-invocation")
	})
}
