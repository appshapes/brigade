package packagecheck

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	yaml "github.com/pb33f/go-yaml"
)

// DescriptionLimit is the most characters the `description` of a skill may
// have: over it, a host may not load the skill at all (work item 306; the
// owner's ruling of 2026-11-13, a second deliberate carve-out from the
// ruling of 2026-09-08 that CI does not police what a skill declares). It
// is the smallest limit a host enforces, as read on 2026-11-23:
//
//   - The Agent Skills format, which all four hosts' skills follow
//     (https://agentskills.io/specification): "Must be 1-1024 characters."
//   - Codex CLI refused the skill, "invalid description: exceeds maximum
//     length of 1024 characters", through release 0.141.0
//     (codex-rs/core-skills/src/loader.rs at the tag rust-v0.141.0:
//     MAX_DESCRIPTION_LEN is 1024, counted with chars().count() after runs
//     of whitespace are made one space). From 0.142.0 its parser checks the
//     name's length alone (codex-rs/skills/src/parser.rs), and shortens
//     descriptions only to fit the list's budget. A member may run either.
//   - Claude Code documents no refusal: its listing cuts the description
//     and `when_to_use` together at 1,536 characters
//     (https://code.claude.com/docs/en/skills).
//   - Muse Code documents no limit on a description: a SKILL.md may be
//     262,144 bytes, and the whole catalog has a budget under which a
//     row loses its description
//     (https://meta-models.github.io/muse-code-sdk/next/guides/extend/skills/).
//     Not measured.
//   - Grok Build: no page of its documentation on skills was found, and
//     its binary (1.0.92) carries no message about a description's length.
//     Not measured.
//
// The count is of characters (Unicode code points) of the value a YAML
// parser gives, so a folded scalar (`>-`) counts as the loader reads it and
// not as its raw lines. Codex counted after collapsing whitespace, which is
// never more.
const DescriptionLimit = 1024

// DescriptionLength is the number of characters in the `description` of a
// SKILL.md's frontmatter as a loader reads it; 0 for a file with no
// frontmatter or no description, which is not this check's to refuse (the
// ruling is about a description too long to load, and no more). An error
// is frontmatter that is never closed or that no YAML parser reads, so
// nothing can be counted.
func DescriptionLength(skill string) (int, error) {
	front, ok, err := frontmatterOf(skill)
	if err != nil || !ok {
		return 0, err
	}
	var parsed struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		return 0, fmt.Errorf("its frontmatter is not YAML: %w", err)
	}
	return utf8.RuneCountInString(parsed.Description), nil
}

// frontmatterOf is the text between a file's first line, `---`, and the
// next line that is `---`. It reads as the most lenient loader would: a
// byte order mark before the first line and blanks after either `---` do
// not hide the frontmatter from it. A file that opens with `---` and never
// closes it is an error, not a file without frontmatter.
func frontmatterOf(text string) (string, bool, error) {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	lines := strings.Split(text, "\n")
	if strings.TrimRight(lines[0], " \t") != "---" {
		return "", false, nil
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			return strings.Join(lines[1:i], "\n") + "\n", true, nil
		}
	}
	return "", false, errors.New("its frontmatter opens with --- and is never closed")
}

// DescriptionFits is nil for a SKILL.md whose description a loader
// accepts, and otherwise says why not, with the length.
func DescriptionFits(skill string) error {
	n, err := DescriptionLength(skill)
	if err != nil {
		return err
	}
	if n > DescriptionLimit {
		return fmt.Errorf("its description is %d characters, and a host may refuse a skill whose description is over %d (work item 306): shorten it in the skill's source under skills/", n, DescriptionLimit)
	}
	return nil
}

// descriptionsFit dies on the first of the skills whose description is
// longer than a loader accepts, naming the file and the length.
func (c *checker) descriptionsFit(skills []string) {
	for _, f := range skills {
		text, ok := c.read(f)
		if !ok {
			c.die("%s cannot be read, so its description cannot be counted", f)
		}
		if err := DescriptionFits(text); err != nil {
			c.die("%s: %v", f, err)
		}
	}
}
