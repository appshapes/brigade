//go:build darwin

package adapterkit_test

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY allocates a real pseudo-terminal pair, because the TTY refusal
// of ReadInput can only be tested for the RIGHT reason against a device
// isatty(3) answers true for — a bytes.Reader proves nothing. Darwin:
// /dev/ptmx, then the grant/unlock/name ioctls (the master itself does
// not read as a terminal on macOS, measured here).
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	fd := m.Fd()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCPTYGRANT, 0); errno != 0 {
		t.Fatalf("TIOCPTYGRANT: %v", errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCPTYUNLK, 0); errno != 0 {
		t.Fatalf("TIOCPTYUNLK: %v", errno)
	}
	var buf [128]byte
	//nolint:gosec // G103: TIOCPTYGNAME fills a caller buffer; the conversion sits inside the Syscall expression as the unsafe rules require
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		t.Fatalf("TIOCPTYGNAME: %v", errno)
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	s, err := os.OpenFile(string(buf[:n]), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open pty slave %q: %v", buf[:n], err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return m, s
}
