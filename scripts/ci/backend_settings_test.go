// The hosted-settings gate (P5-1): scripts/backend-settings.sh, the step `make backend-install` runs after
// `db push` and the reason `supabase config push` is no longer in that chain.
//
// The script's job is to put three hosted settings at the values D32 requires — the `brigade` schema on the
// Data API, the eight auth fields, Realtime's private-channels-only — one FIELD at a time, reading every one
// back. Four properties are worth real evidence and none can be read off the source: that a project already
// at its target is touched with ZERO PATCHes (the script is run by an idempotent make target, and a settings
// writer that rewrites what is already right is one bad diff away from rewriting what is not); that a wrong
// field produces exactly ONE PATCH carrying exactly THAT field; that the two forbidden fields
// (`rate_limit_anonymous_users`, `site_url` — the pair `config push` would have set to 1000 and to a loopback
// URL, P5-1 brief 1.4) are never on the wire at all; and that the personal access token is never an argument
// to curl. All four are measured here rather than inferred: every case runs the real script against an
// httptest.Server that plays the Management API and records every request, on a restricted PATH whose `curl`
// is the P5-0 shim that appends its argv (and the mode of every `-H @<file>`) to a file before exec'ing the
// real curl.
//
// Every case body is written against `reporter` (manifests_test.go) rather than *testing.T, so the same code
// runs twice: against the repository's script, where a report is a failure, and against a deliberately
// mutated copy, where SILENCE is the bug. TestBackendSettingsCasesPassOnAnUnmutatedCopy is the positive
// control for that table.
//
// There is no live case. The only project this could run against is the team's single hosted one, and a test
// that rewrites a production project's auth configuration to prove it can is not a trade worth making; P5-1's
// evidence bundle holds the real run.
package ci_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	backendSettingsScriptRel = "scripts/backend-settings.sh"
	backendSettingsRef       = "wmgtaraqmoufmrnyojzf0"
)

// The fake token the offline cases plant. It must not be JWT-shaped nor an sb_secret_ key:
// scripts/ci/no-secrets.sh scans every tracked file, this one included, and would fail the tree. It is also
// deliberately NOT `sbp_`-prefixed for the same reason — the shape, not the value, is what the scanner reads.
const backendSettingsFakeToken = "tok_fake_backend_settings_access_token"

// The two fields the script must never send. `config push` would have set the first to 1000 against a hosted
// default of 30 per hour per IP and the second to http://127.0.0.1:3000 (P5-1 brief 1.4); this script exists
// because those two are the reason that command is not the mechanism.
var backendSettingsForbidden = []string{"rate_limit_anonymous_users", "site_url"}

// ---------------------------------------------------------------------------------------------------------
// the fake Management API

type backendSettingsRequest struct {
	method string
	path   string
	body   string
}

// backendSettingsServer plays api.supabase.com for exactly the three configuration endpoints. Each group's
// current state is a field, so one case can put one field wrong and nothing else. A PATCH merges into that
// state, which is what makes the read-back assertions real: a script that PATCHed and never read back would
// pass a server that only echoed.
type backendSettingsServer struct {
	url string

	mu        sync.Mutex
	postgrest map[string]any
	auth      map[string]any
	realtime  map[string]any
	requests  []backendSettingsRequest
	// unauthorized turns every answer into a 401, for the case that proves an HTTP failure is fatal.
	unauthorized bool
}

func newBackendSettingsServer(t *testing.T) *backendSettingsServer {
	t.Helper()
	bs := &backendSettingsServer{
		// The shape of a project already at its target, measured on the real project (P5-1 bundle,
		// 00-baseline/): db_schema without `brigade`, every auth field already right, private_only null.
		postgrest: map[string]any{
			"db_schema":            "public,graphql_public",
			"db_extra_search_path": "public, extensions",
			"max_rows":             1000,
			// A credential the real GET carries. No case may ever see it in the script's output.
			"jwt_secret": "tok_fake_backend_settings_jwt_secret",
		},
		auth: map[string]any{
			"external_anonymous_users_enabled":      true,
			"disable_signup":                        false,
			"security_captcha_enabled":              false,
			"jwt_exp":                               3600,
			"sessions_timebox":                      0,
			"sessions_inactivity_timeout":           0,
			"refresh_token_rotation_enabled":        true,
			"security_refresh_token_reuse_interval": 10,
			"rate_limit_anonymous_users":            30,
			"site_url":                              "http://localhost:3000",
			"smtp_pass":                             "tok_fake_backend_settings_smtp_pass",
		},
		realtime: map[string]any{"private_only": nil, "connection_pool": 2},
	}
	srv := httptest.NewServer(http.HandlerFunc(bs.serve))
	t.Cleanup(srv.Close)
	bs.url = srv.URL + "/v1"
	return bs
}

func (bs *backendSettingsServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	bs.mu.Lock()
	bs.requests = append(bs.requests, backendSettingsRequest{
		method: r.Method, path: r.URL.Path, body: string(body),
	})
	var state map[string]any
	switch {
	case strings.HasSuffix(r.URL.Path, "/postgrest"):
		state = bs.postgrest
	case strings.HasSuffix(r.URL.Path, "/config/auth"):
		state = bs.auth
	case strings.HasSuffix(r.URL.Path, "/config/realtime"):
		state = bs.realtime
	}
	unauthorized := bs.unauthorized
	if r.Method == http.MethodPatch && state != nil && !unauthorized {
		var patch map[string]any
		if json.Unmarshal(body, &patch) == nil {
			for k, v := range patch {
				state[k] = v
			}
		}
	}
	out, _ := json.Marshal(state)
	bs.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case unauthorized:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Unauthorized"}`)
	case state == nil:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"the fake Management API has no such path"}`)
	default:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}
}

func (bs *backendSettingsServer) seen() []backendSettingsRequest {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return append([]backendSettingsRequest(nil), bs.requests...)
}

func (bs *backendSettingsServer) patches() []backendSettingsRequest {
	var out []backendSettingsRequest
	for _, r := range bs.seen() {
		if r.method == http.MethodPatch {
			out = append(out, r)
		}
	}
	return out
}

// value reads one field out of a group's current state, so a case can assert what the server ENDED at rather
// than what the script said it did.
func (bs *backendSettingsServer) value(group, field string) any {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	switch group {
	case "postgrest":
		return bs.postgrest[field]
	case "auth":
		return bs.auth[field]
	default:
		return bs.realtime[field]
	}
}

// ---------------------------------------------------------------------------------------------------------
// running the script

// runScriptAtWithArgs is runScriptAt's (keepalive_test.go) twin for a script that takes arguments: this one
// takes a project ref and an optional --dry-run, and the usage cases run it with neither.
func runScriptAtWithArgs(t *testing.T, script, workdir string, env []string, args ...string) result {
	t.Helper()
	//nolint:gosec // G204: a fixed script path — the repository's own, or this test's mutated copy of it
	cmd := exec.CommandContext(t.Context(), "sh", append([]string{script}, args...)...)
	cmd.Dir = workdir
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		t.Fatalf("running %s: %v", script, err)
	}
	return r
}

type backendSettingsRun struct {
	result
	shim string
}

// runBackendSettings runs one copy of the script against one fake server, on the P5-0 shimmed PATH.
func runBackendSettings(t *testing.T, script string, bs *backendSettingsServer, env []string, args ...string) backendSettingsRun {
	t.Helper()
	root := t.TempDir()
	dir, record := keepaliveShim(t, root)
	home := filepath.Join(root, ".home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	full := baseEnv(t, home, append([]string{
		"PATH=" + dir,
		"SUPABASE_API_URL=" + bs.url,
	}, env...)...)
	res := runScriptAtWithArgs(t, script, root, full, args...)
	data, err := os.ReadFile(record)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the curl shim's record: %v", err)
	}
	return backendSettingsRun{result: res, shim: string(data)}
}

// tokenEnv is the environment a configured run gets. `backendSettingsFakeToken` is the value every case then
// asserts is absent from stdout, stderr and the recorded argv.
func backendSettingsTokenEnv() []string {
	return []string{"SUPABASE_ACCESS_TOKEN=" + backendSettingsFakeToken}
}

// ---------------------------------------------------------------------------------------------------------
// assertions

func backendSettingsNoCredentials(r reporter, run backendSettingsRun) {
	r.Helper()
	keepaliveAbsent(r, "the script's output", run.all(),
		backendSettingsFakeToken,
		"tok_fake_backend_settings_jwt_secret", // the postgrest GET carries jwt_secret
		"tok_fake_backend_settings_smtp_pass",  // the auth GET carries smtp_pass and ~24 external_*_secret fields
	)
	// The token on argv is the failure this shim exists to catch: `ps` and any debug log see argv.
	for _, line := range strings.Split(run.shim, "\n") {
		if strings.HasPrefix(line, "ARGV\t") && strings.Contains(line, backendSettingsFakeToken) {
			r.Errorf("the access token is on curl's argv, which `ps` can read:\n%s", line)
		}
	}
}

func backendSettingsPass(r reporter, run backendSettingsRun, mentions ...string) {
	r.Helper()
	backendSettingsNoCredentials(r, run)
	if run.code != 0 {
		r.Errorf("exit %d, want 0\nstdout: %s\nstderr: %s", run.code, run.stdout, run.stderr)
		return
	}
	keepaliveMentions(r, "the script's output", run.all(), mentions...)
}

func backendSettingsFail(r reporter, run backendSettingsRun, mentions ...string) {
	r.Helper()
	backendSettingsNoCredentials(r, run)
	if run.code == 0 {
		r.Errorf("exit 0, want non-zero\nstdout: %s\nstderr: %s", run.stdout, run.stderr)
		return
	}
	keepaliveMentions(r, "the script's output", run.all(), mentions...)
}

// backendSettingsNoForbiddenField is asserted by every case that reaches the wire: neither
// rate_limit_anonymous_users nor site_url may appear in ANY request body the server saw.
func backendSettingsNoForbiddenField(r reporter, bs *backendSettingsServer) {
	r.Helper()
	for _, req := range bs.seen() {
		for _, f := range backendSettingsForbidden {
			if strings.Contains(req.body, f) {
				r.Errorf("a %s %s body names the forbidden field %q: %s", req.method, req.path, f, req.body)
			}
		}
	}
}

// backendSettingsPatchFields returns the sorted field names of one PATCH body.
func backendSettingsPatchFields(r reporter, req backendSettingsRequest) []string {
	r.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(req.body), &m); err != nil {
		r.Errorf("a PATCH body is not JSON (%v): %s", err, req.body)
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------------------------------------
// the cases

type backendSettingsCase struct {
	name string
	run  func(t *testing.T, r reporter, script string)
}

var backendSettingsCaseTable = []backendSettingsCase{
	{
		// (a) The idempotence case, and the one `make backend-install` hits on every run after the first.
		// The only field that is not already right on a fresh project is db_schema, so this case runs the
		// script TWICE and asserts the second run PATCHes nothing at all.
		name: "a second run against a settled project issues zero PATCHes",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			first := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsPass(r, first, "done -- every setting reads back at its target")
			if got := len(bs.patches()); got != 2 {
				r.Errorf("the first run issued %d PATCH(es), want 2 (db_schema and private_only): %v",
					got, bs.patches())
			}
			bs.mu.Lock()
			bs.requests = nil
			bs.mu.Unlock()

			second := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsPass(r, second,
				"postgrest.db_schema: public,graphql_public,brigade (already correct, no PATCH)",
				"realtime.private_only: true (already correct, no PATCH)",
				"auth.jwt_exp: 3600 (already correct, no PATCH)",
			)
			if got := len(bs.patches()); got != 0 {
				r.Errorf("the second run issued %d PATCH(es), want 0: %v", got, bs.patches())
			}
			backendSettingsNoForbiddenField(r, bs)
		},
	},
	{
		// (b) db_schema is APPENDED to what the GET returned, never constructed from memory: a project that
		// exposes an extra schema of its own must keep it.
		name: "db_schema is appended to what the GET returned",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			bs.postgrest["db_schema"] = "public,graphql_public,storefront"
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsPass(r, run,
				"postgrest.db_schema: public,graphql_public,storefront -> public,graphql_public,storefront,brigade")
			if got := bs.value("postgrest", "db_schema"); got != "public,graphql_public,storefront,brigade" {
				r.Errorf("db_schema ended at %q, want the project's own list with brigade appended", got)
			}
			// Only db_schema may be sent: db_extra_search_path, max_rows and the pool fields stay as found.
			for _, p := range bs.patches() {
				if !strings.HasSuffix(p.path, "/postgrest") {
					continue
				}
				if fields := backendSettingsPatchFields(r, p); len(fields) != 1 || fields[0] != "db_schema" {
					r.Errorf("the postgrest PATCH carried %v, want exactly [db_schema]", fields)
				}
			}
			backendSettingsNoForbiddenField(r, bs)
		},
	},
	{
		// (b) One wrong auth field produces exactly one auth PATCH carrying exactly that field. Every other
		// D32 field is already right on this fixture, so a PATCH with two keys is a real defect.
		name: "one wrong auth field produces exactly one PATCH carrying exactly that field",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			bs.auth["external_anonymous_users_enabled"] = false
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsPass(r, run, "auth.external_anonymous_users_enabled: false -> true")
			var authPatches []backendSettingsRequest
			for _, p := range bs.patches() {
				if strings.HasSuffix(p.path, "/config/auth") {
					authPatches = append(authPatches, p)
				}
			}
			if len(authPatches) != 1 {
				r.Errorf("the run issued %d auth PATCH(es), want exactly 1: %v", len(authPatches), authPatches)
				return
			}
			fields := backendSettingsPatchFields(r, authPatches[0])
			if len(fields) != 1 || fields[0] != "external_anonymous_users_enabled" {
				r.Errorf("the auth PATCH carried %v, want exactly [external_anonymous_users_enabled]", fields)
			}
			if got := bs.value("auth", "external_anonymous_users_enabled"); got != true {
				r.Errorf("external_anonymous_users_enabled ended at %v, want true", got)
			}
			backendSettingsNoForbiddenField(r, bs)
		},
	},
	{
		// Every D32 field wrong at once: one PATCH, all eight fields, and the two forbidden ones absent from
		// it. This is the case that would catch a script that PATCHed the whole GET body back.
		name: "every D32 field wrong is one PATCH of exactly those fields",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			bs.auth["external_anonymous_users_enabled"] = false
			bs.auth["disable_signup"] = true
			bs.auth["security_captcha_enabled"] = true
			bs.auth["jwt_exp"] = 604800
			bs.auth["sessions_timebox"] = 24
			bs.auth["sessions_inactivity_timeout"] = 8
			bs.auth["refresh_token_rotation_enabled"] = false
			bs.auth["security_refresh_token_reuse_interval"] = 0
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsPass(r, run,
				"auth.disable_signup: true -> false",
				"auth.security_captcha_enabled: true -> false",
				"auth.jwt_exp: 604800 -> 3600",
				"auth.sessions_timebox: 24 -> 0",
				"auth.sessions_inactivity_timeout: 8 -> 0",
				"auth.refresh_token_rotation_enabled: false -> true",
				"auth.security_refresh_token_reuse_interval: 0 -> 10",
			)
			for _, p := range bs.patches() {
				if !strings.HasSuffix(p.path, "/config/auth") {
					continue
				}
				want := []string{
					"disable_signup", "external_anonymous_users_enabled", "jwt_exp",
					"refresh_token_rotation_enabled", "security_captcha_enabled",
					"security_refresh_token_reuse_interval", "sessions_inactivity_timeout", "sessions_timebox",
				}
				if got := backendSettingsPatchFields(r, p); strings.Join(got, ",") != strings.Join(want, ",") {
					r.Errorf("the auth PATCH carried %v, want exactly %v", got, want)
				}
			}
			backendSettingsNoForbiddenField(r, bs)
		},
	},
	{
		// (c) --dry-run reports the same lines and touches nothing.
		name: "dry run reports every difference and issues zero PATCHes",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			bs.auth["jwt_exp"] = 604800
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef, "--dry-run")
			backendSettingsPass(r, run,
				"postgrest.db_schema: public,graphql_public -> public,graphql_public,brigade (dry run: not applied)",
				"auth.jwt_exp: 604800 -> 3600 (dry run: not applied)",
				"realtime.private_only: null -> true (dry run: not applied)",
				"dry run: nothing was changed",
			)
			if got := len(bs.patches()); got != 0 {
				r.Errorf("a dry run issued %d PATCH(es), want 0: %v", got, bs.patches())
			}
			if got := bs.value("auth", "jwt_exp"); got != float64(604800) && got != 604800 {
				r.Errorf("a dry run changed jwt_exp to %v", got)
			}
			backendSettingsNoForbiddenField(r, bs)
		},
	},
	{
		// (d) No token: refuse before the network, and name the variable.
		name: "no access token refuses before touching the network",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			run := runBackendSettings(t, script, bs, nil, backendSettingsRef)
			backendSettingsFail(r, run, "SUPABASE_ACCESS_TOKEN is not set")
			if got := len(bs.seen()); got != 0 {
				r.Errorf("the fake Management API saw %d request(s) with no token, want 0: %v", got, bs.seen())
			}
		},
	},
	{
		// A typo in the flag must not silently become a real run: `--dryrun` looks enough like `--dry-run`
		// that an operator inspecting a production project would never notice the difference in the output.
		name: "a mistyped flag is refused before touching the network",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef, "--dryrun")
			backendSettingsFail(r, run, "unknown argument: --dryrun")
			if got := len(bs.seen()); got != 0 {
				r.Errorf("the fake Management API saw %d request(s) after a bad flag, want 0: %v", got, bs.seen())
			}
		},
	},
	{
		name: "no project ref is a usage error",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv())
			backendSettingsFail(r, run, "usage: scripts/backend-settings.sh <project-ref>")
			if got := len(bs.seen()); got != 0 {
				r.Errorf("the fake Management API saw %d request(s) with no ref, want 0: %v", got, bs.seen())
			}
		},
	},
	{
		// An HTTP failure is fatal and names the call, and no credential leaks on the error path — which is
		// the path most likely to over-share.
		name: "an HTTP failure is fatal and names the call",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			bs.unauthorized = true
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsFail(r, run, "answered HTTP 401", "/postgrest")
		},
	},
	{
		// The token reaches curl through a 0600 header file, never on argv. The shim records both.
		name: "the token travels in a 0600 header file and never on argv",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			run := runBackendSettings(t, script, bs, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsPass(r, run)
			modes := keepaliveHeaderFileModes(run.shim)
			if len(modes) == 0 {
				r.Errorf("the shim recorded no `-H @<file>` argument: the token is not travelling in a header file\n%s", run.shim)
			}
			for _, m := range modes {
				// HasPrefix, not equality: macOS `ls -l` appends `@` to the mode of a file with extended
				// attributes, and keepalive_test.go reads the same column the same way.
				if !strings.HasPrefix(m, "-rw-------") {
					r.Errorf("a header file is mode %q, want 0600 (-rw-------)", m)
				}
			}
			// Belt to the argv braces: some curl argument must be an @-file.
			if !strings.Contains(run.shim, "\t@") {
				r.Errorf("no curl invocation carried an @<file> argument:\n%s", run.shim)
			}
		},
	},
	{
		// The read-back is not decorative: a server that accepts a PATCH and does not apply it must fail the
		// run. This is the property that separates "we sent it" from "it is set".
		name: "a PATCH that does not stick fails the run",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			bs := newBackendSettingsServer(t)
			// A server whose realtime PATCH is a no-op: the GET after it still answers null.
			bs.realtime["private_only"] = nil
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, _ := io.ReadAll(req.Body)
				bs.mu.Lock()
				bs.requests = append(bs.requests, backendSettingsRequest{req.Method, req.URL.Path, string(body)})
				bs.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				switch {
				case strings.HasSuffix(req.URL.Path, "/postgrest"):
					_, _ = io.WriteString(w, `{"db_schema":"public,graphql_public,brigade"}`)
				case strings.HasSuffix(req.URL.Path, "/config/auth"):
					_, _ = io.WriteString(w, `{"external_anonymous_users_enabled":true,"disable_signup":false,`+
						`"security_captcha_enabled":false,"jwt_exp":3600,"sessions_timebox":0,`+
						`"sessions_inactivity_timeout":0,"refresh_token_rotation_enabled":true,`+
						`"security_refresh_token_reuse_interval":10}`)
				default:
					_, _ = io.WriteString(w, `{"private_only":null}`) // the PATCH is swallowed
				}
			}))
			t.Cleanup(srv.Close)
			stubborn := &backendSettingsServer{url: srv.URL + "/v1"}
			run := runBackendSettings(t, script, stubborn, backendSettingsTokenEnv(), backendSettingsRef)
			backendSettingsFail(r, run,
				"FAILED read-back realtime.private_only: expected true, got null",
				"did not read back at their target")
		},
	},
}

// ---------------------------------------------------------------------------------------------------------
// the tests

func TestBackendSettings(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), backendSettingsScriptRel)
	for _, c := range backendSettingsCaseTable {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, t, script)
		})
	}
}

func TestBackendSettingsScriptIsExecutableAndParses(t *testing.T) {
	t.Parallel()
	path := filepath.Join(testutil.RepoRoot(t), backendSettingsScriptRel)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", backendSettingsScriptRel, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s is mode %v: make backend-install invokes it directly, so it must be executable",
			backendSettingsScriptRel, info.Mode().Perm())
	}
	//nolint:gosec // G304: the repository's own script
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", backendSettingsScriptRel, err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh\n") {
		t.Errorf("%s does not start with #!/bin/sh: plugin-check.sh check 9 shellchecks it with -s sh",
			backendSettingsScriptRel)
	}
	//nolint:gosec // G204: a fixed path under the repository under test
	if out, err := exec.CommandContext(t.Context(), "sh", "-n", path).CombinedOutput(); err != nil {
		t.Errorf("sh -n %s: %v\n%s", backendSettingsScriptRel, err, out)
	}
}

// TestBackendSettingsMakefileAgrees joins the script to the target that runs it: `make backend-install` must
// call it, must NOT call `config push` or `link`, must not print unfiltered `api-keys` output, and the
// standalone `supabase-config-push` target must refuse without i_know=1. A P5-1 that left `config push` in the
// chain is exactly the regression this row exists to catch.
func TestBackendSettingsMakefileAgrees(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G304: the repository's own Makefile
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("reading the Makefile: %v", err)
	}
	mk := string(data)
	at := strings.Index(mk, "\nbackend-install:")
	if at < 0 {
		t.Fatalf("the Makefile has no backend-install target")
	}
	chain := mk[at:]
	for _, want := range []string{
		"scripts/backend-settings.sh $(project)",
		"$(supabase) db push --dry-run --project-ref $(project)",
		"$(supabase) migration list --project-ref $(project)",
	} {
		if !strings.Contains(chain, want) {
			t.Errorf("backend-install does not run %q", want)
		}
	}
	// Repaired 2026-09-10: `link` reveals the legacy service_role key, Supabase no longer hands that to a
	// personal access token, and the command dies on LegacyLinkAuthTokenError for every project and every
	// token (CLI 2.116.0 and 2.117.0). Nothing in the chain may reach for it again.
	if strings.Contains(chain, "$(supabase) link") {
		t.Errorf("backend-install runs `link`, which is broken upstream and was never needed:\n%s", chain)
	}
	// `projects api-keys` prints every key, the legacy service_role JWT included. The chain must filter it.
	if strings.Contains(chain, "projects api-keys") && !strings.Contains(chain, "publishable") {
		t.Errorf("backend-install prints unfiltered api-keys output, which leaks the service_role key:\n%s", chain)
	}
	if strings.Contains(chain, "config push") || strings.Contains(chain, "supabase-config-push") {
		t.Errorf("backend-install still reaches `config push`, which P5-1 removed from its chain:\n%s", chain)
	}
	if !strings.Contains(mk, "supabase-config-push:") || !strings.Contains(mk, "i_know") {
		t.Errorf("the standalone supabase-config-push target has lost its i_know=1 guard")
	}
}

func TestBackendSettingsShellcheck(t *testing.T) {
	t.Parallel()
	if !haveShellcheck() {
		t.Skip("shellcheck is not installed; CI enforces it (plugin-check.sh check 9 globs scripts/*.sh)")
	}
	//nolint:gosec // G204: a fixed path under the repository under test
	cmd := exec.CommandContext(t.Context(), "shellcheck", "-s", "sh",
		filepath.Join(testutil.RepoRoot(t), backendSettingsScriptRel))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellcheck -s sh %s: %v\n%s", backendSettingsScriptRel, err, out)
	}
}

// ---------------------------------------------------------------------------------------------------------
// mutations

func copyBackendSettingsScript(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	//nolint:gosec // G304: the repository's own script
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), backendSettingsScriptRel))
	if err != nil {
		t.Fatalf("reading %s: %v", backendSettingsScriptRel, err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "scripts"), 0o700); err != nil {
		t.Fatalf("creating the copy's scripts directory: %v", err)
	}
	//nolint:gosec // G306: a 0600 fixture under t.TempDir(); the runner invokes it as `sh <path>`
	if err := os.WriteFile(filepath.Join(dst, backendSettingsScriptRel), data, 0o600); err != nil {
		t.Fatalf("writing the copy: %v", err)
	}
	return filepath.Join(dst, backendSettingsScriptRel)
}

var backendSettingsMutations = []struct {
	name    string
	mutate  mutation
	caught  string
	because string
}{
	{
		"the token is passed on argv instead of in a header file",
		mutationEdits(
			// Keep the token in a variable past the point the script unsets it, then put it on argv.
			replaceInFile(backendSettingsScriptRel, "\nunset token ", "\nleaked=$token\nunset token "),
			replaceInFile(backendSettingsScriptRel,
				`-w '%{http_code}' -H @"$hdr" -H 'Accept: application/json' "$api$1"`,
				`-w '%{http_code}' -H "Authorization: Bearer $leaked" -H 'Accept: application/json' "$api$1"`),
		),
		"the token travels in a 0600 header file and never on argv",
		"argv is world-readable through `ps` and is echoed by every debug log; the shim records it, so this " +
			"row proves the assertion reads a measurement and not the source",
	},
	{
		"the settings are PATCHed unconditionally instead of only when they differ",
		replaceInFile(backendSettingsScriptRel,
			`if [ "$db_schema_before" != "$db_schema_after" ] && [ -z "$dry" ]; then`,
			`if [ -z "$dry" ]; then`),
		"a second run against a settled project issues zero PATCHes",
		"an idempotent make target that rewrites what is already right is one bad diff away from rewriting " +
			"what is not; the second run must be silent on the wire",
	},
	{
		"--dry-run still writes",
		replaceInFile(backendSettingsScriptRel, `[ "$private_before" != true ] && [ -z "$dry" ]`,
			`[ "$private_before" != true ]`),
		"dry run reports every difference and issues zero PATCHes",
		"`make backend-install dry=1` is what an administrator runs to SEE the diff before touching a " +
			"production project; a dry run that writes is worse than no dry run",
	},
	{
		"the forbidden rate_limit_anonymous_users is sent",
		replaceInFile(backendSettingsScriptRel, `  'security_refresh_token_reuse_interval 10' >> "$tmp/auth.pairs"`,
			`  'security_refresh_token_reuse_interval 10' \`+"\n"+
				`  'rate_limit_anonymous_users 1000' >> "$tmp/auth.pairs"`),
		"one wrong auth field produces exactly one PATCH carrying exactly that field",
		"1000 against a hosted default of 30/h/IP is a 33x abuse ceiling on a project whose publishable key " +
			"is handed to every team member; this field is the whole reason `config push` is not the mechanism",
	},
	{
		"db_schema is written from memory instead of appended to the GET",
		replaceInFile(backendSettingsScriptRel, `db_schema_after="$db_schema_before,brigade"`,
			`db_schema_after="public,graphql_public,brigade"`),
		"db_schema is appended to what the GET returned",
		"a project that exposes a schema of its own would silently lose it, and the Data API would start " +
			"answering 404 for every route that used it",
	},
	{
		"the read-back is dropped",
		replaceInFile(backendSettingsScriptRel,
			`  if [ "$read_back" != true ]; then`+"\n"+
				`    say "FAILED read-back realtime.private_only: expected true, got $read_back"; failed=1`+"\n"+
				`  fi`,
			`  : "$read_back"`),
		"a PATCH that does not stick fails the run",
		"a 2xx on a PATCH is not evidence the value is set; the read-back is what turns `we sent it` into " +
			"`it is set`, and D32's private_only is the field a silent no-op would hurt most",
	},
	{
		"a response body is echoed",
		// Through jq, not `cat`: the offline cases run on a restricted PATH holding only the tools the
		// script needs, so a `cat` would be caught as exit 127 — a missing binary, not a leak.
		replaceInFile(backendSettingsScriptRel, `db_schema_before=$(jq -r '.db_schema // empty' < "$body")`,
			`jq -c . < "$body"; db_schema_before=$(jq -r '.db_schema // empty' < "$body")`),
		"a second run against a settled project issues zero PATCHes",
		"the postgrest GET carries `jwt_secret` and the auth GET carries ~24 external_*_secret fields, " +
			"smtp_pass, sms_vonage_api_secret, nimbus_oauth_client_secret and five hook_*_secrets arrays",
	},
	{
		"a missing token is not fatal",
		replaceInFile(backendSettingsScriptRel,
			`[ -n "$token" ] || die 'SUPABASE_ACCESS_TOKEN is not set`,
			`[ -n "$token" ] || say 'SUPABASE_ACCESS_TOKEN is not set`),
		"no access token refuses before touching the network",
		"without the guard the script sends unauthenticated requests and reports whatever a 401 body says, " +
			"which reads like a project problem rather than a missing credential",
	},
	{
		"the umask that makes the header file 0600 is dropped",
		replaceInFile(backendSettingsScriptRel, "umask 077", "umask 022"),
		"the token travels in a 0600 header file and never on argv",
		"the header file holds the personal access token for the whole run; at 0644 every other account on " +
			"the machine can read it, and the file lives long enough for that to matter",
	},
	{
		"an unknown second argument is accepted instead of refused",
		replaceInFile(backendSettingsScriptRel,
			`  *)          die "unknown argument: $mode (usage: scripts/backend-settings.sh <project-ref> [--dry-run])" ;;`,
			`  *)          ;;`),
		"a mistyped flag is refused before touching the network",
		"a typo such as `--dryrun` would otherwise be silently ignored and the script would WRITE to a " +
			"production project the operator meant only to inspect",
	},
}

// TestBackendSettingsCasesPassOnAnUnmutatedCopy is the positive control for the table below: if the copy
// helper were broken, every row would "fail" for the wrong reason and the table would be vacuous.
func TestBackendSettingsCasesPassOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	for _, c := range backendSettingsCaseTable {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var rec recorder
			c.run(t, &rec, copyBackendSettingsScript(t))
			if len(rec.msgs) != 0 {
				t.Errorf("%d problem(s) against an unmutated copy: %s", len(rec.msgs), strings.Join(rec.msgs, "; "))
			}
		})
	}
}

func TestBackendSettingsMutations(t *testing.T) {
	t.Parallel()
	for _, m := range backendSettingsMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			script := copyBackendSettingsScript(t)
			root := filepath.Dir(filepath.Dir(script)) // <tmp>/scripts/backend-settings.sh -> <tmp>
			m.mutate(t, root)
			c := backendSettingsCaseByName(t, m.caught)
			var rec recorder
			c.run(t, &rec, script)
			if len(rec.msgs) == 0 {
				t.Fatalf("%q did not see the mutation, so it is vacuous (%s)", m.caught, m.because)
			}
			t.Logf("caught by %q: %s", m.caught, strings.Join(rec.msgs, "; "))
		})
	}
}

func backendSettingsCaseByName(t *testing.T, name string) backendSettingsCase {
	t.Helper()
	for _, c := range backendSettingsCaseTable {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("the mutation table names a case %q that does not exist", name)
	return backendSettingsCase{}
}

// mutationEdits applies several edits as one row, for the rows a single substitution cannot express.
func mutationEdits(ms ...mutation) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		for _, m := range ms {
			m(t, root)
		}
	}
}
