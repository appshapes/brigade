//go:build windows

package adapterkit

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// On Windows the sidecar is locked with LockFileEx: one byte at offset 0,
// exclusive, failing at once when held. Like flock, the lock is bound to
// the open handle and evaporates when the handle closes, so a crashed
// holder is harmless; unlike flock, a second handle in the SAME process
// does not share it, which the callers never rely on.

func tryLock(f *os.File) error {
	var ov windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
}

func unlock(f *os.File) error {
	var ov windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
}

// lockWouldBlock: ERROR_LOCK_VIOLATION is what LOCKFILE_FAIL_IMMEDIATELY
// answers for a range another handle holds; ERROR_IO_PENDING cannot occur
// on a synchronous handle but is treated the same way.
func lockWouldBlock(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}
