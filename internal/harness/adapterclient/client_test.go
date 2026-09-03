package adapterclient

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// ptr is a small pointer helper for the *int script fields.
func ptr[T any](v T) *T { return &v }

// readDump parses the fake's NDJSON dump file into its invocations.
func readDump(t *testing.T, path string) []fakeadapter.Invocation {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dump %s: %v", path, err)
	}
	var out []fakeadapter.Invocation
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var inv fakeadapter.Invocation
		if err := json.Unmarshal(line, &inv); err != nil {
			t.Fatalf("dump line is not JSON: %v (%q)", err, line)
		}
		out = append(out, inv)
	}
	return out
}

// mustProtocolError asserts err is a *protocol.Error and returns it.
func mustProtocolError(t *testing.T, err error) *protocol.Error {
	t.Helper()
	if err == nil {
		t.Fatal("got a nil error, want a *protocol.Error")
	}
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("error is %T (%v), want *protocol.Error", err, err)
	}
	return perr
}

// TestChildEnvironmentIsExactlyTheAllowList is the isolation proof
// (acceptance 5, U-27's spawn half): a hostile parent environment reaches
// no child, and the child sees exactly the allow-list plus the four
// computed BRIGADE_* — no more, no less.
func TestChildEnvironmentIsExactlyTheAllowList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "dump.ndjson")
	scriptPath := writeFakeScript(t, fakeadapter.Script{DumpFile: dumpPath})

	d := newDirs(t)
	c := &Client{
		Adapter:   config.Adapter{Argv: []string{fakeAdapterBin, "--script", scriptPath}, Source: config.SourceMap},
		Profile:   "default",
		ConfigDir: d.BrigadeConfig,
		StateDir:  d.BrigadeState,
		LogLevel:  "debug",
		Environ: []string{
			"PATH=/usr/bin:/bin",
			"HOME=/home/fake",
			"HTTPS_PROXY=http://proxy.local:8080",
			"SSL_CERT_FILE=/etc/ssl/cert.pem",
			"XDG_CONFIG_HOME=/xdg/config",
			// Hostile, all of which must be dropped:
			"BRIGADE_FS_ROOT=/tmp/evil-root",
			"BRIGADE_PROFILE=evil",
			"BRIGADE_STATE_DIR=/tmp/evil-state",
			"GODEBUG=http2debug=2",
			"NODE_OPTIONS=--max-old-space-size=1",
			"CLAUDE_CODE_MESSAGING_TOKEN=super-secret-token",
			"CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/1.sock",
		},
	}
	if _, err := c.Call(t.Context(), "describe", "", nil, nil); err != nil {
		t.Fatalf("describe: %v", err)
	}

	invs := readDump(t, dumpPath)
	if len(invs) != 1 {
		t.Fatalf("dump has %d invocations, want 1", len(invs))
	}
	env := invs[0].Env

	want := map[string]string{
		"PATH":               "/usr/bin:/bin",
		"HOME":               "/home/fake",
		"HTTPS_PROXY":        "http://proxy.local:8080",
		"SSL_CERT_FILE":      "/etc/ssl/cert.pem",
		"XDG_CONFIG_HOME":    "/xdg/config",
		"BRIGADE_PROFILE":    "default",
		"BRIGADE_CONFIG_DIR": d.BrigadeConfig,
		"BRIGADE_STATE_DIR":  d.BrigadeState,
		"BRIGADE_LOG_LEVEL":  "debug",
	}
	if len(env) != len(want) {
		t.Errorf("child env has %d keys, want %d\n got:  %v\n want: %v", len(env), len(want), env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("child env[%s] = %q, want %q", k, env[k], v)
		}
	}
	for _, forbidden := range []string{
		"BRIGADE_FS_ROOT", "GODEBUG", "NODE_OPTIONS",
		"CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_MESSAGING_SOCKET",
	} {
		if _, ok := env[forbidden]; ok {
			t.Errorf("child env carried %s=%q; it must be dropped", forbidden, env[forbidden])
		}
	}
	if env["BRIGADE_PROFILE"] == "evil" {
		t.Error("the hostile inherited BRIGADE_PROFILE reached the child")
	}
	if env["BRIGADE_STATE_DIR"] == "/tmp/evil-state" {
		t.Error("the hostile inherited BRIGADE_STATE_DIR reached the child")
	}
}

// TestAdapterErrorBecomesProtocolError proves a failing envelope the
// adapter PRODUCED comes back as a *protocol.Error with the wire code,
// message, retry_after_ms and details, and that retryability is recomputed
// from the code rather than trusted from the wire.
func TestAdapterErrorBecomesProtocolError(t *testing.T) {
	t.Parallel()
	scriptPath := writeFakeScript(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"message send": {{Error: &protocol.ErrorObject{
				Code: protocol.CodeRateLimited, Message: "slow down",
				Retryable: true, RetryAfterMS: 12000,
				Details: map[string]string{"reason": "send_per_minute"},
			}}},
		},
	})
	c := fakeClient(t, scriptPath)

	env, err := c.Call(t.Context(), "message", "send", nil, &protocol.SendRequest{
		SenderSessionID: "a", RecipientSessionID: "b", Body: "x",
	})
	if env == nil {
		t.Fatal("envelope is nil; a failing envelope the adapter produced should be returned")
	}
	if env.OK {
		t.Fatal("envelope ok = true, want false")
	}
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeRateLimited {
		t.Errorf("code = %q, want rate_limited", perr.Code)
	}
	if perr.Message != "slow down" {
		t.Errorf("message = %q, want the adapter's message", perr.Message)
	}
	if perr.RetryAfterMS != 12000 {
		t.Errorf("retry_after_ms = %d, want 12000", perr.RetryAfterMS)
	}
	if !perr.Object().Retryable {
		t.Error("rate_limited should be retryable when re-rendered from the code")
	}

	// The converse: a wire `retryable: true` on a code the taxonomy calls
	// final is NOT honoured — retryability comes from the code, never the
	// adapter's say-so (4.3).
	scriptPath = writeFakeScript(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"message send": {{Error: &protocol.ErrorObject{
				Code: protocol.CodeUnauthenticated, Message: "sign in", Retryable: true,
			}}},
		},
	})
	c = fakeClient(t, scriptPath)
	env, err = c.Call(t.Context(), "message", "send", nil, &protocol.SendRequest{
		SenderSessionID: "a", RecipientSessionID: "b", Body: "x",
	})
	if env == nil || !env.Error.Retryable {
		t.Fatalf("control: the fake did not put retryable=true on the wire: %+v", env)
	}
	perr = mustProtocolError(t, err)
	if perr.Code != protocol.CodeUnauthenticated {
		t.Errorf("code = %q, want unauthenticated", perr.Code)
	}
	if perr.Object().Retryable {
		t.Error("unauthenticated re-rendered as retryable because the wire said so")
	}
}

// TestUnknownAdapterCodeBecomesInternal proves an error code the harness
// does not recognise is normalised to `internal` (4.6): a novel code cannot
// buy an unmapped exit status.
func TestUnknownAdapterCodeBecomesInternal(t *testing.T) {
	t.Parallel()
	scriptPath := writeFakeScript(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"team members": {{Error: &protocol.ErrorObject{Code: protocol.Code("teapot"), Message: "?"}}},
		},
	})
	c := fakeClient(t, scriptPath)
	_, err := c.Call(t.Context(), "team", "members", nil, nil)
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeInternal {
		t.Errorf("unknown code mapped to %q, want internal", perr.Code)
	}
}

// TestSpawnBrokeReturnsNilEnvelope proves the "adapter broke" versus
// "adapter said" distinction: a runaway/non-JSON stdout is an error Spawn
// produced, with a nil envelope, while a failing envelope keeps its
// envelope.
func TestSpawnBrokeReturnsNilEnvelope(t *testing.T) {
	t.Parallel()
	scriptPath := writeFakeScript(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"session close": {{StdoutBytes: 32}}, // 32 bytes of 'x', not JSON
		},
	})
	c := fakeClient(t, scriptPath)
	env, err := c.Call(t.Context(), "session", "close", sessionFlag("s1"), nil)
	if env != nil {
		t.Errorf("envelope = %+v, want nil for an adapter that broke", env)
	}
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeInternal {
		t.Errorf("code = %q, want internal for non-JSON stdout", perr.Code)
	}
}

// TestSpawnMappedErrors covers the mapping Spawn owns end to end: a
// timeout, a signal death, a stdout overflow and a missing executable each
// arrive as Spawn returns them (unavailable/internal, with a nil
// envelope).
func TestSpawnMappedErrors(t *testing.T) {
	t.Parallel()

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		scriptPath := writeFakeScript(t, fakeadapter.Script{
			Responses: map[string][]fakeadapter.Response{
				"team members": {{SleepMS: 10000}},
			},
		})
		c := fakeClient(t, scriptPath)
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		_, err := c.Call(ctx, "team", "members", nil, nil)
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != "timeout" {
			t.Errorf("timeout mapped to %q/%q, want unavailable/timeout", perr.Code, perr.Details["reason"])
		}
	})

	t.Run("signal death (exit 137)", func(t *testing.T) {
		t.Parallel()
		scriptPath := writeFakeScript(t, fakeadapter.Script{
			Responses: map[string][]fakeadapter.Response{
				"team members": {{ExitCode: ptr(137)}},
			},
		})
		c := fakeClient(t, scriptPath)
		_, err := c.Call(t.Context(), "team", "members", nil, nil)
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeUnavailable {
			t.Errorf("signal death mapped to %q, want unavailable", perr.Code)
		}
		if perr.Details["signal"] != "SIGKILL" {
			t.Errorf("signal death carried details.signal = %q, want SIGKILL (exit 137 = 128+9)", perr.Details["signal"])
		}
	})

	t.Run("stdout overflow", func(t *testing.T) {
		t.Parallel()
		scriptPath := writeFakeScript(t, fakeadapter.Script{
			Responses: map[string][]fakeadapter.Response{
				"team members": {{StdoutBytes: 5 << 20}}, // > the 4 MiB cap
			},
		})
		c := fakeClient(t, scriptPath)
		_, err := c.Call(t.Context(), "team", "members", nil, nil)
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeInternal || perr.Details["reason"] != "stdout_overflow" {
			t.Errorf("overflow mapped to %q/%q, want internal/stdout_overflow", perr.Code, perr.Details["reason"])
		}
	})

	t.Run("missing executable (absolute path)", func(t *testing.T) {
		t.Parallel()
		d := newDirs(t)
		c := &Client{
			Adapter:   config.Adapter{Argv: []string{filepath.Join(t.TempDir(), "does-not-exist")}, Source: config.SourceMap},
			Profile:   "default",
			ConfigDir: d.BrigadeConfig,
			StateDir:  d.BrigadeState,
			Environ:   []string{"PATH=/usr/bin:/bin"},
		}
		_, err := c.Call(t.Context(), "describe", "", nil, nil)
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != "adapter_not_found" {
			t.Errorf("missing executable mapped to %q/%q, want unavailable/adapter_not_found", perr.Code, perr.Details["reason"])
		}
	})

	t.Run("missing executable (bare name on PATH)", func(t *testing.T) {
		t.Parallel()
		d := newDirs(t)
		c := &Client{
			Adapter:   config.Adapter{Argv: []string{"brigade-not-a-real-binary-xyz"}, Source: config.SourceMap},
			Profile:   "default",
			ConfigDir: d.BrigadeConfig,
			StateDir:  d.BrigadeState,
			Environ:   []string{"PATH=/usr/bin:/bin"},
		}
		_, err := c.Call(t.Context(), "describe", "", nil, nil)
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != "adapter_not_found" {
			t.Errorf("bare-name miss mapped to %q/%q, want unavailable/adapter_not_found", perr.Code, perr.Details["reason"])
		}
	})
}

// TestRawAdapterStderrNeverInError is U-24's harness half: whatever an
// adapter writes to stderr never appears in a returned error, whether the
// adapter produced a clean error envelope or broke.
func TestRawAdapterStderrNeverInError(t *testing.T) {
	t.Parallel()
	const canary = "STDERR-LEAK-CANARY-9c1f2a-must-not-surface"

	t.Run("adapter said", func(t *testing.T) {
		t.Parallel()
		scriptPath := writeFakeScript(t, fakeadapter.Script{
			Responses: map[string][]fakeadapter.Response{
				"message send": {{
					Stderr: canary,
					Error:  &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "backend unreachable"},
				}},
			},
		})
		c := fakeClient(t, scriptPath)
		_, err := c.Call(t.Context(), "message", "send", nil, &protocol.SendRequest{
			SenderSessionID: "a", RecipientSessionID: "b", Body: "x",
		})
		perr := mustProtocolError(t, err)
		if strings.Contains(perr.Error(), canary) || strings.Contains(perr.Message, canary) {
			t.Errorf("returned error carried adapter stderr: %q", perr.Error())
		}
	})

	t.Run("adapter broke", func(t *testing.T) {
		t.Parallel()
		scriptPath := writeFakeScript(t, fakeadapter.Script{
			Responses: map[string][]fakeadapter.Response{
				"message send": {{Stderr: canary, StdoutBytes: 16}},
			},
		})
		c := fakeClient(t, scriptPath)
		_, err := c.Call(t.Context(), "message", "send", nil, &protocol.SendRequest{
			SenderSessionID: "a", RecipientSessionID: "b", Body: "x",
		})
		perr := mustProtocolError(t, err)
		if strings.Contains(perr.Error(), canary) {
			t.Errorf("returned error carried adapter stderr: %q", perr.Error())
		}
	})
}

// TestDescribeIsCachedPerAdapter proves `describe` runs at most once per
// adapter within the process: two Describe calls spawn one child.
func TestDescribeIsCachedPerAdapter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "dump.ndjson")
	scriptPath := writeFakeScript(t, fakeadapter.Script{DumpFile: dumpPath})
	c := fakeClient(t, scriptPath)

	for range 2 {
		if _, err := c.Describe(t.Context()); err != nil {
			t.Fatalf("describe: %v", err)
		}
	}
	invs := readDump(t, dumpPath)
	if len(invs) != 1 {
		t.Fatalf("describe spawned %d children, want exactly 1 (the rest cached)", len(invs))
	}
}

// TestDescribeProtocolMismatch proves a describe whose protocol version is
// not this harness's is protocol_mismatch (exit 10), carrying the adapter's
// own name and version for whoami.
func TestDescribeProtocolMismatch(t *testing.T) {
	t.Parallel()
	scriptPath := writeFakeScript(t, fakeadapter.Script{
		Describe: fakeadapter.DescribeJSON("2"),
	})
	c := fakeClient(t, scriptPath)
	_, err := c.Describe(t.Context())
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeProtocolMismatch {
		t.Fatalf("code = %q, want protocol_mismatch", perr.Code)
	}
	if perr.Code.Exit() != 10 {
		t.Errorf("exit = %d, want 10", perr.Code.Exit())
	}
	if perr.Details["adapter_name"] != fakeadapter.AdapterName {
		t.Errorf("details.adapter_name = %q, want %q", perr.Details["adapter_name"], fakeadapter.AdapterName)
	}
	if perr.Details["adapter_protocol_version"] != "2" {
		t.Errorf("details.adapter_protocol_version = %q, want 2", perr.Details["adapter_protocol_version"])
	}
}

// TestDescribeCapabilitiesReachTheCaller proves the cached describe result
// carries the capabilities P3-3/P3-5 gate on.
func TestDescribeCapabilitiesReachTheCaller(t *testing.T) {
	t.Parallel()
	scriptPath := writeFakeScript(t, fakeadapter.Script{
		Describe: fakeadapter.DescribeJSON(protocol.ProtocolVersion, "message.receive", "message.watch.push"),
	})
	c := fakeClient(t, scriptPath)
	d, err := c.Describe(t.Context())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !hasCapability(d, "message.watch.push") {
		t.Errorf("capabilities %v missing message.watch.push", d.Capabilities)
	}
	if hasCapability(d, "session.resume") {
		t.Errorf("capabilities %v unexpectedly has session.resume", d.Capabilities)
	}
}

func hasCapability(d *protocol.DescribeResult, want string) bool {
	for _, c := range d.Capabilities {
		if c == want {
			return true
		}
	}
	return false
}

// TestBundledAdapterArgv proves an empty (bundled) adapter resolves to this
// executable's `adapter supabase` prefix at spawn time, and the profile,
// group and verb follow.
func TestBundledAdapterArgv(t *testing.T) {
	t.Parallel()
	c := &Client{Adapter: config.Adapter{Bundled: true, Source: config.SourceBundled}, Profile: "work"}
	argv, err := c.argv("session", "list", []string{"--include-offline"})
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	self, _ := os.Executable()
	want := []string{self, "adapter", "supabase", "--profile", "work", "session", "list", "--include-offline"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

// TestDescribeArgvHasNoVerb proves the describe argv omits an empty verb.
func TestDescribeArgvHasNoVerb(t *testing.T) {
	t.Parallel()
	c := &Client{Adapter: config.Adapter{Argv: []string{"/opt/adapter"}, Source: config.SourceMap}, Profile: "p"}
	argv, err := c.argv("describe", "", nil)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	want := []string{"/opt/adapter", "--profile", "p", "describe"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

// spawnRecorder is the U-25 seam in use: a Client.Spawn that records every
// SpawnSpec and answers from a script without starting a process.
type spawnRecorder struct {
	mu    sync.Mutex
	specs []adapterkit.SpawnSpec
	reply func(spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error)
}

func (r *spawnRecorder) spawn(_ context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.reply(spec)
}

func (r *spawnRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.specs)
}

// TestSpawnSeamRecordsEveryChild proves the injectable spawn seam P3-3/P3-4
// tests need (brief section 4): a recorder sees every child's argv,
// environment and stdin without a process being started, can assert "zero
// spawns", and shows the messaging token absent from every child's argv
// and environment even when the parent's environ carries it.
func TestSpawnSeamRecordsEveryChild(t *testing.T) {
	t.Parallel()
	const msgTok = "cc-messaging-secret-MUST-NOT-CROSS-7d3e"
	okEnvelope := func(result string) *adapterkit.SpawnResult {
		return &adapterkit.SpawnResult{Envelope: &protocol.Envelope{
			OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: jsontext.Value(result),
		}}
	}
	rec := &spawnRecorder{reply: func(spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if len(spec.Argv) > 0 && spec.Argv[len(spec.Argv)-1] == "describe" {
			return okEnvelope(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion))), nil
		}
		return okEnvelope(`{"acked":["m1"],"unknown":[]}`), nil
	}}
	d := newDirs(t)
	// A unique argv keeps the process-wide describe cache from crossing tests.
	adapterPath := filepath.Join(t.TempDir(), "recorded-adapter")
	c := &Client{
		Adapter:   config.Adapter{Argv: []string{adapterPath, "--root", "/x"}, Source: config.SourceMap},
		Profile:   "default",
		ConfigDir: d.BrigadeConfig,
		StateDir:  d.BrigadeState,
		Environ:   []string{"PATH=/usr/bin:/bin", "CLAUDE_CODE_MESSAGING_TOKEN=" + msgTok, "BRIGADE_PROFILE=evil"},
		Spawn:     rec.spawn,
	}
	if rec.count() != 0 {
		t.Fatalf("a fresh client spawned %d children", rec.count())
	}
	if _, err := c.Describe(t.Context()); err != nil {
		t.Fatalf("describe through the seam: %v", err)
	}
	ack, err := c.Ack(t.Context(), "s1", &protocol.AckRequest{MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatalf("ack through the seam: %v", err)
	}
	if len(ack.Acked) != 1 || ack.Acked[0] != "m1" {
		t.Errorf("ack result = %+v, want m1 acked", ack)
	}
	if rec.count() != 2 {
		t.Fatalf("recorded %d spawns, want 2 (describe, ack)", rec.count())
	}
	describe, ackSpec := rec.specs[0], rec.specs[1]
	wantArgv := []string{adapterPath, "--root", "/x", "--profile", "default", "message", "ack", "--session", "s1"}
	if strings.Join(ackSpec.Argv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Errorf("ack argv = %v, want %v", ackSpec.Argv, wantArgv)
	}
	if describe.Argv[len(describe.Argv)-1] != "describe" || describe.Stdin != nil {
		t.Errorf("describe spec = argv %v stdin %q, want a bare describe with no stdin", describe.Argv, describe.Stdin)
	}
	if !strings.Contains(string(ackSpec.Stdin), `"message_ids":["m1"]`) {
		t.Errorf("ack stdin = %q, want the ack document", ackSpec.Stdin)
	}
	for i, spec := range rec.specs {
		for _, arg := range spec.Argv {
			if strings.Contains(arg, msgTok) {
				t.Errorf("spawn %d: the token is on argv: %v", i, spec.Argv)
			}
		}
		for _, entry := range spec.Env {
			if strings.Contains(entry, msgTok) || strings.HasPrefix(entry, "CLAUDE_CODE_MESSAGING_") {
				t.Errorf("spawn %d: the token or its socket reached the child environment: %q", i, entry)
			}
			if entry == "BRIGADE_PROFILE=evil" {
				t.Errorf("spawn %d: the hostile inherited BRIGADE_PROFILE reached the child", i)
			}
		}
		if !slices.Contains(spec.Env, "BRIGADE_PROFILE=default") || !slices.Contains(spec.Env, "BRIGADE_STATE_DIR="+d.BrigadeState) {
			t.Errorf("spawn %d: the computed BRIGADE_* are missing from %v", i, spec.Env)
		}
	}
}
