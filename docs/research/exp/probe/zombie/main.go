package main

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// Demonstrates that kill(pid, 0) still succeeds on a killed-but-unreaped child (zombie),
// and fails with ESRCH only after the parent reaps it. This is the trap for a watcher test
// that uses a child `sleep` as the fake CLAUDE_PID.
func alive(pid int) string {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return "alive (kill 0 == nil)"
	case errors.Is(err, syscall.ESRCH):
		return "gone (ESRCH)"
	default:
		return "other: " + err.Error()
	}
}

func main() {
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	pid := cmd.Process.Pid
	fmt.Println("started sleeper", pid, "->", alive(pid))
	_ = cmd.Process.Kill()
	time.Sleep(200 * time.Millisecond)
	fmt.Println("after SIGKILL, before Wait ->", alive(pid))
	_ = cmd.Wait()
	fmt.Println("after Wait ->", alive(pid))

	// A second sleeper reaped by a background goroutine (what a test should do), timing a 2 s poller.
	cmd2 := exec.Command("sleep", "300")
	_ = cmd2.Start()
	pid2 := cmd2.Process.Pid
	go func() { _ = cmd2.Wait() }()
	start := time.Now()
	_ = cmd2.Process.Kill()
	for range 50 {
		if errors.Is(syscall.Kill(pid2, 0), syscall.ESRCH) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("sleeper2 seen gone after", time.Since(start).Round(10*time.Millisecond), "->", alive(pid2))
}
