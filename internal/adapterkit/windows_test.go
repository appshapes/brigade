//go:build windows

package adapterkit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockFileExcludesASecondHandleInThisProcess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.lock")
	first, err := LockFile(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := LockFile(path, 200*time.Millisecond); err == nil {
		t.Fatal("a second LockFile on a held lock succeeded")
	} else if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("the contended LockFile gave up after %v, before its timeout", time.Since(start))
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	second, err := LockFile(path, time.Second)
	if err != nil {
		t.Fatalf("LockFile after Unlock = %v", err)
	}
	_ = second.Unlock()
}

func TestTryLockFileReportsWouldBlock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "y.lock")
	a, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if err := TryLockFile(a); err != nil {
		t.Fatal(err)
	}
	err = TryLockFile(b)
	if err == nil || !LockWouldBlock(err) {
		t.Fatalf("TryLockFile on a held lock = %v; want a would-block error", err)
	}
	if LockWouldBlock(errors.New("other")) {
		t.Fatal("LockWouldBlock matched an unrelated error")
	}
}

func TestOpenNoFollowOpensARegularFileAndRefusesAJunction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenNoFollow(target, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenNoFollow(regular) = %v", err)
	}
	_ = f.Close()
	if IsSymlinkRefusal(os.ErrNotExist) {
		t.Fatal("IsSymlinkRefusal matched an unrelated error")
	}
	// A symbolic link needs a privilege most runners lack; os.Symlink says so and the check is skipped
	// then, with the regular-file half above still proven.
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot make a symbolic link here (%v); the refusal half is not measured", err)
	}
	if f, err := OpenNoFollow(link, os.O_RDONLY, 0); err == nil {
		_ = f.Close()
		t.Fatal("OpenNoFollow followed a symbolic link")
	} else if !IsSymlinkRefusal(err) {
		t.Fatalf("OpenNoFollow(symlink) = %v; want the reparse-point refusal", err)
	}
}

func TestReadStrictTrustsTheProfileDirectoryOnWindows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private.json")
	if err := WriteAtomic(path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	data, err := ReadStrict(path)
	if err != nil {
		t.Fatalf("ReadStrict on Windows refused a fresh private file: %v", err)
	}
	if string(data) != "{\"a\":1}\n" {
		t.Fatalf("ReadStrict = %q", data)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if InsecureMode(fi) || ForeignOwner(fi) || WorldWritable(fi) || !IsExecutable(fi) {
		t.Fatalf("the Windows facts: insecure=%v foreign=%v worldwritable=%v executable=%v; want false,false,false,true", InsecureMode(fi), ForeignOwner(fi), WorldWritable(fi), IsExecutable(fi))
	}
}

func TestWindowsDirectoriesComeFromTheProfile(t *testing.T) {
	t.Parallel()
	env := []string{`APPDATA=C:\Users\me\AppData\Roaming`, `LOCALAPPDATA=C:\Users\me\AppData\Local`, `USERPROFILE=C:\Users\me`, `Path=C:\Windows`}
	if d, err := ConfigDir(env); err != nil || d != `C:\Users\me\AppData\Roaming\brigade` {
		t.Fatalf("ConfigDir = %q, %v", d, err)
	}
	if d, err := StateDir(env); err != nil || d != `C:\Users\me\AppData\Local\brigade\state` {
		t.Fatalf("StateDir = %q, %v", d, err)
	}
	if d, ok := CacheBinDir(env); !ok || d != `C:\Users\me\AppData\Local\brigade\bin` {
		t.Fatalf("CacheBinDir = %q, %v", d, ok)
	}
	if h := HomeDir(env); h != `C:\Users\me` {
		t.Fatalf("HomeDir = %q", h)
	}
	if v := Getenv(env, "PATH"); v != `C:\Windows` {
		t.Fatalf("Getenv(PATH) = %q; want the Path= entry, case ignored", v)
	}
	if d, err := ConfigDir([]string{`USERPROFILE=C:\Users\me`}); err == nil {
		t.Fatalf("ConfigDir without APPDATA = %q; want config", d)
	}
	if d, err := ConfigDir([]string{`BRIGADE_CONFIG_DIR=D:\cfg`}); err != nil || d != `D:\cfg` {
		t.Fatalf("BRIGADE_CONFIG_DIR = %q, %v", d, err)
	}
	child := ChildEnv([]string{`SystemRoot=C:\Windows`, `Path=C:\Windows`, `BRIGADE_X=1`, `HTTPS_PROXY=http://p`, `GODEBUG=x`})
	want := map[string]bool{`SystemRoot=C:\Windows`: true, `Path=C:\Windows`: true, `HTTPS_PROXY=http://p`: true}
	if len(child) != len(want) {
		t.Fatalf("ChildEnv = %v; want exactly %v", child, want)
	}
	for _, kv := range child {
		if !want[kv] {
			t.Fatalf("ChildEnv kept %q", kv)
		}
	}
}
