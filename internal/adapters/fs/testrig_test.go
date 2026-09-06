package fs

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// A rig is one isolated adapter installation: its own configuration
// directory, state directory and store root under t.TempDir, its own
// environment built from scratch, and a clock the test owns.
//
// Nothing here reads the process environment. The developer running this
// suite has a real CLAUDE_CONFIG_DIR and real BRIGADE_* values, and a test
// that inherited them would read and write their own state.
type rig struct {
	t     *testing.T
	base  string
	cfg   string
	state string
	root  string
	env   []string
	now   time.Time
}

// fixedStart is the wall clock every rig starts at. A fixed instant makes
// lease expiry and the retention sweep exact rather than approximate.
var fixedStart = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

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
	}
	r.env = []string{
		"HOME=" + filepath.Join(base, "home"),
		"BRIGADE_CONFIG_DIR=" + r.cfg,
		"BRIGADE_STATE_DIR=" + r.state,
		"BRIGADE_FS_ROOT=" + r.root,
	}
	return r
}

// clock hands the adapter this rig's clock. It is read-only during a
// concurrent test, so two goroutines may run commands at once.
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
	return outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// ok runs a command that must succeed and returns its `result` object.
func (r *rig) ok(input string, args ...string) map[string]any {
	r.t.Helper()
	got := r.exec(input, args...)
	if got.code != 0 {
		r.t.Fatalf("%v: exit %d, stdout %s", args, got.code, got.stdout)
	}
	env := decode(r.t, got.stdout)
	result, isObject := env["result"].(map[string]any)
	if !isObject {
		r.t.Fatalf("%v: no result object in %s", args, got.stdout)
	}
	return result
}

// fails runs a command that must fail with one 4.6 code and returns the
// whole envelope, so a caller can assert on details and on byte identity.
func (r *rig) fails(wantCode string, wantExit int, input string, args ...string) outcome {
	r.t.Helper()
	got := r.exec(input, args...)
	if got.code != wantExit {
		r.t.Fatalf("%v: exit %d, want %d (stdout %s)", args, got.code, wantExit, got.stdout)
	}
	assertErrorCode(r.t, got.stdout, wantCode)
	return got
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
// `retryable` is PRESENT — an adapter MUST send it on every failing
// envelope (4.3, B-2), and a typed decode would silently default it.
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

// errorJSON returns the RAW bytes of the `error` member as the adapter
// emitted them, for the byte-identity assertions of 4.5.6 and 4.5.7.
// Re-encoding a decoded map would not do: json/v2 does not order map keys
// deterministically, so two identical envelopes could compare unequal and
// two different ones equal.
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

// team creates a team on the named profile and returns its ref and secret.
func (r *rig) team(profile, name string) (teamRef, secret string) {
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
func (r *rig) send(profile, from, to, body string) map[string]any {
	r.t.Helper()
	return r.ok(`{"sender_session_id":"`+from+`","recipient_session_id":"`+to+`","body":"`+body+`"}`,
		"--profile", profile, "message", "send")
}

// blockingStdin is a stdin that never delivers and never ends: a command
// that reads it hangs forever. It is how the tests prove the rule of 4.1
// that a command taking no input MUST NOT read stdin (B-1).
type blockingStdin struct{ done chan struct{} }

func newBlockingStdin(t *testing.T) *blockingStdin {
	t.Helper()
	b := &blockingStdin{done: make(chan struct{})}
	t.Cleanup(func() { close(b.done) })
	return b
}

func (b *blockingStdin) Read([]byte) (int, error) {
	<-b.done
	return 0, io.EOF
}

// absent asserts that a path does not exist.
func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists, want it absent (%v)", path, err)
	}
}

// chmod is os.Chmod under a name the tests can read: the strict reader
// refuses a credential or profile file that grants anything to group or
// other, and that refusal needs a file in exactly that state to test.
func chmod(path string, mode os.FileMode) error { return os.Chmod(path, mode) }

// lstat and contains keep the test files free of imports they use once.
func lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// rebind rewrites a profile's team_ref, which is what the conformance
// launcher's default --rebind hook does for both bundled adapters (9.2,
// C-26, C-43). The member name `team_ref` in team.json is therefore
// load-bearing, and this helper is the unit-test half of that contract.
func rebind(t *testing.T, r *rig, profile, teamRef string) {
	t.Helper()
	path := filepath.Join(r.cfg, "teams", profile, "team.json")
	data, err := os.ReadFile(path) //nolint:gosec // a path this test built under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["team_ref"] = teamRef
	next, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, next, 0o600); err != nil {
		t.Fatal(err)
	}
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

// mustMessages reads the `messages` array out of a `message receive`
// result.
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

// mustStrings reads a required string array out of a result. A required
// array is always present and is [] when empty (JSON convention 3).
func mustStrings(t *testing.T, result map[string]any, key string) []string {
	t.Helper()
	raw, ok := result[key].([]any)
	if !ok {
		t.Fatalf("member %q is missing or not an array in %v", key, result)
	}
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		value, isString := entry.(string)
		if !isString {
			t.Fatalf("member %q holds a non-string: %v", key, entry)
		}
		out = append(out, value)
	}
	return out
}

// asError is errors.As under a name the store tests can read.
func asError(err error, target **protocol.Error) bool { return errors.As(err, target) }
