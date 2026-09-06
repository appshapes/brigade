package teamfile

import (
	"os"
	"path/filepath"
)

// Toplevel walks up from startDir looking for the enclosing repository
// toplevel: the first directory carrying a `.git` entry — a directory in
// a plain clone, a regular file in a linked worktree; either counts.
// No toplevel means NO team-file discovery at all (owner ruling 2): a
// `.brigade.json` above a repository, or anywhere outside one, must
// never act as a personal default team, so the caller gets false and
// nothing was opened — this function only ever Lstats `.git` entries.
func Toplevel(startDir string) (string, bool) {
	dir := filepath.Clean(startDir)
	for {
		if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if fi.IsDir() || fi.Mode().IsRegular() {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Discover locates the team file for a session working in cwd: the
// NEAREST `.brigade.json` between cwd and the enclosing repository
// toplevel, inclusive. The walk never crosses the toplevel — worktree
// and plain clone alike — and a cwd inside no repository discovers
// nothing, with zero file opens (only `.git` and candidate Lstats).
// The returned path is not yet validated; Parse does that.
func Discover(cwd string) (string, bool) {
	top, ok := Toplevel(cwd)
	if !ok {
		return "", false
	}
	dir := filepath.Clean(cwd)
	for {
		candidate := filepath.Join(dir, FileName)
		if fi, err := os.Lstat(candidate); err == nil && !fi.IsDir() {
			return candidate, true
		}
		if dir == top {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Canonicalize resolves the directory that holds a discovered team file
// to its symlink-free form — the ONE canonicalization create, join and
// the hook all share, so a pin written through a symlinked checkout path
// is found through the real one and vice versa (review low fix 7).
//
// Caveat, documented rather than solved: macOS APFS is case-insensitive
// by default, so /Users/a/dev/Payments and …/payments are one directory
// but two distinct canonical keys; the DEBUG log on a pin miss names the
// key looked up so the mismatch is diagnosable.
func Canonicalize(dir string) (string, error) {
	return filepath.EvalSymlinks(dir)
}
