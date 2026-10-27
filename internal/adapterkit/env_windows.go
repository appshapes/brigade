//go:build windows

package adapterkit

import (
	"path/filepath"
	"strings"
)

// HomeVar names the variable HomeDir reads: USERPROFILE here, HOME on unix
// (a hook on Windows has no HOME at all, E11).
const HomeVar = "USERPROFILE"

// envNameEqual compares two variable names as Windows does, ignoring
// case: os.Environ hands out `Path=`, and a caller asks for PATH.
func envNameEqual(a, b string) bool { return strings.EqualFold(a, b) }

// defaultConfigDir is %APPDATA%\brigade: the roaming profile, where a
// user's own configuration belongs on Windows, and where the Windows
// launcher reads the developer pointer (cmd/brigade-launcher). XDG_* is
// not read on Windows.
func defaultConfigDir(environ []string) (string, error) {
	return profileDir(environ, "APPDATA", "brigade")
}

// defaultStateDir is %LOCALAPPDATA%\brigade: machine-local, the same shape
// as $XDG_STATE_HOME/brigade, holding the launcher's binary cache in its
// bin\ as well.
func defaultStateDir(environ []string) (string, error) {
	return profileDir(environ, "LOCALAPPDATA", "brigade")
}

// CacheBinDir is the launcher's binary cache, %LOCALAPPDATA%\brigade\bin
// (cmd/brigade-launcher/launcher.go names the same directory).
func CacheBinDir(environ []string) (string, bool) {
	dir, err := profileDir(environ, "LOCALAPPDATA", "brigade", "bin")
	if err != nil {
		return "", false
	}
	return dir, true
}

// profileDir joins parts onto the profile variable. A real Windows session
// always has APPDATA and LOCALAPPDATA; an environment built from scratch
// without them (the test environment, a bare service account) falls back
// to the XDG variable the unix resolver reads for the same directory, so
// the same test expectations hold on both platforms.
func profileDir(environ []string, variable string, parts ...string) (string, error) {
	base := Getenv(environ, variable)
	if base == "" || !filepath.IsAbs(base) {
		if xdg := Getenv(environ, xdgFallback[variable]); xdg != "" && filepath.IsAbs(xdg) {
			return filepath.Join(append([]string{xdg}, parts...)...), nil
		}
		return "", errUnresolvable(variable, variable+" is unset or not an absolute path; cannot resolve the brigade directories")
	}
	return filepath.Join(append([]string{base}, parts...)...), nil
}

// xdgFallback pairs each profile variable with the XDG variable of the
// same directory on unix.
var xdgFallback = map[string]string{"APPDATA": "XDG_CONFIG_HOME", "LOCALAPPDATA": "XDG_STATE_HOME"}
