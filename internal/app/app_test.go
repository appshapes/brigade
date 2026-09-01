package app

import (
	"bytes"
	"strings"
	"testing"
)

// run drives one invocation against in-memory streams.
func run(t *testing.T, environ []string, args ...string) (exit int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	exit = Run(args, strings.NewReader(""), &out, &errb, environ)
	return exit, out.String(), errb.String()
}

func TestRunDispatchesToTheCommandTable(t *testing.T) {
	t.Parallel()
	exit, stdout, stderr := run(t, nil, "version")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", exit, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Errorf("stdout = %q, want a version", stdout)
	}
}

// TestRunInterceptsMultiCallEntrypoints proves the seam is live: `hook`,
// `watch` and `adapter` are not unknown commands, and each names the plan
// task that will implement it rather than panicking.
func TestRunInterceptsMultiCallEntrypoints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ args []string }{
		{[]string{"hook", "session-start"}},
		{[]string{"watch", "--sink", "/dev/null"}},
		{[]string{"adapter", "supabase", "describe"}},
	} {
		exit, stdout, stderr := run(t, nil, tc.args...)
		if exit != 1 {
			t.Errorf("%v: exit = %d, want 1", tc.args, exit)
		}
		if stdout != "" {
			t.Errorf("%v: stdout = %q, want empty without --json", tc.args, stdout)
		}
		if !strings.Contains(stderr, "not implemented yet") {
			t.Errorf("%v: stderr = %q", tc.args, stderr)
		}
	}
}

// TestRunNeverPanics walks a spread of malformed and hostile argv shapes.
// The contract every caller depends on — a hook, the bootstrap, the Bash
// tool — is that the process returns a status, never a stack trace.
func TestRunNeverPanics(t *testing.T) {
	t.Parallel()
	shapes := [][]string{
		nil,
		{},
		{""},
		{"--"},
		{"--", "--"},
		{"-"},
		{"---json"},
		{"version", "--json", "--json"},
		{"hook"},
		{"adapter"},
		{"watch", "--"},
		{"help", "--all", "--json"},
		{"send", "--reply-to"},
		{strings.Repeat("x", 4096)},
		{"version", strings.Repeat("y", 4096)},
	}
	for _, args := range shapes {
		exit, stdout, stderr := run(t, nil, args...)
		if exit < 0 || exit > 125 {
			t.Errorf("%v: exit = %d, outside the 0..125 range adapters may use (4.6)", args, exit)
		}
		if exit != 0 && stdout == "" && stderr == "" {
			t.Errorf("%v: failed with exit %d and said nothing on either stream", args, exit)
		}
	}
}

// TestVersionIgnoresTheEnvironment says what it can actually prove: a
// command that reads no configuration answers the same whatever environment
// it is handed, so nothing about the binary's version output depends on the
// developer's own session.
//
// It is deliberately NOT named for the 3.2 isolation rule. The old name here
// claimed Run "reads the environment it is given", which this cannot show:
// the assertion is that the two answers AGREE, so it passes unchanged if Run
// discards its environ slice entirely — which it did, silently, when that
// was tried. The threading itself is asserted where it can be observed, by
// TestDispatchHandsTheEnvironmentToTheCommandSerial in internal/cli, since
// no command in the P1-1 table reads the environment at all. When one does
// (P2-8's `profile`, P3-4's hooks), the end-to-end assertion belongs here.
func TestVersionIgnoresTheEnvironment(t *testing.T) {
	t.Parallel()
	poisoned := []string{"BRIGADE_PROFILE=not-mine", "CLAUDE_CONFIG_DIR=/nonexistent"}
	exitA, outA, _ := run(t, nil, "version")
	exitB, outB, _ := run(t, poisoned, "version")
	if exitA != exitB || outA != outB {
		t.Errorf("version differed with an environment: (%d,%q) vs (%d,%q)", exitA, outA, exitB, outB)
	}
}
