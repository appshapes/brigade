//go:build !windows

package ci_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/packagecheck"
	"github.com/appshapes/brigade/internal/testutil"
)

// skillDescribed is a SKILL.md whose description is exactly n characters as
// a loader reads it, written as the packages write theirs: a folded scalar
// (`>-`) over several lines, which a loader joins with one space each. Its
// raw lines are longer than the value by their indentation, so a check that
// counted the raw text would refuse a description at the limit.
func skillDescribed(name string, n int) string {
	var value strings.Builder
	for i := range n {
		if i%80 == 79 && i != n-1 {
			value.WriteByte(' ')
			continue
		}
		value.WriteByte('a')
	}
	return "---\nname: " + name + "\ndescription: >-\n  " + strings.ReplaceAll(value.String(), " ", "\n  ") + "\n---\n# x\n"
}

// TestPackageChecksHoldTheDescriptionLimit (work item 306, the owner's
// ruling of 2026-11-13): each of the four package checks passes a skill
// whose description is at the limit and fails one that is a character over,
// naming the file and the length. The limit and where it is from are beside
// packagecheck.DescriptionLimit.
func TestPackageChecksHoldTheDescriptionLimit(t *testing.T) {
	t.Parallel()
	limit := packagecheck.DescriptionLimit
	over := strconv.Itoa(limit+1) + " characters"

	for _, pkg := range []struct {
		id, skill string
		tree      func(*tree)
		staged    string
	}{
		{id: "claude-code", skill: "plugin/skills/teammsg/SKILL.md", tree: (*tree).pluginTree, staged: "plugin"},
		{id: "muse-code", skill: "muse/brigade/skills/update/SKILL.md", tree: (*tree).museTree},
		{id: "codex", skill: "codex/brigade/skills/update/SKILL.md", tree: (*tree).codexTree},
	} {
		name := filepath.Base(filepath.Dir(pkg.skill))
		t.Run(pkg.id+" at the limit passes", func(t *testing.T) {
			t.Parallel()
			tr := newTree(t)
			pkg.tree(tr)
			tr.write(pkg.skill, skillDescribed(name, limit))
			if pkg.staged != "" {
				tr.git("add", pkg.staged)
			}
			r := runPackageCheck(t, pkg.id, tr.root, tr.env())
			wantPass(t, r, "all checks passed", "over "+strconv.Itoa(limit)+" characters")
		})
		t.Run(pkg.id+" one character over fails", func(t *testing.T) {
			t.Parallel()
			tr := newTree(t)
			pkg.tree(tr)
			tr.write(pkg.skill, skillDescribed(name, limit+1))
			if pkg.staged != "" {
				tr.git("add", pkg.staged)
			}
			r := runPackageCheck(t, pkg.id, tr.root, tr.env())
			wantFail(t, r, pkg.skill, over)
		})
	}

	// Grok Build's check reads the package's real source, so its case is the
	// real package with one skill's description replaced.
	grok := func(t *testing.T, n int) error {
		t.Helper()
		root := t.TempDir()
		repo := testutil.RepoRoot(t)
		for _, dir := range []string{"grok/brigade", ".grok-plugin", "plugin/bin"} {
			if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(repo, dir))); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "grok/brigade/skills/join/SKILL.md"), []byte(skillDescribed("join", n)), 0o600); err != nil {
			t.Fatal(err)
		}
		return checkGrokSource(root)
	}
	t.Run("grok at the limit passes", func(t *testing.T) {
		t.Parallel()
		if err := grok(t, limit); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("grok one character over fails", func(t *testing.T) {
		t.Parallel()
		err := grok(t, limit+1)
		if err == nil || !strings.Contains(err.Error(), "grok/brigade/skills/join/SKILL.md") || !strings.Contains(err.Error(), over) {
			t.Fatalf("a description one character over: %v", err)
		}
	})
}
