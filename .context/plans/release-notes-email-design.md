# Release-notes email: images and formatting

Card 43 follow-up, implemented under **Trello card 73**. Written 2026-10-07. Status: **approved the same day**
(Rjae: "go ahead", taking section 6's recommendations as the rulings) **and implemented** — execution-log row
P33-1. One deviation from section 3.3: the converter is a dependency-free Go renderer, `cmd/brigade-release-email`
(`internal/releaseemail`), not showdown through npx: no npm at run time, tests that need no network, and a
restricted Markdown subset, so a construct the layout cannot show is a lint finding for the writer instead of an
unstyled block in the inbox. The implementation is the authority where it and this document differ.

The owner's direction: the weekly release-notes email should read better, with images and better formatting, in
the spirit of "The Diff from Delta" (Zed Industries' Delta newsletter, received 2026-10-07).

The prototype that was approved was a hand-laid HTML of the email that went out on 2026-10-05 (run 37326394132,
releases 0.17.0 to 0.21.0), word for word; it was rendered in Chrome at 1160 px and 400 px and sent to the
owner's own Gmail as a preview on 2026-10-07 (subject `[preview] Brigade 0.17.0 to 0.21.0: what's new`, through
Resend, the same provider the workflow uses), and Gmail rendered it as designed. The renderer now produces that
layout; its test fixture (`internal/releaseemail/render_test.go`, `approvedDraft`) is that email in the brief's
new shape, and `scripts/ci/README.md` says how to render a draft on this machine.

## 1. What the Delta email does

Measured from the email itself (its HTML as Gmail shows it) and its web version,
https://delta.dev/blog/the-diff-2026-09.

| | The Diff from Delta |
|---|---|
| Container | a 600 px white card, centred on a grey page; 468 px content column |
| Type | IBM Plex Sans (falls back to the system sans in Gmail), body 14 px, H3 18 px/23 px weight 500, footer 12 px grey |
| Images | 6: a tracking pixel, a patterned banner, a per-issue title card ("The Diff / September 2026"), and one product screenshot per item, each 468 px wide with alt text |
| Items | 3, each an H3, one paragraph of 2–4 sentences with inline code chips and one or two links, then the screenshot |
| Ending | one link, "Read the full September issue" (the web version has 5 items and ~1,000 words; the email is a 427-word teaser), a rule, "The Zed team \| zed.dev \| delta.dev" |
| Footer | why you receive it, a postal address, Unsubscribe |
| Subject and preheader | "The Diff from Delta"; the hidden first line, shown by the inbox after the subject: "More model providers, threads that start threads, and new ways to organize your work." |

What makes it readable: a narrow measure, one idea per heading, a picture under each idea, and very little text.

## 2. What ours does today

Measured from run 37326394132's artifact (email.md, lint clean, APPROVED).

- The mail step converts `email.md` with showdown 2.1.0 (`new showdown.Converter({tables: true})`,
  `dawidd6/action-send-mail` main.js line 36) and sends the result with no wrapper: an `<h1>`, `<h2>`s, paragraphs,
  bare `<code>`, the update block as a bare `<pre>`, an `<hr>` before the footer.
- So Gmail shows black Arial on white at the full width of its reading pane: lines of 150+ characters, no
  colour, no container, no image, nothing a reader's eye can land on. Five bold leads inside paragraphs are the
  only structure.
- The **text** is good. 463 words with the footer; plain language; the words to type are there. The writer's
  brief needs small changes only (section 3.3).

## 3. The proposal

### 3.1 A layout the shell owns

A fixed HTML template, filled by the shell from the approved draft. The writer keeps writing Markdown; the
reviewer keeps reviewing Markdown; the lint keeps linting Markdown. The HTML is generated after the gate, in
`send-release-notes.sh finish`, the step that already owns the footer.

The prototype's design, top to bottom:

1. **Masthead.** The plugin icon (40 px, from the repository at the newest tag the email covers), the word
   Brigade, and "Release notes · 5 October 2026" on the right. Outside the card, small.
2. **The card.** 600 px, white, 1 px grey border, 12 px radius, on a `#f3f4f6` page.
3. **Title block.** An orange eyebrow with the count ("FIVE RELEASES"), the version range as a 30 px navy H1,
   the intro paragraph at 16 px with its last sentence bold.
4. **What's new.** A thin rule, a small grey "WHAT'S NEW" label, then each item as a 19 px H3 with a **version
   chip** that links to that release's notes page on GitHub, and 15 px/24 px paragraphs. Inline code becomes
   a grey chip in monospace.
5. **Terminal cards.** A fenced block in the draft becomes a dark card with a small label ("AN EMAIL TO A
   SESSION", "IN SLACK", "IN CLAUDE CODE") and the words to type in monospace. This is the terminal product's
   equivalent of Delta's product screenshots, and it needs no image.
6. **Before you update.** An amber callout: pale background, orange left border, bullets inside.
7. **Update.** A terminal card with the two commands and the install link.
8. **Demo card.** The demo video's thumbnail (144 px) with "New to Brigade? … Watch the demo →".
9. **Footer.** The shell's footer, word for word as ruled on 2026-09-29, in 12 px grey, centred.
10. **Preheader.** A hidden first line for the inbox list: the intro's key sentence.

Constraints the prototype meets, and the implementation must keep:

- Inline styles only, tables for layout, no `<style>`, no web fonts, no JavaScript: Gmail drops `<style>` on
  some paths (forwarded mail, some apps) and loads no fonts; the system font stack reads well everywhere.
- Colour palette from the icon: navy `#13315c`, orange `#f2a33c`, slate for the terminal cards.
- `color-scheme: light` declared, solid backgrounds everywhere, so dark-mode clients do not invert the card.
- Under 100 KB (Gmail clips at 102 KB). The prototype is 20 KB.
- The plain-text alternative stays `email.md`, unchanged: a text-only reader loses nothing.
- Works at 400 px (rendered): the chips wrap under their headings, the cards stay full width.

### 3.2 Images: where they come from

**Today, two real images exist, and the prototype uses both.** The plugin icon (in the repository since 0.21.0;
the URL is pinned to the newest tag the email covers, so an email never changes under its reader) and the demo
video's thumbnail from `img.youtube.com`. Both are public URLs; Gmail fetches them through its proxy.

**Hosting.** The repository is public, so `raw.githubusercontent.com/appshapes/brigade/<tag>/…` is the host:
no Pages site (none is enabled), no CDN, no upload step, and the email job keeps its read-only token.

**One screenshot per item, the Delta look.** Delta's items each show the product. Brigade's product is a
terminal, and a screenshot of a terminal is honest and makes the same point. Proposal: a `docs/email/`
directory; a release that has something to show gets a `docs/email/<version>-<slug>.png` made by hand at
release time, with alt text, referenced from its CHANGELOG entry; the writer may put an image under an item only
when a source names it. The lint's `email` kind gains one rule: every `![alt](url)` in the draft is a
`raw.githubusercontent.com/appshapes/brigade/<newest tag>/docs/email/…` URL to a file that exists in the
checkout at that tag, with non-empty alt. Width 520 px in the card (the content column), like Delta's 468.

The procedure for those screenshots was given shape on 2026-10-08 (row P33-2): `docs/email/README.md` is the
authority — who, when, `make email-picture`, the CHANGELOG line, the rules a program checks, and what a reader
sees when a change has no picture (its words and a terminal card; nothing marks the absence).

**A generated title card per issue** (Delta's "September 2026" card): the runner image has Chrome 154 but no
ImageMagick, pandoc or rsvg (GitHub's ubuntu-24.04 readme; Blacksmith's image mirrors it), so a headless-Chrome
render of an HTML card to PNG is possible, but the PNG then needs a home the email job can write to, and the job
cannot write to the repository or a release by design. Not proposed: the HTML title block does the job.

**A banner** (Delta's patterned strip): a static `docs/email/banner.png` designed once, above the masthead.
Optional; needs an hour of design. Not in the prototype.

### 3.3 What changes in the pipeline

- **`scripts/ci/send-release-notes.sh finish`** also writes `email.html`: converts `notes.md` with showdown
  2.1.0, the converter the mail action bundles, through its CLI (`npx --yes showdown@2.1.0 makehtml -i notes.md
  -o body.html -c tables`; Node is on the runner; the exact flag to be verified at implementation), then rewrites
  showdown's tags with inline styles (`<h1>`, `<h2>`, `<h3>`, `<p>`, `<code>`, `<pre><code>` → terminal card,
  `<ul>`/`<li>`, `<a>`, `<img>`), lifts the version out of each `###` heading into a chip, wraps the "Before you
  update" section in the callout, and fills `scripts/ci/release-notes-email.html` (the template; placeholders
  for the body, the title, the eyebrow, the date, the icon URL and the preheader). The footer goes into both
  files, as now. Alternative converter: a Go renderer with goldmark pinned in `tools.mod`; more to build, no npm
  fetch at run time. Recommendation: showdown, because its output for the draft's Markdown is exactly what is
  sent today, so the mapping is known.
- **`scripts/ci/send_release_notes_test.go`** gains `finish` cases on a fixture draft: the HTML has no `<style>`,
  every block tag is styled, the size is under 100 KB, the footer's words are present, no address, the chip links
  point at the releases of the window.
- **The workflow**, both mail steps: `html_body: file:///tmp/brigade-email/email.html`,
  `convert_markdown: false`, `body: file:///tmp/brigade-email/email.md` unchanged; `email.html` joins the
  artifact. The `adopt` path needs nothing: the adopted draft is still `notes.md`, and `finish` renders it.
- **The writer's brief** (`.claude/agents/release-notes-email.md`), so the conversion is mechanical: each item
  is a `### Lead (0.18.0)` heading and one to three sentences, not a bold lead inside a paragraph; an item may
  carry one fenced block of the words to type, with a one-line label as its info string (``` ```in slack ```),
  rendered as a terminal card; the "Step by step →" link ends the item's text rather than one line for all; the
  shape rules and word limits otherwise unchanged. The reviewer checks the shape as before.
- **The lint's `email` kind**: the image rule above, and no raw HTML in the draft (the template is the shell's).
- **`scripts/ci/README.md`** and the workflow header: the template, the two files, the image rule.
- **The inventory step** of `ci.yml`: print `node --version` and `google-chrome --version`, so the converter's
  runtime is a measured fact.

Which releases the email covers, who receives it and the footer's words do not change.

### 3.4 Smaller wins in the same change

- The preheader (10 above): the inbox list shows it after the subject. Delta uses it as a one-line table of
  contents; the shell takes ours from the intro paragraph (first 120 characters) with no change to the brief.
- Every version chip links to its release page: Delta's "read the full issue" link, per item.
- The date in the masthead comes from the run, so a reader can tell a stale forward from a fresh email.

## 4. Not proposed

- A newsletter name ("The Diff"). A subject-line change only, if ever wanted.
- An unsubscribe link or a postal address: the footer's wording and the reply-to-stop rule are the owner's
  2026-09-29 ruling, and this document does not reopen them.
- `<style>` blocks, web fonts, dark-mode variants, a GitHub Pages site, a CDN.

## 5. Evidence

- Delta: "The Diff from Delta", Zed Industries, 2026-10-07 15:19; HTML body 34 KB, 427 words, 6 `<img>`,
  3 `<h3>`; web version https://delta.dev/blog/the-diff-2026-09.
- Ours: run 37326394132 (2026-10-05), artifact `release-notes-email-37326394132` (expires 2026-10-12):
  `window.txt` v0.17.0…v0.21.0, `email.md` 3,079 bytes, `lint.txt` clean, `review.md` APPROVED.
- `dawidd6/action-send-mail` main.js: `getText(textOrFile, convertMarkdown)` with
  `new showdown.Converter({ tables: true })`; package.json depends on `showdown ^2.1.0`.
- The icon exists at tag v0.21.0 (HTTP 200) and not at v0.17.0 (404; added 2026-09-30, commit 5c58c9e); the
  demo thumbnail `img.youtube.com/vi/4ELiHEaAagY/mqdefault.jpg` is served.
- Runner image (GitHub `Ubuntu2404-Readme.md`): Node.js 22.23.3 and 24.21.0, Python 3.12.3, Go 1.24–1.26,
  Google Chrome 154.0.8037.57, Chromium 154; no pandoc, ImageMagick, rsvg-convert or wkhtmltopdf.
- Prototype rendered in Chrome 154 at 1160 px (2,254 px tall) and 400 px (3,024 px tall), and in Gmail web
  (preview of 2026-10-07, Resend id `01a1182f-d23a-7584-a0f6-f2d085d1a4c1`): identical to the Chrome render.
  Gmail appends its own YouTube chip under the message because the mail links to youtu.be.

## 6. Rulings wanted

1. **The template** as prototyped: go ahead, or what to change (colours, the eyebrow, the demo card, the card
   itself).
2. **Images.** (a) The icon and the demo thumbnail now, no new asset. (b) A hand-made terminal screenshot per
   release that has something to show, under `docs/email/`, referenced from the CHANGELOG, lint-checked. (c) A
   designed banner. Recommendation: (a) now, (b) as a habit from the next release on, (c) when there is a
   designer's hour.
3. **The brief's shape change**: `###` headings per item and an optional fenced "words to type" block.
   Recommendation: yes; it also reads better as plain text.
4. **The converter**: showdown 2.1.0 through npx in `finish`, or a Go renderer in `tools.mod`. Recommendation:
   showdown.
5. **The demo card and Gmail's YouTube chip**: keep the card and accept the chip (it appears only in Gmail and
   costs nothing), or drop the card. Recommendation: keep.
6. **A card** for the implementation: a new card, or card 43 reopened.

Rough size of the implementation: the template (~120 lines), ~80 lines of shell in `finish`, the Go test cases,
4 lines in the workflow, ~15 lines in the brief, ~10 in the lint, the README rows. One card.
