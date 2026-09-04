package testutil

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SupabaseEnv is what an integration test needs of the local stack (plan
// 9.4): the API url and the publishable key. Never the secret key, the
// service-role key or the JWT secret — those stay out of the adapter and
// out of every test that ships (7.7).
type SupabaseEnv struct {
	URL            string
	PublishableKey string
}

// EnvTestFile is the file `make supabase-env` writes from the running
// stack, gitignored, at the repository root.
const EnvTestFile = ".env.test"

// LiveTestVar is the explicit opt-in every live test in the tree waits
// on. A stack that answers is NOT consent: `make supabase-start
// supabase-env` leaves a listening stack and a .env.test behind for good,
// so before this gate existed every live test ran inside `make test` on
// any machine that had run those two recipes once — measured 2026-09-04
// on this repository's dev machine, `go test -run TestIntegration
// ./internal/adapters/supabase/` was 32 PASS / 5 SKIP / 0 FAIL in
// 55.883 s with no BRIGADE_* variable set, and under `make test`'s
// `-race -shuffle=on` whole-tree load one of them
// (TestIntegrationAdversarialBroadcastPayloadIsIdsOnly) missed its 10 s
// broadcast window and then sat on an untimed websocket read until the
// Realtime server closed the un-heartbeated socket 60 s later, failing
// the commit gate with `read: failed to get reader: failed to read frame
// header: EOF`. The reads are bounded now, and this variable is why a
// developer's `make test` no longer reaches them at all.
//
// `make test-integration` sets it (and BRIGADE_TEST_DOCKER beside it);
// CI's `supabase` job gets it from that same recipe. Nothing else sets
// it, which is what makes CLAUDE.md's "`make test` is Docker-free and
// stack-free" true on a developer's machine as well as on a bare runner.
const LiveTestVar = "BRIGADE_TEST_LIVE"

// requireLiveMessage names what to run, not what is missing.
const requireLiveMessage = LiveTestVar + " unset: the live Supabase tests are opt-in — run `make test-integration` (it sets " +
	LiveTestVar + "=1 and sources " + EnvTestFile + "), or set " + LiveTestVar + "=1 yourself with the stack up"

// requireSupabaseMessage is the documented skip reason.
const requireSupabaseMessage = "no local Supabase stack: run `make supabase-start supabase-env`"

// RequireSupabase returns the local stack's url and publishable key, or
// skips the test. Every live test is opt-in behind [LiveTestVar]: without
// it this skips before it looks at anything — no .env.test read, no
// health probe — so `make test` is Docker-free AND stack-free even on a
// machine whose local stack is up.
//
// With the opt-in set, the values come from SUPABASE_URL and
// SUPABASE_PUBLISHABLE_KEY in the process environment (`make
// test-integration` sources .env.test first), else from .env.test at the
// repository root. From here on a missing pair or a stack whose auth
// service does not answer /auth/v1/health within two seconds is a
// FAILURE, not a skip: the opt-in says a stack is expected, and a skip
// here would let `make test-integration` and CI's `supabase` job exit 0
// having executed nothing (measured 2026-09-04 before this rule: with the
// variable set and no stack, 4/4 SKIP, exit 0).
//
// The keys are read here and nowhere else in the test tree. Nothing under
// testutil is linked into the shipped binary.
func RequireSupabase(tb testing.TB) SupabaseEnv {
	tb.Helper()
	if os.Getenv(LiveTestVar) == "" {
		tb.Skip(requireLiveMessage)
	}
	env := SupabaseEnv{URL: os.Getenv("SUPABASE_URL"), PublishableKey: os.Getenv("SUPABASE_PUBLISHABLE_KEY")}
	if env.URL == "" || env.PublishableKey == "" {
		env = loadEnvTest(tb)
	}
	if env.URL == "" || env.PublishableKey == "" {
		tb.Fatal(LiveTestVar + " is set but " + requireSupabaseMessage)
	}
	if !supabaseHealthy(tb, env.URL) {
		tb.Fatal(LiveTestVar + " is set but " + requireSupabaseMessage + " (the stack at " + env.URL + " does not answer)")
	}
	return env
}

// RequireSupabaseDB returns the local stack's Postgres DSN — the
// `postgres` superuser connection `make supabase-env` writes as
// SUPABASE_DB_URL. Without [LiveTestVar] it skips like [RequireSupabase];
// with it, a missing DSN fails for the same reason a missing stack does.
//
// It exists for the fixtures of plan 9.4 that nothing on the wire can
// build: backdating `last_seen_at` or a message's `created_at`, running
// brigade.gc_expired(), and reading auth.users to confirm what a
// principal is. That is the ONLY reason the test tree talks to Postgres,
// and only from a _test.go file; everything else goes through the
// adapter. The DSN carries the local stack's postgres password, so it is
// returned, never logged: no caller may put it in a t.Log, a failure
// message or a committed file.
//
// The opt-in gate and the stack health check both run first (inside
// [RequireSupabase]), so a run without [LiveTestVar] costs nothing and a
// stale .env.test naming a stack that is not up fails on the two-second
// health probe rather than hanging on a dial.
func RequireSupabaseDB(tb testing.TB) string {
	tb.Helper()
	RequireSupabase(tb)
	dsn := os.Getenv(SupabaseDBURLVar)
	if dsn == "" {
		dsn = loadEnvTestValues(tb)[SupabaseDBURLVar]
	}
	if dsn == "" {
		tb.Fatal(LiveTestVar + " is set but " + SupabaseDBURLVar + " is not set and " + EnvTestFile + " does not carry it: run `make supabase-env`")
	}
	return dsn
}

// SupabaseDBURLVar is the name `make supabase-env` writes the DSN under.
const SupabaseDBURLVar = "SUPABASE_DB_URL"

// loadEnvTest parses .env.test for the two SUPABASE_* aliases. Every
// failure yields the zero value; the caller skips.
func loadEnvTest(tb testing.TB) SupabaseEnv {
	tb.Helper()
	values := loadEnvTestValues(tb)
	return SupabaseEnv{URL: values["SUPABASE_URL"], PublishableKey: values["SUPABASE_PUBLISHABLE_KEY"]}
}

// loadEnvTestValues parses .env.test at the repository root. An
// unreadable file yields an empty map; every caller skips on a missing
// member rather than failing.
func loadEnvTestValues(tb testing.TB) map[string]string {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join(RepoRoot(tb), EnvTestFile))
	if err != nil {
		return map[string]string{}
	}
	return parseDotenv(string(data))
}

// parseDotenv reads NAME=value lines, tolerating `export`, blank lines,
// comments and double quotes around the value.
func parseDotenv(text string) map[string]string {
	values := map[string]string{}
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(name)] = value
	}
	return values
}

// supabaseHealthy reports whether GET <url>/auth/v1/health answers 200
// within two seconds.
func supabaseHealthy(tb testing.TB, base string) bool {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/auth/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}
