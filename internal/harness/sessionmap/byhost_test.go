//go:build !windows

package sessionmap_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

func TestCheckHost(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"muse", "claude-code", "a1"} {
		if err := sessionmap.CheckHost(ok); err != nil {
			t.Errorf("CheckHost(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Muse", "a/b", "../x", "a b", strings.Repeat("a", 33), "a.b"} {
		details := assertConfig(t, sessionmap.CheckHost(bad), sessionmap.ReasonMapInvalid)
		if details["field"] != "host" {
			t.Errorf("CheckHost(%q): details.field = %q", bad, details["field"])
		}
	}
}

// TestByHostRoundTrip writes, reads and removes a by-host pointer (card
// 88), checks its mode and path, and refuses the shapes it must refuse.
func TestByHostRoundTrip(t *testing.T) {
	t.Parallel()
	s := sessionmap.Store{StateDir: filepath.Join(t.TempDir(), "state")}
	const id = "01a14785-947f-7d93-a4f4-6ee1f6b2be94"
	want := &sessionmap.ByHost{HostPID: 4242, BrigadeSessionID: "6f0f", UpdatedAt: time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)}
	if err := s.WriteByHost("muse", id, want); err != nil {
		t.Fatal(err)
	}
	path, err := s.ByHostPath("muse", id)
	if err != nil || path != filepath.Join(s.StateDir, "sessions", "by-host", "muse", id+".json") {
		t.Fatalf("path %q %v", path, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	got, err := s.ReadByHost("muse", id)
	if err != nil || got.HostPID != 4242 || got.BrigadeSessionID != "6f0f" || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("ReadByHost = %+v, %v", got, err)
	}
	// Overwrite wins, as the by-native map does.
	if err := s.WriteByHost("muse", id, &sessionmap.ByHost{HostPID: 4243, BrigadeSessionID: "6f0f"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadByHost("muse", id); got.HostPID != 4243 {
		t.Fatalf("overwrite did not take: %+v", got)
	}
	if err := s.DeleteByHost("muse", id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadByHost("muse", id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteByHost("muse", id); err != nil {
		t.Fatalf("a second delete is not an error: %v", err)
	}
	// Refusals: an invalid pointer, a hostile host, a hostile id.
	assertConfig(t, s.WriteByHost("muse", id, &sessionmap.ByHost{BrigadeSessionID: "6f0f"}), sessionmap.ReasonMapInvalid)
	assertConfig(t, s.WriteByHost("muse", id, &sessionmap.ByHost{HostPID: 1}), sessionmap.ReasonMapInvalid)
	if _, err := s.ByHostPath("../"+evilMarker, id); err == nil {
		t.Fatal("a traversal host was accepted")
	}
	if _, err := s.ByHostPath("muse", "../"+evilMarker); err == nil {
		t.Fatal("a traversal id was accepted")
	}
}

// TestByPIDHarnessAndModel pins the two card-88 members: absent reads as a
// Claude Code map; a muse map carries both; a hostile value is refused.
func TestByPIDHarnessAndModel(t *testing.T) {
	t.Parallel()
	s := sessionmap.Store{StateDir: filepath.Join(t.TempDir(), "state")}
	m := validByPID()
	m.ClaudePID = 4242
	if err := s.WriteByPID(&m); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadByPID(4242)
	if err != nil || got.Harness != "" || got.Model != "" {
		t.Fatalf("a map without the members: %+v %v", got, err)
	}
	path, _ := s.ByPIDPath(4242)
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"harness"`) || strings.Contains(string(data), `"model"`) {
		t.Fatalf("absent members were written: %s", data)
	}
	m.Harness, m.Model = "muse", "muse-wonder-full-1.0"
	if err := s.WriteByPID(&m); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadByPID(4242); err != nil || got.Harness != "muse" || got.Model != "muse-wonder-full-1.0" {
		t.Fatalf("a muse map: %+v %v", got, err)
	}
	m.Harness = "Muse Code"
	assertConfig(t, m.Validate(), sessionmap.ReasonMapInvalid)
	m.Harness = "muse"
	m.Model = "model\nwith newline"
	assertConfig(t, m.Validate(), sessionmap.ReasonMapInvalid)
}
