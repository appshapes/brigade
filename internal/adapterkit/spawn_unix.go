//go:build darwin || linux

package adapterkit

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// cancelProcess is what a context cancel does to a child: SIGTERM first so
// the adapter can flush; WaitDelay escalates to SIGKILL and closes the
// pipes (plan 7.3).
func cancelProcess(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// signalDeath reports the signal that killed the child, when one did.
func signalDeath(exitErr *exec.ExitError) (string, bool) {
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return signalName(ws.Signal()), true
	}
	return "", false
}

// childEnvPlatform is the platform half of ChildEnv's allow-list: nothing
// on unix beyond the shared names.
var childEnvPlatform = map[string]bool{}

// signalNames renders a death signal by its POSIX name instead of
// Signal.String(), whose "terminated"/"killed" prose is ambiguous across
// platforms.
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL:  "SIGILL",
	syscall.SIGTRAP: "SIGTRAP",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGBUS:  "SIGBUS",
	syscall.SIGFPE:  "SIGFPE",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGUSR1: "SIGUSR1",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGTERM: "SIGTERM",
}

func signalName(sig syscall.Signal) string {
	if name, ok := signalNames[sig]; ok {
		return name
	}
	return "signal " + strconv.Itoa(int(sig))
}
