//go:build windows

package adapterkit

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// renameRetryFor bounds how long a rename waits out another process's
// open handle on the destination.
const renameRetryFor = 2 * time.Second

// renameInto is MoveFileEx with replace. Windows refuses to replace a file
// another process holds open without FILE_SHARE_DELETE, which Go's opens
// do not grant, so a strict reader in a hook or the watcher makes the
// rename fail for the few milliseconds its handle lives; the rename is
// retried through that window and reports the last error after it.
func renameInto(tmp, path string) error {
	deadline := time.Now().Add(renameRetryFor)
	for {
		err := os.Rename(tmp, path)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
