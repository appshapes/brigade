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

// requireSupabaseMessage is the documented skip reason.
const requireSupabaseMessage = "no local Supabase stack: run `make supabase-start supabase-env`"

// RequireSupabase returns the local stack's url and publishable key, or
// skips the test. The values come from SUPABASE_URL and
// SUPABASE_PUBLISHABLE_KEY in the process environment (`make
// test-integration` sources .env.test first), else from .env.test at the
// repository root, read best-effort: a missing file, a file without the
// two members, or a stack whose auth service does not answer
// /auth/v1/health within two seconds all skip with the same message, so
// `make test` stays Docker-free and a stale .env.test never fails a unit
// run.
//
// The keys are read here and nowhere else in the test tree. Nothing under
// testutil is linked into the shipped binary.
func RequireSupabase(tb testing.TB) SupabaseEnv {
	tb.Helper()
	env := SupabaseEnv{URL: os.Getenv("SUPABASE_URL"), PublishableKey: os.Getenv("SUPABASE_PUBLISHABLE_KEY")}
	if env.URL == "" || env.PublishableKey == "" {
		env = loadEnvTest(tb)
	}
	if env.URL == "" || env.PublishableKey == "" {
		tb.Skip(requireSupabaseMessage)
	}
	if !supabaseHealthy(tb, env.URL) {
		tb.Skip(requireSupabaseMessage + " (the stack at " + env.URL + " does not answer)")
	}
	return env
}

// RequireSupabaseDB returns the local stack's Postgres DSN — the
// `postgres` superuser connection `make supabase-env` writes as
// SUPABASE_DB_URL — or skips the test with the same message
// [RequireSupabase] uses.
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
// The stack health check runs first, so a stale .env.test naming a stack
// that is not up skips rather than hangs on a dial.
func RequireSupabaseDB(tb testing.TB) string {
	tb.Helper()
	RequireSupabase(tb)
	dsn := os.Getenv(SupabaseDBURLVar)
	if dsn == "" {
		dsn = loadEnvTestValues(tb)[SupabaseDBURLVar]
	}
	if dsn == "" {
		tb.Skip(requireSupabaseMessage + " (" + SupabaseDBURLVar + " is not set and " + EnvTestFile + " does not carry it)")
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
