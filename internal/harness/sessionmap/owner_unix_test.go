//go:build darwin || linux

package sessionmap

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noSys is a FileInfo from no unix filesystem: Sys is nil.
type noSys struct{}

func (noSys) Name() string       { return "x" }
func (noSys) Size() int64        { return 0 }
func (noSys) Mode() fs.FileMode  { return 0o600 }
func (noSys) ModTime() time.Time { return time.Time{} }
func (noSys) IsDir() bool        { return false }
func (noSys) Sys() any           { return nil }

func TestOwnerUIDOfARealFileIsTheCurrentUser(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := ownerUID(fi)
	if !ok || uid != os.Getuid() {
		t.Fatalf("ownerUID = %d, %v; want %d, true", uid, ok, os.Getuid())
	}
}

func TestOwnerUIDWithoutAUnixStatIsUnknown(t *testing.T) {
	t.Parallel()
	if uid, ok := ownerUID(noSys{}); ok {
		t.Fatalf("ownerUID(noSys) = %d, true; want ok=false (unprovable ownership must be refused)", uid)
	}
}

func TestOpenNoFollowRefusesASymlinkAndOpensARegularFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openNoFollow(link); err == nil {
		_ = f.Close()
		t.Fatal("openNoFollow followed a symlink")
	} else if !isSymlinkRefusal(err) {
		t.Fatalf("openNoFollow(symlink) = %v, want ELOOP", err)
	}
	f, err := openNoFollow(target)
	if err != nil {
		t.Fatalf("openNoFollow(regular) = %v", err)
	}
	_ = f.Close()
	if isSymlinkRefusal(os.ErrNotExist) {
		t.Fatal("isSymlinkRefusal must not match an unrelated error")
	}
}
