package inbound

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// These tests use real files under t.TempDir and no synctest bubble.

func seenStore(t *testing.T) FileSeenStore {
	t.Helper()
	return FileSeenStore{Path: SeenPath(filepath.Join(t.TempDir(), "state-root"), 4242)}
}

func TestSeenPath(t *testing.T) {
	t.Parallel()
	got := SeenPath("/s", 77)
	if got != filepath.Join("/s", "state", "77.seen.json") {
		t.Fatalf("SeenPath = %q", got)
	}
}

func TestFileSeenStoreRoundTripModesAndMissing(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	ids, err := s.Load()
	if err != nil || ids != nil {
		t.Fatalf("missing file: (%v, %v), want (nil, nil)", ids, err)
	}
	want := []string{"m1", "m2", "m3"}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %o, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o, want 0700", di.Mode().Perm())
	}
	got, err := s.Load()
	if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Load = (%v, %v), want %v", got, err, want)
	}
	// Overwrite semantics: a later Save replaces, never appends.
	if err := s.Save([]string{"m9"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || len(got) != 1 || got[0] != "m9" {
		t.Fatalf("after overwrite: (%v, %v)", got, err)
	}
	// An empty list is a valid file that loads as empty.
	if err := s.Save(nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || len(got) != 0 {
		t.Fatalf("empty list: (%v, %v)", got, err)
	}
}

func TestFileSeenStoreBounded(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	ids := make([]string, 0, SeenCapacity+500)
	for i := range SeenCapacity + 500 {
		ids = append(ids, "id-"+strconv.Itoa(i))
	}
	if err := s.Save(ids); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != SeenCapacity || got[0] != "id-500" || got[len(got)-1] != "id-"+strconv.Itoa(SeenCapacity+499) {
		t.Fatalf("bounded load: %d ids, first %q, last %q", len(got), got[0], got[len(got)-1])
	}
	// The file itself holds only the bound (not merely the reader).
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"id-499"`) || !strings.Contains(string(data), `"id-500"`) {
		t.Fatal("file was not bounded at save time")
	}
}

func TestFileSeenStoreRefusesInsecureMode(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	if err := s.Save([]string{"m1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode is what this test refuses
		t.Fatal(err)
	}
	_, err := s.Load()
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeConfig || perr.Details["reason"] != "insecure_mode" {
		t.Fatalf("0644 seen file: err %v, want config/insecure_mode", err)
	}
	// Positive control: back to 0600 it loads.
	if err := os.Chmod(s.Path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || len(got) != 1 {
		t.Fatalf("0600 seen file: (%v, %v)", got, err)
	}
	// A Save after a refusal replaces the insecure file with a 0600 one.
	if err := os.Chmod(s.Path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode is what this test refuses
		t.Fatal(err)
	}
	if err := s.Save([]string{"m2"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode after re-save %o", fi.Mode().Perm())
	}
}

func TestFileSeenStoreMalformedAndBadIDs(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(s.Path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"not json":      `{"version":1,"message_ids":[`,
		"wrong version": `{"version":2,"message_ids":["a"]}`,
		"no version":    `{"message_ids":["a"]}`,
		"wrong type":    `{"version":1,"message_ids":"a"}`,
		"too large":     `{"version":1,"message_ids":["` + strings.Repeat("x", MaxSeenFileBytes) + `"]}`,
	} {
		write(content)
		if got, err := s.Load(); err == nil {
			t.Errorf("%s: loaded %v, want an error", name, got)
		}
	}
	// A file holding more than the bound loads as the LAST SeenCapacity.
	var many strings.Builder
	many.WriteString(`{"version":1,"message_ids":[`)
	for i := range SeenCapacity + 100 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`"f` + strconv.Itoa(i) + `"`)
	}
	many.WriteString(`]}`)
	write(many.String())
	got, err := s.Load()
	if err != nil || len(got) != SeenCapacity || got[0] != "f100" || got[len(got)-1] != "f"+strconv.Itoa(SeenCapacity+99) {
		t.Fatalf("over-bound file: %d ids, err %v", len(got), err)
	}
	// Bad ids are skipped, good ones kept, order preserved.
	write(`{"version":1,"message_ids":["", "ok1", "` + strings.Repeat("x", MaxMessageIDBytes+1) + `", "ok2", "bad�"]}`)
	got, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "ok1,ok2,bad�" {
		t.Fatalf("filtered load = %q", got)
	}
}

func TestMemorySeenStore(t *testing.T) {
	t.Parallel()
	var m MemorySeenStore
	if got, err := m.Load(); err != nil || len(got) != 0 || m.Saves() != 0 {
		t.Fatalf("zero store: %v %v %d", got, err, m.Saves())
	}
	in := []string{"a", "b"}
	if err := m.Save(in); err != nil {
		t.Fatal(err)
	}
	in[0] = "mutated"
	got, _ := m.Load()
	if strings.Join(got, ",") != "a,b" || m.Saves() != 1 {
		t.Fatalf("Save did not copy: %v (saves %d)", got, m.Saves())
	}
	got[0] = "mutated"
	if ids := m.IDs(); ids[0] != "a" {
		t.Fatalf("Load did not copy: %v", ids)
	}
}

func TestFileSeenStoreSaveFailsWhenTheDirectoryCannotBeMade(t *testing.T) {
	t.Parallel()
	// The parent of the state directory is a regular file, so the 0700
	// chain cannot be created and Save reports it instead of writing
	// somewhere else.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := FileSeenStore{Path: SeenPath(blocker, 1)}
	if err := s.Save([]string{"m1"}); err == nil {
		t.Fatal("Save succeeded under a file")
	}
	if _, err := os.Stat(s.Path); err == nil {
		t.Fatal("something was written under the blocker file")
	}
}
