// Package testutil holds the helpers the Brigade test tree shares. It is
// imported only from tests and from the test-only entrypoints of the cmd/
// packages; nothing under it is linked into the shipped binary.
//
// P1-1 scope. The plan's full list (7.1: fakesock, fakeregistry,
// fakeadapter, dotenv, …) belongs to the phases that need it. What is here
// is what P1-1 needs and what P1-1 exercises: [Env] and [Dirs] to build a
// child environment explicitly, [Build] and [BuildStamped] to compile a
// repository binary the way the release does, [RepoRoot], and [RunID].
// Anything added later should keep the same rule: a helper with no caller
// and no test is not a helper, it is a claim.
//
// Everything here is safe to call from a test that has already called
// t.Parallel: the package holds no mutable state, and every path it hands
// out is derived from the caller's own t.TempDir.
package testutil

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ModulePath is this repository's Go module path. [RepoRoot] uses it to
// tell this module's go.mod from any other one it walks past.
const ModulePath = "github.com/appshapes/brigade"

// RepoRoot returns the absolute path of the repository root: the directory
// holding the go.mod that declares [ModulePath].
//
// It walks up from the working directory rather than from the compiled-in
// path of this source file, so it keeps working when the tree is moved and
// fails loudly rather than silently pointing at a stale location.
func RepoRoot(tb testing.TB) string {
	tb.Helper()
	start, err := os.Getwd()
	if err != nil {
		tb.Fatalf("testutil.RepoRoot: getwd: %v", err)
	}
	for dir := start; ; {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && moduleOf(string(data)) == ModulePath {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatalf("testutil.RepoRoot: no go.mod declaring module %s at or above %s", ModulePath, start)
		}
		dir = parent
	}
}

// moduleOf returns the module path declared by a go.mod file, or "".
func moduleOf(gomod string) string {
	for line := range strings.Lines(gomod) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// runIDLayout is the timestamp half of a run id. The trailing Z is a
// literal: the time is always UTC.
const runIDLayout = "20060102T150405Z"

// RunID returns an identifier unique to one test run, in the form
// 20260831T142530Z-1f4b9c02 (plan 9.4).
//
// Integration tests put it in every name they create so that two runs — or
// two developers sharing one stack — never collide and no reset is needed
// between them. It is sortable by time on purpose: a leftover row says when
// it was made.
func RunID() string {
	var b [4]byte
	// crypto/rand.Read never returns an error; it panics on a broken
	// system rather than handing back predictable bytes.
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format(runIDLayout) + "-" + hex.EncodeToString(b[:])
}
