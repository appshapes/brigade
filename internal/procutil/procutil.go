//go:build darwin || linux

// Package procutil answers one question about a process id — is the
// process alive, and which incarnation of the pid is it — for the watcher's
// single-instance guard (plan 6.6, corrected by E0-5; package tree 7.1).
//
// E0-5 item 1 is the reason this package exists. A SIGKILLed Claude Code
// process stays a ZOMBIE until whoever owns it reaps the corpse — measured
// at 27.6 s and 59.0 s, with no upper bound that belongs to Brigade — and
// for that whole time kill(pid, 0) keeps returning success, `ps -o
// lstart=` keeps printing the original start time, and the socket file
// stays on disk. A guard built on kill(pid, 0) alone therefore reports a
// dead session as alive. [Lookup] reads the process STATE from the kernel
// (a sysctl on darwin, /proc on linux) so a zombie reads as dead, and it
// reads the process's start time at the finest granularity the kernel
// offers so that a reused pid reads as a different process: E0-5's
// "unfixed limitation" — `ps -o lstart=` has 1 s resolution, and six
// processes were observed sharing one token — is fixed by construction on
// darwin, where the token carries microseconds.
//
// The package spawns nothing and reads nothing but the kernel's own
// process table. Its two implementation files carry `//go:build darwin`
// and `//go:build linux` so a third operating system fails to build
// instead of compiling a guard that cannot see process state (D33).
package procutil

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Info is what the guard needs to know about a pid at one instant.
//
// The fields are deliberately separate rather than folded into one
// "alive" boolean: the pidfile guard (internal/harness/pidfile) combines
// them with the stored start token, and a caller diagnosing a stale
// pidfile wants to say WHICH condition failed.
type Info struct {
	// PID is the pid that was looked up, echoed so a caller holding
	// several Infos cannot confuse them.
	PID int
	// Exists reports that the pid names a process the kernel still knows
	// about: kill(pid, 0) returned nil or EPERM. A zombie EXISTS in this
	// sense — that is the whole E0-5 trap — so Exists alone never means
	// alive; read Zombie too.
	Exists bool
	// Zombie reports that the process has exited and is waiting to be
	// reaped (darwin p_stat SZOMB, linux /proc state Z). It can only be
	// true when Exists is.
	Zombie bool
	// Foreign reports that kill(pid, 0) returned EPERM: the pid belongs
	// to another user, so it cannot be the watcher this user's hook
	// spawned. The guard treats it as pid reuse.
	Foreign bool
	// StartToken identifies THIS incarnation of the pid. It is the
	// process's start time as the kernel records it, rendered as a
	// decimal string that is compared byte for byte and never parsed
	// again: darwin "<sec>.<usec>" (usec zero-padded to six digits),
	// linux the start time in clock ticks since boot (field 22 of
	// /proc/<pid>/stat). It is empty when the process does not exist or
	// its state could not be read.
	StartToken string
}

// ErrInvalidPID is returned by [Lookup] for a pid that is not positive: 0
// and negative values address process groups in kill(2), and a guard that
// signalled a whole group by mistake would be a bug rather than a lookup.
var ErrInvalidPID = errors.New("procutil: pid must be positive")

// errGone is returned by the per-OS query when the process vanished
// between the kill(pid, 0) probe and the state read. Lookup turns it into
// Exists=false; it never escapes the package.
var errGone = errors.New("procutil: process is gone")

// Lookup reports the state of pid.
//
// It returns (Info{PID: pid}, nil) — Exists false and everything else zero
// — for a pid the kernel does not know, including a pid that was reaped a
// moment ago and a pid that vanished between the two kernel calls this
// function makes. It returns an error only for an invalid pid
// ([ErrInvalidPID]) or when the kernel refused a query it should have
// answered; in that case the Info alongside is partial (Exists and Foreign
// are set, StartToken is empty), which the pidfile guard reads as "not
// proven alive".
//
// Lookup never signals anything: kill(pid, 0) delivers no signal, it only
// asks the kernel whether it could.
func Lookup(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, ErrInvalidPID
	}
	info := Info{PID: pid}
	switch err := unix.Kill(pid, 0); {
	case err == nil:
		info.Exists = true
	case errors.Is(err, unix.EPERM):
		info.Exists = true
		info.Foreign = true
	case errors.Is(err, unix.ESRCH):
		return info, nil
	default:
		return info, fmt.Errorf("procutil: kill(%d, 0): %w", pid, err)
	}
	st, err := query(pid)
	if err != nil {
		if errors.Is(err, errGone) {
			return Info{PID: pid}, nil
		}
		return info, err
	}
	info.Zombie = st.zombie
	info.StartToken = st.token
	return info, nil
}

// state is what the per-OS query returns: whether the process is a zombie
// and its start token.
type state struct {
	zombie bool
	token  string
}
