package sound_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/sound"
)

// TestResolve pins the player per platform (card 35): a fixed argv that
// names the player found on PATH, a fixed volume and the system's own
// sound file — or nil and one fixed reason, in the order a member would
// fix things: install the player, then the file.
func TestResolve(t *testing.T) {
	t.Parallel()
	found := func(name string) (string, bool) { return "/opt/bin/" + name, true }
	missing := func(string) (string, bool) { return "", false }
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	cases := map[string]struct {
		goos     string
		lookPath func(string) (string, bool)
		exists   func(string) bool
		argv     []string
		why      string
	}{
		"darwin":              {"darwin", found, yes, []string{"/opt/bin/afplay", "-v", "0.25", "/System/Library/Sounds/Glass.aiff"}, ""},
		"darwin, no afplay":   {"darwin", missing, yes, nil, sound.WhyNoPlayerDarwin},
		"darwin, no file":     {"darwin", found, no, nil, sound.WhyNoFile},
		"linux":               {"linux", found, yes, []string{"/opt/bin/paplay", "--volume=16384", "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"}, ""},
		"linux, no paplay":    {"linux", missing, yes, nil, sound.WhyNoPlayerLinux},
		"linux, no file":      {"linux", found, no, nil, sound.WhyNoFile},
		"another system":      {"plan9", found, yes, nil, sound.WhyUnknownOS},
		"windows is not ours": {"windows", found, yes, nil, sound.WhyUnknownOS},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			argv, why := sound.Resolve(tc.goos, tc.lookPath, tc.exists)
			if !slices.Equal(argv, tc.argv) || why != tc.why {
				t.Fatalf("Resolve = %q, %q; want %q, %q", argv, why, tc.argv, tc.why)
			}
			for _, a := range argv {
				if strings.ContainsAny(a, " ;|&$`") && !strings.HasPrefix(a, "/") {
					t.Errorf("argv element %q looks shell-shaped", a)
				}
			}
		})
	}
}

// TestLookPath: an executable regular file on an absolute PATH entry is
// found, first hit first; a file without the execute bit, a directory
// and an absent name are not; a relative entry — "" and "." among them —
// is skipped, never searched.
func TestLookPath(t *testing.T) {
	t.Parallel()
	first := t.TempDir()
	second := t.TempDir()
	write := func(dir, name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(first, "plain", 0o600)
	inSecond := write(second, "player", 0o700)
	if err := os.Mkdir(filepath.Join(first, "dirname"), 0o700); err != nil {
		t.Fatal(err)
	}
	pathVar := first + string(os.PathListSeparator) + second
	if p, ok := sound.LookPath(pathVar, "player"); !ok || p != inSecond {
		t.Errorf("player = %q, %v; want %q, true", p, ok, inSecond)
	}
	inFirst := write(first, "player", 0o700)
	if p, ok := sound.LookPath(pathVar, "player"); !ok || p != inFirst {
		t.Errorf("player after a first-dir copy = %q, %v; want %q, true (first hit wins)", p, ok, inFirst)
	}
	for _, name := range []string{"plain", "dirname", "absent"} {
		if p, ok := sound.LookPath(pathVar, name); ok {
			t.Errorf("%s = %q, found; want not found", name, p)
		}
	}
	for _, rel := range []string{"", ".", "sub/../.", "player", "./" + second} {
		if p, ok := sound.LookPath(rel, "player"); ok {
			t.Errorf("the relative PATH entry %q found %q; want it skipped", rel, p)
		}
	}
}

func TestExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "sound.aiff")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !sound.Exists(file) {
		t.Error("a regular file does not exist")
	}
	if sound.Exists(dir) {
		t.Error("a directory exists as a file")
	}
	if sound.Exists(filepath.Join(dir, "absent")) {
		t.Error("an absent path exists")
	}
}
