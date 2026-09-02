package cases

import (
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c01Describe: `describe` on a scratch principal with BRIGADE_TEST_OFFLINE=1
// answers ok, protocol_version "1", profile.state unconfigured, exit 0,
// creates nothing under the principal's config and state directories and
// does not create <run>/shared (4.2, 4.4.1, 4.5.13); the stdout discipline
// of 4.1 is the launcher's global check on the same spawn. B-1: the same
// describe with stdin a pipe that is never closed returns within the
// timeout, so the command does not read stdin.
func c01Describe() conformance.Case {
	return conformance.Case{
		ID:    "C-01",
		Rule:  "4.2 describe offline",
		Title: "describe with no profile: ok, unconfigured, no files created, no stdin read",
		Tags:  []string{conformance.TagCore},
		Run:   runC01,
	}
}

func runC01(t *conformance.T) {
	// The start-of-run describe ran before any case on its own fresh
	// principal, so whatever it left behind is describe's doing whatever
	// order the cases run in (measured: an adapter whose describe creates
	// the store root is caught here, and only here, when C-01 is not the
	// first case).
	for _, what := range t.DescribeTouched() {
		t.Errorf("the start-of-run describe %s; describe must touch nothing (4.2)", what)
	}
	p := t.Scratch("c01")
	sharedBefore := snapshotShared(t)
	r := t.ExecEnv(p, []string{"BRIGADE_TEST_OFFLINE=1"}, nil, "describe")
	var d protocol.DescribeResult
	t.OK(r, &d)
	if d.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("describe: protocol_version %q, want %q", d.ProtocolVersion, protocol.ProtocolVersion)
	}
	if d.Profile.State != protocol.ProfileStateUnconfigured {
		t.Errorf("describe: profile.state %q, want %q", d.Profile.State, protocol.ProfileStateUnconfigured)
	}
	if d.Profile.TeamRef != "" || d.Profile.TeamName != "" || d.Profile.PrincipalRef != "" || d.Profile.HumanLabel != "" {
		t.Errorf("describe: profile carries team members while unconfigured (4.4.1)")
	}
	assertEmptyDir(t, "config dir", p.ConfigDir)
	assertEmptyDir(t, "state dir", p.StateDir)
	assertNothingAdded(t, sharedBefore)

	// B-1: stdin held open and never written; the command must not read it.
	// The same offline environment as the first describe (measured: without
	// it this second spawn was the one describe of the case that an adapter
	// could answer from the network unnoticed).
	r2 := t.ExecStdinOpenEnv(p, []string{"BRIGADE_TEST_OFFLINE=1"}, "describe")
	var d2 protocol.DescribeResult
	t.OK(r2, &d2)
	if d2.Profile.State != protocol.ProfileStateUnconfigured {
		t.Errorf("describe with stdin held open: profile.state %q, want unconfigured", d2.Profile.State)
	}
}

// sharedSnapshot is the set of entries under <run>/shared (relative paths)
// before a describe, or nil when the directory did not exist.
type sharedSnapshot struct {
	dir     string
	existed bool
	entries map[string]bool
}

// snapshotShared records what <run>/shared holds before the spawn. In id
// order C-01 runs first and the directory does not exist yet; in any other
// order an earlier case's store is already there, and describe is judged
// on what it ADDED, never on what it found (measured: the bare "does not
// exist afterwards" check failed C-01 in every shuffled run).
func snapshotShared(t *conformance.T) *sharedSnapshot {
	dir := t.SharedDir()
	if dir == "" {
		return nil
	}
	snap := &sharedSnapshot{dir: dir, entries: map[string]bool{}}
	if _, err := os.Lstat(dir); err != nil {
		return snap
	}
	snap.existed = true
	_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err == nil {
			snap.entries[path] = true
		}
		return nil
	})
	return snap
}

// assertNothingAdded records a failure when describe created <run>/shared
// or added any entry under it (4.2: describe touches nothing).
func assertNothingAdded(t *conformance.T, snap *sharedSnapshot) {
	if snap == nil {
		return
	}
	if _, err := os.Lstat(snap.dir); err != nil {
		return
	}
	if !snap.existed {
		t.Errorf("describe created the shared directory %s; describe must touch nothing (4.2)", snap.dir)
		return
	}
	added := 0
	_ = filepath.WalkDir(snap.dir, func(path string, _ os.DirEntry, err error) error {
		if err == nil && !snap.entries[path] {
			added++
		}
		return nil
	})
	if added > 0 {
		t.Errorf("describe added %d entries under the shared directory; describe must touch nothing (4.2)", added)
	}
}

// assertEmptyDir records a failure when dir holds any entry.
func assertEmptyDir(t *conformance.T, what, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("%s %s: %v", what, dir, err)
		return
	}
	if len(entries) > 0 {
		t.Errorf("describe wrote %d entries under the %s; want none (4.2)", len(entries), what)
	}
}
