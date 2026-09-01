package testutil

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestRepoRootIsTheDirectoryHoldingThisModule(t *testing.T) {
	t.Parallel()

	root := RepoRoot(t)
	if !filepath.IsAbs(root) {
		t.Errorf("RepoRoot = %q, want an absolute path", root)
	}

	// This test runs with its own package directory as the working
	// directory, so the answer is checkable against a known relative path
	// rather than against RepoRoot's own logic.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if want := filepath.Join(root, "internal", "testutil"); want != cwd {
		t.Errorf("RepoRoot = %q, so this package would be at %q, but it is at %q", root, want, cwd)
	}

	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading the go.mod RepoRoot named: %v", err)
	}
	if got := moduleOf(string(data)); got != ModulePath {
		t.Errorf("the go.mod at %s declares module %q, want %q", root, got, ModulePath)
	}
}

func TestModuleOf(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		gomod string
		want  string
	}{
		{"plain", "module example.com/x\n\ngo 1.27.0\n", "example.com/x"},
		{"leading blank lines", "\n\nmodule example.com/x\n", "example.com/x"},
		{"indented", "\tmodule example.com/x\n", "example.com/x"},
		{"no trailing newline", "module example.com/x", "example.com/x"},
		{"first module line wins", "module a\nmodule b\n", "a"},
		{"empty", "", ""},
		{"no module directive", "go 1.27.0\n\nrequire example.com/y v1.0.0\n", ""},
		{"module inside a require path is not a directive", "require modulefoo v1.0.0\n", ""},
		// The directive is the word "module" followed by a space. A prefix
		// test against "module" alone accepts these and reports a mangled
		// path, which is how a nested module's go.mod could be mistaken for
		// this one's.
		{"a longer word starting with module is not a directive", "modulefoo example.com/x\n", ""},
		{"module with no argument is not a directive", "module\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := moduleOf(tc.gomod); got != tc.want {
				t.Errorf("moduleOf(%q) = %q, want %q", tc.gomod, got, tc.want)
			}
		})
	}
}

// runIDPattern is the shape plan 9.4 fixes for a run id.
var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

func TestRunIDHasTheDocumentedShape(t *testing.T) {
	t.Parallel()

	id := RunID()
	if !runIDPattern.MatchString(id) {
		t.Fatalf("RunID() = %q, want a match for %s", id, runIDPattern)
	}
	stamp, err := time.Parse(runIDLayout, id[:len(runIDLayout)])
	if err != nil {
		t.Fatalf("the timestamp half of %q does not parse: %v", id, err)
	}
	if since := time.Since(stamp); since < -time.Minute || since > time.Minute {
		t.Errorf("RunID() = %q, whose timestamp is %v away from now", id, since)
	}
}

// TestRunIDsDoNotRepeat is the reason the random tail exists: two runs
// inside the same second must still produce different ids, or integration
// fixtures created by two developers on one stack collide.
func TestRunIDsDoNotRepeat(t *testing.T) {
	t.Parallel()

	const n = 1000
	seen := make(map[string]struct{}, n)
	for range n {
		id := RunID()
		if _, dup := seen[id]; dup {
			t.Fatalf("RunID() returned %q twice in %d calls", id, n)
		}
		seen[id] = struct{}{}
	}
}

// TestRepoRootWalksPastANestedModule is the case RepoRoot exists for and the
// one its own test could not reach: this repository commits Go code under
// docs/research/, scripts/experiments/ and .ignored/, each inside its own
// go.mod, and a helper that stopped at the first go.mod it found would hand
// a test run from one of those directories the wrong root — and `go build`
// would then compile the wrong module.
//
// The existing test runs from internal/testutil, where the first go.mod
// above it is already this module's, so a RepoRoot that ignored ModulePath
// entirely answered identically and stayed green.
func TestRepoRootWalksPastANestedModuleSerial(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "docs", "research", "probe")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("laying out the tree: %v", err)
	}
	write := func(dir, module string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+module+"\n\ngo 1.27.0\n"), 0o600); err != nil {
			t.Fatalf("writing %s/go.mod: %v", dir, err)
		}
	}
	write(root, ModulePath)
	write(nested, "example.com/research/probe")

	t.Chdir(nested)
	got := RepoRoot(t)
	// t.TempDir can hand back a path through a symlink (/var on macOS);
	// compare what the filesystem resolves both to.
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving %s: %v", root, err)
	}
	if resolved, rerr := filepath.EvalSymlinks(got); rerr == nil {
		got = resolved
	}
	if got != want {
		t.Errorf("RepoRoot from inside a nested module = %q, want the %s root %q", got, ModulePath, want)
	}
}
