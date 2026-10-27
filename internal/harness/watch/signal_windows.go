//go:build windows

package watch

import (
	"syscall"

	"github.com/appshapes/brigade/internal/procutil"
)

// realSignal is Deps.Signal in production on Windows: the guard's SIGTERM
// to a superseded watcher becomes procutil.Terminate, which sets that
// watcher's stop event (or terminates a process that has none).
func realSignal(pid int, _ syscall.Signal) error { return procutil.Terminate(pid) }
