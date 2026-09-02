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

// TestRunInterceptsMultiCallEntrypoints proves the seam is live: `hook`
// and `watch` are not unknown commands, and each names the plan task that
// will implement it rather than panicking.
func TestRunInterceptsMultiCallEntrypoints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ args []string }{
		{[]string{"hook", "session-start"}},
		{[]string{"watch", "--sink", "/dev/null"}},
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

// TestRunDispatchesTheBundledAdapter pins the P2-6 seam: `brigade adapter
// supabase …` reaches the Supabase adapter's own dispatcher, which speaks
// BAP/1 on stdout — here a `describe` on an empty configuration
// directory, ok with profile.state unconfigured and exit 0 (4.2, C-01)
// — and a missing or unknown adapter name is `usage` (exit 2) through the
// table's reporter, with the envelope on stdout under --json and nothing
// on stdout without it.
func TestRunDispatchesTheBundledAdapter(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=" + t.TempDir(), "BRIGADE_CONFIG_DIR=" + t.TempDir(), "BRIGADE_TEST_OFFLINE=1"}
	exit, stdout, stderr := run(t, env, "adapter", "supabase", "describe")
	if exit != 0 {
		t.Fatalf("adapter supabase describe: exit = %d, want 0 (stdout %q, stderr %q)", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, `"ok":true`) || !strings.Contains(stdout, `"state":"unconfigured"`) {
		t.Errorf("adapter supabase describe: stdout = %q, want an ok envelope with profile.state unconfigured", stdout)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Errorf("adapter supabase describe: stdout is not exactly one line: %q", stdout)
	}

	for _, tc := range []struct {
		args []string
		json bool
	}{
		{[]string{"adapter"}, false},
		{[]string{"adapter", "nosuch", "describe"}, false},
		{[]string{"adapter", "nosuch", "describe", "--json"}, true},
	} {
		exit, stdout, stderr := run(t, env, tc.args...)
		if exit != 2 {
			t.Errorf("%v: exit = %d, want 2 (usage)", tc.args, exit)
		}
		if tc.json {
			if !strings.Contains(stdout, `"code":"usage"`) {
				t.Errorf("%v: stdout = %q, want a usage envelope", tc.args, stdout)
			}
		} else {
			if stdout != "" {
				t.Errorf("%v: stdout = %q, want empty without --json", tc.args, stdout)
			}
			if !strings.Contains(stderr, "usage") {
				t.Errorf("%v: stderr = %q, want a usage line", tc.args, stderr)
			}
		}
	}

	// The adapter's own poison scan applies after the name (4.5.14, C-05):
	// the value reaches neither stream.
	exit, stdout, stderr = run(t, env, "adapter", "supabase", "session", "list", "--join-secret", "brg1.x.NOTREAL")
	if exit != 2 || !strings.Contains(stdout, `"code":"usage"`) {
		t.Errorf("poison flag: exit %d stdout %q, want exit 2 and a usage envelope", exit, stdout)
	}
	if strings.Contains(stdout+stderr, "NOTREAL") {
		t.Errorf("poison flag: the argv value was echoed")
	}
}
