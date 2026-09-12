package conformance_test

import (
	"encoding/json/v2"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/conformance/cases"
	"github.com/appshapes/brigade/internal/testutil"
)

// conformanceSeed is the --shuffle seed TestConformanceFSWholeRun uses. It
// defaults to the clock, so every `go test` run exercises a DIFFERENT case
// order and an order dependency is found by the gate rather than by a
// verifier with a private tool; the seed is logged, and
//
//	go test ./internal/conformance -run TestConformanceFSWholeRun -conformance-seed=<n>
//
// replays exactly the order that failed.
var conformanceSeed = flag.Int64("conformance-seed", 0,
	"case order for TestConformanceFSWholeRun (0: derived from the clock at test start)")

// wholeRunPass, wholeRunFail and wholeRunSkip are the counts the fs
// adapter must produce for the whole non-slow run: every case but C-14
// passes (C-44 included: the fs adapter advertises session.model and
// session.context_used_tokens), C-14 is the one skip (it is tagged slow),
// nothing fails.
const (
	wholeRunPass = 45
	wholeRunFail = 0
	wholeRunSkip = 1
)

// runSuite runs the suite in-process against adapter with the given
// selection and returns the exit code and the parsed report. The test
// process's own environment is handed to Run only so that PATH and TMPDIR
// reach the children; nothing else of it does.
func runSuite(tb testing.TB, adapter string, opts conformance.Options) (int, conformance.Report, string) {
	tb.Helper()
	opts.Adapter = adapter
	opts.SharedEnv = "BRIGADE_FS_ROOT"
	opts.JSON = true
	if opts.Timeout == 0 {
		opts.Timeout = conformance.DefaultTimeout
	}
	var stdout, stderr strings.Builder
	code := conformance.Run(tb.Context(), opts, cases.All(), os.Environ(), &stdout, &stderr)
	var rep conformance.Report
	if err := json.Unmarshal([]byte(stdout.String()), &rep); err != nil {
		tb.Fatalf("stdout is not the JSON report: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	return code, rep, stderr.String()
}

// TestConformanceFS runs every case against the fs adapter, one subtest
// per case in its own run directory. A skip is a failure here: the fs
// adapter advertises every capability the cases need, and the slow cases
// run unless -short.
func TestConformanceFS(t *testing.T) {
	t.Parallel()
	adapter := testutil.Build(t, "./cmd/brigade-adapter-fs")
	for _, c := range cases.All() {
		t.Run(c.ID, func(t *testing.T) {
			t.Parallel()
			if c.IsSlow() && testing.Short() {
				t.Skip("slow case under -short")
			}
			code, rep, stderr := runSuite(t, adapter, conformance.Options{Only: []string{c.ID}, Slow: !testing.Short()})
			if code != conformance.ExitPass || len(rep.Results) != 1 || rep.Results[0].Status != conformance.StatusPass {
				t.Fatalf("%s: exit %d, results %+v\n%s", c.ID, code, rep.Results, stderr)
			}
		})
	}
}

// TestConformanceFSWholeRun is the shape `make test` uses: every non-slow
// case in one run directory, exit 0, and the wall time LOGGED, never
// asserted. Measured unloaded from the binary: 20.3 s, of which 8 s are
// the quiet windows the brief mandates (C-36's 5 s ExpectNone, C-41's 2 s
// restart window, C-33's 1 s) and the rest ~500 adapter spawns; a -race
// build adds to it, and the machine is the rest of the input — the same
// 45 passing cases took 1m44 at load average 20 and 4m14 beside two
// concurrent per-case loops (2026-09-11), which is where a 120 s ceiling
// failed with every case green, after a 30 s one had tripped at 30.75 s.
// So there is no ceiling: a hang is caught by the package's -timeout, a
// regression by the driver reading the logged number (the execution log
// records it) and by `make test`'s separate brigade-conformance run. The
// test is deliberately NOT parallel: Go runs the sequential tests of a
// package before resuming the parallel ones, so this run is measured on an
// otherwise idle process rather than alongside forty-six per-case runs
// and four mutant builds, which doubled its wall time when it was parallel.
//
// It runs SHUFFLED, with a seed that changes on every invocation. The
// cases share a fixture, a store and the adapter's rate budgets, so id
// order is one order out of 46! and a case that silently depends on
// running after another one passes for years in id order — the P1-6
// verifier found exactly two such cases (C-01 and C-12) with a throwaway
// shuffling tool that the binary itself did not have. The seed is logged
// on every run, so a failure is replayed with -conformance-seed.
func TestConformanceFSWholeRun(t *testing.T) {
	adapter := testutil.Build(t, "./cmd/brigade-adapter-fs")
	seed := *conformanceSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	t.Logf("case order: --shuffle %d (replay with -conformance-seed=%d)", seed, seed)
	start := time.Now()
	code, rep, stderr := runSuite(t, adapter, conformance.Options{Shuffle: seed})
	wall := time.Since(start)
	t.Logf("whole fs run: %d pass, %d fail, %d skip in %s", rep.Summary.Pass, rep.Summary.Fail, rep.Summary.Skip, wall)
	if code != conformance.ExitPass || rep.Summary.Fail != wholeRunFail {
		t.Fatalf("exit %d with seed %d\n%s", code, seed, stderr)
	}
	if rep.Shuffle != seed {
		t.Fatalf("the report says seed %d, ran %d", rep.Shuffle, seed)
	}
	want := conformance.Summary{Pass: wholeRunPass, Fail: wholeRunFail, Skip: wholeRunSkip}
	if rep.Summary != want {
		t.Fatalf("summary %+v, want %+v (seed %d)", rep.Summary, want, seed)
	}
	if rep.Summary.Pass+rep.Summary.Skip != len(cases.All()) {
		t.Fatalf("summary does not add up: %+v of %d cases", rep.Summary, len(cases.All()))
	}
}
