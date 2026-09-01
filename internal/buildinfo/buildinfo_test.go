package buildinfo

import (
	"bytes"
	binaryinfo "debug/buildinfo"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// reader returns a debug.ReadBuildInfo stand-in reporting mainVersion.
func reader(mainVersion string, ok bool) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		if !ok {
			return nil, false
		}
		info := &debug.BuildInfo{}
		info.Main.Path = "github.com/appshapes/brigade"
		info.Main.Version = mainVersion
		return info, true
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stamped string
		main    string
		ok      bool
		want    string
	}{
		// The `make build` / goreleaser path: -X wins outright.
		{"ldflags stamp wins", "0.0.0-dev", "v9.9.9", true, "0.0.0-dev"},
		{"ldflags stamp wins over absent build info", "0.1.0", "", false, "0.1.0"},

		// The `go install ...@v0.1.0` path: no -X, so the module version
		// recorded in the binary's build info is what gets reported.
		{"go install fallback", "", "v0.1.0", true, "v0.1.0"},
		{"go install pseudo-version fallback", "", "v0.0.0-20260831030451-39dc44e69c28", true, "v0.0.0-20260831030451-39dc44e69c28"},

		// Neither source knows anything.
		{"devel is not a version", "", "(devel)", true, Unknown},
		{"empty main version", "", "", true, Unknown},
		{"no build info at all", "", "", false, Unknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolve(tc.stamped, reader(tc.main, tc.ok)); got != tc.want {
				t.Errorf("resolve(%q, build info %q ok=%v) = %q, want %q", tc.stamped, tc.main, tc.ok, got, tc.want)
			}
		})
	}
}

// TestResolveNilInfo covers the (nil, true) return that the documented
// contract of debug.ReadBuildInfo does not promise but a caller must not
// panic on.
func TestResolveNilInfo(t *testing.T) {
	t.Parallel()
	got := resolve("", func() (*debug.BuildInfo, bool) { return nil, true })
	if got != Unknown {
		t.Errorf("resolve with nil build info = %q, want %q", got, Unknown)
	}
}

// TestStringNeverEmpty pins the one property every caller relies on: the
// version line is never blank, whatever this test binary was built from.
func TestStringNeverEmpty(t *testing.T) {
	t.Parallel()
	if String() == "" {
		t.Error("String() returned the empty string")
	}
}

// wantFor restates the rule of [resolve] for an unstamped binary, in the
// words of the package comment rather than by calling resolve: the module
// version recorded in the build info, unless the toolchain recorded nothing
// or the "(devel)" placeholder, in which case Unknown.
//
// It is written out rather than delegated on purpose. An expectation
// computed by calling resolve would agree with resolve however broken
// resolve became, which is exactly how the test this replaced could not
// fail: String() is literally resolve(Version, debug.ReadBuildInfo), so
// comparing the two while Version is empty compared an expression with
// itself.
func wantFor(mainVersion string) string {
	if mainVersion == "" || mainVersion == "(devel)" {
		return Unknown
	}
	return mainVersion
}

// TestStringUsesFallbackInThisBinary checks the branch String() takes in an
// unstamped binary against an independently stated expectation.
//
// It cannot tell a working fallback from one that ignores build info
// altogether, because `go test` records Main.Version as "(devel)" and both
// answers are then Unknown. TestBuiltBinaryTakesTheReadBuildInfoFallback is
// the test that can.
func TestStringUsesFallbackInThisBinary(t *testing.T) {
	t.Parallel()
	if Version != "" {
		t.Skipf("test binary was stamped with -X (Version=%q); the fallback branch is not the one running", Version)
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		t.Skip("this test binary carries no build info")
	}
	if got, want := String(), wantFor(info.Main.Version); got != want {
		t.Errorf("String() = %q, want %q for build info Main.Version %q", got, want, info.Main.Version)
	}
}

// TestBuiltBinaryTakesTheReadBuildInfoFallback is the P1-1 acceptance item,
// done the only way that means anything: a real unstamped build of
// ./cmd/brigade, run as a child process, with the expectation read out of
// the binary itself by debug/buildinfo rather than computed by the code
// under test.
//
// This is the `go install github.com/appshapes/brigade/cmd/brigade@v0.1.0`
// shape — no -X, so buildinfo has to fall back — and it is the only place
// the fallback is shown to produce a real version. Everything else in this
// file drives resolve with a hand-made debug.BuildInfo, which proves the
// rule but not that String() consults the toolchain at all: with
// `debug.ReadBuildInfo` replaced by a reader returning (nil, false),
// `go test ./...` stayed green across the whole repository until this test
// existed.
func TestBuiltBinaryTakesTheReadBuildInfoFallback(t *testing.T) {
	t.Parallel()

	binary := testutil.Build(t, "./cmd/brigade") // no -ldflags: Version stays empty
	info, err := binaryinfo.ReadFile(binary)
	if err != nil {
		t.Fatalf("reading the build info of %s: %v", binary, err)
	}
	recorded := info.Main.Version

	printed := versionOf(t, binary)
	if want := wantFor(recorded); printed != want {
		t.Errorf("the built binary printed %q; its build info records Main.Version %q, so it should print %q",
			printed, recorded, want)
	}

	t.Run("the fallback reports a real version", func(t *testing.T) {
		t.Parallel()
		if wantFor(recorded) == Unknown {
			t.Skipf("this build recorded Main.Version = %q, so a working fallback and one that "+
				"ignored build info entirely would both print %q; nothing here can tell them apart. "+
				"Build inside a version-controlled checkout to exercise it.", recorded, Unknown)
		}
		if printed == Unknown {
			t.Errorf("the built binary printed %q although its build info records Main.Version %q; "+
				"the debug.ReadBuildInfo fallback is not being consulted", Unknown, recorded)
		}
	})
}

// versionOf runs `<binary> version` and returns the single line it printed.
func versionOf(t *testing.T, binary string) string {
	t.Helper()
	//nolint:gosec // G204: the argv is a binary this test just built; no shell is involved
	cmd := exec.CommandContext(t.Context(), binary, "version")
	cmd.Env = testutil.Env(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s version: %v (stderr %q)", binary, err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("%s version wrote %q to stderr, want empty", binary, stderr.String())
	}
	out := stdout.String()
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("%s version printed %q, want exactly one newline-terminated line", binary, out)
	}
	return strings.TrimSuffix(out, "\n")
}
