package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

const museID = "01a14785-947f-7d93-a4f4-6ee1f6b2be94"

// museMap is sampleMap as the Muse Code hook writes it (card 88): the
// harness member, the model, no socket, keyed by the Muse process's pid.
// Its pid is 777, the pointer's host_pid in every case below.
func museMap() sessionmap.ByPID {
	m := sampleMap(777)
	m.Harness = string(config.HostMuse)
	m.Model = "muse-wonder-full-1.0"
	m.ClaudeSessionID = museID
	m.SocketPath = ""
	return m
}

func TestHostOfAndInSession(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  []string
		host config.Host
	}{
		{"terminal", []string{"HOME=/h"}, ""},
		{"claude", []string{"CLAUDE_PID=4242"}, config.HostClaude},
		{"muse", []string{"MUSE_SESSION_ID=" + museID}, config.HostMuse},
		{"claude wins over a leaked muse id", []string{"MUSE_SESSION_ID=" + museID, "CLAUDE_PID=4242"}, config.HostClaude},
		{"an empty muse id is a terminal", []string{"MUSE_SESSION_ID="}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := config.HostOf(tc.env); got != tc.host {
				t.Fatalf("HostOf = %q, want %q", got, tc.host)
			}
			if got := config.InSession(tc.env); got != (tc.host != "") {
				t.Fatalf("InSession = %v, want %v", got, tc.host != "")
			}
			// The strip rule applies in a Muse session exactly as in a
			// Claude one: a BRIGADE_* in a session's environment is never
			// honoured (U-27).
			trusted := config.Trusted(append([]string{"BRIGADE_PROFILE=" + evilMarker}, tc.env...))
			if (tc.host != "") == slices.Contains(trusted, "BRIGADE_PROFILE="+evilMarker) {
				t.Fatalf("Trusted kept BRIGADE_PROFILE in a session (or dropped it in a terminal): %v", trusted)
			}
		})
	}
}

// TestMuseIgnoresXDG pins the HOME-only resolution of a Muse session (card
// 88): the hook never sees XDG_*, so the shell tool must not honour them
// either, or the two resolve different directories.
func TestMuseIgnoresXDG(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=/h", "XDG_STATE_HOME=/" + evilMarker, "XDG_CONFIG_HOME=/" + evilMarker, "MUSE_SESSION_ID=" + museID}
	if got, err := config.BrigadeStateDir(env); err != nil || got != "/h/.local/state/brigade" {
		t.Fatalf("BrigadeStateDir = %q, %v", got, err)
	}
	if got, err := config.BrigadeConfigDir(env); err != nil || got != "/h/.config/brigade" {
		t.Fatalf("BrigadeConfigDir = %q, %v", got, err)
	}
	// A Claude session keeps honouring XDG_*, as every proof relies on.
	claude := []string{"HOME=/h", "XDG_STATE_HOME=/x", "CLAUDE_PID=4242"}
	if got, err := config.BrigadeStateDir(claude); err != nil || got != "/x/brigade" {
		t.Fatalf("Claude BrigadeStateDir = %q, %v", got, err)
	}
}

func TestMuseSessionID(t *testing.T) {
	t.Parallel()
	if id, err := config.MuseSessionID([]string{"MUSE_SESSION_ID=" + museID}); err != nil || id != museID {
		t.Fatalf("MuseSessionID = %q, %v", id, err)
	}
	for name, env := range map[string][]string{
		"unset":     {"HOME=/h"},
		"traversal": {"MUSE_SESSION_ID=../../" + evilMarker},
		"too long":  {"MUSE_SESSION_ID=" + strings.Repeat("a", 81)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := config.MuseSessionID(env)
			var perr *protocol.Error
			if !errors.As(err, &perr) || perr.Code != protocol.CodeConfig {
				t.Fatalf("err = %v, want a config error", err)
			}
			if strings.Contains(perr.Message, evilMarker) {
				t.Fatalf("the value was echoed: %q", perr.Message)
			}
			want := config.ReasonInvalidNativeID
			if name == "unset" {
				want = config.ReasonNotInSession
			}
			if perr.Details["reason"] != want {
				t.Fatalf("reason %q, want %q", perr.Details["reason"], want)
			}
		})
	}
}

// TestSessionMuse resolves a Muse Code session through the by-host pointer
// (card 88): the model's shell knows MUSE_SESSION_ID and not the pid, so
// the pointer names the pid and the map must agree with it.
func TestSessionMuse(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=/h", "MUSE_SESSION_ID=" + museID, "BRIGADE_STATE_DIR=/" + evilMarker}

	t.Run("positive control", func(t *testing.T) {
		t.Parallel()
		stateDir := filepath.Join(t.TempDir(), "state")
		want := museMap()
		writeMap(t, stateDir, want)
		s := sessionmap.Store{StateDir: stateDir}
		if err := s.WriteByHost("muse", museID, &sessionmap.ByHost{HostPID: 777, BrigadeSessionID: want.BrigadeSessionID}); err != nil {
			t.Fatal(err)
		}
		got, err := config.Session(env, stateDir)
		if err != nil {
			t.Fatalf("Session: %v", err)
		}
		if got.ClaudePID != 777 || got.Harness != "muse" || got.Model != want.Model || got.BrigadeSessionID != want.BrigadeSessionID || got.ClaudeSessionID != museID {
			t.Fatalf("Session = %+v", *got)
		}
	})
	t.Run("no pointer is not_registered", func(t *testing.T) {
		t.Parallel()
		stateDir := filepath.Join(t.TempDir(), "state")
		writeMap(t, stateDir, museMap())
		_, err := config.Session(env, stateDir)
		assertReason(t, err, config.ReasonNotRegistered)
	})
	t.Run("a pointer whose map is gone is not_registered", func(t *testing.T) {
		t.Parallel()
		stateDir := filepath.Join(t.TempDir(), "state")
		s := sessionmap.Store{StateDir: stateDir}
		if err := s.WriteByHost("muse", museID, &sessionmap.ByHost{HostPID: 777, BrigadeSessionID: "6f0f"}); err != nil {
			t.Fatal(err)
		}
		_, err := config.Session(env, stateDir)
		assertReason(t, err, config.ReasonNotRegistered)
	})
	for name, mutate := range map[string]func(*sessionmap.ByPID){
		"another native id":       func(m *sessionmap.ByPID) { m.ClaudeSessionID = "other-id" },
		"a claude map":            func(m *sessionmap.ByPID) { m.Harness = "" },
		"another brigade session": func(m *sessionmap.ByPID) { m.BrigadeSessionID = "someone-else" },
	} {
		t.Run("a pointer disagreeing with its map is refused: "+name, func(t *testing.T) {
			t.Parallel()
			stateDir := filepath.Join(t.TempDir(), "state")
			m := museMap()
			mutate(&m)
			writeMap(t, stateDir, m)
			s := sessionmap.Store{StateDir: stateDir}
			if err := s.WriteByHost("muse", museID, &sessionmap.ByHost{HostPID: 777, BrigadeSessionID: museMap().BrigadeSessionID}); err != nil {
				t.Fatal(err)
			}
			_, err := config.Session(env, stateDir)
			assertReason(t, err, sessionmap.ReasonMapMismatch)
		})
	}
}

func assertReason(t *testing.T, err error, reason string) {
	t.Helper()
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeConfig {
		t.Fatalf("err = %v, want a config error", err)
	}
	if perr.Details["reason"] != reason {
		t.Fatalf("reason %q, want %q (%s)", perr.Details["reason"], reason, perr.Message)
	}
}

// TestMuseOptionsEnviron reads the options file a Muse session gets its
// options from (card 88, D4).
func TestMuseOptionsEnviron(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := []string{"HOME=/h", "MUSE_SESSION_ID=" + museID}

	t.Run("a missing file is the defaults with no warning", func(t *testing.T) {
		t.Parallel()
		env, warn := config.MuseOptionsEnviron(base, filepath.Join(dir, "absent.json"), os.ReadFile)
		if warn != "" || !slices.Equal(env, base) {
			t.Fatalf("env %v warn %q", env, warn)
		}
	})
	t.Run("known keys become option variables, unknown keys and odd values are ignored", func(t *testing.T) {
		t.Parallel()
		p := write("ok.json", `{"team_inbound": " hold ", "poll_on_prompt": true, "message_interval": 45, "frame": "guarded",
			"label": "none", "not_an_option": "x", "sync": {"nested": 1}, "workspace_label": null}`)
		env, warn := config.MuseOptionsEnviron(base, p, os.ReadFile)
		if warn != "" {
			t.Fatalf("warning %q", warn)
		}
		opts, err := config.ParseOptions(env)
		if err != nil {
			t.Fatalf("ParseOptions: %v", err)
		}
		if opts.TeamInbound != config.InboundHold || !opts.PollOnPrompt || opts.MessageInterval.Seconds() != 45 || string(opts.Frame) != "guarded" || opts.Label != config.LabelNone {
			t.Fatalf("options %+v", opts)
		}
		if !opts.Sync || opts.SyncWarning != "" {
			t.Fatalf("a nested value should have been skipped, leaving sync at its default: %+v", opts)
		}
		for _, e := range env {
			if strings.Contains(e, "NOT_AN_OPTION") {
				t.Fatalf("an unknown key reached the environment: %s", e)
			}
		}
		if got := adapterkit.Getenv(env, config.OptionTeamInbound); got != "hold" {
			t.Fatalf("team_inbound rendered as %q, want trimmed", got)
		}
	})
	for name, body := range map[string]string{
		"not json":     "{",
		"an array":     `["team_inbound"]`,
		"a string":     `"hold"`,
		"null":         "null",
		"over the cap": strings.Repeat(" ", config.MaxMuseOptionsBytes+1),
	} {
		t.Run("an unusable file is the defaults with the warning: "+name, func(t *testing.T) {
			t.Parallel()
			p := write(strings.ReplaceAll(name, " ", "-")+".json", body)
			env, warn := config.MuseOptionsEnviron(base, p, os.ReadFile)
			if warn != config.WarnMuseOptionsUnusable || !slices.Equal(env, base) {
				t.Fatalf("env %v warn %q", env, warn)
			}
		})
	}
	t.Run("the path is under the config directory the environment resolves", func(t *testing.T) {
		t.Parallel()
		p, err := config.MuseOptionsPath([]string{"HOME=/h", "MUSE_SESSION_ID=" + museID, "BRIGADE_CONFIG_DIR=/" + evilMarker})
		if err != nil || p != "/h/.config/brigade/"+config.MuseOptionsFile {
			t.Fatalf("path %q %v (BRIGADE_CONFIG_DIR must be ignored in a session)", p, err)
		}
	})
}
