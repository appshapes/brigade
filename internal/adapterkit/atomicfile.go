package adapterkit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/protocol"
)

// MkdirPrivate creates dir and any missing parents with mode 0700, the
// only directory mode Brigade state and profile trees use (3.2; gosec
// G301 is configured to the same value). Like os.MkdirAll it leaves the
// modes of already-existing directories alone.
func MkdirPrivate(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// WriteAtomic writes data to path with mode 0600, atomically: a 0600
// temporary file in path's own directory, write, fsync, rename over path,
// fsync the directory (5.1). Two concurrent writers therefore leave ONE
// file carrying one writer's complete content — the later rename wins —
// and a reader can never observe a truncated or interleaved file. A crash
// leaves either the old content or the new, never a partial write.
//
// The temporary file is created by os.CreateTemp (0600 before any byte is
// written) and chmodded to exactly 0600 so an unusual umask cannot narrow
// the mode; a pre-existing world-readable file at path is REPLACED by the
// 0600 result, because the rename swaps the inode.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("adapterkit: create temp for atomic write: %w", err)
	}
	tmp := f.Name()
	fail := func(step string, err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("adapterkit: atomic write %s: %w", step, err)
	}
	if err := f.Chmod(0o600); err != nil {
		return fail("chmod", err)
	}
	if _, err := f.Write(data); err != nil {
		return fail("write", err)
	}
	if err := f.Sync(); err != nil {
		return fail("fsync", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("adapterkit: atomic write close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("adapterkit: atomic write rename: %w", err)
	}
	// fsync the directory so the rename itself is durable (5.1). The file
	// is already in place; a failure here is reported because the write's
	// durability claim would otherwise be silently weaker than documented.
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("adapterkit: atomic write open dir: %w", err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return fmt.Errorf("adapterkit: atomic write fsync dir: %w", serr)
	}
	if cerr != nil {
		return fmt.Errorf("adapterkit: atomic write close dir: %w", cerr)
	}
	return nil
}

// ReadStrict reads a file that must be private: a regular file whose mode
// grants nothing to group or other. A file that fails the mode check is
// REFUSED with the `config` code (exit 11) and its content is never read —
// a group- or world-readable credential is treated as already leaked
// (U-10, E0-1), and reading it anyway would let a misconfigured install
// keep working silently.
//
// A missing file is returned as the underlying *fs.PathError (so
// errors.Is(err, fs.ErrNotExist) holds): whether "missing" means `config`
// (profile.json, 4.6) or `unauthenticated` (session.json, 5.1) is the
// caller's mapping, not this helper's.
func ReadStrict(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "expected a regular file",
			Details: map[string]string{"path": path, "reason": "not_regular"},
		}
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "file mode grants group or other access; it must be 0600 (chmod 600 it, and check what else read it)",
			Details: map[string]string{"path": path, "reason": "insecure_mode"},
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("adapterkit: read %s: %w", path, err)
	}
	return data, nil
}
