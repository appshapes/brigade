package tscmd

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// envelope is the shape the scripts actually read fields out of: the 4.3
// result envelope of a failing `brigade --json` call.
const envelope = `{"ok":false,"protocol_version":"1",
  "error":{"code":"usage","message":"unknown command","retryable":false,
           "details":{"reason":"not_implemented"}},
  "counts":[7,8,9],"nothing":null,"ratio":1.5}`

func TestLookupReadsScalars(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ path, want string }{
		{".ok", "false"},
		{".protocol_version", "1"},
		{".error.code", "usage"},
		{".error.message", "unknown command"},
		{".error.retryable", "false"},
		{".error.details.reason", "not_implemented"},
		{".counts.0", "7"},
		{".counts.2", "9"},
		{".nothing", "null"},
		{".ratio", "1.5"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			got, err := lookup([]byte(envelope), tc.path)
			if err != nil {
				t.Fatalf("lookup(%s): %v", tc.path, err)
			}
			if got != tc.want {
				t.Errorf("lookup(%s) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestLookupRefusesWhatItCannotCompare is the important half. Every case
// here is a way an assertion could otherwise pass, or fail for a reason the
// message would not name: a member that was renamed away, a path into a
// value that has no single form, output that is not JSON at all.
func TestLookupRefusesWhatItCannotCompare(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, doc, path, wantErrContains string
	}{
		{"missing member", envelope, ".error.reason", "no such member"},
		{"missing member lists what is there", envelope, ".error.reason", `"code"`},
		{"missing top-level member", envelope, ".nope", "no such member"},
		{"descending into a string", envelope, ".error.code.deeper", "is not an object"},
		{"whole object", envelope, ".error", "no single value"},
		{"whole array", envelope, ".counts", "no single value"},
		{"array index out of range", envelope, ".counts.9", "outside the 3-element array"},
		{"array needs an index", envelope, ".counts.first", "needs an index"},
		{"path without a leading dot", envelope, "error.code", "must start with a dot"},
		{"empty segment", envelope, ".error..code", "empty segment"},
		{"not json", "brigade failed (usage): no\n", ".ok", "not one JSON document"},
		{"empty document", "", ".ok", "not one JSON document"},
		{"two documents", "{\"a\":1}\n{\"a\":2}\n", ".a", "not one JSON document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := lookup([]byte(tc.doc), tc.path)
			if err == nil {
				t.Fatalf("lookup(%s) = %q, want an error", tc.path, got)
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("lookup(%s) failed with %q, want it to mention %q", tc.path, err, tc.wantErrContains)
			}
		})
	}
}

// TestLookupAcceptsATrailingNewline covers what a command's stdout actually
// looks like: the envelope writer ends every document with one.
func TestLookupAcceptsATrailingNewline(t *testing.T) {
	t.Parallel()

	got, err := lookup([]byte(`{"ok":true}`+"\n"), ".ok")
	if err != nil || got != "true" {
		t.Errorf("lookup = (%q, %v), want (\"true\", nil)", got, err)
	}
}

func TestExpandVars(t *testing.T) {
	t.Parallel()

	env := map[string]string{"VERSION": "0.1.0", "WORK": "/tmp/w", "$": "$", "EMPTY": ""}
	get := func(name string) string { return env[name] }

	if got, err := expandVars("v=$VERSION at ${WORK}/x", get); err != nil || got != "v=0.1.0 at /tmp/w/x" {
		t.Errorf("expandVars = (%q, %v)", got, err)
	}
	if got, err := expandVars("a literal $$ sign", get); err != nil || got != "a literal $ sign" {
		t.Errorf("expandVars($$) = (%q, %v)", got, err)
	}
	if got, err := expandVars("no variables here", get); err != nil || got != "no variables here" {
		t.Errorf("expandVars(plain) = (%q, %v)", got, err)
	}
}

// TestExpandVarsRefusesAnUnsetVariable is why expandVars exists at all
// instead of a bare os.Expand: silently expanding to nothing is how a
// fixture ends up asserting nothing.
func TestExpandVarsRefusesAnUnsetVariable(t *testing.T) {
	t.Parallel()

	get := func(string) string { return "" }
	for _, in := range []string{"$MISSING", "prefix $MISSING suffix", "${MISSING}", "$SET_BUT_EMPTY"} {
		got, err := expandVars(in, get)
		if err == nil {
			t.Errorf("expandVars(%q) = %q, want an error", in, got)
		}
	}
	if _, err := expandVars("$A and $B", get); err == nil || !strings.Contains(err.Error(), "$A, $B") {
		t.Errorf("expandVars named %v, want both missing variables", err)
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	if code, ok := exitCode(nil); code != 0 || !ok {
		t.Errorf("exitCode(nil) = (%d, %v), want (0, true)", code, ok)
	}
	// A command that never started: no status to report, and reporting 0
	// would turn "the binary is missing" into "it succeeded".
	if code, ok := exitCode(exec.ErrNotFound); code != 0 || ok {
		t.Errorf("exitCode(ErrNotFound) = (%d, %v), want (0, false)", code, ok)
	}
}

// TestExitCodeReadsARealFailure runs a real process rather than fabricating
// an *exec.ExitError, because a fabricated one does not behave like a real
// one: a zero os.ProcessState reports ExitCode() == 0 on this toolchain, so
// a test built on it would exercise the success branch while claiming to
// exercise the failure branch. The exact status is asserted by the txtar
// script, which drives `status 7`, `status 2` and `status 1`; here the
// claim is only that a real failure is reported as one.
func TestExitCodeReadsARealFailure(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), sleeperCommand[0], "not-a-time-interval")
	code, ok := exitCode(cmd.Run())
	if !ok || code == 0 {
		t.Errorf("exitCode(%v) = (%d, %v), want a non-zero status", cmd.Args, code, ok)
	}
}

// TestExitCodeRefusesASignalledProcess covers the case a script must never
// be able to assert: a process killed by a signal has no exit status, and
// reporting 0 for one would let `status 0` pass on a command that was
// killed.
func TestExitCodeRefusesASignalledProcess(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), sleeperCommand[0], sleeperCommand[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %v: %v", cmd.Args, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing %v: %v", cmd.Args, err)
	}
	if code, ok := exitCode(cmd.Wait()); code != 0 || ok {
		t.Errorf("exitCode(killed) = (%d, %v), want (0, false)", code, ok)
	}
}

// TestCommandsAreAllInstalled keeps the script-visible names and the Go
// functions from drifting apart: a renamed function that was never added
// back to the table would make every script using it fail with "unknown
// command", far from the cause.
func TestCommandsAreAllInstalled(t *testing.T) {
	t.Parallel()

	want := []string{"expand", "json", "jsonenv", "sleeper", "status"}
	var got []string
	for name := range Commands() {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("Commands() installs %v, want %v", got, want)
	}
}
