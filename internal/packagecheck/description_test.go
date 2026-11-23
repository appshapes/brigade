package packagecheck_test

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/packagecheck"
)

// TestDescriptionLengthIsWhatALoaderReads: the count is of the YAML value,
// in characters, whatever form the frontmatter writes it in; a file with no
// frontmatter or no description counts nothing; frontmatter that is not
// YAML is an error, because nothing can be counted.
func TestDescriptionLengthIsWhatALoaderReads(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		skill string
		want  int
		bad   bool
	}{
		"folded, two lines joined by one space": {skill: "---\nname: x\ndescription: >-\n  one two\n  three\n---\nbody\n", want: len("one two three")},
		"folded and clipped keeps its line end": {skill: "---\ndescription: >\n  one\n  two\n---\n", want: len("one two\n")},
		"literal keeps its line breaks":         {skill: "---\ndescription: |-\n  one\n  two\n---\n", want: len("one\ntwo")},
		"plain on one line":                     {skill: "---\ndescription: one two\nname: x\n---\n", want: len("one two")},
		"plain continued on the next line":      {skill: "---\ndescription: one\n  two\n---\n", want: len("one two")},
		"double-quoted, an escape is one":       {skill: "---\ndescription: \"a\\\"b\"\n---\n", want: 3},
		"characters, not bytes":                 {skill: "---\ndescription: >-\n  à — é\n---\n", want: 5},
		"backticks and a hash are text here":    {skill: "---\ndescription: >-\n  `/brigade:slack #channel`\n---\n", want: len("`/brigade:slack #channel`")},
		"CRLF line ends":                        {skill: "---\r\ndescription: >-\r\n  one\r\n  two\r\n---\r\nbody\r\n", want: len("one two")},
		"no description":                        {skill: "---\nname: x\n---\n# x\n", want: 0},
		"no frontmatter":                        {skill: "# x\ndescription: not frontmatter\n", want: 0},
		"frontmatter never closed":              {skill: "---\ndescription: one\n", bad: true},
		"a blank after the closing line":        {skill: "---\ndescription: one two\n--- \nbody\n", want: len("one two")},
		"a byte order mark and a tab":           {skill: "\ufeff---\t\ndescription: one two\n---\n", want: len("one two")},
		"closed on the last line, no line end":  {skill: "---\ndescription: one two\n---", want: len("one two")},
		"an empty frontmatter":                  {skill: "---\n---\nbody\n", want: 0},
		"a body that mentions description":      {skill: "---\nname: x\n---\ndescription: " + strings.Repeat("a", 2000) + "\n", want: 0},
		"frontmatter that is not YAML":          {skill: "---\ndescription: [one\n---\n", bad: true},
		"a description that is not a scalar":    {skill: "---\ndescription:\n  - one\n---\n", bad: true},
	} {
		got, err := packagecheck.DescriptionLength(c.skill)
		if c.bad {
			if err == nil {
				t.Errorf("%s: counted %d, want an error", name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %d, %v; want %d", name, got, err, c.want)
		}
	}
}

func TestDescriptionFitsAtTheLimitAndNotOver(t *testing.T) {
	t.Parallel()
	skill := func(n int) string { return "---\ndescription: " + strings.Repeat("a", n) + "\n---\n" }
	if err := packagecheck.DescriptionFits(skill(packagecheck.DescriptionLimit)); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	err := packagecheck.DescriptionFits(skill(packagecheck.DescriptionLimit + 1))
	if err == nil || !strings.Contains(err.Error(), "1025 characters") {
		t.Errorf("one over: %v", err)
	}
}
