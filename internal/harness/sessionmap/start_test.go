package sessionmap_test

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

func validStart() sessionmap.StartFacts {
	return sessionmap.StartFacts{
		ClaudePID: 4242, ClaudeSessionID: "native-1", ConfigDir: "/cfg/brigade",
		PluginBin: "/plugins/brigade/bin/brigade", WrittenAt: time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC),
	}
}

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		t.Fatalf("not a protocol error: %v", err)
	}
	return pe.Details["reason"]
}

// TestStartFactsRoundTrip: the file is 0600 beside the map, reads back
// whole, and a missing one is fs.ErrNotExist for the caller to name.
func TestStartFactsRoundTrip(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	want := validStart()
	if err := s.WriteStart(&want); err != nil {
		t.Fatal(err)
	}
	path, err := s.StartPath(want.ClaudePID)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("start facts mode = %v, %v", fi, err)
	}
	got, err := s.ReadStart(want.ClaudePID)
	if err != nil || *got != want {
		t.Fatalf("ReadStart = %+v, %v; want %+v", got, err, want)
	}
	if _, err := s.ReadStart(9999); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing start facts: %v", err)
	}
	if err := s.DeleteStart(want.ClaudePID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadStart(want.ClaudePID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteStart(want.ClaudePID); err != nil {
		t.Fatalf("a second delete: %v", err)
	}
}

// TestStartFactsRefusals: the map's privacy rule applies (a group-readable
// file is refused), a planted file naming another pid is refused, and an
// invalid document never gets written.
func TestStartFactsRefusals(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	f := validStart()
	if err := s.WriteStart(&f); err != nil {
		t.Fatal(err)
	}
	path, _ := s.StartPath(f.ClaudePID)
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode IS the case
		t.Fatal(err)
	}
	if _, err := s.ReadStart(f.ClaudePID); reasonOf(t, err) != sessionmap.ReasonMapNotPrivate {
		t.Fatalf("group-readable start facts: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	// Planted under another pid's name.
	other, _ := s.StartPath(4243)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, data, 0o600); err != nil { //nolint:gosec // G703: a path this test derived from its own store
		t.Fatal(err)
	}
	if _, err := s.ReadStart(4243); reasonOf(t, err) != sessionmap.ReasonMapMismatch {
		t.Fatalf("planted start facts: %v", err)
	}
	// A planted file with a relative config dir is refused by the READER
	// too, not only by the writer.
	if err := os.WriteFile(path, []byte(`{"claude_pid":4242,"config_dir":"relative/dir"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadStart(f.ClaudePID); reasonOf(t, err) != sessionmap.ReasonMapInvalid {
		t.Fatalf("relative config dir on read: %v", err)
	}
	// Invalid: a relative config dir, a non-positive pid.
	bad := validStart()
	bad.ConfigDir = "relative/dir"
	if err := s.WriteStart(&bad); reasonOf(t, err) != sessionmap.ReasonMapInvalid {
		t.Fatalf("relative config dir: %v", err)
	}
	bad = validStart()
	bad.ClaudePID = 0
	if err := s.WriteStart(&bad); reasonOf(t, err) != sessionmap.ReasonMapInvalid {
		t.Fatalf("pid 0: %v", err)
	}
}
