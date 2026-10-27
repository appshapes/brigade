package socketpost

import (
	"io/fs"
	"os"
)

// The pre-checks of plan 6.7 (U-19) run before any connection is opened:
// the socket the watcher posts a frame to must be the one Claude Code
// created for this user's own session, and nothing that merely sits at
// that path. On macOS and Linux that is one Lstat: a 0600 socket of this
// uid at an absolute path short enough to dial (precheck_unix.go). On
// Windows the inbox is a named pipe in the session-local namespace,
// which no other logon can open, authenticated by the token on the
// first line; the pre-check is the pipe name's shape, and a stat would
// itself open a client connection (precheck_windows.go, card 133, E11).

// The reasons a pre-check reports in Error.Reason.
const (
	ReasonEmpty        = "empty"
	ReasonRelative     = "relative"
	ReasonPathTooLong  = "path_too_long"
	ReasonMissing      = "missing"
	ReasonStat         = "stat"
	ReasonSymlink      = "symlink"
	ReasonNotSocket    = "not_socket"
	ReasonNotPipe      = "not_pipe"
	ReasonMode         = "mode"
	ReasonForeignUID   = "foreign_uid"
	ReasonOwnerUnknown = "owner_unknown"
)

// lstat is the default stat: os.Lstat, which does not follow a symlink,
// so a link planted at the socket path shows as ModeSymlink and is
// refused rather than followed.
func lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }
