package docscheck

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// --- 13. decisions -----------------------------------------------------------------
//
// A decision is an ADR, one file under docs/decisions/ (ADR 0001, work items
// 302 and 309). The gate holds two things about them. From the tree: every
// ADR's name, number, title line, Status and Decision section, and its place
// in the index. From git, where the tree is a repository: that an ADR which
// has LANDED keeps its text. Only its Status line changes, or a wrong fact
// is corrected under a dated Corrected entry in its header; the title, the
// other header lines and the Decision section never change, and an ADR is
// never removed or renamed.
//
// What has landed is the default branch as this tree has it: the merge base
// of HEAD and the remote's default branch, or HEAD itself where the
// repository has no such branch. Each ADR that commit holds is compared with
// the tree, and that is the whole of the second half. So an ADR that a
// branch added can be revised, commit after commit, until it merges; an edit
// to a landed one is refused in the working tree, in a commit that is not on
// the default branch yet and in a pull request; and putting the landed text
// back always satisfies the gate. It does not look back at what the default
// branch already did: an edit that reached it some other way is the landed
// text from then on. A tree with no `.git` (a plain copy, an archive) has no
// history to read, and the half is skipped there.
//
// Two things are not part of an ADR's text here. A link's target: when the
// page a link names moves, the link is repointed, so the links check stays
// satisfiable. And the number of an ADR whose number another landed ADR
// also carries: it is renumbered, which is the one rename there is.

// The decisions' files, relative to the root.
const (
	DecisionsDir      = "docs/decisions"
	DecisionsIndex    = "docs/decisions/README.md"
	DecisionsTemplate = "docs/decisions/0000-template.md"
	// DecisionsList is the index's section that lists every ADR.
	DecisionsList = "The decisions"
)

// decisionHeading is the section of an ADR that never changes.
const decisionHeading = "## Decision"

// gitTimeout bounds one git call of the history half, and gitWaitDelay how
// long its pipes may stay open after it is stopped: hang catchers.
const (
	gitTimeout   = 2 * time.Minute
	gitWaitDelay = 2 * time.Second
)

var (
	adrNameRe = regexp.MustCompile(`^(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)
	// adrStatusRe is the Status line of an ADR file. There is no Proposed:
	// a decision that still waits on someone is a work item on the board.
	adrStatusRe = regexp.MustCompile(`^- \*\*Status:\*\* (?:Accepted|Accepted; superseded in part by (\d{4})|Superseded by (\d{4})|Rejected)$`)
	// adrCorrectedRe is the first line of a correction's header entry.
	adrCorrectedRe = regexp.MustCompile(`^- \*\*Corrected:\*\* (\d{4}-\d{2}-\d{2}): \S`)
	// The loose forms find a header line that means to be one, so that a
	// misspelt Status or Corrected line is named rather than missed.
	adrLooseStatusRe    = regexp.MustCompile(`(?i)^\s*[-*]\s*\**\s*status\b`)
	adrLooseCorrectedRe = regexp.MustCompile(`(?i)^\s*[-*]\s*\**\s*corrected\b`)
	// adrStrayCorrectedRe is a Corrected entry's label in a section's body,
	// where it does not belong: the bold label, not a sentence that starts
	// with the word.
	adrStrayCorrectedRe = regexp.MustCompile(`(?i)^\s*[-*]\s*\*\*\s*corrected\s*:?\s*\*\*`)
	adrTitleRe          = regexp.MustCompile(`^# (\d{4})\. `)
)

// An adr is one ADR's text, taken apart into what the rules compare.
type adr struct {
	title     string
	statuses  []string // the header entries that are, or mean to be, a Status line
	corrected []string // the header's Corrected entries, each whole, in order
	fields    []string // every other header entry, in order
	decision  string   // the body of the Decision section
	hasDecide bool
	rest      string   // every other section, heading and body, in order
	stray     []string // lines that mean to be a Corrected entry outside the header
}

// parseADR takes an ADR apart. The header is what stands between the title
// and the first `## ` heading: one entry per `- ` line, with the indented
// lines that continue it; any other line there is an entry of its own, so
// that a change to it is seen.
func parseADR(text string) adr {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var a adr
	if len(lines) > 0 {
		a.title = strings.TrimRight(lines[0], " \t")
	}
	i := 1
	var entries []string
	for ; i < len(lines) && !strings.HasPrefix(lines[i], "## "); i++ {
		line := strings.TrimRight(lines[i], " \t")
		switch {
		case line == "":
		case strings.HasPrefix(line, "  ") && len(entries) > 0:
			entries[len(entries)-1] += "\n" + line
		default:
			entries = append(entries, line)
		}
	}
	for _, e := range entries {
		first, _, _ := strings.Cut(e, "\n")
		switch {
		case adrLooseStatusRe.MatchString(first):
			a.statuses = append(a.statuses, e)
		case adrLooseCorrectedRe.MatchString(first):
			a.corrected = append(a.corrected, e)
		default:
			a.fields = append(a.fields, e)
		}
	}
	// The sections. A `## ` line inside a fenced code block is not a heading,
	// and nothing inside one is a Corrected entry.
	var rest strings.Builder
	for i < len(lines) {
		heading := strings.TrimRight(lines[i], " \t")
		j, fenced := i+1, false
		for ; j < len(lines); j++ {
			if fenceRe.MatchString(lines[j]) {
				fenced = !fenced
				continue
			}
			if fenced {
				continue
			}
			if strings.HasPrefix(lines[j], "## ") {
				break
			}
			if adrStrayCorrectedRe.MatchString(lines[j]) {
				a.stray = append(a.stray, strings.TrimSpace(lines[j]))
			}
		}
		body := strings.TrimSpace(strings.Join(lines[i+1:j], "\n"))
		if heading == decisionHeading && !a.hasDecide {
			a.decision, a.hasDecide = body, true
		} else {
			rest.WriteString(heading + "\n" + body + "\n")
		}
		i = j
	}
	a.rest = rest.String()
	return a
}

// unlinked is an ADR's text as the history half compares it: the target of
// every link dropped, so that repointing a link is no change. A link is
// what the links check takes for one (linkRe and refDefRe, outside a fenced
// block and outside inline code); anything else that looks like a target is
// the ADR's own words and is kept.
func unlinked(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	fenced := false
	for i, line := range lines {
		if fenceRe.MatchString(line) {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		var b strings.Builder
		at := 0
		for _, span := range codeRe.FindAllStringIndex(line, -1) {
			b.WriteString(dropTargets(line[at:span[0]]))
			b.WriteString(line[span[0]:span[1]])
			at = span[1]
		}
		b.WriteString(dropTargets(line[at:]))
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// dropTargets empties the target of every link in a piece of a line that
// holds no inline code.
func dropTargets(s string) string {
	drop := func(re *regexp.Regexp, s string) string {
		var b strings.Builder
		at := 0
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			b.WriteString(s[at:m[2]])
			at = m[3]
		}
		b.WriteString(s[at:])
		return b.String()
	}
	return drop(refDefRe, drop(linkRe, s))
}

// decisions is the rule. It reads the directory itself, not the pages the
// gate loaded, so that a file whose name is wrong is still seen.
func (c *checker) decisions() {
	index := c.docs[DecisionsIndex]
	if index == nil {
		c.find("decisions", DecisionsIndex, "the decisions index is missing")
		return
	}
	if !c.isFile(DecisionsTemplate) {
		c.find("decisions", DecisionsTemplate, "the ADR template is missing")
	}
	indexed := map[string]bool{}
	if list := index.section(DecisionsList); list == nil {
		c.find("decisions", DecisionsIndex, "there is no `## %s` section to list the ADRs in", DecisionsList)
	} else {
		for _, l := range index.bodyLinks(list.from, list.to) {
			if dest, _, ok := resolve(DecisionsIndex, l.target); ok {
				indexed[dest] = true
			}
		}
	}
	entries, err := os.ReadDir(c.abs(DecisionsDir))
	if err != nil {
		c.find("decisions", DecisionsDir, "%v", err)
		return
	}
	byNumber := map[string]string{}
	parsed := map[string]adr{}
	texts := map[string]string{}
	var rels []string
	for _, e := range entries {
		rel := path.Join(DecisionsDir, e.Name())
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || rel == DecisionsIndex || rel == DecisionsTemplate {
			continue
		}
		m := adrNameRe.FindStringSubmatch(e.Name())
		if m == nil || m[1] == "0000" {
			c.find("decisions", rel, "an ADR is named NNNN-short-title.md, the number from 0001 and the title in lowercase words joined by hyphens")
			continue
		}
		num := m[1]
		if other, taken := byNumber[num]; taken {
			c.find("decisions", rel, "reuses the number %s of %s; give one of the two the next free number", num, path.Base(other))
		} else {
			byNumber[num] = rel
		}
		data, err := os.ReadFile(c.abs(rel))
		if err != nil {
			c.find("decisions", rel, "%v", err)
			continue
		}
		a := parseADR(string(data))
		parsed[rel], texts[rel] = a, string(data)
		rels = append(rels, rel)
		if rest, ok := strings.CutPrefix(a.title, "# "+num+". "); !ok || strings.TrimSpace(rest) == "" {
			c.find("decisions", rel, "the first line must be `# %s. ` and the decision's title", num)
		}
		if !a.hasDecide {
			c.find("decisions", rel, "there is no `%s` section", decisionHeading)
		}
		if !indexed[rel] {
			c.find("decisions", DecisionsIndex, "%s is not listed under `## %s`", e.Name(), DecisionsList)
		}
		for _, s := range a.stray {
			c.find("decisions", rel, "a Corrected entry belongs in the header, after the Supersedes line: %q", s)
		}
		for _, entry := range a.corrected {
			if m := adrCorrectedRe.FindStringSubmatch(entry); m == nil {
				c.find("decisions", rel, "a correction's entry is `- **Corrected:** YYYY-MM-DD: <what was wrong, and what the ADR says now>`")
			} else if _, err := time.Parse("2006-01-02", m[1]); err != nil {
				c.find("decisions", rel, "the Corrected entry's date %s is not a date", m[1])
			}
		}
	}
	// The Status lines, once every number is known: a superseding ADR exists.
	for _, rel := range rels {
		a := parsed[rel]
		if len(a.statuses) != 1 {
			c.find("decisions", rel, "there must be one `- **Status:**` line in the header; there are %d", len(a.statuses))
			continue
		}
		m := adrStatusRe.FindStringSubmatch(a.statuses[0])
		if m == nil {
			c.find("decisions", rel, "the Status is `Accepted`, `Accepted; superseded in part by NNNN`, `Superseded by NNNN` or `Rejected`, never Proposed: a decision that still waits is a work item tagged `decision`")
			continue
		}
		for _, by := range m[1:] {
			if by != "" && byNumber[by] == "" {
				c.find("decisions", rel, "its Status names ADR %s, which is not in %s", by, DecisionsDir)
			}
		}
	}
	c.sum.Decisions = len(rels)
	c.decisionsHistory(texts)
}

// decisionsHistory is the half that reads git: every landed ADR keeps its
// text. texts is every ADR of the tree, by its path.
func (c *checker) decisionsHistory(texts map[string]string) {
	if _, err := os.Lstat(c.abs(".git")); err != nil {
		return // not a repository: no history to hold the ADRs against
	}
	const unchecked = "so the rule that a landed ADR keeps its text was not checked"
	top, err := c.git(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		c.find("decisions", DecisionsDir, "git could not read this repository, %s: %v", unchecked, err)
		return
	}
	if !sameDir(strings.TrimSpace(top), c.root) {
		c.find("decisions", DecisionsDir, "git reads %s as the repository of this tree, %s", strings.TrimSpace(top), unchecked)
		return
	}
	if _, err := c.git(nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return // a repository with no commit yet
	}
	base, branch := c.landed()
	listing, err := c.git(nil, "ls-tree", "-r", "--name-only", "-z", base, "--", DecisionsDir+"/")
	if err != nil {
		c.find("decisions", DecisionsDir, "%s: %v", unchecked, err)
		return
	}
	var landed []string
	numbers := map[string]int{}
	for rel := range strings.SplitSeq(listing, "\x00") {
		if m := adrNameRe.FindStringSubmatch(path.Base(rel)); m != nil && rel != DecisionsTemplate && path.Dir(rel) == DecisionsDir {
			landed = append(landed, rel)
			numbers[m[1]]++
		}
	}
	sort.Strings(landed)
	was, err := c.gitBlobs(base, landed)
	if err != nil {
		c.find("decisions", DecisionsDir, "%s: %v", unchecked, err)
		return
	}
	from := "its text on " + branch
	isLanded := map[string]bool{}
	for _, rel := range landed {
		isLanded[rel] = true
	}
	for _, rel := range landed {
		before := parseADR(unlinked(was[rel]))
		if now, ok := texts[rel]; ok {
			c.heldAgainst(rel, before, parseADR(unlinked(now)), from)
			continue
		}
		// Gone from the tree. The one rename there is: an ADR whose number
		// another landed ADR also carries is given a number of its own.
		if numbers[adrNameRe.FindStringSubmatch(path.Base(rel))[1]] > 1 && renumbered(before, texts, isLanded) {
			continue
		}
		c.find("decisions", rel, "this ADR landed on %s and is gone from the tree; an ADR is superseded by a new one, never removed or renamed", branch)
	}
}

// renumbered reports whether the tree holds, under a name that had not
// landed, the landed ADR before with nothing changed but its number.
func renumbered(before adr, texts map[string]string, isLanded map[string]bool) bool {
	unnumbered := func(a adr) adr {
		a.title = adrTitleRe.ReplaceAllString(a.title, "# ")
		return a
	}
	want := unnumbered(before)
	for rel, text := range texts {
		if isLanded[rel] {
			continue
		}
		got := unnumbered(parseADR(unlinked(text)))
		if got.title == want.title && slices.Equal(got.statuses, want.statuses) && slices.Equal(got.corrected, want.corrected) &&
			slices.Equal(got.fields, want.fields) && got.decision == want.decision && got.rest == want.rest {
			return true
		}
	}
	return false
}

// landed is the commit the ADRs are held against and the branch it is on:
// the merge base of HEAD and the remote's default branch, or HEAD where the
// repository has no remote branch named master or main. The remote named
// origin is preferred, then any other.
func (c *checker) landed() (base, branch string) {
	var refs []string
	if head, err := c.git(nil, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil && strings.TrimSpace(head) != "" {
		refs = append(refs, strings.TrimSpace(head))
	}
	refs = append(refs, "refs/remotes/origin/master", "refs/remotes/origin/main")
	if others, err := c.git(nil, "for-each-ref", "--format=%(refname)", "refs/remotes/*/master", "refs/remotes/*/main"); err == nil {
		refs = append(refs, strings.Fields(others)...)
	}
	for _, ref := range refs {
		if _, err := c.git(nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			continue
		}
		if mb, err := c.git(nil, "merge-base", "HEAD", ref); err == nil && strings.TrimSpace(mb) != "" {
			return strings.TrimSpace(mb), strings.TrimPrefix(ref, "refs/remotes/")
		}
	}
	return "HEAD", "HEAD"
}

// heldAgainst reports what an ADR changed from the text of it that landed.
func (c *checker) heldAgainst(rel string, was, now adr, from string) {
	if was.title != now.title {
		c.find("decisions", rel, "its title changed from %s; an ADR keeps its title", from)
	}
	if !slices.Equal(was.fields, now.fields) {
		c.find("decisions", rel, "a header line other than Status changed from %s; only the Status line changes, and a correction adds a Corrected entry", from)
	}
	if was.decision != now.decision {
		c.find("decisions", rel, "its Decision section changed from %s; a change of mind is a new ADR that supersedes this one", from)
	}
	if len(now.corrected) < len(was.corrected) || !slices.Equal(was.corrected, now.corrected[:len(was.corrected)]) {
		c.find("decisions", rel, "a Corrected entry it had in %s was changed or removed; earlier corrections stay as they are", from)
		return
	}
	if was.rest != now.rest && len(now.corrected) == len(was.corrected) {
		c.find("decisions", rel, "its text changed from %s beyond the Status line; to correct a fact that was wrong, add `- **Corrected:** YYYY-MM-DD: <what was wrong, and what the ADR says now>` to its header", from)
	}
}

// gitBlobs reads the files rels as the commit base has them, in one git
// process (`cat-file --batch`), which prints each as a header line, its
// bytes and a newline.
func (c *checker) gitBlobs(base string, rels []string) (map[string]string, error) {
	out := map[string]string{}
	if len(rels) == 0 {
		return out, nil
	}
	var names strings.Builder
	for _, rel := range rels {
		names.WriteString(base + ":" + rel + "\n")
	}
	raw, err := c.git(strings.NewReader(names.String()), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(strings.NewReader(raw))
	for _, rel := range rels {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("git cat-file: no answer for %s", rel)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("git cat-file: %s is %s", rel, strings.TrimSpace(header))
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("git cat-file: %s has no size: %s", rel, strings.TrimSpace(header))
		}
		body := make([]byte, size+1) // the bytes and the newline after them
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, fmt.Errorf("git cat-file: %s is cut short", rel)
		}
		out[rel] = string(body[:size])
	}
	return out, nil
}

// git runs git in the tree and returns its stdout: an argv array, the
// allow-listed environment, no shell, a deadline that catches a hang, and
// only commands whose output no configuration of the user's changes
// (rev-parse, symbolic-ref, for-each-ref, merge-base, ls-tree, cat-file).
func (c *checker) git(stdin io.Reader, args ...string) (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("git is not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	argv := append([]string{"-C", c.root}, args...)
	//nolint:forbidigo,gosec // the documentation gate is dev-only and never ships; this is its one spawn: git, an argv array of fixed words plus a path of the tree, the allow-listed environment, no shell
	cmd := exec.CommandContext(ctx, git, argv...)
	cmd.Env = adapterkit.ChildEnv(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = stdin
	cmd.Cancel = func() error { return adapterkit.CancelProcess(cmd.Process) }
	cmd.WaitDelay = gitWaitDelay
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return out.String(), nil
}

// sameDir reports whether two paths name one directory: the directories
// themselves are compared, not their spellings, so a path through a link or
// in another case on a case-insensitive file system is the same one.
func sameDir(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(sa, sb)
}
