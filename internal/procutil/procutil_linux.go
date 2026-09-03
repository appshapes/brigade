//go:build linux

package procutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
)

// query reads /proc/<pid>/stat. The file is world-readable by default
// (hidepid mounts are the exception, and they surface as EACCES, which is
// returned as an error rather than guessed around). A pid the kernel does
// not know has no /proc directory, so ENOENT is errGone; a process that
// has exited but not been reaped still has one, with state Z — the E0-5
// case this package exists for. The start token is field 22, the start
// time in clock ticks since boot, taken as the decimal string the kernel
// printed and never parsed into a number: it is compared byte for byte
// and nothing else. A tick is 10 ms (CLK_TCK 100), so a pid reused within
// the same tick as its predecessor started is undetectable here — far
// finer than the 1 s of `ps -o lstart=` E0-5 measured, and with the
// kernel's default pid_max of 4194304 a reuse inside one tick is not a
// realistic event; darwin's microsecond token is finer still.
func query(pid int) (state, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return state{}, errGone
		}
		return state{}, fmt.Errorf("procutil: read /proc/%d/stat: %w", pid, err)
	}
	st, err := parseProcStat(data)
	if err != nil {
		return state{}, fmt.Errorf("procutil: /proc/%d/stat: %w", pid, err)
	}
	if st.pid != pid {
		return state{}, fmt.Errorf("procutil: /proc/%d/stat names pid %d", pid, st.pid)
	}
	return state{
		zombie: st.state == 'Z',
		token:  st.startTicks,
	}, nil
}
