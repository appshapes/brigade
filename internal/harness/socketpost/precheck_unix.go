//go:build darwin || linux

package socketpost

import (
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"
)

// MaxSocketPath is the longest socket path the platform's sockaddr_un
// can carry, terminating NUL excluded: 103 bytes on darwin, 107 on linux.
// It is derived from the kernel structure the standard library dials
// with, so it is exactly the length Go's own dial would refuse with
// EINVAL; a longer path is refused here with a clear reason instead
// (6.7: "reported as not injected with a clear log line, never a crash").
const MaxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// socketMode is the only permission mode a session's inbox socket has
// (A.2: mode 0600 in a 0700 directory).
const socketMode fs.FileMode = 0o600

// precheck refuses everything that is not this user's 0600 socket at an
// absolute path short enough to dial. stat is os.Lstat unless the caller
// injected one, and it is called at most once. A missing path is
// KindSocketGone (re-read the registry); every other refusal is
// KindPrecheck.
func precheck(path string, stat func(string) (fs.FileInfo, error), uid int) *Error {
	switch {
	case path == "":
		return newError(KindPrecheck, ReasonEmpty, nil)
	case !filepath.IsAbs(path):
		return newError(KindPrecheck, ReasonRelative, nil)
	case len(path) > MaxSocketPath:
		return newError(KindPrecheck, ReasonPathTooLong, nil)
	}
	info, err := stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return newError(KindSocketGone, ReasonMissing, err)
		}
		return newError(KindPrecheck, ReasonStat, err)
	}
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return newError(KindPrecheck, ReasonSymlink, nil)
	case mode&fs.ModeSocket == 0:
		return newError(KindPrecheck, ReasonNotSocket, nil)
	case mode.Perm() != socketMode:
		return newError(KindPrecheck, ReasonMode, nil)
	}
	owner, ok := ownerUID(info)
	switch {
	case !ok:
		return newError(KindPrecheck, ReasonOwnerUnknown, nil)
	case owner != uid:
		return newError(KindPrecheck, ReasonForeignUID, nil)
	}
	return nil
}

// ownerUID reads the owning uid out of a FileInfo's platform data. Only
// the *syscall.Stat_t that os.Lstat produces on darwin and linux is
// understood; anything else fails closed as unknown.
func ownerUID(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return int(st.Uid), true
}
