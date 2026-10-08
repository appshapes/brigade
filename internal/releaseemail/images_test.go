package releaseemail

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// pngBytes is a flat PNG of the given size; noisy is one that does not compress, for the size bound.
func pngBytes(t *testing.T, w, h int, noisy bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(1)) //nolint:gosec // G404: test pixels, not a secret
	for y := range h {
		for x := range w {
			c := color.RGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0xff}
			if noisy {
				c = color.RGBA{R: uint8(rnd.Intn(256)), G: uint8(rnd.Intn(256)), B: uint8(rnd.Intn(256)), A: 0xff} //nolint:gosec // G115: Intn(256) fits
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const goodChangelog = `# Changelog

## [Unreleased]

## [0.28.0] — 2026-10-12

### Added

- **A summary line on a mailed message** (card 70). A ` + "`summary:`" + ` line may follow the ` + "`to:`" + ` line.
  ![The gateway's answer to a sessions mail, in a mail client](docs/email/0.28.0-mail-summary.png)

## [0.27.0] — 2026-10-06

### Added

- **Something earlier** (card 61). Words.
`

// imagesTree lays out a checkout with the given CHANGELOG and pictures.
func imagesTree(t *testing.T, changelog string, files map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	if changelog != "" {
		if err := os.WriteFile(filepath.Join(root, "CHANGELOG.md"), []byte(changelog), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "docs", "email"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, "docs", "email", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCheckImagesPassesAPictureMadeByTheRules(t *testing.T) {
	t.Parallel()
	root := imagesTree(t, goodChangelog, map[string][]byte{
		"0.28.0-mail-summary.png": pngBytes(t, ImageWidth, 300, false),
		"README.md":               []byte("# the rules\n"),
	})
	if fs := CheckImages(root); len(fs) != 0 {
		t.Fatalf("findings on a tree made by the rules:\n%s", fs)
	}
	// No pictures and no picture lines is also clean: today's tree.
	if fs := CheckImages(imagesTree(t, "# Changelog\n\n## [0.1.0]\n\n- Words.\n", nil)); len(fs) != 0 {
		t.Fatalf("findings on a tree without pictures:\n%s", fs)
	}
	if fs := CheckImages(t.TempDir()); len(fs) != 0 {
		t.Fatalf("findings on an empty root:\n%s", fs)
	}
}

func TestCheckImagesRefusals(t *testing.T) {
	t.Parallel()
	good := pngBytes(t, ImageWidth, 300, false)
	for _, tc := range []struct {
		name      string
		changelog string
		files     map[string][]byte
		want      string
	}{
		{"a name without a version", goodChangelog, map[string][]byte{"mail-summary.png": good}, "docs/email/mail-summary.png: the name is not <version>-<slug>.png"},
		{"a name with capitals", goodChangelog, map[string][]byte{"0.28.0-Mail.png": good}, "the name is not <version>-<slug>.png"},
		{"a jpeg", goodChangelog, map[string][]byte{"0.28.0-mail-summary.jpg": good}, "the name is not <version>-<slug>.png"},
		{"not a PNG", goodChangelog, map[string][]byte{"0.28.0-mail-summary.png": []byte("GIF89a not a png at all, long enough to read")}, "docs/email/0.28.0-mail-summary.png: not a PNG"},
		{"too narrow", goodChangelog, map[string][]byte{"0.28.0-mail-summary.png": pngBytes(t, 520, 300, false)}, "520 px wide; every picture is 1040"},
		{"too short", goodChangelog, map[string][]byte{"0.28.0-mail-summary.png": pngBytes(t, ImageWidth, 199, false)}, "199 px tall; between 200 and 1040"},
		{"too tall", goodChangelog, map[string][]byte{"0.28.0-mail-summary.png": pngBytes(t, ImageWidth, 1041, false)}, "1041 px tall; between 200 and 1040"},
		{"too big", goodChangelog, map[string][]byte{"0.28.0-mail-summary.png": pngBytes(t, ImageWidth, 300, true)}, "bytes; at most 307200"},
		{"not named in the changelog", goodChangelog, map[string][]byte{"0.28.0-roster.png": good}, "docs/email/0.28.0-roster.png: not named by a picture line in the CHANGELOG's [0.28.0] section"},
		{"no section for its version", goodChangelog, map[string][]byte{"0.29.0-roster.png": good}, "docs/email/0.29.0-roster.png: CHANGELOG.md has no [0.29.0] section"},
		{"no changelog at all", "", map[string][]byte{"0.28.0-mail-summary.png": good}, "there is no CHANGELOG.md to name it"},
		{"a picture line to a missing file", goodChangelog, nil, "line 10: docs/email/0.28.0-mail-summary.png does not exist in the checkout"},
		{"a picture line without alt", strings.Replace(goodChangelog, "![The gateway's answer to a sessions mail, in a mail client]", "![ ]", 1), map[string][]byte{"0.28.0-mail-summary.png": good}, "line 10: docs/email/0.28.0-mail-summary.png: the picture line has no alt text"},
		{"a picture line in the wrong section", strings.Replace(goodChangelog, "## [0.28.0] — 2026-10-12", "## [0.27.1] — 2026-10-12", 1), map[string][]byte{"0.28.0-mail-summary.png": good}, "line 10: docs/email/0.28.0-mail-summary.png: named in the [0.27.1] section, but its name says 0.28.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := CheckImages(imagesTree(t, tc.changelog, tc.files))
			if len(fs) == 0 {
				t.Fatalf("no finding")
			}
			if !strings.Contains(fs.Error(), tc.want) {
				t.Fatalf("the findings lack %q:\n%s", tc.want, fs)
			}
		})
	}
}

// The repository itself keeps the rules: this is the check `make test` runs on every push.
func TestRepositoryPicturesKeepTheRules(t *testing.T) {
	t.Parallel()
	if fs := CheckImages(testutil.RepoRoot(t)); len(fs) != 0 {
		t.Fatalf("docs/email/ or the CHANGELOG's picture lines break docs/email/README.md's rules:\n%s", fs)
	}
}

func TestRunImages(t *testing.T) {
	t.Parallel()
	good := imagesTree(t, goodChangelog, map[string][]byte{"0.28.0-mail-summary.png": pngBytes(t, ImageWidth, 300, false)})
	var stderr bytes.Buffer
	if code := Run([]string{"-images", "-root", good}, &stderr); code != 0 || stderr.Len() != 0 {
		t.Errorf("-images on a good tree: exit %d\n%s", code, stderr.String())
	}
	bad := imagesTree(t, goodChangelog, nil)
	stderr.Reset()
	if code := Run([]string{"-images", "-root", bad}, &stderr); code != 1 || !strings.Contains(stderr.String(), "brigade-release-email: line 10: docs/email/0.28.0-mail-summary.png does not exist") {
		t.Errorf("-images on a bad tree: exit %d\n%s", code, stderr.String())
	}
}

func TestOnePictureUnderOneHeading(t *testing.T) {
	t.Parallel()
	root := imagesTree(t, goodChangelog, map[string][]byte{"0.28.0-mail-summary.png": pngBytes(t, ImageWidth, 300, false), "0.28.0-second.png": pngBytes(t, ImageWidth, 300, false)})
	one := strings.Replace(approvedDraft, "### People on Slack", "![One](docs/email/0.28.0-mail-summary.png)\n\n### People on Slack", 1)
	if err := Check(one, root); err != nil {
		t.Fatalf("one picture under a heading is refused: %v", err)
	}
	two := strings.Replace(approvedDraft, "### People on Slack", "![One](docs/email/0.28.0-mail-summary.png)\n\n![Two](docs/email/0.28.0-second.png)\n\n### People on Slack", 1)
	if err := Check(two, root); err == nil || !strings.Contains(err.Error(), "a second picture under one heading") {
		t.Fatalf("two pictures under a heading passed: %v", err)
	}
}
