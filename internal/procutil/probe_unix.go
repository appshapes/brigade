//go:build darwin || linux

package procutil

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// probe asks the kernel whether pid exists and whether it is this user's,
// with kill(pid, 0), which delivers no signal.
func probe(pid int) (exists, foreign bool, err error) {
	switch err := unix.Kill(pid, 0); {
	case err == nil:
		return true, false, nil
	case errors.Is(err, unix.EPERM):
		return true, true, nil
	case errors.Is(err, unix.ESRCH):
		return false, false, nil
	default:
		return false, false, fmt.Errorf("procutil: kill(%d, 0): %w", pid, err)
	}
}

// Terminate asks pid to stop the way a hook or a superseding watcher does:
// SIGTERM, which the watcher handles by closing its session first.
func Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// Kill ends pid at once: SIGKILL.
func Kill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// IsNotRunning reports whether a Terminate or Kill error means the pid
// names no process (ESRCH).
func IsNotRunning(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}

// DetachAttr is the SysProcAttr of a child that must outlive its parent
// with no controlling terminal: its own session (Setsid).
func DetachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// StopRequests is the platform's out-of-band stop channel for a
// long-running Brigade process: none on unix, where Terminate is a
// signal the process registers for itself (signal.NotifyContext). The
// cleanup is a no-op.
func StopRequests() (<-chan struct{}, func()) {
	return nil, func() {}
}
