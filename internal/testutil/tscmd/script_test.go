package tscmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// This file is the positive control for the five commands this package
// installs. Every other test here exercises a pure helper (lookup,
// expandVars, exitCode); none of them runs Status, JSON, JSONEnv, Expand or
// Sleeper at all, and the txtar scripts that do run them can only observe
// the commands PASSING. That gap is not theoretical: with the assertion
// removed from Status — `_, _ = got, want` in place of the comparison —
// `go test ./...` stayed green across the whole repository, which means
// every `status <n> …` line in every script was being carried by an
// instrument nothing had ever shown could fail.
//
// runScript closes it by running a script under a testscript.T that RECORDS
// a failure instead of failing this test, so a script that must fail can be
// asserted as such.

// recordingT is a testscript.T that captures the outcome of a script.
//
// FailNow, Fatal and Skip end the calling goroutine with runtime.Goexit the
// way testing.T does, because testscript relies on them not returning: after
// `ts.t.FailNow()` its script loop would otherwise carry on to the next
// line. Run therefore gives every script a goroutine of its own to end.
type recordingT struct {
	mu     sync.Mutex
	failed bool
	log    strings.Builder
}

func (r *recordingT) Skip(args ...any)  { r.record("SKIP", args); runtime.Goexit() }
func (r *recordingT) Fatal(args ...any) { r.record("FATAL", args); r.fail(); runtime.Goexit() }
func (r *recordingT) Log(args ...any)   { r.record("LOG", args) }
func (r *recordingT) FailNow()          { r.fail(); runtime.Goexit() }
func (r *recordingT) Parallel()         {}
func (r *recordingT) Verbose() bool     { return false }

func (r *recordingT) Run(_ string, f func(testscript.T)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(r)
	}()
	<-done
}

func (r *recordingT) record(kind string, args []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(&r.log, "%s: %s\n", kind, fmt.Sprint(args...))
}

func (r *recordingT) fail() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = true
}

func (r *recordingT) result() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed, r.log.String()
}

// runScript runs one script with this package's commands installed and
// reports whether it failed, together with everything it logged.
func runScript(t *testing.T, script string) (failed bool, log string) {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case.txtar"), []byte(script), 0o600); err != nil {
		t.Fatalf("writing the script: %v", err)
	}

	rec := &recordingT{}
	// RunT itself may end this goroutine (its own Fatal path), so it gets
	// one that is not the test's.
	done := make(chan struct{})
	go func() {
		defer close(done)
		testscript.RunT(rec, testscript.Params{Dir: dir, Cmds: Commands()})
	}()
	<-done

	return rec.result()
}

// check runs a script and asserts the outcome, requiring a failing script to
// say why: a script that failed for an unrelated reason (a typo in a
// builtin, a missing file) would otherwise look like the control passing.
func check(t *testing.T, name, script string, wantFail bool, wantLog string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		failed, log := runScript(t, script)
		if failed != wantFail {
			t.Fatalf("script failed = %v, want %v\n--- script ---\n%s\n--- log ---\n%s", failed, wantFail, script, log)
		}
		if wantLog != "" && !strings.Contains(log, wantLog) {
			t.Errorf("log does not mention %q\n--- log ---\n%s", wantLog, log)
		}
	})
}

// TestHarnessItself is the control on the control: runScript must be able to
// report both outcomes, or every assertion below would be vacuous.
func TestHarnessItself(t *testing.T) {
	t.Parallel()
	check(t, "a passing script passes", "exec sleep 0\n", false, "")
	check(t, "a failing script fails", "! exec sleep 0\n", true, "unexpected command success")
}

// TestStatusAssertsTheExitCode is the missing positive control. `status` is
// what every script uses to pin a 4.6 exit status, and until now nothing
// showed it comparing anything.
func TestStatusAssertsTheExitCode(t *testing.T) {
	t.Parallel()

	check(t, "right code passes", "status 0 sleep 0\n", false, "")
	check(t, "wrong code fails", "status 1 sleep 0\n", true, "exited 0, want 1")
	check(t, "non-zero code passes", "status 1 sleep not-a-time-interval\n", false, "")
	check(t, "wrong non-zero code fails", "status 2 sleep not-a-time-interval\n", true, "exited 1, want 2")

	// A command that never ran has no exit status. Reporting 0 for it would
	// turn "the binary is missing" into `status 0` passing.
	check(t, "a command that cannot start fails", "status 0 no-such-program-4c1f9a\n", true, "did not run to completion")

	// Misuse must be loud, not silently permissive.
	check(t, "no arguments", "status\n", true, "usage: status")
	check(t, "one argument", "status 0\n", true, "usage: status")
	check(t, "a non-numeric code", "status two sleep 0\n", true, "is not an exit code")
	check(t, "negation is refused", "! status 0 sleep 0\n", true, "name the exit code you expect")
}

// TestStatusLeavesTheStreamsForTheNextAssertion pins the reason `status`
// exists at all rather than a wrapper around `exec`: the checks that follow
// it in a script read the stdout and stderr of the command it ran.
func TestStatusLeavesTheStreamsForTheNextAssertion(t *testing.T) {
	t.Parallel()
	check(t, "stdout survives", "status 0 echo hello\nstdout hello\n", false, "")
	check(t, "stdout is really compared", "status 0 echo hello\nstdout goodbye\n", true, "no match for")
}

// TestJSONAssertsOneField covers the arms the scripts never reach: a
// mismatch, a negation that should have fired, and a path that is not there.
func TestJSONAssertsOneField(t *testing.T) {
	t.Parallel()

	const doc = "\n-- doc.json --\n" + `{"ok":false,"error":{"code":"usage"},"n":7}` + "\n"

	check(t, "match passes", "json doc.json .error.code usage\n"+doc, false, "")
	check(t, "mismatch fails", "json doc.json .error.code internal\n"+doc, true, `is "usage", want "internal"`)
	check(t, "negation of a mismatch passes", "! json doc.json .error.code internal\n"+doc, false, "")
	check(t, "negation of a match fails", "! json doc.json .error.code usage\n"+doc, true, "says it must not be")

	// A vanished member must be an error in BOTH forms. If it were reported
	// as an empty value instead, `! json` on a renamed member would pass
	// while asserting nothing at all.
	check(t, "missing member fails", "json doc.json .error.reason x\n"+doc, true, "no such member")
	check(t, "missing member fails when negated too", "! json doc.json .error.reason x\n"+doc, true, "no such member")
	check(t, "output that is not json fails", "status 0 echo notjson\njson stdout .ok true\n", true, "not one JSON document")

	check(t, "wrong argument count", "json doc.json .ok\n"+doc, true, "usage: json")
}

// TestJSONEnvExportsTheField proves the value reaches the script environment
// and is the value that was in the document.
func TestJSONEnvExportsTheField(t *testing.T) {
	t.Parallel()

	const doc = "\n-- doc.json --\n" + `{"result":{"version":"0.4.2"}}` + "\n-- in.txt --\nv=$V\n-- want.txt --\nv=0.4.2\n"

	check(t, "exports the value", "jsonenv doc.json .result.version V\nexpand in.txt out.txt\ncmp out.txt want.txt\n"+doc, false, "")
	check(t, "a missing member fails", "jsonenv doc.json .result.nope V\n"+doc, true, "no such member")
	check(t, "negation is refused", "! jsonenv doc.json .result.version V\n"+doc, true, "meaningless here")
	check(t, "a bad variable name is refused", "jsonenv doc.json .result.version V=1\n"+doc, true, "not usable as a variable name")
}

// TestExpandRefusesAnUnsetVariable is the property that stops a fixture
// quietly expanding to nothing.
func TestExpandRefusesAnUnsetVariable(t *testing.T) {
	t.Parallel()

	const files = "\n-- in.txt --\nwork=$WORK\n-- missing.txt --\nv=$NOT_SET_4c1f9a\n"

	check(t, "a set variable expands", "expand in.txt out.txt\ngrep '^work=/' out.txt\n"+files, false, "")
	check(t, "an unset variable fails", "expand missing.txt out.txt\n"+files, true, "is unset or empty")
	check(t, "negation is refused", "! expand in.txt out.txt\n"+files, true, "meaningless here")
	check(t, "wrong argument count", "expand in.txt\n"+files, true, "usage: expand")
}

// TestSleeperExportsALivePID pins what the fixture promises: a pid, in the
// script environment, of a process that exists. cmd/brigade's script proves
// the kill(pid, 0) half through a child process; here the claim is only that
// SLEEPER_PID is set to a plausible pid rather than left empty.
func TestSleeperExportsALivePID(t *testing.T) {
	t.Parallel()

	const files = "\n-- in.txt --\npid=$SLEEPER_PID\n"

	check(t, "exports a numeric pid", "sleeper\nexpand in.txt out.txt\ngrep '^pid=[0-9]+$' out.txt\n"+files, false, "")
	// Without `sleeper` the variable is unset, so expand refuses it — which
	// is what makes the case above a real assertion rather than a match
	// against an empty string.
	check(t, "the variable comes from sleeper", "expand in.txt out.txt\n"+files, true, "is unset or empty")
	check(t, "arguments are refused", "sleeper 1\n", true, "usage: sleeper")
	check(t, "negation is refused", "! sleeper\n", true, "meaningless here")
}
