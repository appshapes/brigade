package conformance_test

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/conformance/cases"
	"github.com/appshapes/brigade/internal/testutil"
)

// slowRunGap is how far these tests move the store's clock: 125 s, the
// wall time at which C-12 actually started in the hosted run that found
// the bug (`--shuffle 5150907`, C-12 at t = 124.8 s of a 160 s run). It is
// deliberately longer than protocol.LeaseDefaultSeconds (90 s) — the lease
// the fixture used to take — and far shorter than the lease it takes now,
// so it separates the two without any test waiting for either.
const slowRunGap = 125 * time.Second

// runCaseList runs an arbitrary case list in-process against adapter, in
// the shape `make test` uses for the fs adapter (--shared-env
// BRIGADE_FS_ROOT, --json). It exists beside runSuite because these two
// tests need a list that is NOT cases.All(): one real case plus a seam.
func runCaseList(tb testing.TB, adapter string, list []conformance.Case) (int, conformance.Report, string) {
	tb.Helper()
	opts := conformance.Options{
		Adapter:   adapter,
		SharedEnv: "BRIGADE_FS_ROOT",
		JSON:      true,
		Timeout:   conformance.DefaultTimeout,
	}
	var stdout, stderr strings.Builder
	code := conformance.Run(tb.Context(), opts, list, os.Environ(), &stdout, &stderr)
	var rep conformance.Report
	if err := json.Unmarshal([]byte(stdout.String()), &rep); err != nil {
		tb.Fatalf("stdout is not the JSON report: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	return code, rep, stderr.String()
}

// caseByID returns the real case with id from the shipped case list.
func caseByID(tb testing.TB, id string) conformance.Case {
	tb.Helper()
	for _, c := range cases.All() {
		if c.ID == id {
			return c
		}
	}
	tb.Fatalf("case %s is not in cases.All()", id)
	return conformance.Case{}
}

// rewindStore moves every session in the fs adapter's store back by d:
// `lease_until`, `last_seen_at`, `created_at` and `closed_at` all lose d,
// which is what a run that took d longer looks like to an adapter that
// computes `state` from `lease_until` at read time (4.5.8). It is the
// clock seam these tests use instead of sleeping: the fs store is plain
// JSON under <run>/shared/teams/<team>/sessions/, no adapter process is
// alive between two cases, and the fixture is the only thing registered.
func rewindStore(t *conformance.T, root string, d time.Duration) {
	if root == "" {
		t.Fatalf("rewindStore: the run has no shared directory")
	}
	matches, err := filepath.Glob(filepath.Join(root, "teams", "*", "sessions", "*.json"))
	if err != nil {
		t.Fatalf("rewindStore: %v", err)
	}
	// A silent zero would make every test in this file pass vacuously.
	if len(matches) < 3 {
		t.Fatalf("rewindStore: %d session files under %s, want A, B and C's three", len(matches), root)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path) //nolint:gosec // a store this test's own run created
		if err != nil {
			t.Fatalf("rewindStore: %v", err)
		}
		var session map[string]any
		if err := json.Unmarshal(data, &session); err != nil {
			t.Fatalf("rewindStore: %s does not parse: %v", filepath.Base(path), err)
		}
		for _, key := range []string{"lease_until", "last_seen_at", "created_at", "closed_at"} {
			s, ok := session[key].(string)
			if !ok {
				continue
			}
			ts, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				t.Fatalf("rewindStore: %s.%s is not a timestamp: %v", filepath.Base(path), key, err)
			}
			session[key] = ts.Add(-d).Format(time.RFC3339Nano)
		}
		out, err := json.Marshal(session)
		if err != nil {
			t.Fatalf("rewindStore: %v", err)
		}
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatalf("rewindStore: %v", err)
		}
	}
	t.Logf("rewound %d session files by %s", len(matches), d)
}

// rewindCase is the seam case: it builds the fixture and then moves the
// store's clock back by d, so the case after it runs as if d of wall time
// had passed since the fixture was registered.
func rewindCase(d time.Duration) conformance.Case {
	return conformance.Case{
		ID:    "T-01",
		Rule:  "test seam",
		Title: "build the fixture, then age the store by " + d.String(),
		Tags:  []string{conformance.TagCore},
		Run: func(t *conformance.T) {
			t.A()
			t.B()
			t.C()
			rewindStore(t, t.SharedDir(), d)
		},
	}
}

// TestFixtureOutlivesASlowRun is the regression test for P5-15. C-12
// asserts A's fixture session is present in a LIVE `session list`
// (`include_offline = false`), nothing heartbeats the fixture, and the
// fixture used to take the protocol default of 90 s — so C-12 passed only
// when it happened to run inside the first 90 s of a run. In id order
// against the hosted Supabase project it started at t = 10.9 s and passed;
// under `--shuffle 5150907` it started at t = 124.8 s and failed with "A's
// fixture session is absent", the backend having behaved exactly as C-14
// requires. The fs suite hid it because the whole fs run is ~20 s.
//
// With the store aged by slowRunGap this reproduces in ~2 s, and it fails
// on the fixture as it was: revert fixture.register's explicit
// lease_seconds and C-12 fails here.
func TestFixtureOutlivesASlowRun(t *testing.T) {
	t.Parallel()
	adapter := testutil.Build(t, "./cmd/brigade-adapter-fs")
	code, rep, stderr := runCaseList(t, adapter, []conformance.Case{rewindCase(slowRunGap), caseByID(t, "C-12")})
	for _, res := range rep.Results {
		if res.Status != conformance.StatusPass {
			t.Errorf("%s is %s after %s of simulated wall time: %s", res.ID, res.Status, slowRunGap, res.Reason)
		}
	}
	if code != conformance.ExitPass {
		t.Fatalf("exit %d, want %d\n%s", code, conformance.ExitPass, stderr)
	}
}

// TestFixtureLeaseCoversTheSuiteBudget is the drift join on the adapter
// side: whatever the fixture asks for, what A's session actually holds
// must cover conformance.SuiteWallClockBudget. It reads `lease_until`
// against the adapter's own `server_time`, so it measures the granted
// lease rather than the requested one, and it fails if a future fixture
// takes a shorter lease, if the fs adapter narrows its advertised range,
// or if the budget is raised past what the range can grant.
func TestFixtureLeaseCoversTheSuiteBudget(t *testing.T) {
	t.Parallel()
	adapter := testutil.Build(t, "./cmd/brigade-adapter-fs")
	check := conformance.Case{
		ID:    "T-02",
		Rule:  "test seam",
		Title: "A's fixture session holds a lease that covers the suite's budget",
		Tags:  []string{conformance.TagCore},
		Run: func(t *conformance.T) {
			a := t.A()
			advertised := time.Duration(t.Describe().Lease.MaxSeconds) * time.Second
			if advertised < conformance.SuiteWallClockBudget {
				t.Errorf("describe.lease.max_seconds is %s, under the suite's %s budget", advertised, conformance.SuiteWallClockBudget)
			}
			sessions, raw := t.List(a, "", false)
			serverTime, ok := raw["server_time"].(string)
			if !ok {
				t.Fatalf("session list: server_time absent")
			}
			now, err := time.Parse(time.RFC3339Nano, serverTime)
			if err != nil {
				t.Fatalf("session list: server_time %q does not parse: %v", serverTime, err)
			}
			found := false
			for _, s := range sessions {
				if s.SessionID != t.Session(a) {
					continue
				}
				found = true
				if left := s.LeaseUntil.Sub(now); left < conformance.SuiteWallClockBudget {
					t.Errorf("A's fixture session has %s of lease left, under the suite's %s budget: a run longer than that lists it offline (4.5.8) and C-12 fails",
						left, conformance.SuiteWallClockBudget)
				}
			}
			if !found {
				t.Errorf("session list: A's fixture session is absent")
			}
		},
	}
	code, rep, stderr := runCaseList(t, adapter, []conformance.Case{check})
	for _, res := range rep.Results {
		if res.Status != conformance.StatusPass {
			t.Errorf("%s is %s: %s", res.ID, res.Status, res.Reason)
		}
	}
	if code != conformance.ExitPass {
		t.Fatalf("exit %d, want %d\n%s", code, conformance.ExitPass, stderr)
	}
}
