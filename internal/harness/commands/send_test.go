package commands

import (
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

const recipientID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// sentRequest decodes the request document the first `message send` child
// received on its stdin.
func sentRequest(t *testing.T, f *fixture) protocol.SendRequest {
	t.Helper()
	sends := f.rec.specsOf("message send")
	if len(sends) == 0 {
		t.Fatal("no send was spawned")
	}
	var req protocol.SendRequest
	if err := json.Unmarshal(sends[0].Stdin, &req); err != nil {
		t.Fatalf("send stdin: %v", err)
	}
	return req
}

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
		f.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m1", false)))
		body := strings.Repeat("y", protocol.MaxBodyBytes) // exactly the cap is fine
		if err := Send(f.inv(f.sessionEnv(), body, recipientID), SendOptions{}); err != nil {
			t.Fatalf("send at the cap: %v", err)
		}
		// The roster is read once, after the probe and before the send
		// (card 34).
		if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session list", "message send"}) {
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
	f.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m1", false)))
	exact := strings.Repeat("é", protocol.MaxSummaryChars)
	if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{Summary: exact}); err != nil {
		t.Fatalf("control: a summary of exactly the cap must pass: %v", err)
	}
}

// TestSendHappyPath pins the request document, the confirmation and the
// --json result. The first line names the recipient by id and is what it
// was before card 34 — the proof scripts read the two ids off it — and
// the recipient line under it is the roster's word on who that is.
func TestSendHappyPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("session list", okAnswer(listResult()))
	f.rec.on("message send", okAnswer(sendResultJSON("m-1", false)), okAnswer(sendResultJSON("m-1", true)))
	body := "the tenant_id migration has landed\n"
	if err := Send(f.inv(f.sessionEnv(), body, recipientID), SendOptions{Summary: "migration", ReplyTo: "m-0"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	want := "accepted: message m-1 to " + recipientID + ". " + AcceptedNote + "\n" +
		"recipient: bbbbb, idle, inbound accept, name \"billing\" (unverified)\n"
	if f.out.String() != want {
		t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
	}
	req := sentRequest(t, f)
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
	to, _ := result["recipient"].(map[string]any)
	if to["session_name"] != "billing" || to["state"] != "idle" || to["inbound"] != "accept" || to["note"] != RecipientNote {
		t.Errorf("recipient = %v", result["recipient"])
	}
	if _, has := to["waiting"]; has {
		t.Errorf("an online, accepting recipient carries waiting: %v", to["waiting"])
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
	f.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m-2", false)))
	path := filepath.Join(t.TempDir(), "body-SECRET-PATH.txt")
	if err := os.WriteFile(path, []byte("from a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Send(f.inv(f.sessionEnv(), "stdin is ignored", recipientID), SendOptions{BodyFile: path}); err != nil {
		t.Fatalf("send --body-file: %v", err)
	}
	if req := sentRequest(t, f); req.Body != "from a file" {
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
		f.rec.on("session list", okAnswer(listResult()))
		f.rec.on("message send", failAnswer(protocol.CodeUnavailable, "backend down"), okAnswer(sendResultJSON("m-3", false)))
		if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		// The roster is read once: the retry repeats the send, not the read.
		if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session list", "message send", "message send"}) {
			t.Fatalf("spawned %v, want one retry", got)
		}
		sends := f.rec.specsOf("message send")
		if string(sends[0].Stdin) != string(sends[1].Stdin) {
			t.Error("the retry carried a different request (the key must be the same)")
		}
		if !slices.Equal(f.sleeps, []time.Duration{RetryPause}) {
			t.Errorf("sleeps = %v, want one RetryPause", f.sleeps)
		}
	})
	t.Run("unavailable twice fails", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("session list", okAnswer(listResult()))
		f.rec.on("message send", failAnswer(protocol.CodeUnavailable, "backend down"))
		err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{})
		wantCode(t, err, protocol.CodeUnavailable, "")
		if n := len(f.rec.specsOf("message send")); n != 2 {
			t.Errorf("%d sends, want 2 (never a third)", n)
		}
	})
	t.Run("a spawn-level timeout is not retried", func(t *testing.T) {
		// The first attempt spent the whole 20 s budget; a retry would cost
		// the model's Bash call another 20 s for an adapter that hung.
		t.Parallel()
		f := newFixture(t)
		timeout := &protocol.Error{Code: protocol.CodeUnavailable, Message: "adapter did not finish within its deadline", Details: map[string]string{"reason": "timeout"}}
		f.rec.on("session list", okAnswer(listResult()))
		f.rec.on("message send", answer{spawnErr: timeout}, okAnswer(sendResultJSON("m-4", false)))
		err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{})
		wantCode(t, err, protocol.CodeUnavailable, "timeout")
		if n := len(f.rec.specsOf("message send")); n != 1 {
			t.Errorf("%d sends, want 1 (no retry after a timeout)", n)
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
		f.rec.on("session list", okAnswer(listResult()))
		f.rec.on("message send", answer{spawnErr: crash}, okAnswer(sendResultJSON("m-5", false)))
		if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if n := len(f.rec.specsOf("message send")); n != 2 {
			t.Errorf("%d sends, want 2", n)
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
			f.rec.on("session list", okAnswer(listResult()))
			f.rec.on("message send", a, okAnswer(sendResultJSON("never", false)))
			err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{})
			perr := wantCodeErr(t, err, code, "")
			if n := len(f.rec.specsOf("message send")); n != 1 {
				t.Errorf("%d sends, want 1 (no retry)", n)
			}
			if len(f.sleeps) != 0 {
				t.Errorf("slept %v before a retry that must not happen", f.sleeps)
			}
			if code == protocol.CodeRateLimited {
				if !strings.HasSuffix(perr.Message, "; retry after 2 s") || perr.RetryAfterMS != 1500 {
					t.Errorf("rate_limited message = %q (retry_after_ms %d)", perr.Message, perr.RetryAfterMS)
				}
			} else if perr.Message != "no" {
				// No retry-after, and no roster hint either: the roster was
				// read, so the adapter's word stands as it came.
				t.Errorf("%s message = %q, want the adapter's own", code, perr.Message)
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
	f.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m-5", false)))
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

	// P7-7: the profile surface is gone EVERYWHERE — outside a session
	// the shell's BRIGADE_PROFILE steers nothing either; the chain
	// resolves (an empty store answers the default name).
	g := newFixture(t)
	g.rec.on("session list", okAnswer(listResult()))
	if err := Sessions(g.inv(g.terminalEnv("BRIGADE_PROFILE=evil"), ""), SessionsOptions{}); err != nil {
		t.Fatalf("control: sessions in a terminal: %v", err)
	}
	argv := g.rec.spec(t, 0).Argv
	if argv[slices.Index(argv, "--profile")+1] != "default" {
		t.Errorf("control: argv %v let the shell's BRIGADE_PROFILE steer the child", argv)
	}
}

// rosterRec is one `session list` record for the recipient tests: an id,
// a name, a state and an inbound policy, with the rest filled in validly.
func rosterRec(id, name, state, inbound string) map[string]any {
	return map[string]any{
		"session_id": id, "session_name": name, "principal_ref": "principal-of-" + name,
		"state": state, "activity": "idle", "inbound": inbound,
		"last_seen_at": fixtureNow.Add(-time.Minute), "lease_until": fixtureNow.Add(time.Minute),
		"created_at": fixtureNow.Add(-time.Hour), "is_self": false,
	}
}

// rosterJSON is a canned `session list` result over the given records.
func rosterJSON(truncated bool, records ...map[string]any) string {
	sessions := make([]any, 0, len(records))
	for _, r := range records {
		sessions = append(sessions, r)
	}
	b, err := json.Marshal(map[string]any{
		"team_ref": fixtureTeamRef, "team_name": fixtureTeamName, "server_time": fixtureNow,
		"truncated": truncated, "sessions": sessions,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestSendByShortID is card 34's first half: the five characters the
// SESSION column shows, or any longer tail of the id, address the session.
// The adapter receives the id in full, and the idempotency key is the one
// the full id gives — so a send by the short id and a send by the full id
// inside the minute are one logical message.
func TestSendByShortID(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"bbbbb", "bbbbbbbbbbbb", recipientID} {
		t.Run(arg, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m-s", false)))
			if err := Send(f.inv(f.sessionEnv(), "body", arg), SendOptions{}); err != nil {
				t.Fatalf("send %s: %v", arg, err)
			}
			req := sentRequest(t, f)
			if req.RecipientSessionID != recipientID {
				t.Errorf("recipient_session_id = %q, want the id in full", req.RecipientSessionID)
			}
			if req.IdempotencyKey != IdempotencyKey(selfSessionID, recipientID, "body", fixtureNow) {
				t.Error("the idempotency key was not derived from the full id")
			}
			want := "accepted: message m-s to " + recipientID + ". " + AcceptedNote + "\n" +
				"recipient: bbbbb, idle, inbound accept, name \"billing\" (unverified)\n"
			if f.out.String() != want {
				t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
			}
			// The roster is asked for the offline sessions too: a message to
			// one is accepted like any other, and `waiting:` has to say so.
			if list := f.rec.specsOf("session list"); len(list) != 1 || !slices.Contains(list[0].Argv, "--include-offline") {
				t.Errorf("session list children = %v", list)
			}
		})
	}
}

// TestSendMatchesIDsNeverNames: a session NAMED like another session's
// short id takes none of its mail. The name is its owner's text; the id is
// the adapter's.
func TestSendMatchesIDsNeverNames(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("session list", okAnswer(rosterJSON(false,
		rosterRec("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "bbbbb", "active", "accept"),
		rosterRec(recipientID, "billing", "idle", "accept"),
	))).on("message send", okAnswer(sendResultJSON("m-n", false)))
	if err := Send(f.inv(f.sessionEnv(), "body", "bbbbb"), SendOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if req := sentRequest(t, f); req.RecipientSessionID != recipientID {
		t.Errorf("recipient_session_id = %q: the name matched", req.RecipientSessionID)
	}
}

// TestSendAmbiguousShortID: a tail that ends two ids is refused before
// anything is sent. The refusal lists the ids in full with their states —
// here one of them is offline, the row the default table hides — and
// carries no name and not the argument.
func TestSendAmbiguousShortID(t *testing.T) {
	t.Parallel()
	const first, second = "11111111111111111111111111177777", "22222222222222222222222222277777"
	f := newFixture(t)
	f.rec.on("session list", okAnswer(rosterJSON(false,
		rosterRec(first, "NAME-ONE", "idle", "accept"),
		rosterRec(second, "NAME-TWO", "offline", "accept"),
		rosterRec(recipientID, "billing", "idle", "accept"),
	))).on("message send", okAnswer(sendResultJSON("never", false)))
	err := Send(f.inv(f.sessionEnv(), "body", "77777"), SendOptions{})
	perr := wantCodeErr(t, err, protocol.CodeInvalidInput, "ambiguous")
	want := "these characters end 2 session ids: " + first + " (idle), " + second + " (offline); address one by its full session_id"
	if perr.Message != want {
		t.Errorf("message:\n got %q\nwant %q", perr.Message, want)
	}
	if perr.Details["field"] != "session_id" || perr.Details["matches"] != "2" {
		t.Errorf("details = %v", perr.Details)
	}
	if strings.Contains(perr.Message, "NAME-") {
		t.Errorf("the refusal carries a name: %q", perr.Message)
	}
	if n := len(f.rec.specsOf("message send")); n != 0 {
		t.Errorf("%d sends for an ambiguous recipient", n)
	}
	if f.out.Len() != 0 {
		t.Errorf("stdout = %q", f.out.String())
	}

	// The id in full still reaches the session the tail could not name.
	if err := Send(f.inv(f.sessionEnv(), "body", second), SendOptions{}); err != nil {
		t.Fatalf("send by the full id: %v", err)
	}
	if req := sentRequest(t, f); req.RecipientSessionID != second {
		t.Errorf("recipient_session_id = %q", req.RecipientSessionID)
	}
}

// TestSendUnmatchedGoesAsGiven: an argument the roster does not resolve —
// a tail under five characters, a tail no id ends with, an id the capped
// roster lacks — goes to the adapter as it was given, and the adapter's
// answer stands: it is the one authority on which ids exist. No recipient
// line follows an accepted one, because nothing is known about it.
func TestSendUnmatchedGoesAsGiven(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"bbbb", "zzzzz", "ffffffffffffffffffffffffffffffff"} {
		t.Run(arg, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.rec.on("session list", okAnswer(listResult())).on("message send", failAnswer(protocol.CodeNotFound, "no such session"))
			err := Send(f.inv(f.sessionEnv(), "body", arg), SendOptions{})
			perr := wantCodeErr(t, err, protocol.CodeNotFound, "")
			if perr.Message != "no such session" {
				t.Errorf("message = %q, want the adapter's own", perr.Message)
			}
			if req := sentRequest(t, f); req.RecipientSessionID != arg {
				t.Errorf("recipient_session_id = %q, want the argument as given", req.RecipientSessionID)
			}
		})
	}

	t.Run("accepted without a roster record", func(t *testing.T) {
		t.Parallel()
		const unlisted = "ffffffffffffffffffffffffffffffff"
		f := newFixture(t)
		f.rec.on("session list", okAnswer(rosterJSON(true, rosterRec(recipientID, "billing", "idle", "accept"))))
		f.rec.on("message send", okAnswer(sendResultTo("m-u", unlisted, false)))
		if err := Send(f.inv(f.sessionEnv(), "body", unlisted), SendOptions{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if want := "accepted: message m-u to " + unlisted + ". " + AcceptedNote + "\n"; f.out.String() != want {
			t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
		}
	})
}

// TestSendSaysWhyTheMessageWaits is card 34's second half. A message to an
// offline, holding or refusing session is `accepted` like any other, and
// until now that was all its sender was told.
func TestSendSaysWhyTheMessageWaits(t *testing.T) {
	t.Parallel()
	const parked = "99999999999999999999999999999999"
	roster := rosterJSON(false,
		rosterRec(recipientID, "billing", "idle", "accept"),
		rosterRec("cccccccccccccccccccccccccccccccc", injectionName, "active", "refuse"),
		rosterRec("dddddddddddddddddddddddddddddddd", "gone", "offline", "accept"),
		rosterRec("88888888888888888888888888888888", "careful", "idle", "hold"),
		rosterRec(parked, "parked", "offline", "hold"),
	)
	cases := []struct {
		name, arg, full, sessionName, state, inbound, recipient string
		waiting                                                 []string
	}{
		{"offline", "ddddd", "dddddddddddddddddddddddddddddddd", "gone", "offline", "accept",
			`recipient: ddddd, offline, inbound accept, name "gone" (unverified)`, []string{WaitingOffline}},
		{"hold", "88888", "88888888888888888888888888888888", "careful", "idle", "hold",
			`recipient: 88888, idle, inbound hold, name "careful" (unverified)`, []string{WaitingHold}},
		// The hostile name stays on its own line, last on it, with its tags
		// neutralised and its newline folded: it can add no line and put
		// nothing after the state.
		{"refuse, with a hostile name", "ccccc", "cccccccccccccccccccccccccccccccc", injectionName, "active", "refuse",
			`recipient: ccccc, active, inbound refuse, name "` + nameLine(injectionName) + `" (unverified)`, []string{WaitingRefuse}},
		{"offline and hold", "99999", parked, "parked", "offline", "hold",
			`recipient: 99999, offline, inbound hold, name "parked" (unverified)`, []string{WaitingOffline, WaitingHold}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.rec.on("session list", okAnswer(roster)).on("message send", okAnswer(sendResultTo("m-w", tc.full, false)))
			if err := Send(f.inv(f.sessionEnv(), "body", tc.arg), SendOptions{}); err != nil {
				t.Fatalf("send: %v", err)
			}
			lines := []string{"accepted: message m-w to " + tc.full + ". " + AcceptedNote, tc.recipient}
			for _, w := range tc.waiting {
				lines = append(lines, "waiting: "+w)
			}
			if want := strings.Join(lines, "\n") + "\n"; f.out.String() != want {
				t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
			}
			if strings.Contains(f.out.String(), "<system-reminder>") || strings.Contains(f.out.String(), "</brigade-message>") {
				t.Errorf("a tag survived: %q", f.out.String())
			}

			f.out.Reset()
			inv := f.inv(f.sessionEnv(), "body", tc.arg)
			inv.JSON = true
			if err := Send(inv, SendOptions{}); err != nil {
				t.Fatalf("send --json: %v", err)
			}
			_, result := envelopeOf(t, f.out.String())
			to, _ := result["recipient"].(map[string]any)
			// --json carries the name sanitised and unfolded, as `sessions
			// --json` does: a JSON string escapes its own newline.
			if name, _ := to["session_name"].(string); name != protocol.SanitizeName(tc.sessionName) || strings.Contains(name, "<system-reminder>") {
				t.Errorf("session_name = %q", name)
			}
			if to["state"] != tc.state || to["inbound"] != tc.inbound || to["note"] != RecipientNote {
				t.Errorf("recipient = %v", to)
			}
			got, _ := to["waiting"].([]any)
			if len(got) != len(tc.waiting) {
				t.Fatalf("waiting = %v, want %v", to["waiting"], tc.waiting)
			}
			for i, w := range tc.waiting {
				if got[i] != w {
					t.Errorf("waiting[%d] = %v, want %q", i, got[i], w)
				}
			}
		})
	}
	// No text says the message was delivered or read (4.5.1).
	for _, text := range []string{WaitingOffline, WaitingHold, WaitingRefuse, RecipientNote} {
		if strings.Contains(text, "delivered") {
			t.Errorf("%q says delivered", text)
		}
	}
}

// TestSendWithoutRoster: the roster read is an aid, never a gate. When it
// fails the argument goes to the adapter as given and the confirmation is
// the one line it always was; nothing reaches stderr at the default level.
// The `not_found` of a SHORT id then says the roster was not read, because
// the id went out unresolved and "no such session" alone would mislead its
// sender; an id of full length gets the adapter's answer as it came.
func TestSendWithoutRoster(t *testing.T) {
	t.Parallel()
	down := failAnswer(protocol.CodeUnavailable, "backend down")

	t.Run("accepted", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("session list", down).on("message send", okAnswer(sendResultJSON("m-d", false)))
		if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if want := "accepted: message m-d to " + recipientID + ". " + AcceptedNote + "\n"; f.out.String() != want {
			t.Errorf("stdout:\n got %q\nwant %q", f.out.String(), want)
		}
		if f.errb.Len() != 0 {
			t.Errorf("stderr = %q", f.errb.String())
		}
		// The read is not retried and costs the send no pause.
		if n := len(f.rec.specsOf("session list")); n != 1 {
			t.Errorf("%d roster reads, want 1", n)
		}
		if len(f.sleeps) != 0 {
			t.Errorf("slept %v", f.sleeps)
		}

		f.out.Reset()
		inv := f.inv(f.sessionEnv(), "body", recipientID)
		inv.JSON = true
		if err := Send(inv, SendOptions{}); err != nil {
			t.Fatalf("send --json: %v", err)
		}
		if _, result := envelopeOf(t, f.out.String()); result["recipient"] != nil {
			t.Errorf("recipient = %v without a roster", result["recipient"])
		}
	})

	t.Run("the not_found of a short id names the unread roster", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.rec.on("session list", down).on("message send", failAnswer(protocol.CodeNotFound, "no such session"))
		err := Send(f.inv(f.sessionEnv(), "body", "bbbbb"), SendOptions{})
		perr := wantCodeErr(t, err, protocol.CodeNotFound, "")
		if perr.Message != "no such session"+RosterUnreadHint || perr.Details["roster"] != "unread" {
			t.Errorf("message = %q, details = %v", perr.Message, perr.Details)
		}
		if req := sentRequest(t, f); req.RecipientSessionID != "bbbbb" {
			t.Errorf("recipient_session_id = %q, want the argument as given", req.RecipientSessionID)
		}
	})

	t.Run("every other failure stands as it came", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name, arg string
			code      protocol.Code
		}{
			{"the not_found of an id of full length", recipientID, protocol.CodeNotFound},
			{"another code for a short id", "bbbbb", protocol.CodeConflict},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t)
				f.rec.on("session list", down).on("message send", failAnswer(tc.code, "no"))
				perr := wantCodeErr(t, Send(f.inv(f.sessionEnv(), "body", tc.arg), SendOptions{}), tc.code, "")
				if perr.Message != "no" || perr.Details["roster"] != "" {
					t.Errorf("message = %q, details = %v", perr.Message, perr.Details)
				}
			})
		}
	})
}

// TestSendRosterBudget: the roster read runs under RosterTimeout, not the
// 20 s of the send, so a roster that hangs costs the model's Bash call a
// quarter of what a hung send does.
func TestSendRosterBudget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("session list", okAnswer(listResult())).on("message send", okAnswer(sendResultJSON("m-b", false)))
	if err := Send(f.inv(f.sessionEnv(), "body", recipientID), SendOptions{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if b := f.rec.budgetOf("session list"); b <= 0 || b > RosterTimeout {
		t.Errorf("roster budget = %v, want within %v", b, RosterTimeout)
	}
	if b := f.rec.budgetOf("message send"); b <= RosterTimeout {
		t.Errorf("send budget = %v, want the 20 s of 4.1", b)
	}
}

// TestResolveRecipient is the matching rule on its own: the id byte for
// byte first, then the one id whose SHOWN form ends with the argument.
func TestResolveRecipient(t *testing.T) {
	t.Parallel()
	rec := func(id string) protocol.SessionRecord { return protocol.SessionRecord{SessionID: id} }
	roster := []protocol.SessionRecord{
		rec("aaaa-1111-abcde"),
		rec("bbbb-2222-vwxyz"),
		// An id that IS another id's tail: the exact match wins over the tail.
		rec("abcde"),
		// An id carrying a character the roster never shows: the table
		// prints this one as "cccc-3333-qrstu", so that is what matches.
		rec("cccc-3333-qr\"stu"),
		// An id that sanitises away to nothing is shown as "?" and matches
		// no argument.
		rec("\"<>"),
		rec("dddd-4444-éèêëē"),
	}
	cases := []struct {
		name, arg, want string
		ok              bool
	}{
		{"the id in full", "bbbb-2222-vwxyz", "bbbb-2222-vwxyz", true},
		{"the SESSION column's five characters", "vwxyz", "bbbb-2222-vwxyz", true},
		{"a longer tail", "2222-vwxyz", "bbbb-2222-vwxyz", true},
		{"an id that is another's tail: exact first", "abcde", "abcde", true},
		{"the tail as shown, not as stored", "qrstu", "cccc-3333-qr\"stu", true},
		{"five characters are counted in code points", "éèêëē", "dddd-4444-éèêëē", true},
		{"four characters", "wxyz", "", false},
		{"a head, not a tail", "bbbb-", "", false},
		{"a tail in another case", "VWXYZ", "", false},
		{"the mark of an id that sanitised away", "?", "", false},
		{"nothing", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok, err := resolveRecipient(tc.arg, roster)
			if err != nil {
				t.Fatalf("resolveRecipient: %v", err)
			}
			if ok != tc.ok || got.SessionID != tc.want {
				t.Errorf("resolved %q (ok %v), want %q (ok %v)", got.SessionID, ok, tc.want, tc.ok)
			}
		})
	}

	t.Run("more matches than the refusal lists", func(t *testing.T) {
		t.Parallel()
		var many []protocol.SessionRecord
		for i := range ambiguousShown + 2 {
			many = append(many, protocol.SessionRecord{SessionID: strconv.Itoa(i) + "-same-tail", State: protocol.SessionStateIdle})
		}
		_, ok, err := resolveRecipient("-tail", many)
		perr := wantCodeErr(t, err, protocol.CodeInvalidInput, "ambiguous")
		if ok || !strings.HasSuffix(perr.Message, "4-same-tail (idle) and 2 more; address one by its full session_id") {
			t.Errorf("ok %v, message = %q", ok, perr.Message)
		}
		if strings.Contains(perr.Message, "5-same-tail") {
			t.Errorf("the refusal lists more than %d ids: %q", ambiguousShown, perr.Message)
		}
	})
}
