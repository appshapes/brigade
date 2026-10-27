// Advisory file locking: flock(2) on macOS and Linux (flock_unix.go),
// LockFileEx on Windows (flock_windows.go, card 133). This file is the
// shared retry loop; the three primitives it calls are the platform's.

package adapterkit

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// DefaultLockTimeout is the 10 s bound of 5.1: a holder that outlives it
// makes the waiter fail with `unavailable` (exit 9) instead of hanging a
// hook or a send forever. E0-6 proved the bound live (a 13 s holder
// produced exactly this failure), so it is load-bearing, not decoration.
const DefaultLockTimeout = 10 * time.Second

// lockRetryInterval is how often a contended LockFile retries LOCK_NB.
// E0-6 measured the plan's original 100 ms poll rounding EVERY contended
// acquisition up to a whole poll quantum (min 100.2 ms, median 101.1 ms,
// max 303.1 ms, against a lock held for only 36 ms) and named the fix:
// poll at 5–10 ms, or block on a goroutine. This is that fix; do not raise
// it back.
const lockRetryInterval = 5 * time.Millisecond

// SidecarPath names the lock sidecar for a file that WriteAtomic replaces:
// the lock must live on a file that is never renamed over, because a lock
// taken on the old inode would not protect the new one (5.1).
func SidecarPath(path string) string {
	return path + ".lock"
}

// A FileLock is a held exclusive advisory flock. The zero value holds
// nothing; Unlock on it is a no-op.
type FileLock struct {
	f *os.File
}

// LockFile takes an exclusive advisory flock(2) on path — normally a
// SidecarPath — creating it 0600 when absent. It retries a non-blocking
// attempt every 5 ms until timeout has elapsed (at least one attempt is
// always made), then fails with `unavailable`. The lock is released by
// Unlock, or by the process dying — flock evaporates with the last open
// descriptor, which is the property that makes a crashed holder harmless.
func LockFile(path string, timeout time.Duration) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("adapterkit: open lock file: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := tryLock(f)
		if err == nil {
			return &FileLock{f: f}, nil
		}
		if !lockWouldBlock(err) {
			_ = f.Close()
			return nil, fmt.Errorf("adapterkit: flock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, &protocol.Error{
				Code:    protocol.CodeUnavailable,
				Message: "timed out waiting for the file lock; another process has held it for over " + timeout.String(),
				Details: map[string]string{
					"path":       path,
					"reason":     "lock_timeout",
					"timeout_ms": strconv.FormatInt(timeout.Milliseconds(), 10),
				},
			}
		}
		time.Sleep(lockRetryInterval)
	}
}

// Unlock releases the lock and closes the sidecar descriptor. It is safe
// to call more than once; later calls are no-ops.
func (l *FileLock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	ferr := unlock(f)
	cerr := f.Close()
	if ferr != nil {
		return fmt.Errorf("adapterkit: funlock: %w", ferr)
	}
	return cerr
}

// TryLockFile takes an exclusive, non-blocking lock on an already open
// file, for a caller that opened it with its own flags and checks (the
// Grok hook lock). LockWouldBlock tells a contended attempt from a
// failure; the lock is released by closing the file.
func TryLockFile(f *os.File) error { return tryLock(f) }

// LockWouldBlock reports whether a TryLockFile error means the lock is
// held by someone else.
func LockWouldBlock(err error) bool { return lockWouldBlock(err) }
