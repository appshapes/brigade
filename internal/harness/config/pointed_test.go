package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

const (
	museID  = "01a14785-947f-7d93-a4f4-6ee1f6b2be94"
	codexID = "019a2b3c-4d5e-7f80-9a1b-2c3d4e5f6071"
)

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
		{"codex", []string{"CODEX_THREAD_ID=" + codexID}, config.HostCodex},
		{"claude wins over a leaked muse id", []string{"MUSE_SESSION_ID=" + museID, "CLAUDE_PID=4242"}, config.HostClaude},
		{"claude wins over a leaked codex id", []string{"CODEX_THREAD_ID=" + codexID, "CLAUDE_PID=4242"}, config.HostClaude},
		{"muse wins over a leaked codex id", []string{"CODEX_THREAD_ID=" + codexID, "MUSE_SESSION_ID=" + museID}, config.HostMuse},
		{"an empty muse id is a terminal", []string{"MUSE_SESSION_ID="}, ""},
		{"an empty codex id is a terminal", []string{"CODEX_THREAD_ID="}, ""},
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

// TestPointedHostsIgnoreXDG pins the HOME-only resolution of a Muse session
// (card 88) and a Codex session (card 107): the hook never sees XDG_* (Muse)
// or sees the TUI's while the shell sees the daemon's (Codex), so the shell
// tool must not honour them either, or the two resolve different
// directories.
func TestPointedHostsIgnoreXDG(t *testing.T) {
	t.Parallel()
	for name, marker := range map[string]string{"muse": "MUSE_SESSION_ID=" + museID, "codex": "CODEX_THREAD_ID=" + codexID} {
		env := []string{"HOME=/h", "XDG_STATE_HOME=/" + evilMarker, "XDG_CONFIG_HOME=/" + evilMarker, marker}
		if got, err := config.BrigadeStateDir(env); err != nil || got != "/h/.local/state/brigade" {
			t.Fatalf("%s: BrigadeStateDir = %q, %v", name, got, err)
		}
		if got, err := config.BrigadeConfigDir(env); err != nil || got != "/h/.config/brigade" {
			t.Fatalf("%s: BrigadeConfigDir = %q, %v", name, got, err)
		}
	}
	// A Claude session keeps honouring XDG_*, as every proof relies on.
	claude := []string{"HOME=/h", "XDG_STATE_HOME=/x", "CLAUDE_PID=4242"}
	if got, err := config.BrigadeStateDir(claude); err != nil || got != "/x/brigade" {
		t.Fatalf("Claude BrigadeStateDir = %q, %v", got, err)
	}
}

func TestNativeIDs(t *testing.T) {
	t.Parallel()
	if id, err := config.MuseSessionID([]string{"MUSE_SESSION_ID=" + museID}); err != nil || id != museID {
		t.Fatalf("MuseSessionID = %q, %v", id, err)
	}
	if id, err := config.CodexThreadID([]string{"CODEX_THREAD_ID=" + codexID}); err != nil || id != codexID {
		t.Fatalf("CodexThreadID = %q, %v", id, err)
	}
	// Each reader reads its own variable and no other.
	if _, err := config.CodexThreadID([]string{"MUSE_SESSION_ID=" + museID}); err == nil {
		t.Fatal("CodexThreadID read MUSE_SESSION_ID")
	}
	if _, err := config.NativeID(config.HostClaude, []string{"CLAUDE_PID=4242"}); err == nil {
		t.Fatal("NativeID answered for Claude Code, which has no native id variable")
	}
	for name, env := range map[string][]string{
		"unset":           {"HOME=/h"},
		"traversal":       {"MUSE_SESSION_ID=../../" + evilMarker},
		"too long":        {"MUSE_SESSION_ID=" + strings.Repeat("a", 81)},
		"codex traversal": {"CODEX_THREAD_ID=../../" + evilMarker},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var err error
			if strings.HasPrefix(name, "codex") {
				_, err = config.CodexThreadID(env)
			} else {
				_, err = config.MuseSessionID(env)
			}
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

// TestOptionsFileNames pins which host reads which file (cards 88, 107) and
// that Claude Code and a terminal read none.
func TestOptionsFileNames(t *testing.T) {
	t.Parallel()
	if got := config.OptionsFile(config.HostMuse); got != "muse-options.json" {
		t.Fatalf("muse file %q", got)
	}
	if got := config.OptionsFile(config.HostCodex); got != "codex-options.json" {
		t.Fatalf("codex file %q", got)
	}
	if got := config.OptionsFile(config.HostClaude); got != "" {
		t.Fatalf("claude file %q, want none", got)
	}
	if p, err := config.OptionsPath([]string{"HOME=/h", "CLAUDE_PID=4242"}); err != nil || p != "" {
		t.Fatalf("claude path %q %v, want none", p, err)
	}
	if p, err := config.OptionsPath([]string{"HOME=/h", "CODEX_THREAD_ID=" + codexID, "XDG_CONFIG_HOME=/" + evilMarker}); err != nil || p != "/h/.config/brigade/codex-options.json" {
		t.Fatalf("codex path %q %v (XDG_CONFIG_HOME must be ignored in a Codex session)", p, err)
	}
	if got := config.WarnOptionsUnusable("/h/.config/brigade/codex-options.json"); got != "Brigade: codex-options.json is not a JSON object of option names to values; the defaults apply for this session." {
		t.Fatalf("warning %q", got)
	}
}

// TestOptionsEnviron reads the options file a Muse session gets its
// options from (card 88, D4); a Codex session reads its own the same way.
func TestOptionsEnviron(t *testing.T) {
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
		env, warn := config.OptionsEnviron(base, filepath.Join(dir, "absent.json"), os.ReadFile)
		if warn != "" || !slices.Equal(env, base) {
			t.Fatalf("env %v warn %q", env, warn)
		}
	})
	t.Run("known keys become option variables, unknown keys and odd values are ignored", func(t *testing.T) {
		t.Parallel()
		p := write("ok.json", `{"team_inbound": " hold ", "poll_on_prompt": true, "message_interval": 45, "frame": "guarded",
			"label": "none", "not_an_option": "x", "sync": {"nested": 1}, "workspace_label": null}`)
		env, warn := config.OptionsEnviron(base, p, os.ReadFile)
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
		"over the cap": strings.Repeat(" ", config.MaxOptionsFileBytes+1),
	} {
		t.Run("an unusable file is the defaults with the warning: "+name, func(t *testing.T) {
			t.Parallel()
			p := write(strings.ReplaceAll(name, " ", "-")+".json", body)
			env, warn := config.OptionsEnviron(base, p, os.ReadFile)
			if warn != config.WarnOptionsUnusable(p) || !slices.Equal(env, base) {
				t.Fatalf("env %v warn %q", env, warn)
			}
		})
	}
	t.Run("the path is under the config directory the environment resolves", func(t *testing.T) {
		t.Parallel()
		p, err := config.OptionsPath([]string{"HOME=/h", "MUSE_SESSION_ID=" + museID, "BRIGADE_CONFIG_DIR=/" + evilMarker})
		if err != nil || p != "/h/.config/brigade/"+config.MuseOptionsFile {
			t.Fatalf("path %q %v (BRIGADE_CONFIG_DIR must be ignored in a session)", p, err)
		}
	})
}

// codexMap is sampleMap as the Codex CLI hook writes it (card 107): the
// harness member, the model, no socket, keyed by the TUI's pid.
func codexMap() sessionmap.ByPID {
	m := sampleMap(777)
	m.Harness = string(config.HostCodex)
	m.Model = "gpt-5.6-sol"
	m.ClaudeSessionID = codexID
	m.SocketPath = ""
	return m
}

// TestSessionCodex resolves a Codex CLI session through the by-host pointer
// (card 107), as TestSessionMuse does for Muse: the model's shell knows
// CODEX_THREAD_ID and not the pid.
func TestSessionCodex(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=/h", "CODEX_THREAD_ID=" + codexID, "BRIGADE_STATE_DIR=/" + evilMarker, "XDG_STATE_HOME=/" + evilMarker}

	t.Run("positive control", func(t *testing.T) {
		t.Parallel()
		stateDir := filepath.Join(t.TempDir(), "state")
		want := codexMap()
		writeMap(t, stateDir, want)
		s := sessionmap.Store{StateDir: stateDir}
		if err := s.WriteByHost("codex", codexID, &sessionmap.ByHost{HostPID: 777, BrigadeSessionID: want.BrigadeSessionID}); err != nil {
			t.Fatal(err)
		}
		got, err := config.Session(env, stateDir)
		if err != nil {
			t.Fatalf("Session: %v", err)
		}
		if got.ClaudePID != 777 || got.Harness != "codex" || got.Model != want.Model || got.BrigadeSessionID != want.BrigadeSessionID || got.ClaudeSessionID != codexID {
			t.Fatalf("Session = %+v", *got)
		}
	})
	t.Run("a muse pointer does not answer for a codex id", func(t *testing.T) {
		t.Parallel()
		stateDir := filepath.Join(t.TempDir(), "state")
		writeMap(t, stateDir, codexMap())
		s := sessionmap.Store{StateDir: stateDir}
		if err := s.WriteByHost("muse", codexID, &sessionmap.ByHost{HostPID: 777, BrigadeSessionID: codexMap().BrigadeSessionID}); err != nil {
			t.Fatal(err)
		}
		_, err := config.Session(env, stateDir)
		assertReason(t, err, config.ReasonNotRegistered)
	})
	t.Run("a pointer whose map says muse is refused", func(t *testing.T) {
		t.Parallel()
		stateDir := filepath.Join(t.TempDir(), "state")
		m := codexMap()
		m.Harness = string(config.HostMuse)
		writeMap(t, stateDir, m)
		s := sessionmap.Store{StateDir: stateDir}
		if err := s.WriteByHost("codex", codexID, &sessionmap.ByHost{HostPID: 777, BrigadeSessionID: codexMap().BrigadeSessionID}); err != nil {
			t.Fatal(err)
		}
		_, err := config.Session(env, stateDir)
		assertReason(t, err, sessionmap.ReasonMapMismatch)
	})
	t.Run("the message names the host", func(t *testing.T) {
		t.Parallel()
		stateDir := filepath.Join(t.TempDir(), "state")
		_, err := config.Session(env, stateDir)
		var perr *protocol.Error
		if !errors.As(err, &perr) || !strings.Contains(perr.Message, "Codex CLI session") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestStateKey pins card 116's key resolution for a command in the model's
// shell: CLAUDE_PID on Claude Code; on a pointed host the attached
// pointer's key, else the start pointer's (a session not yet joined), else
// not_registered; a terminal is not_in_session.
func TestStateKey(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "state")
	s := sessionmap.Store{StateDir: stateDir}
	if key, err := config.StateKey([]string{"HOME=/h", "CLAUDE_PID=4242"}, s); err != nil || key != 4242 {
		t.Fatalf("claude: %d %v", key, err)
	}
	_, err := config.StateKey([]string{"HOME=/h"}, s)
	assertReason(t, err, config.ReasonNotInSession)
	codex := []string{"HOME=/h", "CODEX_THREAD_ID=" + codexID}
	_, err = config.StateKey(codex, s)
	assertReason(t, err, config.ReasonNotRegistered)
	if err := s.WriteStartPointer("codex", codexID, 1<<30|9, time.Now()); err != nil {
		t.Fatal(err)
	}
	if key, err := config.StateKey(codex, s); err != nil || key != 1<<30|9 {
		t.Fatalf("start pointer: %d %v", key, err)
	}
	// Once attached, the by-host pointer wins (it names the same key in
	// practice; here a different one shows which was read).
	if err := s.WriteByHost("codex", codexID, &sessionmap.ByHost{HostPID: 1<<30 | 10, BrigadeSessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	if key, err := config.StateKey(codex, s); err != nil || key != 1<<30|10 {
		t.Fatalf("attached pointer: %d %v", key, err)
	}
	// A Muse session reads its own pointers, never Codex's.
	muse := []string{"HOME=/h", "MUSE_SESSION_ID=" + museID}
	_, err = config.StateKey(muse, s)
	assertReason(t, err, config.ReasonNotRegistered)
	if err := s.WriteStartPointer("muse", museID, 777, time.Now()); err != nil {
		t.Fatal(err)
	}
	if key, err := config.StateKey(muse, s); err != nil || key != 777 {
		t.Fatalf("muse start pointer: %d %v", key, err)
	}
}
