package conformance

import (
	"context"
	"encoding/json/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// This file holds the fixtures the unit tests share: fake adapters written
// as shell scripts (a shebang line and an argv array, never a shell string
// built from test data) and launched as `/bin/sh <script>` (viaShell,
// shellCommand), a describe document the fakes can answer, a runner wired
// to one of them, and the one deadline every wait on a fake uses.

// fakeDescribe is the envelope a fake adapter prints for `describe`.
func fakeDescribe(tb testing.TB, capabilities ...string) string {
	tb.Helper()
	if capabilities == nil {
		capabilities = []string{}
	}
	d := protocol.DescribeResult{
		ProtocolVersion: protocol.ProtocolVersion,
		Adapter:         protocol.AdapterInfo{Name: "fake-adapter", Version: "9.9.9"},
		Delivery: protocol.DeliveryInfo{
			Guarantee: protocol.GuaranteeAtLeastOnce, Ordering: "none", AckState: protocol.AckStateInjected,
		},
		Capabilities: capabilities,
		Limits:       protocol.DefaultLimits(),
		Lease:        protocol.DefaultLease(),
		Retention:    protocol.DefaultRetention(),
		Profile:      protocol.ProfileInfo{Name: "default", State: protocol.ProfileStateUnconfigured},
	}
	result, err := json.Marshal(&d)
	if err != nil {
		tb.Fatal(err)
	}
	env := protocol.Envelope{OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: result}
	b, err := json.Marshal(&env)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// fakeDeadline bounds every wait on a fake that is only AWAITED — its line
// read, its exit reaped — and never measured. It is a hang catcher: a fake
// that has not printed or exited in 30 s is stuck, and the assertion
// behind the wait (a parsed line, an exit status, a note) does not change
// with how long the fake took under that bound. It replaced 5 s bounds a
// loaded machine crossed — 28 failures in 20 runs at load average 15–21
// (2026-09-11), every one a fake that had not yet run, none a wrong
// answer. viaShell removed the cause; this constant makes a stuck fake
// read as stuck instead of as a pass or a timeout.
const fakeDeadline = 30 * time.Second

// usageEnvelope is a failing envelope a fake adapter prints for anything
// it does not implement.
const usageEnvelope = `{"ok":false,"protocol_version":"1","error":{"code":"usage","message":"fake adapter","retryable":false}}`

// writeScript writes an executable shell script into a directory owned by
// the test and returns its path. Every fake goes through it, and it goes
// through testutil.WriteExecutable: the package's tests fork in parallel,
// and runs 34516308749 and 34537042672 lost a fake's first spawn to
// ETXTBSY on a script a sibling's fork still held open (the mechanism and
// the numbers are on the helper). Since viaShell the shell READS every
// fake and nothing execs it, so the ForkLock hold here is belt-and-braces
// and the 0700 mode is not load-bearing; the helper stays because the
// tree-wide witness TestNoTestWritesAnExecutableAnyOtherWay requires it.
func writeScript(tb testing.TB, name, body string) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), name)
	testutil.WriteExecutable(tb, path, []byte("#!/bin/sh\n"+body))
	return path
}

// viaShell launches a fake written by writeScript as `/bin/sh <script>`:
// the script becomes the first fixed argument, so the freshly written
// file is READ by the system shell and never exec'd itself, and the fake
// sees the same argv ($1 is still the command). Exec'ing a freshly written
// file is what both failure mechanisms need. macOS assesses a new
// executable on its first exec (syspolicyd: "Doing an XProtect scan b/c
// not exempt"), a user-space scan that competes with the load: 25 fresh
// scripts exec'd at once took p50 4.3 s / max 7.4 s to print one line at
// load average 20 and p50 1.7 s / max 3.1 s at 0.8, against p50 8–16 ms /
// max 23 ms read through /bin/sh, while exec.Cmd.Start returned in
// 2–30 ms either way — so the whole cost sat inside the tests' 5 s
// windows. Linux refuses the exec with ETXTBSY while a concurrent fork
// still holds the writer's fd (testutil.WriteExecutable). /bin/sh itself
// is assessed once for the life of the machine and nothing holds it open
// for writing. Under one load window the unmodified tree failed 28 times
// in 20 runs; the shell launch alone, every 5 s bound untouched, 0 in 20.
func viaShell(script string, opts Options) Options {
	opts.Adapter = "/bin/sh"
	opts.FixedArgs = append([]string{script}, opts.FixedArgs...)
	return opts
}

// shellCommand is viaShell for a --setup or --rebind script, which
// launcher.operator runs from a whitespace-split command string. A
// t.TempDir path carries no whitespace; one that did would be split, so it
// is refused here rather than mis-run.
func shellCommand(tb testing.TB, script string) string {
	tb.Helper()
	if strings.ContainsAny(script, " \t\n") {
		tb.Fatalf("shellCommand: %q contains whitespace, which operator splits on", script)
	}
	return "/bin/sh " + script
}

// fakeAdapter is a script that answers describe with a valid document and
// every other command with a usage envelope, exit 2.
func fakeAdapter(tb testing.TB, capabilities ...string) string {
	tb.Helper()
	return writeScript(tb, "fake-adapter", `
case "$1" in
  describe) printf '%s\n' '`+fakeDescribe(tb, capabilities...)+`' ;;
  *) printf '%s\n' '`+usageEnvelope+`'; exit 2 ;;
esac
`)
}

// newTestRunner builds a runner for the unit tests: the adapter as opts
// names it (viaShell for a script; a system binary such as /usr/bin/env
// as itself), a run directory owned by the test, an explicit environment,
// and the describe result the fake answers (so the fixture's capability
// check has something to read).
func newTestRunner(tb testing.TB, opts Options, environ []string) *runner {
	tb.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	r := &runner{
		opts:     opts,
		launcher: newLauncher(opts.Adapter, opts, environ, tb.TempDir(), testLog{tb}),
		describe: &protocol.DescribeResult{Capabilities: []string{}},
		runID:    newRunID(),
	}
	return r
}

// testLog routes the launcher's -v output to the test log.
type testLog struct{ tb testing.TB }

func (l testLog) Write(p []byte) (int, error) {
	l.tb.Log(string(p))
	return len(p), nil
}

// runFake runs body as a case named T-01 through the runner and returns
// its result, exactly as Run would compute it.
func runFake(tb testing.TB, r *runner, body func(*T)) CaseResult {
	tb.Helper()
	res, launcherErr := r.runCase(context.WithoutCancel(tb.Context()), Case{ID: "T-01", Rule: "test", Run: body})
	if launcherErr != nil {
		res.Reason = "launcher: " + launcherErr.Error()
	}
	return res
}

// scratchPrincipal makes a principal under the runner's run directory.
func scratchPrincipal(tb testing.TB, r *runner, name string) *Principal {
	tb.Helper()
	p, err := r.newPrincipal(name, name)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}
