package sessionmap_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

// TestStartPointer pins card 116's start-time pointer: written beside the
// attached pointer under by-host/<host>/, read back as the key it names,
// a missing one is fs.ErrNotExist, a bad key is refused, and a hostile id
// never becomes a path.
func TestStartPointer(t *testing.T) {
	t.Parallel()
	s := sessionmap.Store{StateDir: filepath.Join(t.TempDir(), "state")}
	const id = "01a14aa0-8a90-7712-a2b8-79405a4acbca"
	if _, err := s.ReadStartPointer("codex", id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing pointer: %v", err)
	}
	if err := s.WriteStartPointer("codex", id, 1<<30|7, time.Now()); err != nil {
		t.Fatal(err)
	}
	path, err := s.StartPointerPath("codex", id)
	if err != nil || filepath.Dir(path) != s.ByHostDir("codex") || filepath.Base(path) != id+".start.json" {
		t.Fatalf("path %q %v", path, err)
	}
	key, err := s.ReadStartPointer("codex", id)
	if err != nil || key != 1<<30|7 {
		t.Fatalf("read %d %v", key, err)
	}
	// Beside, not instead of, the attached pointer.
	if _, err := s.ReadByHost("codex", id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the start pointer stood in for the attached pointer: %v", err)
	}
	if err := s.WriteStartPointer("codex", id, 0, time.Now()); err == nil {
		t.Fatal("a zero key was accepted")
	}
	if _, err := s.StartPointerPath("codex", "../evil"); err == nil {
		t.Fatal("a traversal id became a path")
	}
	if err := s.DeleteStartPointer("codex", id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadStartPointer("codex", id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteStartPointer("codex", id); err != nil {
		t.Fatalf("a second delete is not an error: %v", err)
	}
}
