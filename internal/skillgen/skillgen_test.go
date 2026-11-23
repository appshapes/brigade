package skillgen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/skillgen"
	"github.com/appshapes/brigade/internal/testutil"
)

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

// TestCopiesAreCurrent is the gate inside `make test`: every package's
// copy of every skill is what its source renders, byte for byte. A copy
// that was edited by hand, or a source changed without `make
// skills-generate`, fails here and in `make skills-check`.
func TestCopiesAreCurrent(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	stale, err := skillgen.Check(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stale {
		t.Error(s)
	}
	names, err := skillgen.Sources(root)
	if err != nil || len(names) != 15 {
		t.Fatalf("%d sources, %v; the four packages ship fifteen skills", len(names), err)
	}
}

// TestCheckFindsAStaleAndAMissingCopy: a tree with one source, whose
// copies are written, checks clean; one copy changed and one removed are
// each reported, and a write puts both back.
func TestCheckFindsAStaleAndAMissingCopy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := "---\nname: hello\ndescription: Says hello.\nuser-invocable: true\nallowed-tools: Bash(brigade:*)\n---\n\n" +
		"Type `{{invoke \"hello\"}}`{{if in \"grok\"}} in a direct TUI{{end}}, in one {{say \"claude\" \"Bash\" \"*\" \"shell\"}} call.\n"
	if err := os.MkdirAll(filepath.Join(root, skillgen.SourceDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, skillgen.SourceDir, "hello.md"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := skillgen.Write(root, "")
	if err != nil || len(changed) != len(skillgen.Harnesses) {
		t.Fatalf("write: %v, %v", changed, err)
	}
	if stale, err := skillgen.Check(root); err != nil || len(stale) != 0 {
		t.Fatalf("a tree just written: %v, %v", stale, err)
	}
	want := map[string]string{
		"claude": "---\nname: hello\ndescription: Says hello.\nuser-invocable: true\nallowed-tools: Bash(brigade:*)\n---\n\nType `/brigade:hello`, in one Bash call.\n",
		"muse":   "---\nname: hello\ndescription: Says hello.\nuser-invocable: true\n---\n\nType `brigade:hello`, in one shell call.\n",
		"codex":  "---\nname: hello\ndescription: Says hello.\n---\n\nType `$brigade:hello`, in one shell call.\n",
		"grok":   "---\nname: hello\ndescription: Says hello.\n---\n\nType `/brigade:hello` in a direct TUI, in one shell call.\n",
	}
	for _, h := range skillgen.Harnesses {
		got, err := os.ReadFile(filepath.Join(root, skillgen.Copy(h, "hello")))
		if err != nil || string(got) != want[h.ID] {
			t.Errorf("%s:\n%q\nwant\n%q (%v)", h.ID, got, want[h.ID], err)
		}
	}
	codex, _ := skillgen.ByID("codex")
	grok, _ := skillgen.ByID("grok")
	if err := os.WriteFile(filepath.Join(root, skillgen.Copy(codex, "hello")), []byte("edited by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, skillgen.Copy(grok, "hello"))); err != nil {
		t.Fatal(err)
	}
	stale, err := skillgen.Check(root)
	if err != nil || len(stale) != 2 || stale[0].Path != skillgen.Copy(codex, "hello") || stale[0].Why != "differs" ||
		stale[1].Path != skillgen.Copy(grok, "hello") || stale[1].Why != "missing" {
		t.Fatalf("a changed copy and a missing one: %+v, %v", stale, err)
	}
	if changed, err := skillgen.Write(root, ""); err != nil || len(changed) != 2 {
		t.Fatalf("the write that puts them back: %v, %v", changed, err)
	}
	if stale, err := skillgen.Check(root); err != nil || len(stale) != 0 {
		t.Fatalf("after the write: %v, %v", stale, err)
	}
	// A skill written by hand into one package has no source: the check
	// names it, and a write leaves it for a person to remove or to source.
	orphan := filepath.Join(root, skillgen.Copy(codex, "byhand"))
	if err := os.MkdirAll(filepath.Dir(orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("---\nname: byhand\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := skillgen.Write(root, ""); err != nil {
		t.Fatal(err)
	}
	stale, err = skillgen.Check(root)
	if err != nil || len(stale) != 1 || stale[0].Path != skillgen.Copy(codex, "byhand") || stale[0].Why != "has no source" {
		t.Fatalf("a skill with no source: %+v, %v", stale, err)
	}
}

// TestTheTableIsTheRegistrys: every harness of docs/harnesses.json has a
// row of the table that writes into its package's skills directory, in the
// registry's order, and the table has no other row. A harness added to the
// registry without one fails here, before its skills are four hand-written
// copies again.
func TestTheTableIsTheRegistrys(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "docs", "harnesses.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Harnesses []struct {
			ID     string `json:"id"`
			Skills string `json:"skills"`
		} `json:"harnesses"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	var want, got []string
	for _, h := range registry.Harnesses {
		want = append(want, h.Skills)
	}
	for _, h := range skillgen.Harnesses {
		got = append(got, h.Skills)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the table writes into %v; the registry's harnesses keep their skills in %v", got, want)
	}
}

// TestASourceMustSpeakForEveryHarness: a mark that names a harness the
// table does not have, or a `say` with no words for one, is an error and
// not a copy with a hole in it.
func TestASourceMustSpeakForEveryHarness(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"an unknown harness in a block": "{{if in \"cursor\"}}x{{end}}",
		"an unknown harness in say":     "{{say \"cursor\" \"x\" \"*\" \"y\"}}",
		"say with no words for one":     "{{say \"claude\" \"x\" \"muse\" \"y\"}}",
		"say with an odd count":         "{{say \"claude\"}}",
		"no frontmatter":                "",
	} {
		root := t.TempDir()
		src := "---\nname: hello\ndescription: d\n---\n" + body + "\n"
		if name == "no frontmatter" {
			src = "hello\n"
		}
		if err := os.MkdirAll(filepath.Join(root, skillgen.SourceDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, skillgen.SourceDir, "hello.md"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := skillgen.Check(root); err == nil {
			t.Errorf("%s: the check passed", name)
		}
	}
}
