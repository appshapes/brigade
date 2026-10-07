package releaseemail

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// approvedDraft is the email of 2026-10-05 (run 37326394132, 0.17.0 to 0.21.0), word for word, in the shape
// the brief asks for since card 73: each item a `###` heading with its version, the words to type in a
// fenced block with a label. Every address and every tracker reference it ever carried was already absent:
// the gate had approved it.
const approvedDraft = `# Brigade 0.17.0 to 0.21.0

Brigade is team messaging between the Claude Code sessions of different people and machines. These five releases
are mostly one feature: **people who do not run Claude Code can now take part in a team, by email or from Slack.**

## What's new

### People on email take part in a team (0.18.0)

An administrator installs a mail gateway into the team's own Supabase project, where it joins the roster as
` + "`mail-gateway`" + `. Anyone writes to its address with a first line ` + "`to: <session>`" + `, and the mail reaches that session
as an ordinary team message.

` + "```an email to a session" + `
to: <session>
<your message>
` + "```" + `

A person's mail is untrusted, like any teammate's message: an "approved" in an email approves nothing.
[Step by step →](https://github.com/appshapes/brigade/blob/master/docs/mail-gateway.md)

### People on Slack take part too (0.19.0)

The Slack twin, hosted the same way: a person direct-messages the team's bot with a first line ` + "`to: <session>`" + `.

` + "```in slack" + `
/brigade sessions
/brigade send <session> <text>
` + "```" + `

### A roster that reads as groups (0.17.0)

` + "`brigade sessions` and `/brigade:sessions`" + ` leave one blank line after each session, so a long roster is no longer a
single block.

## Before you update

A member whose team has no gateway has nothing to do. For an administrator of a team that runs one:

- **Re-run ` + "`make gateway-install`" + ` once** after updating to 0.21.0. The mail provider's webhook now points at the
  new connector; until that re-run, mail to the gateway is refused.
- Every member takes the current release too: an older plugin may not know the team's gateway.

## Update

` + "```in claude code" + `
/brigade:update
/reload-plugins
` + "```" + `

Claude Code also updates Brigade in the background and tells you when to reload. Not installed yet?
https://github.com/appshapes/brigade#install
`

// footer is what send-release-notes.sh finish puts under the draft.
const footer = `

---

Every release and its notes: https://github.com/appshapes/brigade/releases

You are receiving this because you are on the list for the release notes of [appshapes/brigade](https://github.com/appshapes/brigade). To stop these emails, reply to this one and say so.
`

func options() Options {
	return Options{
		Repo:   "appshapes/brigade",
		Server: "https://github.com",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Date:   time.Date(2026, 10, 5, 14, 38, 0, 0, time.UTC),
		Window: []string{"v0.17.0", "v0.18.0", "v0.19.0", "v0.20.0", "v0.21.0"},
	}
}

func render(t *testing.T, src string, o Options) string {
	t.Helper()
	html, err := Render(src, o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return string(html)
}

func TestRenderLaysOutTheApprovedDraft(t *testing.T) {
	t.Parallel()
	html := render(t, approvedDraft+footer, options())

	for _, want := range []string{
		// The masthead: the icon pinned to the commit, the name, the date.
		`<img src="https://raw.githubusercontent.com/appshapes/brigade/0123456789abcdef0123456789abcdef01234567/plugin/.claude-plugin/icon.png"`,
		`Release notes &middot; 5 October 2026`,
		// The title block: the count as the eyebrow, the range without the word Brigade, the intro with its bold.
		`>Five releases</p>`,
		`<h1 style="` + stH1 + `">0.17.0 to 0.21.0</h1>`,
		`<strong>people who do not run Claude Code can now take part in a team, by email or from Slack.</strong>`,
		// The preheader is the items' leads.
		`People on email take part in a team · People on Slack take part too · A roster that reads as groups</div>`,
		// Each item: a heading, a chip linking to the release, inline code as a chip, the escaped placeholders.
		`<h3 style="` + stH3 + `">People on email take part in a team <a href="https://github.com/appshapes/brigade/releases/tag/v0.18.0" style="` + stChip + `">0.18.0</a></h3>`,
		`<code style="` + stCode + `">to: &lt;session&gt;</code>`,
		// The fenced block as a terminal card with its label, escaped.
		`<p style="` + stCardLabel + `">an email to a session</p>`,
		`<pre style="` + stPre + `">to: &lt;session&gt;
&lt;your message&gt;</pre>`,
		// The link, and the arrow kept.
		`<a href="https://github.com/appshapes/brigade/blob/master/docs/mail-gateway.md" style="` + stLink + `">Step by step →</a>`,
		// The callout with its heading and bullets.
		`<td style="` + stCallout + `">`,
		`<h2 style="` + stH2Callout + `">Before you update</h2>`,
		`<li style="` + stLI + `"><strong>Re-run <code style="` + stCode + `">make gateway-install</code> once</strong> after updating to 0.21.0.`,
		// The "What's new" label and the plain "Update" heading.
		`<h2 style="` + stH2Label + `">What's new</h2>`,
		`<h2 style="` + stH2 + `">Update</h2>`,
		// A bare URL becomes a link.
		`<a href="https://github.com/appshapes/brigade#install" style="` + stLink + `">https://github.com/appshapes/brigade#install</a>`,
		// The demo card.
		`<img src="` + demoThumb + `"`,
		// The footer, outside the card, with grey links.
		`<td style="` + stFooter + `">`,
		`<a href="https://github.com/appshapes/brigade/releases" style="` + stFooterA + `">https://github.com/appshapes/brigade/releases</a>`,
		`<a href="https://github.com/appshapes/brigade" style="` + stFooterA + `">appshapes/brigade</a>. To stop these emails, reply to this one and say so.</p>`,
		`<meta name="color-scheme" content="light">`,
		`<title>Brigade 0.17.0 to 0.21.0</title>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the HTML lacks:\n%s\n\n%s", want, html)
		}
	}
	for _, never := range []string{"<style", "<script", "@import", "http://", "TODO"} {
		if strings.Contains(html, never) {
			t.Errorf("the HTML carries %q", never)
		}
	}
	// Every block tag carries its inline style: Gmail has nothing else to go on.
	for _, tag := range []string{"<p", "<h1", "<h2", "<h3", "<li", "<ul", "<pre", "<code", "<a", "<td", "<img"} {
		for _, at := range findAll(html, tag+" ") {
			end := strings.Index(html[at:], ">")
			if !strings.Contains(html[at:at+end], ` style="`) {
				t.Errorf("an unstyled %s: %s", tag, html[at:at+end+1])
			}
		}
	}
	if n := strings.Count(html, "<p>"); n != 0 {
		t.Errorf("%d bare <p>", n)
	}
	if len(html) > MaxBytes {
		t.Errorf("the approved draft renders to %d bytes, over the %d clip", len(html), MaxBytes)
	}
}

func findAll(s, sub string) []int {
	var at []int
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return at
		}
		at = append(at, i+j)
		i += j + 1
	}
}

func TestRenderOneReleaseAndAVersionOutsideTheWindow(t *testing.T) {
	t.Parallel()
	o := options()
	o.Window = []string{"v0.19.0"}
	o.Date = time.Time{}
	html := render(t, approvedDraft+footer, o)
	if !strings.Contains(html, ">One release</p>") {
		t.Errorf("one release is not the eyebrow:\n%s", html)
	}
	if strings.Contains(html, "&middot;") {
		t.Errorf("a zero date still prints")
	}
	// 0.18.0 is not in this window: a chip without a link.
	if !strings.Contains(html, `<span style="`+stChip+`">0.18.0</span>`) || strings.Contains(html, "releases/tag/v0.18.0") {
		t.Errorf("a version outside the window is linked, or not shown:\n%s", html)
	}
	if !strings.Contains(html, `releases/tag/v0.19.0" style="`) {
		t.Errorf("0.19.0 is in the window and not linked")
	}
	// No window at all: the eyebrow is generic, and the raw host follows the server.
	o.Window = nil
	o.Server = "https://github.example"
	html = render(t, approvedDraft+footer, o)
	if !strings.Contains(html, ">Release notes</p>") {
		t.Errorf("no window: the eyebrow is not generic")
	}
	if !strings.Contains(html, `src="https://github.example/appshapes/brigade/raw/0123456789abcdef0123456789abcdef01234567/plugin/.claude-plugin/icon.png"`) {
		t.Errorf("the icon is not served from the server's own raw path:\n%s", html)
	}
}

func TestRenderPinsAnImageToTheCommit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "email"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "email", "0.18.0-mail.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	draft := strings.Replace(approvedDraft, "### People on Slack", "![The roster with the mail gateway on it](docs/email/0.18.0-mail.png)\n\n### People on Slack", 1)
	o := options()
	o.Root = root
	html := render(t, draft+footer, o)
	want := `<img src="https://raw.githubusercontent.com/appshapes/brigade/0123456789abcdef0123456789abcdef01234567/docs/email/0.18.0-mail.png" width="520" alt="The roster with the mail gateway on it" style="` + stImg + `">`
	if !strings.Contains(html, want) {
		t.Errorf("the image is not pinned to the commit:\n%s", html)
	}
	if err := Check(draft, root); err != nil {
		t.Errorf("Check refuses the image that exists: %v", err)
	}
	// The file must exist in the checkout.
	if err := Check(draft, t.TempDir()); err == nil || !strings.Contains(err.Error(), "docs/email/0.18.0-mail.png does not exist in the checkout") {
		t.Errorf("a missing image passed: %v", err)
	}
}

func TestCheckRefusesWhatTheShapeForbids(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		draft string
		want  string
	}{
		{"no title", strings.TrimPrefix(approvedDraft, "# Brigade 0.17.0 to 0.21.0\n"), "line 1: the first line must be the `# Brigade …` heading"},
		{"a second title", approvedDraft + "\n# Again\n", "a second # heading"},
		{"a heading too deep", strings.Replace(approvedDraft, "### A roster", "#### A roster", 1), "a heading deeper than ### is not supported"},
		{"an item before a section", strings.Replace(approvedDraft, "## What's new\n\n", "", 1), "a ### heading before the first ## section"},
		{"a table", approvedDraft + "\n| a | b |\n|---|---|\n| 1 | 2 |\n", "a table is not supported"},
		{"a quote", approvedDraft + "\n> Said someone.\n", "a block quote is not supported"},
		{"raw HTML", approvedDraft + "\n<table><tr><td>x</td></tr></table>\n", "raw HTML is not supported"},
		{"a nested list", strings.Replace(approvedDraft, "- Every member", "  - Nested\n- Every member", 1), "a nested list is not supported"},
		{"indented code", approvedDraft + "\n    /brigade:update\n", "an indented code block is not supported"},
		{"an unclosed fence", approvedDraft + "\n```\n/brigade:update\n", "the fenced block is never closed"},
		{"a setext heading", approvedDraft + "\nAnother\n=======\n", "an underlined (setext) heading is not supported"},
		{"dashes under text", approvedDraft + "\nAnother\n---\n", "a line of dashes under text is read as a heading"},
		{"a rule in a draft", approvedDraft + footer, "the draft carries a rule (---)"},
		{"a relative link", strings.Replace(approvedDraft, "(https://github.com/appshapes/brigade/blob/master/docs/mail-gateway.md)", "(docs/mail-gateway.md)", 1), `the link "docs/mail-gateway.md" is not an absolute https URL`},
		{"an image without alt", approvedDraft + "\n![](docs/email/x.png)\n", "an image needs alt text"},
		{"an image from elsewhere", approvedDraft + "\n![A picture](https://example.com/x.png)\n", "an image must be a file under docs/email/"},
		{"an image inside a paragraph", approvedDraft + "\nSee ![this](docs/email/x.png) here.\n", "an image goes on a line of its own"},
		{"a backtick in a fence label", strings.Replace(approvedDraft, "```in slack", "```in `slack`", 1), "a fence's info string may not contain a backtick"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Check(tc.draft, "")
			if err == nil {
				t.Fatalf("the draft passed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the findings lack %q:\n%s", tc.want, err)
			}
			var fs Findings
			if !errors.As(err, &fs) {
				t.Fatalf("the error is not Findings: %T", err)
			}
		})
	}
	// The control: the approved draft, with and without an image that exists, passes.
	if err := Check(approvedDraft, ""); err != nil {
		t.Errorf("the approved draft is refused: %v", err)
	}
}

func TestCheckReportsEveryFindingAtOnce(t *testing.T) {
	t.Parallel()
	draft := approvedDraft + "\n> quote\n\n| table |\n\n#### deep\n"
	err := Check(draft, "")
	if err == nil {
		t.Fatal("passed")
	}
	for _, want := range []string{"a block quote", "a table", "a heading deeper"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err)
		}
	}
	if n := strings.Count(err.Error(), "\n"); n != 2 {
		t.Errorf("want three lines, one a finding, got:\n%s", err)
	}
}

func TestRenderRefusesAnEmailOverTheClip(t *testing.T) {
	t.Parallel()
	big := approvedDraft + "\n" + strings.Repeat("A paragraph of filler text that says nothing a reader needs.\n\n", 2500) + footer
	_, err := Render(big, options())
	if err == nil || !strings.Contains(err.Error(), "Gmail clips") {
		t.Errorf("an email over %d bytes was rendered: %v", MaxBytes, err)
	}
	if _, err := Render(approvedDraft+footer, Options{}); err == nil {
		t.Errorf("Render without a repository, server and commit passed")
	}
}

func TestInline(t *testing.T) {
	t.Parallel()
	st := inlineStyle{code: "C", link: "L"}
	for _, tc := range []struct{ in, want string }{
		{"plain text", "plain text"},
		{"a & b < c > d \"e\"", "a &amp; b &lt; c &gt; d &quot;e&quot;"},
		{"**bold** and *em* and _em_", "<strong>bold</strong> and <em>em</em> and <em>em</em>"},
		{"snake_case_name stays", "snake_case_name stays"},
		{"2 * 3 * 4", "2 * 3 * 4"},
		{"`to: <session>`", `<code style="C">to: &lt;session&gt;</code>`},
		{"``a ` b``", `<code style="C">a ` + "`" + ` b</code>`},
		{"an unclosed `code", "an unclosed `code"},
		{"[text](https://example.com/x)", `<a href="https://example.com/x" style="L">text</a>`},
		{"[**bold** link](https://example.com)", `<a href="https://example.com" style="L"><strong>bold</strong> link</a>`},
		{"<https://example.com/a>", `<a href="https://example.com/a" style="L">https://example.com/a</a>`},
		{"see https://example.com/a. Then", `see <a href="https://example.com/a" style="L">https://example.com/a</a>. Then`},
		{"(https://example.com/a)", `(<a href="https://example.com/a" style="L">https://example.com/a</a>)`},
		{`escaped \* star \_ under \` + "`" + ` tick`, "escaped * star _ under ` tick"},
		{"[not a link] text", "[not a link] text"},
		{"Step by step →", "Step by step →"},
	} {
		got, fs := inline(tc.in, st, 1)
		if len(fs) > 0 {
			t.Errorf("inline(%q) found: %v", tc.in, fs)
		}
		if got != tc.want {
			t.Errorf("inline(%q)\n got %s\nwant %s", tc.in, got, tc.want)
		}
	}
	if _, fs := inline("[x](docs/x.md)", st, 7); len(fs) != 1 || fs[0].Line != 7 {
		t.Errorf("a relative link is not one finding at its line: %v", fs)
	}
}

func TestPreheaderFallsBackToTheIntroAndIsBounded(t *testing.T) {
	t.Parallel()
	html := render(t, "# Brigade 0.1.0\n\nA first sentence of some length. A second one.\n\n## Update\n\nType `/brigade:update`.\n"+footer, options())
	if !strings.Contains(html, `">A first sentence of some length. A second one.</div>`) {
		t.Errorf("no items: the preheader is not the intro:\n%s", html)
	}
	long := "# Brigade 0.1.0\n\nIntro.\n\n## What's new\n\n" + strings.Repeat("### A lead that is rather long for a preheader line (0.1.0)\n\nText.\n\n", 12)
	html = render(t, long+footer, options())
	at := strings.Index(html, stPreheader+`">`)
	end := strings.Index(html[at:], "</div>")
	pre := html[at+len(stPreheader)+2 : at+end]
	if len(pre) > 150 || !strings.HasSuffix(pre, "…") {
		t.Errorf("the preheader is %d bytes and ends %q", len(pre), pre[len(pre)-3:])
	}
}

func TestRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	draft := filepath.Join(dir, "notes.md")
	email := filepath.Join(dir, "email.md")
	window := filepath.Join(dir, "window.txt")
	out := filepath.Join(dir, "email.html")
	for path, data := range map[string]string{draft: approvedDraft, email: approvedDraft + footer, window: "v0.17.0\nv0.18.0\n"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (int, string) {
		var stderr bytes.Buffer
		code := Run(args, &stderr)
		return code, stderr.String()
	}

	if code, err := run("-check", "-in", draft, "-root", dir); code != 0 || err != "" {
		t.Errorf("-check on the approved draft: exit %d\n%s", code, err)
	}
	if code, err := run("-check", "-in", email, "-root", dir); code != 1 || !strings.Contains(err, "brigade-release-email: line ") || !strings.Contains(err, "the draft carries a rule") {
		t.Errorf("-check on a draft with a footer: exit %d\n%s", code, err)
	}
	if code, err := run("-in", email, "-out", out, "-window", window, "-repo", "appshapes/brigade", "-commit", "abc123", "-date", "2026-10-05"); code != 0 || err != "" {
		t.Fatalf("render: exit %d\n%s", code, err)
	}
	html, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"5 October 2026", ">Two releases</p>", "releases/tag/v0.18.0", "/abc123/plugin/.claude-plugin/icon.png"} {
		if !bytes.Contains(html, []byte(want)) {
			t.Errorf("the rendered file lacks %q", want)
		}
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("email.html mode: %v %v", info.Mode(), err)
	}
	if code, _ := run("-in", email, "-out", out, "-repo", "appshapes/brigade", "-commit", "abc", "-date", "yesterday"); code != 2 {
		t.Errorf("a bad -date: exit %d, want 2", code)
	}
	if code, _ := run("-in", email); code != 2 {
		t.Errorf("render without -out, -repo and -commit: exit %d, want 2", code)
	}
	if code, _ := run(); code != 2 {
		t.Errorf("no arguments: exit %d, want 2", code)
	}
	if code, err := run("-check", "-in", filepath.Join(dir, "missing.md")); code != 1 || !strings.Contains(err, "missing.md") {
		t.Errorf("a missing input: exit %d\n%s", code, err)
	}
}
