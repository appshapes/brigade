package testutil

import (
	"bytes"
	"debug/buildinfo"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runBinary executes a built binary in an isolated environment and returns
// its output.
func runBinary(t *testing.T, binary string, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Env = Env(t)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v (stderr %q)", binary, strings.Join(args, " "), err, errb.String())
	}
	return out.String(), errb.String()
}

func TestBuildProducesAnExecutable(t *testing.T) {
	t.Parallel()

	binary := Build(t, "./cmd/brigade")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("Build returned %q: %v", binary, err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("%s has mode %04o, which the owner cannot execute", binary, info.Mode().Perm())
	}
	stdout, stderr := runBinary(t, binary, "version")
	if strings.TrimSpace(stdout) == "" {
		t.Errorf("an unstamped build printed %q for `version`, want some version", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// TestBuildStampedStampsTheVersion is the check that keeps
// VersionLDFlagTarget honest. If the symbol moves, or the -ldflags
// quoting stops working, the stamped build silently falls back to the
// build-info version — so the test insists on a value nothing else in the
// tree could have produced, and insists the unstamped build differs.
func TestBuildStampedStampsTheVersion(t *testing.T) {
	t.Parallel()

	want := "0.0.0-" + RunID()
	stamped, _ := runBinary(t, BuildStamped(t, "./cmd/brigade", want), "version")
	if stamped != want+"\n" {
		t.Errorf("a stamped build printed %q, want %q", stamped, want+"\n")
	}

	unstamped, _ := runBinary(t, BuildStamped(t, "./cmd/brigade", ""), "version")
	if unstamped == stamped {
		t.Errorf("an unstamped build printed %q too, so the stamp proves nothing", unstamped)
	}
}

// TestBuildUsesTheReleaseFlags checks the claim Build's doc comment makes
// and nothing else tested: the binary a test drives is built the way the
// release builds it (plan 9.4), so a test cannot pass against an artefact
// the release would never produce. Both settings were removable with the
// whole repository staying green.
//
// The expectation is read back out of the binary by debug/buildinfo rather
// than from the argv Build assembled, so it fails if the flags stop taking
// effect as well as if they stop being passed.
func TestBuildUsesTheReleaseFlags(t *testing.T) {
	t.Parallel()

	info, err := buildinfo.ReadFile(Build(t, "./cmd/brigade"))
	if err != nil {
		t.Fatalf("reading the build info: %v", err)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	for _, want := range []struct{ key, value string }{
		// -trimpath: no path from this machine is compiled into the binary.
		{"-trimpath", "true"},
		// CGO_ENABLED=0: the static build the release ships, and the reason
		// Build must never also pass -race.
		{"CGO_ENABLED", "0"},
	} {
		if got, ok := settings[want.key]; !ok || got != want.value {
			t.Errorf("build setting %s = %q (present %v), want %q", want.key, got, ok, want.value)
		}
	}
	if _, raced := settings["-race"]; raced {
		t.Error("the binary was built with -race, which cannot work with CGO_ENABLED=0")
	}
}
