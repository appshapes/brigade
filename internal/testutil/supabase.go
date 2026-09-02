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

// loadEnvTest parses .env.test for the two SUPABASE_* aliases. Every
// failure yields the zero value; the caller skips.
func loadEnvTest(tb testing.TB) SupabaseEnv {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join(RepoRoot(tb), EnvTestFile))
	if err != nil {
		return SupabaseEnv{}
	}
	values := parseDotenv(string(data))
	return SupabaseEnv{URL: values["SUPABASE_URL"], PublishableKey: values["SUPABASE_PUBLISHABLE_KEY"]}
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
