//go:build darwin || linux

package watch

import "syscall"

// realSignal is Deps.Signal in production: kill(2).
func realSignal(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
