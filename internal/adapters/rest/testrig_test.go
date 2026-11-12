//go:build !windows

// The tests in this file's lane drive the adapter in-process against the
// stand-in server of the stub subpackage, which runs the fs adapter (whose
// own suite is not run on Windows), and they read file modes: unix stats.

package rest

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapters/rest/stub"
	"github.com/appshapes/brigade/internal/protocol"
)

// hangCatcher bounds every positive wait of these tests: an event, an
// exit. It is a "did this ever happen" bound and never a performance one.
const hangCatcher = 30 * time.Second

// A backend is one stand-in server behind a recording front: every request
// the adapter makes is kept, and a test may answer a request itself
// instead of letting the stub do it.
type backend struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests []seenRequest
	fault    func(*seenRequest) *canned
}

// A seenRequest is one request as the server saw it.
type seenRequest struct {
	Path          string
	Authorization string
	CreateToken   string
	ContentType   string
	UserAgent     string
	Body          map[string]any
	Raw           string
}

// A canned answer replaces the stub's for one request.
type canned struct {
	status int
	body   string
	header map[string]string
}

func newBackend(t *testing.T) *backend {
	t.Helper()
	return newBackendWith(t, stub.Options{Poll: 20 * time.Millisecond})
}

func newBackendWith(t *testing.T, o stub.Options) *backend {
	t.Helper()
	server, err := stub.New(t.TempDir(), o)
	if err != nil {
		t.Fatal(err)
	}
	b := &backend{t: t}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen := seenRequest{
			Path: r.URL.Path, Authorization: r.Header.Get("Authorization"),
			CreateToken: r.Header.Get("Brigade-Create-Token"),
			ContentType: r.Header.Get("Content-Type"), UserAgent: r.Header.Get("User-Agent"), Raw: string(raw),
		}
		_ = json.Unmarshal(raw, &seen.Body)
		b.mu.Lock()
		b.requests = append(b.requests, seen)
		fault := b.fault
		b.mu.Unlock()
		if fault != nil {
			if answer := fault(&seen); answer != nil {
				for k, v := range answer.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(answer.status)
				_, _ = io.WriteString(w, answer.body)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// url is the backend's base url.
func (b *backend) url() string { return b.srv.URL }

// answerWith installs (or, with nil, removes) the function that may answer
// a request instead of the stub.
func (b *backend) answerWith(fault func(*seenRequest) *canned) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fault = fault
}

// seen returns the requests made so far to path ("" for every path).
func (b *backend) seen(path string) []seenRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []seenRequest
	for _, r := range b.requests {
		if path == "" || r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// forget drops the recorded requests.
func (b *backend) forget() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests = nil
}

// A rig is one isolated adapter installation: its own configuration
// directory under t.TempDir and its own environment built from scratch,
// at debug level so that every "no secret on stderr" assertion means
// something. Nothing here reads the process environment. Everything a
// command of the rig wrote to stdout and stderr is kept in transcript.
type rig struct {
	t   *testing.T
	cfg string
	env []string

	mu         sync.Mutex
	transcript strings.Builder
}

// newRig returns an installation whose environment names backendURL in
// BRIGADE_REST_URL ("" for none).
func newRig(t *testing.T, backendURL string) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{t: t, cfg: filepath.Join(base, "config")}
	r.env = []string{
		"HOME=" + filepath.Join(base, "home"),
		"BRIGADE_CONFIG_DIR=" + r.cfg,
		"BRIGADE_STATE_DIR=" + filepath.Join(base, "state"),
		"BRIGADE_LOG_LEVEL=debug",
	}
	if backendURL != "" {
		r.env = append(r.env, backendURLVar+"="+backendURL)
	}
	return r
}

// outcome is one command's exit status and its two streams.
type outcome struct {
	code   int
	stdout string
	stderr string
}

// exec runs one adapter command in-process with the rig's environment.
func (r *rig) exec(input string, args ...string) outcome {
	r.t.Helper()
	return r.execEnv(nil, input, args...)
}

// execEnv is exec with extra variables appended to the environment.
func (r *rig) execEnv(extra []string, input string, args ...string) outcome {
	r.t.Helper()
	var stdout, stderr bytes.Buffer
	env := append(append([]string{}, r.env...), extra...)
	code := run(args, strings.NewReader(input), &stdout, &stderr, env, options{})
	if code < 0 || code > 12 {
		r.t.Fatalf("exit status %d is outside the 4.6 range 0..12 (args %v)", code, args)
	}
	assertOneEnvelope(r.t, stdout.String())
	r.keep(stdout.String(), stderr.String())
	return outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// keep adds output to the rig's transcript.
func (r *rig) keep(parts ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range parts {
		r.transcript.WriteString(p)
	}
}

// output is everything the rig's commands have written so far.
func (r *rig) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.transcript.String()
}

// ok runs a command that must succeed and returns its `result` object.
func (r *rig) ok(input string, args ...string) map[string]any {
	r.t.Helper()
	got := r.exec(input, args...)
	if got.code != 0 {
		r.t.Fatalf("%v: exit %d, stdout %s stderr %s", args, got.code, got.stdout, got.stderr)
	}
	result, isObject := decode(r.t, got.stdout)["result"].(map[string]any)
	if !isObject {
		r.t.Fatalf("%v: no result object in %s", args, got.stdout)
	}
	return result
}

// fails runs a command that must fail with one 4.6 code and returns the
// whole outcome, so a caller can assert on details and on byte identity.
func (r *rig) fails(want protocol.Code, input string, args ...string) outcome {
	r.t.Helper()
	got := r.exec(input, args...)
	if got.code != want.Exit() {
		r.t.Fatalf("%v: exit %d, want %d for %s (stdout %s stderr %s)", args, got.code, want.Exit(), want, got.stdout, got.stderr)
	}
	assertErrorCode(r.t, got.stdout, want)
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
func assertErrorCode(t *testing.T, out string, want protocol.Code) {
	t.Helper()
	env := decode(t, out)
	if ok, _ := env["ok"].(bool); ok {
		t.Fatalf("envelope reports ok: true, want a failure: %s", out)
	}
	object, isObject := env["error"].(map[string]any)
	if !isObject {
		t.Fatalf("no error object in %s", out)
	}
	if got, _ := object["code"].(string); got != string(want) {
		t.Fatalf("error.code = %q, want %q (%s)", got, want, out)
	}
	retryable, present := object["retryable"].(bool)
	if !present {
		t.Fatalf("error object carries no boolean retryable member: %s", out)
	}
	if retryable != want.Retryable() {
		t.Fatalf("retryable = %v, want %v for %s", retryable, want.Retryable(), want)
	}
}

// details reads error.details out of a failing envelope.
func details(t *testing.T, out string) map[string]any {
	t.Helper()
	object, _ := decode(t, out)["error"].(map[string]any)
	d, _ := object["details"].(map[string]any)
	return d
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

func (r *rig) profilePath() string { return filepath.Join(r.cfg, "teams", "default", "team.json") }
func (r *rig) credentialPath() string {
	return filepath.Join(r.cfg, "teams", "default", credentialFileName)
}

// storedCredential reads the credential out of credential.json, or "" when
// the file is absent.
func (r *rig) storedCredential() string {
	r.t.Helper()
	data, err := os.ReadFile(r.credentialPath())
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var cred credential
	if err := json.Unmarshal(data, &cred); err != nil {
		r.t.Fatalf("credential.json does not parse: %v", err)
	}
	return cred.Credential
}

// state is describe's profile.state.
func (r *rig) state() string {
	r.t.Helper()
	profile, _ := r.ok("", "describe")["profile"].(map[string]any)
	return str(r.t, profile, "state")
}

// ---- the flows ----

// create runs `team create` and returns the result.
func (r *rig) create(name, label string) map[string]any {
	r.t.Helper()
	return r.ok(`{"team_name":"`+name+`","human_label":"`+label+`"}`, "team", "create")
}

// join runs `team join` with secret and returns the result.
func (r *rig) join(secret, label string) map[string]any {
	r.t.Helper()
	return r.ok(`{"join_secret":"`+secret+`","human_label":"`+label+`"}`, "team", "join")
}

// register registers a session and returns its id.
func (r *rig) register(name string) string {
	r.t.Helper()
	return str(r.t, r.ok(registration(name), "session", "register"), "session_id")
}

// registration is a valid 4.4.2 document for a session named name.
func registration(name string) string {
	return `{"harness":"test","harness_version":"1","session_name":"` + name + `","activity":"busy","inbound":"accept"}`
}

// send sends body from one session to another and returns the message id.
func (r *rig) send(from, to, body string) string {
	r.t.Helper()
	doc := `{"sender_session_id":"` + from + `","recipient_session_id":"` + to + `","body":"` + body + `"}`
	return str(r.t, r.ok(doc, "message", "send"), "message_id")
}

// pair is the usual fixture: alice creates a team on b, bob joins it, and
// each registers one session.
type pair struct {
	alice, bob       *rig
	secret           string
	aliceSes, bobSes string
}

func newPair(t *testing.T, b *backend) *pair {
	t.Helper()
	p := &pair{alice: newRig(t, b.url()), bob: newRig(t, b.url())}
	p.secret = str(t, p.alice.create("ops", "alice@example.com"), "join_secret")
	p.bob.join(p.secret, "bob@example.com")
	p.aliceSes = p.alice.register("alice-session")
	p.bobSes = p.bob.register("bob-session")
	return p
}

// ---- the watch ----

// fastTiming is the watch's intervals for a test that does not care about
// them: the production shape, with a drain far enough away not to fire.
func fastTiming() timing {
	tm := defaultTiming()
	tm.wait = 2 * time.Second
	tm.drain = time.Hour
	tm.pace = 10 * time.Millisecond
	tm.backoff = []time.Duration{20 * time.Millisecond}
	return tm
}

// syncBuffer is a stderr a test goroutine may read while the adapter
// goroutine is still writing to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// watching drives one `message watch` in-process: a pipe for stdin, a pipe
// for stdout read with the protocol's own line reader, the exit status on
// a channel, and stop, which is what SIGTERM does to the watch's context.
type watching struct {
	t      *testing.T
	r      *rig
	stdin  *io.PipeWriter
	lines  chan []byte
	done   chan int
	stderr *syncBuffer
	stop   context.CancelFunc
}

func (r *rig) watch(tm timing, args ...string) *watching {
	r.t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	ctx, stop := context.WithCancel(context.Background())
	w := &watching{
		t: r.t, r: r, stdin: stdinW, lines: make(chan []byte, 256),
		done: make(chan int, 1), stderr: &syncBuffer{}, stop: stop,
	}
	argv := append([]string{"message", "watch"}, args...)
	go func() {
		code := run(argv, stdinR, stdoutW, w.stderr, r.env, options{timing: &tm, base: ctx})
		_ = stdoutW.Close()
		w.done <- code
	}()
	go func() {
		defer close(w.lines)
		reader := protocol.NewLineReader(stdoutR)
		for {
			line, err := reader.Next()
			if err != nil {
				return
			}
			r.keep(string(line), "\n")
			w.lines <- append([]byte(nil), line...)
		}
	}()
	r.t.Cleanup(func() {
		stop()
		_ = stdinW.Close()
		_ = stdinR.Close()
		select {
		case <-w.done:
		case <-time.After(hangCatcher):
		}
		r.keep(w.stderr.String())
	})
	return w
}

// next returns the next event, decoded, or fails the test.
func (w *watching) next() map[string]any {
	w.t.Helper()
	select {
	case line, ok := <-w.lines:
		if !ok {
			w.t.Fatalf("the watch closed stdout before the expected event (stderr %s)", w.stderr.String())
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			w.t.Fatalf("a watch line is not JSON: %v (%q)", err, line)
		}
		return event
	case <-time.After(hangCatcher):
		w.t.Fatalf("no watch event within %s (stderr %s)", hangCatcher, w.stderr.String())
		return nil
	}
}

// expect returns the next event and fails unless it is of kind.
func (w *watching) expect(kind string) map[string]any {
	w.t.Helper()
	event := w.next()
	if got, _ := event["event"].(string); got != kind {
		w.t.Fatalf("watch event %v, want a %q event", event, kind)
	}
	return event
}

// ready consumes the `ready` event.
func (w *watching) ready() {
	w.t.Helper()
	event := w.expect(protocol.EventReady)
	if mode, _ := event["mode"].(string); mode != protocol.WatchModePush {
		w.t.Fatalf("ready: mode %q, want push", mode)
	}
}

// message consumes a `message` event and returns the message id.
func (w *watching) message() string {
	w.t.Helper()
	message, _ := w.expect(protocol.EventMessage)["message"].(map[string]any)
	return str(w.t, message, "message_id")
}

// quiet asserts that no event arrives for the given time.
func (w *watching) quiet(d time.Duration) {
	w.t.Helper()
	select {
	case line, ok := <-w.lines:
		if ok {
			w.t.Fatalf("an unexpected watch event arrived: %q", line)
		}
		w.t.Fatalf("the watch ended while it was expected to stay quiet (stderr %s)", w.stderr.String())
	case <-time.After(d):
	}
}

// command writes one NDJSON line to the watch's stdin.
func (w *watching) command(line string) {
	w.t.Helper()
	if _, err := io.WriteString(w.stdin, line+"\n"); err != nil {
		w.t.Fatalf("watch stdin: %v", err)
	}
}

// exit waits for the watch to end and returns its status.
func (w *watching) exit() int {
	w.t.Helper()
	select {
	case code := <-w.done:
		w.done <- code
		return code
	case <-time.After(hangCatcher):
		w.t.Fatalf("the watch did not exit within %s (stderr %s)", hangCatcher, w.stderr.String())
		return -1
	}
}
