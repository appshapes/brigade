package fs

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWatchExitsZeroWhenSignalledOnReady pins the ordering 4.4.9 needs:
// the SIGTERM handler is installed BEFORE `ready` is written, so a
// harness that signals the instant it sees `ready` (C-38) gets exit 0,
// never a death by signal. The window between `ready` and the handler was
// real: with the handler installed after the write, CI's slower runner saw
// C-38 report "exit -1 after SIGTERM, want 0". A race cannot be made to
// fail on demand, so this test drives the real binary and signals at the
// earliest observable moment, repeatedly; it documents the contract and
// catches a regression on any machine slow enough to show it.
func TestWatchExitsZeroWhenSignalledOnReady(t *testing.T) {
	t.Parallel()
	c := newChild(t, "")
	alice, _, _ := c.fixture()
	for round := range 8 {
		cmd := exec.CommandContext(t.Context(), c.binary, "message", "watch", "--session", alice) //nolint:gosec // argv built by this test
		cmd.Env = c.env
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		first := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(stdout).ReadString('\n')
			first <- line
		}()
		select {
		case line := <-first:
			if !strings.Contains(line, `"event":"ready"`) {
				t.Fatalf("round %d: first line %q is not ready", round, line)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("round %d: no ready within 5 s (stderr %s)", round, stderr.String())
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("round %d: SIGTERM: %v", round, err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("round %d: did not exit within 5 s of SIGTERM", round)
		}
		_ = stdin.Close()
		if code := cmd.ProcessState.ExitCode(); code != 0 {
			t.Fatalf("round %d: exit %d after SIGTERM, want 0 (%s; stderr %s)", round, code, cmd.ProcessState, stderr.String())
		}
	}
}
