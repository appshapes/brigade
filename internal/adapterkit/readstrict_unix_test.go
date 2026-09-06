//go:build darwin || linux

package adapterkit

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// readStrictHangCatcher bounds a ReadStrict that must return at once. It is
// a hang catcher, never a performance bound: a FIFO with no writer blocks a
// plain open forever, so a test that times out here has found the defect
// the O_NONBLOCK open exists to prevent.
const readStrictHangCatcher = 30 * time.Second

// strictRefusalReason runs ReadStrict and returns the details.reason of its
// `config` refusal, failing the test on anything else.
func strictRefusalReason(t *testing.T, path string) string {
	t.Helper()
	_, err := ReadStrict(path)
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("ReadStrict(%s) = %v, want a *protocol.Error refusal", path, err)
	}
	if perr.Code != protocol.CodeConfig {
		t.Fatalf("code = %q, want config", perr.Code)
	}
	if perr.Code.Exit() != 11 {
		t.Fatalf("exit = %d, want 11", perr.Code.Exit())
	}
	return perr.Details["reason"]
}

// TestReadStrictRefusesSymlink pins the O_NOFOLLOW half of the hardening:
// a symlink to a perfectly private file is refused as `symlink`, while the
// target itself reads — the positive control that the refusal is about
// the link, not the content.
func TestReadStrictRefusesSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "team.json")
	content := []byte(`{"version":1}`)
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if got := strictRefusalReason(t, link); got != "symlink" {
		t.Errorf("reason = %q, want symlink", got)
	}
	got, err := ReadStrict(target)
	if err != nil {
		t.Fatalf("the target itself must read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("target content = %q, want %q", got, content)
	}
}

// TestReadStrictDoesNotBlockOnFIFO pins the O_NONBLOCK half: a FIFO
// planted at a private file's path — nobody will ever write to it — is
// refused as not_regular at once instead of blocking the caller until a
// writer appears (the P3-2 finding that hung every session-bound command
// on the by-pid map, now closed for every ReadStrict caller too).
func TestReadStrictDoesNotBlockOnFIFO(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "adapter")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan string, 1)
	go func() {
		_, err := ReadStrict(path)
		var perr *protocol.Error
		if !errors.As(err, &perr) {
			done <- "no refusal: " + errString(err)
			return
		}
		done <- perr.Details["reason"]
	}()
	select {
	case reason := <-done:
		if reason != "not_regular" {
			t.Fatalf("reason = %q, want not_regular", reason)
		}
	case <-time.After(readStrictHangCatcher):
		t.Fatalf("ReadStrict blocked on a FIFO for %s; the open must not wait for a writer", readStrictHangCatcher)
	}
}

// TestReadStrictRefusesOversize pins the LimitReader: a file one byte past
// MaxStrictBytes is refused as too_large without its content being
// returned, and a file of exactly MaxStrictBytes reads whole.
func TestReadStrictRefusesOversize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	exact := filepath.Join(dir, "exact")
	if err := os.WriteFile(exact, bytes.Repeat([]byte("x"), MaxStrictBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStrict(exact)
	if err != nil {
		t.Fatalf("a file of exactly MaxStrictBytes must read: %v", err)
	}
	if len(got) != MaxStrictBytes {
		t.Errorf("read %d bytes, want %d", len(got), MaxStrictBytes)
	}

	over := filepath.Join(dir, "over")
	if err := os.WriteFile(over, bytes.Repeat([]byte("x"), MaxStrictBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := strictRefusalReason(t, over); reason != "too_large" {
		t.Errorf("reason = %q, want too_large", reason)
	}
}

// TestReadStrictOwnerCheck covers the owner rule at the two layers a test
// can reach: a file the current user owns passes, and a FileInfo that
// cannot prove an owner (no unix stat behind it) is refused rather than
// trusted. A file owned by another uid cannot be created without
// privileges, so that row is the fileOwnerUID contract plus the uid
// comparison in ReadStrict, both read here.
func TestReadStrictOwnerCheck(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(path, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := fileOwnerUID(fi)
	if !ok || uid != os.Getuid() {
		t.Fatalf("fileOwnerUID = (%d, %v), want (%d, true)", uid, ok, os.Getuid())
	}
	if _, err := ReadStrict(path); err != nil {
		t.Fatalf("a file owned by the current user must read: %v", err)
	}
	if _, ok := fileOwnerUID(noSysInfo{fi}); ok {
		t.Error("a FileInfo without a unix stat reported an owner; it must fail closed")
	}
}

// TestReadStrictMissingIsNotARefusal is the control for the symlink test's
// error mapping: a dangling path keeps fs.ErrNotExist, so callers can still
// tell "missing" from "refused".
func TestReadStrictMissingIsNotARefusal(t *testing.T) {
	t.Parallel()
	_, err := ReadStrict(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		t.Fatalf("a missing file was refused as %v; it must be the caller's mapping", perr)
	}
}

// noSysInfo wraps a FileInfo and hides its Sys, standing in for a
// filesystem whose stat carries no owner.
type noSysInfo struct{ fs.FileInfo }

func (noSysInfo) Sys() any { return nil }

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
