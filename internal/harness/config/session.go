package config

import (
	"errors"
	"io/fs"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// Session resolves the session-bound configuration (6.4) for either host.
// In a Claude Code session: CLAUDE_PID from environ, then the by-pid map
// under stateDir through sessionmap's strict reader. In a Muse Code session
// (card 88): MUSE_SESSION_ID from environ, then the by-host pointer the
// SessionStart hook wrote for that id (sessions/by-host/muse/<uuid>.json),
// which names the host pid the map is keyed by, then the by-pid map — which
// must carry that native id and the muse harness, or the pointer is not
// trusted. Everything a command needs — profile, config dir, adapter argv,
// Brigade session id, team, socket or sink — comes out of the map; no option
// and no BRIGADE_* is consulted, which is why stateDir is the caller's
// (config.BrigadeStateDir, which ignores BRIGADE_STATE_DIR inside a session).
//
// Failures are `config` (exit 11): details.reason not_in_session when
// neither variable is set, not_registered when the map (or the pointer) is
// missing (the hook did not run or failed, or the plugin was enabled
// mid-session — the message suggests /reload-plugins or a restart),
// map_not_private when the file is not a regular, non-symlinked, 0600 file
// owned by the current uid (E0-7: the map is an unauthenticated trust
// boundary and this is the only check available), map_malformed or
// map_invalid when its content is not a usable map, map_mismatch when a
// Muse pointer and its map disagree.
func Session(environ []string, stateDir string) (*sessionmap.ByPID, error) {
	return SessionIn(environ, sessionmap.Store{StateDir: stateDir})
}

// SessionIn is Session with the map store given explicitly, so a test can
// inject the store's owner check (sessionmap.Store.Stat) and exercise the
// foreign-uid refusal that no test can create for real.
func SessionIn(environ []string, store sessionmap.Store) (*sessionmap.ByPID, error) {
	switch HostOf(environ) {
	case HostMuse:
		return museSession(environ, store)
	case HostClaude, "":
		// Claude Code, or a terminal: ClaudePID's own not_in_session error
		// is the answer for the latter.
	}
	pid, err := ClaudePID(environ)
	if err != nil {
		return nil, err
	}
	m, err := store.ReadByPID(pid)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notRegistered("this session is not registered with Brigade (no session map for this Claude Code process): the SessionStart hook did not run or failed, or the plugin was enabled mid-session; run /reload-plugins or restart the session")
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// museSession is the Muse half of SessionIn: pointer, then map, then the
// cross-check that binds them (card 88).
func museSession(environ []string, store sessionmap.Store) (*sessionmap.ByPID, error) {
	id, err := MuseSessionID(environ)
	if err != nil {
		return nil, err
	}
	ptr, err := store.ReadByHost(string(HostMuse), id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notRegistered("this session is not registered with Brigade (no session pointer for this Muse Code session): the SessionStart hook did not run or failed, or the plugin was enabled mid-session; send one prompt or restart the session")
	}
	if err != nil {
		return nil, err
	}
	m, err := store.ReadByPID(ptr.HostPID)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notRegistered("this session's map is gone (its Muse Code process ended or the session was closed); send one prompt or restart the session")
	}
	if err != nil {
		return nil, err
	}
	if m.Harness != string(HostMuse) || m.ClaudeSessionID != id || m.BrigadeSessionID != ptr.BrigadeSessionID {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the session pointer and the session map disagree; refusing to trust them",
			Details: map[string]string{"reason": sessionmap.ReasonMapMismatch},
		}
	}
	return m, nil
}

// notRegistered is the `config` failure for a missing map or pointer.
func notRegistered(message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"reason": ReasonNotRegistered},
	}
}
