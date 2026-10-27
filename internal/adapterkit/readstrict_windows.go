//go:build windows

package adapterkit

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// errReparsePoint is what OpenNoFollow answers for a symbolic link or a
// junction at the path: Windows has no O_NOFOLLOW, so the link is opened
// as itself (FILE_FLAG_OPEN_REPARSE_POINT, which Go passes through the
// high bits of the open flag) and refused on the handle's own attributes.
var errReparsePoint = errors.New("path is a symbolic link or junction")

// OpenNoFollow is the Windows reading of the unix open: the reparse point
// itself is opened, never what it points at, and a handle whose
// attributes say "symbolic link" is closed and refused. There is no FIFO
// to block on at a filesystem path on Windows.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flag|windows.FILE_FLAG_OPEN_REPARSE_POINT, perm) //nolint:gosec // G304: the caller's own path, as on unix
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		_ = f.Close()
		return nil, &fs.PathError{Op: "open", Path: path, Err: errReparsePoint}
	}
	return f, nil
}

// IsSymlinkRefusal reports whether an OpenNoFollow error is the reparse-point refusal.
func IsSymlinkRefusal(err error) bool {
	return errors.Is(err, errReparsePoint)
}

func openStrict(path string) (*os.File, error) {
	return OpenNoFollow(path, os.O_RDONLY, 0)
}

func isSymlinkRefusal(err error) bool {
	return IsSymlinkRefusal(err)
}
