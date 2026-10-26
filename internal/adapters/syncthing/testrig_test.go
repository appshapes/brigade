package syncthing

import (
	"bytes"
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/procutil"
	engine "github.com/appshapes/brigade/internal/syncthing"
	"github.com/appshapes/brigade/internal/syncthing/syncthingtest"
	"github.com/appshapes/brigade/internal/testutil"
)

// A rig is one isolated adapter installation: its own configuration
// directory, state directory and store root under t.TempDir, its own
// environment built from scratch, and a clock the test owns. In local
// mode (the default) BRIGADE_SYNCTHING_STORE names the root and no engine
// is touched; withEngine points the rig at a fake engine instead.
type rig struct {
	t     *testing.T
	base  string
	cfg   string
	state string
	root  string
	env   []string
	now   time.Time
	deps  engine.Deps
	fake  *syncthingtest.Fake
	// wall makes clock the wall clock (withRealEngine).
	wall bool
}

// fixedStart is the wall clock every rig starts at.
var fixedStart = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{
		t:     t,
		base:  base,
		cfg:   filepath.Join(base, "config"),
		state: filepath.Join(base, "state"),
		root:  filepath.Join(base, "root"),
		now:   fixedStart,
		deps:  engine.Deps{Syncthing: func() ([]string, error) { return nil, exec.ErrNotFound }},
	}
	r.env = []string{
		"HOME=" + filepath.Join(base, "home"),
		"BRIGADE_CONFIG_DIR=" + r.cfg,
		"BRIGADE_STATE_DIR=" + r.state,
		storeVar + "=" + r.root,
	}
	return r
}

// withEngine turns the rig into engine mode: no store variable (the root
// is <state>/syncthing/teams), a fake REST server, and a home under the
// state directory whose daemon.pid names a live sleeper, so the engine
// reads as running and never tries to start anything.
func (r *rig) withEngine() *rig {
	r.t.Helper()
	r.env = r.env[:3]
	r.root = filepath.Join(r.state, "syncthing", "teams")
	fake, srv := syncthingtest.New(r.t)
	r.fake = fake
	r.deps = fakeDeps(srv)
	home := engine.Home(r.state)
	if err := os.MkdirAll(filepath.Join(home, "refs"), 0o700); err != nil {
		r.t.Fatal(err)
	}
	writeFile(r.t, filepath.Join(home, "config.xml"), `<configuration version="37"><gui enabled="true"><address>127.0.0.1:1</address><apikey>`+syncthingtest.Key+`</apikey></gui></configuration>`)
	writeFile(r.t, filepath.Join(home, "port"), "1\n")
	pid := testutil.NewSleeper(r.t)
	info, err := procutil.Lookup(pid)
	if err != nil {
		r.t.Fatal(err)
	}
	writeFile(r.t, filepath.Join(home, "daemon.pid"), strconv.Itoa(pid)+" "+info.StartToken+"\n")
	return r
}

func fakeDeps(srv *httptest.Server) engine.Deps {
	return engine.Deps{
		Syncthing: func() ([]string, error) { return nil, exec.ErrNotFound },
		BaseURL:   func(int) string { return srv.URL },
		StartWait: 2 * time.Second,
		StopWait:  time.Second,
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { //nolint:gosec // G703: a path the test built under t.TempDir
		t.Fatal(err)
	}
}

// clock hands the adapter this rig's clock.
func (r *rig) clock() func() time.Time {
	if r.wall {
		return time.Now
	}
	return func() time.Time { return r.now }
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
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(input), &stdout, &stderr, r.env, r.clock(), r.deps)
	if code < 0 || code > 12 {
		r.t.Fatalf("exit status %d is outside the 4.6 range 0..12 (args %v)", code, args)
	}
	return outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// ok runs a command that must succeed and returns its `result` object.
func (r *rig) ok(input string, args ...string) map[string]any {
	r.t.Helper()
	got := r.exec(input, args...)
	if got.code != 0 {
		r.t.Fatalf("%v: exit %d, stdout %s stderr %s", args, got.code, got.stdout, got.stderr)
	}
	env := decode(r.t, got.stdout)
	result, isObject := env["result"].(map[string]any)
	if !isObject {
		r.t.Fatalf("%v: no result object in %s", args, got.stdout)
	}
	return result
}

// fails runs a command that must fail with one 4.6 code and returns the
// whole envelope.
func (r *rig) fails(wantCode string, wantExit int, input string, args ...string) outcome {
	r.t.Helper()
	got := r.exec(input, args...)
	if got.code != wantExit {
		r.t.Fatalf("%v: exit %d, want %d (stdout %s)", args, got.code, wantExit, got.stdout)
	}
	assertErrorCode(r.t, got.stdout, wantCode)
	return got
}

func decode(t *testing.T, out string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not one JSON document: %v (%q)", err, out)
	}
	return env
}

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
	if _, present := object["retryable"]; !present {
		t.Fatalf("error object carries no retryable member: %s", out)
	}
}

// errorJSON returns the RAW bytes of the `error` member as emitted, for
// the byte-identity assertions of 4.5.7.
func errorJSON(t *testing.T, out string) string {
	t.Helper()
	at := strings.Index(out, `"error":`)
	if at < 0 {
		t.Fatalf("no error member in %q", out)
	}
	return strings.TrimRight(out[at:], "\n")
}

func str(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok {
		t.Fatalf("member %q is missing or not a string in %v", key, object)
	}
	return value
}

// team creates a team on the named profile and returns its ref and secret.
func (r *rig) team(profile, name string) (teamRef, secret string) { //nolint:unparam // the rig's vocabulary; the suite happens to use one team
	r.t.Helper()
	result := r.ok(`{"team_name":"`+name+`","human_label":"`+profile+`@example.com"}`,
		"--profile", profile, "team", "create")
	return str(r.t, result, "team_ref"), str(r.t, result, "join_secret")
}

// join binds a second profile to an existing team.
func (r *rig) join(profile, secret string) map[string]any {
	r.t.Helper()
	return r.ok(`{"join_secret":"`+secret+`","human_label":"`+profile+`@example.com"}`,
		"--profile", profile, "team", "join")
}

// register registers one session and returns its id.
func (r *rig) register(profile, name string) string {
	r.t.Helper()
	result := r.ok(`{"harness":"claude-code","harness_version":"2.1.251","session_name":"`+name+
		`","activity":"busy","inbound":"accept"}`, "--profile", profile, "session", "register")
	return str(r.t, result, "session_id")
}

// send delivers one message and returns the whole SendResponse.
func (r *rig) send(profile, from, to, body string) map[string]any { //nolint:unparam // the rig's vocabulary
	r.t.Helper()
	return r.ok(`{"sender_session_id":"`+from+`","recipient_session_id":"`+to+`","body":"`+body+`"}`,
		"--profile", profile, "message", "send")
}

// mustSessions reads the `sessions` array out of a `session list` result.
func mustSessions(t *testing.T, result map[string]any) []map[string]any {
	t.Helper()
	raw, ok := result["sessions"].([]any)
	if !ok {
		t.Fatalf("no sessions array in %v", result)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		object, isObject := entry.(map[string]any)
		if !isObject {
			t.Fatalf("a session entry is not an object: %v", entry)
		}
		out = append(out, object)
	}
	return out
}

// mustMessages reads the `messages` array out of a `message receive` result.
func mustMessages(t *testing.T, result map[string]any) []map[string]any {
	t.Helper()
	raw, ok := result["messages"].([]any)
	if !ok {
		t.Fatalf("no messages array in %v", result)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		object, isObject := entry.(map[string]any)
		if !isObject {
			t.Fatalf("a message entry is not an object: %v", entry)
		}
		out = append(out, object)
	}
	return out
}

// principalOf reads a profile's principal reference from its describe.
func (r *rig) principalOf(profile string) string {
	r.t.Helper()
	result := r.ok("", "--profile", profile, "describe")
	p, _ := result["profile"].(map[string]any)
	return str(r.t, p, "principal_ref")
}

// readStore reads one JSON file of the store into a loose map.
func (r *rig) readStore(parts ...string) map[string]any {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{r.root}, parts...)...))
	if err != nil {
		r.t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

// withRealEngine turns the rig into a real installation: no store
// variable, the root under the state directory, the syncthing on PATH,
// and the wall clock (a revocation's grace and a proof's timestamp are
// measured against it). The daemon the rig starts dies with the test.
func (r *rig) withRealEngine() *rig {
	r.t.Helper()
	r.env = append(r.env[:3], "PATH="+os.Getenv("PATH"))
	r.root = filepath.Join(r.state, "syncthing", "teams")
	r.deps = engine.RealDeps()
	r.wall = true
	home := engine.Home(r.state)
	r.t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(home, "daemon.pid")) //nolint:gosec // G304: the rig's own home
		if err != nil {
			return
		}
		pidText, _, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
		if pid, err := strconv.Atoi(pidText); err == nil && pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return r
}
