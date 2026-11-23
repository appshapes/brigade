// Package skillgen writes the four packages' skills from one source each
// (work item 393). A skill is written once, in skills/<name>.md at the
// repository's root, as the text the Claude Code package ships with the
// places marked where a harness differs. A harness's copy is that text
// with the marks resolved for it: how a skill is invoked there, which
// frontmatter its loader reads, and the blocks only it has. Four copies
// still ship, because each harness loads its own
// (<package>/skills/<name>/SKILL.md); none of them is edited by hand.
//
// The source is a Go text/template. What a template may ask is the
// Funcs below and nothing else, so a difference between two harnesses is
// either a row of the table (Harnesses) or a block in the source that
// names the harness it is for, where a reader of the source sees it.
package skillgen

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/template"

	"github.com/appshapes/brigade/internal/packagecheck"
)

// SourceDir is where the sources live, relative to the repository's root.
const SourceDir = "skills"

// A Harness is one row of the table: what its copies differ by wherever
// a source does not say more.
type Harness struct {
	// ID is the word a source names the harness by.
	ID string
	// Skills is the package's skills directory.
	Skills string
	// Invoke is how a user invokes a skill by name there: the text put
	// before the skill's name.
	Invoke string
	// Frontmatter are the keys of a skill's frontmatter the harness's
	// loader is given, besides name and description, which every one
	// has. A key a source carries and the harness does not take is left
	// out of its copy.
	Frontmatter []string
}

// Harnesses is the table, in the order the registry lists them.
var Harnesses = []Harness{
	{ID: "claude", Skills: "plugin/skills", Invoke: "/brigade:", Frontmatter: []string{"user-invocable", "argument-hint", "allowed-tools"}},
	{ID: "muse", Skills: "muse/brigade/skills", Invoke: "brigade:", Frontmatter: []string{"user-invocable", "argument-hint"}},
	{ID: "codex", Skills: "codex/brigade/skills", Invoke: "$brigade:"},
	{ID: "grok", Skills: "grok/brigade/skills", Invoke: "/brigade:"},
}

// ByID is the harness of that id.
func ByID(id string) (Harness, bool) {
	for _, h := range Harnesses {
		if h.ID == id {
			return h, true
		}
	}
	return Harness{}, false
}

// Sources are the names of the skills that have a source under root, in
// order.
func Sources(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, SourceDir))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// funcs are what a source may ask of the harness it is rendered for.
func funcs(h Harness) template.FuncMap {
	is := func(ids ...string) (bool, error) {
		for _, id := range ids {
			if _, ok := ByID(id); !ok {
				return false, fmt.Errorf("no harness %q", id)
			}
		}
		return slices.Contains(ids, h.ID), nil
	}
	return template.FuncMap{
		// invoke is the skill's invocation there: /brigade:join in Claude
		// Code and Grok Build, $brigade:join in Codex CLI, brigade:join in
		// Muse Code.
		"invoke": func(name string) string { return h.Invoke + name },
		// argsOf is what a skill's arguments came after, where the source
		// says "is what came after {{argsOf "join"}}:": the command name
		// with the invocation in Claude Code, the skill's name with it in
		// Muse Code, and the invocation alone in the other two.
		"argsOf": func(name string) string {
			switch h.ID {
			case "claude":
				return "the command name, `" + h.Invoke + name + "`"
			case "muse":
				return "the skill's name, `" + h.Invoke + name + "`"
			}
			return "`" + h.Invoke + name + "`"
		},
		// argsOfCommand is argsOf for a source that names no invocation
		// in Claude Code and Muse Code: "the command name" there, and the
		// invocation in the other two.
		"argsOfCommand": func(name string) string {
			if h.ID == "claude" || h.ID == "muse" {
				return "the command name"
			}
			return "`" + h.Invoke + name + "`"
		},
		// say is a few words that differ by harness, where a block would
		// be heavier than the difference: {{say "claude" "the plugin's
		// option" "*" "the option in the options file"}}. Each pair is the
		// harnesses it is for, by id and separated by spaces, or "*" for
		// every other one, and then the words. A harness with no pair is
		// an error: a source says something for every harness.
		"say": func(pairs ...string) (string, error) {
			if len(pairs)%2 != 0 {
				return "", errors.New("say takes pairs: the harnesses, then the words")
			}
			rest, found := "", false
			for i := 0; i < len(pairs); i += 2 {
				if pairs[i] == "*" {
					rest, found = pairs[i+1], true
					continue
				}
				for _, id := range strings.Fields(pairs[i]) {
					if _, ok := ByID(id); !ok {
						return "", fmt.Errorf("no harness %q", id)
					}
					if id == h.ID {
						return pairs[i+1], nil
					}
				}
			}
			if !found {
				return "", fmt.Errorf("say has no words for %s", h.ID)
			}
			return rest, nil
		},
		// in is true for the harnesses named: {{if in "muse" "codex"}}.
		"in": is,
		// not is true for every harness but the ones named.
		"notin": func(ids ...string) (bool, error) {
			ok, err := is(ids...)
			return !ok, err
		},
	}
}

// Render is the copy of the skill for the harness: the source under root
// with its marks resolved, and the frontmatter keys the harness does not
// take left out.
func Render(root, skill string, h Harness) ([]byte, error) {
	path := filepath.Join(root, SourceDir, skill+".md")
	//nolint:gosec // G304: a source of this repository, named by the caller
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := template.New(skill).Option("missingkey=error").Funcs(funcs(h)).Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, nil); err != nil {
		return nil, fmt.Errorf("%s for %s: %w", path, h.ID, err)
	}
	text, err := frontmatter(out.String(), h, path)
	if err != nil {
		return nil, err
	}
	// A copy whose description a host may refuse is never written: the
	// package checks hold the same limit on what ships (work item 306), and
	// here it is said of the source, where it is put right. The marks make
	// a description as long as the harness's own forms, so each rendering
	// is counted.
	if err := packagecheck.DescriptionFits(string(text)); err != nil {
		return nil, fmt.Errorf("%s for %s: %w", path, h.ID, err)
	}
	return text, nil
}

// frontmatter leaves out of text's frontmatter the keys the harness does
// not take. A key is a line that starts at the margin with a word and a
// colon; the lines under it that are indented are its value.
func frontmatter(text string, h Harness, path string) ([]byte, error) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, fmt.Errorf("%s: no frontmatter", path)
	}
	end := slices.Index(lines[1:], "---")
	if end < 0 {
		return nil, fmt.Errorf("%s: the frontmatter does not end", path)
	}
	end++
	keep := []string{"---"}
	taken := true
	for _, line := range lines[1:end] {
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			key, _, ok := strings.Cut(line, ":")
			if !ok {
				return nil, fmt.Errorf("%s: a frontmatter line that is no key: %q", path, line)
			}
			taken = key == "name" || key == "description" || slices.Contains(h.Frontmatter, key)
		}
		if taken {
			keep = append(keep, line)
		}
	}
	return []byte(strings.Join(append(keep, lines[end:]...), "\n")), nil
}

// Copy is where the harness's copy of the skill is, relative to root.
func Copy(h Harness, skill string) string {
	return filepath.Join(h.Skills, skill, "SKILL.md")
}

// A Stale copy is one that is not what its source renders.
type Stale struct {
	Path string
	// Why is "missing", "differs" or "has no source".
	Why string
}

// String says what is wrong with the copy and what puts it right.
func (s Stale) String() string {
	if s.Why == "has no source" {
		return s.Path + " has no source under " + SourceDir + "/: write its source, or remove the copy"
	}
	return s.Path + " " + s.Why + " from its source under " + SourceDir + "/: run `make skills-generate`, never edit a copy"
}

// Check reports the copies under root that are not what their source
// renders: the gate's half. Nothing is written.
func Check(root string) ([]Stale, error) {
	var stale []Stale
	err := each(root, func(h Harness, skill string, want []byte) error {
		rel := Copy(h, skill)
		//nolint:gosec // G304: a path built from the table and a source's name
		got, err := os.ReadFile(filepath.Join(root, rel))
		switch {
		case errors.Is(err, os.ErrNotExist):
			stale = append(stale, Stale{Path: rel, Why: "missing"})
		case err != nil:
			return err
		case !bytes.Equal(got, want):
			stale = append(stale, Stale{Path: rel, Why: "differs"})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	orphans, err := unsourced(root)
	return append(stale, orphans...), err
}

// unsourced are the skills a package carries that no source renders: one
// written by hand into a package, or left behind when its source was
// renamed or removed. Write never deletes one: the copy is removed by hand,
// or given its source.
func unsourced(root string) ([]Stale, error) {
	names, err := Sources(root)
	if err != nil {
		return nil, err
	}
	var out []Stale
	for _, h := range Harnesses {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(h.Skills)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && !slices.Contains(names, e.Name()) {
				out = append(out, Stale{Path: Copy(h, e.Name()), Why: "has no source"})
			}
		}
	}
	return out, nil
}

// Write writes every copy under root (into out, when it is not empty, in
// place of root: a look at what would be written) and answers the paths
// it changed.
func Write(root, out string) ([]string, error) {
	if out == "" {
		out = root
	}
	// Every copy is rendered before one is written: a source that fails for
	// one harness leaves the packages as they were, not half rewritten.
	type copyOf struct {
		rel  string
		want []byte
	}
	var copies []copyOf
	if err := each(root, func(h Harness, skill string, want []byte) error {
		copies = append(copies, copyOf{rel: Copy(h, skill), want: want})
		return nil
	}); err != nil {
		return nil, err
	}
	var changed []string
	for _, c := range copies {
		path := filepath.Join(out, c.rel)
		//nolint:gosec // G304: as in Check
		if got, err := os.ReadFile(path); err == nil && bytes.Equal(got, c.want) {
			continue
		}
		//nolint:gosec // G301: a package's skill directory is an ordinary one
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return changed, err
		}
		//nolint:gosec // G306: a skill is a public file of the package
		if err := os.WriteFile(path, c.want, 0o644); err != nil {
			return changed, err
		}
		changed = append(changed, c.rel)
	}
	return changed, nil
}

// each renders every source for every harness.
func each(root string, f func(h Harness, skill string, want []byte) error) error {
	names, err := Sources(root)
	if err != nil {
		return err
	}
	for _, skill := range names {
		for _, h := range Harnesses {
			want, err := Render(root, skill, h)
			if err != nil {
				return err
			}
			if err := f(h, skill, want); err != nil {
				return err
			}
		}
	}
	return nil
}
