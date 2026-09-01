package adapterkit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// WritePidfile creates path with O_CREATE|O_EXCL, mode 0600, and writes
// content to it. An existing file — a live or stale predecessor — fails
// with an error for which errors.Is(err, fs.ErrExist) holds, and the
// caller decides whether to run its liveness check and replace flow
// (P3-5); this helper never deletes someone else's file. The content is
// fsynced before the create is reported successful.
func WritePidfile(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("adapterkit: create pidfile: %w", err)
	}
	fail := func(step string, err error) error {
		_ = f.Close()
		// O_EXCL succeeded, so the file is ours and safe to remove.
		_ = os.Remove(path)
		return fmt.Errorf("adapterkit: pidfile %s: %w", step, err)
	}
	if _, err := f.Write(content); err != nil {
		return fail("write", err)
	}
	if err := f.Sync(); err != nil {
		return fail("fsync", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("adapterkit: pidfile close: %w", err)
	}
	return nil
}

// RemovePidfile unlinks path ONLY when its current content is
// byte-for-byte equal to content, and reports whether it removed the
// file. Compare-then-delete is REQUIRED, not a courtesy (E0-5): in the
// replace flow a superseded process's cleanup would otherwise delete the
// pidfile that by then belongs to its replacement. A missing file and a
// content mismatch both return (false, nil) — in either case the file is
// not this process's to remove, and the caller has nothing to act on.
func RemovePidfile(path string, content []byte) (bool, error) {
	current, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("adapterkit: read pidfile: %w", err)
	}
	if !bytes.Equal(current, content) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("adapterkit: remove pidfile: %w", err)
	}
	return true, nil
}
