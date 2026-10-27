//go:build (darwin || linux) && !windows

package sessionmap

import (
	"os"
	"path/filepath"
	"testing"
)

// The owner and mode facts are adapterkit's now (private_unix.go,
// private_windows.go; card 133) and tested there; this file keeps the
// symlink refusal, which needs a symlink the test can make.

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
