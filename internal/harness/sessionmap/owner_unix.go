// The privacy checks of the strict reader need the file's owner uid and a
// symlink-refusing open, both of which are unix facts (D33: a third OS
// fails to build rather than trusting a map it cannot check).

//go:build darwin || linux

package sessionmap

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a symlink at the
// last component: a symlinked map is refused (ELOOP) rather than resolved,
// because the map's privacy is judged on the file itself. O_NONBLOCK makes
// the open of a FIFO planted at the path return at once instead of
// blocking until a writer appears (the fstat that follows then refuses it
// as not_regular); it has no effect on a regular file.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// isSymlinkRefusal reports whether an openNoFollow error means "that was
// a symlink".
func isSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}

// ownerUID returns the owner uid recorded in fi. ok is false when fi
// carries no unix stat (a FileInfo from another filesystem), which the
// caller treats as "cannot prove ownership" and refuses.
func ownerUID(fi fs.FileInfo) (uid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return int(st.Uid), true
}
