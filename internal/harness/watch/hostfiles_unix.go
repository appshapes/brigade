//go:build darwin || linux

package watch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// The configured host directory is the trust anchor. Its ancestors may use
// system aliases (e.g. /var on macOS), but the directory itself must not be a
// symlink. refresh pins this opened directory's identity before using it.
func openHostRoot(path string) (*os.File, os.FileInfo) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil
	}
	fi, err := f.Stat()
	if err != nil || !fi.IsDir() {
		_ = f.Close()
		return nil, nil
	}
	return f, fi
}

// Resolve every component relative to an already opened directory, refusing
// symlinks in the open itself rather than checking a path before opening it.
// This applies equally to discovery, cached journals and the title index.
// O_NONBLOCK prevents FIFO leaves from hanging the heartbeat; fstat admits
// only regular file leaves. All temporary directory descriptors are closed.
// No filesystem error or path escapes to a caller that could log it.
func openHostPath(root *os.File, path string, directory bool) (*os.File, os.FileInfo) {
	if !filepath.IsLocal(path) || path != filepath.Clean(path) || path == "." {
		return nil, nil
	}
	parts := strings.Split(path, string(filepath.Separator))
	parent := root
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		isDir := i < len(parts)-1 || directory
		if isDir {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(parent.Fd()), part, flags, 0)
		runtime.KeepAlive(parent)
		if parent != root {
			_ = parent.Close()
		}
		if err != nil {
			return nil, nil
		}
		parent = os.NewFile(uintptr(fd), part)
	}
	fi, err := parent.Stat()
	if err != nil || (directory && !fi.IsDir()) || (!directory && !fi.Mode().IsRegular()) {
		_ = parent.Close()
		return nil, nil
	}
	return parent, fi
}
