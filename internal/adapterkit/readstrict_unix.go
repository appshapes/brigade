// The strict reader's open and owner checks are unix facts — O_NOFOLLOW,
// O_NONBLOCK and the owner uid of an fstat — that exist on the two
// supported platforms only (D33): a third OS fails to build rather than
// trusting a private file it cannot check.

//go:build darwin || linux

package adapterkit

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// openStrict opens path read-only without following a symlink at the last
// component (a symlinked private file is refused rather than resolved:
// its privacy is judged on the file itself) and without blocking on a
// FIFO planted at the path (O_NONBLOCK makes the open return at once; the
// fstat that follows refuses it as not_regular). Neither flag has any
// effect on a regular file.
func openStrict(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// isSymlinkRefusal reports whether an openStrict error means "that was a
// symlink": O_NOFOLLOW answers ELOOP on both platforms.
func isSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}

// fileOwnerUID returns the owner uid recorded in fi. ok is false when fi
// carries no unix stat, which the caller treats as "cannot prove
// ownership" and refuses.
func fileOwnerUID(fi fs.FileInfo) (uid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return int(st.Uid), true
}
