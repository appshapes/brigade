//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// procStart reads /proc/<pid>/stat field 22 (starttime, clock ticks since
// boot). For a PID-reuse guard the raw tick value is compared verbatim; the
// wall-clock conversion below is for display only.
func procStart(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "err: " + err.Error()
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // comm may contain spaces and parentheses
	if i < 0 {
		return "unparsable"
	}
	fields := strings.Fields(s[i+1:]) // fields[0] is field 3 (state)
	if len(fields) < 20 {
		return "short"
	}
	ticks := fields[19] // field 22
	st, _ := os.ReadFile("/proc/stat")
	var btime int64
	for _, line := range strings.Split(string(st), "\n") {
		if strings.HasPrefix(line, "btime ") {
			btime, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		}
	}
	n, _ := strconv.ParseInt(ticks, 10, 64)
	const hz = 100 // sysconf(_SC_CLK_TCK) on mainstream Linux; display only
	return fmt.Sprintf("starttime_ticks=%s btime=%d start~%s", ticks, btime, time.Unix(btime+n/hz, 0).UTC().Format(time.RFC3339))
}
