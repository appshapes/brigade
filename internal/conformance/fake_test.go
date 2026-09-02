package conformance

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// This file holds the fixtures the unit tests share: fake adapters written
// as shell scripts (a shebang line and an argv array, never a shell string
// built from test data), a describe document the fakes can answer, and a
// runner wired to one of them.

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

// usageEnvelope is a failing envelope a fake adapter prints for anything
// it does not implement.
const usageEnvelope = `{"ok":false,"protocol_version":"1","error":{"code":"usage","message":"fake adapter","retryable":false}}`

// writeScript writes an executable shell script into a directory owned by
// the test and returns its path.
func writeScript(tb testing.TB, name, body string) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil { //nolint:gosec // an executable test fixture
		tb.Fatal(err)
	}
	return path
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

// newTestRunner builds a runner for the unit tests: the adapter path as
// given, a run directory owned by the test, an explicit environment, and
// the describe result the fake answers (so the fixture's capability check
// has something to read).
func newTestRunner(tb testing.TB, adapter string, opts Options, environ []string) *runner {
	tb.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	opts.Adapter = adapter
	r := &runner{
		opts:     opts,
		launcher: newLauncher(adapter, opts, environ, tb.TempDir(), testLog{tb}),
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
