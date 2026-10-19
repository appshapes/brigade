// Package docscheck is the documentation gate of card 110: the structural
// rules every release must pass, checked offline against the tree.
//
// The documentation is one tree for humans and one for agents. Humans enter
// at README.md, which names five personas; each persona root names its
// topics; each topic root names its actions with a TL;DR and a Details link;
// the leaves hold the details. Agents enter at CLAUDE.md, whose @imports load
// the rule files under docs/agent/repository/. A harness Brigade ships a
// package for is one entry in docs/harnesses.json and six pages the gate
// requires. Run reports every rule the tree breaks; nothing here reaches the
// network, so a site going down is never a red build (cavalry ADR 0006).
package docscheck

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// The files and directories the gate knows by name, relative to the root.
const (
	ReadmeFile       = "README.md"
	AgentsFile       = "AGENTS.md"
	ClaudeFile       = "CLAUDE.md"
	ChangelogFile    = "CHANGELOG.md"
	MakefileFile     = "Makefile"
	VersionFile      = "plugin/bin/VERSION"
	PluginManifest   = "plugin/.claude-plugin/plugin.json"
	RegistryFile     = "docs/harnesses.json"
	DocsDir          = "docs"
	ResearchDir      = "docs/research"
	ExperimentsDir   = "docs/experiments"
	ExperimentsIndex = "docs/experiments/README.md"
	RulesDir         = "docs/agent/repository"
	AgentTeamDir     = "docs/agent/in-a-team"
	OptionsPage      = "docs/team-member/configure/options.md"
	MakeTargetsPage  = "docs/developer/reference/make-targets.md"

	// IfYouWantTo is the heading of the navigation table every persona root
	// and topic root carries.
	IfYouWantTo = "If you want to"
	// DetailsPrefix starts the line of an action section that names its leaf.
	DetailsPrefix = "Details:"
	// MaxOverviewSentences bounds a persona root's topic section.
	MaxOverviewSentences = 2
	// MaxActionLines bounds a topic root's action section: a TL;DR, not the page.
	MaxActionLines = 20
	// PreReleaseVersion is the pin of a tree that has not been released.
	PreReleaseVersion = "0.0.0"
)

// Persona is one of the five readers the tree is organised for.
type Persona struct {
	Dir   string // the directory under docs/
	Title string // the H1 of its root page
}

// Personas is the order README.md lists them in.
var Personas = []Persona{
	{Dir: "project-owner", Title: "Project Owner"},
	{Dir: "team-admin", Title: "Team Admin"},
	{Dir: "team-member", Title: "Team Member"},
	{Dir: "developer", Title: "Developer"},
	{Dir: "agent", Title: "AI Agent"},
}

// referenceDirs are directories under a persona that hold generated or
// reference pages rather than a topic: no README.md, no navigation table.
var referenceDirs = map[string]bool{
	"docs/developer/reference": true,
}

// memberTopics are the member topics a harness needs a page in.
var memberTopics = []string{"install", "join", "update", "configure", "leave"}

// manifestDirs name a harness package by the manifest directory it carries;
// a package found this way must be in the registry.
var manifestDirs = []string{".claude-plugin", ".muse-plugin", ".codex-plugin", ".grok-plugin"}

// Harness is one entry of docs/harnesses.json.
type Harness struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor"`
	Host    string `json:"host"`
	Package string `json:"package"`
	Skills  string `json:"skills"`
}

// Registry is docs/harnesses.json.
type Registry struct {
	Comment   string    `json:"_comment"`
	Harnesses []Harness `json:"harnesses"`
}

// Finding is one rule the tree breaks.
type Finding struct {
	Check   string // readme, persona, topic, links, reach, harness, agent, options, make, changelog
	Path    string // the file the finding is about, relative to the root
	Message string
}

func (f Finding) String() string {
	return f.Check + ": " + f.Path + ": " + f.Message
}

// Summary is what a passing run counted.
type Summary struct {
	Pages     int
	Links     int
	Harnesses int
	Targets   int
}

func (s Summary) String() string {
	return fmt.Sprintf("%d pages, %d links, %d harnesses, %d make targets", s.Pages, s.Links, s.Harnesses, s.Targets)
}

// Run checks the repository at root and returns every finding. An error is
// a tree the gate cannot read at all (no README.md, an unreadable registry),
// not a finding.
func Run(root string) ([]Finding, Summary, error) {
	c := &checker{root: root, docs: map[string]*doc{}}
	if err := c.load(); err != nil {
		return nil, Summary{}, err
	}
	c.readme()
	c.personas()
	c.links()
	c.reach()
	c.harnesses()
	c.agent()
	c.options()
	c.makeTargets()
	c.changelog()
	sort.SliceStable(c.findings, func(i, j int) bool {
		if c.findings[i].Check != c.findings[j].Check {
			return c.findings[i].Check < c.findings[j].Check
		}
		return c.findings[i].Path < c.findings[j].Path
	})
	return c.findings, c.sum, nil
}

// LoadRegistry reads docs/harnesses.json under root.
func LoadRegistry(root string) (*Registry, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(RegistryFile)))
	if err != nil {
		return nil, err
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("%s: %w", RegistryFile, err)
	}
	return &reg, nil
}

// ---------------------------------------------------------------------------
// the markdown model

type heading struct {
	level int
	text  string
	slug  string
	line  int // 0-based
}

type link struct {
	target string
	line   int
}

type doc struct {
	rel      string
	raw      []string // every line as written
	text     []string // fenced blocks blanked, inline code removed
	headings []heading
	links    []link
}

var (
	fenceRe   = regexp.MustCompile("^\\s*(```|~~~)")
	codeRe    = regexp.MustCompile("`[^`\n]*`")
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	linkRe    = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	refDefRe  = regexp.MustCompile(`^\s*\[[^\]]+\]:\s*(\S+)`)
	schemeRe  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
	importRe  = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9_./-]+)`)
	tableRe   = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	tableSep  = regexp.MustCompile(`^\s*\|(\s*:?-+:?\s*\|)+\s*$`)
	listRe    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	targetRe  = regexp.MustCompile(`^([A-Za-z0-9_.-]+):.*##\s*(.*)$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

func parseDoc(rel string, data []byte) *doc {
	d := &doc{rel: rel}
	d.raw = strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	d.text = make([]string, len(d.raw))
	inFence := false
	slugs := map[string]int{}
	for i, line := range d.raw {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			d.text[i] = ""
			continue
		}
		if inFence {
			d.text[i] = ""
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			title := strings.ReplaceAll(m[2], "`", "")
			title = linkRe.ReplaceAllStringFunc(title, func(s string) string {
				return s[strings.Index(s, "[")+1 : strings.Index(s, "]")]
			})
			slug := githubSlug(title)
			if n, dup := slugs[slug]; dup {
				slugs[slug] = n + 1
				slug = fmt.Sprintf("%s-%d", slug, n)
			} else {
				slugs[slug] = 1
			}
			d.headings = append(d.headings, heading{level: len(m[1]), text: title, slug: slug, line: i})
		}
		stripped := codeRe.ReplaceAllString(line, "")
		d.text[i] = stripped
		for _, m := range linkRe.FindAllStringSubmatch(stripped, -1) {
			d.links = append(d.links, link{target: m[1], line: i})
		}
		if m := refDefRe.FindStringSubmatch(stripped); m != nil {
			d.links = append(d.links, link{target: m[1], line: i})
		}
	}
	return d
}

// githubSlug is GitHub's heading anchor: lowercased, punctuation dropped,
// spaces to hyphens.
func githubSlug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// canon is the looser comparison that survives slug dialects: letters and
// digits only.
func canon(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (d *doc) hasAnchor(anchor string) bool {
	anchor = strings.ToLower(anchor)
	for _, h := range d.headings {
		if h.slug == anchor || canon(h.slug) == canon(anchor) {
			return true
		}
	}
	return false
}

// h1 is the first level-1 heading, or nil.
func (d *doc) h1() *heading {
	for i := range d.headings {
		if d.headings[i].level == 1 {
			return &d.headings[i]
		}
	}
	return nil
}

// sections returns the level-2 headings with the line range of each body:
// [from, to) in line numbers, the heading line excluded.
type section struct {
	heading
	from, to int
}

func (d *doc) sections() []section {
	var out []section
	for i, h := range d.headings {
		if h.level != 2 {
			continue
		}
		to := len(d.raw)
		for _, n := range d.headings[i+1:] {
			if n.level <= 2 {
				to = n.line
				break
			}
		}
		out = append(out, section{heading: h, from: h.line + 1, to: to})
	}
	return out
}

func (d *doc) section(title string) *section {
	for _, s := range d.sections() {
		if canon(s.text) == canon(title) {
			return &s
		}
	}
	return nil
}

// bodyLinks are the links whose line is inside [from, to).
func (d *doc) bodyLinks(from, to int) []link {
	var out []link
	for _, l := range d.links {
		if l.line >= from && l.line < to {
			out = append(out, l)
		}
	}
	return out
}

// tableRows returns the link targets of each data row of the first table in
// [from, to), one slice per row, and whether a table was found.
func (d *doc) tableRows(from, to int) ([][]string, bool) {
	var rows [][]string
	seen := false
	for i := from; i < to; i++ {
		line := d.text[i]
		if !tableRe.MatchString(line) {
			if seen {
				break
			}
			continue
		}
		if !seen {
			seen = true
			continue // the header row
		}
		if tableSep.MatchString(line) {
			continue
		}
		var targets []string
		for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
			targets = append(targets, m[1])
		}
		rows = append(rows, targets)
	}
	return rows, seen
}

// listItems returns the link targets of each list item in [from, to).
func (d *doc) listItems(from, to int) [][]string {
	var items [][]string
	for i := from; i < to; i++ {
		line := d.text[i]
		if !listRe.MatchString(line) {
			continue
		}
		var targets []string
		for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
			targets = append(targets, m[1])
		}
		items = append(items, targets)
	}
	return items
}

// sentences counts the sentences in [from, to): terminators followed by a
// space or the end of a line, link targets and code removed, a few
// abbreviations tolerated.
func (d *doc) sentences(from, to int) int {
	var b strings.Builder
	for i := from; i < to; i++ {
		b.WriteString(linkRe.ReplaceAllStringFunc(d.text[i], func(s string) string {
			return s[strings.Index(s, "[")+1 : strings.Index(s, "]")]
		}))
		b.WriteByte('\n')
	}
	text := b.String()
	for _, abbr := range []string{"e.g.", "i.e.", "etc.", "vs."} {
		text = strings.ReplaceAll(text, abbr, strings.ReplaceAll(abbr, ".", ""))
	}
	n := 0
	runes := []rune(text)
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i+1 == len(runes) || unicode.IsSpace(runes[i+1]) || runes[i+1] == ')' || runes[i+1] == '"' {
			n++
		}
	}
	return n
}

// contentLines counts the non-blank lines in [from, to).
func (d *doc) contentLines(from, to int) int {
	n := 0
	for i := from; i < to; i++ {
		if strings.TrimSpace(d.raw[i]) != "" {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// the checker

type checker struct {
	root     string
	docs     map[string]*doc // every markdown file the gate reads, by slash path
	findings []Finding
	sum      Summary
	reg      *Registry
}

func (c *checker) find(check, rel, format string, args ...any) {
	c.findings = append(c.findings, Finding{Check: check, Path: rel, Message: fmt.Sprintf(format, args...)})
}

func (c *checker) abs(rel string) string { return filepath.Join(c.root, filepath.FromSlash(rel)) }

func (c *checker) exists(rel string) bool {
	_, err := os.Stat(c.abs(rel))
	return err == nil
}

func (c *checker) isFile(rel string) bool {
	st, err := os.Stat(c.abs(rel))
	return err == nil && st.Mode().IsRegular()
}

// load reads every markdown file the gate checks: the root pages, the
// package pages, the changelog and docs/ outside research/.
func (c *checker) load() error {
	for _, rel := range []string{ReadmeFile, AgentsFile, ClaudeFile, ChangelogFile} {
		if err := c.loadDoc(rel); err != nil {
			return err
		}
	}
	reg, err := LoadRegistry(c.root)
	if err != nil {
		return err
	}
	c.reg = reg
	for _, h := range reg.Harnesses {
		rel := path.Join(h.Package, "README.md")
		if c.isFile(rel) {
			if err := c.loadDoc(rel); err != nil {
				return err
			}
		}
	}
	err = filepath.WalkDir(c.abs(DocsDir), func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, c.root+string(filepath.Separator)))
		if d.IsDir() {
			if rel == ResearchDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".md") {
			return c.loadDoc(rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.sum.Pages = len(c.docs)
	return nil
}

func (c *checker) loadDoc(rel string) error {
	if _, done := c.docs[rel]; done {
		return nil
	}
	data, err := os.ReadFile(c.abs(rel))
	if err != nil {
		return err
	}
	c.docs[rel] = parseDoc(rel, data)
	return nil
}

// resolve turns a link target seen in rel into a slash path relative to the
// root and an anchor; ok is false for an external target.
func resolve(rel, target string) (dest, anchor string, ok bool) {
	if schemeRe.MatchString(target) || strings.HasPrefix(target, "<") || strings.HasPrefix(target, "//") {
		return "", "", false
	}
	p, anchor, _ := strings.Cut(target, "#")
	if p == "" {
		return rel, anchor, true
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(strings.TrimPrefix(p, "/")), anchor, true
	}
	return path.Clean(path.Join(path.Dir(rel), p)), anchor, true
}

// imports returns the @path imports on one code-stripped line of CLAUDE.md:
// a token beginning with @ that names a path (it contains a slash or ends in
// .md), so an @handle or an address is never one.
func imports(line string) []string {
	var out []string
	for _, m := range importRe.FindAllStringSubmatch(line, -1) {
		if strings.Contains(m[1], "/") || strings.HasSuffix(m[1], ".md") {
			out = append(out, m[1])
		}
	}
	return out
}

// --- 1. readme ------------------------------------------------------------

func (c *checker) readme() {
	d := c.docs[ReadmeFile]
	h1 := d.h1()
	if h1 == nil || d.headings[0].level != 1 {
		c.find("readme", ReadmeFile, "the first heading must be the H1 title")
	}
	h2 := d.sections()
	for _, h := range d.headings {
		if h.level > 2 {
			c.find("readme", ReadmeFile, "line %d: README.md has no headings below H2; %q belongs on a persona page", h.line+1, h.text)
		}
	}
	if len(h2) != 1 {
		c.find("readme", ReadmeFile, "README.md must have exactly one H2 section, the personas; it has %d", len(h2))
		return
	}
	items := d.listItems(h2[0].from, h2[0].to)
	var got []string
	for _, it := range items {
		if len(it) != 1 {
			c.find("readme", ReadmeFile, "each persona item carries exactly one link, to the persona's root page; one carries %d", len(it))
			continue
		}
		dest, _, _ := resolve(ReadmeFile, it[0])
		got = append(got, dest)
	}
	var want []string
	for _, p := range Personas {
		want = append(want, path.Join(DocsDir, p.Dir, "README.md"))
	}
	if !slices.Equal(got, want) {
		c.find("readme", ReadmeFile, "the %q section must list exactly the five persona roots in order %v; it lists %v", h2[0].text, want, got)
	}
}

// --- 2. persona and topic roots ---------------------------------------------

func (c *checker) personas() {
	for _, p := range Personas {
		dir := path.Join(DocsDir, p.Dir)
		rel := path.Join(dir, "README.md")
		d, ok := c.docs[rel]
		if !ok {
			c.find("persona", rel, "the %s root page is missing", p.Title)
			continue
		}
		if h := d.h1(); h == nil || canon(h.text) != canon(p.Title) {
			c.find("persona", rel, "the H1 must be %q", p.Title)
		}
		topics := c.subdirs(dir)
		var topicDirs []string
		for _, t := range topics {
			tdir := path.Join(dir, t)
			troot := path.Join(tdir, "README.md")
			if referenceDirs[tdir] {
				continue
			}
			if !c.isFile(troot) {
				c.find("persona", troot, "every topic directory has a README.md (the topic root); add it, or register %s as a reference directory", tdir)
				continue
			}
			topicDirs = append(topicDirs, t)
			c.topic(p, t, troot)
		}
		// The navigation table.
		nav := d.section(IfYouWantTo)
		if nav == nil {
			c.find("persona", rel, "a persona root has an %q table", "## "+IfYouWantTo)
		} else {
			rows, found := d.tableRows(nav.from, nav.to)
			if !found {
				c.find("persona", rel, "the %q section holds a table", IfYouWantTo)
			}
			linked := map[string]bool{}
			for _, row := range rows {
				if len(row) == 0 {
					c.find("persona", rel, "every %q row links to a topic root", IfYouWantTo)
					continue
				}
				for _, t := range row {
					dest, _, external := resolve(rel, t)
					if !external {
						continue
					}
					if path.Dir(path.Dir(dest)) != dir || path.Base(dest) != "README.md" || !slices.Contains(topicDirs, path.Base(path.Dir(dest))) {
						c.find("persona", rel, "line of %q: %s is not one of this persona's topic roots", IfYouWantTo, t)
						continue
					}
					linked[path.Base(path.Dir(dest))] = true
				}
			}
			for _, t := range topicDirs {
				if !linked[t] {
					c.find("persona", rel, "the %q table has no row for the topic %s/", IfYouWantTo, t)
				}
			}
		}
		// One section per topic, two sentences at most, linking to the topic root.
		sectioned := map[string]bool{}
		for _, s := range d.sections() {
			if canon(s.text) == canon(IfYouWantTo) {
				continue
			}
			var topic string
			for _, l := range d.bodyLinks(s.from, s.to) {
				dest, _, external := resolve(rel, l.target)
				if !external {
					continue
				}
				if path.Dir(path.Dir(dest)) == dir && path.Base(dest) == "README.md" && slices.Contains(topicDirs, path.Base(path.Dir(dest))) {
					topic = path.Base(path.Dir(dest))
					break
				}
			}
			if topic == "" {
				c.find("persona", rel, "section %q links to no topic root of this persona; a persona root has only the %q table and one section per topic", s.text, IfYouWantTo)
				continue
			}
			if sectioned[topic] {
				c.find("persona", rel, "section %q: the topic %s/ already has a section", s.text, topic)
			}
			sectioned[topic] = true
			if n := d.sentences(s.from, s.to); n > MaxOverviewSentences {
				c.find("persona", rel, "section %q has %d sentences; a topic overview has at most %d, the details belong on the topic root", s.text, n, MaxOverviewSentences)
			}
		}
		for _, t := range topicDirs {
			if !sectioned[t] {
				c.find("persona", rel, "the topic %s/ has no section on the persona root", t)
			}
		}
	}
}

// subdirs lists the directories directly under rel, sorted.
func (c *checker) subdirs(rel string) []string {
	entries, err := os.ReadDir(c.abs(rel))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (c *checker) topic(p Persona, name, rel string) {
	d := c.docs[rel]
	if d == nil {
		return
	}
	dir := path.Dir(rel)
	if d.h1() == nil {
		c.find("topic", rel, "a topic root starts with an H1")
	}
	sections := d.sections()
	nav := d.section(IfYouWantTo)
	if nav == nil {
		c.find("topic", rel, "a topic root has an %q table", "## "+IfYouWantTo)
	}
	// Actions: every H2 but the table.
	type action struct {
		section
		details []string
	}
	var actions []action
	for _, s := range sections {
		if canon(s.text) == canon(IfYouWantTo) {
			continue
		}
		a := action{section: s}
		for i := s.from; i < s.to; i++ {
			if strings.HasPrefix(strings.TrimSpace(d.text[i]), DetailsPrefix) {
				for _, m := range linkRe.FindAllStringSubmatch(d.text[i], -1) {
					a.details = append(a.details, m[1])
				}
				if len(linkRe.FindAllString(d.text[i], -1)) == 0 {
					c.find("topic", rel, "section %q: the %s line carries no link", s.text, DetailsPrefix)
				}
			}
		}
		if len(a.details) == 0 {
			c.find("topic", rel, "section %q has no %q line linking to its detail page", s.text, DetailsPrefix)
		}
		if n := d.contentLines(s.from, s.to); n > MaxActionLines {
			c.find("topic", rel, "section %q is %d lines; an action section is a TL;DR of at most %d, the rest belongs on its detail page", s.text, n, MaxActionLines)
		}
		actions = append(actions, a)
	}
	if len(actions) == 0 {
		c.find("topic", rel, "a topic root has at least one action section")
	}
	// The table's rows name the actions by anchor.
	if nav != nil {
		rows, found := d.tableRows(nav.from, nav.to)
		if !found {
			c.find("topic", rel, "the %q section holds a table", IfYouWantTo)
		}
		named := map[string]bool{}
		for _, row := range rows {
			if len(row) == 0 {
				c.find("topic", rel, "every %q row links to an action section of this page", IfYouWantTo)
				continue
			}
			for _, t := range row {
				dest, anchor, external := resolve(rel, t)
				if !external {
					continue
				}
				if dest != rel || anchor == "" {
					c.find("topic", rel, "%q row %s: a topic root's table links to its own action sections (#anchor), not elsewhere", IfYouWantTo, t)
					continue
				}
				hit := false
				for _, a := range actions {
					if a.slug == strings.ToLower(anchor) || canon(a.slug) == canon(anchor) {
						named[a.slug] = true
						hit = true
					}
				}
				if !hit {
					c.find("topic", rel, "%q row %s names no action section of this page", IfYouWantTo, t)
				}
			}
		}
		for _, a := range actions {
			if !named[a.slug] {
				c.find("topic", rel, "action %q has no row in the %q table", a.text, IfYouWantTo)
			}
		}
	}
	// No orphan leaf: every page in the directory is a Details target.
	linked := map[string]bool{}
	for _, a := range actions {
		for _, t := range a.details {
			dest, _, external := resolve(rel, t)
			if external {
				linked[dest] = true
			}
		}
	}
	entries, err := os.ReadDir(c.abs(dir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		leaf := path.Join(dir, e.Name())
		if !linked[leaf] {
			c.find("topic", rel, "%s is a leaf no %s line of this topic root names (persona %s, topic %s)", e.Name(), DetailsPrefix, p.Dir, name)
		}
	}
}

// --- 4. links ----------------------------------------------------------------

func (c *checker) links() {
	rels := make([]string, 0, len(c.docs))
	for rel := range c.docs {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		d := c.docs[rel]
		for _, l := range d.links {
			dest, anchor, external := resolve(rel, l.target)
			if !external {
				continue
			}
			c.sum.Links++
			if !c.exists(dest) {
				c.find("links", rel, "line %d: %s does not exist", l.line+1, l.target)
				continue
			}
			if anchor == "" {
				continue
			}
			if !strings.HasSuffix(dest, ".md") {
				c.find("links", rel, "line %d: %s: an anchor on a page that is not markdown", l.line+1, l.target)
				continue
			}
			target := c.docs[dest]
			if target == nil {
				data, err := os.ReadFile(c.abs(dest))
				if err != nil {
					c.find("links", rel, "line %d: %s: %v", l.line+1, l.target, err)
					continue
				}
				target = parseDoc(dest, data)
				c.docs[dest] = target
			}
			if !target.hasAnchor(anchor) {
				c.find("links", rel, "line %d: %s: no heading in %s matches #%s", l.line+1, l.target, dest, anchor)
			}
		}
	}
	// CLAUDE.md's imports.
	for i, line := range c.docs[ClaudeFile].text {
		for _, target := range imports(line) {
			if target == "./CLAUDE.user.md" {
				continue // local-only, gitignored, absent in a fresh clone by design
			}
			dest, _, _ := resolve(ClaudeFile, target)
			if !c.isFile(dest) {
				c.find("links", ClaudeFile, "line %d: the import @%s names no file", i+1, target)
			}
		}
	}
}

// --- 5. reach ----------------------------------------------------------------

func (c *checker) reach() {
	seen := map[string]bool{ReadmeFile: true}
	queue := []string{ReadmeFile}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		d := c.docs[rel]
		if d == nil {
			data, err := os.ReadFile(c.abs(rel))
			if err != nil {
				continue
			}
			d = parseDoc(rel, data)
			c.docs[rel] = d
		}
		for _, l := range d.links {
			dest, _, external := resolve(rel, l.target)
			if !external || seen[dest] || !strings.HasSuffix(dest, ".md") || !c.isFile(dest) {
				continue
			}
			seen[dest] = true
			queue = append(queue, dest)
		}
	}
	rels := make([]string, 0, len(c.docs))
	for rel := range c.docs {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if !strings.HasPrefix(rel, DocsDir+"/") || seen[rel] {
			continue
		}
		c.find("reach", rel, "no chain of links from README.md reaches this page; link it from a topic root or an index")
	}
	// Every experiment report is in its index.
	index := c.docs[ExperimentsIndex]
	if index == nil {
		c.find("reach", ExperimentsIndex, "the experiments index is missing")
		return
	}
	indexed := map[string]bool{}
	for _, l := range index.links {
		dest, _, external := resolve(ExperimentsIndex, l.target)
		if external {
			indexed[dest] = true
		}
	}
	entries, err := os.ReadDir(c.abs(ExperimentsDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		if rel := path.Join(ExperimentsDir, e.Name()); !indexed[rel] {
			c.find("reach", ExperimentsIndex, "%s is not listed in the experiments index", e.Name())
		}
	}
}

// --- 6. harnesses -------------------------------------------------------------

func (c *checker) harnesses() {
	ids := map[string]bool{}
	packages := map[string]bool{}
	for _, h := range c.reg.Harnesses {
		c.sum.Harnesses++
		if h.ID == "" || h.Name == "" || h.Package == "" || h.Skills == "" || h.Host == "" {
			c.find("harness", RegistryFile, "entry %q: id, name, host, package and skills are all required", h.ID)
			continue
		}
		if ids[h.ID] {
			c.find("harness", RegistryFile, "entry %q appears twice", h.ID)
		}
		ids[h.ID] = true
		packages[path.Clean(h.Package)] = true
		if !c.exists(h.Package) {
			c.find("harness", RegistryFile, "entry %q: package directory %s does not exist", h.ID, h.Package)
		}
		for _, topic := range memberTopics {
			page := path.Join(DocsDir, "team-member", topic, h.ID+".md")
			if !c.isFile(page) {
				c.find("harness", page, "the %s page of harness %q is missing", topic, h.Name)
			}
		}
		agentPage := path.Join(AgentTeamDir, h.ID+".md")
		ad := c.docs[agentPage]
		if ad == nil {
			c.find("harness", agentPage, "the agent page of harness %q is missing", h.Name)
		} else {
			skills, err := os.ReadDir(c.abs(h.Skills))
			if err != nil {
				c.find("harness", RegistryFile, "entry %q: skills directory %s: %v", h.ID, h.Skills, err)
			}
			text := strings.Join(ad.raw, "\n")
			for _, s := range skills {
				if !s.IsDir() {
					continue
				}
				skill := path.Join(h.Skills, s.Name(), "SKILL.md")
				if !c.isFile(skill) {
					continue
				}
				if !strings.Contains(text, skill) {
					c.find("harness", agentPage, "the skill %s is not named on the agent page of harness %q", skill, h.Name)
				}
			}
		}
		readme := path.Join(h.Package, "README.md")
		rd := c.docs[readme]
		if rd == nil {
			c.find("harness", readme, "the package page of harness %q is missing", h.Name)
			continue
		}
		install := path.Join(DocsDir, "team-member", "install", h.ID+".md")
		linked := false
		for _, l := range rd.links {
			if dest, _, external := resolve(readme, l.target); external && dest == install {
				linked = true
			}
		}
		if !linked {
			c.find("harness", readme, "the package page links to the member's install page %s", install)
		}
	}
	// A package the registry does not know.
	err := filepath.WalkDir(c.root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || !d.IsDir() {
			return nil //nolint:nilerr // a directory that cannot be read is not a package
		}
		relPath, err := filepath.Rel(c.root, p)
		if err != nil || relPath == "." {
			return nil //nolint:nilerr // the root itself is walked, never skipped
		}
		rel := filepath.ToSlash(relPath)
		if rel == ".git" || rel == "node_modules" || rel == ".ignored" || rel == ".context" || rel == DocsDir || strings.HasPrefix(rel, "dist") {
			return filepath.SkipDir
		}
		if strings.Count(rel, "/") > 3 {
			return filepath.SkipDir
		}
		if slices.Contains(manifestDirs, d.Name()) {
			// A manifest directory with a plugin.json is a package; the repository root's .claude-plugin/ holds
			// the marketplace file only and is not one.
			if _, err := os.Stat(filepath.Join(p, "plugin.json")); err == nil {
				pkg := path.Dir(rel)
				if !packages[pkg] {
					c.find("harness", RegistryFile, "%s is a harness package (it carries %s/plugin.json) that the registry does not list; add it with its six pages", pkg, d.Name())
				}
			}
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		c.find("harness", RegistryFile, "walking the tree: %v", err)
	}
}

// --- 7. agent ------------------------------------------------------------------

func (c *checker) agent() {
	claude := c.docs[ClaudeFile]
	imported := map[string]bool{}
	for _, line := range claude.text {
		for _, target := range imports(line) {
			dest, _, _ := resolve(ClaudeFile, target)
			imported[dest] = true
		}
	}
	agents := c.docs[AgentsFile]
	linked := map[string]bool{}
	for _, l := range agents.links {
		if dest, _, external := resolve(AgentsFile, l.target); external {
			linked[dest] = true
		}
	}
	entries, err := os.ReadDir(c.abs(RulesDir))
	if err != nil {
		c.find("agent", RulesDir, "the rule files are missing: %v", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		rel := path.Join(RulesDir, e.Name())
		if !imported[rel] {
			c.find("agent", ClaudeFile, "the rule file %s is not imported (@%s)", rel, rel)
		}
		if !linked[rel] {
			c.find("agent", AgentsFile, "the rule file %s is not linked", rel)
		}
	}
}

// --- 8. options ----------------------------------------------------------------

func (c *checker) options() {
	data, err := os.ReadFile(c.abs(PluginManifest))
	if err != nil {
		c.find("options", PluginManifest, "%v", err)
		return
	}
	var manifest struct {
		UserConfig map[string]any `json:"userConfig"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		c.find("options", PluginManifest, "%v", err)
		return
	}
	page := c.docs[OptionsPage]
	if page == nil {
		c.find("options", OptionsPage, "the options page is missing")
		return
	}
	text := strings.Join(page.raw, "\n")
	keys := make([]string, 0, len(manifest.UserConfig))
	for k := range manifest.UserConfig {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.Contains(text, "`"+k+"`") {
			c.find("options", OptionsPage, "the option `%s` of %s is not documented", k, PluginManifest)
		}
	}
}

// --- 9. make targets -------------------------------------------------------------

// GenerateMakeTargets renders the make-target reference page from the
// Makefile's `target: ## help` lines, in Makefile order.
func GenerateMakeTargets(root string) ([]byte, int, error) {
	data, err := os.ReadFile(filepath.Join(root, MakefileFile))
	if err != nil {
		return nil, 0, err
	}
	var b bytes.Buffer
	b.WriteString("# make targets\n\n")
	b.WriteString("Every target with a help line, in the Makefile's order; `make help` prints the same list. Generated by\n")
	b.WriteString("`make docs-generate` from the Makefile, and `make docs-check` fails when this page is stale, so edit the\n")
	b.WriteString("Makefile's `##` comment, never this page. A trailing ` (CI)` marks a target a workflow step runs.\n\n")
	b.WriteString("| Target | What it does |\n| --- | --- |\n")
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		m := targetRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n++
		fmt.Fprintf(&b, "| `%s` | %s |\n", m[1], strings.ReplaceAll(strings.TrimSpace(m[2]), "|", "\\|"))
	}
	return b.Bytes(), n, nil
}

func (c *checker) makeTargets() {
	want, n, err := GenerateMakeTargets(c.root)
	if err != nil {
		c.find("make", MakefileFile, "%v", err)
		return
	}
	c.sum.Targets = n
	got, err := os.ReadFile(c.abs(MakeTargetsPage))
	if err != nil {
		c.find("make", MakeTargetsPage, "the generated reference is missing; run `make docs-generate`")
		return
	}
	if !bytes.Equal(bytes.TrimRight(got, "\n"), bytes.TrimRight(want, "\n")) {
		c.find("make", MakeTargetsPage, "stale against the Makefile's help lines; run `make docs-generate` and commit the result")
	}
}

// --- 10. changelog -----------------------------------------------------------------

func (c *checker) changelog() {
	data, err := os.ReadFile(c.abs(VersionFile))
	if err != nil {
		c.find("changelog", VersionFile, "%v", err)
		return
	}
	v := strings.TrimSpace(string(data))
	if v == PreReleaseVersion || v == "" {
		return
	}
	if !versionRe.MatchString(v) {
		c.find("changelog", VersionFile, "%q is not a version", v)
		return
	}
	for _, h := range c.docs[ChangelogFile].headings {
		if h.level == 2 && strings.HasPrefix(h.text, "["+v+"]") {
			return
		}
	}
	c.find("changelog", ChangelogFile, "no `## [%s]` section for the version %s pins; a release ships with its changelog section", v, VersionFile)
}

// ErrFindings is returned by Check when the tree breaks a rule.
var ErrFindings = errors.New("docscheck: the documentation breaks a rule")

// Check runs the gate and formats the result as the lines a person reads:
// one finding per line, then the summary. It returns ErrFindings when there
// is anything to fix.
func Check(root string) (string, error) {
	findings, sum, err := Run(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range findings {
		b.WriteString("docs-check: ")
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	if len(findings) > 0 {
		fmt.Fprintf(&b, "docs-check: %s, %d finding(s)\n", sum, len(findings))
		return b.String(), ErrFindings
	}
	fmt.Fprintf(&b, "docs-check: %s, ok\n", sum)
	return b.String(), nil
}
