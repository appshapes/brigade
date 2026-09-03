package fakeadapter

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// run drives Main with in-memory streams and a script written to a temp
// file, returning stdout, stderr and the exit code.
func run(t *testing.T, s Script, args []string, stdin string, environ ...string) (string, string, int) {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	if err := os.WriteFile(scriptPath, data, 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	full := append([]string{"--script", scriptPath}, args...)
	var out, errb bytes.Buffer
	code := Main(full, strings.NewReader(stdin), &out, &errb, environ)
	return out.String(), errb.String(), code
}

// TestDescribeDefault proves an empty script answers `describe` with a
// valid protocol_version "1" envelope on stdout.
func TestDescribeDefault(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := run(t, Script{}, []string{"describe"}, "")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	var env protocol.Envelope
	if err := protocol.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v (%q)", err, stdout)
	}
	if !env.OK {
		t.Fatal("describe envelope not ok")
	}
	var d protocol.DescribeResult
	if err := protocol.Decode([]byte(env.Result), &d); err != nil {
		t.Fatalf("describe result invalid: %v", err)
	}
	if d.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("protocol_version = %q, want %q", d.ProtocolVersion, protocol.ProtocolVersion)
	}
	if d.Adapter.Name != AdapterName {
		t.Errorf("adapter.name = %q, want %q", d.Adapter.Name, AdapterName)
	}
}

// TestDescribeCustom proves a scripted describe (a different protocol
// version and a narrow capability set) is returned verbatim.
func TestDescribeCustom(t *testing.T) {
	t.Parallel()
	stdout, _, code := run(t, Script{Describe: DescribeJSON("2", "message.receive")}, []string{"describe"}, "")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var env protocol.Envelope
	if err := protocol.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	var d protocol.DescribeResult
	if err := protocol.Decode([]byte(env.Result), &d); err != nil {
		t.Fatal(err)
	}
	if d.ProtocolVersion != "2" {
		t.Errorf("protocol_version = %q, want 2", d.ProtocolVersion)
	}
	if len(d.Capabilities) != 1 || d.Capabilities[0] != "message.receive" {
		t.Errorf("capabilities = %v, want [message.receive]", d.Capabilities)
	}
}

// TestErrorResponseExitStatus proves an error response emits the failing
// envelope and exits with the code's 4.6 status.
func TestErrorResponseExitStatus(t *testing.T) {
	t.Parallel()
	s := Script{Responses: map[string][]Response{
		"team create": {{Error: &protocol.ErrorObject{Code: protocol.CodeConflict, Message: "bound"}}},
	}}
	stdout, _, code := run(t, s, []string{"team", "create"}, `{"team_name":"x"}`)
	if code != 7 {
		t.Fatalf("exit = %d, want 7 (conflict)", code)
	}
	var env protocol.Envelope
	if err := protocol.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeConflict {
		t.Errorf("envelope = %+v, want a conflict error", env)
	}
}

// TestOrderedResponsesAdvanceAcrossProcesses proves the response list is
// consumed one per invocation and the last entry repeats — the mechanism
// behind "rate_limited on the third send".
func TestOrderedResponsesAdvanceAcrossProcesses(t *testing.T) {
	t.Parallel()
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	ok := jsontext.Value(`{"status":"accepted","message_id":"m","recipient_session_id":"r","created_at":"2026-01-01T00:00:00Z","duplicate":false,"hop_count":0}`)
	s := Script{Responses: map[string][]Response{
		"message send": {
			{Result: ok},
			{Result: ok},
			{Error: &protocol.ErrorObject{Code: protocol.CodeRateLimited, Message: "slow", RetryAfterMS: 1000}},
		},
	}}
	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	codes := make([]int, 5)
	for i := range codes {
		var out, errb bytes.Buffer
		codes[i] = Main([]string{"--script", scriptPath, "message", "send"}, strings.NewReader("{}"), &out, &errb, nil)
	}
	// success, success, then rate_limited (8) which repeats.
	want := []int{0, 0, 8, 8, 8}
	for i, w := range want {
		if codes[i] != w {
			t.Errorf("call %d exit = %d, want %d (sequence %v)", i, codes[i], w, codes)
		}
	}
}

// TestDumpRecordsArgvStdinAndEnv proves the dump captures what a child
// received, which is how adapterclient's isolation test observes the fork.
func TestDumpRecordsArgvStdinAndEnv(t *testing.T) {
	t.Parallel()
	dumpPath := filepath.Join(t.TempDir(), "dump.ndjson")
	s := Script{
		DumpFile:  dumpPath,
		Responses: map[string][]Response{"session register": {{Result: jsontext.Value(`{"session_id":"s"}`)}}},
	}
	_, _, code := run(t, s, []string{"session", "register"}, "a body line",
		"HOME=/home/x", "BRIGADE_PROFILE=work")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	var inv Invocation
	if err := json.Unmarshal(bytes.TrimSpace(data), &inv); err != nil {
		t.Fatalf("dump not JSON: %v", err)
	}
	if inv.Group != "session" || inv.Verb != "register" {
		t.Errorf("dump group/verb = %q/%q, want session/register", inv.Group, inv.Verb)
	}
	if inv.StdinBytes != len("a body line") {
		t.Errorf("stdin_bytes = %d, want %d", inv.StdinBytes, len("a body line"))
	}
	if inv.Env["HOME"] != "/home/x" || inv.Env["BRIGADE_PROFILE"] != "work" {
		t.Errorf("dump env = %v, want it to record HOME and BRIGADE_PROFILE", inv.Env)
	}
}

// TestStdoutBytesEmitsRawBytes proves the stdout_bytes option writes N
// bytes instead of an envelope (the runaway/non-JSON stdout path).
func TestStdoutBytesEmitsRawBytes(t *testing.T) {
	t.Parallel()
	s := Script{Responses: map[string][]Response{"session close": {{StdoutBytes: 100}}}}
	stdout, _, code := run(t, s, []string{"session", "close", "--session", "s1"}, "")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if len(stdout) != 100 || strings.Trim(stdout, "x") != "" {
		t.Errorf("stdout = %d bytes, want 100 'x' bytes", len(stdout))
	}
}

// TestNoScriptIsUsage proves a missing script (no --script, no env var) is
// a usage refusal, not a panic.
func TestNoScriptIsUsage(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	code := Main([]string{"describe"}, strings.NewReader(""), &out, &errb, nil)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb.String(), "no --script") {
		t.Errorf("stderr = %q, want a no-script message", errb.String())
	}
}

// TestScriptEnvVarFallback proves BRIGADE_FAKE_ADAPTER_SCRIPT is honoured
// only when no --script is given (a human running the binary by hand).
func TestScriptEnvVarFallback(t *testing.T) {
	t.Parallel()
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(scriptPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := Main([]string{"describe"}, strings.NewReader(""), &out, &errb, []string{ScriptEnvVar + "=" + scriptPath})
	if code != 0 {
		t.Fatalf("exit = %d via env-var script, want 0 (stderr %q)", code, errb.String())
	}
}

// TestDescribeJSONIsValid proves the helper builds a describe document that
// validates, so a test can rely on it.
func TestDescribeJSONIsValid(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1", "2"} {
		var d protocol.DescribeResult
		if err := protocol.Decode(DescribeJSON(version), &d); err != nil {
			t.Errorf("DescribeJSON(%q) is not valid: %v", version, err)
		}
		if d.ProtocolVersion != version {
			t.Errorf("DescribeJSON(%q) protocol_version = %q", version, d.ProtocolVersion)
		}
	}
}
