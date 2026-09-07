package config

import (
	"path/filepath"
	"strconv"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The watcher's environment (6.6). The hook builds it FROM SCRATCH and
// these six BRIGADE_* variables are the hook's own values; the watcher is
// the one process that reads BRIGADE_* inside a session, and only because
// its environment was never the session's. P3-5 wires FromWatcherEnv; the
// hook is its only legitimate producer (WatcherEnv.Vars).
const (
	WatcherClaudePIDVar      = "BRIGADE_CLAUDE_PID"
	WatcherProfileVar        = "BRIGADE_PROFILE"
	WatcherConfigDirVar      = "BRIGADE_CONFIG_DIR"
	WatcherStateDirVar       = "BRIGADE_STATE_DIR"
	WatcherAdapterCommandVar = "BRIGADE_ADAPTER_COMMAND"
	WatcherTeamInboundVar    = "BRIGADE_TEAM_INBOUND"
)

// ReasonWatcherEnvIncomplete: a variable the hook always sets is missing.
const ReasonWatcherEnvIncomplete = "watcher_env_incomplete"

// A WatcherEnv is the watcher's configuration as the hook passed it.
type WatcherEnv struct {
	// ClaudePID is the Claude Code process the watcher serves
	// (BRIGADE_CLAUDE_PID, never CLAUDE_PID: the watcher's environment is
	// not the session's).
	ClaudePID int
	// Profile, ConfigDir and StateDir are the hook's resolved values.
	Profile   string
	ConfigDir string
	StateDir  string
	// Adapter is the RESOLVED adapter (DecodeAdapter of the JSON array the
	// hook recorded in the map); a name is refused here.
	Adapter Adapter
	// TeamInbound is the hook's effective policy; TeamInboundWarning is
	// set only if the hook passed something it should not have.
	TeamInbound        Inbound
	TeamInboundWarning string
}

// FromWatcherEnv reads exactly the six watcher variables from environ —
// nothing else in environ is consulted, CLAUDE_PLUGIN_OPTION_* included.
// BRIGADE_CLAUDE_PID, BRIGADE_CONFIG_DIR and BRIGADE_STATE_DIR are
// required (the hook always sets them); the profile defaults to "default"
// and is validated; the two directories must be absolute. Failures are
// `config`.
func FromWatcherEnv(environ []string) (WatcherEnv, error) {
	var w WatcherEnv
	raw := adapterkit.Getenv(environ, WatcherClaudePIDVar)
	if raw == "" {
		return WatcherEnv{}, watcherErr(WatcherClaudePIDVar, ReasonWatcherEnvIncomplete, "the watcher environment lacks the Claude Code pid; `brigade watch` is started by the SessionStart hook, not by hand")
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return WatcherEnv{}, watcherErr(WatcherClaudePIDVar, ReasonInvalidClaudePID, "the watcher's Claude Code pid is not a positive integer")
	}
	w.ClaudePID = pid

	w.Profile = adapterkit.Getenv(environ, WatcherProfileVar)
	if w.Profile == "" {
		w.Profile = adapterkit.DefaultProfileName
	}
	if err := adapterkit.CheckProfileName(w.Profile); err != nil {
		return WatcherEnv{}, watcherErr(WatcherProfileVar, "invalid_team_key", "the watcher's team key is invalid")
	}

	for _, d := range []struct {
		name string
		dst  *string
	}{{WatcherConfigDirVar, &w.ConfigDir}, {WatcherStateDirVar, &w.StateDir}} {
		v := adapterkit.Getenv(environ, d.name)
		switch {
		case v == "":
			return WatcherEnv{}, watcherErr(d.name, ReasonWatcherEnvIncomplete, "the watcher environment lacks "+d.name)
		case !filepath.IsAbs(v):
			return WatcherEnv{}, watcherErr(d.name, ReasonRelativePath, d.name+" must be an absolute path")
		}
		*d.dst = filepath.Clean(v)
	}

	w.Adapter, err = DecodeAdapter(adapterkit.Getenv(environ, WatcherAdapterCommandVar))
	if err != nil {
		return WatcherEnv{}, err
	}
	w.TeamInbound, w.TeamInboundWarning = ParseInbound(adapterkit.Getenv(environ, WatcherTeamInboundVar))
	return w, nil
}

// Vars renders the six watcher variables in os.Environ form, for the hook
// to append to the from-scratch environment it builds (6.6). The
// messaging socket and token are NOT here: the hook adds them itself, and
// the token never passes through this package.
func (w WatcherEnv) Vars() ([]string, error) {
	adapter, err := w.Adapter.Encode()
	if err != nil {
		return nil, err
	}
	return []string{
		WatcherClaudePIDVar + "=" + strconv.Itoa(w.ClaudePID),
		WatcherProfileVar + "=" + w.Profile,
		WatcherConfigDirVar + "=" + w.ConfigDir,
		WatcherStateDirVar + "=" + w.StateDir,
		WatcherAdapterCommandVar + "=" + adapter,
		WatcherTeamInboundVar + "=" + string(w.TeamInbound),
	}, nil
}

// watcherErr is the `config` failure for one watcher variable.
func watcherErr(variable, reason, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"variable": variable, "reason": reason},
	}
}
