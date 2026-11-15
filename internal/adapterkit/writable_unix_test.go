//go:build !windows

// These tests take a directory's write bit away, and read a symbolic
// link: unix modes and unix links.

package adapterkit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// readOnlyDir is a directory of the test's own that takes no new file. A
// process that runs as root writes wherever it likes, so such a run proves
// nothing here and is skipped.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory whatever its mode")
	}
	dir := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G302: restoring a DIRECTORY mode so the test's cleanup can remove it
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// names lists what a directory holds.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestCanWriteAtomic: a path in a directory that takes a file passes and
// leaves nothing behind, whether or not something is at the path, and what
// is there is not touched; a directory that takes no file, one that is not
// there and a path that names a directory are each refused.
func TestCanWriteAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.ticket")
	if err := adapterkit.CanWriteAtomic(path); err != nil {
		t.Fatalf("a path in a directory of the test's own: %v", err)
	}
	if left := names(t, dir); len(left) != 0 {
		t.Fatalf("the probe left %v behind", left)
	}
	//nolint:gosec // G306: a mode the probe must leave as it is
	if err := os.WriteFile(path, []byte("what was there\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.CanWriteAtomic(path); err != nil {
		t.Fatalf("a path that holds a file: %v", err)
	}
	info, err := os.Stat(path)
	data, rerr := os.ReadFile(path) //nolint:gosec // the test's own temp file
	if err != nil || rerr != nil || string(data) != "what was there\n" || info.Mode().Perm() != 0o644 {
		t.Fatalf("the probe changed the file at the path: %q %v (%v %v)", data, info.Mode().Perm(), err, rerr)
	}
	if left := names(t, dir); len(left) != 1 {
		t.Fatalf("the probe left %v beside the file", left)
	}
	if err := adapterkit.CanWriteAtomic(dir); err == nil {
		t.Errorf("a path that names a directory passed")
	}
	if err := adapterkit.CanWriteAtomic(filepath.Join(dir, "missing", "t")); err == nil {
		t.Errorf("a path in a directory that is not there passed")
	}
	locked := readOnlyDir(t)
	if err := adapterkit.CanWriteAtomic(filepath.Join(locked, "t")); err == nil {
		t.Errorf("a path in a directory that takes no file passed")
	}
	if left := names(t, locked); len(left) != 0 {
		t.Errorf("the refused probe left %v behind", left)
	}
}

// TestCanWriteAtomicToldByTheLengthOfAName: a file system takes a name of
// so many bytes and no more, and WriteAtomic's temporary file is named
// after the file it writes, with a dot, `.tmp-` and up to ten digits more.
// With the 255 bytes most file systems give a name, a name of 239 bytes is
// the longest the write always takes, and one of 249 the shortest it never
// takes. The probe says what the write does at both, and at the lengths
// below the first that it once refused, because its own temporary file was
// longer than the write's; and it leaves nothing behind at any of them.
func TestCanWriteAtomicToldByTheLengthOfAName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	named := func(length int) string { return filepath.Join(dir, strings.Repeat("n", length)) }
	// The numbers hold where a name has 255 bytes: elsewhere the test has
	// nothing to say.
	if err := os.WriteFile(named(255), nil, 0o600); err != nil {
		t.Skipf("this file system takes no name of 255 bytes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.Repeat("m", 256)), nil, 0o600); err == nil {
		t.Skip("this file system takes a name of 256 bytes")
	}
	if err := os.Remove(named(255)); err != nil {
		t.Fatal(err)
	}
	written := 0
	for _, length := range []int{1, 22, 23, 24, 200, 222, 225, 230, 238, 239} {
		if err := adapterkit.CanWriteAtomic(named(length)); err != nil {
			t.Errorf("a name of %d bytes: the probe refuses what the write takes: %v", length, err)
		}
		if left := names(t, dir); len(left) != written {
			t.Fatalf("a name of %d bytes: the probe left something behind: %d files, want the %d written", length, len(left), written)
		}
		if err := adapterkit.WriteAtomic(named(length), []byte("x\n")); err != nil {
			t.Errorf("a name of %d bytes: the write the probe passed fails: %v", length, err)
			continue
		}
		written++
	}
	for _, length := range []int{249, 250, 255} {
		if err := adapterkit.CanWriteAtomic(named(length)); err == nil {
			t.Errorf("a name of %d bytes: the probe passes what the write is refused", length)
		}
		if err := adapterkit.WriteAtomic(named(length), []byte("x\n")); err == nil {
			t.Errorf("a name of %d bytes: the write is taken, and the test's numbers are wrong", length)
		}
		if left := names(t, dir); len(left) != written {
			t.Fatalf("a name of %d bytes: something was left behind: %d files, want the %d written", length, len(left), written)
		}
	}
}

// TestTicketFileTargetMustTakeAFile: the check `team open-move` and `team
// moved` make before any network call refuses a directory that takes no
// file and a path that names a directory, as `config`
// ticket_file_unwritable, and writes nothing that stays. A ticket file
// that is a symbolic link passes the check untouched, and the write then
// puts the ticket in the link's place: the file it pointed at keeps what
// it held.
func TestTicketFileTargetMustTakeAFile(t *testing.T) {
	t.Parallel()
	locked := readOnlyDir(t)
	refusal(t, "a directory that takes no file", adapterkit.CheckTicketFileTarget(filepath.Join(locked, "t")),
		protocol.CodeConfig, "reason", "ticket_file_unwritable", "")
	dir := t.TempDir()
	refusal(t, "a path that names a directory", adapterkit.CheckTicketFileTarget(dir),
		protocol.CodeConfig, "reason", "ticket_file_unwritable", "")

	// Positive control: the same directory, writable, passes.
	//nolint:gosec // G302: a DIRECTORY mode
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.CheckTicketFileTarget(filepath.Join(locked, "t")); err != nil {
		t.Errorf("the directory, once it takes a file: %v", err)
	}
	if left := names(t, locked); len(left) != 0 {
		t.Errorf("the check left %v behind", left)
	}

	// A symbolic link at the target: never followed, by the check or by the
	// write.
	elsewhere := filepath.Join(dir, "someone-elses-file")
	if err := os.WriteFile(elsewhere, []byte("not a ticket\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ops.ticket")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.CheckTicketFileTarget(link); err != nil {
		t.Fatalf("a target that is a symbolic link: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the check replaced the link (%v %v)", info, err)
	}
	if err := adapterkit.WriteTicketFile(link, teamTicket); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.Mode().IsRegular() || !adapterkit.OwnerReadWriteOnly(info) {
		t.Fatalf("the ticket file is not a regular file for its owner alone in the link's place: %v %v", info, err)
	}
	if data, err := os.ReadFile(elsewhere); err != nil || string(data) != "not a ticket\n" { //nolint:gosec // the test's own temp file
		t.Errorf("the write followed the link: the file it pointed at holds %q (%v)", data, err)
	}
	if data, err := os.ReadFile(link); err != nil || string(data) != teamTicket+"\n" { //nolint:gosec // the test's own temp file
		t.Errorf("the ticket file does not hold the ticket: %v", err)
	}

	// A link to a directory, and a link to nothing, are links: the path names
	// no directory, so the check passes, and the write puts the ticket in the
	// link's place. The directory the first pointed at holds what it held.
	pointedAt := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(pointedAt, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"a directory": pointedAt, "nothing": filepath.Join(dir, "missing")} {
		link := filepath.Join(dir, "linked-to-"+filepath.Base(target)+".ticket")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := adapterkit.CheckTicketFileTarget(link); err != nil {
			t.Errorf("a target that is a symbolic link to %s: %v", name, err)
			continue
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the check replaced the link to %s (%v %v)", name, info, err)
		}
		if err := adapterkit.WriteTicketFile(link, teamTicket); err != nil {
			t.Errorf("the write over a link to %s: %v", name, err)
			continue
		}
		if info, err := os.Lstat(link); err != nil || !info.Mode().IsRegular() || !adapterkit.OwnerReadWriteOnly(info) {
			t.Errorf("the ticket file is not a regular file for its owner alone in the place of the link to %s: %v %v", name, info, err)
		}
	}
	if left := names(t, pointedAt); len(left) != 0 {
		t.Errorf("the check or the write put %v in the directory a link pointed at", left)
	}
	if _, err := os.Lstat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("the check or the write made the file a link to nothing pointed at (%v)", err)
	}
	for _, name := range names(t, dir) {
		if strings.Contains(name, ".probe-") || strings.Contains(name, ".tmp-") {
			t.Errorf("the checks left %s behind", name)
		}
	}
}
