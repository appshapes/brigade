//go:build windows

package syncthing

import (
	"errors"
	"syscall"

	"github.com/appshapes/brigade/internal/procutil"
)

// errNotRunning is what signalDaemon answers for a daemon that is
// already gone (unix answers ESRCH); notRunning reads both.
var errNotRunning = errors.New("syncthing: daemon is not running")

// signalDaemon on Windows: there is no signal to ask with, and the
// graceful path is the REST shutdown stop() tries first, so SIGTERM and
// SIGKILL alike end the daemon with TerminateProcess. `syncthing serve
// --no-restart` runs the real Syncthing in the process start() spawned,
// so there is no child to reach past it.
func signalDaemon(pid int, _ syscall.Signal) error {
	if !alive(pid, "") {
		return errNotRunning
	}
	return procutil.Kill(pid)
}

// groupAlive: Windows has no process groups of the daemon's id to poll.
func groupAlive(int) bool { return false }

// notRunning reports whether a signalDaemon error means no such process.
func notRunning(err error) bool {
	return err == errNotRunning || procutil.IsNotRunning(err) //nolint:errorlint // the sentinel signalDaemon itself returns
}
