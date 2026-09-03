package config

import (
	"errors"
	"io/fs"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// Session resolves the session-bound configuration (6.4): CLAUDE_PID from
// environ, then the by-pid map under stateDir through sessionmap's strict
// reader. Everything a command needs — profile, config dir, adapter argv,
// Brigade session id, team, socket path — comes out of the map; no option
// and no BRIGADE_* is consulted, which is why stateDir is the caller's
// (config.BrigadeStateDir, which ignores BRIGADE_STATE_DIR inside a session).
//
// Failures are `config` (exit 11): details.reason not_in_session when
// CLAUDE_PID is unset, not_registered when the map is missing (the hook
// did not run or failed, or the plugin was enabled mid-session — the
// message suggests /reload-plugins or a restart), map_not_private when
// the file is not a regular, non-symlinked, 0600 file owned by the
// current uid (E0-7: the map is an unauthenticated trust boundary and
// this is the only check available), map_malformed or map_invalid when
// its content is not a usable map.
func Session(environ []string, stateDir string) (*sessionmap.ByPID, error) {
	return SessionIn(environ, sessionmap.Store{StateDir: stateDir})
}

// SessionIn is Session with the map store given explicitly, so a test can
// inject the store's owner check (sessionmap.Store.Stat) and exercise the
// foreign-uid refusal that no test can create for real.
func SessionIn(environ []string, store sessionmap.Store) (*sessionmap.ByPID, error) {
	pid, err := ClaudePID(environ)
	if err != nil {
		return nil, err
	}
	m, err := store.ReadByPID(pid)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "this session is not registered with Brigade (no session map for this Claude Code process): the SessionStart hook did not run or failed, or the plugin was enabled mid-session; run /reload-plugins or restart the session",
			Details: map[string]string{"reason": ReasonNotRegistered},
		}
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}
