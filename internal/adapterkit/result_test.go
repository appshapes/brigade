package adapterkit_test

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// decodeEnvelope parses one written envelope and validates it against the
// protocol's own rules.
func decodeEnvelope(t *testing.T, raw []byte) *protocol.Envelope {
	t.Helper()
	var env protocol.Envelope
	if err := protocol.Unmarshal(raw, &env); err != nil {
		t.Fatalf("output is not a protocol envelope: %v\n%s", err, raw)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("envelope fails its own validation: %v\n%s", err, raw)
	}
	return &env
}

func TestWriteResultEnvelope(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	exit := adapterkit.WriteResult(&buf, map[string]any{"session_id": "s-1"})
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	out := buf.Bytes()
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Fatalf("envelope is not newline-terminated: %q", out)
	}
	if n := bytes.Count(out, []byte("\n")); n != 1 {
		t.Fatalf("output is %d lines, want exactly one document on one line", n)
	}
	env := decodeEnvelope(t, out)
	if !env.OK || env.ProtocolVersion != protocol.ProtocolVersion {
		t.Fatalf("ok=%v version=%q, want ok=true version=%q", env.OK, env.ProtocolVersion, protocol.ProtocolVersion)
	}
	var result map[string]string
	if err := json.Unmarshal(env.Result, &result); err != nil || result["session_id"] != "s-1" {
		t.Fatalf("result member did not round-trip: %v %v", result, err)
	}
}

func TestWriteErrorEnvelope(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	exit := adapterkit.WriteError(&buf, &protocol.Error{
		Code:    protocol.CodeUsage,
		Message: "stdin is a terminal",
	})
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 for usage", exit)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeUsage {
		t.Fatalf("envelope = %+v, want ok=false code=usage", env)
	}
	// `retryable` is REQUIRED by 4.3 even when false: check the member is
	// physically present, not merely zero after decoding.
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	errObj, ok := raw["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in %q", buf.String())
	}
	if _, ok := errObj["retryable"]; !ok {
		t.Fatalf("retryable member is absent from %q", buf.String())
	}
	if _, ok := raw["result"]; ok {
		t.Fatalf("failing envelope carries a result member: %q", buf.String())
	}
}

func TestWriteErrorRetryableAndAfter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	exit := adapterkit.WriteError(&buf, &protocol.Error{
		Code:         protocol.CodeRateLimited,
		Message:      "slow down",
		RetryAfterMS: 12000,
	})
	if exit != 8 {
		t.Fatalf("exit = %d, want 8", exit)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if !env.Error.Retryable || env.Error.RetryAfterMS != 12000 {
		t.Fatalf("error = %+v, want retryable=true retry_after_ms=12000", env.Error)
	}
}

func TestWriteErrorWrappedProtocolError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	wrapped := fmt.Errorf("loading profile: %w", &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "profile is not configured",
	})
	exit := adapterkit.WriteError(&buf, wrapped)
	if exit != 11 {
		t.Fatalf("exit = %d, want 11", exit)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if env.Error.Code != protocol.CodeConfig {
		t.Fatalf("code = %q, want config", env.Error.Code)
	}
}

func TestWriteErrorNeverEchoesUnclassifiedText(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	exit := adapterkit.WriteError(&buf, errors.New("sb_secret_boom: SQLSTATE 42501 at /Users/x"))
	if exit != 1 {
		t.Fatalf("exit = %d, want 1 for an unclassified error", exit)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if env.Error.Code != protocol.CodeInternal {
		t.Fatalf("code = %q, want internal", env.Error.Code)
	}
	if strings.Contains(buf.String(), "sb_secret_boom") || strings.Contains(buf.String(), "SQLSTATE") {
		t.Fatalf("raw error text reached the protocol stream: %q", buf.String())
	}
	if env.Error.Message != "internal error" {
		t.Fatalf("message = %q, want the fixed internal text", env.Error.Message)
	}
}

func TestWriteErrorNil(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	exit := adapterkit.WriteError(&buf, nil)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if env.Error.Code != protocol.CodeInternal {
		t.Fatalf("code = %q, want internal", env.Error.Code)
	}
}

func TestWriteResultUnmarshalableBecomesInternal(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	exit := adapterkit.WriteResult(&buf, make(chan int))
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	env := decodeEnvelope(t, buf.Bytes())
	if env.OK || env.Error.Code != protocol.CodeInternal {
		t.Fatalf("envelope = %+v, want a failing internal envelope", env)
	}
}

// runChildEcho re-executes this test binary as the echo child (see
// TestHelperChildEcho) with stdin connected to the given reader, and
// returns what the CHILD PROCESS wrote to each stream.
func runChildEcho(t *testing.T, stdin io.Reader) (stdout, stderr string) {
	t.Helper()
	//nolint:gosec // G204/G702: the argv re-executes this very test binary; no shell is involved
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperChildEcho$")
	cmd.Env = append(os.Environ(), "ADAPTERKIT_CHILD=echo")
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("child: %v\nstdout: %s\nstderr: %s", err, out.String(), errb.String())
	}
	return out.String(), errb.String()
}

// childEnvelope extracts the one envelope line from a child's stdout and
// asserts the test-framework noise is all that accompanies it.
func childEnvelope(t *testing.T, stdout string) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	var envelopes []string
	for _, l := range lines {
		if strings.HasPrefix(l, "{") {
			envelopes = append(envelopes, l)
		}
	}
	if len(envelopes) != 1 {
		t.Fatalf("child stdout carries %d JSON lines, want exactly one envelope:\n%s", len(envelopes), stdout)
	}
	return []byte(envelopes[0] + "\n")
}

func TestChildProcessResultEnvelopeOnStdoutOnly(t *testing.T) {
	t.Parallel()
	doc := `{"session_id":"s-1"}`
	stdout, stderr := runChildEcho(t, strings.NewReader(doc))
	env := decodeEnvelope(t, childEnvelope(t, stdout))
	if !env.OK {
		t.Fatalf("child refused a valid document: %s", stdout)
	}
	var result map[string]int
	if err := json.Unmarshal(env.Result, &result); err != nil || result["bytes"] != len(doc) {
		t.Fatalf("result = %v (%v), want bytes=%d", result, err, len(doc))
	}
	if strings.Contains(stderr, "protocol_version") {
		t.Fatalf("envelope material leaked to stderr: %q", stderr)
	}
}

func TestChildProcessOversizeEnvelopeOnStdoutOnly(t *testing.T) {
	t.Parallel()
	stdout, stderr := runChildEcho(t, bytes.NewReader(pattern(adapterkit.MaxInputBytes+1)))
	env := decodeEnvelope(t, childEnvelope(t, stdout))
	if env.OK || env.Error.Code != protocol.CodeInvalidInput {
		t.Fatalf("envelope = %+v, want ok=false code=invalid_input", env)
	}
	if strings.Contains(stderr, "protocol_version") {
		t.Fatalf("envelope material leaked to stderr: %q", stderr)
	}
}
