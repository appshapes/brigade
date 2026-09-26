//go:build darwin || linux

package adapterkit_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// TestRunQuiet pins the quiet spawn seam (card 35): a child that exits 0
// is a nil error, one that exits non-zero or is missing is an error, an
// empty argv is refused before anything runs, and a child that outlives
// its context is ended rather than waited for. The last wait is a hang
// catcher, never a performance bound.
func TestRunQuiet(t *testing.T) {
	t.Parallel()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` on PATH")
	}
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no `false` on PATH")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no `sleep` on PATH")
	}
	ctx := context.Background()
	if err := adapterkit.RunQuiet(ctx, adapterkit.QuietSpec{Argv: []string{truePath}}); err != nil {
		t.Errorf("true: %v, want nil", err)
	}
	if err := adapterkit.RunQuiet(ctx, adapterkit.QuietSpec{Argv: []string{falsePath}}); err == nil {
		t.Error("false: nil error, want the exit status")
	}
	if err := adapterkit.RunQuiet(ctx, adapterkit.QuietSpec{Argv: []string{"/nonexistent/brigade-quiet-test"}}); err == nil {
		t.Error("a missing executable: nil error")
	}
	if err := adapterkit.RunQuiet(ctx, adapterkit.QuietSpec{}); err == nil {
		t.Error("an empty argv: nil error")
	}
	tctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- adapterkit.RunQuiet(tctx, adapterkit.QuietSpec{Argv: []string{sleepPath, "30"}, WaitDelay: time.Second})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a child past its deadline: nil error")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunQuiet did not return after its context ended")
	}
}
