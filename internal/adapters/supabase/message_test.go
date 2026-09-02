package supabase

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// The `message send|receive|ack` verbs against the fake backend (P2-9):
// the arguments each RPC is called with, the shape of each result, every
// local refusal that must never reach the network, and the mapping of
// every raise send_message can produce.

const (
	testSenderID    = "cccccccc-dddd-4eee-8fff-00000000000a"
	testRecipientID = "cccccccc-dddd-4eee-8fff-00000000000b"
	testMessageID   = "cccccccc-dddd-4eee-8fff-00000000000c"
	// notAUUID is the shape the conformance suite's randomRef produces:
	// 16 random bytes as hex, which no Supabase id ever is.
	notAUUID = "0123456789abcdef0123456789abcdef"
)

// sendDoc is a minimal `message send` document.
func sendDoc(sender, recipient string) string {
	return `{"sender_session_id":"` + sender + `","recipient_session_id":"` + recipient + `","body":"hi"}`
}

// messageResultJSON is what brigade.message_result() answers.
const messageResultJSON = `{"message_id":"` + testMessageID + `","recipient_session_id":"` + testRecipientID +
	`","created_at":"2026-09-02T12:00:00.5+00:00","duplicate":false,"hop_count":3}`

// envelopeJSON is what brigade.envelope() answers for one message.
func envelopeJSON(id string) string {
	return `{"protocol_version":"1","kind":"text","message_id":"` + id + `","team_ref":"` + testTeamID + `",` +
		`"sender":{"principal_ref":"` + testUserID + `","human_label":"alice@example.com","session_id":"` + testSenderID +
		`","session_name":"a"},"recipient_session_id":"` + testRecipientID + `","summary":null,"body":"hi",` +
		`"reply_to":null,"hop_count":0,"created_at":"2026-09-02T12:00:00.5+00:00","delivery_state":"accepted"}`
}

func TestMessageSendArgumentsAndResult(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(messageResultJSON))
	got := r.exec(sendDoc(testSenderID, testRecipientID), "message", "send")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	args := (*seen)[0]
	if args["__fn"] != "send_message" {
		t.Fatalf("RPC %v, want send_message", args["__fn"])
	}
	if v := arg(t, args, "p_sender_session_id"); v != testSenderID {
		t.Errorf("p_sender_session_id = %v", v)
	}
	if v := arg(t, args, "p_recipient_session_id"); v != testRecipientID {
		t.Errorf("p_recipient_session_id = %v", v)
	}
	if v := arg(t, args, "p_body"); v != "hi" {
		t.Errorf("p_body = %v", v)
	}
	// An absent summary is the backend's NULL, not an empty string.
	if v := arg(t, args, "p_summary"); v != nil {
		t.Errorf("p_summary = %v, want null", v)
	}
	// No reply_to member at all rather than an explicit null: the RPC's
	// own default is null and the implicit chain of 4.5.12 applies.
	if _, present := args["p_reply_to"]; present {
		t.Errorf("p_reply_to was sent for a message with no reply_to")
	}

	// The status of 4.4.7 is the adapter's; message_result carries none.
	var res protocol.SendResponse
	env := decode(t, got.stdout)
	raw, err := json.Marshal(env["result"])
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Decode(raw, &res); err != nil {
		t.Fatalf("the send result does not validate: %v", err)
	}
	if res.Status != protocol.SendStatusAccepted {
		t.Errorf("status = %q, want accepted", res.Status)
	}
	if res.MessageID != testMessageID || res.HopCount != 3 || res.Duplicate {
		t.Errorf("result = %+v", res)
	}
}

// A send with no idempotency_key of its own gets a fresh random one, so
// two identical sends are two messages (4.5.4 keys on the CALLER's key).
func TestMessageSendMintsAnIdempotencyKey(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(messageResultJSON))
	for i := 0; i < 2; i++ {
		if got := r.exec(sendDoc(testSenderID, testRecipientID), "message", "send"); got.code != 0 {
			t.Fatalf("exit %d: %s", got.code, got.stdout)
		}
	}
	first, _ := arg(t, (*seen)[0], "p_idempotency_key").(string)
	second, _ := arg(t, (*seen)[1], "p_idempotency_key").(string)
	if len(first) != 32 || len(second) != 32 {
		t.Fatalf("minted keys %q and %q, want 32 hex characters", first, second)
	}
	if first == second {
		t.Errorf("two keyless sends shared one minted idempotency key")
	}
	req := &protocol.SendRequest{IdempotencyKey: first, SenderSessionID: testSenderID,
		RecipientSessionID: testRecipientID, Body: "hi"}
	if err := req.Validate(); err != nil {
		t.Errorf("a minted key does not pass the request's own validation: %v", err)
	}
}

// A caller's own key is passed through verbatim, as are summary and a
// uuid-shaped reply_to.
func TestMessageSendPassesThroughOptionalMembers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(messageResultJSON))
	doc := `{"sender_session_id":"` + testSenderID + `","recipient_session_id":"` + testRecipientID +
		`","body":"hi","summary":"s","idempotency_key":"k-1","reply_to":"` + testMessageID + `"}`
	if got := r.exec(doc, "message", "send"); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	args := (*seen)[0]
	for name, want := range map[string]any{
		"p_summary":         "s",
		"p_idempotency_key": "k-1",
		"p_reply_to":        testMessageID,
	} {
		if v := arg(t, args, name); v != want {
			t.Errorf("%s = %v, want %v", name, v, want)
		}
	}
}

// An id that cannot name a session or a message is the uniform not_found
// before any dial, byte-identical to the backend's PT404 (C-24, C-25,
// C-29).
func TestMessageSendIDShapes(t *testing.T) {
	t.Parallel()
	uniform := ""
	for _, tc := range []struct {
		name, doc string
	}{
		{"sender", sendDoc(notAUUID, testRecipientID)},
		{"recipient", sendDoc(testSenderID, notAUUID)},
		{"reply_to", `{"sender_session_id":"` + testSenderID + `","recipient_session_id":"` + testRecipientID +
			`","body":"hi","reply_to":"` + notAUUID + `"}`},
	} {
		r := newRig(t)
		r.joined()
		got := r.fails("not_found", 6, tc.doc, "message", "send")
		if r.be.total() != 0 {
			t.Errorf("%s: the backend was called for an id that cannot name anything", tc.name)
		}
		if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
			t.Fatalf("%s: envelope differs from the uniform not_found: %s", tc.name, got.stdout)
		}
		if uniform == "" {
			uniform = got.stdout
		} else if got.stdout != uniform {
			t.Fatalf("%s: envelope differs from the other local refusals", tc.name)
		}
	}

	// The backend's own not_found for a real but foreign id is the SAME
	// envelope (4.5.7: no existence oracle).
	r := newRig(t)
	r.joined()
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusNotFound, "PT404", "brigade:not_found")
	}
	got := r.fails("not_found", 6, sendDoc(testSenderID, testRecipientID), "message", "send")
	if got.stdout != uniform {
		t.Fatalf("a foreign id differs from a malformed one:\n%s\n%s", got.stdout, uniform)
	}
}

// The forbidden sender members of 4.4.6 are refused by Validate, BEFORE
// any network call (C-23), each naming itself in details.field.
func TestMessageSendForbiddenMembers(t *testing.T) {
	t.Parallel()
	for _, member := range []string{"sender", "principal_ref", "human_label", "team_ref", "created_at", "hop_count"} {
		for _, value := range []string{`{"x":1}`, `null`, `"x"`} {
			r := newRig(t)
			r.joined()
			doc := `{"sender_session_id":"` + testSenderID + `","recipient_session_id":"` + testRecipientID +
				`","body":"hi","` + member + `":` + value + `}`
			got := r.fails("invalid_input", 3, doc, "message", "send")
			if details(t, got.stdout)["field"] != member {
				t.Errorf("%s=%s: details = %v, want field %s", member, value, details(t, got.stdout), member)
			}
			if r.be.total() != 0 {
				t.Errorf("%s: the backend was called for a forged sender member", member)
			}
		}
	}
}

// Every raise send_message can produce maps to its 4.6 code, and no
// server text ever reaches stdout (4.3, U-24).
func TestMessageSendBackendRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raise, code string
		exit              int
		reason            string
		retryAfterMS      int
	}{
		{"sender closed", "brigade:conflict:sender_closed", "conflict", 7, "sender_closed", 0},
		{"key reused", "brigade:conflict:idempotency_key", "conflict", 7, "idempotency_key", 0},
		{"self", "brigade:invalid_input:recipient_is_self", "invalid_input", 3, "rejected_by_backend", 0},
		{"revoked", "brigade:unauthorized", "unauthorized", 5, "", 0},
		{"pair cap", "brigade:rate_limited:sender_quota_for_recipient:60", "rate_limited", 8, "sender_quota_for_recipient", 60000},
		{"inbox full", "brigade:rate_limited:recipient_inbox_full:60", "rate_limited", 8, "recipient_inbox_full", 60000},
		{"per minute", "brigade:rate_limited:send_per_minute:60", "rate_limited", 8, "send_per_minute", 60000},
		{"per principal", "brigade:rate_limited:principal_per_minute:60", "rate_limited", 8, "principal_per_minute", 60000},
		{"hops", "brigade:loop_detected:max_hops", "loop_detected", 12, "max_hops", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.joined()
			r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
				postgrest(w, http.StatusInternalServerError, "P0001", tc.raise)
			}
			got := r.fails(tc.code, tc.exit, sendDoc(testSenderID, testRecipientID), "message", "send")
			if tc.reason != "" && details(t, got.stdout)["reason"] != tc.reason {
				t.Errorf("details = %v, want reason %s", details(t, got.stdout), tc.reason)
			}
			if tc.retryAfterMS > 0 {
				env := decode(t, got.stdout)
				object, _ := env["error"].(map[string]any)
				if ms, _ := object["retry_after_ms"].(float64); int(ms) != tc.retryAfterMS {
					t.Errorf("retry_after_ms = %v, want %d", object["retry_after_ms"], tc.retryAfterMS)
				}
			}
			if strings.Contains(got.stdout, "brigade:") || strings.Contains(got.stdout, "P0001") {
				t.Errorf("server text reached stdout: %s", got.stdout)
			}
		})
	}
}

func TestMessageReceive(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`[` + envelopeJSON(testMessageID) + `]`))
	result := r.ok("message", "receive", "--session", testRecipientID, "--limit", "7")
	args := (*seen)[0]
	if args["__fn"] != "fetch_inbox" {
		t.Fatalf("RPC %v", args["__fn"])
	}
	if v := arg(t, args, "p_session_id"); v != testRecipientID {
		t.Errorf("p_session_id = %v", v)
	}
	if v := arg(t, args, "p_limit"); v != float64(7) {
		t.Errorf("p_limit = %v, want 7", v)
	}
	messages, _ := result["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("%d messages, want 1 (%v)", len(messages), result)
	}
	raw, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.MessageEnvelope
	if err := protocol.Decode(raw, &m); err != nil {
		t.Fatalf("the envelope does not validate: %v", err)
	}
	if m.MessageID != testMessageID || m.Sender.SessionID != testSenderID {
		t.Errorf("envelope = %+v", m)
	}
}

// An empty inbox is an EMPTY ARRAY on the wire (JSON convention 3, C-30),
// and the default --limit is 50 (4.1).
func TestMessageReceiveEmptyAndDefaultLimit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`[]`))
	got := r.exec("", "message", "receive", "--session", testRecipientID)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if !strings.Contains(got.stdout, `"messages":[]`) {
		t.Errorf("an empty inbox is not [] on the wire: %s", got.stdout)
	}
	if v := arg(t, (*seen)[0], "p_limit"); v != float64(receiveDefaultLimit) {
		t.Errorf("p_limit = %v, want %d", v, receiveDefaultLimit)
	}
}

// --limit outside 1..200 is `usage` (brief section 3), checked on argv
// before the ladder and before any dial.
func TestMessageReceiveLimitRange(t *testing.T) {
	t.Parallel()
	for _, limit := range []string{"0", "-1", "201"} {
		r := newRig(t)
		r.joined()
		r.fails("usage", 2, "", "message", "receive", "--session", testRecipientID, "--limit", limit)
		if r.be.total() != 0 {
			t.Errorf("--limit %s: the backend was called", limit)
		}
	}
	r := newRig(t)
	r.joined()
	r.be.captureRPC(only(`[]`))
	for _, limit := range []string{"1", "200"} {
		if got := r.exec("", "message", "receive", "--session", testRecipientID, "--limit", limit); got.code != 0 {
			t.Errorf("--limit %s: exit %d (%s)", limit, got.code, got.stdout)
		}
	}
}

func TestMessageReceiveRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.fails("usage", 2, "", "message", "receive")
	got := r.fails("not_found", 6, "", "message", "receive", "--session", notAUUID)
	if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("envelope differs from the uniform not_found: %s", got.stdout)
	}
	if r.be.total() != 0 {
		t.Fatalf("the backend was called for an id that cannot name a session")
	}
}

func TestMessageAck(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`{"acked":["` + testMessageID + `"],"unknown":[]}`))
	got := r.exec(`{"message_ids":["`+testMessageID+`"]}`, "message", "ack", "--session", testRecipientID)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	args := (*seen)[0]
	if args["__fn"] != "ack_messages" {
		t.Fatalf("RPC %v", args["__fn"])
	}
	if v := arg(t, args, "p_session_id"); v != testRecipientID {
		t.Errorf("p_session_id = %v", v)
	}
	ids, _ := arg(t, args, "p_message_ids").([]any)
	if len(ids) != 1 || ids[0] != testMessageID {
		t.Errorf("p_message_ids = %v", ids)
	}
	if !strings.Contains(got.stdout, `"acked":["`+testMessageID+`"]`) || !strings.Contains(got.stdout, `"unknown":[]`) {
		t.Errorf("ack result = %s", got.stdout)
	}
	// A repeated ack of an acknowledged id answers identical bytes (C-30).
	second := r.exec(`{"message_ids":["`+testMessageID+`"]}`, "message", "ack", "--session", testRecipientID)
	if second.stdout != got.stdout {
		t.Errorf("ack is not idempotent on the wire:\n%s\n%s", got.stdout, second.stdout)
	}
}

// An id that cannot name a message is `unknown`, never an error, and is
// not sent to a uuid[] argument; the RPC still runs, because ownership of
// the SESSION is checked there (C-30).
func TestMessageAckUnknownIDShapes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`{"acked":[],"unknown":[]}`))
	got := r.exec(`{"message_ids":["`+notAUUID+`","x"]}`, "message", "ack", "--session", testRecipientID)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	ids, _ := arg(t, (*seen)[0], "p_message_ids").([]any)
	if len(ids) != 0 {
		t.Errorf("p_message_ids = %v, want the empty array", ids)
	}
	var res protocol.AckResult
	env := decode(t, got.stdout)
	raw, err := json.Marshal(env["result"])
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Decode(raw, &res); err != nil {
		t.Fatalf("the ack result does not validate: %v", err)
	}
	if len(res.Acked) != 0 || len(res.Unknown) != 2 {
		t.Errorf("result = %+v, want both ids unknown", res)
	}
	if !strings.Contains(got.stdout, `"acked":[]`) {
		t.Errorf("acked is not [] on the wire: %s", got.stdout)
	}
}

func TestMessageAckRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.fails("usage", 2, `{"message_ids":["x"]}`, "message", "ack")
	// The document is validated before the id shape.
	e := r.fails("invalid_input", 3, `{"message_ids":[]}`, "message", "ack", "--session", notAUUID)
	if details(t, e.stdout)["field"] != "message_ids" {
		t.Errorf("details = %v, want field message_ids", details(t, e.stdout))
	}
	got := r.fails("not_found", 6, `{"message_ids":["`+testMessageID+`"]}`, "message", "ack", "--session", notAUUID)
	if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("envelope differs from the uniform not_found: %s", got.stdout)
	}
	if r.be.total() != 0 {
		t.Fatalf("the backend was called for an id that cannot name a session")
	}
}

// A revoked membership ends `message receive` and `message ack` with the
// uniform unauthorized (C-08), whatever the session's state.
func TestMessageRevokedMembership(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"message", "receive", "--session", testRecipientID},
		{"message", "ack", "--session", testRecipientID},
	} {
		r := newRig(t)
		r.joined()
		r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
			postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized")
		}
		got := r.fails("unauthorized", 5, `{"message_ids":["`+testMessageID+`"]}`, args...)
		if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotMember())) {
			t.Fatalf("%v: envelope differs from the uniform unauthorized: %s", args, got.stdout)
		}
	}
}

func TestMessageUnknownVerb(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.fails("usage", 2, "", "message", "frobnicate")
}
