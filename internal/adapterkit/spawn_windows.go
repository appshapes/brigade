//go:build windows

package adapterkit

import (
	"os"
	"os/exec"
)

// cancelProcess on Windows is TerminateProcess: there is no SIGTERM to
// ask a child to flush with, and an adapter's one-shot run has nothing
// to flush that its stdout did not already carry. WaitDelay still bounds
// the wait and closes the pipes.
func cancelProcess(p *os.Process) error {
	return p.Kill()
}

// signalDeath: a Windows child never dies of a signal; a termination
// shows as an exit status, which the caller reports as an exit.
func signalDeath(*exec.ExitError) (string, bool) {
	return "", false
}

// childEnvPlatform is the platform half of ChildEnv's allow-list: what a
// Windows process needs to load its own libraries, find its temporary
// directory and resolve its profile (the adapter writes under the
// brigade directories, which BrigadeConfigDir and BrigadeStateDir name
// from APPDATA and LOCALAPPDATA).
var childEnvPlatform = map[string]bool{
	"SystemRoot":   true,
	"windir":       true,
	"ComSpec":      true,
	"PATHEXT":      true,
	"TEMP":         true,
	"TMP":          true,
	"USERPROFILE":  true,
	"HOMEDRIVE":    true,
	"HOMEPATH":     true,
	"APPDATA":      true,
	"LOCALAPPDATA": true,
	"SystemDrive":  true,
}
