//go:build windows

package watch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// The Windows reading of hostfiles_unix.go (card 133). Windows has no
// openat, so each component is opened by its path from the already
// opened root's own path, with adapterkit.OpenNoFollow refusing a
// symbolic link or junction at every step; a component opened to be
// checked is closed again, and only the leaf is kept. The hosts whose
// files these open (Muse, Codex, Grok) are not supported on Windows,
// so this is what keeps the watcher building and answers "no host
// file" there, never a hang or a followed link.

func openHostRoot(path string) (*os.File, os.FileInfo) {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
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

func openHostPath(root *os.File, path string, directory bool) (*os.File, os.FileInfo) {
	if !filepath.IsLocal(path) || path != filepath.Clean(path) || path == "." {
		return nil, nil
	}
	parts := strings.Split(path, string(filepath.Separator))
	current := root.Name()
	for i, part := range parts {
		current = filepath.Join(current, part)
		isDir := i < len(parts)-1 || directory
		f, err := adapterkit.OpenNoFollow(current, os.O_RDONLY, 0)
		if err != nil {
			return nil, nil
		}
		fi, err := f.Stat()
		if err != nil || (isDir && !fi.IsDir()) || (!isDir && !fi.Mode().IsRegular()) {
			_ = f.Close()
			return nil, nil
		}
		if i == len(parts)-1 {
			return f, fi
		}
		_ = f.Close()
	}
	return nil, nil
}
