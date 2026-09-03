package procutil

import (
	"bytes"
	"errors"
	"strconv"
)

// procStat is the subset of /proc/<pid>/stat the linux query needs. The
// parser lives in an untagged file so it is compiled and tested on every
// development machine, not only on the linux CI runner that actually
// reads /proc.
type procStat struct {
	pid        int
	state      byte
	startTicks string
}

// procStatMinFields is the number of whitespace-separated fields that must
// follow the closing parenthesis for field 22 to exist: fields 3..22 are
// twenty fields.
const procStatMinFields = 20

var errProcStatMalformed = errors.New("malformed stat line")

// parseProcStat parses one /proc/<pid>/stat line.
//
// The line is `<pid> (<comm>) <state> <ppid> …`; comm is the executable's
// name truncated to 16 bytes and it may contain spaces and parentheses
// (`(sleep 1)` is a legal comm), so the only safe split is at the LAST
// `)`: everything after it is the whitespace-separated field list
// starting at field 3, the state. Field 22, the start time in clock
// ticks, is therefore index 19 of that list. The value is kept as the
// bytes the kernel wrote; it is a token, not a number.
func parseProcStat(line []byte) (procStat, error) {
	open := bytes.IndexByte(line, '(')
	closeIdx := bytes.LastIndexByte(line, ')')
	if open < 1 || closeIdx < open {
		return procStat{}, errProcStatMalformed
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(line[:open])))
	if err != nil || pid <= 0 {
		return procStat{}, errProcStatMalformed
	}
	fields := bytes.Fields(line[closeIdx+1:])
	if len(fields) < procStatMinFields {
		return procStat{}, errProcStatMalformed
	}
	if len(fields[0]) != 1 {
		return procStat{}, errProcStatMalformed
	}
	ticks := fields[19]
	for _, c := range ticks {
		if c < '0' || c > '9' {
			return procStat{}, errProcStatMalformed
		}
	}
	return procStat{
		pid:        pid,
		state:      fields[0][0],
		startTicks: string(ticks),
	}, nil
}
