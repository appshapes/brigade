// Package notify resolves the fixed programs a session's watcher runs when
// a teammate's message arrives and the member asked to be told (cards 35
// and 36): a quiet system sound through the platform's own player
// (`message_sound`), and a desktop notification through the platform's
// own notifier (`message_notification`). Each is an argument list nothing
// from the message ever reaches: the sound's is fixed at resolve time;
// the banner's carries Brigade's own title and text, and the session's
// own name and a count, as arguments — never spliced into script code.
// The hook uses the resolvers to say at session start when the machine
// cannot; the watcher uses them to run.
package notify

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The interval between two sounds, or two banners, for one session — a
// burst of messages is one of each, not a nuisance. DefaultInterval
// applies unless the member's `message_interval` option says otherwise
// (card 38), within IntervalFloor (a burst must not become a flood) and
// IntervalCeiling (an hour: past that the option is a way to switch off).
const (
	DefaultInterval = 30 * time.Second
	IntervalFloor   = 5 * time.Second
	IntervalCeiling = 3600 * time.Second
)

// Timeout bounds one run; a player or notifier that has not returned by
// then is ended (SIGTERM, then SIGKILL after the spawn seam's wait
// delay).
const Timeout = 10 * time.Second

// Title is the banner's title, Brigade's own word.
const Title = "Brigade"

// The player, the sound file and the volume per platform. The volume is
// about a quarter of the alert volume — heard, not startling: afplay's
// -v is a linear gain of the file's own level, paplay's --volume is of
// 65536.
const (
	darwinPlayer = "afplay"
	darwinFile   = "/System/Library/Sounds/Glass.aiff"
	darwinVolume = "0.25"
	linuxPlayer  = "paplay"
	linuxFile    = "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga"
	linuxVolume  = "--volume=16384"
)

// The notifier per platform. On macOS terminal-notifier, when installed,
// gives the banner Brigade's title and one slot per session that a burst
// replaces; osascript, always present, shows a banner attributed to
// Script Editor. On Linux notify-send speaks to the desktop's
// notification daemon over D-Bus, which it finds through XDG_RUNTIME_DIR.
const (
	darwinNotifier = "terminal-notifier"
	darwinFallback = "osascript"
	linuxNotifier  = "notify-send"
)

// The reasons Sound and Banner give for a machine that cannot: fixed
// text, safe on a session-start line, naming what to install.
const (
	WhyNoPlayerDarwin   = "afplay is not on PATH"
	WhyNoPlayerLinux    = "paplay is not on PATH"
	WhyNoFile           = "the system sound file is missing"
	WhyNoNotifierDarwin = "neither terminal-notifier nor osascript is on PATH"
	WhyNoNotifierLinux  = "notify-send is not on PATH"
	WhyUnknownOS        = "no player or notifier is known for this system"
)

// Sound is the fixed argv of the player this machine has, or nil and the
// reason it has none. goos is runtime.GOOS; lookPath searches PATH for an
// executable name (the caller's own search, so the hook's and the
// watcher's seams both serve); exists says whether a file is present.
// Nothing here runs a program.
func Sound(goos string, lookPath func(name string) (string, bool), exists func(path string) bool) ([]string, string) {
	switch goos {
	case "darwin":
		p, ok := lookPath(darwinPlayer)
		if !ok {
			return nil, WhyNoPlayerDarwin
		}
		if !exists(darwinFile) {
			return nil, WhyNoFile
		}
		return []string{p, "-v", darwinVolume, darwinFile}, ""
	case "linux":
		p, ok := lookPath(linuxPlayer)
		if !ok {
			return nil, WhyNoPlayerLinux
		}
		if !exists(linuxFile) {
			return nil, WhyNoFile
		}
		return []string{p, linuxVolume, linuxFile}, ""
	default:
		return nil, WhyUnknownOS
	}
}

// Banner is the notifier this machine has, as a one-element argv that
// BannerArgv completes for each showing, or nil and the reason it has
// none. Nothing here runs a program.
func Banner(goos string, lookPath func(name string) (string, bool)) ([]string, string) {
	switch goos {
	case "darwin":
		if p, ok := lookPath(darwinNotifier); ok {
			return []string{p}, ""
		}
		if p, ok := lookPath(darwinFallback); ok {
			return []string{p}, ""
		}
		return nil, WhyNoNotifierDarwin
	case "linux":
		if p, ok := lookPath(linuxNotifier); ok {
			return []string{p}, ""
		}
		return nil, WhyNoNotifierLinux
	default:
		return nil, WhyUnknownOS
	}
}

// BannerArgv completes a Banner argv for one showing: the notifier's own
// flags, the title, the text for the session's name and the count, and —
// for terminal-notifier — a group per session so a burst replaces the
// previous banner. Every value is an argument. osascript reads its script
// from -e statements and the title and text from its run handler's
// argv, so a name can hold quotes, parentheses or an apostrophe and stay
// data; notify-send takes the summary and the body as arguments, the
// body's markup characters escaped because a daemon may parse it, and
// its backslashes doubled because notify-send decodes backslash escapes
// (g_strcompress) before the daemon sees the text. An
// argv whose program is none of the three is given notify-send's shape:
// a test seam, or a notifier the member put on PATH under another name,
// is treated as the plainest of them.
func BannerArgv(base []string, sessionID, name string, count int) []string {
	text := Text(name, count)
	argv := slices.Clone(base)
	switch filepath.Base(base[0]) {
	case darwinNotifier:
		return append(argv, "-title", Title, "-message", text, "-group", "brigade-"+sessionID)
	case darwinFallback:
		return append(argv,
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run",
			Title, text)
	default:
		return append(argv, "--app-name="+Title, Title, escapeMarkup(text))
	}
}

// Text is the banner's body: Brigade's own words and the session's own
// name — never a byte of the message. count is how many messages arrived
// since the last banner, this one included.
func Text(name string, count int) string {
	who := "this session"
	if name != "" {
		who = name
	}
	if count > 1 {
		return strconv.Itoa(count) + " messages arrived for " + who + "."
	}
	return "A message arrived for " + who + "."
}

var markupEscaper = strings.NewReplacer("\\", "\\\\", "&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeMarkup(s string) string { return markupEscaper.Replace(s) }

// LookPath searches pathVar (a PATH value) for an executable regular file
// named name and returns the first hit. A relative entry — "" and "."
// included — is skipped, as foldersync skips it: a program is never
// started from a path relative to whatever the cwd happens to be, and
// the hook probes from the project directory. The hook's own search for
// the shadowing check of 6.2 keeps a shell's reading, on purpose: there
// the question is what a shell would run.
func LookPath(pathVar, name string) (string, bool) {
	for _, dir := range filepath.SplitList(pathVar) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		return p, true
	}
	return "", false
}

// Exists reports whether path names an existing regular file.
func Exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
