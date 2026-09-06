package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

func sampleMap(pid int) sessionmap.ByPID {
	return sessionmap.ByPID{
		ClaudePID:        pid,
		ClaudeSessionID:  "beee3690-1111-4222-8333-444455556666",
		BrigadeSessionID: "6f0f2b41-5a3c-49d7-b8e2-0c7a4f1e6d33",
		TeamRef:          "team-ref-1",
		TeamName:         "ops",
		SessionName:      "payments-api",
		PermissionMode:   "default",
		Inbound:          protocol.InboundAccept,
		FrameLevel:       "open",
		SocketPath:       "/tmp/cc-socks/4242.sock",
		TeamKey:          "work",
		ConfigDir:        "/home/u/.config/brigade",
		AdapterCommand:   []string{"/opt/adapter-fs", "--root", "/srv/store"},
		PluginBin:        "/plugin/bin/brigade",
		HarnessVersion:   "2.1.259",
		RegisteredAt:     time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 9, 2, 12, 0, 5, 0, time.UTC),
	}
}

func writeMap(t *testing.T, stateDir string, m sessionmap.ByPID) string {
	t.Helper()
	s := sessionmap.Store{StateDir: stateDir}
	if err := s.WriteByPID(&m); err != nil {
		t.Fatal(err)
	}
	path, err := s.ByPIDPath(m.ClaudePID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// sysInfo is a real FileInfo whose Sys reports another owner.
type sysInfo struct {
	fs.FileInfo
	sys any
}

func (i sysInfo) Sys() any { return i.sys }

func statWithUIDDelta(delta uint32) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, errors.New("no Stat_t")
		}
		copied := *st
		copied.Uid += delta
		return sysInfo{FileInfo: fi, sys: &copied}, nil
	}
}

func TestSessionPositiveControlResolvesEveryField(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "state")
	want := sampleMap(4242)
	writeMap(t, stateDir, want)
	got, err := config.Session([]string{"HOME=/h", "CLAUDE_PID=4242", "BRIGADE_PROFILE=" + evilMarker}, stateDir)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.ClaudePID != 4242 || got.TeamKey != "work" || got.ConfigDir != want.ConfigDir || got.BrigadeSessionID != want.BrigadeSessionID ||
		got.TeamRef != want.TeamRef || got.TeamName != want.TeamName || got.SocketPath != want.SocketPath || got.Inbound != want.Inbound ||
		!slices.Equal(got.AdapterCommand, want.AdapterCommand) || got.ClaudeSessionID != want.ClaudeSessionID || got.SessionName != want.SessionName ||
		got.PluginBin != want.PluginBin || got.HarnessVersion != want.HarnessVersion || !got.RegisteredAt.Equal(want.RegisteredAt) {
		t.Fatalf("Session =\n %+v\nwant\n %+v", *got, want)
	}
	adapter, err := config.AdapterFromArgv(got.AdapterCommand)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, adapter, want.AdapterCommand, false, config.SourceMap)
}

func TestSessionRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		environ []string
		prepare func(t *testing.T, stateDir string)
		reason  string
	}{
		{"no CLAUDE_PID", []string{"HOME=/h"}, nil, config.ReasonNotInSession},
		{"CLAUDE_PID junk", []string{"CLAUDE_PID=" + evilMarker}, nil, config.ReasonInvalidClaudePID},
		{"CLAUDE_PID zero", []string{"CLAUDE_PID=0"}, nil, config.ReasonInvalidClaudePID},
		{"no map", []string{"CLAUDE_PID=4242"}, nil, config.ReasonNotRegistered},
		{"map for another pid only", []string{"CLAUDE_PID=4242"}, func(t *testing.T, stateDir string) {
			t.Helper()
			writeMap(t, stateDir, sampleMap(4243))
		}, config.ReasonNotRegistered},
		{"map 0644", []string{"CLAUDE_PID=4242"}, func(t *testing.T, stateDir string) {
			t.Helper()
			path := writeMap(t, stateDir, sampleMap(4242))
			//nolint:gosec // G302: a group- or world-readable file is the PRECONDITION this test refuses
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}, sessionmap.ReasonMapNotPrivate},
		{"map 0660", []string{"CLAUDE_PID=4242"}, func(t *testing.T, stateDir string) {
			t.Helper()
			path := writeMap(t, stateDir, sampleMap(4242))
			//nolint:gosec // G302: a group- or world-readable file is the PRECONDITION this test refuses
			if err := os.Chmod(path, 0o660); err != nil {
				t.Fatal(err)
			}
		}, sessionmap.ReasonMapNotPrivate},
		{"map is a symlink", []string{"CLAUDE_PID=4242"}, func(t *testing.T, stateDir string) {
			t.Helper()
			genuine := writeMap(t, filepath.Join(t.TempDir(), "elsewhere"), sampleMap(4242))
			s := sessionmap.Store{StateDir: stateDir}
			if err := os.MkdirAll(s.ByPIDDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			path, err := s.ByPIDPath(4242)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(genuine, path); err != nil {
				t.Fatal(err)
			}
		}, sessionmap.ReasonMapNotPrivate},
		{"map malformed", []string{"CLAUDE_PID=4242"}, func(t *testing.T, stateDir string) {
			t.Helper()
			path := writeMap(t, stateDir, sampleMap(4242))
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, sessionmap.ReasonMapMalformed},
		{"map with a relative adapter", []string{"CLAUDE_PID=4242"}, func(t *testing.T, stateDir string) {
			t.Helper()
			path := writeMap(t, stateDir, sampleMap(4242))
			if err := os.WriteFile(path, []byte(`{"claude_pid":4242,"brigade_session_id":"x","profile":"default","config_dir":"/c","inbound":"accept","adapter_command":["bin/`+evilMarker+`"]}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, sessionmap.ReasonMapInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := filepath.Join(t.TempDir(), "state")
			if tc.prepare != nil {
				tc.prepare(t, stateDir)
			}
			_, err := config.Session(tc.environ, stateDir)
			assertConfig(t, err, tc.reason)
		})
	}
}

func TestSessionRefusesAForeignOwnerThroughTheInjectedStat(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeMap(t, stateDir, sampleMap(4242))
	environ := []string{"CLAUDE_PID=4242"}

	// Control: the injected stat reporting the TRUE owner passes, so the
	// refusal below is the uid comparison and not the injection.
	if _, err := config.SessionIn(environ, sessionmap.Store{StateDir: stateDir, Stat: statWithUIDDelta(0)}); err != nil {
		t.Fatalf("control refused: %v", err)
	}
	_, err := config.SessionIn(environ, sessionmap.Store{StateDir: stateDir, Stat: statWithUIDDelta(1)})
	details := assertConfig(t, err, sessionmap.ReasonMapNotPrivate)
	if details["check"] != "foreign_owner" {
		t.Fatalf("check = %q", details["check"])
	}
}

func TestSessionIgnoresAHostileStateDirInsideASession(t *testing.T) {
	t.Parallel()
	// End to end: the state dir a session-bound command resolves comes from
	// XDG_STATE_HOME, not from an inherited BRIGADE_STATE_DIR, so a map
	// planted under the hostile directory is never read and the genuine
	// one is.
	d := newDirs(t)
	evilState := filepath.Join(t.TempDir(), "evil-state")
	planted := sampleMap(4242)
	planted.TeamKey = "planted"
	writeMap(t, evilState, planted)
	genuine := sampleMap(4242)
	writeMap(t, d.brigadeState(), genuine)

	env := d.environ("CLAUDE_PID=4242", "BRIGADE_STATE_DIR="+evilState)
	stateDir, err := config.BrigadeStateDir(env)
	if err != nil {
		t.Fatal(err)
	}
	if stateDir != d.brigadeState() {
		t.Fatalf("StateDir = %q, want %q", stateDir, d.brigadeState())
	}
	got, err := config.Session(env, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.TeamKey != "work" {
		t.Fatalf("the planted map was read: profile %q", got.TeamKey)
	}

	// Positive control: outside a session the same variable IS the state
	// dir, and the map under it is what a terminal command would see.
	outside := d.environ("BRIGADE_STATE_DIR=" + evilState)
	stateDir, err = config.BrigadeStateDir(outside)
	if err != nil || stateDir != evilState {
		t.Fatalf("StateDir(outside) = %q, %v", stateDir, err)
	}
	_, err = config.Session(outside, stateDir)
	assertConfig(t, err, config.ReasonNotInSession)
}
