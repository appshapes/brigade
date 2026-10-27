//go:build !windows

package inbound_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
)

// TestSinkPaths pins the per-watcher sink naming of card 88: under the
// session's inbox directory, sorting by name as by time, the newest found
// among regular .ndjson files only.
func TestSinkPaths(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const sid = "6f0f2b41-5a3c-49d7-b8e2-0c7a4f1e6d33"
	if inbound.NewestSink(stateDir, sid) != "" {
		t.Fatal("a missing directory has a newest sink")
	}
	earlier := inbound.NewSinkPath(stateDir, sid, time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC))
	later := inbound.NewSinkPath(stateDir, sid, time.Date(2026, 10, 17, 2, 0, 1, 0, time.UTC))
	if filepath.Dir(earlier) != inbound.SinkDir(stateDir, sid) || filepath.Dir(earlier) != filepath.Join(stateDir, "inbox", sid) {
		t.Fatalf("dir %q", filepath.Dir(earlier))
	}
	if filepath.Base(earlier) >= filepath.Base(later) {
		t.Fatalf("names do not sort by time: %q !< %q", earlier, later)
	}
	if err := os.MkdirAll(inbound.SinkDir(stateDir, sid), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{later, earlier, filepath.Join(inbound.SinkDir(stateDir, sid), "zzz-not-a-sink.txt")} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(inbound.SinkDir(stateDir, sid), "zzzz.ndjson"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := inbound.NewestSink(stateDir, sid); got != later {
		t.Fatalf("NewestSink = %q, want %q", got, later)
	}
}

func TestAdoptGrokSinkMovesPrivateFiles(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	const session = "s"
	from := strings.Repeat("a", 32)
	to := strings.Repeat("b", 32)
	if err := inbound.AdoptGrokSink(state, session, from, to); err != nil {
		t.Fatal(err)
	}
	src := inbound.GrokSinkDir(state, session, from)
	dst := inbound.GrokSinkDir(state, session, to)
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("{\"message_id\":\"kept\"}\n")
	const name = "00000000000000000001.ndjson"
	if err := os.WriteFile(filepath.Join(src, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, name), []byte("occupant\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := inbound.AdoptGrokSink(state, session, from, to); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "00000000000000000001~1.ndjson"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("moved file %q %v", got, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("old incarnation directory remained")
	}
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(src, "00000000000000000002.ndjson")); err != nil {
		t.Fatal(err)
	}
	if err := inbound.AdoptGrokSink(state, session, from, to); err == nil {
		t.Fatal("symlink sink adopted")
	}
}
