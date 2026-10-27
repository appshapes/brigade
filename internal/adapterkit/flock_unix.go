// Advisory file locking uses flock(2) on macOS and Linux (D33); Windows
// locks the same sidecar with LockFileEx (flock_windows.go, card 133).

//go:build darwin || linux

package adapterkit

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive, non-blocking flock on f.
func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlock releases the flock on f; the descriptor is closed by the caller.
func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// lockWouldBlock reports whether a tryLock error means "held by someone
// else, try again", as opposed to a real failure.
func lockWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR)
}
