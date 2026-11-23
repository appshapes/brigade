//go:build !windows

package ci_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/packagecheck"
	"github.com/appshapes/brigade/internal/testutil"
)

// checkGrokSource is the check's own reading of the package's source
// (internal/packagecheck), which this test's mutations are run against.
func checkGrokSource(root string) error { return packagecheck.GrokSource(root) }

func TestGrokPackageSource(t *testing.T) {
	t.Parallel()
	onceInCI(t, "make grok-plugin-check")
	if err := checkGrokSource(testutil.RepoRoot(t)); err != nil {
		t.Fatal(err)
	}
}

func TestGrokPackageSourceRejectsDrift(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"skill missing", "skill unreachable", "extra file", "symlink", "wrong hook host", "wrong catalog", "long end timeout", "mcp manifest"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := testutil.RepoRoot(t)
			for _, dir := range []string{"grok/brigade", ".grok-plugin", "plugin/bin"} {
				if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(repo, dir))); err != nil {
					t.Fatal(err)
				}
			}
			replace := func(rel, from, to string) {
				t.Helper()
				path := filepath.Join(root, rel)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(b, []byte(from)) {
					t.Fatalf("mutation anchor missing: %s", from)
				}
				if err := os.WriteFile(path, bytes.Replace(b, []byte(from), []byte(to), 1), 0600); err != nil { //nolint:gosec // allowlisted package copy under t.TempDir only
					t.Fatal(err)
				}
			}
			switch mutation {
			case "skill missing":
				if err := os.Remove(filepath.Join(root, "grok/brigade/skills/join/SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "skill unreachable":
				replace("grok/brigade/skills/join/SKILL.md", "name: join", "name: join\ndisable-model-invocation: true")
			case "extra file":
				if err := os.WriteFile(filepath.Join(root, "grok/brigade/unexpected"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("README.md", filepath.Join(root, "grok/brigade/link")); err != nil {
					t.Fatal(err)
				}
			case "wrong hook host":
				replace("grok/brigade/hooks/hooks.json", "--host grok", "--host codex")
			case "wrong catalog":
				replace(".grok-plugin/marketplace.json", `"path": "grok/brigade"`, `"path": "codex/brigade"`)
			case "long end timeout":
				replace("grok/brigade/hooks/hooks.json", `"timeout": 1`, `"timeout": 3`)
			case "mcp manifest":
				replace("grok/brigade/.grok-plugin/plugin.json", `"name": "brigade",`, `"name": "brigade", "mcpServers": {},`)
			}
			if err := checkGrokSource(root); err == nil {
				t.Fatal("accepted package drift")
			}
		})
	}
}
