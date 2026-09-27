package notify_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/notify"
)

// TestSound pins the player per platform (card 35): a fixed argv that
// names the player found on PATH, a fixed volume and the system's own
// sound file — or nil and one fixed reason, in the order a member would
// fix things: install the player, then the file.
func TestSound(t *testing.T) {
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
		"darwin, no afplay":   {"darwin", missing, yes, nil, notify.WhyNoPlayerDarwin},
		"darwin, no file":     {"darwin", found, no, nil, notify.WhyNoFile},
		"linux":               {"linux", found, yes, []string{"/opt/bin/paplay", "--volume=16384", "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"}, ""},
		"linux, no paplay":    {"linux", missing, yes, nil, notify.WhyNoPlayerLinux},
		"linux, no file":      {"linux", found, no, nil, notify.WhyNoFile},
		"another system":      {"plan9", found, yes, nil, notify.WhyUnknownOS},
		"windows is not ours": {"windows", found, yes, nil, notify.WhyUnknownOS},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			argv, why := notify.Sound(tc.goos, tc.lookPath, tc.exists)
			if !slices.Equal(argv, tc.argv) || why != tc.why {
				t.Fatalf("Sound = %q, %q; want %q, %q", argv, why, tc.argv, tc.why)
			}
		})
	}
}

// TestBanner pins the notifier per platform (card 36): terminal-notifier
// before osascript on macOS, notify-send on Linux, one fixed reason
// otherwise.
func TestBanner(t *testing.T) {
	t.Parallel()
	only := func(have string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			if name == have {
				return "/opt/bin/" + name, true
			}
			return "", false
		}
	}
	all := func(name string) (string, bool) { return "/opt/bin/" + name, true }
	none := func(string) (string, bool) { return "", false }
	cases := map[string]struct {
		goos     string
		lookPath func(string) (string, bool)
		argv     []string
		why      string
	}{
		"darwin with both":            {"darwin", all, []string{"/opt/bin/terminal-notifier"}, ""},
		"darwin with osascript alone": {"darwin", only("osascript"), []string{"/opt/bin/osascript"}, ""},
		"darwin with neither":         {"darwin", none, nil, notify.WhyNoNotifierDarwin},
		"linux":                       {"linux", only("notify-send"), []string{"/opt/bin/notify-send"}, ""},
		"linux without notify-send":   {"linux", only("osascript"), nil, notify.WhyNoNotifierLinux},
		"another system":              {"plan9", all, nil, notify.WhyUnknownOS},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			argv, why := notify.Banner(tc.goos, tc.lookPath)
			if !slices.Equal(argv, tc.argv) || why != tc.why {
				t.Fatalf("Banner = %q, %q; want %q, %q", argv, why, tc.argv, tc.why)
			}
		})
	}
}

// TestBannerArgv pins each notifier's argument list (card 36): the title
// and the text are arguments — osascript's script is fixed -e statements
// that read them from argv, so a name full of quotes stays data;
// notify-send's body has its markup escaped; terminal-notifier gets a
// group per session. The count changes the text, and an empty name reads
// "this session".
func TestBannerArgv(t *testing.T) {
	t.Parallel()
	const hostile = `x" & (do shell script "id") & "<b>`
	cases := map[string]struct {
		base  []string
		name  string
		count int
		want  []string
	}{
		"terminal-notifier": {[]string{"/opt/bin/terminal-notifier"}, "brigade-8d", 1,
			[]string{"/opt/bin/terminal-notifier", "-title", "Brigade", "-message", "A message arrived for brigade-8d.", "-group", "brigade-sess-1"}},
		"osascript, a hostile name stays an argument": {[]string{"/usr/bin/osascript"}, hostile, 3,
			[]string{"/usr/bin/osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "Brigade", "3 messages arrived for " + hostile + "."}},
		"notify-send escapes markup": {[]string{"/usr/bin/notify-send"}, "a<b>&c", 2,
			[]string{"/usr/bin/notify-send", "--app-name=Brigade", "Brigade", "2 messages arrived for a&lt;b&gt;&amp;c."}},
		"notify-send doubles backslashes before it decodes them": {[]string{"/usr/bin/notify-send"}, `x\074b\076\n`, 1,
			[]string{"/usr/bin/notify-send", "--app-name=Brigade", "Brigade", `A message arrived for x\\074b\\076\\n.`}},
		"an unknown notifier gets notify-send's shape": {[]string{"/nonexistent/brigade-test-notifier"}, "", 1,
			[]string{"/nonexistent/brigade-test-notifier", "--app-name=Brigade", "Brigade", "A message arrived for this session."}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := notify.BannerArgv(tc.base, "sess-1", tc.name, tc.count)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("BannerArgv =\n%q\nwant\n%q", got, tc.want)
			}
			if len(tc.base) != 1 {
				t.Fatalf("the base was changed: %q", tc.base)
			}
		})
	}
	for _, a := range notify.BannerArgv([]string{"/usr/bin/osascript"}, "s", hostile, 1) {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "-e") {
			t.Errorf("an osascript argument %q looks like an option", a)
		}
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
	if p, ok := notify.LookPath(pathVar, "player"); !ok || p != inSecond {
		t.Errorf("player = %q, %v; want %q, true", p, ok, inSecond)
	}
	inFirst := write(first, "player", 0o700)
	if p, ok := notify.LookPath(pathVar, "player"); !ok || p != inFirst {
		t.Errorf("player after a first-dir copy = %q, %v; want %q, true (first hit wins)", p, ok, inFirst)
	}
	for _, name := range []string{"plain", "dirname", "absent"} {
		if p, ok := notify.LookPath(pathVar, name); ok {
			t.Errorf("%s = %q, found; want not found", name, p)
		}
	}
	for _, rel := range []string{"", ".", "sub/../.", "player", "./" + second} {
		if p, ok := notify.LookPath(rel, "player"); ok {
			t.Errorf("the relative PATH entry %q found %q; want it skipped", rel, p)
		}
	}
}

// TestOsascriptScriptCompiles (macOS only): the fixed -e statements
// BannerArgv gives osascript are valid AppleScript, checked with
// osacompile, so a banner is never lost to a script that does not parse.
func TestOsascriptScriptCompiles(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("osacompile is macOS's")
	}
	osacompile, err := exec.LookPath("osacompile")
	if err != nil {
		t.Skip("no osacompile on PATH")
	}
	argv := notify.BannerArgv([]string{"/usr/bin/osascript"}, "s", "name", 1)
	var args []string
	for i := 1; i+1 < len(argv); i += 2 {
		if argv[i] != "-e" {
			break
		}
		args = append(args, "-e", argv[i+1])
	}
	if len(args) != 6 {
		t.Fatalf("expected three -e statements, got %q", argv)
	}
	out := filepath.Join(t.TempDir(), "banner.scpt")
	cmd := exec.CommandContext(t.Context(), osacompile, append(args, "-o", out)...) //nolint:gosec // G204: osacompile from LookPath over Brigade's own fixed statements
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("osacompile: %v\n%s", err, b)
	}
}

func TestExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "sound.aiff")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !notify.Exists(file) {
		t.Error("a regular file does not exist")
	}
	if notify.Exists(dir) {
		t.Error("a directory exists as a file")
	}
	if notify.Exists(filepath.Join(dir, "absent")) {
		t.Error("an absent path exists")
	}
}
