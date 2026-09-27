package watchstate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var at = time.Date(2026, 9, 27, 12, 0, 0, 0, time.FixedZone("x", -4*3600))

// TestWriteThenRead: each state comes back as written, in UTC, from a
// private file under state/ named after the Claude pid.
func TestWriteThenRead(t *testing.T) {
	t.Parallel()
	for _, state := range []string{Connecting, Connected, Retrying} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := Path(dir, 4242)
			if want := filepath.Join(dir, "state", "4242.watch.json"); path != want {
				t.Fatalf("path %q, want %q", path, want)
			}
			if err := Write(path, 4100, state, at); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := Read(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got.State != state || got.PID != 4100 || !got.Since.Equal(at) || got.Since.Location() != time.UTC || got.Version != FileVersion {
				t.Errorf("state = %+v", got)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("file mode: %v %v", info, err)
			}
			// A second write replaces the first.
			if err := Write(path, 4100, Connected, at.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if got, _ := Read(path); got.State != Connected || !got.Since.Equal(at.Add(time.Minute)) {
				t.Errorf("after the second write: %+v", got)
			}
		})
	}
}

// TestWriteRefusesWhatReadWouldRefuse: an unknown state word and a zero
// time are never written.
func TestWriteRefusesWhatReadWouldRefuse(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 1)
	for name, err := range map[string]error{
		"an unknown state": Write(path, 1, "asleep", at),
		"no state":         Write(path, 1, "", at),
		"no time":          Write(path, 1, Connected, time.Time{}),
		"no pid":           Write(path, 0, Connected, at),
	} {
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused write left a file: %v", err)
	}
}

// TestReadIsStrict: a missing file is fs.ErrNotExist, a file that is not
// the document is ErrMalformed, and a file that is not private is refused
// by the strict reader.
func TestReadIsStrict(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Read(Path(dir, 7)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"not json":         "connected",
		"another version":  `{"version":2,"pid":5,"state":"connected","since":"2026-09-27T16:00:00Z"}`,
		"an unknown state": `{"version":1,"pid":5,"state":"asleep","since":"2026-09-27T16:00:00Z"}`,
		"no time":          `{"version":1,"pid":5,"state":"connected"}`,
		"no pid":           `{"version":1,"state":"connected","since":"2026-09-27T16:00:00Z"}`,
		"empty":            ``,
	} {
		path := filepath.Join(dir, "state", "bad.watch.json")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
	open := filepath.Join(dir, "state", "open.watch.json")
	if err := os.WriteFile(open, []byte(`{"version":1,"pid":5,"state":"connected","since":"2026-09-27T16:00:00Z"}`), 0o644); err != nil { //nolint:gosec // G306: a file that is NOT private is the case
		t.Fatal(err)
	}
	if _, err := Read(open); err == nil {
		t.Error("a world-readable file was read")
	}
}

// TestRemove: a watcher removes its own file and no other's — the one a
// successor wrote stays — and removing a missing one is no error.
func TestRemove(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 9)
	if err := Write(path, 4100, Retrying, at); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path, 4101); err != nil {
		t.Fatalf("remove by another watcher: %v", err)
	}
	if got, err := Read(path); err != nil || got.PID != 4100 {
		t.Fatalf("another watcher removed the file: %+v %v", got, err)
	}
	if err := Remove(path, 4100); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file is still there: %v", err)
	}
	if err := Remove(path, 4100); err != nil {
		t.Errorf("removing a missing file: %v", err)
	}
}
