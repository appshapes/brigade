//go:build linux

package adapterkit_test

import (
	"os"
	"strconv"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY allocates a real pseudo-terminal pair, because the TTY refusal
// of ReadInput can only be tested for the RIGHT reason against a device
// isatty(3) answers true for — a bytes.Reader proves nothing. Linux:
// /dev/ptmx, TIOCSPTLCK to unlock the slave, TIOCGPTN for its number.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	fd := m.Fd()
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Fatalf("TIOCSPTLCK: %v", errno)
	}
	var ptn int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&ptn))); errno != 0 {
		t.Fatalf("TIOCGPTN: %v", errno)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(ptn)), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open pty slave %d: %v", ptn, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return m, s
}
