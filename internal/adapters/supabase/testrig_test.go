package supabase

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// A rig is one isolated adapter installation: its own configuration
// directory under t.TempDir, its own environment built from scratch, a
// clock the test owns and a fake backend (GoTrue and PostgREST on one
// httptest server). Nothing here reads the process environment: the
// developer running this suite has a real CLAUDE_CONFIG_DIR and real
// BRIGADE_* values, and a test that inherited them would read and write
// their own state.
type rig struct {
	t    *testing.T
	base string
	cfg  string
	env  []string
	now  time.Time
	be   *fakeBackend
}

// fixedStart is the wall clock every rig starts at.
var fixedStart = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// testKey is the fake backend's publishable key. It is not a real key.
const testKey = "sb_publishable_test_not_a_real_key"

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{
		t:    t,
		base: base,
		cfg:  filepath.Join(base, "config"),
		now:  fixedStart,
		be:   newFakeBackend(t),
	}
	r.env = []string{
		"HOME=" + filepath.Join(base, "home"),
		"BRIGADE_CONFIG_DIR=" + r.cfg,
		"BRIGADE_STATE_DIR=" + filepath.Join(base, "state"),
	}
	return r
}

// clock hands the adapter this rig's clock.
func (r *rig) clock() func() time.Time { return func() time.Time { return r.now } }

// outcome is one command's exit status and its two streams.
type outcome struct {
	code   int
	stdout string
	stderr string
}

// exec runs one adapter command in-process with the rig's environment.
func (r *rig) exec(input string, args ...string) outcome {
	r.t.Helper()
	return r.execEnv(r.env, input, args...)
}

func (r *rig) execEnv(env []string, input string, args ...string) outcome {
	r.t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(input), &stdout, &stderr, env, r.clock())
	if code < 0 || code > 12 {
		r.t.Fatalf("exit status %d is outside the 4.6 range 0..12 (args %v)", code, args)
	}
	if code != 0 {
		assertOneEnvelope(r.t, stdout.String())
	}
	return outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// ok runs a no-input command that must succeed and returns its `result`
// object.
func (r *rig) ok(args ...string) map[string]any {
	r.t.Helper()
	got := r.exec("", args...)
	if got.code != 0 {
		r.t.Fatalf("%v: exit %d, stdout %s stderr %s", args, got.code, got.stdout, got.stderr)
	}
	assertOneEnvelope(r.t, got.stdout)
	env := decode(r.t, got.stdout)
	result, isObject := env["result"].(map[string]any)
	if !isObject {
		r.t.Fatalf("%v: no result object in %s", args, got.stdout)
	}
	return result
}

// fails runs a command that must fail with one 4.6 code and returns the
// whole outcome, so a caller can assert on details and on byte identity.
func (r *rig) fails(wantCode string, wantExit int, input string, args ...string) outcome {
	r.t.Helper()
	got := r.exec(input, args...)
	if got.code != wantExit {
		r.t.Fatalf("%v: exit %d, want %d (stdout %s stderr %s)", args, got.code, wantExit, got.stdout, got.stderr)
	}
	assertErrorCode(r.t, got.stdout, wantCode)
	return got
}

// assertOneEnvelope pins 4.1: stdout is exactly one JSON object and one
// newline.
func assertOneEnvelope(t *testing.T, out string) {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout is not exactly one line: %q", out)
	}
	decode(t, out)
}

// decode parses one envelope into a loose map. The typed struct cannot
// tell an absent `retryable` from a false one; a map can (B-2).
func decode(t *testing.T, out string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not one JSON document: %v (%q)", err, out)
	}
	return env
}

// assertErrorCode checks the failing envelope's shape: the code, and that
// `retryable` is PRESENT (4.3, B-2) and equal to the code's row.
func assertErrorCode(t *testing.T, out, want string) {
	t.Helper()
	env := decode(t, out)
	if ok, _ := env["ok"].(bool); ok {
		t.Fatalf("envelope reports ok: true, want a failure: %s", out)
	}
	object, isObject := env["error"].(map[string]any)
	if !isObject {
		t.Fatalf("no error object in %s", out)
	}
	if got, _ := object["code"].(string); got != want {
		t.Fatalf("error.code = %q, want %q (%s)", got, want, out)
	}
	retryable, present := object["retryable"].(bool)
	if !present {
		t.Fatalf("error object carries no boolean retryable member: %s", out)
	}
	if retryable != protocol.Code(want).Retryable() {
		t.Fatalf("retryable = %v, want %v for %s", retryable, protocol.Code(want).Retryable(), want)
	}
}

// details reads error.details out of a failing envelope.
func details(t *testing.T, out string) map[string]any {
	t.Helper()
	env := decode(t, out)
	object, _ := env["error"].(map[string]any)
	d, _ := object["details"].(map[string]any)
	return d
}

// errorJSON returns the RAW bytes of the `error` member as the adapter
// emitted them, for the byte-identity assertions of 4.5.6 and 4.5.7.
func errorJSON(t *testing.T, out string) string {
	t.Helper()
	at := strings.Index(out, `"error":`)
	if at < 0 {
		t.Fatalf("no error member in %q", out)
	}
	return strings.TrimRight(out[at:], "\n")
}

// str reads a string member out of a result object.
func str(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok {
		t.Fatalf("member %q is missing or not a string in %v", key, object)
	}
	return value
}

// ---- the profile files ----

// profileDir is the rig's default profile directory.
func (r *rig) profileDir() string { return filepath.Join(r.cfg, "profiles", "default") }

// initProfile runs `profile init` against the fake backend.
func (r *rig) initProfile() {
	r.t.Helper()
	r.ok("profile", "init", "--url", r.be.srv.URL, "--key", testKey)
}

// bindTeam writes a team binding into profile.json, the way `team join`
// will (and the way the conformance suite's default --rebind does).
func (r *rig) bindTeam(teamRef, teamName string) {
	r.t.Helper()
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil {
		r.t.Fatalf("load profile: %v", err)
	}
	p.TeamRef, p.TeamName, p.HumanLabel = teamRef, teamName, "alice@example.com"
	if err := adapterkit.SaveProfile(r.cfg, "default", p); err != nil {
		r.t.Fatalf("save profile: %v", err)
	}
}

// sessionPath is session.json of the default profile.
func (r *rig) sessionPath() string { return filepath.Join(r.profileDir(), credentialFileName) }

// writeSession writes a session.json, 0600 in a 0700 directory.
func (r *rig) writeSession(s *session) {
	r.t.Helper()
	if err := adapterkit.MkdirPrivate(r.profileDir()); err != nil {
		r.t.Fatal(err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(r.sessionPath(), data, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// readSession reads session.json back, or nil when absent.
func (r *rig) readSession() *session {
	r.t.Helper()
	data, err := os.ReadFile(r.sessionPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil {
		r.t.Fatalf("session.json does not parse: %v", err)
	}
	return &s
}

// session builds a credential whose access token expires ttl after the
// rig's clock, carrying refresh token rt.
func (r *rig) session(ttl time.Duration, rt string) *session {
	return &session{
		AccessToken:  mintJWT(testUserID, r.now.Add(ttl)),
		TokenType:    "bearer",
		RefreshToken: rt,
		User:         sessionUser{ID: testUserID, Role: "authenticated", IsAnonymous: true},
	}
}

// joined puts the rig in the `joined` state: a profile with the fake
// backend, a fresh credential and a team binding.
func (r *rig) joined() {
	r.t.Helper()
	r.initProfile()
	r.writeSession(r.session(time.Hour, "rt-1"))
	r.bindTeam(testTeamID, "ops")
}

// command builds a command the way run does and runs its setup, so a
// test can drive authenticate and rpc directly.
func (r *rig) command(args ...string) *command {
	r.t.Helper()
	c := &command{
		stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard,
		environ: r.env, now: r.clock(), ctx: r.t.Context(),
	}
	c.log = testLogger(r.t)
	if err := c.setup(args); err != nil {
		r.t.Fatalf("setup %v: %v", args, err)
	}
	return c
}

// testLogger is the adapter's redacting logger writing to the test log
// at debug, so a leak in a log line is visible in a failing test's output.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return adapterlog.New(testWriter{t: t}, slog.LevelDebug, nil)
}

// testWriter forwards log lines to t.Log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// ---- JWTs ----

const (
	testUserID = "11111111-2222-4333-8444-555555555555"
	testTeamID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

// mintJWT builds an unsigned JWT-shaped token: the adapter reads exp and
// sub from the payload and never verifies a signature (5.1).
func mintJWT(sub string, exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		`{"aud":"authenticated","role":"authenticated","is_anonymous":true,"sub":"` + sub +
			`","exp":` + strconv.FormatInt(exp.Unix(), 10) + `}`))
	return header + "." + payload + ".not-a-signature"
}

// ---- the fake backend ----

// A recorded request is one exchange the fake backend saw.
type recorded struct {
	path   string
	header http.Header
	body   []byte
}

// A fakeBackend is GoTrue and PostgREST on one httptest server, with
// hooks a test sets to script refusals. The defaults: sign-up mints a
// fresh anonymous session, refresh rotates the refresh token, sign-out
// answers 204, every RPC answers 200 `{}`.
type fakeBackend struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests []recorded
	minted   int

	onSignup  func(w http.ResponseWriter, r *http.Request)
	onRefresh func(w http.ResponseWriter, r *http.Request, refreshToken string)
	onLogout  func(w http.ResponseWriter, r *http.Request)
	onRPC     func(w http.ResponseWriter, r *http.Request, fn, token string, args map[string]any)
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	be := &fakeBackend{t: t}
	be.srv = httptest.NewServer(http.HandlerFunc(be.serve))
	t.Cleanup(be.srv.Close)
	return be
}

func (be *fakeBackend) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	be.mu.Lock()
	be.requests = append(be.requests, recorded{path: r.URL.RequestURI(), header: r.Header.Clone(), body: body})
	be.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	switch {
	case r.URL.Path == "/auth/v1/signup":
		if be.onSignup != nil {
			be.onSignup(w, r)
			return
		}
		be.writeSession(w, "rt-1")
	case r.URL.Path == "/auth/v1/token":
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		if be.onRefresh != nil {
			be.onRefresh(w, r, req["refresh_token"])
			return
		}
		be.writeSession(w, req["refresh_token"]+"+1")
	case r.URL.Path == "/auth/v1/logout":
		if be.onLogout != nil {
			be.onLogout(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, rpcPath):
		fn := strings.TrimPrefix(r.URL.Path, rpcPath)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var args map[string]any
		_ = json.Unmarshal(body, &args)
		if be.onRPC != nil {
			be.onRPC(w, r, fn, token, args)
			return
		}
		writeJSON(w, http.StatusOK, `{}`)
	default:
		writeJSON(w, http.StatusNotFound, `{"message":"no such route"}`)
	}
}

// writeSession answers a GoTrue session whose access token lives an hour.
func (be *fakeBackend) writeSession(w http.ResponseWriter, rt string) {
	be.mu.Lock()
	be.minted++
	be.mu.Unlock()
	token := mintJWT(testUserID, time.Now().Add(time.Hour))
	writeJSON(w, http.StatusOK, `{"access_token":"`+token+`","token_type":"bearer","expires_in":3600,"refresh_token":"`+rt+
		`","user":{"id":"`+testUserID+`","aud":"authenticated","role":"authenticated","is_anonymous":true}}`)
}

// writeJSON writes one JSON body with a status.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// postgrest writes a PostgREST error body with a SQLSTATE and a message.
func postgrest(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, `{"code":"`+code+`","details":null,"hint":null,"message":"`+message+`"}`)
}

// authErrorNew is the 2024-01-01 GoTrue error shape.
func authErrorNew(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, `{"code":"`+code+`","message":"server text with a token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig"}`)
}

// authErrorLegacy is the shape without X-Supabase-Api-Version.
func authErrorLegacy(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, `{"code":`+strconv.Itoa(status)+`,"error_code":"`+code+`","msg":"server text"}`)
}

// calls counts the requests whose path starts with prefix.
func (be *fakeBackend) calls(prefix string) int {
	be.mu.Lock()
	defer be.mu.Unlock()
	n := 0
	for _, r := range be.requests {
		if strings.HasPrefix(r.path, prefix) {
			n++
		}
	}
	return n
}

// last returns the most recent request with the prefix, or nil.
func (be *fakeBackend) last(prefix string) *recorded {
	be.mu.Lock()
	defer be.mu.Unlock()
	for i := len(be.requests) - 1; i >= 0; i-- {
		if strings.HasPrefix(be.requests[i].path, prefix) {
			r := be.requests[i]
			return &r
		}
	}
	return nil
}

// total is the number of requests the fake has seen.
func (be *fakeBackend) total() int {
	be.mu.Lock()
	defer be.mu.Unlock()
	return len(be.requests)
}

// absent asserts that a path does not exist.
func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists, want it absent (%v)", path, err)
	}
}

// entries lists a directory recursively (relative paths), or nil when it
// does not exist.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err == nil && path != dir {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, rel)
		}
		return nil
	})
	return out
}
