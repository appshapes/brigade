package releaseemail

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Options drive one render.
type Options struct {
	Repo   string    // owner/name
	Server string    // the GitHub server, https://github.com in production
	Commit string    // the commit the images are pinned to (GITHUB_SHA): an email never changes under its reader
	Date   time.Time // the masthead's date; zero leaves it out
	Window []string  // the tags the email covers, oldest first; a ### heading's version links to its release
	Root   string    // the checkout, where an image's file must exist; empty skips the check
}

// MaxBytes is the size above which the HTML is refused: Gmail clips a message over 102 KB and shows the rest
// behind a "View entire message" link, footer included.
const MaxBytes = 100 * 1024

// Check parses a DRAFT — the writer's notes.md, without the footer — and returns every finding, or nil when
// the draft renders. root is the checkout, where an image's file must exist.
func Check(src, root string) error {
	blocks, findings := parse(src)
	doc, more := analyze(blocks, root, true)
	findings = append(findings, more...)
	if len(findings) == 0 {
		// A dry run of the layout: the inline findings (a relative link, say) surface here.
		if _, fs := layout(doc, Options{Repo: "owner/name", Server: "https://github.com", Commit: "check"}); len(fs) > 0 {
			findings = append(findings, fs...)
		}
	}
	if len(findings) > 0 {
		return findings
	}
	return nil
}

// Render renders the finished email — the draft with the footer under it — into the HTML the mail step
// sends. The returned error is a Findings when the Markdown is at fault.
func Render(src string, o Options) ([]byte, error) {
	if o.Repo == "" || o.Server == "" || o.Commit == "" {
		return nil, errors.New("the repository, the server and the commit are required")
	}
	blocks, findings := parse(src)
	doc, more := analyze(blocks, o.Root, false)
	findings = append(findings, more...)
	if len(findings) > 0 {
		return nil, findings
	}
	html, fs := layout(doc, o)
	if len(fs) > 0 {
		return nil, fs
	}
	if len(html) > MaxBytes {
		return nil, Findings{{Msg: fmt.Sprintf("the email is %d bytes; Gmail clips a message over %d", len(html), MaxBytes)}}
	}
	return []byte(html), nil
}

// ---- structure ------------------------------------------------------------------------------------------

// document is the draft's shape: the title, the intro, the sections, the footer.
type document struct {
	title    string
	intro    []block
	sections []section
	footer   []block
}

// section is one `## ` heading and the blocks under it.
type section struct {
	heading string
	blocks  []block
}

var (
	reImagePath = regexp.MustCompile(`^docs/email/[A-Za-z0-9][A-Za-z0-9._-]*\.(?:png|jpe?g|gif)$`)
	reVersion   = regexp.MustCompile(`^(.*?)\s*\((\d+\.\d+\.\d+)\)\s*$`)
)

// analyze arranges the blocks into a document and reports what the shape forbids. draft is true for a
// writer's draft, which carries no footer and so no rule.
func analyze(blocks []block, root string, draft bool) (document, Findings) {
	var (
		doc      document
		findings Findings
		inFooter bool
		pictures int // under the current heading
	)
	fail := func(line int, msg string) { findings = append(findings, Finding{Line: line, Msg: msg}) }

	if len(blocks) == 0 || blocks[0].kind != kindHeading || blocks[0].level != 1 {
		fail(1, "the first line must be the `# Brigade …` heading")
	}
	for i, b := range blocks {
		switch {
		case inFooter && b.kind == kindParagraph:
			doc.footer = append(doc.footer, b)
		case inFooter && b.kind == kindRule:
			fail(b.line, "a second rule; the footer is everything after the first")
		case inFooter:
			fail(b.line, "the footer may carry paragraphs only")
		case b.kind == kindRule:
			if draft {
				fail(b.line, "the draft carries a rule (---); the footer is the shell's and goes under the draft later")
			}
			inFooter = true
		case b.kind == kindHeading && b.level == 1:
			if i == 0 {
				doc.title = b.text
			} else {
				fail(b.line, "a second # heading; the email has one title")
			}
		case b.kind == kindHeading && b.level == 2:
			pictures = 0
			doc.sections = append(doc.sections, section{heading: b.text})
		case b.kind == kindImage:
			pictures++
			if pictures > 1 {
				fail(b.line, "a second picture under one heading; one picture per change")
			}
			if b.alt == "" {
				fail(b.line, "an image needs alt text: what it shows, in words")
			}
			if !reImagePath.MatchString(b.src) {
				fail(b.line, fmt.Sprintf("an image must be a file under docs/email/ of the repository (png, jpg or gif), not %q", b.src))
			} else if root != "" {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(b.src))); err != nil {
					fail(b.line, b.src+" does not exist in the checkout")
				}
			}
			fallthrough
		default:
			if b.kind == kindHeading {
				pictures = 0
			}
			if len(doc.sections) == 0 {
				if b.kind == kindHeading {
					fail(b.line, "a ### heading before the first ## section")
				}
				doc.intro = append(doc.intro, b)
			} else {
				s := &doc.sections[len(doc.sections)-1]
				s.blocks = append(s.blocks, b)
			}
		}
	}
	return doc, findings
}

// ---- layout ---------------------------------------------------------------------------------------------

// The palette is the icon's: navy and orange, slate for the terminal cards, greys for the chrome. Inline
// styles only, tables for layout, no <style>, no web font and no script: Gmail drops <style> on some paths
// and loads no fonts, and the system stack reads well in every client.
const (
	font = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif"
	mono = "SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace"

	stP         = "margin:0 0 12px;font-family:" + font + ";font-size:15px;line-height:24px;color:#1f2937"
	stIntro     = "margin:0 0 14px;font-family:" + font + ";font-size:16px;line-height:25px;color:#374151"
	stEyebrow   = "margin:0 0 6px;font-family:" + font + ";font-size:12px;line-height:16px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:#d98a1f"
	stH1        = "margin:0 0 14px;font-family:" + font + ";font-size:30px;line-height:36px;font-weight:700;color:#13315c"
	stH2        = "margin:32px 0 10px;font-family:" + font + ";font-size:17px;line-height:23px;font-weight:700;color:#111827"
	stH2Label   = "margin:22px 0 4px;font-family:" + font + ";font-size:13px;line-height:18px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:#6b7280"
	stH2Callout = "margin:0 0 8px;font-family:" + font + ";font-size:17px;line-height:23px;font-weight:700;color:#7c4a03"
	stH3        = "margin:24px 0 8px;font-family:" + font + ";font-size:19px;line-height:25px;font-weight:600;color:#111827"
	stChip      = "display:inline-block;vertical-align:middle;margin:-3px 0 0 6px;padding:1px 8px;border-radius:999px;background:#eef2f7;font-family:" + font + ";font-size:12px;line-height:18px;font-weight:600;color:#13315c;text-decoration:none"
	stCode      = "font-family:" + mono + ";font-size:13px;background:#f3f4f6;border-radius:4px;padding:1px 4px;color:#111827"
	stLink      = "color:#13315c;font-weight:600"
	stUL        = "margin:0 0 12px;padding:0 0 0 20px;font-family:" + font + ";font-size:15px;line-height:24px;color:#1f2937"
	stLI        = "margin:0 0 8px"
	stCard      = "background:#0f172a;border-radius:8px;padding:12px 16px 14px"
	stCardLabel = "margin:0 0 6px;font-family:" + font + ";font-size:11px;line-height:14px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;color:#94a3b8"
	stPre       = "margin:0;font-family:" + mono + ";font-size:13px;line-height:20px;color:#e2e8f0;white-space:pre-wrap"
	stCallout   = "background:#fff8eb;border-left:4px solid #f2a33c;border-radius:0 8px 8px 0;padding:16px 20px 6px"
	stImg       = "display:block;width:100%;max-width:520px;height:auto;border:1px solid #e5e7eb;border-radius:8px;margin:4px 0 14px"
	stRule      = "border-top:1px solid #e5e7eb;font-size:0;line-height:0"
	stMastName  = "padding-left:12px;vertical-align:middle;font-family:" + font + ";font-size:18px;line-height:22px;font-weight:700;color:#13315c"
	stMastDate  = "vertical-align:middle;font-family:" + font + ";font-size:13px;line-height:18px;color:#6b7280"
	stCardTD    = "background:#ffffff;border:1px solid #e5e7eb;border-radius:12px;padding:36px 40px 32px"
	stFooter    = "padding:22px 16px 0;text-align:center;font-family:" + font + ";font-size:12px;line-height:19px;color:#6b7280"
	stFooterP   = "margin:0 0 6px"
	stFooterA   = "color:#6b7280"
	stDemo      = "border:1px solid #e5e7eb;border-radius:10px;padding:0"
	stDemoText  = "vertical-align:middle;padding:12px 16px;font-family:" + font + ";font-size:14px;line-height:21px;color:#374151"
	stPreheader = "display:none;max-height:0;overflow:hidden;font-size:1px;line-height:1px;color:#f3f4f6;opacity:0"

	// The demo card at the end: the README's video. Template chrome, like the footer: the writer never names it.
	demoURL   = "https://youtu.be/4ELiHEaAagY"
	demoThumb = "https://img.youtube.com/vi/4ELiHEaAagY/mqdefault.jpg"
	demoAlt   = "Brigade demo video, 2 minutes 14 seconds"
	demoText  = "Two minutes of two people's sessions working together."

	iconPath = "plugin/.claude-plugin/icon.png"
)

// countWords are the eyebrow's numerals.
var countWords = []string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"}

// layout writes the HTML of an analyzed document.
func layout(doc document, o Options) (string, Findings) {
	var (
		b        strings.Builder
		findings Findings
		st       = inlineStyle{code: stCode, link: stLink}
	)
	render := func(src string, line int) string {
		html, fs := inline(src, st, line)
		findings = append(findings, fs...)
		return html
	}
	inWindow := map[string]bool{}
	for _, tag := range o.Window {
		inWindow[strings.TrimPrefix(tag, "v")] = true
	}

	title := strings.TrimPrefix(doc.title, "Brigade ")
	eyebrow := "Release notes"
	switch n := len(o.Window); {
	case n == 1:
		eyebrow = "One release"
	case n > 1 && n < len(countWords):
		eyebrow = strings.ToUpper(countWords[n][:1]) + countWords[n][1:] + " releases"
	case n >= len(countWords):
		eyebrow = strconv.Itoa(n) + " releases"
	}

	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<meta name=\"color-scheme\" content=\"light\">\n<meta name=\"supported-color-schemes\" content=\"light\">\n")
	b.WriteString("<title>" + escape(doc.title) + "</title>\n</head>\n")
	b.WriteString("<body style=\"margin:0;padding:0;background:#f3f4f6;-webkit-text-size-adjust:100%\">\n")
	if pre := preheader(doc); pre != "" {
		b.WriteString("<div style=\"" + stPreheader + "\">" + escape(pre) + "</div>\n")
	}
	b.WriteString("<table role=\"presentation\" width=\"100%\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\" style=\"background:#f3f4f6\">\n")
	b.WriteString("<tr><td align=\"center\" style=\"padding:28px 12px 36px\">\n")
	b.WriteString("<table role=\"presentation\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\" width=\"600\" style=\"width:100%;max-width:600px\">\n")

	// Masthead.
	home := o.Server + "/" + o.Repo
	b.WriteString("<tr><td style=\"padding:0 4px 16px\">\n<table role=\"presentation\" width=\"100%\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\"><tr>\n")
	b.WriteString("<td width=\"40\" style=\"width:40px;vertical-align:middle\"><a href=\"" + escape(home) + "\" style=\"text-decoration:none\">")
	b.WriteString("<img src=\"" + escape(rawURL(o, iconPath)) + "\" width=\"40\" height=\"40\" alt=\"Brigade\" style=\"display:block;width:40px;height:40px;border:0;border-radius:9px\"></a></td>\n")
	b.WriteString("<td style=\"" + stMastName + "\"><a href=\"" + escape(home) + "\" style=\"color:#13315c;text-decoration:none\">Brigade</a></td>\n")
	b.WriteString("<td align=\"right\" style=\"" + stMastDate + "\">Release notes")
	if !o.Date.IsZero() {
		b.WriteString(" &middot; " + o.Date.Format("2 January 2006"))
	}
	b.WriteString("</td>\n</tr></table>\n</td></tr>\n")

	// The card.
	b.WriteString("<tr><td style=\"" + stCardTD + "\">\n")
	b.WriteString("<p style=\"" + stEyebrow + "\">" + escape(eyebrow) + "</p>\n")
	b.WriteString("<h1 style=\"" + stH1 + "\">" + render(title, 1) + "</h1>\n")
	for _, blk := range doc.intro {
		writeBlock(&b, blk, stIntro, render, o)
	}
	for _, s := range doc.sections {
		switch strings.ToLower(s.heading) {
		case "before you update":
			b.WriteString("<table role=\"presentation\" width=\"100%\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\" style=\"margin:30px 0 0\"><tr><td style=\"" + stCallout + "\">\n")
			b.WriteString("<h2 style=\"" + stH2Callout + "\">" + render(s.heading, 0) + "</h2>\n")
			for _, blk := range s.blocks {
				writeBlock(&b, blk, stP, render, o)
			}
			b.WriteString("</td></tr></table>\n")
			continue
		case "what's new", "what’s new":
			b.WriteString("<table role=\"presentation\" width=\"100%\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\" style=\"margin:14px 0 0\"><tr><td style=\"" + stRule + "\">&nbsp;</td></tr></table>\n")
			b.WriteString("<h2 style=\"" + stH2Label + "\">" + render(s.heading, 0) + "</h2>\n")
		default:
			b.WriteString("<h2 style=\"" + stH2 + "\">" + render(s.heading, 0) + "</h2>\n")
		}
		for _, blk := range s.blocks {
			if blk.kind == kindHeading {
				lead, version := blk.text, ""
				if m := reVersion.FindStringSubmatch(blk.text); m != nil {
					lead, version = m[1], m[2]
				}
				b.WriteString("<h3 style=\"" + stH3 + "\">" + render(lead, blk.line))
				switch {
				case version != "" && inWindow[version]:
					b.WriteString(" <a href=\"" + escape(home+"/releases/tag/v"+version) + "\" style=\"" + stChip + "\">" + version + "</a>")
				case version != "":
					b.WriteString(" <span style=\"" + stChip + "\">" + version + "</span>")
				}
				b.WriteString("</h3>\n")
				continue
			}
			writeBlock(&b, blk, stP, render, o)
		}
	}

	// The demo card.
	b.WriteString("<table role=\"presentation\" width=\"100%\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\" style=\"margin:30px 0 0\"><tr><td style=\"" + stDemo + "\">\n")
	b.WriteString("<table role=\"presentation\" width=\"100%\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\"><tr>\n")
	b.WriteString("<td width=\"144\" style=\"width:144px;vertical-align:middle;padding:0\"><a href=\"" + demoURL + "\" style=\"text-decoration:none\">")
	b.WriteString("<img src=\"" + demoThumb + "\" width=\"144\" alt=\"" + demoAlt + "\" style=\"display:block;width:144px;height:auto;border:0;border-radius:10px 0 0 10px\"></a></td>\n")
	b.WriteString("<td style=\"" + stDemoText + "\"><strong style=\"color:#111827\">New to Brigade?</strong> " + demoText + " <a href=\"" + demoURL + "\" style=\"" + stLink + ";white-space:nowrap\">Watch the demo &rarr;</a></td>\n")
	b.WriteString("</tr></table>\n</td></tr></table>\n")
	b.WriteString("</td></tr>\n")

	// The footer: the shell's words, small and grey, outside the card.
	if len(doc.footer) > 0 {
		b.WriteString("<tr><td style=\"" + stFooter + "\">\n")
		fst := inlineStyle{code: stCode, link: stFooterA}
		for _, blk := range doc.footer {
			html, fs := inline(blk.text, fst, blk.line)
			findings = append(findings, fs...)
			b.WriteString("<p style=\"" + stFooterP + "\">" + html + "</p>\n")
		}
		b.WriteString("</td></tr>\n")
	}

	b.WriteString("</table>\n</td></tr>\n</table>\n</body>\n</html>\n")
	return b.String(), findings
}

// writeBlock writes a paragraph, a list, a fenced block or an image. pStyle is the paragraph style of the
// place: the intro's or the body's.
func writeBlock(b *strings.Builder, blk block, pStyle string, render func(string, int) string, o Options) {
	switch blk.kind {
	case kindParagraph:
		b.WriteString("<p style=\"" + pStyle + "\">" + render(blk.text, blk.line) + "</p>\n")
	case kindList:
		tag := "ul"
		if blk.ordered {
			tag = "ol"
		}
		b.WriteString("<" + tag + " style=\"" + stUL + "\">\n")
		for _, item := range blk.items {
			b.WriteString("<li style=\"" + stLI + "\">" + render(item, blk.line) + "</li>\n")
		}
		b.WriteString("</" + tag + ">\n")
	case kindFence:
		b.WriteString("<table role=\"presentation\" width=\"100%\" cellpadding=\"0\" cellspacing=\"0\" border=\"0\" style=\"margin:0 0 12px\"><tr><td style=\"" + stCard + "\">\n")
		if blk.info != "" {
			b.WriteString("<p style=\"" + stCardLabel + "\">" + escape(blk.info) + "</p>\n")
		}
		b.WriteString("<pre style=\"" + stPre + "\">" + escape(blk.code) + "</pre>\n")
		b.WriteString("</td></tr></table>\n")
	case kindImage:
		b.WriteString("<img src=\"" + escape(rawURL(o, blk.src)) + "\" width=\"520\" alt=\"" + escape(blk.alt) + "\" style=\"" + stImg + "\">\n")
	case kindHeading, kindRule:
		// Headings are the sections' and the items', written by layout; a rule starts the footer.
	}
}

// rawURL is where a file of the repository is served from, at the commit the email is pinned to. GitHub's
// raw host for github.com; the server's own /raw/ path for any other.
func rawURL(o Options, path string) string {
	if o.Server == "https://github.com" {
		return "https://raw.githubusercontent.com/" + o.Repo + "/" + o.Commit + "/" + path
	}
	return o.Server + "/" + o.Repo + "/raw/" + o.Commit + "/" + path
}

// preheader is the hidden first line an inbox shows after the subject: the items' leads, like a table of
// contents; the intro's first sentence when there are no items. At most 140 characters.
func preheader(doc document) string {
	var leads []string
	for _, s := range doc.sections {
		for _, blk := range s.blocks {
			if blk.kind == kindHeading {
				lead := blk.text
				if m := reVersion.FindStringSubmatch(lead); m != nil {
					lead = m[1]
				}
				leads = append(leads, plain(lead))
			}
		}
	}
	text := strings.Join(leads, " · ")
	if text == "" {
		for _, blk := range doc.intro {
			if blk.kind == kindParagraph {
				text = plain(blk.text)
				break
			}
		}
	}
	if len(text) > 140 {
		cut := strings.LastIndex(text[:140], " ")
		if cut < 60 {
			cut = 140
		}
		text = strings.TrimRight(text[:cut], " ·,;") + "…"
	}
	return text
}
