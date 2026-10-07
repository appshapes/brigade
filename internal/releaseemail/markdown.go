// Package releaseemail renders the release-notes email (card 43, card 73): the Markdown an agent drafted and
// the gate approved, laid out as the HTML an inbox shows. The Markdown is a small, fixed subset — the shape
// `.claude/agents/release-notes-email.md` asks the writer for — and anything outside it is refused with its
// line number rather than guessed at, so the lint can hand the finding back to the writer's fix cycle.
//
// The package is DEV-ONLY: cmd/brigade-release-email links it; bin/brigade never does. It has no dependency
// outside the standard library on purpose (`make deps-check` guards the shipped binary, and a Markdown library
// would be one more module to pin for one email a week).
package releaseemail

import (
	"fmt"
	"regexp"
	"strings"
)

// kind is the kind of a block.
type kind int

const (
	kindHeading kind = iota
	kindParagraph
	kindList
	kindFence
	kindRule
	kindImage
)

// block is one block of the draft: a heading, a paragraph, a list, a fenced code block, a thematic break or
// an image on a line of its own. `line` is the 1-based line it starts on, for the findings.
type block struct {
	kind    kind
	line    int
	level   int      // heading: 1..3
	text    string   // heading and paragraph: the inline source, soft line breaks joined with one space
	items   []string // list: each item's inline source
	ordered bool     // list: `1.` rather than `-`
	info    string   // fence: the info string, the card's label
	code    string   // fence: the lines, verbatim, without the final newline
	alt     string   // image: the alt text
	src     string   // image: the path as written
}

var (
	reHeading  = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*(?:[ \t]#+)?[ \t]*$`)
	reFence    = regexp.MustCompile("^(```+|~~~+)[ \t]*(.*?)[ \t]*$")
	reRule     = regexp.MustCompile(`^(?:-[ \t]*){3,}$|^(?:\*[ \t]*){3,}$|^(?:_[ \t]*){3,}$`)
	reSetext   = regexp.MustCompile(`^(?:=+|-+)[ \t]*$`)
	reBullet   = regexp.MustCompile(`^[-*+][ \t]+(.*)$`)
	reOrdered  = regexp.MustCompile(`^\d{1,9}[.)][ \t]+(.*)$`)
	reImage    = regexp.MustCompile(`^!\[([^\]]*)\]\(([^()\s]+)\)[ \t]*$`)
	reRawHTML  = regexp.MustCompile(`^<[A-Za-z/!?]`)
	reIndented = regexp.MustCompile(`^(?: {4}|\t)`)
)

// Finding is one thing wrong with the draft, at a line.
type Finding struct {
	Line int
	Msg  string
}

func (f Finding) Error() string {
	if f.Line > 0 {
		return fmt.Sprintf("line %d: %s", f.Line, f.Msg)
	}
	return f.Msg
}

// Findings is every finding of one parse or render, so the writer's fix cycle reads them all at once.
type Findings []Finding

func (fs Findings) Error() string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Error()
	}
	return strings.Join(parts, "\n")
}

// parse splits the draft into blocks. It returns every finding rather than the first.
func parse(src string) ([]block, Findings) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	var (
		blocks   []block
		findings Findings
		para     *block // the paragraph being collected, if any
		list     *block // the list being collected, if any
	)
	fail := func(line int, msg string) { findings = append(findings, Finding{Line: line, Msg: msg}) }
	closeOpen := func() {
		if para != nil {
			blocks = append(blocks, *para)
			para = nil
		}
		if list != nil {
			blocks = append(blocks, *list)
			list = nil
		}
	}

	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		n := i + 1
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimLeft(line, " \t")

		switch {
		case trimmed == "":
			closeOpen()

		case reFence.MatchString(line):
			closeOpen()
			m := reFence.FindStringSubmatch(line)
			fenceMark, info := m[1], m[2]
			if strings.ContainsAny(info, "`") {
				fail(n, "a fence's info string may not contain a backtick")
			}
			var code []string
			closed := false
			for i++; i < len(lines); i++ {
				l := strings.TrimRight(lines[i], " \t")
				if t := strings.TrimLeft(l, " "); strings.HasPrefix(t, fenceMark[:1]) && strings.Trim(t, fenceMark[:1]) == "" && len(t) >= len(fenceMark) {
					closed = true
					break
				}
				code = append(code, lines[i])
			}
			if !closed {
				fail(n, "the fenced block is never closed")
			}
			blocks = append(blocks, block{kind: kindFence, line: n, info: info, code: strings.Join(code, "\n")})

		case list != nil && len(line) > len(trimmed) && (reBullet.MatchString(trimmed) || reOrdered.MatchString(trimmed)):
			fail(n, "a nested list is not supported; keep one level of bullets")

		case list != nil && len(line) > len(trimmed):
			// A continuation line of the current item.
			list.items[len(list.items)-1] += " " + trimmed

		case reBullet.MatchString(trimmed) && len(line) == len(trimmed):
			if para != nil {
				blocks = append(blocks, *para)
				para = nil
			}
			text := reBullet.FindStringSubmatch(trimmed)[1]
			if list == nil || list.ordered {
				if list != nil {
					blocks = append(blocks, *list)
				}
				list = &block{kind: kindList, line: n}
			}
			list.items = append(list.items, text)

		case reOrdered.MatchString(trimmed) && len(line) == len(trimmed):
			if para != nil {
				blocks = append(blocks, *para)
				para = nil
			}
			text := reOrdered.FindStringSubmatch(trimmed)[1]
			if list == nil || !list.ordered {
				if list != nil {
					blocks = append(blocks, *list)
				}
				list = &block{kind: kindList, line: n, ordered: true}
			}
			list.items = append(list.items, text)

		case strings.HasPrefix(trimmed, "#") && reHeading.MatchString(trimmed):
			closeOpen()
			m := reHeading.FindStringSubmatch(trimmed)
			level := len(m[1])
			if level > 3 {
				fail(n, "a heading deeper than ### is not supported")
				level = 3
			}
			if m[2] == "" {
				fail(n, "a heading has no text")
			}
			blocks = append(blocks, block{kind: kindHeading, line: n, level: level, text: m[2]})

		case reRule.MatchString(trimmed):
			if para != nil {
				fail(n, "a line of dashes under text is read as a heading by Markdown; put a blank line before a rule")
				closeOpen()
				continue
			}
			closeOpen()
			blocks = append(blocks, block{kind: kindRule, line: n})

		case para != nil && reSetext.MatchString(trimmed):
			fail(n, "an underlined (setext) heading is not supported; write `## ` before the heading")
			closeOpen()

		case para == nil && list == nil && reImage.MatchString(trimmed):
			closeOpen()
			m := reImage.FindStringSubmatch(trimmed)
			blocks = append(blocks, block{kind: kindImage, line: n, alt: m[1], src: m[2]})

		case strings.HasPrefix(trimmed, ">"):
			fail(n, "a block quote is not supported")
			closeOpen()

		case strings.HasPrefix(trimmed, "|"):
			fail(n, "a table is not supported")
			closeOpen()

		case reRawHTML.MatchString(trimmed):
			fail(n, "raw HTML is not supported; the layout is the renderer's")
			closeOpen()

		case para == nil && list == nil && reIndented.MatchString(raw):
			fail(n, "an indented code block is not supported; use a fence")

		default:
			if list != nil {
				// A line at column 0 under a list that is not an item ends the list.
				blocks = append(blocks, *list)
				list = nil
			}
			if para == nil {
				para = &block{kind: kindParagraph, line: n, text: trimmed}
			} else {
				para.text += " " + trimmed
			}
		}
	}
	closeOpen()
	return blocks, findings
}

// ---- inline ---------------------------------------------------------------------------------------------

// inlineStyle is what the inline renderer needs to know about where it is.
type inlineStyle struct {
	code string // the style of an inline code span
	link string // the style of a link
}

var reBareURL = regexp.MustCompile(`https://[^\s<>"'` + "`" + `]+`)

// inline renders the inline Markdown of a heading, a paragraph or a list item: `code`, **bold**, *emphasis*,
// [text](https://…), <https://…>, bare https:// URLs, and backslash escapes. Everything else is text, HTML-
// escaped. A link that is not absolute https is a finding: a relative link goes nowhere from an inbox.
func inline(src string, st inlineStyle, line int) (string, Findings) {
	var (
		b        strings.Builder
		findings Findings
		text     strings.Builder // plain text collected until the next construct
	)
	flush := func() {
		if text.Len() > 0 {
			b.WriteString(autolink(text.String(), st))
			text.Reset()
		}
	}
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src) && strings.IndexByte("\\`*_{}[]()#+-.!<>|", src[i+1]) >= 0:
			text.WriteByte(src[i+1])
			i += 2

		case c == '`':
			// A code span: as many backticks open it as close it.
			n := 0
			for i+n < len(src) && src[i+n] == '`' {
				n++
			}
			end := strings.Index(src[i+n:], strings.Repeat("`", n))
			if end < 0 {
				text.WriteString(src[i : i+n])
				i += n
				continue
			}
			flush()
			code := strings.TrimSpace(src[i+n : i+n+end])
			b.WriteString(`<code style="` + st.code + `">` + escape(code) + `</code>`)
			i += n + end + n

		case c == '*' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "**")
			if end <= 0 {
				text.WriteByte(c)
				i++
				continue
			}
			flush()
			innerHTML, fs := inline(src[i+2:i+2+end], st, line)
			findings = append(findings, fs...)
			b.WriteString("<strong>" + innerHTML + "</strong>")
			i += 2 + end + 2

		case (c == '*' || c == '_') && i+1 < len(src) && src[i+1] != ' ' && (c == '*' || i == 0 || !isWordByte(src[i-1])):
			end := strings.IndexByte(src[i+1:], c)
			if end <= 0 || src[i+end] == ' ' || (c == '_' && i+1+end+1 < len(src) && isWordByte(src[i+1+end+1])) {
				text.WriteByte(c)
				i++
				continue
			}
			flush()
			innerHTML, fs := inline(src[i+1:i+1+end], st, line)
			findings = append(findings, fs...)
			b.WriteString("<em>" + innerHTML + "</em>")
			i += 1 + end + 1

		case c == '[':
			textEnd := strings.IndexByte(src[i:], ']')
			if textEnd < 0 || i+textEnd+1 >= len(src) || src[i+textEnd+1] != '(' {
				text.WriteByte(c)
				i++
				continue
			}
			urlEnd := strings.IndexByte(src[i+textEnd+2:], ')')
			if urlEnd < 0 {
				text.WriteByte(c)
				i++
				continue
			}
			label := src[i+1 : i+textEnd]
			url := strings.TrimSpace(src[i+textEnd+2 : i+textEnd+2+urlEnd])
			if !strings.HasPrefix(url, "https://") {
				findings = append(findings, Finding{Line: line, Msg: fmt.Sprintf("the link %q is not an absolute https URL", url)})
			}
			flush()
			labelHTML, fs := inline(label, st, line)
			findings = append(findings, fs...)
			b.WriteString(`<a href="` + escape(url) + `" style="` + st.link + `">` + labelHTML + `</a>`)
			i += textEnd + 2 + urlEnd + 1

		case c == '!' && i+1 < len(src) && src[i+1] == '[':
			findings = append(findings, Finding{Line: line, Msg: "an image goes on a line of its own: `![what it shows](docs/email/<file>)`"})
			text.WriteByte(c)
			i++

		case c == '<' && strings.HasPrefix(src[i:], "<https://"):
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				text.WriteByte(c)
				i++
				continue
			}
			flush()
			url := src[i+1 : i+end]
			b.WriteString(`<a href="` + escape(url) + `" style="` + st.link + `">` + escape(url) + `</a>`)
			i += end + 1

		default:
			text.WriteByte(c)
			i++
		}
	}
	flush()
	return b.String(), findings
}

// autolink escapes text and turns every bare https:// URL in it into a link. Trailing punctuation stays text.
func autolink(text string, st inlineStyle) string {
	var b strings.Builder
	last := 0
	for _, m := range reBareURL.FindAllStringIndex(text, -1) {
		url := text[m[0]:m[1]]
		trail := ""
		for len(url) > 0 && strings.IndexByte(".,;:!?)", url[len(url)-1]) >= 0 {
			trail = url[len(url)-1:] + trail
			url = url[:len(url)-1]
		}
		b.WriteString(escape(text[last:m[0]]))
		b.WriteString(`<a href="` + escape(url) + `" style="` + st.link + `">` + escape(url) + `</a>`)
		b.WriteString(escape(trail))
		last = m[1]
	}
	b.WriteString(escape(text[last:]))
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// escape is HTML text escaping, the four characters that matter in both text and attribute values.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// plain strips the inline Markdown of a heading or paragraph down to its text, for the preheader and the
// alt texts.
func plain(src string) string {
	html, _ := inline(src, inlineStyle{}, 0)
	text := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html, "")
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`).Replace(text)
}
