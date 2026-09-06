package adapterkit_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

func TestWriteAtomicModeAndContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "team.json")
	data := []byte(`{"version":1}` + "\n")
	if err := adapterkit.WriteAtomic(path, data); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q, want %q", got, data)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestWriteAtomicModeHonorsRequestedMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "brigade.json")
	data := []byte(`{"version":1}` + "\n")
	if err := adapterkit.WriteAtomicMode(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 0644 (the variant must not fall back to 0600)", fi.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q, want %q", got, data)
	}
}

func TestWriteAtomicReplacesWorldReadableWith0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.json")
	//nolint:gosec // G306: a world-readable file is the PRECONDITION this test replaces with 0600
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode after replace = %o, want 0600 (the rename must swap the inode)", fi.Mode().Perm())
	}
}

func TestWriteAtomicSecondWriterWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := adapterkit.WriteAtomic(path, []byte("first writer")); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(path, []byte("second writer")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second writer" {
		t.Fatalf("content = %q, want the second writer's", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want exactly one file (no temp droppings)", names)
	}
}

// TestWriteAtomicNeverTearsUnderConcurrency is the atomicity detector:
// while two writers race full-file replacements, a reader must only ever
// observe one COMPLETE payload or the other. An implementation that wrote
// in place (truncate + write) would show the reader empty or partial
// content — the positive-control run for this task proved this test goes
// red for exactly that mutation.
//
// The writers stop when the READER has seen enough, not after a fixed
// count. Run 33944568302 (the ubuntu `fast` job, the whole tree under
// `-race -shuffle=on -count=1 -covermode=atomic` on four cores) failed
// the positive control with "only 19 states" and nothing torn: the old
// floor compared two rates nothing couples. The writers' 60 replacements
// cost whatever fsync costs on the filesystem underneath — ~23 ms each on
// this Mac's APFS, a few hundred microseconds where the page cache
// absorbs them — while the reader is ONE goroutine re-reading 256 KiB
// against a whole tree's parallel tests on four cores, so whether it
// cleared 20 samples before the writers ran out of iterations was the
// scheduler's to decide. Measured: 0 failures in 30 on darwin (625–6728
// samples) and 0 in 30 in Docker at --cpus=1 on the overlay (189–368),
// against 9 in 30 at --cpus=1 and 27 in 30 at --cpus=4 with TMPDIR on a
// tmpfs, where fsync is free and the samples collapse to 1–24 — the CI
// run's 19 sits inside that band.
//
// So perWriter is now a FLOOR — every run still does at least the 60
// replacements it always did — and the writers keep going until the
// reader has caught minTransitions replacements LANDING: a sample that
// differs from the sample before it, which is what "observed a distinct
// state" was always meant to mean and is strictly more than a repeat read
// of one payload proves. A run that cannot reach the floor inside
// provokeWindow fails saying the race could not be provoked, which is a
// different fact from a tear and now says so.
func TestWriteAtomicNeverTearsUnderConcurrency(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	payloadA := bytes.Repeat([]byte("A"), 256*1024)
	payloadB := bytes.Repeat([]byte("B"), 256*1024)
	if err := adapterkit.WriteAtomic(path, payloadA); err != nil {
		t.Fatal(err)
	}

	const (
		perWriter      = 30
		minTransitions = 20
		provokeWindow  = 30 * time.Second
	)
	var transitions atomic.Int64
	deadline := time.Now().Add(provokeWindow)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	writerErr := make(chan error, 2)
	for _, payload := range [][]byte{payloadA, payloadB} {
		wg.Add(1)
		go func(p []byte) {
			defer wg.Done()
			for i := 0; ; i++ {
				if err := adapterkit.WriteAtomic(path, p); err != nil {
					writerErr <- err
					return
				}
				select {
				case <-stop:
					return
				default:
				}
				if i+1 >= perWriter &&
					(transitions.Load() >= minTransitions || !time.Now().Before(deadline)) {
					return
				}
			}
		}(payload)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	// Registered AFTER t.TempDir's own cleanup, so LIFO runs it first: no
	// writer is still replacing the file when the directory is removed,
	// however this test ends — including the t.Fatalf paths below.
	t.Cleanup(func() { close(stop); <-done })

	var last byte
	for {
		select {
		case err := <-writerErr:
			t.Fatalf("writer: %v", err)
		case <-done:
			// A writer that failed on its last iteration races the close;
			// report the error rather than the state count.
			select {
			case err := <-writerErr:
				t.Fatalf("writer: %v", err)
			default:
			}
			if n := transitions.Load(); n < minTransitions {
				t.Fatalf("the reader caught only %d of %d replacements landing within %s; "+
					"the race could not be provoked, so this run proves nothing about tearing",
					n, minTransitions, provokeWindow)
			}
			// Final state: one complete payload, 0600, no temp files.
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payloadA) && !bytes.Equal(got, payloadB) {
				t.Fatalf("final content is %d bytes and matches neither payload", len(got))
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("directory holds %d entries after the race, want 1", len(entries))
			}
			return
		default:
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reader: the file must always exist once created: %v", err)
			}
			var state byte
			switch {
			case bytes.Equal(got, payloadA):
				state = 'A'
			case bytes.Equal(got, payloadB):
				state = 'B'
			default:
				t.Fatalf("reader observed a torn file: %d bytes (payloads are %d)", len(got), len(payloadA))
			}
			if last != 0 && state != last {
				transitions.Add(1)
			}
			last = state
		}
	}
}

func TestWriteAtomicMissingDirectoryFails(t *testing.T) {
	t.Parallel()
	err := adapterkit.WriteAtomic(filepath.Join(t.TempDir(), "no-such-dir", "f"), []byte("x"))
	if err == nil {
		t.Fatal("WriteAtomic into a missing directory succeeded")
	}
}

func TestWriteAtomicLeavesNoTempOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G302: restoring a DIRECTORY mode; directories are 0700 (G301), not 0600
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if err := adapterkit.WriteAtomic(filepath.Join(locked, "f"), []byte("x")); err == nil {
		t.Fatal("WriteAtomic into an unwritable directory succeeded")
	}
	entries, err := os.ReadDir(locked)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failure left %d entries behind", len(entries))
	}
}

func TestReadStrictModes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mode   os.FileMode
		refuse bool
	}{
		{"0600 is read", 0o600, false},
		{"0400 is read", 0o400, false},
		{"0644 world-readable is refused", 0o644, true},
		{"0640 group-readable is refused", 0o640, true},
		{"0604 other-readable is refused", 0o604, true},
		{"0602 other-writable is refused", 0o602, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "session.json")
			content := []byte(`{"access_token":"x"}`)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			// Chmod after the write: os.WriteFile's mode passes through
			// umask, an explicit chmod does not.
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			got, err := adapterkit.ReadStrict(path)
			if tc.refuse {
				perr := asProtocol(t, err)
				if perr.Code != protocol.CodeConfig {
					t.Fatalf("code = %q, want config", perr.Code)
				}
				if exit := perr.Code.Exit(); exit != 11 {
					t.Fatalf("exit = %d, want 11", exit)
				}
				if perr.Details["reason"] != "insecure_mode" {
					t.Fatalf("details = %v, want reason=insecure_mode", perr.Details)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadStrict(%o): %v", tc.mode, err)
			}
			if !bytes.Equal(got, content) {
				t.Fatalf("content = %q, want %q", got, content)
			}
		})
	}
}

func TestReadStrictMissingFileKeepsErrNotExist(t *testing.T) {
	t.Parallel()
	_, err := adapterkit.ReadStrict(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist so callers can map missing separately from insecure", err)
	}
}

func TestReadStrictRefusesNonRegular(t *testing.T) {
	t.Parallel()
	_, err := adapterkit.ReadStrict(t.TempDir())
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeConfig || perr.Details["reason"] != "not_regular" {
		t.Fatalf("err = %v, want config/not_regular", err)
	}
}

func TestMkdirPrivate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deep := filepath.Join(root, "teams", "default")
	if err := adapterkit.MkdirPrivate(deep); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "teams"), deep} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %o, want 0700", p, fi.Mode().Perm())
		}
	}
}
