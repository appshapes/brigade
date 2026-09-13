package testutil

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// WriteExecutable writes body to path as a 0700 file that this process, or
// a child of it, will exec, and fails the test if it cannot.
//
// It exists because os.WriteFile followed by exec is not safe in a process
// that forks concurrently (golang/go#22315). os.WriteFile opens the file
// without taking syscall.ForkLock, so a fork in flight on another
// goroutine hands the still-open write fd to its child — O_CLOEXEC does
// not help, the child keeps the fd until its own execve — and an execve of
// the file in that window fails with ETXTBSY ("text file busy"). Nothing
// retries it: os/exec returns the error, adapterkit maps it to
// spawn_error, the conformance launcher to "spawn failed". CI hit it four
// times in eight days on the 4-CPU Linux runner (runs 34232764450,
// 34355720402, 34516308749, 34537042672); a synthetic harness (4 writers,
// 4 forkers, linux 6.12 at 2 CPUs) put it at 697 of 30,000 execs (2.3%).
//
// Holding syscall.ForkLock for reading across open, write and close
// removes the mechanism rather than narrowing it: syscall.forkExec takes
// the lock for writing around the clone, so no fork can begin while the fd
// exists, and a fork already in flight holds the write lock until the
// clone returns — on Linux with CLONE_VFORK, not before the child has
// exec'd. A child can only inherit an fd that exists when it is cloned,
// and under the lock none does. 0 of 66,000 in the same harness.
//
// The file is always a NEW inode (the old one is unlinked first). An fd
// leaked by an earlier unguarded write of the same path refers to the old
// inode, and Linux through 6.19 wakes the vfork parent (exec_mmap) before
// it closes the child's O_CLOEXEC fds (do_close_on_exec), so waiting for
// the lock alone does not wait for that fd: 1 of 96,000 with a lock-only
// barrier, 0 of 54,000 with a fresh inode. That is what lets a testscript
// Setup hook rewrite the files testscript already extracted.
//
// macOS does not enforce ETXTBSY (0 of 400 unguarded), so the lock costs
// nothing there and the helper is the same on both platforms.
func WriteExecutable(tb testing.TB, path string, body []byte) {
	tb.Helper()
	if err := WriteExecutableFile(path, body); err != nil {
		tb.Fatalf("testutil.WriteExecutable: %v", err)
	}
}

// WriteExecutableFile is [WriteExecutable] for a caller without a
// testing.TB: a testscript Setup hook or a TestMain.
func WriteExecutableFile(path string, body []byte) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	//nolint:gosec // G302: an executable fixture; 0700 keeps it owner-only
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
