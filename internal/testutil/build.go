package testutil

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// VersionLDFlagTarget is the linker symbol the release stamps the version
// into. It is declared here so that a test asserting the stamped version
// and the Makefile's -ldflags cannot drift apart silently: if the symbol
// moves, TestBuiltBinaryReportsTheStampedVersion in cmd/brigade goes red.
const VersionLDFlagTarget = ModulePath + "/internal/buildinfo.Version"

// Build compiles a package of this repository into a directory belonging to
// the test and returns the absolute path of the binary.
//
// pkg is a package pattern relative to the repository root, for example
// "./cmd/brigade". The build uses the release flags — -trimpath and
// CGO_ENABLED=0 — so that the artefact under test is the artefact that
// ships (plan 9.4).
//
// -race is never passed: the race detector needs cgo and CGO_ENABLED=0
// turns it off, which is exactly the combination plan 7.3 forbids. The test
// process itself is race-instrumented by `go test -race`; the child it
// builds is not.
//
// The result is not cached across calls. A cache would need a directory
// outliving any single test, and testutil has no process-wide teardown hook
// to remove one; `go build` caches the link step itself, so a repeat build
// of an unchanged package is a copy.
func Build(tb testing.TB, pkg string) string {
	tb.Helper()
	return BuildStamped(tb, pkg, "")
}

// BuildStamped is [Build] with the version stamped the way the release
// ldflags stamp it. An empty version stamps nothing, which is the `go
// install` shape: buildinfo then falls back to the module version recorded
// in the binary's build info.
func BuildStamped(tb testing.TB, pkg, version string) string {
	tb.Helper()
	out := filepath.Join(tb.TempDir(), path.Base(pkg))
	args := []string{"build", "-trimpath", "-o", out}
	// Coverage across the process boundary (plan 9.4). BRIGADE_COVER=1
	// instruments the child so that a `go tool covdata` run over GOCOVERDIR
	// sees what the tests drove through the real binary rather than only
	// what the test process itself executed; CI's supabase job sets both.
	// It is opt-in because an instrumented binary run WITHOUT GOCOVERDIR
	// warns on stderr and writes nothing, and because -cover changes the
	// artefact under test — the default build stays the one that ships.
	if os.Getenv("BRIGADE_COVER") != "" {
		args = append(args, "-cover")
	}
	if version != "" {
		args = append(args, "-ldflags", "-X "+VersionLDFlagTarget+"="+version)
	}
	args = append(args, pkg)

	cmd := exec.CommandContext(tb.Context(), "go", args...)
	cmd.Dir = RepoRoot(tb)
	// The toolchain is not the thing under test: it gets the ambient
	// environment, so that GOPATH, the module cache and the build cache are
	// the developer's own. Only CGO_ENABLED is forced.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if combined, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("testutil.Build: go %s: %v\n%s", strings.Join(args, " "), err, combined)
	}
	return out
}
