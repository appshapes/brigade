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
	return WriteAtomicMode(path, data, 0o600)
}

// WriteAtomicMode is WriteAtomic with the final mode a parameter. It
// exists for the one public file Brigade ever writes — the 0644
// `.brigade.json` a team's administrator commits to the repository —
// and everything private stays on WriteAtomic's fixed 0600. The mode is
// applied to the temporary file before any byte lands, so no reader ever
// observes the destination path with a mode other than the one asked for.
func WriteAtomicMode(path string, data []byte, mode os.FileMode) error {
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
	if err := f.Chmod(mode); err != nil {
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

// MaxStrictBytes caps what ReadStrict will read: every private file it
// serves — a profile, a credential, the adapter sidecar and registry, a
// seen file, a pidfile — is a few KiB at most, so anything past 1 MiB is
// not one of them and is refused rather than buffered.
const MaxStrictBytes = 1 << 20

// ReadStrict reads a file that must be private: a regular file, opened
// without following a symlink and without blocking on a FIFO, whose mode
// grants nothing to group or other, owned by the current uid, and no
// larger than MaxStrictBytes. Every check runs on the OPEN descriptor, so
// the file cannot be swapped between the check and the read. A file that
// fails a check is REFUSED with the `config` code (exit 11) and its
// content is never read — a group- or world-readable credential is
// treated as already leaked (U-10, E0-1), and reading it anyway would let
// a misconfigured install keep working silently. details.reason names the
// failed check: symlink, not_regular, insecure_mode, foreign_owner or
// too_large.
//
// A missing file is returned as the underlying *fs.PathError (so
// errors.Is(err, fs.ErrNotExist) holds): whether "missing" means `config`
// (profile.json, 4.6) or `unauthenticated` (session.json, 5.1) is the
// caller's mapping, not this helper's.
func ReadStrict(path string) ([]byte, error) {
	f, err := openStrict(path)
	if err != nil {
		if isSymlinkRefusal(err) {
			return nil, strictRefusal(path, "symlink", "expected a regular file, not a symbolic link")
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, strictRefusal(path, "not_regular", "expected a regular file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, strictRefusal(path, "insecure_mode",
			"file mode grants group or other access; it must be 0600 (chmod 600 it, and check what else read it)")
	}
	if uid, ok := fileOwnerUID(fi); !ok || uid != os.Getuid() {
		return nil, strictRefusal(path, "foreign_owner", "file is not owned by the current user; refusing to trust it")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxStrictBytes+1))
	if err != nil {
		return nil, fmt.Errorf("adapterkit: read %s: %w", path, err)
	}
	if len(data) > MaxStrictBytes {
		return nil, strictRefusal(path, "too_large", "file is larger than the 1 MiB a private state file can be")
	}
	return data, nil
}

// strictRefusal is the `config` failure of a ReadStrict check. The
// message is fixed text; reason names the check for callers and tests.
func strictRefusal(path, reason, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"path": path, "reason": reason},
	}
}
