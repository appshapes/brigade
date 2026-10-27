//go:build windows

package adapterkit

import "io/fs"

// On Windows the mode bits Go reports carry no permission (0666 for every writable file, 0444 for a
// read-only one) and os.Getuid is -1, so none of the unix checks can run. The ruling (card 133, 2026-10-27)
// trusts the profile directories Windows gives each user a private ACL on (%APPDATA%, %LOCALAPPDATA%,
// %USERPROFILE%), which is where every file these checks guard lives; verifying the DACL itself is a later
// card. A symbolic link or junction is still refused, by OpenNoFollow, before any of these is asked.

func insecureMode(fs.FileInfo) bool { return false }

func foreignOwner(fs.FileInfo) bool { return false }

func worldWritable(fs.FileInfo) bool { return false }

// executable: Windows has no execute bit; a regular file is what a shell runs there (by extension, which
// the PATH searches that call this already spell out or leave to Git Bash).
func executable(fs.FileInfo) bool { return true }
