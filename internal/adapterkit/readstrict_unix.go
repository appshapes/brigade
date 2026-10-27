// The strict reader's symlink-refusing open is a unix fact — O_NOFOLLOW
// and O_NONBLOCK — on macOS and Linux (D33); Windows refuses a reparse
// point the same way through FILE_FLAG_OPEN_REPARSE_POINT
// (readstrict_windows.go, card 133). The owner check lives in
// private_unix.go.

//go:build darwin || linux

package adapterkit

import (
	"errors"
	"os"
	"syscall"
)

// OpenNoFollow opens path with flag and perm without following a symlink
// at the last component (O_NOFOLLOW: a symlinked private file is refused
// rather than resolved, its privacy being judged on the file itself) and
// without blocking on a FIFO planted at the path (O_NONBLOCK makes the
// open return at once; the fstat that follows refuses it as not regular).
// Neither flag has any effect on a regular file. IsSymlinkRefusal tells
// the symlink refusal from any other open error.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
}

// IsSymlinkRefusal reports whether an OpenNoFollow error means "that was
// a symlink": ELOOP is the POSIX answer, EMLINK what some filesystems
// give for O_NOFOLLOW on a link.
func IsSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}

// openStrict opens path read-only without following a symlink at the last
// component (a symlinked private file is refused rather than resolved:
// its privacy is judged on the file itself) and without blocking on a
// FIFO planted at the path (O_NONBLOCK makes the open return at once; the
// fstat that follows refuses it as not_regular). Neither flag has any
// effect on a regular file.
func openStrict(path string) (*os.File, error) {
	return OpenNoFollow(path, os.O_RDONLY, 0)
}

// isSymlinkRefusal reports whether an openStrict error means "that was a
// symlink": O_NOFOLLOW answers ELOOP on both platforms.
func isSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}
