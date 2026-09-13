//go:build linux

package testutil

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

// TestWriteExecutableUnderConcurrentForks is the proof that the helper can
// fail: with os.WriteFile in its place it fails every run (8 of 8, at 14–71
// of the 1,000 execs, linux 6.12 at 2 CPUs under -race), so 1,000 execs do
// not pass by luck. It is not a timing bound; it asks whether ETXTBSY ever
// happened. Linux only: macOS does not enforce ETXTBSY and assesses a
// fresh executable on its first exec (~150 ms each), so 1,000 of them
// would cost the macos job minutes for nothing.
func TestWriteExecutableUnderConcurrentForks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := t.Context()
	stop := make(chan struct{})
	var forkers sync.WaitGroup
	for range 4 {
		forkers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_ = exec.CommandContext(ctx, "/bin/true").Run()
				}
			}
		})
	}
	var busy atomic.Int64
	var writers sync.WaitGroup
	for w := range 4 {
		writers.Go(func() {
			path := filepath.Join(dir, "s"+strconv.Itoa(w))
			for range 250 {
				if err := WriteExecutableFile(path, []byte("#!/bin/sh\nexit 0\n")); err != nil {
					t.Errorf("write %s: %v", path, err)
					return
				}
				err := exec.CommandContext(ctx, path).Run()
				switch {
				case errors.Is(err, syscall.ETXTBSY):
					busy.Add(1)
				case err != nil:
					t.Errorf("exec %s: %v", path, err)
				}
			}
		})
	}
	writers.Wait()
	close(stop)
	forkers.Wait()
	if n := busy.Load(); n != 0 {
		t.Fatalf("%d of 1000 execs failed with ETXTBSY", n)
	}
}
