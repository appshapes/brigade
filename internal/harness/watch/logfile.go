package watch

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// A rotatingFile is the watcher's log file (6.6, 3.2): 0600 in a 0700
// directory, append mode, rotated ONCE when a write would take it past
// limit bytes — the current file is renamed to <path>.1 (replacing any
// earlier generation) and a fresh one is opened. The hook's own stdout and
// stderr descriptors keep pointing at the renamed inode, which is fine: a
// Go runtime crash still lands in a file that is kept.
type rotatingFile struct {
	path  string
	limit int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

func newRotatingFile(path string, limit int64) *rotatingFile {
	return &rotatingFile{path: path, limit: limit}
}

// open creates the directory chain and opens the file for appending.
func (r *rotatingFile) open() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.openLocked()
}

func (r *rotatingFile) openLocked() error {
	if err := adapterkit.MkdirPrivate(filepath.Dir(r.path)); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600 under BRIGADE_STATE_DIR by design
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f = f
	r.size = st.Size()
	return nil
}

// Write implements io.Writer. A write that would exceed the limit rotates
// first, so one generation is kept. Failures are swallowed: a log line is
// never worth failing the watcher, and there is nowhere else to report.
func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.openLocked(); err != nil {
			return len(p), nil
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.limit {
		_ = r.f.Close()
		r.f = nil
		_ = os.Rename(r.path, r.path+".1")
		if err := r.openLocked(); err != nil {
			return len(p), nil
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	if err != nil {
		return len(p), nil
	}
	return n, nil
}

// Close closes the current file.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
