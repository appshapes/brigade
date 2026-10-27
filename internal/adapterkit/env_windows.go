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

// defaultStateDir is %LOCALAPPDATA%\brigade\state: machine-local, beside
// the launcher's binary cache.
func defaultStateDir(environ []string) (string, error) {
	return profileDir(environ, "LOCALAPPDATA", "brigade", "state")
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

func profileDir(environ []string, variable string, parts ...string) (string, error) {
	base := Getenv(environ, variable)
	if base == "" || !filepath.IsAbs(base) {
		return "", errUnresolvable(variable, variable+" is unset or not an absolute path; cannot resolve the brigade directories")
	}
	return filepath.Join(append([]string{base}, parts...)...), nil
}
