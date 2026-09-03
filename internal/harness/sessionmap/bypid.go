// Package sessionmap is the hook-written state that binds a Claude Code
// process to its Brigade session (plan 3.2, 6.5): the by-pid map at
// ${stateDir}/sessions/by-pid/<claude_pid>.json, from which the
// session-bound commands and the watcher resolve EVERYTHING (profile,
// config dir, adapter argv, Brigade session id, team, socket path), and the
// by-native map at ${stateDir}/sessions/by-native/<claude_session_id>.json,
// which lets a later `claude --resume` re-open the same Brigade session
// (3.7). Neither map ever holds the messaging token (3.2).
//
// Trust: the by-pid map is an unauthenticated trust boundary guarded by
// filesystem permissions alone (E0-7) — whoever can write it decides which
// profile and team a session acts as. The reader therefore applies every
// check available and refuses anything else with `config`,
// details.reason "map_not_private": a regular file opened without following
// a symlink, mode granting nothing to group or other, owned by the current
// uid, within the size cap, valid JSON of the expected shape whose
// claude_pid matches its file name. A hook that rewrites the map on every
// SessionStart (E0-8: it re-fires on /clear) is what keeps a planted map
// from surviving to the first Bash call; this package supplies the
// overwrite semantics that idempotence needs.
//
// Every side effect here is a file under the caller's Store.StateDir; the
// clock is the caller's (RegisteredAt/UpdatedAt are set by the hook) and
// the owner check is injectable through Store.Stat, so tests stay hermetic.
package sessionmap

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The details.reason values of the `config` errors this package returns.
const (
	// ReasonMapNotPrivate: the file failed the privacy checks (mode, owner,
	// symlink, regular file). details.check names which.
	ReasonMapNotPrivate = "map_not_private"
	// ReasonMapMalformed: the file is not a JSON object of the expected
	// shape, or exceeds MaxMapBytes.
	ReasonMapMalformed = "map_malformed"
	// ReasonMapInvalid: the JSON parsed but a member fails Validate;
	// details.field names it.
	ReasonMapInvalid = "map_invalid"
	// ReasonMapMismatch: the by-pid file's claude_pid is not the pid in its
	// file name (a copied or planted file).
	ReasonMapMismatch = "map_mismatch"
	// ReasonMapUnreadable: an I/O failure other than "missing" — the
	// underlying error is never echoed.
	ReasonMapUnreadable = "map_unreadable"
	// ReasonStateDirRelative: the Store's StateDir is empty or relative;
	// the harness always computes an absolute one (3.2).
	ReasonStateDirRelative = "state_dir_relative"
)

// ByPID is the by-pid map (plan 3.2): everything a session-bound command
// or the watcher needs, resolved by the hook once at SessionStart. It
// carries RESOLVED values only — the profile after its default applied,
// the config dir as an absolute path, the adapter as the argv prefix
// ResolveAdapter produced — never a raw option value and never the token.
type ByPID struct {
	// ClaudePID is the Claude Code process id; it is also the file name.
	ClaudePID int `json:"claude_pid"`
	// ClaudeSessionID is the native session id from the hook's stdin; it
	// is never sent to any backend (T10).
	ClaudeSessionID string `json:"claude_session_id"`
	// BrigadeSessionID is the adapter-assigned session id (4.4.3).
	BrigadeSessionID string `json:"brigade_session_id"`
	// TeamRef and TeamName are the profile's team binding at registration.
	TeamRef  string `json:"team_ref"`
	TeamName string `json:"team_name"`
	// SessionName is the display name registered (6.5).
	SessionName string `json:"session_name"`
	// PermissionMode is recorded for diagnostics only; the inbound policy
	// never depends on it (D18).
	PermissionMode string `json:"permission_mode"`
	// NonInteractive is true for a `claude -p` session (CLAUDE_CODE_ENTRYPOINT
	// sdk-cli, 6.5); diagnostics only.
	NonInteractive bool `json:"non_interactive"`
	// Inbound is the EFFECTIVE policy the hook chose: accept or refuse (6.8).
	Inbound string `json:"inbound"`
	// SocketPath is CLAUDE_CODE_MESSAGING_SOCKET as the hook saw it, or ""
	// on a host without an inbox socket. The token is NOT here.
	SocketPath string `json:"socket_path"`
	// Profile is the resolved profile name.
	Profile string `json:"profile"`
	// ConfigDir is the resolved absolute Brigade config directory.
	ConfigDir string `json:"config_dir"`
	// AdapterCommand is the resolved argv prefix prepended verbatim to
	// every adapter invocation; [] means the bundled adapter, which the
	// adapterclient resolves through os.Executable() at spawn time and
	// which is deliberately never pinned to a path here.
	AdapterCommand []string `json:"adapter_command"`
	// PluginBin is the bootstrap's resolved realpath when the hook knows
	// it (the shadowing check of 6.2), else "".
	PluginBin string `json:"plugin_bin"`
	// HarnessVersion is the Claude Code version from the registry's
	// `version` member when present, else "unknown".
	HarnessVersion string `json:"harness_version"`
	// RegisteredAt is when the Brigade session was registered; UpdatedAt
	// when this file was last rewritten. Both are the hook's clock.
	RegisteredAt time.Time `json:"registered_at,omitzero"`
	UpdatedAt    time.Time `json:"updated_at,omitzero"`
}

// Validate checks the members every by-pid map must carry before it is
// written or trusted: a positive pid, a Brigade session id, a valid
// profile name, an absolute config dir, an inbound value this harness
// implements (accept or refuse; hold widens this with P5-9), a
// well-formed adapter argv and an absolute or empty socket path. The
// failure is `config` with details.field naming the member; the value is
// never echoed.
func (m *ByPID) Validate() error {
	switch {
	case m.ClaudePID <= 0:
		return errInvalid("claude_pid")
	case m.BrigadeSessionID == "":
		return errInvalid("brigade_session_id")
	case adapterkit.CheckProfileName(m.Profile) != nil:
		return errInvalid("profile")
	case !filepath.IsAbs(m.ConfigDir):
		return errInvalid("config_dir")
	case m.Inbound != protocol.InboundAccept && m.Inbound != protocol.InboundRefuse:
		return errInvalid("inbound")
	case CheckAdapterCommand(m.AdapterCommand) != nil:
		return errInvalid("adapter_command")
	case m.SocketPath != "" && !filepath.IsAbs(m.SocketPath):
		return errInvalid("socket_path")
	}
	return nil
}

// CheckAdapterCommand validates a resolved adapter argv prefix: every
// element non-empty and, when there is one, the first an absolute path.
// An empty argv is valid and means the bundled adapter. A relative
// executable is refused because a planted map must not be able to make
// the harness exec a path relative to whatever the cwd happens to be.
func CheckAdapterCommand(argv []string) error {
	for _, a := range argv {
		if a == "" {
			return errors.New("adapter command has an empty element")
		}
	}
	if len(argv) > 0 && !filepath.IsAbs(argv[0]) {
		return errors.New("adapter command must start with an absolute path")
	}
	return nil
}

// errInvalid is the `config` failure for a map member that fails Validate.
func errInvalid(field string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "the session map is not usable (member " + field + " is missing or invalid); restart the session or run /reload-plugins so the hook rewrites it",
		Details: map[string]string{"reason": ReasonMapInvalid, "field": field},
	}
}
