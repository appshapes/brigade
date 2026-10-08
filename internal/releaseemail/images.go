package releaseemail

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The rules of docs/email/, the pictures a release may show under a change in the email. They are written out
// in docs/email/README.md; this file is the half of them a program can check. The lint cannot look at a
// picture, so what a picture shows -- no address, no person, no secret -- stays the maker's duty.
const (
	// ImageWidth is the width every picture has, in pixels: twice the 520 px column the email shows it in, so
	// it is crisp on a dense screen. `make email-picture` resamples to it.
	ImageWidth = 1040
	// ImageMinHeight and ImageMaxHeight bound the height: a strip too thin to read, or a column taller than
	// the card is wide, is not a picture of one change.
	ImageMinHeight = 200
	ImageMaxHeight = 1040
	// ImageMaxBytes bounds the file: a reader on a phone fetches every picture of the email.
	ImageMaxBytes = 300 * 1024

	imagesDir = "docs/email"
)

var (
	reImageName        = regexp.MustCompile(`^(\d+\.\d+\.\d+)-([a-z0-9]+(?:-[a-z0-9]+)*)\.png$`)
	reChangelogImage   = regexp.MustCompile(`!\[([^\]]*)\]\((docs/email/[^)\s]+)\)`)
	reChangelogSection = regexp.MustCompile(`^## \[([^\]]+)\]`)
)

// CheckImages reads docs/email/ and CHANGELOG.md of a checkout and returns every finding against the rules:
// a file is `<version>-<slug>.png`, a PNG, ImageWidth wide, between ImageMinHeight and ImageMaxHeight tall,
// at most ImageMaxBytes, and named by a picture line in the CHANGELOG section of its version; every picture
// line in the CHANGELOG has alt text and points at a file that exists, in the section its name says. A line
// number is the CHANGELOG's; a file's finding has none.
func CheckImages(root string) Findings {
	var findings Findings
	fail := func(line int, msg string) { findings = append(findings, Finding{Line: line, Msg: msg}) }

	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fail(0, "CHANGELOG.md: "+err.Error())
	}
	sections, refs := changelogPictures(string(changelog))

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(imagesDir)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fail(0, imagesDir+": "+err.Error())
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "README.md" {
			continue
		}
		rel := imagesDir + "/" + name
		m := reImageName.FindStringSubmatch(name)
		if m == nil {
			fail(0, rel+": the name is not <version>-<slug>.png (digits and dots, a dash, lower-case letters, digits and dashes)")
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			fail(0, rel+": "+err.Error())
			continue
		}
		if len(data) > ImageMaxBytes {
			fail(0, rel+": "+strconv.Itoa(len(data))+" bytes; at most "+strconv.Itoa(ImageMaxBytes))
		}
		w, h, ok := pngSize(data)
		switch {
		case !ok:
			fail(0, rel+": not a PNG")
		case w != ImageWidth:
			fail(0, rel+": "+strconv.Itoa(w)+" px wide; every picture is "+strconv.Itoa(ImageWidth)+" (make email-picture resamples)")
		case h < ImageMinHeight || h > ImageMaxHeight:
			fail(0, rel+": "+strconv.Itoa(h)+" px tall; between "+strconv.Itoa(ImageMinHeight)+" and "+strconv.Itoa(ImageMaxHeight))
		}
		version := m[1]
		section, has := sections[version]
		switch {
		case len(changelog) == 0:
			fail(0, rel+": there is no CHANGELOG.md to name it")
		case !has:
			fail(0, rel+": CHANGELOG.md has no ["+version+"] section")
		case !strings.Contains(section, "]("+rel+")"):
			fail(0, rel+": not named by a picture line in the CHANGELOG's ["+version+"] section")
		}
	}

	for _, r := range refs {
		if strings.TrimSpace(r.alt) == "" {
			fail(r.line, r.path+": the picture line has no alt text; say what the picture shows")
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(r.path))); err != nil {
			fail(r.line, r.path+" does not exist in the checkout")
		}
		if m := reImageName.FindStringSubmatch(strings.TrimPrefix(r.path, imagesDir+"/")); m != nil && m[1] != r.section {
			fail(r.line, r.path+": named in the ["+r.section+"] section, but its name says "+m[1])
		}
	}
	return findings
}

// pictureRef is one `![alt](docs/email/…)` line of the CHANGELOG.
type pictureRef struct {
	line    int
	alt     string
	path    string
	section string
}

// changelogPictures splits the CHANGELOG into its `## [version]` sections and finds every picture line.
func changelogPictures(changelog string) (sections map[string]string, refs []pictureRef) {
	sections = map[string]string{}
	var (
		current string
		text    strings.Builder
	)
	flush := func() {
		if current != "" {
			sections[current] += text.String()
		}
		text.Reset()
	}
	for i, line := range strings.Split(changelog, "\n") {
		if m := reChangelogSection.FindStringSubmatch(line); m != nil {
			flush()
			current = m[1]
			continue
		}
		text.WriteString(line)
		text.WriteByte('\n')
		for _, m := range reChangelogImage.FindAllStringSubmatch(line, -1) {
			refs = append(refs, pictureRef{line: i + 1, alt: m[1], path: m[2], section: current})
		}
	}
	flush()
	return sections, refs
}

// pngSize reads a PNG's width and height from its IHDR chunk.
func pngSize(data []byte) (width, height int, ok bool) {
	const signature = "\x89PNG\r\n\x1a\n"
	if len(data) < 24 || string(data[:8]) != signature || string(data[12:16]) != "IHDR" {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(data[16:20])), int(binary.BigEndian.Uint32(data[20:24])), true
}
