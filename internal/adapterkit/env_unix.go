//go:build darwin || linux

package adapterkit

import "path/filepath"

// HomeVar names the variable HomeDir reads: HOME here, USERPROFILE on Windows.
const HomeVar = "HOME"

// envNameEqual compares two variable names: byte for byte on unix, where
// PATH and Path are different variables.
func envNameEqual(a, b string) bool { return a == b }

// defaultConfigDir is $XDG_CONFIG_HOME/brigade, else $HOME/.config/brigade.
func defaultConfigDir(environ []string) (string, error) {
	return xdgOrHome(environ, "XDG_CONFIG_HOME", ".config")
}

// defaultStateDir is $XDG_STATE_HOME/brigade, else $HOME/.local/state/brigade.
func defaultStateDir(environ []string) (string, error) {
	return xdgOrHome(environ, "XDG_STATE_HOME", ".local", "state")
}

// CacheBinDir is the bootstrap's binary cache, plugin/bin/brigade's own
// rule: an absolute XDG_DATA_HOME, else HOME/.local/share, then
// brigade/bin. ok is false when neither resolves.
func CacheBinDir(environ []string) (string, bool) {
	base := Getenv(environ, "XDG_DATA_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home := HomeDir(environ)
		if home == "" {
			return "", false
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "brigade", "bin"), true
}

// xdgOrHome is the absolute-XDG-else-HOME chain of 3.2: a relative XDG
// value is IGNORED (the XDG spec requires it, and 6.2 explains why it
// matters here: hooks run with cwd = project directory, so a trusted
// repository's settings `env` block could otherwise point the config or
// state directory at a path inside the project).
func xdgOrHome(environ []string, xdgVar string, homeParts ...string) (string, error) {
	if v := Getenv(environ, xdgVar); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "brigade"), nil
	}
	home := HomeDir(environ)
	if home == "" {
		return "", errNoHome("cannot resolve the brigade directories")
	}
	parts := append([]string{home}, homeParts...)
	parts = append(parts, "brigade")
	return filepath.Join(parts...), nil
}

// errNoHome is the `config` error for an unset or relative HOME.
func errNoHome(what string) error {
	return errUnresolvable(HomeVar, HomeVar+" is unset or not an absolute path; "+what)
}
