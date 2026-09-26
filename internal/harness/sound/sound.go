// Package sound resolves the one fixed program a session's watcher runs
// when a teammate's message arrives and the `message_sound` option is on
// (card 35): a quiet system sound through the platform's own player, with
// an argument list nothing from the message ever reaches. The hook uses
// it to say at session start when the machine cannot play; the watcher
// uses it to play.
package sound

import (
	"os"
	"path/filepath"
	"time"
)

// MinInterval is the least time between two sounds for one session: a
// burst of messages is one sound, not a nuisance.
const MinInterval = 30 * time.Second

// Timeout bounds one play; a player that has not returned by then is
// ended (SIGTERM, then SIGKILL after the spawn seam's wait delay).
const Timeout = 10 * time.Second

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

// The reasons Resolve gives for a machine that cannot play: fixed text,
// safe on a session-start line, naming what to install.
const (
	WhyNoPlayerDarwin = "afplay is not on PATH"
	WhyNoPlayerLinux  = "paplay is not on PATH"
	WhyNoFile         = "the system sound file is missing"
	WhyUnknownOS      = "no player is known for this system"
)

// Resolve is the fixed argv of the player this machine has, or nil and
// the reason it has none. goos is runtime.GOOS; lookPath searches PATH
// for an executable name (the caller's own search, so the hook's and the
// watcher's seams both serve); exists says whether a file is present.
// Nothing here runs a program.
func Resolve(goos string, lookPath func(name string) (string, bool), exists func(path string) bool) ([]string, string) {
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

// LookPath searches pathVar (a PATH value) for an executable regular file
// named name and returns the first hit. A relative entry — "" and "."
// included — is skipped, as foldersync skips it: a player is never
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
