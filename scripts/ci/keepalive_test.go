// The keep-alive gate (P5-0): scripts/ci/keepalive.sh, the only step of .github/workflows/keepalive.yml.
//
// The script's job is to make the hosted Supabase project answer a few database requests every day so the
// Free plan never pauses it, and to fail loudly when it cannot. Two properties are worth real evidence and
// neither can be read off the source: that the four rungs go out in the right order with the right headers,
// and that NEITHER credential — the publishable key or the access token the sign-up mints — is ever an
// argument to curl. Both are measured here rather than inferred: every offline case runs the real script
// against an httptest.Server that plays GoTrue and PostgREST and records every request, on a restricted PATH
// whose `curl` is a SHIM that appends its argv (and the mode of every `-H @<file>` argument) to a file before
// exec'ing the real curl.
//
// Every case body is written against `reporter` (manifests_test.go) rather than *testing.T, so the same code
// runs twice: against the repository's script, where a report is a failure, and against a deliberately
// mutated copy, where SILENCE is the bug. The mutation table at the bottom of this file names, for each
// single-edit mutation, the case that must catch it; TestKeepaliveCasesPassOnAnUnmutatedCopy is its positive
// control.
//
// The live case (BRIGADE_TEST_LIVE=1, testutil.RequireSupabase) is the only one that talks to a real stack.
// It deliberately makes no direct Postgres assertion: rung 3 answering 200 means PostgREST accepted the JWT
// rung 2 minted and Postgres ran brigade.my_team_ids() as `authenticated`, which cannot happen unless the
// auth.users row exists — the row count would restate what the 200 already proves, at the cost of a database
// dependency in this package.
package ci_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	keepaliveScriptRel   = "scripts/ci/keepalive.sh"
	keepaliveWorkflowRel = ".github/workflows/keepalive.yml"
	keepaliveDocRel      = "docs/setup.md"
)

// keepaliveSignupBody is the anonymous sign-up body, retyped from
// internal/adapters/supabase/gotrue.go:215 (`body := []byte(...)` in signUpAnonymous). The drift join below
// requires all THREE witnesses — this constant, that Go source and the script — to agree, so a change to the
// adapter's body that the script does not follow is a failure here and not a silent divergence on the wire.
const keepaliveSignupBody = `{"data":{},"gotrue_meta_security":{}}`

// The fake credentials the offline cases plant. NEITHER may be JWT-shaped (eyJ….eyJ….…) nor an sb_secret_
// key: scripts/ci/no-secrets.sh scans every tracked file, this one included, and would fail the tree.
const (
	keepaliveFakeKey   = "sb_publishable_keepalive_fake_not_a_real_key"
	keepaliveFakeToken = "tok_fake_keepalive_access_token"
)

// ---------------------------------------------------------------------------------------------------------
// the fake backend

// keepaliveRequest is one request the fake backend saw, recorded whole so a case can assert on the method,
// the path AND the headers the rung is supposed to carry.
type keepaliveRequest struct {
	method  string
	path    string // RequestURI: the path plus the raw query, so ?scope=global is part of the assertion
	headers http.Header
	body    string
}

// keepaliveServer plays GoTrue and PostgREST for exactly the four paths the script climbs. Each rung's
// status (and, where it matters, its body) is a field, so one case breaks one rung and nothing else.
type keepaliveServer struct {
	url string

	healthStatus int
	signupStatus int
	signupBody   string
	rpcStatus    int
	rpcBody      string
	logoutStatus int

	mu       sync.Mutex
	requests []keepaliveRequest
}

func newKeepaliveServer(t *testing.T) *keepaliveServer {
	t.Helper()
	ks := &keepaliveServer{
		healthStatus: http.StatusOK,
		signupStatus: http.StatusOK,
		signupBody: `{"access_token":"` + keepaliveFakeToken + `","token_type":"bearer","expires_in":3600,` +
			`"refresh_token":"tok_fake_keepalive_refresh_token",` +
			`"user":{"id":"00000000-0000-4000-8000-000000000001","is_anonymous":true}}`,
		rpcStatus:    http.StatusOK,
		rpcBody:      `[]`,
		logoutStatus: http.StatusNoContent,
	}
	srv := httptest.NewServer(http.HandlerFunc(ks.serve))
	t.Cleanup(srv.Close)
	ks.url = srv.URL // http://127.0.0.1:<port> — which is also the loopback exception to the https-only rule
	return ks
}

func (ks *keepaliveServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	ks.mu.Lock()
	ks.requests = append(ks.requests, keepaliveRequest{
		method: r.Method, path: r.URL.RequestURI(), headers: r.Header.Clone(), body: string(body),
	})
	status, out := http.StatusNotFound, `{"code":"PGRST202","message":"the fake backend has no such path"}`
	switch r.URL.Path {
	case "/auth/v1/health":
		status, out = ks.healthStatus, `{"version":"fake","name":"GoTrue"}`
	case "/auth/v1/signup":
		status, out = ks.signupStatus, ks.signupBody
	case "/rest/v1/rpc/my_team_ids":
		status, out = ks.rpcStatus, ks.rpcBody
	case "/auth/v1/logout":
		status, out = ks.logoutStatus, ""
	}
	ks.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != http.StatusNoContent && out != "" {
		_, _ = io.WriteString(w, out)
	}
}

func (ks *keepaliveServer) seen() []keepaliveRequest {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return append([]keepaliveRequest(nil), ks.requests...)
}

// ---------------------------------------------------------------------------------------------------------
// running the script

// keepaliveRun is one run: what the script printed, and what the curl shim recorded while it ran.
type keepaliveRun struct {
	result
	shim string // the shim's record: one ARGV line per curl invocation, plus a HEADERFILE line per `@<file>`
}

// runScriptAt is runScript's (checks_test.go) twin for a script that is not necessarily the repository's own
// copy: the mutation table runs the same cases against a mutated copy under t.TempDir(), so the runner has to
// take a path rather than a name under scripts/ci/.
func runScriptAt(t *testing.T, script, workdir string, env []string) result {
	t.Helper()
	//nolint:gosec // G204: a fixed script path — the repository's own, or this test's mutated copy of it
	cmd := exec.CommandContext(t.Context(), "sh", script)
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

// keepaliveShim builds the PATH one offline run gets: symlinks to the three tools the script actually needs
// besides curl, and a `curl` of our own that records argv and then execs the real one. Absolute paths are
// baked into the shim because it runs on that same restricted PATH. It returns the directory and the record.
func keepaliveShim(t *testing.T, root string) (dir, record string) {
	t.Helper()
	dir = restrictedPath(t, root, "jq", "mktemp", "rm")
	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Skipf("curl is not on PATH: cannot run the keep-alive cases (%v)", err)
	}
	realLs, err := exec.LookPath("ls")
	if err != nil {
		t.Skipf("ls is not on PATH: the shim reports the header files' mode with it (%v)", err)
	}
	record = filepath.Join(root, "curl-argv.log")
	shim := "#!/bin/sh\n" +
		"# The P5-0 curl shim: record argv (and the mode of every `@<file>` argument, which is how the 0600\n" +
		"# header-file requirement is measured), then exec the real curl. Absolute paths only — this runs on\n" +
		"# the restricted PATH the script under test runs on.\n" +
		"{\n" +
		"  printf 'ARGV'\n" +
		"  for a in \"$@\"; do printf '\\t%s' \"$a\"; done\n" +
		"  printf '\\n'\n" +
		"  for a in \"$@\"; do\n" +
		"    case $a in\n" +
		"      @*) m=$(\"" + realLs + "\" -l \"${a#@}\"); printf 'HEADERFILE\\t%s\\t%s\\n' \"${m%% *}\" \"${a#@}\" ;;\n" +
		"    esac\n" +
		"  done\n" +
		"} >> \"" + record + "\"\n" +
		"exec \"" + realCurl + "\" \"$@\"\n"
	//nolint:gosec // G306: a PATH entry has to be executable; this is a 0700 file under this test's t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(shim), 0o700); err != nil {
		t.Fatalf("writing the curl shim: %v", err)
	}
	return dir, record
}

// runKeepalive runs one script with the given BRIGADE_* settings (full `NAME=value` entries) on the shimmed
// PATH, and returns its output together with the shim's record.
func runKeepalive(t *testing.T, script string, vars ...string) keepaliveRun {
	t.Helper()
	root := t.TempDir()
	dir, record := keepaliveShim(t, root)
	home := filepath.Join(root, ".home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	env := baseEnv(t, home, append([]string{"PATH=" + dir}, vars...)...)
	res := runScriptAt(t, script, root, env)
	data, err := os.ReadFile(record)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the curl shim's record: %v", err)
	}
	return keepaliveRun{result: res, shim: string(data)}
}

// keepaliveVars is the configured pair every full-ladder case runs with.
func keepaliveVars(url string) []string {
	return []string{"BRIGADE_SUPABASE_URL=" + url, "BRIGADE_SUPABASE_PUBLISHABLE_KEY=" + keepaliveFakeKey}
}

// ---------------------------------------------------------------------------------------------------------
// assertions
//
// keepalivePass and keepaliveFail are wantPass and wantFail (checks_test.go) written against `reporter`: a
// mutation row has to OBSERVE a failing assertion rather than abort the test on it, and those two call
// t.Fatalf. Nothing else about them differs.

// keepaliveNoCredentials is asserted by every case, before its exit code is even looked at: a FAILING run is
// the one most likely to echo a response body, and the sign-up body carries the access token. So the ladder's
// error paths are held to the same rule as its happy path, and a case that fails early still checks it.
func keepaliveNoCredentials(r reporter, res result) {
	r.Helper()
	keepaliveAbsent(r, "the script's output", res.all(), keepaliveFakeToken, keepaliveFakeKey)
}

func keepalivePass(r reporter, res result, mentions ...string) {
	r.Helper()
	keepaliveNoCredentials(r, res)
	if res.code != 0 {
		r.Errorf("exit %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
		return
	}
	keepaliveMentions(r, "the script's output", res.all(), mentions...)
}

func keepaliveFail(r reporter, res result, mentions ...string) {
	r.Helper()
	keepaliveNoCredentials(r, res)
	if res.code == 0 {
		r.Errorf("exit 0, want non-zero\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
		return
	}
	keepaliveMentions(r, "the script's output", res.all(), mentions...)
}

func keepaliveMentions(r reporter, what, text string, want ...string) {
	r.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			r.Errorf("%s does not mention %q\n%s", what, w, text)
		}
	}
}

func keepaliveAbsent(r reporter, what, text string, unwanted ...string) {
	r.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			r.Errorf("%s contains %q, which is a credential and must never appear there\n%s", what, u, text)
		}
	}
}

// keepaliveArgvLine returns the shim's ARGV line for the curl invocation whose arguments contain want (each
// rung is identified by its url), or "" when there was none.
func keepaliveArgvLine(shim, want string) string {
	for _, line := range strings.Split(shim, "\n") {
		if strings.HasPrefix(line, "ARGV\t") && strings.Contains(line, want) {
			return line
		}
	}
	return ""
}

// keepaliveHeaderFileModes returns the `ls -l` mode column the shim recorded for every `-H @<file>`.
func keepaliveHeaderFileModes(shim string) []string {
	var modes []string
	for _, line := range strings.Split(shim, "\n") {
		if fields := strings.Split(line, "\t"); len(fields) >= 2 && fields[0] == "HEADERFILE" {
			modes = append(modes, fields[1])
		}
	}
	return modes
}

// keepaliveWire compares what the fake backend saw with what the rung order requires.
func keepaliveWire(r reporter, got []keepaliveRequest, want [][2]string) bool {
	r.Helper()
	if len(got) != len(want) {
		r.Errorf("the backend saw %d request(s), want %d: %s", len(got), len(want), keepaliveTrace(got))
		return false
	}
	ok := true
	for i, w := range want {
		if got[i].method != w[0] || got[i].path != w[1] {
			r.Errorf("request %d was %s %s, want %s %s", i, got[i].method, got[i].path, w[0], w[1])
			ok = false
		}
	}
	return ok
}

func keepaliveTrace(got []keepaliveRequest) string {
	var b strings.Builder
	for _, req := range got {
		b.WriteString("\n  " + req.method + " " + req.path)
	}
	if b.Len() == 0 {
		return "\n  (none)"
	}
	return b.String()
}

func keepaliveHeader(r reporter, req keepaliveRequest, name, want string) {
	r.Helper()
	if got := req.headers.Get(name); got != want {
		r.Errorf("%s %s carried %s: %q, want %q", req.method, req.path, name, got, want)
	}
}

// ---------------------------------------------------------------------------------------------------------
// the offline cases

type keepaliveCase struct {
	name string
	run  func(t *testing.T, r reporter, script string)
}

var keepaliveCaseTable = []keepaliveCase{
	{
		// A fork, or the repository before its administrator sets the two variables: green, loud, and it
		// must not touch the network at all.
		name: "no variables set is a green no-op",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			run := runKeepalive(t, script)
			keepalivePass(r, run.result,
				"keepalive: BRIGADE_SUPABASE_URL and BRIGADE_SUPABASE_PUBLISHABLE_KEY are not set; nothing to do (a fork, or the variables are not configured yet)",
				"::notice::keepalive: BRIGADE_SUPABASE_URL and BRIGADE_SUPABASE_PUBLISHABLE_KEY are not set")
			keepaliveWire(r, ks.seen(), nil)
		},
	},
	{
		name: "only the url is set is a misconfiguration, not a fork",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			run := runKeepalive(t, script, "BRIGADE_SUPABASE_URL="+ks.url)
			keepaliveFail(r, run.result,
				"::error::keepalive: BRIGADE_SUPABASE_URL is set but BRIGADE_SUPABASE_PUBLISHABLE_KEY is not",
				"half-configured repository is a misconfiguration, not a fork")
			keepaliveWire(r, ks.seen(), nil)
		},
	},
	{
		name: "only the key is set is a misconfiguration, not a fork",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			run := runKeepalive(t, script, "BRIGADE_SUPABASE_PUBLISHABLE_KEY="+keepaliveFakeKey)
			keepaliveFail(r, run.result,
				"::error::keepalive: BRIGADE_SUPABASE_PUBLISHABLE_KEY is set but BRIGADE_SUPABASE_URL is not",
				"half-configured repository is a misconfiguration, not a fork")
			keepaliveWire(r, ks.seen(), nil)
		},
	},
	{
		// 5.2's https-only rule. The loopback half of it is not a separate case: every other case in this
		// table runs against httptest's own http://127.0.0.1:<port>, so an over-strict rule fails all of them.
		name: "a non-https url is refused",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			run := runKeepalive(t, script,
				"BRIGADE_SUPABASE_URL=http://example.com",
				"BRIGADE_SUPABASE_PUBLISHABLE_KEY="+keepaliveFakeKey)
			keepaliveFail(r, run.result,
				"::error::keepalive: BRIGADE_SUPABASE_URL must use https (http is allowed only for 127.0.0.1/localhost)")
			keepaliveWire(r, ks.seen(), nil)
		},
	},
	{
		name: "the full ladder",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			run := runKeepalive(t, script, keepaliveVars(ks.url)...)
			keepalivePass(r, run.result,
				"keepalive: health ok (HTTP 200)",
				"keepalive: anonymous sign-up ok (HTTP 200): one auth.users row -- database activity generated",
				"keepalive: rpc ok (HTTP 200): brigade.my_team_ids() answered",
				"keepalive: sign-out ok (HTTP 204)",
				"keepalive: done -- rungs: health 200, signup 200, rpc 200, logout 204")

			got := ks.seen()
			if !keepaliveWire(r, got, [][2]string{
				{"GET", "/auth/v1/health"},
				{"POST", "/auth/v1/signup"},
				{"POST", "/rest/v1/rpc/my_team_ids"},
				{"POST", "/auth/v1/logout?scope=global"},
			}) {
				return
			}
			for _, req := range got {
				keepaliveHeader(r, req, "apikey", keepaliveFakeKey)
			}
			// rung 2: the body is the adapter's, byte for byte.
			if got[1].body != keepaliveSignupBody {
				r.Errorf("the sign-up body was %q, want the adapter's %q", got[1].body, keepaliveSignupBody)
			}
			keepaliveHeader(r, got[1], "Content-Type", "application/json")
			// rung 3: the bearer from rung 2, both profile headers, an empty argument object.
			keepaliveHeader(r, got[2], "Authorization", "Bearer "+keepaliveFakeToken)
			keepaliveHeader(r, got[2], "Accept-Profile", "brigade")
			keepaliveHeader(r, got[2], "Content-Profile", "brigade")
			keepaliveHeader(r, got[2], "Content-Type", "application/json")
			if got[2].body != "{}" {
				r.Errorf("the rpc body was %q, want %q", got[2].body, "{}")
			}
			// rung 4: the same bearer.
			keepaliveHeader(r, got[3], "Authorization", "Bearer "+keepaliveFakeToken)

			// Neither credential in the log, and neither on argv — measured from the shim's record, not
			// inferred from reading the script.
			keepaliveAbsent(r, "the script's output", run.all(), keepaliveFakeToken, keepaliveFakeKey)
			keepaliveAbsent(r, "the curl argv the shim recorded", run.shim, keepaliveFakeToken, keepaliveFakeKey)
			if !strings.Contains(run.shim, "\t-H\t@") {
				r.Errorf("no `-H @<file>` reached curl at all, so the argv assertion above is vacuous:\n%s", run.shim)
			}
			// The health rung, and only the health rung, retries. A fake that always answers at once cannot
			// show this, so it is read off the shim's record of what curl was actually given: without the
			// flags a transient 502/503 in front of the project turns the daily run red for nothing, and
			// with them on the SIGN-UP rung a retried insert would mint a second auth.users row.
			if line := keepaliveArgvLine(run.shim, "/auth/v1/health"); line == "" {
				r.Errorf("the shim recorded no curl invocation for /auth/v1/health:\n%s", run.shim)
			} else if !strings.Contains(line, "\t--retry\t3\t--retry-delay\t10\t--retry-all-errors\t") {
				r.Errorf("the health rung did not carry `--retry 3 --retry-delay 10 --retry-all-errors`, so a "+
					"transient blip would page an administrator:\n%s", line)
			}
			for _, rung := range []string{"/auth/v1/signup", "/rest/v1/rpc/my_team_ids", "/auth/v1/logout"} {
				if line := keepaliveArgvLine(run.shim, rung); strings.Contains(line, "\t--retry\t") {
					r.Errorf("the %s rung retries: only the reachability probe may, because a retried "+
						"sign-up would insert a second auth.users row:\n%s", rung, line)
				}
			}

			modes := keepaliveHeaderFileModes(run.shim)
			if len(modes) != 4 {
				r.Errorf("the shim recorded %d header file(s), want one per rung (4):\n%s", len(modes), run.shim)
			}
			for _, mode := range modes {
				if !strings.HasPrefix(mode, "-rw-------") {
					r.Errorf("a header file was mode %q, want 0600 (-rw-------)", mode)
				}
			}
		},
	},
	{
		// The alert this whole workflow exists to raise: a paused, deleted or unreachable project. 540 and
		// not 503 on purpose — 503 is in curl's transient set, so `--retry 3 --retry-delay 10` would make
		// this case take thirty seconds to prove the same thing.
		name: "an unreachable project is the alert",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			ks.healthStatus = 540
			run := runKeepalive(t, script, keepaliveVars(ks.url)...)
			keepaliveFail(r, run.result,
				"::error::keepalive: the project at 127.0.0.1",
				"did not answer /auth/v1/health (HTTP 540)",
				"paused, deleted or unreachable -- see docs/setup.md")
			got := ks.seen()
			if len(got) < 1 {
				r.Errorf("the backend saw no request at all, so the health rung never ran")
			}
			for _, req := range got {
				if req.path != "/auth/v1/health" {
					r.Errorf("after a failing health rung the script still sent %s %s: %s",
						req.method, req.path, keepaliveTrace(got))
				}
			}
		},
	},
	{
		name: "anonymous sign-ins turned off names the dashboard setting",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			ks.signupStatus = http.StatusUnprocessableEntity
			ks.signupBody = `{"code":422,"error_code":"anonymous_provider_disabled","msg":"Anonymous sign-ins are disabled"}`
			run := runKeepalive(t, script, keepaliveVars(ks.url)...)
			keepaliveFail(r, run.result,
				"::error::keepalive: anonymous sign-up refused (HTTP 422, anonymous_provider_disabled)",
				"Anonymous sign-ins are disabled",
				"Authentication -> Sign In / Providers -> Allow anonymous sign-ins")
			keepaliveWire(r, ks.seen(), [][2]string{
				{"GET", "/auth/v1/health"},
				{"POST", "/auth/v1/signup"},
			})
		},
	},
	{
		// What the hosted project actually answered on 2026-09-04 (run 33942074574), before P5-1: the `brigade`
		// schema is not among the API's exposed schemas, so PostgREST answers 406 PGRST106 -- not the 404 the
		// first version of this script waited for. The same warning, the same green run.
		name: "an unexposed brigade schema (406 PGRST106) is a warning, not a failure",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			ks.rpcStatus = http.StatusNotAcceptable
			ks.rpcBody = `{"code":"PGRST106","message":"Invalid schema: brigade"}`
			run := runKeepalive(t, script, keepaliveVars(ks.url)...)
			keepalivePass(r, run.result,
				"::warning::keepalive: the brigade schema or my_team_ids() is not on this project yet (HTTP 406 PGRST106; P5-1 applies the migrations and exposes the schema)",
				"keepalive: done -- rungs: health 200, signup 200, rpc 406, logout 204")
		},
	},
	{
		// A 404 that is not PostgREST's PGRST202 -- the gateway's, a wrong path, an HTML body -- is not "P5-1 has
		// not run yet" and must be the alert, not a warning that hides a broken URL for a year.
		name: "a 404 without a PostgREST code is the alert",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			ks.rpcStatus = http.StatusNotFound
			ks.rpcBody = `<html>no Route matched with those values</html>`
			run := runKeepalive(t, script, keepaliveVars(ks.url)...)
			keepaliveFail(r, run.result,
				"::error::keepalive: the Data API refused POST /rest/v1/rpc/my_team_ids (HTTP 404): code= message=")
		},
	},
	{
		// The hosted project before P5-1 applies the migrations: a warning, never a failure, because rung 2
		// has already generated the day's activity and that is what this workflow is for.
		name: "a missing brigade schema is a warning, not a failure",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			ks.rpcStatus = http.StatusNotFound
			ks.rpcBody = `{"code":"PGRST202","message":"Could not find the function brigade.my_team_ids"}`
			run := runKeepalive(t, script, keepaliveVars(ks.url)...)
			keepalivePass(r, run.result,
				"::warning::keepalive: the brigade schema or my_team_ids() is not on this project yet (HTTP 404 PGRST202; P5-1 applies the migrations and exposes the schema)",
				"the sign-up above already counted as activity",
				"keepalive: done -- rungs: health 200, signup 200, rpc 404, logout 204")
			keepaliveWire(r, ks.seen(), [][2]string{
				{"GET", "/auth/v1/health"},
				{"POST", "/auth/v1/signup"},
				{"POST", "/rest/v1/rpc/my_team_ids"},
				{"POST", "/auth/v1/logout?scope=global"},
			})
		},
	},
	{
		name: "a refused sign-out is a warning",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			ks := newKeepaliveServer(t)
			ks.logoutStatus = http.StatusUnauthorized
			run := runKeepalive(t, script, keepaliveVars(ks.url)...)
			keepalivePass(r, run.result,
				"::warning::keepalive: the global sign-out answered HTTP 401, not 204",
				"keepalive: done -- rungs: health 200, signup 200, rpc 200, logout 401")
		},
	},
	{
		// The drift join is a case like the others so the mutation table can aim at it too.
		name: "the drift join",
		run: func(t *testing.T, r reporter, script string) {
			t.Helper()
			checkKeepaliveDrift(r, script, testutil.RepoRoot(t))
		},
	},
}

func TestKeepalive(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), keepaliveScriptRel)
	for _, c := range keepaliveCaseTable {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, t, script)
		})
	}
}

// ---------------------------------------------------------------------------------------------------------
// the drift join
//
// The script hard-codes four endpoints, two profile headers, one RPC name and one request body. Every one of
// them belongs to something else — the adapter's Go sources or the migration that defines the function — and
// a shell script cannot import any of it. This is the join, in the shape proof_test.go uses: read the
// literal out of the script, read the definition out of the source that owns it, and require them to agree.

// keepaliveRPCName pulls the RPC the script calls out of its `rpc_fn=` assignment.
var keepaliveRPCName = regexp.MustCompile(`(?m)^rpc_fn=([a-z_][a-z0-9_]*)$`)

func checkKeepaliveDrift(r reporter, script, repoRoot string) {
	r.Helper()
	data, err := os.ReadFile(script)
	if err != nil {
		r.Errorf("reading %s: %v", script, err)
		return
	}
	text := string(data)

	// ---- the script's own shape ----
	keepaliveMentions(r, keepaliveScriptRel, text,
		"#!/bin/sh\n",
		"set -eu\n",
		"umask 077\n",
		// The header files, and nothing that would put a credential on argv instead.
		`-H @"$hdr"`,
		`-H @"$hdr_auth"`,
	)
	for _, forbidden := range []string{`-H "Authorization`, `-H 'Authorization`, `-H "apikey`, `-H 'apikey`} {
		if strings.Contains(text, forbidden) {
			r.Errorf("%s passes %s on argv: the bearer token and the apikey go through the 0600 header file "+
				"(`-H @<file>`), because a runner's `ps` and the Actions debug log both see argv", keepaliveScriptRel, forbidden)
		}
	}

	// ---- the endpoints, against the adapter that owns them ----
	gotrue, ok := readText(r, repoRoot, "internal/adapters/supabase/gotrue.go")
	if !ok {
		return
	}
	postgrest, ok := readText(r, repoRoot, "internal/adapters/supabase/postgrest.go")
	if !ok {
		return
	}
	for _, join := range []struct{ literal, owner, ownerText string }{
		{`"/auth/v1/signup"`, "gotrue.go", gotrue},
		{`"/auth/v1/logout?scope=global"`, "gotrue.go", gotrue},
		{`rpcPath = "/rest/v1/rpc/"`, "postgrest.go", postgrest},
		{`schemaProfile = "brigade"`, "postgrest.go", postgrest},
		{`"Accept-Profile"`, "postgrest.go", postgrest},
		{`"Content-Profile"`, "postgrest.go", postgrest},
	} {
		if !strings.Contains(join.ownerText, join.literal) {
			r.Errorf("%s no longer contains %s, so the join that pins the script to it is vacuous", join.owner, join.literal)
		}
	}
	keepaliveMentions(r, keepaliveScriptRel, text,
		"$url/auth/v1/health",
		"$url/auth/v1/signup",
		`"$url/auth/v1/logout?scope=global"`,
		`"$url/rest/v1/rpc/$rpc_fn"`,
		"-H 'Accept-Profile: brigade'",
		"-H 'Content-Profile: brigade'",
	)

	// ---- the sign-up body: this file, the adapter and the script must all three agree ----
	if !strings.Contains(gotrue, "`"+keepaliveSignupBody+"`") {
		r.Errorf("internal/adapters/supabase/gotrue.go no longer sends %s: signUpAnonymous has changed and "+
			"%s must follow it", keepaliveSignupBody, keepaliveScriptRel)
	}
	if !strings.Contains(text, "signup_body='"+keepaliveSignupBody+"'") {
		r.Errorf("%s does not send the adapter's sign-up body %s", keepaliveScriptRel, keepaliveSignupBody)
	}

	// ---- the RPC name, against the migration that defines and grants it ----
	m := keepaliveRPCName.FindStringSubmatch(text)
	if m == nil {
		r.Errorf("%s has no `rpc_fn=<name>` assignment: the RPC it calls cannot be joined to the migration", keepaliveScriptRel)
		return
	}
	migration, ok := readText(r, repoRoot, "supabase/migrations/20260830120000_brigade_schema.sql")
	if !ok {
		return
	}
	fn := m[1]
	if !strings.Contains(migration, "function brigade."+fn+"()") {
		r.Errorf("%s calls brigade.%s(), which the schema migration does not define", keepaliveScriptRel, fn)
	}
	// Anonymous principals are `authenticated`, so the grant is what makes the rung answer 200 rather than 403.
	if !strings.Contains(migration, "on function brigade."+fn+"() to authenticated") {
		r.Errorf("the schema migration does not grant execute on brigade.%s() to `authenticated`, so the "+
			"keep-alive's anonymous principal could not call it", fn)
	}
}

// TestKeepaliveWorkflowAndDocsAgree pins the three places the two repository-variable names are written down
// to each other. A typo in any one of them is a workflow that silently no-ops for ever (the script cannot
// tell an unset variable from a misspelt one) — which is exactly the failure mode a keep-alive must not have.
func TestKeepaliveWorkflowAndDocsAgree(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	workflow, ok := readText(t, root, keepaliveWorkflowRel)
	if !ok {
		return
	}
	doc, ok := readText(t, root, keepaliveDocRel)
	if !ok {
		return
	}
	// The adapter's own names (P5-0 brief 3.1): internal/adapters/supabase/team.go declares them.
	team, ok := readText(t, root, "internal/adapters/supabase/team.go")
	if !ok {
		return
	}
	for _, name := range []string{"BRIGADE_SUPABASE_URL", "BRIGADE_SUPABASE_PUBLISHABLE_KEY"} {
		keepaliveMentions(t, "the adapter's environment names", team, `Var = "`+name+`"`)
		keepaliveMentions(t, keepaliveWorkflowRel, workflow, name+`: "${{ vars.`+name+` }}"`)
		keepaliveMentions(t, keepaliveDocRel, doc, "gh variable set "+name)
	}
	keepaliveMentions(t, keepaliveWorkflowRel, workflow,
		"name: keepalive",
		"workflow_dispatch: {}",
		"permissions: {contents: read}",
		"runs-on: blacksmith-4vcpu-ubuntu-2404",
		"uses: actions/checkout@v7",
		"run: "+keepaliveScriptRel,
	)
	// A secret would be masked in the log for no gain, and there is no secret this workflow may hold.
	if strings.Contains(workflow, "secrets.") {
		t.Errorf("%s names a secret: the url and the publishable key are public by design and are VARIABLES; "+
			"the secret/service-role key and the PAT are never here", keepaliveWorkflowRel)
	}
	keepaliveMentions(t, keepaliveDocRel, doc, "keepalive.yml", "gh workflow run keepalive.yml", "Resume project")
}

// TestKeepaliveScriptIsExecutableAndParses is the cheapest guard against a script CI would only discover on
// the day the schedule fires: the workflow's step is `run: scripts/ci/keepalive.sh`, which EXECS the file.
func TestKeepaliveScriptIsExecutableAndParses(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	path := filepath.Join(root, keepaliveScriptRel)
	//nolint:gosec // G204: a fixed path under the repository under test
	if out, err := exec.CommandContext(t.Context(), "sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n %s: %v\n%s", keepaliveScriptRel, err, out)
	}
	//nolint:gosec // G304: as above
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", keepaliveScriptRel, err)
	}
	if !strings.HasPrefix(string(data), "#!/bin/sh\n") {
		t.Errorf("%s does not begin with the `#!/bin/sh` shebang", keepaliveScriptRel)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", keepaliveScriptRel, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable (%v): the workflow runs it as a command", keepaliveScriptRel, fi.Mode())
	}
	//nolint:gosec // G204: `git ls-files -s` on a fixed path in the repository under test
	ls := exec.CommandContext(t.Context(), "git", "ls-files", "-s", keepaliveScriptRel)
	ls.Dir = root
	out, err := ls.Output()
	if err != nil {
		t.Skipf("git ls-files is unavailable here: %v", err)
	}
	if s := strings.TrimSpace(string(out)); s != "" && !strings.HasPrefix(s, "100755 ") {
		t.Errorf("%s is not recorded 100755 in the index (%q)", keepaliveScriptRel, s)
	}
}

// TestKeepaliveShellcheck runs the same check CI runs (plugin-check.sh check 9 shellchecks every
// scripts/ci/*.sh with `-s sh`), and skips like TestPluginCheck when shellcheck is not installed.
func TestKeepaliveShellcheck(t *testing.T) {
	t.Parallel()
	if !haveShellcheck() {
		t.Skip("shellcheck is not installed; CI's plugin-check.sh check 9 enforces it")
	}
	//nolint:gosec // G204: a fixed path under the repository under test
	cmd := exec.CommandContext(t.Context(), "shellcheck", "-s", "sh", filepath.Join(testutil.RepoRoot(t), keepaliveScriptRel))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellcheck -s sh %s: %v\n%s", keepaliveScriptRel, err, out)
	}
}

// ---------------------------------------------------------------------------------------------------------
// mutations: every case above, shown failing
//
// A case that has never been seen to fail is not a check. Each row copies the real script into t.TempDir(),
// makes ONE change (or one pair of changes, where a single edit could not express the mutation), and
// requires the NAMED case to report. A row may also be visible to another case — the drift join in
// particular overlaps the wire assertions on purpose — which is not a defect; what would be a defect is a
// row nothing sees.

// copyKeepaliveScript copies the script into a fresh temporary directory and returns the copy's path.
func copyKeepaliveScript(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	//nolint:gosec // G304: the repository's own script
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), keepaliveScriptRel))
	if err != nil {
		t.Fatalf("reading %s: %v", keepaliveScriptRel, err)
	}
	if err := os.MkdirAll(filepath.Join(dst, filepath.Dir(keepaliveScriptRel)), 0o700); err != nil {
		t.Fatalf("creating the copy's scripts directory: %v", err)
	}
	//nolint:gosec // G306: a 0600 fixture under t.TempDir(); the runner invokes it as `sh <path>`
	if err := os.WriteFile(filepath.Join(dst, keepaliveScriptRel), data, 0o600); err != nil {
		t.Fatalf("writing the copy: %v", err)
	}
	return filepath.Join(dst, keepaliveScriptRel)
}

// keepaliveEdits applies several mutations as one row, for the two rows a single substitution cannot express.
func keepaliveEdits(ms ...mutation) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		for _, m := range ms {
			m(t, root)
		}
	}
}

var keepaliveMutations = []struct {
	name    string
	mutate  mutation
	caught  string // the case above that must report
	because string
}{
	{
		"the RPC name is misspelt",
		replaceInFile(keepaliveScriptRel, "rpc_fn=my_team_ids", "rpc_fn=my_team_id"),
		"the full ladder",
		"the Data API rung must call the function the migration actually defines and grants",
	},
	{
		"the Accept-Profile header is dropped",
		replaceInFile(keepaliveScriptRel, " -H 'Accept-Profile: brigade'", ""),
		"the full ladder",
		"without Accept-Profile PostgREST looks for public.my_team_ids and answers 404, which this script " +
			"would misread as `the migrations are not applied yet`",
	},
	{
		"a token is planted on argv beside the header file",
		keepaliveEdits(
			replaceInFile(keepaliveScriptRel, "unset token", ": token"),
			replaceInFile(keepaliveScriptRel, `-H @"$hdr_auth" -H 'Accept-Profile: brigade'`,
				`-H @"$hdr_auth" -H "X-Brigade-Debug: $token" -H 'Accept-Profile: brigade'`),
		),
		"the full ladder",
		"the no-credential-on-argv assertion must read the shim's record; this row plants a token there and " +
			"changes nothing else, so only that assertion can catch it",
	},
	{
		"the bearer header file is replaced by an argv header",
		keepaliveEdits(
			replaceInFile(keepaliveScriptRel, "unset token", ": token"),
			replaceInFile(keepaliveScriptRel, `-H @"$hdr_auth" -H 'Accept-Profile: brigade'`,
				`-H "Authorization: Bearer $token" -H 'Accept-Profile: brigade'`),
		),
		"the full ladder",
		"CLAUDE.md: never a secret on argv — a runner's `ps` and the Actions debug log both see it",
	},
	{
		"the no-op branch is removed",
		replaceInFile(keepaliveScriptRel, `if [ -z "$url" ] && [ -z "$key" ]; then`, "if false; then"),
		"no variables set is a green no-op",
		"a fork, and this repository until its administrator sets the variables, must exit 0 without a request",
	},
	{
		"the missing-key guard is removed",
		replaceInFile(keepaliveScriptRel, `[ -n "$key" ] || die 'BRIGADE_SUPABASE_URL is set`,
			`[ -n "$key" ] || : 'BRIGADE_SUPABASE_URL is set`),
		"only the url is set is a misconfiguration, not a fork",
		"half-configured is a misconfiguration, and a silent green run would hide it for a week",
	},
	{
		"the missing-url guard is removed",
		replaceInFile(keepaliveScriptRel, `[ -n "$url" ] || die 'BRIGADE_SUPABASE_PUBLISHABLE_KEY is set`,
			`[ -n "$url" ] || : 'BRIGADE_SUPABASE_PUBLISHABLE_KEY is set`),
		"only the key is set is a misconfiguration, not a fork",
		"the same misconfiguration the other way round must name the variable that is missing",
	},
	{
		"the https rule stops refusing",
		replaceInFile(keepaliveScriptRel, `  *) die "BRIGADE_SUPABASE_URL must use https`,
			`  *) : "BRIGADE_SUPABASE_URL must use https`),
		"a non-https url is refused",
		"http would put the anonymous JWT and the key on the wire in clear (5.2)",
	},
	{
		"a failing health rung stops being fatal",
		replaceInFile(keepaliveScriptRel, `|| die "the project at $host did not answer`,
			`|| warn "the project at $host did not answer`),
		"an unreachable project is the alert",
		"a failing run IS the alert for a paused project; a warning would be an e-mail nobody gets",
	},
	{
		"the anonymous-sign-ins hint is dropped",
		replaceInFile(keepaliveScriptRel,
			`    *anonymous_provider_disabled*|*signup_disabled*|*'nonymous sign-ins are disabled'*|*'ignups not allowed'*)`,
			`    *this_pattern_matches_nothing*)`),
		"anonymous sign-ins turned off names the dashboard setting",
		"the one refusal an administrator can fix in thirty seconds must name the setting to change",
	},
	{
		"a missing brigade schema becomes a failure",
		replaceInFile(keepaliveScriptRel, `PGRST106|PGRST202) warn "the brigade schema`, `PGRST106|PGRST202) die "the brigade schema`),
		"a missing brigade schema is a warning, not a failure",
		"P5-1 has not applied the migrations to the hosted project yet, and the sign-up already counted",
	},
	{
		"the unexposed-schema shape (PGRST106) stops being recognised",
		replaceInFile(keepaliveScriptRel, `PGRST106|PGRST202) warn`, `PGRST202) warn`),
		"an unexposed brigade schema (406 PGRST106) is a warning, not a failure",
		"the hosted project's real pre-P5-1 answer would page the administrator every day",
	},
	{
		"a refused sign-out becomes a failure",
		replaceInFile(keepaliveScriptRel, `  *)   warn "the global sign-out`, `  *)   die "the global sign-out`),
		"a refused sign-out is a warning",
		"the activity had already happened by then; a red run here would be a false alarm",
	},
	{
		"the umask is loosened",
		replaceInFile(keepaliveScriptRel, "umask 077", "umask 022"),
		"the full ladder",
		"the header files carry the key and the access token and must be 0600; the shim reads their mode",
	},
	{
		"the summary line drifts",
		replaceInFile(keepaliveScriptRel, `say "done -- rungs: health $health`, `say "finished: health $health`),
		"the full ladder",
		"the summary is the one line a reader scans on the run page, and every case pins it",
	},
	{
		"the health rung's retries are removed",
		replaceInFile(keepaliveScriptRel, " --retry 3 --retry-delay 10 --retry-all-errors", ""),
		"the full ladder",
		"without them a transient 502/503 from whatever sits in front of the project turns the daily run red " +
			"and e-mails an administrator about nothing; measured against a fake that answers 503 once and " +
			"then 200, the script exits 1 without the flags and 0 with them",
	},
	{
		"the Content-Profile header is dropped",
		replaceInFile(keepaliveScriptRel, " -H 'Content-Profile: brigade'", ""),
		"the full ladder",
		"the RPC rung is a POST, and Content-Profile is the header that chooses the schema a POST writes " +
			"against; both profile headers are the adapter's (postgrest.go) and both belong on the wire",
	},
	{
		"the sign-up response body is echoed",
		// Through jq, not `cat`: the offline cases run on a restricted PATH that holds only the four tools
		// the script needs, so a `cat` here would be caught as exit 127 — a missing binary, not a leak.
		replaceInFile(keepaliveScriptRel, `say 'anonymous sign-up ok (HTTP 200)`,
			`jq -c . < "$body"; say 'anonymous sign-up ok (HTTP 200)`),
		"the full ladder",
		"rung 2's 200 body IS the session: printing it would put the access token in the Actions log, where " +
			"it is readable by anyone with read access to the run for as long as the log is retained",
	},
	{
		"a failing rung names the key in its error",
		replaceInFile(keepaliveScriptRel, `die "the project at $host did not answer`,
			`die "the project at $host ($key) did not answer`),
		"an unreachable project is the alert",
		"the error paths are the ones most likely to over-share; the key is public by design but the log " +
			"must not train anyone to paste it around, and this row proves the check runs on a FAILING run too",
	},
	{
		"set -eu is disabled",
		replaceInFile(keepaliveScriptRel, "set -eu", "set +eu"),
		"the drift join",
		"errexit and nounset are what stop a mistyped variable from silently sending an unauthenticated " +
			"request and reporting success",
	},
	{
		"the sign-up body drifts from the adapter's",
		replaceInFile(keepaliveScriptRel, "signup_body='"+keepaliveSignupBody+"'", `signup_body='{"data":{}}'`),
		"the drift join",
		"the body must stay byte-identical to gotrue.go's signUpAnonymous, or the keep-alive stops " +
			"exercising the path the adapter uses",
	},
}

// TestKeepaliveCasesPassOnAnUnmutatedCopy is the positive control for the whole table below: if the copy
// helper were broken, every row would "fail" for the wrong reason and the table would be vacuous.
func TestKeepaliveCasesPassOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	for _, c := range keepaliveCaseTable {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var rec recorder
			c.run(t, &rec, copyKeepaliveScript(t))
			if len(rec.msgs) != 0 {
				t.Errorf("%d problem(s) against an unmutated copy: %s", len(rec.msgs), strings.Join(rec.msgs, "; "))
			}
		})
	}
}

func TestKeepaliveMutations(t *testing.T) {
	t.Parallel()
	for _, m := range keepaliveMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			script := copyKeepaliveScript(t)
			root := filepath.Dir(filepath.Dir(filepath.Dir(script))) // <tmp>/scripts/ci/keepalive.sh -> <tmp>
			m.mutate(t, root)
			c := keepaliveCaseByName(t, m.caught)
			var rec recorder
			c.run(t, &rec, script)
			if len(rec.msgs) == 0 {
				t.Fatalf("%q did not see the mutation, so it is vacuous (%s)", m.caught, m.because)
			}
			t.Logf("caught by %q: %s", m.caught, strings.Join(rec.msgs, "; "))
		})
	}
}

func keepaliveCaseByName(t *testing.T, name string) keepaliveCase {
	t.Helper()
	for _, c := range keepaliveCaseTable {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("the mutation table names a case %q that does not exist", name)
	return keepaliveCase{}
}

// ---------------------------------------------------------------------------------------------------------
// live

// TestKeepaliveLiveAgainstTheLocalStack runs the real script, on the real PATH, against the local Supabase
// stack — the same four rungs the daily workflow will climb against the hosted project. It is opt-in behind
// BRIGADE_TEST_LIVE like every other live test in the tree, and it does add one anonymous row to the local
// stack's auth.users, which is exactly what the workflow is for.
func TestKeepaliveLiveAgainstTheLocalStack(t *testing.T) {
	t.Parallel()
	env := testutil.RequireSupabase(t)
	root := t.TempDir()
	home := filepath.Join(root, ".home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	res := runScriptAt(t, filepath.Join(testutil.RepoRoot(t), keepaliveScriptRel), root, baseEnv(t, home,
		"BRIGADE_SUPABASE_URL="+env.URL,
		"BRIGADE_SUPABASE_PUBLISHABLE_KEY="+env.PublishableKey,
	))
	wantPass(t, res,
		"keepalive: health ok (HTTP 200)",
		"keepalive: anonymous sign-up ok (HTTP 200)",
		"keepalive: rpc ok (HTTP 200): brigade.my_team_ids() answered",
		"keepalive: sign-out ok (HTTP 204)",
		"keepalive: done -- rungs: health 200, signup 200, rpc 200, logout 204",
	)
	// The key is public by design, but the log should not train anyone to paste it around, and the access
	// token is a real bearer credential for as long as it lives.
	if strings.Contains(res.all(), env.PublishableKey) {
		t.Errorf("the publishable key reached the script's output")
	}
	if strings.Contains(res.all(), "eyJ") {
		t.Errorf("something JWT-shaped reached the script's output:\n%s", res.all())
	}
}
