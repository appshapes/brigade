package packagecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The pin of the Codex CLI and Grok Build packages (work item 424, ADR
// 0030). Both harnesses are given this repository as a plugin marketplace
// and read its marketplace file from the default branch. Until the pin each
// file named the package's directory, so a member had what `master` held
// when their harness last looked. Now each names the package at one commit,
// the commit of the last published release, and the release sequence is
// what moves it (scripts/release-pin.sh). A push to `master` changes
// nothing for a member; the commit that moves the pin does.
const (
	// CodexMarketplace and GrokMarketplace are the two files that carry
	// the pin.
	CodexMarketplace = ".agents/plugins/marketplace.json"
	GrokMarketplace  = ".grok-plugin/marketplace.json"
	// PinURL is the repository both entries fetch the package from.
	PinURL = "https://github.com/appshapes/brigade.git"
)

// pinned is one marketplace file's entry: where it is, the source kind its
// harness reads a subdirectory of a repository at a commit with, and the
// package's directory and manifest.
type pinned struct {
	file, kind, dir, manifest string
}

var pins = []pinned{
	{CodexMarketplace, "git-subdir", "codex/brigade", "codex/brigade/.codex-plugin/plugin.json"},
	{GrokMarketplace, "url", "grok/brigade", "grok/brigade/.grok-plugin/plugin.json"},
}

var (
	commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// A file's one line that carries the commit: the writer changes that
	// line and nothing else, so the file keeps its form.
	pinLineRE = regexp.MustCompile(`(?m)^([ \t]*"sha":[ \t]*")[0-9a-f]{40}(",?[ \t]*)$`)
)

// commitIn is the commit a marketplace file's one entry names, once the
// entry has the pinned form: a plugin `brigade` whose source is exactly the
// kind, this repository's URL, the package's directory and a full commit.
func (p pinned) commitIn(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.file)))
	if err != nil {
		return "", fmt.Errorf("missing %s", p.file)
	}
	var market struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string         `json:"name"`
			Source map[string]any `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &market); err != nil {
		return "", fmt.Errorf("%s is not JSON: %w", p.file, err)
	}
	if market.Name != "brigade" || len(market.Plugins) != 1 || market.Plugins[0].Name != "brigade" {
		return "", fmt.Errorf("%s does not name the one plugin brigade of the marketplace brigade", p.file)
	}
	src := market.Plugins[0].Source
	commit, _ := src["sha"].(string)
	if len(src) != 4 || src["source"] != p.kind || src["url"] != PinURL || src["path"] != p.dir || !commitRE.MatchString(commit) {
		return "", fmt.Errorf(`%s does not name the package at a release's commit: its "source" is exactly {"source": %q, "url": %q, "path": %q, "sha": "<the release's commit, 40 characters>"} (work item 424; the release sequence writes the commit, never a hand)`, p.file, p.kind, PinURL, p.dir)
	}
	return commit, nil
}

// Pin is where the two marketplace files stand.
type Pin struct {
	// Commit is the commit both files name.
	Commit string
	// Release is the release tag at that commit, "" where the tree cannot
	// say: no git repository, or a clone whose release tags do not reach
	// the release plugin/bin/VERSION pins.
	Release string
	// Ahead is the release plugin/bin/VERSION pins, when that release is
	// tagged and the two files still name another: a release whose last
	// step has not run yet, or did not run.
	Ahead string
}

// String is the pin in one line.
func (p Pin) String() string {
	switch {
	case p.Release == "":
		return fmt.Sprintf("the Codex CLI and Grok Build packages are pinned to the commit %s (the release tags are not here to read it against: `git fetch --tags`)", p.Commit)
	case p.Ahead != "":
		return fmt.Sprintf("the Codex CLI and Grok Build packages are still at %s (%s), and %s is tagged: their members get %s when `make release-pin version=%s work_item=<n>` has moved the pin", p.Release, p.Commit, p.Ahead, p.Ahead, strings.TrimPrefix(p.Ahead, "v"))
	default:
		return fmt.Sprintf("the Codex CLI and Grok Build packages are pinned to %s (%s)", p.Release, p.Commit)
	}
}

// ReadPin reads the pin of the tree at o.Root and holds it to its rules:
// both files have the pinned form and name one commit, and, where the tree
// has release tags, that commit is the commit of one and holds both
// packages.
func ReadPin(o Options) (Pin, error) {
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	c := &checker{name: "packages-pin", o: o}
	var pin Pin
	for _, p := range pins {
		commit, err := p.commitIn(o.Root)
		if err != nil {
			return Pin{}, err
		}
		if pin.Commit != "" && commit != pin.Commit {
			return Pin{}, fmt.Errorf("%s names the commit %s and %s names %s: one release is one commit, and the release sequence writes both", pins[0].file, pin.Commit, p.file, commit)
		}
		pin.Commit = commit
	}
	if !c.inGit() {
		return pin, nil
	}
	// Every release tag with its commit. An annotated tag's commit is what
	// it points at; a tag fetched without its object is the commit itself.
	refs, err := c.command("git", "for-each-ref", "--format=%(refname:short) %(objectname) %(*objectname)", "refs/tags/v*")
	if err != nil {
		return Pin{}, errors.New("git for-each-ref failed: the release tags cannot be read")
	}
	releases := map[string]string{}
	var at []string
	for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		commit := fields[len(fields)-1]
		releases[fields[0]] = commit
		if commit == pin.Commit {
			at = append(at, fields[0])
		}
	}
	// The release the tree itself pins. A clone that has no tag for it has
	// not fetched the tags the tree goes with (a fork whose branch was
	// synced, a clone of one branch): which release the commit is cannot be
	// read there, and CI's full clone is where it is.
	version, _ := c.read("plugin/bin/VERSION")
	current := "v" + firstLine(version)
	if len(at) == 0 && releases[current] == "" {
		return pin, nil
	}
	if len(at) == 0 {
		return Pin{}, fmt.Errorf("%s and %s name the commit %s, which is the commit of no release tag (v*): the pin is always a release's commit, and the release sequence writes it", pins[0].file, pins[1].file, pin.Commit)
	}
	sort.Strings(at)
	pin.Release = at[0]
	for _, p := range pins {
		if _, err := c.spawn(io.Discard, "git", "cat-file", "-e", pin.Commit+":"+p.manifest); err != nil {
			return Pin{}, fmt.Errorf("the release %s (%s) has no %s: %s would give its harness no package", pin.Release, pin.Commit, p.manifest, p.file)
		}
	}
	// A pre-release moves no pin (scripts/release-pin.sh), so it is never
	// what the packages are behind.
	if releases[current] != "" && releases[current] != pin.Commit && !strings.Contains(current, "-") {
		pin.Ahead = current
	}
	return pin, nil
}

// MovePin writes the commit into both marketplace files and reads the pin
// the tree then has. A commit that breaks the pin's rules is refused with
// both files as they were.
func MovePin(o Options, commit string) (Pin, error) {
	before := make([][]byte, len(pins))
	for i, p := range pins {
		data, err := os.ReadFile(filepath.Join(o.Root, filepath.FromSlash(p.file)))
		if err != nil {
			return Pin{}, fmt.Errorf("missing %s", p.file)
		}
		before[i] = data
	}
	if err := WritePin(o.Root, commit); err != nil {
		return Pin{}, err
	}
	pin, err := ReadPin(o)
	if err != nil {
		for i, p := range pins {
			//nolint:gosec // G306: a tracked file of the repository, as git checks it out
			if werr := os.WriteFile(filepath.Join(o.Root, filepath.FromSlash(p.file)), before[i], 0o644); werr != nil {
				return Pin{}, fmt.Errorf("%w; and %s could not be put back: %w", err, p.file, werr)
			}
		}
		return Pin{}, err
	}
	return pin, nil
}

// WritePin sets the commit of both marketplace files and changes nothing
// else in them. What the commit has to be is ReadPin's to say, on the tree
// it leaves: MovePin does both, and is the release sequence's
// (scripts/release-pin.sh).
func WritePin(root, commit string) error {
	if !commitRE.MatchString(commit) {
		return fmt.Errorf("not a full commit sha (40 lowercase hexadecimal characters): %q", commit)
	}
	texts := make([]string, len(pins))
	for i, p := range pins {
		if _, err := p.commitIn(root); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.file)))
		if err != nil {
			return err
		}
		if n := len(pinLineRE.FindAllIndex(data, -1)); n != 1 {
			return fmt.Errorf(`%s has %d lines that are a "sha" alone, want exactly 1: the commit is written on a line of its own`, p.file, n)
		}
		texts[i] = pinLineRE.ReplaceAllString(string(data), "${1}"+commit+"${2}")
	}
	for i, p := range pins {
		//nolint:gosec // G306: a tracked file of the repository, as git checks it out
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p.file)), []byte(texts[i]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pin holds the two marketplace files to their rules and says where they
// stand: the part of the Codex CLI and the Grok Build check that is about
// both. A release whose pin has not moved is said and is no failure: until
// its last step runs, the members are on the release before.
func (c *checker) pin() {
	pin, err := ReadPin(c.o)
	if err != nil {
		c.die("%s", err)
	}
	if pin.Ahead != "" {
		c.say("behind: " + pin.String())
		return
	}
	c.ok("%s", pin)
}
