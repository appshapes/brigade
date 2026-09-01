// The hostile-umask half of the mode discipline (E0-1, U-10): the
// in-process tests run under the developer's umask, where os.CreateTemp's
// own 0600 already suffices, so WriteAtomic's explicit chmod — the code
// that keeps the mode EXACTLY 0600 under a narrowing umask — was
// invisible to them (the P1-3 adversarial pass proved that mutant
// survived). syscall.Umask is process-global and would race parallel
// tests, so the umask is set in a re-exec'd CHILD process instead.

//go:build darwin || linux

package adapterkit_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// TestHelperChildUmask is not a test: it is the child half of
// TestWriteAtomicExact0600UnderHostileUmask, selected by
// ADAPTERKIT_UMASK_CHILD.
func TestHelperChildUmask(t *testing.T) {
	if os.Getenv("ADAPTERKIT_UMASK_CHILD") != "write" {
		t.Skip("child-process helper; run by the umask tests")
	}
	// 0o277 clears the owner-write bit: it narrows a plain 0600 create
	// to 0400, so only an explicit chmod after the create keeps the
	// documented exact mode. (0o177 would be a broken instrument — its
	// bits do not overlap 0600 at all.) The old umask is restored before
	// the test binary exits: a coverage-instrumented run writes its
	// meta-data file at exit, and creating that file under 0o277 fails
	// with permission denied.
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)
	if err := adapterkit.WriteAtomic(os.Getenv("ADAPTERKIT_UMASK_PATH"), []byte("under umask")); err != nil {
		t.Fatalf("child WriteAtomic: %v", err)
	}
}

func TestWriteAtomicExact0600UnderHostileUmask(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.json")
	var out bytes.Buffer
	//nolint:gosec // G204/G702: the argv re-executes this very test binary; no shell is involved
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperChildUmask$")
	cmd.Env = append(os.Environ(),
		"ADAPTERKIT_UMASK_CHILD=write",
		"ADAPTERKIT_UMASK_PATH="+path,
	)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("child: %v\n%s", err, out.String())
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode under umask 0277 = %o, want exactly 0600 (the explicit chmod is load-bearing)", fi.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "under umask" {
		t.Fatalf("content = %q", got)
	}
}
