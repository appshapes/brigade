package adapterkit_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
)

func TestWritePidfileCreates0600WithContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "1234.json")
	content := []byte(`{"pid":1234,"start_token":"t"}`)
	if err := adapterkit.WritePidfile(path, content); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content = %q, want %q", got, content)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestWritePidfileRefusesExisting(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "1234.json")
	if err := adapterkit.WritePidfile(path, []byte("mine")); err != nil {
		t.Fatal(err)
	}
	err := adapterkit.WritePidfile(path, []byte("intruder"))
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v, want fs.ErrExist (O_EXCL must refuse a live predecessor)", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "mine" {
		t.Fatalf("the refused create clobbered the file: %q", got)
	}
}

func TestRemovePidfileMatchingContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "1234.json")
	content := []byte("mine")
	if err := adapterkit.WritePidfile(path, content); err != nil {
		t.Fatal(err)
	}
	removed, err := adapterkit.RemovePidfile(path, content)
	if err != nil || !removed {
		t.Fatalf("RemovePidfile = (%v, %v), want (true, nil)", removed, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still exists after a matched remove")
	}
}

// TestRemovePidfileKeepsAReplacement is E0-5's correction 3: the replace
// path leaves the superseded watcher running, and its cleanup must NOT
// delete the pidfile that by then belongs to its replacement.
func TestRemovePidfileKeepsAReplacement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "1234.json")
	mine := []byte(`{"pid":1234,"start_token":"a"}`)
	if err := adapterkit.WritePidfile(path, mine); err != nil {
		t.Fatal(err)
	}
	// The replacement takes over the pidfile (same length, different
	// bytes, so a length-only comparison would wrongly delete it).
	theirs := []byte(`{"pid":1234,"start_token":"b"}`)
	if err := os.WriteFile(path, theirs, 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := adapterkit.RemovePidfile(path, mine)
	if err != nil || removed {
		t.Fatalf("RemovePidfile = (%v, %v), want (false, nil) for a replaced file", removed, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the replacement's pidfile was deleted: %v", err)
	}
	if string(got) != string(theirs) {
		t.Fatalf("content = %q, want the replacement's %q", got, theirs)
	}
}

func TestRemovePidfileMissingIsNotAnError(t *testing.T) {
	t.Parallel()
	removed, err := adapterkit.RemovePidfile(filepath.Join(t.TempDir(), "absent"), []byte("x"))
	if err != nil || removed {
		t.Fatalf("RemovePidfile = (%v, %v), want (false, nil)", removed, err)
	}
}
