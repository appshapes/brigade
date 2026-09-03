//go:build darwin

package procutil

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// szomb is SZOMB from <sys/proc.h>: the p_stat value of a process that
// has exited and awaits reaping. The other values (SIDL 1, SRUN 2, SSLEEP
// 3, SSTOP 4) all describe a process that is still alive in the sense the
// guard cares about. golang.org/x/sys/unix does not export the constant.
const szomb = 5

// query reads the process's kinfo_proc through the kern.proc.pid sysctl —
// the same table `ps` reads, readable for any pid by any user, no root
// needed (measured: pid 1's entry is returned to uid 501).
//
// For a pid the kernel does not know the sysctl returns zero bytes, which
// golang.org/x/sys reports as EIO (measured for a reaped child and for a
// bogus pid); that is errGone here. p_starttime is a struct timeval set at
// fork, so two processes started within one second carry different
// microsecond values (measured: .958667 and .961267 in the same second).
func query(pid int) (state, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) || errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return state{}, errGone
		}
		return state{}, fmt.Errorf("procutil: sysctl kern.proc.pid %d: %w", pid, err)
	}
	if int(kp.Proc.P_pid) != pid {
		return state{}, fmt.Errorf("procutil: sysctl kern.proc.pid %d answered for pid %d", pid, kp.Proc.P_pid)
	}
	tv := kp.Proc.P_starttime
	return state{
		zombie: kp.Proc.P_stat == szomb,
		token:  fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec),
	}, nil
}
