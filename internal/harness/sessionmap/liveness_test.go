package sessionmap_test

import (
	"testing"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

// TestLivenessPID pins card 107's host_process_pid: the process the watcher
// polls is the map's key unless the map names another, and a negative one
// is refused.
func TestLivenessPID(t *testing.T) {
	t.Parallel()
	m := sessionmap.ByPID{ClaudePID: 4242}
	if m.LivenessPID() != 4242 {
		t.Fatalf("LivenessPID = %d, want the key", m.LivenessPID())
	}
	m.HostProcessPID = 777
	if m.LivenessPID() != 777 {
		t.Fatalf("LivenessPID = %d, want host_process_pid", m.LivenessPID())
	}
	m = sessionmap.ByPID{ClaudePID: 4242, BrigadeSessionID: "s", TeamKey: "default", ConfigDir: "/c", Inbound: "accept", FrameLevel: "open", HostProcessPID: -1}
	if err := m.Validate(); err == nil {
		t.Fatal("a negative host_process_pid was accepted")
	}
}
