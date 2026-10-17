package inbound_test

import (
	"os"
	"path/filepath"
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
