//go:build windows

package procutil

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestDetachAttrGivesAHiddenConsoleNotNone(t *testing.T) {
	t.Parallel()
	flags := DetachAttr().CreationFlags
	if flags&windows.DETACHED_PROCESS != 0 {
		t.Fatalf("CreationFlags %#x carry DETACHED_PROCESS: Windows then ignores CREATE_NO_WINDOW, and the console-less watcher's adapter child opens a visible console window (card 133, 0.41.1)", flags)
	}
	if flags&windows.CREATE_NO_WINDOW == 0 || flags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("CreationFlags %#x: want CREATE_NO_WINDOW and CREATE_NEW_PROCESS_GROUP", flags)
	}
}

func TestLookupSeesThisProcess(t *testing.T) {
	t.Parallel()
	info, err := Lookup(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Exists || info.Zombie || info.Foreign {
		t.Fatalf("Lookup(self) = %+v; want Exists, not Zombie, not Foreign", info)
	}
	if info.StartToken == "" {
		t.Fatal("Lookup(self) has no start token: the creation time was not read")
	}
	if info.Command == "" {
		t.Fatal("Lookup(self) has no command: the image name was not read")
	}
	if info.ParentPID <= 0 {
		t.Fatalf("Lookup(self).ParentPID = %d; want the test runner's pid", info.ParentPID)
	}
	if again, _ := Lookup(os.Getpid()); again.StartToken != info.StartToken {
		t.Fatalf("the start token moved between two lookups: %q then %q", info.StartToken, again.StartToken)
	}
}

func TestLookupOfAnUnusedPIDIsGone(t *testing.T) {
	t.Parallel()
	// Windows pids are multiples of four; an odd one is never a process.
	info, err := Lookup(4000001)
	if err != nil {
		t.Fatal(err)
	}
	if info.Exists || info.StartToken != "" {
		t.Fatalf("Lookup(unused) = %+v; want Exists false and no token", info)
	}
}

func TestAnExitedChildReadsAsAZombieWhileItsHandleIsHeld(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(context.Background(), "cmd.exe", "/c", "exit", "0") //nolint:gosec // G204: a fixed argv, no shell string
	if err := cmd.Start(); err != nil {
		t.Skip("cmd.exe not runnable here:", err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := Lookup(pid)
		if err != nil {
			t.Fatal(err)
		}
		if info.Exists && info.Zombie {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the exited child never read as a zombie: %+v", info)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminateSetsTheStopEventOfAListeningProcess(t *testing.T) {
	t.Parallel()
	stop, cleanup := StopRequests()
	if stop == nil {
		t.Fatal("StopRequests returned no channel on Windows")
	}
	defer cleanup()
	select {
	case <-stop:
		t.Fatal("the stop channel closed before anyone asked")
	default:
	}
	if err := Terminate(os.Getpid()); err != nil {
		t.Fatalf("Terminate(self) = %v; want the event set, never TerminateProcess", err)
	}
	select {
	case <-stop:
	case <-time.After(5 * time.Second):
		t.Fatal("Terminate did not reach the stop channel within 5 s")
	}
}

func TestIsNotRunningNamesAnUnusedPID(t *testing.T) {
	t.Parallel()
	err := Kill(4000001)
	if err == nil {
		t.Fatal("Kill(unused pid) succeeded")
	}
	if !IsNotRunning(err) {
		t.Fatalf("IsNotRunning(%v) = false; want true for an unused pid", err)
	}
}
