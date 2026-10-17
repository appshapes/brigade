// Package config is the only harness package that reads configuration
// from the environment (plan 3.2, 6.5, 7.3; forbidigo enforces the static
// half). Inside a Claude Code session — CLAUDE_PID set — the sources are
// exactly: the CLAUDE_PLUGIN_OPTION_* values the hooks receive (Options),
// the by-pid map the hook wrote from them (Session), the watcher's own
// hook-built environment (FromWatcherEnv), CLAUDE_PID, CLAUDE_CONFIG_DIR,
// HOME and XDG_*. Every inherited BRIGADE_* variable is IGNORED there
// (U-27): a trusted repository's settings `env` block is written into the
// session's process environment, and honouring one would let a repository
// choose the principal, the team, the inbound policy or the state
// directory a session acts with. Outside a session (a human's terminal,
// CLAUDE_PID unset) the shell's BRIGADE_* are the user's own and are
// honoured exactly as adapterkit does.
//
// Adapter selection (D36) lives here too: ResolveAdapter applies the
// override → sidecar → profile member → bundled chain and yields the argv
// prefix the adapterclient prepends to every invocation.
//
// Everything takes an environ []string in os.Environ() form and touches
// no global state; the one process fact consulted, the current uid, is
// read by sessionmap's strict reader and injectable there.
package config

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// The two Claude Code variables this package reads directly, and the one
// Muse Code variable (card 88).
const (
	// ClaudePIDVar is set in hooks and in the Bash tool (6.5); its
	// presence is what "inside a Claude Code session" means.
	ClaudePIDVar = "CLAUDE_PID"
	// ClaudeConfigDirVar is inherited from the user's shell, never
	// injected; unset means the default under HOME.
	ClaudeConfigDirVar = "CLAUDE_CONFIG_DIR"
	// MuseSessionIDVar is set in Muse Code's hooks, in its shell tool and
	// in its monitors (Muse 1.4.4 on): the session's UUID. Its presence
	// is what "inside a Muse Code session" means (card 88). It is as
	// strong as CLAUDE_PID and no stronger: an environment variable the
	// model can unset, which docs/security.md accepts; the host's own
	// approval policy is the gate.
	MuseSessionIDVar = "MUSE_SESSION_ID"
)

// A Host is the coding harness a session runs in: the value the
// registration's `harness` member carries (protocol.SessionRegistration).
type Host string

// The two hosts Brigade knows. The empty Host means "not in a session".
const (
	HostClaude Host = "claude-code"
	HostMuse   Host = "muse"
)

// HostOf names the host environ belongs to: Claude Code when CLAUDE_PID is
// set (it wins, so a Muse variable leaking into a Claude session changes
// nothing), Muse Code when MUSE_SESSION_ID is set, else "" — a terminal.
func HostOf(environ []string) Host {
	switch {
	case adapterkit.Getenv(environ, ClaudePIDVar) != "":
		return HostClaude
	case adapterkit.Getenv(environ, MuseSessionIDVar) != "":
		return HostMuse
	}
	return ""
}

// claudeConfigDefaultDir is the directory under HOME that Claude Code uses
// when CLAUDE_CONFIG_DIR is unset (6.5). It is spelled here, once, as the
// default of ClaudeConfigDir — never assumed anywhere else.
const claudeConfigDefaultDir = ".claude"

// brigadePrefix selects the variables Strip removes.
const brigadePrefix = "BRIGADE_"

// The details.reason values of the `config` errors this package returns
// for the environment itself.
const (
	// ReasonNotInSession: CLAUDE_PID is unset, so a session-bound command
	// was run from a plain terminal.
	ReasonNotInSession = "not_in_session"
	// ReasonInvalidClaudePID: CLAUDE_PID is set but not a positive integer.
	ReasonInvalidClaudePID = "invalid_claude_pid"
	// ReasonNotRegistered: no by-pid map exists for CLAUDE_PID — the hook
	// did not run or failed, or the plugin was enabled mid-session.
	ReasonNotRegistered = "not_registered"
	// ReasonRelativePath: a directory value that must be absolute is not.
	ReasonRelativePath = "relative_path"
	// ReasonUnresolvable: HOME is unset or relative, so no default can be
	// computed.
	ReasonUnresolvable = "unresolvable"
	// ReasonInvalidNativeID: MUSE_SESSION_ID is set but is not a value that
	// can name a session (card 88).
	ReasonInvalidNativeID = "invalid_native_id"
)

// InSession reports whether environ belongs to a session of either host:
// CLAUDE_PID or MUSE_SESSION_ID is set and non-empty. The value is not
// parsed here (ClaudePID and MuseSessionID do that) because the strip rule
// must apply even to a malformed one.
func InSession(environ []string) bool {
	return HostOf(environ) != ""
}

// MuseSessionID reads MUSE_SESSION_ID as a native session id usable as a
// path component (sessionmap.CheckNativeID's rules; Muse's ids are UUIDs).
// Unset is `config` with details.reason not_in_session; a value that is not
// a safe id is `config` with invalid_native_id. The value is never echoed.
func MuseSessionID(environ []string) (string, error) {
	raw := adapterkit.Getenv(environ, MuseSessionIDVar)
	if raw == "" {
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "not inside a Muse Code session (MUSE_SESSION_ID is unset)",
			Details: map[string]string{"reason": ReasonNotInSession, "variable": MuseSessionIDVar},
		}
	}
	if err := sessionmap.CheckNativeID(raw); err != nil {
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: MuseSessionIDVar + " is not a usable session id",
			Details: map[string]string{"reason": ReasonInvalidNativeID, "variable": MuseSessionIDVar},
		}
	}
	return raw, nil
}

// Strip returns environ without every entry whose name begins with
// BRIGADE_. It is unconditional; Trusted applies the session rule.
func Strip(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		if strings.HasPrefix(entry, brigadePrefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Trusted returns the environment the resolvers may honour: environ with
// every BRIGADE_* removed inside a session, environ itself outside one
// (3.2). Every resolver in this package reads through it. Inside a Muse
// Code session the XDG_*_HOME variables go too (card 88): Muse runs its
// hooks from a cleared environment that has HOME and no XDG_*, so the hook
// resolves Brigade's directories from HOME alone, while the shell tool
// inherits the user's whole environment — with XDG_STATE_HOME set, a
// command there would look for the session's map where the hook never
// wrote it. Both sides therefore resolve from HOME in a Muse session; a
// member who keeps Brigade's config elsewhere names it with the
// `config_dir` option of muse-options.json.
func Trusted(environ []string) []string {
	switch HostOf(environ) {
	case HostMuse:
		return stripXDG(Strip(environ))
	case HostClaude:
		return Strip(environ)
	}
	return environ
}

// xdgPrefix selects the variables stripXDG removes.
const xdgPrefix = "XDG_"

// stripXDG returns environ without every XDG_* entry.
func stripXDG(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		if strings.HasPrefix(entry, xdgPrefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// BrigadeConfigDir resolves the Brigade config directory through Trusted:
// inside a session the XDG default (BRIGADE_CONFIG_DIR is ignored; the
// config_dir OPTION is the only way to relocate it, and Options applies
// it), outside a session exactly adapterkit.ConfigDir.
func BrigadeConfigDir(environ []string) (string, error) {
	return adapterkit.ConfigDir(Trusted(environ))
}

// BrigadeStateDir resolves the Brigade state directory through Trusted: inside a
// session it is ALWAYS the XDG default — a repository's env block must not
// be able to move the state directory, which is where the by-pid map that
// decides a session's identity lives (E0-7).
func BrigadeStateDir(environ []string) (string, error) {
	return adapterkit.StateDir(Trusted(environ))
}

// ClaudePID reads CLAUDE_PID as a positive integer. Unset is `config`
// with details.reason not_in_session; a non-integer or non-positive value
// is `config` with invalid_claude_pid. The value is never echoed.
func ClaudePID(environ []string) (int, error) {
	raw := adapterkit.Getenv(environ, ClaudePIDVar)
	if raw == "" {
		return 0, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "not inside a Claude Code session (CLAUDE_PID is unset); this command runs from a session's Bash tool; the terminal commands resolve their team from the checkout's pin (or --team)",
			Details: map[string]string{"reason": ReasonNotInSession, "variable": ClaudePIDVar},
		}
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return 0, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "CLAUDE_PID is not a positive integer",
			Details: map[string]string{"reason": ReasonInvalidClaudePID, "variable": ClaudePIDVar},
		}
	}
	return pid, nil
}

// ClaudeConfigDir resolves Claude Code's own config directory, the parent
// of the session registry (6.5): CLAUDE_CONFIG_DIR when set (it must be
// absolute; a relative value is `config`), else HOME/.claude. It is read
// from the environment in every context — hooks and the Bash tool inherit
// it from the user's shell — and is not a BRIGADE_* variable, so the
// session strip rule does not touch it.
func ClaudeConfigDir(environ []string) (string, error) {
	if v := adapterkit.Getenv(environ, ClaudeConfigDirVar); v != "" {
		if !filepath.IsAbs(v) {
			return "", &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: ClaudeConfigDirVar + " must be an absolute path",
				Details: map[string]string{"reason": ReasonRelativePath, "variable": ClaudeConfigDirVar},
			}
		}
		return filepath.Clean(v), nil
	}
	home := adapterkit.Getenv(environ, "HOME")
	if home == "" || !filepath.IsAbs(home) {
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "HOME is unset or not an absolute path; cannot resolve the Claude Code config directory",
			Details: map[string]string{"reason": ReasonUnresolvable, "variable": "HOME"},
		}
	}
	return filepath.Join(home, claudeConfigDefaultDir), nil
}
