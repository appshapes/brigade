package skillgen_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/skillgen"
	"github.com/appshapes/brigade/internal/testutil"
)

// wordsKept are the skills whose four copies the source reproduces word
// for word: nothing in them differed but what the table and the source's
// marks say (work item 393). While the copies are still the hand-written
// ones, this is what shows that a source says what its copies said. A
// skill that is not listed has a source that says the same thing in one
// wording where the copies had several; the plan lists each
// (.context/plans/skills-one-source.md).
var wordsKept = []string{"work-item", "work-item-next", "work-item-start", "work-items"}

var wordRe = regexp.MustCompile(`\S+`)

// TestSourcesRender: every source renders for every harness, and its
// frontmatter carries the keys that harness takes and no other.
func TestSourcesRender(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	names, err := skillgen.Sources(root)
	if err != nil || len(names) == 0 {
		t.Fatalf("sources: %v, %v", names, err)
	}
	for _, skill := range names {
		for _, h := range skillgen.Harnesses {
			out, err := skillgen.Render(root, skill, h)
			if err != nil {
				t.Fatalf("%s for %s: %v", skill, h.ID, err)
			}
			text := string(out)
			if strings.Contains(text, "{{") || strings.Contains(text, "}}") || strings.Contains(text, "<no value>") {
				t.Errorf("%s for %s: a mark was left in the copy", skill, h.ID)
			}
			front, _, _ := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
			if !strings.Contains(front, "name: "+skill+"\n") || !strings.Contains(front, "description:") {
				t.Errorf("%s for %s: the frontmatter has no name or no description:\n%s", skill, h.ID, front)
			}
			for _, key := range []string{"user-invocable", "argument-hint", "allowed-tools"} {
				if has := strings.Contains(front, "\n"+key+":"); has && !slices.Contains(h.Frontmatter, key) {
					t.Errorf("%s for %s: the frontmatter carries %s, which that harness does not take", skill, h.ID, key)
				}
			}
		}
	}
}

// TestSourcesKeepTheClaudeCodeCopy: a source is the Claude Code package's
// text with the marks put in, so rendered for Claude Code it is that
// copy, byte for byte. A mark that changed a word of it fails here.
func TestSourcesKeepTheClaudeCodeCopy(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	names, err := skillgen.Sources(root)
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := skillgen.ByID("claude")
	for _, skill := range names {
		want, err := os.ReadFile(filepath.Join(root, skillgen.Copy(claude, skill)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := skillgen.Render(root, skill, claude)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: the source rendered for Claude Code is not the package's copy", skill)
		}
	}
}

// TestSourcesKeepTheWordsOfTheirCopies: for the skills of wordsKept, the
// source rendered for each harness has the words of that package's copy,
// in order; only the line breaks may differ.
func TestSourcesKeepTheWordsOfTheirCopies(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	for _, skill := range wordsKept {
		for _, h := range skillgen.Harnesses {
			copyText, err := os.ReadFile(filepath.Join(root, skillgen.Copy(h, skill)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := skillgen.Render(root, skill, h)
			if err != nil {
				t.Fatal(err)
			}
			a, b := wordRe.FindAllString(string(copyText), -1), wordRe.FindAllString(string(got), -1)
			if slices.Equal(a, b) {
				continue
			}
			i := 0
			for i < len(a) && i < len(b) && a[i] == b[i] {
				i++
			}
			from := max(0, i-6)
			t.Errorf("%s for %s: the source's words leave the copy's at word %d:\n  copy:   … %s\n  source: … %s", skill, h.ID, i,
				strings.Join(a[from:min(len(a), i+8)], " "), strings.Join(b[from:min(len(b), i+8)], " "))
		}
	}
}
