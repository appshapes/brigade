// Package musedigest computes the package digest Muse Code computes over a
// plugin package (card 114), so marketplace.json's `integrity.digest` can be
// written and verified without Muse.
//
// A Muse marketplace entry carries `integrity.digest`, "the SHA-256 of the
// package tree", and Muse installs the package only when that value equals
// the `package_sha256` it computes itself (card 115). The Muse documentation
// gives no serialisation. It is this, read from Muse Code 1.4.14 and held
// against the digests Muse reports (the tests pin them):
//
//	SHA-256 over, for every regular file in the tree, in ascending byte order
//	of its path:
//
//	    path  0x00  the file's bytes  0xFF
//
// The path is relative to the package root, its components joined with "/",
// with no leading "./". Nothing else enters the digest: no file mode, size,
// time or owner, no directory entry (an empty directory changes nothing), no
// count and no header. Directories named .git, .hg or .svn are left out at
// every depth, with everything under them; a FILE of one of those names is
// hashed like any other, and so is every other dot file (.DS_Store included,
// which is why a package directory must be clean). File bytes are taken as
// they are: no line-ending or Unicode normalisation.
//
// Muse refuses a package that holds a symlink or anything else that is
// neither a regular file nor a directory, so Sum refuses one too rather than
// return a digest Muse would never compute.
package musedigest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
)

// Prefix starts every digest: `sha256:` and 64 lower-case hex digits.
const Prefix = "sha256:"

// ErrNotPackage is returned for a tree Muse would not install: one holding
// something other than regular files and directories, or no file at all.
var ErrNotPackage = errors.New("not a Muse package tree")

// vcsDirs are the directory names Muse leaves out of the digest, at any depth.
var vcsDirs = map[string]bool{".git": true, ".hg": true, ".svn": true}

// Sum returns the digest of the package tree in fsys, whose root is the
// package root (the directory holding .muse-plugin/).
func Sum(fsys fs.FS) (string, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		switch {
		case d.IsDir():
			if name != "." && vcsDirs[path.Base(name)] {
				return fs.SkipDir
			}
		case d.Type().IsRegular():
			files = append(files, name)
		default:
			return fmt.Errorf("%w: %s is neither a regular file nor a directory (%s); Muse refuses the package", ErrNotPackage, name, d.Type())
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("%w: it holds no file", ErrNotPackage)
	}
	// WalkDir's order is lexical per directory, which is not the byte order of the whole path: "a.b" sorts
	// before "a/b" here, and after it in a walk.
	slices.Sort(files)
	h := sha256.New()
	for _, name := range files {
		if err := addFile(h, fsys, name); err != nil {
			return "", err
		}
	}
	return Prefix + hex.EncodeToString(h.Sum(nil)), nil
}

// addFile writes one file's record: its path, a zero byte, its bytes, 0xFF.
func addFile(h io.Writer, fsys fs.FS, name string) (err error) {
	f, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err := io.WriteString(h, name); err != nil {
		return err
	}
	if _, err := h.Write([]byte{0x00}); err != nil {
		return err
	}
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	_, err = h.Write([]byte{0xff})
	return err
}

// SumDir is Sum over the directory dir.
func SumDir(dir string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrNotPackage, dir)
	}
	return Sum(os.DirFS(dir))
}
