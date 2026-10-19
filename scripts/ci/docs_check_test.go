// The documentation gate (card 110). internal/docscheck holds the rules; this file is what proves each of them can
// FAIL, which is the only property a check really has: the gate runs against the repository itself (and must pass),
// and against a copy of the tree with one violation planted at a time (and must name it). `make test` runs both, so
// the gate bites in `make push`, in CI's fast job and in the release sequence before anything is tagged.
package ci_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/docscheck"
	"github.com/appshapes/brigade/internal/testutil"
)

// TestDocsGatePassesOnTheRepository is the gate as CI runs it.
func TestDocsGatePassesOnTheRepository(t *testing.T) {
	t.Parallel()
	findings, sum, err := docscheck.Run(testutil.RepoRoot(t))
	if err != nil {
		t.Fatalf("docscheck.Run: %v", err)
	}
	for _, f := range findings {
		t.Errorf("%s", f)
	}
	if len(findings) == 0 {
		t.Logf("docs-check: %s, ok", sum)
	}
	if sum.Harnesses < 2 || sum.Targets == 0 || sum.Pages == 0 {
		t.Errorf("the summary looks empty: %s", sum)
	}
}

// docsSkipDirs are the parts of the repository the copy leaves out: build output, caches, the git directory and
// the research evidence directories (the gate reads only docs/research's top-level pages, which links name).
var docsSkipDirs = map[string]bool{
	".git": true, "bin": true, "dist": true, "dist-cross": true, "node_modules": true, ".ignored": true,
	".claude/worktrees": true, ".playwright-cli": true, "supabase/.temp": true, "scripts/ci/testdata": true,
}

// copyDocsTree copies the repository's regular files under a fresh temporary directory, so a mutation can be
// planted without touching the checkout. Everything the gate reads or a link names is in it.
func copyDocsTree(t *testing.T) string {
	t.Helper()
	src := testutil.RepoRoot(t)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relPath)
		if d.IsDir() {
			if docsSkipDirs[rel] || (strings.HasPrefix(rel, docscheck.ResearchDir+"/")) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, relPath), 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: a path under the repository being tested
		if err != nil {
			return err
		}
		//nolint:gosec // G703: filepath.Join(t.TempDir(), a path relative to the repository root)
		return os.WriteFile(filepath.Join(dst, relPath), data, 0o600)
	})
	if err != nil {
		t.Fatalf("copying the tree: %v", err)
	}
	return dst
}

func docsWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func docsRead(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // G304: a path under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// docsReplace rewrites the first occurrence of old in rel, and fails loudly when old is absent: a mutation that
// changed nothing would make the failure test vacuous.
func docsReplace(t *testing.T, root, rel, old, replacement string) {
	t.Helper()
	text := docsRead(t, root, rel)
	if !strings.Contains(text, old) {
		t.Fatalf("the mutation's target %q is not in %s, so the mutation would prove nothing", old, rel)
	}
	docsWrite(t, root, rel, strings.Replace(text, old, replacement, 1))
}

func docsAppend(t *testing.T, root, rel, text string) {
	t.Helper()
	docsWrite(t, root, rel, docsRead(t, root, rel)+text)
}

var docsMutations = []struct {
	name   string
	check  string
	mutate func(t *testing.T, root string)
}{
	{"readme: a persona dropped from the list", "readme", func(t *testing.T, root string) {
		t.Helper()
		docsReplace(t, root, docscheck.ReadmeFile, "- **[Developer](docs/developer/README.md)**", "- **Developer**")
	}},
	{"readme: a second H2", "readme", func(t *testing.T, root string) {
		t.Helper()
		docsAppend(t, root, docscheck.ReadmeFile, "\n## Install\n\nType `/plugin install brigade@brigade`.\n")
	}},
	{"persona: a topic overview of three sentences", "persona", func(t *testing.T, root string) {
		t.Helper()
		docsReplace(t, root, "docs/team-member/README.md", "## Install\n\n", "## Install\n\nOne sentence more. ")
	}},
	{"persona: a topic with no section", "persona", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, "docs/team-member/extra/README.md", "# Extra\n\n## If you want to\n\n| If you want to | Go to |\n| --- | --- |\n| do it | [Do it](#do-it) |\n\n## Do it\n\n1. Done.\n\nDetails: [Do it](do-it.md).\n")
		docsWrite(t, root, "docs/team-member/extra/do-it.md", "# Do it\n\nDone.\n")
	}},
	{"topic: an action with no Details line", "topic", func(t *testing.T, root string) {
		t.Helper()
		docsReplace(t, root, "docs/team-member/install/README.md", "Details: [Install in Muse Code](muse-code.md).", "See the page.")
	}},
	{"topic: an orphan leaf", "topic", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, "docs/team-member/install/stray.md", "# Stray\n\nNobody names this page.\n")
	}},
	{"topic: an action over twenty lines", "topic", func(t *testing.T, root string) {
		t.Helper()
		docsReplace(t, root, "docs/team-member/install/README.md", "## Install in Muse Code\n", "## Install in Muse Code\n"+strings.Repeat("- one more line of detail that belongs on the leaf\n", 21))
	}},
	{"links: a missing page", "links", func(t *testing.T, root string) {
		t.Helper()
		docsAppend(t, root, "docs/team-member/install/terminal.md", "\nSee [nowhere](nowhere.md).\n")
	}},
	{"links: a missing anchor", "links", func(t *testing.T, root string) {
		t.Helper()
		docsAppend(t, root, "docs/team-member/install/terminal.md", "\nSee [a section](claude-code.md#no-such-heading).\n")
	}},
	{"links: a CLAUDE.md import that names no file", "links", func(t *testing.T, root string) {
		t.Helper()
		docsAppend(t, root, docscheck.ClaudeFile, "\n@docs/agent/repository/missing.md\n")
	}},
	{"reach: a page nothing links", "reach", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, "docs/lost.md", "# Lost\n\nNo chain of links reaches this page.\n")
	}},
	{"reach: an experiment missing from the index", "reach", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, "docs/experiments/E99-unlisted.md", "# E99\n\nNot in the index.\n")
	}},
	{"harness: a registry entry without its pages", "harness", func(t *testing.T, root string) {
		t.Helper()
		docsReplace(t, root, docscheck.RegistryFile, `  "harnesses": [`, `  "harnesses": [
    {"id": "codex", "name": "Codex", "vendor": "OpenAI", "host": "codex", "package": "codex", "skills": "codex/skills"},`)
	}},
	{"harness: a package the registry does not list", "harness", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, "other/.claude-plugin/plugin.json", "{}\n")
	}},
	{"harness: an unlisted native Grok package", "harness", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, "other/.grok-plugin/plugin.json", "{}\n")
	}},
	{"harness: a skill missing from the agent page", "harness", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, "plugin/skills/extra/SKILL.md", "---\nname: extra\n---\n")
	}},
	{"agent: a rule file CLAUDE.md does not import", "agent", func(t *testing.T, root string) {
		t.Helper()
		docsReplace(t, root, docscheck.ClaudeFile, "@docs/agent/repository/gates.md\n", "")
	}},
	{"options: an option with no row", "options", func(t *testing.T, root string) {
		t.Helper()
		docsReplace(t, root, docscheck.PluginManifest, `"userConfig": {`, `"userConfig": {
    "brand_new_option": {"type": "string", "description": "undocumented"},`)
	}},
	{"make: a stale make-targets page", "make", func(t *testing.T, root string) {
		t.Helper()
		docsAppend(t, root, docscheck.MakeTargetsPage, "| `imaginary` | a target the Makefile does not have |\n")
	}},
	{"changelog: a pinned version with no section", "changelog", func(t *testing.T, root string) {
		t.Helper()
		docsWrite(t, root, docscheck.VersionFile, "9.9.9\n")
	}},
}

// TestDocsGatePassesOnAnUnmutatedCopy is the positive control: the same code path on an untouched copy, and silence.
func TestDocsGatePassesOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	root := copyDocsTree(t)
	findings, _, err := docscheck.Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("the gate reported %d finding(s) on an unmutated copy: %v", len(findings), findings)
	}
}

// TestDocsGateFailsOnPlantedViolations proves every check can fail, one planted violation at a time, and that the
// finding names the check the violation belongs to.
func TestDocsGateFailsOnPlantedViolations(t *testing.T) {
	t.Parallel()
	for _, m := range docsMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			root := copyDocsTree(t)
			m.mutate(t, root)
			findings, _, err := docscheck.Run(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) == 0 {
				t.Fatalf("the mutation changed nothing the gate can see, so the check %q is vacuous", m.check)
			}
			for _, f := range findings {
				if f.Check == m.check {
					t.Logf("caught: %s", f)
					return
				}
			}
			t.Fatalf("the gate reported %v, none of them under the check %q", findings, m.check)
		})
	}
}

// TestDocsGateOutput pins the human form `brigade-docs check` prints: one finding per line and a summary line.
func TestDocsGateOutput(t *testing.T) {
	t.Parallel()
	root := copyDocsTree(t)
	docsWrite(t, root, "docs/lost.md", "# Lost\n")
	out, err := docscheck.Check(root)
	if err == nil || !strings.Contains(out, "docs-check: reach: docs/lost.md:") || !strings.Contains(out, "finding(s)") {
		t.Fatalf("Check printed %q with err %v", out, err)
	}
}
