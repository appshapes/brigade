package commands

import (
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

const recipientID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// TestSendRefusesBodyBeforeAnySpawn is U-05: an empty, an oversize and a
// non-UTF-8 body are invalid_input with the 4.3.1 details, and the spawn
// recorder saw ZERO children — not even the describe. The happy path at
// the end is the positive control that the same fixture does spawn.
func TestSendRefusesBodyBeforeAnySpawn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body, reason, actual string
	}{
		{"empty", "", "empty", "0"},
		{"oversize by one byte", strings.Repeat("x", protocol.MaxBodyBytes+1), "too_long", "16385"},
		{"oversize by far", strings.Repeat("x", 3*protocol.MaxBodyBytes), "too_long", "16385"},
		{"not utf-8", "ok\xff\xfe", "not_utf8", "4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			err := Send(f.inv(f.sessionEnv(), tc.body, recipientID), SendOptions{})
			perr := wantCodeErr(t, err, protocol.CodeInvalidInput, tc.reason)
			if perr.Code.Exit() != 3 {
				t.Errorf("exit = %d, want 3", perr.Code.Exit())
			}
			d := perr.Details
			if d["field"] != "body" || d["limit"] != "16384" || d["unit"] != "bytes" || d["actual"] != tc.actual {
				t.Errorf("details = %v", d)
			}
			if f.rec.count() != 0 {
				t.Errorf("%d children spawned for a refused body", f.rec.count())
			}
		})
	}

	t.Run("control: a valid body spawns", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("message send", okAnswer(sendResultJSON("m1", false)))
		body := strings.Repeat("y", protocol.MaxBodyBytes) // exactly the cap is fine
		if err := Send(f.inv(f.sessionEnv(), body, recipientID), SendOptions{}); err != nil {
			t.Fatalf("send at the cap: %v", err)
		}
		if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "message send"}) {
			t.Errorf("spawned %v", got)
		}
	})
}

// TestSendSummaryTooLongBeforeSpawn: the summary cap is code points, and
// it too is measured before any spawn.
func TestSendSummaryTooLongBeforeSpawn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	long := strings.Repeat("é", protocol.MaxSummaryChars+1) // 201 code points, 402 bytes
	err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{Summary: long})
	perr := wantCodeErr(t, err, protocol.CodeInvalidInput, "too_long")
	if perr.Details["field"] != "summary" || perr.Details["unit"] != "codepoints" || perr.Details["actual"] != "201" {
		t.Errorf("details = %v", perr.Details)
	}
	if f.rec.count() != 0 {
		t.Errorf("%d children spawned", f.rec.count())
	}
	f.rec.on("message send", okAnswer(sendResultJSON("m1", false)))
	exact := strings.Repeat("é", protocol.MaxSummaryChars)
	if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{Summary: exact}); err != nil {
		t.Fatalf("control: a summary of exactly the cap must pass: %v", err)
	}
}

// TestSendHappyPath pins the request document, the confirmation line and
// the --json result, and that the recipient is named by id.
func TestSendHappyPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("message send", okAnswer(sendResultJSON("m-1", false)), okAnswer(sendResultJSON("m-1", true)))
	body := "the tenant_id migration has landed\n"
	if err := Send(f.inv(f.sessionEnv(), body, recipientID), SendOptions{Summary: "migration", ReplyTo: "m-0"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	want := "accepted: message m-1 to " + recipientID + ". " + AcceptedNote + "\n"
	if f.out.String() != want {
		t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
	}
	var req protocol.SendRequest
	if err := json.Unmarshal(f.rec.spec(t, 1).Stdin, &req); err != nil {
		t.Fatalf("send stdin: %v", err)
	}
	if req.SenderSessionID != selfSessionID || req.RecipientSessionID != recipientID || req.Body != body ||
		req.Summary != "migration" || req.ReplyTo != "m-0" {
		t.Errorf("request = %+v", req)
	}
	if req.IdempotencyKey != IdempotencyKey(selfSessionID, recipientID, body, fixtureNow) {
		t.Errorf("idempotency key = %q, want D11's derivation", req.IdempotencyKey)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("the request does not validate: %v", err)
	}

	// The duplicate form, as --json.
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), body, recipientID)
	inv.JSON = true
	if err := Send(inv, SendOptions{}); err != nil {
		t.Fatalf("send --json: %v", err)
	}
	ok, result := envelopeOf(t, f.out.String())
	if !ok || result["message_id"] != "m-1" || result["duplicate"] != true ||
		result["self_session_id"] != selfSessionID || result["note"] != AcceptedNote {
		t.Errorf("envelope = ok %v result %v", ok, result)
	}
	f.out.Reset()
	if err := Send(f.inv(f.sessionEnv(), body, recipientID), SendOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), ", duplicate of an earlier send. ") {
		t.Errorf("duplicate line = %q", f.out.String())
	}
}

// TestSendBodyFile reads the body from the file and never echoes the path.
func TestSendBodyFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("message send", okAnswer(sendResultJSON("m-2", false)))
	path := filepath.Join(t.TempDir(), "body-SECRET-PATH.txt")
	if err := os.WriteFile(path, []byte("from a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Send(f.inv(f.sessionEnv(), "stdin is ignored", recipientID), SendOptions{BodyFile: path}); err != nil {
		t.Fatalf("send --body-file: %v", err)
	}
	var req protocol.SendRequest
	if err := json.Unmarshal(f.rec.spec(t, 1).Stdin, &req); err != nil {
		t.Fatal(err)
	}
	if req.Body != "from a file" {
		t.Errorf("body = %q, want the file's content", req.Body)
	}
	missing := filepath.Join(t.TempDir(), "absent-SECRET-PATH")
	err := Send(f.inv(f.sessionEnv(), "", recipientID), SendOptions{BodyFile: missing})
	perr := wantCodeErr(t, err, protocol.CodeInvalidInput, "unreadable_file")
	if strings.Contains(perr.Message, "SECRET-PATH") {
		t.Errorf("the path was echoed: %q", perr.Message)
	}
}

// TestIdempotencyKeyIsStableWithinAMinute is D11 with an injected clock:
// the same inputs inside one minute give one key; the next minute, a
// different sender, recipient or body each give another.
func TestIdempotencyKeyIsStableWithinAMinute(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	k := IdempotencyKey("s", "r", "b", base)
	if got := IdempotencyKey("s", "r", "b", base.Add(59*time.Second)); got != k {
		t.Errorf("key changed inside the minute: %q vs %q", got, k)
	}
	if got := IdempotencyKey("s", "r", "b", base.Add(60*time.Second)); got == k {
		t.Error("key did not change across the minute boundary")
	}
	for name, other := range map[string]string{
		"sender":    IdempotencyKey("S", "r", "b", base),
		"recipient": IdempotencyKey("s", "R", "b", base),
		"body":      IdempotencyKey("s", "r", "B", base),
	} {
		if other == k {
			t.Errorf("a different %s gave the same key", name)
		}
	}
	// The separator is load-bearing: ("ab","c") and ("a","bc") differ.
	if IdempotencyKey("ab", "c", "b", base) == IdempotencyKey("a", "bc", "b", base) {
		t.Error("the NUL separator does not separate")
	}
	if len(k) != 43 || strings.ContainsAny(k, "+/=") {
		t.Errorf("key %q is not unpadded base64url of a sha256", k)
	}
	if err := (&protocol.SendRequest{SenderSessionID: "s", RecipientSessionID: "r", Body: "b", IdempotencyKey: k}).Validate(); err != nil {
		t.Errorf("the key does not validate as a request member: %v", err)
	}
}

// TestSendRetriesOnceOnUnavailableOnly (6.4): one retry on `unavailable`
// with the SAME key after the pause; `rate_limited` and `loop_detected`
// are never retried, and the rate-limited message names its retry-after.
func TestSendRetriesOnceOnUnavailableOnly(t *testing.T) {
	t.Parallel()
	t.Run("unavailable then accepted", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("message send", failAnswer(protocol.CodeUnavailable, "backend down"), okAnswer(sendResultJSON("m-3", false)))
		if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "message send", "message send"}) {
			t.Errorf("spawned %v, want one retry", got)
		}
		if string(f.rec.spec(t, 1).Stdin) != string(f.rec.spec(t, 2).Stdin) {
			t.Error("the retry carried a different request (the key must be the same)")
		}
		if !slices.Equal(f.sleeps, []time.Duration{RetryPause}) {
			t.Errorf("sleeps = %v, want one RetryPause", f.sleeps)
		}
	})
	t.Run("unavailable twice fails", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("message send", failAnswer(protocol.CodeUnavailable, "backend down"))
		err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{})
		wantCode(t, err, protocol.CodeUnavailable, "")
		if n := f.rec.count(); n != 3 {
			t.Errorf("spawned %d, want 3 (describe + two sends, never a third)", n)
		}
	})
	t.Run("a spawn-level timeout is not retried", func(t *testing.T) {
		// The first attempt spent the whole 20 s budget; a retry would cost
		// the model's Bash call another 20 s for an adapter that hung.
		t.Parallel()
		f := newFixture(t)
		timeout := &protocol.Error{Code: protocol.CodeUnavailable, Message: "adapter did not finish within its deadline", Details: map[string]string{"reason": "timeout"}}
		f.rec.on("message send", answer{spawnErr: timeout}, okAnswer(sendResultJSON("m-4", false)))
		err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{})
		wantCode(t, err, protocol.CodeUnavailable, "timeout")
		if n := f.rec.count(); n != 2 {
			t.Errorf("spawned %d, want 2 (describe + one send, no retry after a timeout)", n)
		}
		if len(f.sleeps) != 0 {
			t.Errorf("slept %v before a retry that must not happen", f.sleeps)
		}
	})
	t.Run("a spawn-level crash is retried", func(t *testing.T) {
		// An adapter that died by a signal is `unavailable` without the
		// timeout reason: a second attempt is cheap and may succeed.
		t.Parallel()
		f := newFixture(t)
		crash := &protocol.Error{Code: protocol.CodeUnavailable, Message: "adapter killed by a signal", Details: map[string]string{"signal": "SIGKILL"}}
		f.rec.on("message send", answer{spawnErr: crash}, okAnswer(sendResultJSON("m-5", false)))
		if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if n := f.rec.count(); n != 3 {
			t.Errorf("spawned %d, want 3", n)
		}
	})
	for _, code := range []protocol.Code{protocol.CodeRateLimited, protocol.CodeLoopDetected, protocol.CodeNotFound, protocol.CodeConflict} {
		t.Run("never retried: "+string(code), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			a := failAnswer(code, "no")
			if code == protocol.CodeRateLimited {
				a.errObj.RetryAfterMS = 1500
			}
			f.rec.on("message send", a, okAnswer(sendResultJSON("never", false)))
			err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{})
			perr := wantCodeErr(t, err, code, "")
			if n := f.rec.count(); n != 2 {
				t.Errorf("spawned %d, want 2 (no retry)", n)
			}
			if len(f.sleeps) != 0 {
				t.Errorf("slept %v before a retry that must not happen", f.sleeps)
			}
			if code == protocol.CodeRateLimited {
				if !strings.HasSuffix(perr.Message, "; retry after 2 s") || perr.RetryAfterMS != 1500 {
					t.Errorf("rate_limited message = %q (retry_after_ms %d)", perr.Message, perr.RetryAfterMS)
				}
			} else if strings.Contains(perr.Message, "retry after") {
				t.Errorf("%s carried a retry-after: %q", code, perr.Message)
			}
		})
	}
}

// TestSendOutsideSessionIsConfig: a terminal has no Brigade session to
// send from, and nothing is spawned.
func TestSendOutsideSessionIsConfig(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	err := Send(f.inv(f.terminalEnv(), "body", recipientID), SendOptions{})
	wantCode(t, err, protocol.CodeConfig, "not_in_session")
	if f.rec.count() != 0 {
		t.Errorf("%d children spawned", f.rec.count())
	}
}

// TestSendUsage: exactly one recipient; a terminal on stdin is refused
// rather than waited on.
func TestSendUsage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	wantCode(t, Send(f.inv(f.sessionEnv(), "body"), SendOptions{}), protocol.CodeUsage, "")
	wantCode(t, Send(f.inv(f.sessionEnv(), "body", "a", "b"), SendOptions{}), protocol.CodeUsage, "")
	wantCode(t, Send(f.inv(f.sessionEnv(), "body", ""), SendOptions{}), protocol.CodeUsage, "")
	inv := f.inv(f.sessionEnv(), "typed", recipientID)
	inv.Deps.IsTerminal = func(io.Reader) bool { return true }
	perr := wantCodeErr(t, Send(inv, SendOptions{}), protocol.CodeUsage, "")
	if !strings.Contains(perr.Message, "heredoc") {
		t.Errorf("message = %q, want the heredoc hint", perr.Message)
	}
	if f.rec.count() != 0 {
		t.Errorf("%d children spawned", f.rec.count())
	}
	// A nil stdin is an empty body, not a panic.
	inv = f.inv(f.sessionEnv(), "", recipientID)
	inv.In = nil
	wantCode(t, Send(inv, SendOptions{}), protocol.CodeInvalidInput, "empty")
}

// TestSendIgnoresHostileEnvironment is U-27's unit half for the commands:
// with CLAUDE_PID set, hostile inherited BRIGADE_PROFILE, BRIGADE_CONFIG_DIR,
// BRIGADE_STATE_DIR, BRIGADE_TEAM_INBOUND and BRIGADE_ADAPTER_COMMAND
// change nothing — the map's profile and adapter are used, the child's
// environment carries the map's values, and the messaging token reaches
// no child. The positive control is the same environment WITHOUT
// CLAUDE_PID, where BRIGADE_PROFILE is the user's own and is honoured.
func TestSendIgnoresHostileEnvironment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("message send", okAnswer(sendResultJSON("m-5", false)))
	hostile := []string{
		"BRIGADE_PROFILE=evil",
		"BRIGADE_CONFIG_DIR=" + filepath.Join(t.TempDir(), "evil-config"),
		"BRIGADE_STATE_DIR=" + filepath.Join(t.TempDir(), "evil-state"),
		"BRIGADE_TEAM_INBOUND=refuse",
		"BRIGADE_ADAPTER_COMMAND=/evil/adapter",
		"CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/x.sock",
		"CLAUDE_CODE_MESSAGING_TOKEN=" + msgTok,
	}
	if err := Send(f.inv(f.sessionEnv(hostile...), "body", recipientID), SendOptions{}); err != nil {
		t.Fatalf("send under a hostile environment: %v", err)
	}
	assertChildIsolation(t, f.rec, fixtureProfile)
	for i := range f.rec.count() {
		spec := f.rec.spec(t, i)
		if spec.Argv[0] != f.adapterPath {
			t.Errorf("spawn %d ran %s, want the map's adapter", i, spec.Argv[0])
		}
		if !slices.Contains(spec.Env, "BRIGADE_CONFIG_DIR="+f.dirs.BrigadeConfig) || !slices.Contains(spec.Env, "BRIGADE_STATE_DIR="+f.stateDir) {
			t.Errorf("spawn %d env %v does not carry the map's config dir and the XDG state dir", i, spec.Env)
		}
	}

	// Control: outside a session the shell's BRIGADE_PROFILE is the user's
	// own and IS honoured — the same variable steers the child's --profile.
	g := newFixture(t)
	g.rec.on("session list", okAnswer(listResult()))
	if err := Sessions(g.inv(g.terminalEnv("BRIGADE_PROFILE=evil"), ""), SessionsOptions{}); err != nil {
		t.Fatalf("control: sessions in a terminal: %v", err)
	}
	argv := g.rec.spec(t, 0).Argv
	if argv[slices.Index(argv, "--profile")+1] != "evil" {
		t.Errorf("control: argv %v does not honour the shell's BRIGADE_PROFILE outside a session", argv)
	}
}
