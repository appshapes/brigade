//go:build darwin || linux

package syncthing

import (
	"syscall"
)

// signalDaemon sends sig to the daemon's process group and to the daemon
// itself (a daemon that is not a group leader — one a test planted — has
// no group of its id). It answers nil when either reached a process.
// The signals go to the daemon's process GROUP: `syncthing serve` is a
// monitor process running the real Syncthing as its child, start's
// Setsid makes the monitor the group's leader (the group id is its pid),
// and it is the child that holds the sockets — signalling the monitor
// alone could leave the child listening.
func signalDaemon(pid int, sig syscall.Signal) error {
	gerr := syscall.Kill(-pid, sig)
	var perr error = syscall.ESRCH
	if alive(pid, "") {
		perr = syscall.Kill(pid, sig)
	}
	if gerr == nil || perr == nil {
		return nil
	}
	return perr
}

// groupAlive reports whether a process group of id pid still has a member
// this user can signal: the daemon's children outlive it for a moment.
func groupAlive(pid int) bool {
	return syscall.Kill(-pid, 0) == nil
}

// notRunning reports whether a signalDaemon error means no such process.
func notRunning(err error) bool {
	return err == syscall.ESRCH //nolint:errorlint // the sentinel signalDaemon itself returns
}
