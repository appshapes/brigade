//go:build darwin || linux

package adapterkit_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// Every child in this file is a REAL process: a /bin/sh script written to
// the test's own directory and exec'd directly (argv arrays, no `sh -c`),
// because the four mapped failure modes — timeout, signal death, missing
// executable, stdout overflow — only exist across a process boundary.

// childPATH is the only PATH the scripts get: enough to find sleep, cat,
// dd and wc on both supported platforms, and handed over explicitly
// because Spawn never inherits an environment.
const childPATH = "PATH=/usr/bin:/bin"

// writeChildScript writes an executable /bin/sh script into the test's
// directory and returns its path.
func writeChildScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child.sh")
	//nolint:gosec // G306: a test child must be executable; 0700 keeps it owner-only, matching the repo's G302 dir rule
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("write child script: %v", err)
	}
	return path
}

// envelopeLine renders one VALID success envelope through the kit's own
// printer, without the trailing newline (scripts add their own).
func envelopeLine(t *testing.T, result any) string {
	t.Helper()
	var buf bytes.Buffer
	if exit := adapterkit.WriteResult(&buf, result); exit != 0 {
		t.Fatalf("building the test envelope failed with exit %d", exit)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// errorEnvelopeLine renders one VALID failing envelope the same way.
func errorEnvelopeLine(t *testing.T, perr *protocol.Error) (line string, exit int) {
	t.Helper()
	var buf bytes.Buffer
	exit = adapterkit.WriteError(&buf, perr)
	return strings.TrimRight(buf.String(), "\n"), exit
}

// mustProtocolError asserts Spawn's error is the mapped *protocol.Error.
func mustProtocolError(t *testing.T, err error) *protocol.Error {
	t.Helper()
	if err == nil {
		t.Fatal("Spawn returned a nil error, want a mapped *protocol.Error")
	}
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("Spawn error is %T (%v), want *protocol.Error", err, err)
	}
	return perr
}

// resultField decodes one string-keyed member out of a success envelope.
func resultField(t *testing.T, res *adapterkit.SpawnResult, key string) string {
	t.Helper()
	if res == nil || res.Envelope == nil || !res.Envelope.OK {
		t.Fatalf("no success envelope to read %q from: %+v", key, res)
	}
	var m map[string]any
	if err := json.Unmarshal(res.Envelope.Result, &m); err != nil {
		t.Fatalf("result member does not decode: %v", err)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("result has no %q member: %v", key, m)
	}
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		t.Fatalf("result member %q is %T, want a scalar", key, v)
		return ""
	}
}

// fileSize returns the size of path, or 0 when it does not exist yet.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// assertChildStopped proves the child's write loop is dead: the file it
// was appending to stops growing once Spawn has returned.
func assertChildStopped(t *testing.T, heartbeat string) {
	t.Helper()
	before := fileSize(t, heartbeat)
	time.Sleep(300 * time.Millisecond)
	if after := fileSize(t, heartbeat); after != before {
		t.Fatalf("child kept writing after Spawn returned: heartbeat grew %d -> %d bytes", before, after)
	}
}

func TestSpawnSuccessEnvelope(t *testing.T) {
	t.Parallel()
	script := writeChildScript(t, "printf '%s\\n' '"+envelopeLine(t, map[string]string{"session_id": "s-1"})+"'")
	res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
		Argv: []string{script},
		Env:  []string{childPATH},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if res.ExitCode != 0 || res.WaitDelayExpired {
		t.Fatalf("res = %+v, want exit 0 without a wait-delay expiry", res)
	}
	if got := resultField(t, res, "session_id"); got != "s-1" {
		t.Fatalf("result.session_id = %q, want s-1", got)
	}
}

func TestSpawnStdinDocument(t *testing.T) {
	t.Parallel()
	// The child measures what actually arrived on its stdin and reports
	// it inside a valid envelope.
	script := writeChildScript(t,
		`n=$(cat | wc -c | tr -d ' ')`+"\n"+
			`printf '{"ok":true,"protocol_version":"`+protocol.ProtocolVersion+`","result":{"stdin_bytes":%s}}\n' "$n"`)

	t.Run("document fed from a bytes.Reader", func(t *testing.T) {
		t.Parallel()
		doc := []byte(`{"hello":"adapter"}`)
		res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
			Argv:  []string{script},
			Env:   []string{childPATH},
			Stdin: doc,
		})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		if got := resultField(t, res, "stdin_bytes"); got != strconv.Itoa(len(doc)) {
			t.Fatalf("child saw %s stdin bytes, want %d", got, len(doc))
		}
	})

	t.Run("nil Stdin is no stdin at all", func(t *testing.T) {
		t.Parallel()
		// A child that reads a nil Stdin must see immediate EOF, not a
		// pipe it can block on: this test finishing at all is the point.
		res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
			Argv: []string{script},
			Env:  []string{childPATH},
		})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		if got := resultField(t, res, "stdin_bytes"); got != "0" {
			t.Fatalf("child saw %s stdin bytes with a nil Stdin, want 0", got)
		}
	})
}

func TestSpawnTimeout(t *testing.T) {
	t.Parallel()
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	script := writeChildScript(t, `while :; do echo beat >>"$1"; sleep 0.05; done`)

	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := adapterkit.Spawn(ctx, adapterkit.SpawnSpec{
		Argv:      []string{script, heartbeat},
		Env:       []string{childPATH},
		WaitDelay: 500 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if res != nil {
		t.Fatalf("Spawn returned a result on timeout: %+v", res)
	}
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeUnavailable {
		t.Fatalf("code = %q, want unavailable", perr.Code)
	}
	if got := perr.Code.Exit(); got != 9 {
		t.Fatalf("exit = %d, want 9", got)
	}
	// THE distinguishing assertion (plan 4.6): the deadline is reported
	// as a timeout, even though what wait(2) saw was the SIGTERM death
	// the cancel itself caused. A classifier keyed on cmd.Wait's error
	// would answer details.signal=SIGTERM here and this test would fail.
	if got := perr.Details["reason"]; got != "timeout" {
		t.Fatalf(`details.reason = %q, want "timeout" (details: %v)`, got, perr.Details)
	}
	if sig, ok := perr.Details["signal"]; ok {
		t.Fatalf("timeout mapping carries details.signal=%q; the signal is an effect, not the cause", sig)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Spawn took %v against a 250ms deadline and a 500ms wait delay", elapsed)
	}
	assertChildStopped(t, heartbeat)
}

func TestSpawnSignalDeath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sig string
	}{
		// SIGKILL cannot be the shadow of our own SIGTERM cancel; SIGTERM
		// proves an externally-terminated child is still reported as a
		// signal death, not laundered into a timeout, while the context
		// is live.
		{name: "SIGKILL", sig: "KILL"},
		{name: "SIGTERM", sig: "TERM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script := writeChildScript(t, "kill -"+tc.sig+" $$")
			res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
				Argv: []string{script},
				Env:  []string{childPATH},
			})
			if res != nil {
				t.Fatalf("Spawn returned a result for a signal death: %+v", res)
			}
			perr := mustProtocolError(t, err)
			if perr.Code != protocol.CodeUnavailable {
				t.Fatalf("code = %q, want unavailable", perr.Code)
			}
			if got := perr.Code.Exit(); got != 9 {
				t.Fatalf("exit = %d, want 9", got)
			}
			if got := perr.Details["signal"]; got != tc.name {
				t.Fatalf("details.signal = %q, want %q (details: %v)", got, tc.name, perr.Details)
			}
			// The counterpart of the timeout test's distinguishing
			// assertion: no deadline was involved, so no "reason".
			if reason, ok := perr.Details["reason"]; ok {
				t.Fatalf("signal mapping carries details.reason=%q; nothing timed out here", reason)
			}
		})
	}
}

func TestSpawnMissingExecutable(t *testing.T) {
	t.Parallel()
	t.Run("absolute path", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "no-such-adapter")
		res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
			Argv: []string{missing},
			Env:  []string{childPATH},
		})
		if res != nil {
			t.Fatalf("Spawn returned a result for a missing executable: %+v", res)
		}
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeUnavailable || perr.Code.Exit() != 9 {
			t.Fatalf("code = %q (exit %d), want unavailable (9)", perr.Code, perr.Code.Exit())
		}
		if got := perr.Details["reason"]; got != "adapter_not_found" {
			t.Fatalf(`details.reason = %q, want "adapter_not_found"`, got)
		}
	})
	t.Run("PATH search", func(t *testing.T) {
		t.Parallel()
		// A bare name is resolved by exec's PATH search, and on go1.27.0
		// that miss is exec.ErrNotFound, which does NOT wrap
		// fs.ErrNotExist (measured in P1-3; plan 4.6's os.ErrNotExist
		// predicate alone misses it — P1-4 decision 9). Both misses must
		// land on the same mapping.
		res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
			Argv: []string{"brigade-no-such-adapter-8f3a"},
			Env:  []string{"PATH=" + t.TempDir()},
		})
		if res != nil {
			t.Fatalf("Spawn returned a result for a missing executable: %+v", res)
		}
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeUnavailable || perr.Code.Exit() != 9 {
			t.Fatalf("code = %q (exit %d), want unavailable (9)", perr.Code, perr.Code.Exit())
		}
		if got := perr.Details["reason"]; got != "adapter_not_found" {
			t.Fatalf(`details.reason = %q, want "adapter_not_found"`, got)
		}
	})
}

func TestSpawnStdoutThatIsNotAnEnvelope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, reason string
	}{
		{name: "not JSON at all", body: `echo "adapter crashed: stack trace at main.go:42"`, reason: "stdout_not_json"},
		{name: "valid JSON, invalid envelope", body: `printf '{"ok":true}\n'`, reason: "stdout_invalid_envelope"},
		// Two documents on one stdout must fail as a whole: this is the
		// strictness that keeps "envelope plus trailing noise" from ever
		// parsing, and it is why these tests can trust a success parse.
		{name: "two envelopes", body: "printf '%s\\n%s\\n' '" +
			`{"ok":true,"protocol_version":"` + protocol.ProtocolVersion + `","result":{}}` + "' '" +
			`{"ok":true,"protocol_version":"` + protocol.ProtocolVersion + `","result":{}}` + "'", reason: "stdout_not_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script := writeChildScript(t, tc.body)
			res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
				Argv: []string{script},
				Env:  []string{childPATH},
			})
			if res != nil {
				t.Fatalf("Spawn returned a result for unparseable stdout: %+v", res)
			}
			perr := mustProtocolError(t, err)
			if perr.Code != protocol.CodeInternal || perr.Code.Exit() != 1 {
				t.Fatalf("code = %q (exit %d), want internal (1)", perr.Code, perr.Code.Exit())
			}
			if got := perr.Details["reason"]; got != tc.reason {
				t.Fatalf("details.reason = %q, want %q", got, tc.reason)
			}
			if strings.Contains(perr.Message, "stack trace") {
				t.Fatalf("child stdout text leaked into the mapped message: %q", perr.Message)
			}
		})
	}
}

func TestSpawnAdapterErrorEnvelopePassesThrough(t *testing.T) {
	t.Parallel()
	line, exit := errorEnvelopeLine(t, &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: "stdin exceeds the protocol cap",
	})
	script := writeChildScript(t, "printf '%s\\n' '"+line+"'\nexit "+strconv.Itoa(exit))
	res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
		Argv: []string{script},
		Env:  []string{childPATH},
	})
	if err != nil {
		t.Fatalf("Spawn: %v — an adapter that reports its own error in a valid envelope is speaking the protocol", err)
	}
	if res.Envelope.OK || res.Envelope.Error == nil {
		t.Fatalf("envelope = %+v, want a failing envelope", res.Envelope)
	}
	if res.Envelope.Error.Code != protocol.CodeInvalidInput {
		t.Fatalf("error.code = %q, want invalid_input", res.Envelope.Error.Code)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want the adapter's own 3", res.ExitCode)
	}
}

func TestSpawnOverflowCancelsRunawayChild(t *testing.T) {
	t.Parallel()
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	// 5 MiB of stdout, then an endless heartbeat loop: a Spawn that only
	// truncated would leave this child running until the outer deadline.
	script := writeChildScript(t,
		"dd if=/dev/zero bs=1048576 count=5 2>/dev/null\n"+
			`while :; do echo beat >>"$1"; sleep 0.05; done`)

	// The outer deadline exists so a truncate-only implementation shows
	// up as a wrong reason and a slow return, not as a hung test.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	res, err := adapterkit.Spawn(ctx, adapterkit.SpawnSpec{
		Argv:      []string{script, heartbeat},
		Env:       []string{childPATH},
		WaitDelay: 500 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if res != nil {
		t.Fatalf("Spawn returned a result past the stdout cap: %+v", res)
	}
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeInternal || perr.Code.Exit() != 1 {
		t.Fatalf("code = %q (exit %d), want internal (1)", perr.Code, perr.Code.Exit())
	}
	if got := perr.Details["reason"]; got != "stdout_overflow" {
		t.Fatalf(`details.reason = %q, want "stdout_overflow"`, got)
	}
	// The cap must CANCEL the child, not merely stop recording: the
	// return happens long before the outer deadline, which is still live.
	if ctx.Err() != nil {
		t.Fatalf("outer context expired (%v): the cap did not cancel the child, the deadline did", ctx.Err())
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Spawn took %v to stop a runaway child; the cap should have cancelled it immediately", elapsed)
	}
	assertChildStopped(t, heartbeat)
}

func TestSpawnStdoutCapBoundary(t *testing.T) {
	t.Parallel()
	// Build a VALID envelope padded to exactly MaxAdapterStdout bytes of
	// stdout (newline included): "past 4 MiB" must mean past, not at.
	overhead := len(envelopeLine(t, map[string]string{"pad": ""})) + 1
	pad := strings.Repeat("a", adapterkit.MaxAdapterStdout-overhead)
	exact := envelopeLine(t, map[string]string{"pad": pad}) + "\n"
	if len(exact) != adapterkit.MaxAdapterStdout {
		t.Fatalf("test construction: payload is %d bytes, want exactly %d", len(exact), adapterkit.MaxAdapterStdout)
	}

	t.Run("exactly at the cap parses", func(t *testing.T) {
		t.Parallel()
		payload := filepath.Join(t.TempDir(), "payload")
		if err := os.WriteFile(payload, []byte(exact), 0o600); err != nil {
			t.Fatal(err)
		}
		script := writeChildScript(t, `cat "$1"`)
		res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
			Argv: []string{script, payload},
			Env:  []string{childPATH},
		})
		if err != nil {
			t.Fatalf("Spawn refused a stdout of exactly %d bytes: %v", adapterkit.MaxAdapterStdout, err)
		}
		if got := len(resultField(t, res, "pad")); got != len(pad) {
			t.Fatalf("padded result did not round-trip: %d bytes, want %d", got, len(pad))
		}
	})

	t.Run("one byte past the cap overflows", func(t *testing.T) {
		t.Parallel()
		payload := filepath.Join(t.TempDir(), "payload")
		if err := os.WriteFile(payload, append([]byte(exact), 'x'), 0o600); err != nil {
			t.Fatal(err)
		}
		script := writeChildScript(t, `cat "$1"`)
		res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
			Argv: []string{script, payload},
			Env:  []string{childPATH},
		})
		if res != nil {
			t.Fatalf("Spawn returned a result one byte past the cap: %+v", res)
		}
		perr := mustProtocolError(t, err)
		if perr.Code != protocol.CodeInternal || perr.Details["reason"] != "stdout_overflow" {
			t.Fatalf("mapping = %q/%v, want internal/stdout_overflow", perr.Code, perr.Details)
		}
	})
}

func TestSpawnWaitDelayGrandchildResultStands(t *testing.T) {
	t.Parallel()
	// The child misbehaves exactly as 7.3 warns — it hands its stdout to
	// a grandchild — but exits 0 with a valid result already printed.
	// The grandchild outlives the test by a wide margin on purpose: the
	// bound below is a hang catcher against ITS lifetime, not a
	// performance bound on the wait delay (a 5 s bound tripped at 5.56 s
	// under a loaded -race run whose isolated time is 0.5 s).
	script := writeChildScript(t,
		"printf '%s\\n' '"+envelopeLine(t, map[string]string{"session_id": "s-wd"})+"'\n"+
			"sleep 30 &\n"+
			"exit 0")
	var logBuf bytes.Buffer
	childLog := slog.New(slog.NewJSONHandler(&logBuf, nil))

	start := time.Now()
	res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
		Argv:      []string{script},
		Env:       []string{childPATH},
		WaitDelay: 300 * time.Millisecond,
		Logger:    childLog,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Spawn: %v — a parsed result with exit 0 stands even past the wait delay", err)
	}
	if !res.WaitDelayExpired {
		t.Fatal("WaitDelayExpired = false, want true for a grandchild-held stdout")
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	if got := resultField(t, res, "session_id"); got != "s-wd" {
		t.Fatalf("result.session_id = %q, want s-wd", got)
	}
	if !strings.Contains(logBuf.String(), "wait_delay_expired") {
		t.Fatalf("the tolerated ErrWaitDelay case must be logged (7.3); log: %q", logBuf.String())
	}
	// Returned when WaitDelay forced the pipes closed, without waiting
	// out the grandchild's 30 s: the lower bound proves the delay was
	// honoured, the upper bound (half the grandchild's lifetime) catches
	// a Spawn that waited for the grandchild, and the wall time is logged
	// so a slow run is visible without being a failure.
	t.Logf("Spawn returned after %v with a 300ms wait delay and a 30 s grandchild", elapsed)
	if elapsed < 200*time.Millisecond || elapsed > 15*time.Second {
		t.Fatalf("Spawn took %v, want more than the 300ms wait delay and far less than the grandchild's 30 s lifetime", elapsed)
	}
}

func TestSpawnWaitDelayWithoutResultIsInternal(t *testing.T) {
	t.Parallel()
	script := writeChildScript(t, "sleep 3 &\nexit 0")
	res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
		Argv:      []string{script},
		Env:       []string{childPATH},
		WaitDelay: 300 * time.Millisecond,
	})
	if res != nil {
		t.Fatalf("Spawn returned a result with no envelope on stdout: %+v", res)
	}
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeInternal || perr.Details["reason"] != "stdout_not_json" {
		t.Fatalf("mapping = %q/%v, want internal/stdout_not_json", perr.Code, perr.Details)
	}
}

func TestSpawnNeverInheritsParentEnvironment(t *testing.T) {
	// t.Setenv plants the canary in THIS process, so no t.Parallel here.
	t.Setenv("BRIGADE_SPAWN_CANARY", "leaked-from-parent")
	script := writeChildScript(t,
		`printf '{"ok":true,"protocol_version":"`+protocol.ProtocolVersion+
			`","result":{"canary":"%s"}}\n' "${BRIGADE_SPAWN_CANARY:-unset}"`)

	// Env nil must mean EMPTY, not inherited (3.2): printf and the ${...}
	// expansion are shell builtins, so the child needs no PATH at all.
	res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
		Argv: []string{script},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := resultField(t, res, "canary"); got != "unset" {
		t.Fatalf("child saw canary=%q through a nil Env; the parent environment leaked", got)
	}

	// Positive control for this test's own instrument: the same child
	// DOES see a value that is passed explicitly, so the "unset" above is
	// isolation, not a broken probe.
	res, err = adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{
		Argv: []string{script},
		Env:  []string{"BRIGADE_SPAWN_CANARY=handed-over"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := resultField(t, res, "canary"); got != "handed-over" {
		t.Fatalf("child saw canary=%q, want the explicitly passed value", got)
	}
}

func TestSpawnEmptyArgv(t *testing.T) {
	t.Parallel()
	res, err := adapterkit.Spawn(t.Context(), adapterkit.SpawnSpec{})
	if res != nil {
		t.Fatalf("Spawn returned a result for an empty argv: %+v", res)
	}
	perr := mustProtocolError(t, err)
	if perr.Code != protocol.CodeInternal || perr.Details["reason"] != "empty_argv" {
		t.Fatalf("mapping = %q/%v, want internal/empty_argv", perr.Code, perr.Details)
	}
}

func TestChildEnvAllowList(t *testing.T) {
	t.Parallel()
	environ := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/u",
		"TMPDIR=/tmp",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"LCONF=not-an-LC-variable",
		"XDG_CONFIG_HOME=/xdg/config",
		"CLAUDE_CONFIG_DIR=/claude-config",
		"HTTP_PROXY=http://proxy:3128",
		"https_proxy=http://proxy:3128",
		"NO_PROXY=localhost",
		"SSL_CERT_FILE=/etc/ca.pem",
		"SSL_CERT_DIR=", // empty value counts as unset
		"GODEBUG=http2debug=1",
		"GOFLAGS=-mod=vendor",
		"NODE_OPTIONS=--inspect",
		"BRIGADE_CONFIG_DIR=/planted/by/a/repo",
		"BRIGADE_PROFILE=attacker",
		"BRIGADE_TEAM_INBOUND=accept",
		"CLAUDE_CODE_MESSAGING_TOKEN=tok-secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
		"malformed-entry-without-equals",
	}
	got := adapterkit.ChildEnv(environ,
		"BRIGADE_PROFILE=default",
		"BRIGADE_CONFIG_DIR=/computed/config",
		"BRIGADE_STATE_DIR=/computed/state",
		"BRIGADE_LOG_LEVEL=info",
	)
	want := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/u",
		"TMPDIR=/tmp",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"XDG_CONFIG_HOME=/xdg/config",
		"CLAUDE_CONFIG_DIR=/claude-config",
		"HTTP_PROXY=http://proxy:3128",
		"https_proxy=http://proxy:3128",
		"NO_PROXY=localhost",
		"SSL_CERT_FILE=/etc/ca.pem",
		"BRIGADE_PROFILE=default",
		"BRIGADE_CONFIG_DIR=/computed/config",
		"BRIGADE_STATE_DIR=/computed/state",
		"BRIGADE_LOG_LEVEL=info",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ChildEnv:\n got %q\nwant %q", got, want)
	}
	// The computed value must be what a lookup resolves, even though an
	// inherited BRIGADE_* was present in the input.
	if v := adapterkit.Getenv(got, "BRIGADE_CONFIG_DIR"); v != "/computed/config" {
		t.Fatalf("BRIGADE_CONFIG_DIR resolves to %q, want the computed value", v)
	}
	for _, entry := range got {
		if strings.Contains(entry, "tok-secret") || strings.Contains(entry, "aws-secret") {
			t.Fatalf("a secret survived the allow-list: %q", entry)
		}
	}
}
