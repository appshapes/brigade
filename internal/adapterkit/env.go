package adapterkit

import (
	"path/filepath"

	"github.com/appshapes/brigade/internal/protocol"
)

// This file is the adapter kit's environment seam. The .golangci.yml
// allow-list permits os.Getenv here, but every resolver deliberately takes
// the environment as an os.Environ()-form slice instead: entry points pass
// os.Environ() (not forbidden anywhere), tests pass literals and stay
// parallel, and no code path can accidentally read the developer's real
// environment. Trust context (3.2): an adapter's environment is built from
// scratch by the harness, or is the user's own shell — the harness strips
// inherited BRIGADE_* inside Claude Code sessions before it ever spawns an
// adapter, so honouring BRIGADE_* first here is safe.

// Getenv returns the value of name in environ, or "" when it is absent.
// The LAST occurrence wins, matching os/exec's dedupEnv (an appended
// override beats an earlier value) and cli.Context.Getenv; an empty value
// counts as unset, as with os.Getenv. Exported so other adapterkit files
// (the spawn helper's allow-list construction) share one lookup.
func Getenv(environ []string, name string) string {
	prefix := name + "="
	for i := len(environ) - 1; i >= 0; i-- {
		if len(environ[i]) > len(prefix) && environ[i][:len(prefix)] == prefix {
			return environ[i][len(prefix):]
		}
	}
	return ""
}

// ConfigDir resolves the Brigade configuration directory (3.2):
// $BRIGADE_CONFIG_DIR, else $XDG_CONFIG_HOME/brigade, else
// $HOME/.config/brigade.
//
// A relative XDG_CONFIG_HOME is IGNORED (the XDG spec requires it, and 6.2
// explains why it matters here: hooks run with cwd = project directory, so
// a trusted repository's settings `env` block could otherwise point the
// config or state directory at a path inside the project). A relative
// BRIGADE_CONFIG_DIR is refused with `config` rather than ignored: the
// harness always computes an absolute value, so a relative one is a
// misconfigured shell, and silently falling back to ~/.config would honour
// a different directory than the user named. os.UserConfigDir is not used
// because it answers ~/Library/Application Support on macOS.
func ConfigDir(environ []string) (string, error) {
	return baseDir(environ, "BRIGADE_CONFIG_DIR", "XDG_CONFIG_HOME", ".config")
}

// StateDir resolves the Brigade state directory (3.2):
// $BRIGADE_STATE_DIR, else $XDG_STATE_HOME/brigade, else
// $HOME/.local/state/brigade. The same absolute-only rules as ConfigDir
// apply.
func StateDir(environ []string) (string, error) {
	return baseDir(environ, "BRIGADE_STATE_DIR", "XDG_STATE_HOME", ".local", "state")
}

// ProfileName resolves the profile a command acts on when no --profile
// flag was given: $BRIGADE_PROFILE, else "default" (4.1). The value is not
// validated here; CheckProfileName runs when the name becomes a path.
func ProfileName(environ []string) string {
	if v := Getenv(environ, "BRIGADE_PROFILE"); v != "" {
		return v
	}
	return DefaultProfileName
}

// baseDir implements the shared BRIGADE_* → absolute XDG_* → $HOME chain.
// homeParts are the path elements between $HOME and the trailing
// "brigade".
func baseDir(environ []string, brigadeVar, xdgVar string, homeParts ...string) (string, error) {
	if v := Getenv(environ, brigadeVar); v != "" {
		if !filepath.IsAbs(v) {
			return "", &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: brigadeVar + " must be an absolute path",
				Details: map[string]string{"variable": brigadeVar, "reason": "relative_path"},
			}
		}
		return filepath.Clean(v), nil
	}
	if v := Getenv(environ, xdgVar); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "brigade"), nil
	}
	home := Getenv(environ, "HOME")
	if home == "" || !filepath.IsAbs(home) {
		return "", &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "HOME is unset or not an absolute path; cannot resolve the brigade directories",
			Details: map[string]string{"variable": "HOME", "reason": "unresolvable"},
		}
	}
	parts := append([]string{home}, homeParts...)
	parts = append(parts, "brigade")
	return filepath.Join(parts...), nil
}
