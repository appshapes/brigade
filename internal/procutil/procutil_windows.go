//go:build windows

package procutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive is STILL_ACTIVE, the exit code GetExitCodeProcess reports
// for a process that has not exited.
const stillActive = 259

// probe opens the process for the least information Windows grants on
// every pid: no such pid answers ERROR_INVALID_PARAMETER, another user's
// (or a protected) process ERROR_ACCESS_DENIED, which is what EPERM means
// on unix: it exists, it is not ours.
func probe(pid int) (exists, foreign bool, err error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // G115: a pid is non-negative, checked by Lookup
	switch {
	case err == nil:
		_ = windows.CloseHandle(h)
		return true, false, nil
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return false, false, nil
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return true, true, nil
	default:
		return false, false, fmt.Errorf("procutil: OpenProcess(%d): %w", pid, err)
	}
}

// query reads the process's state through its handle: a process that has
// exited but whose object someone still holds a handle to reads as a
// zombie (the E0-5 trap in Windows clothing), its creation time is the
// start token (a FILETIME, 100 ns units, so a reused pid reads as a
// different process), its image name is the command, and the parent comes
// from the process snapshot.
func query(pid int) (state, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // G115: as above
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return state{}, errGone
		}
		return state{}, fmt.Errorf("procutil: OpenProcess(%d): %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return state{}, fmt.Errorf("procutil: GetExitCodeProcess(%d): %w", pid, err)
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return state{}, fmt.Errorf("procutil: GetProcessTimes(%d): %w", pid, err)
	}
	st := state{
		zombie: code != stillActive,
		token:  strconv.FormatUint(uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime), 10),
	}
	st.command = imageName(h)
	st.parent = parentPID(pid)
	return st, nil
}

// imageName is the executable's base name without its .exe, the nearest
// thing Windows has to the kernel comm.
func imageName(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(windows.MAX_LONG_PATH)
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	name := filepath.Base(windows.UTF16ToString(buf[:n]))
	return strings.TrimSuffix(strings.TrimSuffix(name, ".exe"), ".EXE")
}

// parentPID walks the process snapshot for pid's parent; 0 when the
// snapshot cannot be read or the pid is not in it.
func parentPID(pid int) int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err := windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if int(pe.ProcessID) == pid {
			return int(pe.ParentProcessID)
		}
	}
	return 0
}

// stopEventName is the named event a Brigade process listens on for the
// stop a unix process gets as SIGTERM: in the session-local namespace, so
// only this logon's processes can see or set it.
func stopEventName(pid int) string {
	return `Local\brigade-stop-` + strconv.Itoa(pid)
}

// Terminate asks pid to stop as SIGTERM does on unix: a Brigade process
// that called StopRequests has its stop event set and ends its own way
// (the watcher closes its session first); any other process is
// terminated outright.
func Terminate(pid int) error {
	name, err := windows.UTF16PtrFromString(stopEventName(pid))
	if err != nil {
		return err
	}
	if h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name); err == nil {
		defer func() { _ = windows.CloseHandle(h) }()
		return windows.SetEvent(h)
	}
	return Kill(pid)
}

// Kill ends pid at once: TerminateProcess.
func Kill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)) //nolint:gosec // G115: a pid is non-negative
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.TerminateProcess(h, 1)
}

// IsNotRunning reports whether a Terminate or Kill error means the pid
// names no process: ERROR_INVALID_PARAMETER is what OpenProcess answers
// for one.
func IsNotRunning(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}

// DetachAttr is the SysProcAttr of a child that must outlive its parent
// unseen: its own process group (so a console Ctrl-C never reaches it)
// and a hidden console of its own, which its children inherit.
// CREATE_NO_WINDOW and not DETACHED_PROCESS: Windows ignores the first
// beside the second, and a process with no console at all gives every
// console child it starts (the watcher's adapter, Syncthing's own child)
// a fresh, visible window; seen on a Windows 11 session (card 133, 0.41.1).
func DetachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

// StopRequests creates this process's stop event and returns a channel
// that closes when someone sets it (Terminate from a hook or another
// watcher). The cleanup sets and closes the event so the waiting
// goroutine ends; after it, Terminate falls back to TerminateProcess.
func StopRequests() (<-chan struct{}, func()) {
	name, err := windows.UTF16PtrFromString(stopEventName(os.Getpid()))
	if err != nil {
		return nil, func() {}
	}
	h, err := windows.CreateEvent(nil, 1, 0, name) // manual reset, not signalled
	if err != nil {
		return nil, func() {}
	}
	ch := make(chan struct{})
	go func() {
		_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
		close(ch)
	}()
	return ch, func() {
		_ = windows.SetEvent(h)
		<-ch
		_ = windows.CloseHandle(h)
	}
}
