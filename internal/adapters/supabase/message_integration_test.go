package supabase

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// The `message *` verbs against the REAL stack (P2-9, brief section 7):
// the adapter-stamped sender of 4.5.5, the uniform not_found of 4.5.6 and
// 4.5.7 as the migrations actually raise it, deterministic idempotency,
// the server-computed hop chain, and the closed-session rules. Every test
// self-skips without the stack and mints its own principals and team.

// randomUUID is a uuid the backend has certainly never issued.
const randomUUID = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"

// regDoc is the standard registration document.
func regDoc(name string) string {
	return `{"harness":"h","harness_version":"1","session_name":"` + name + `","activity":"busy","inbound":"accept"}`
}

// sendLive sends one message and returns the result object.
func sendLive(t *testing.T, r *rig, doc string) map[string]any {
	t.Helper()
	got := r.exec(doc, "message", "send")
	if got.code != 0 {
		t.Fatalf("message send: exit %d, stdout %s stderr %s", got.code, got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	return result
}

// receiveLive drains a session's inbox.
func receiveLive(t *testing.T, r *rig, session string) []map[string]any {
	t.Helper()
	result := r.ok("message", "receive", "--session", session)
	items, ok := result["messages"].([]any)
	if !ok {
		t.Fatalf("message receive: no messages array in %v", result)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, _ := item.(map[string]any)
		out = append(out, m)
	}
	return out
}

// TestIntegrationMessageRoundTrip: send, receive, ack (I-02, I-03, I-06,
// C-20, C-30). The whole sender identity is stamped by the backend from
// its own authority; receive returns the envelope; ack is idempotent and
// empties the inbox.
func TestIntegrationMessageRoundTrip(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-9-round"))
	b := liveJoin(t, secret)
	senderName := liveName(t, "sa")
	_, sa := registerLive(t, a, regDoc(senderName))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	res := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+
		`","body":"hello","summary":"greeting"}`)
	if res["status"] != protocol.SendStatusAccepted {
		t.Errorf("send: status %v, want accepted (4.4.7)", res["status"])
	}
	if dup, _ := res["duplicate"].(bool); dup {
		t.Errorf("send: duplicate true on a first send")
	}
	if hop, _ := res["hop_count"].(float64); hop != 0 {
		t.Errorf("send: hop_count %v, want 0", res["hop_count"])
	}
	messageID, _ := res["message_id"].(string)

	messages := receiveLive(t, b, sb)
	if len(messages) != 1 {
		t.Fatalf("receive: %d messages, want 1", len(messages))
	}
	m := messages[0]
	if m["message_id"] != messageID || m["body"] != "hello" || m["summary"] != "greeting" {
		t.Errorf("receive: envelope = %v", m)
	}
	if m["delivery_state"] != protocol.DeliveryStateAccepted || m["kind"] != protocol.KindText ||
		m["protocol_version"] != protocol.ProtocolVersion {
		t.Errorf("receive: envelope header = %v", m)
	}
	sender, _ := m["sender"].(map[string]any)
	if sender["session_id"] != sa || sender["session_name"] != senderName ||
		sender["human_label"] != "alice@example.com" {
		t.Errorf("receive: sender %v is not stamped from the adapter's authority (4.5.5)", sender)
	}
	if !validUUID(sender["principal_ref"].(string)) || m["team_ref"] == "" {
		t.Errorf("receive: principal_ref %v / team_ref %v", sender["principal_ref"], m["team_ref"])
	}

	// The sender's own session does not own the message: unknown, never
	// an error (C-30).
	notOwner := a.exec(`{"message_ids":["`+messageID+`"]}`, "message", "ack", "--session", sa)
	if notOwner.code != 0 {
		t.Fatalf("ack by a non-owner: exit %d, %s", notOwner.code, notOwner.stdout)
	}
	if !strings.Contains(notOwner.stdout, `"acked":[]`) || !strings.Contains(notOwner.stdout, messageID) {
		t.Errorf("ack by a non-owner: %s", notOwner.stdout)
	}

	first := b.exec(`{"message_ids":["`+messageID+`"]}`, "message", "ack", "--session", sb)
	if first.code != 0 {
		t.Fatalf("ack: exit %d, %s", first.code, first.stdout)
	}
	if !strings.Contains(first.stdout, `"acked":["`+messageID+`"]`) || !strings.Contains(first.stdout, `"unknown":[]`) {
		t.Errorf("ack: %s", first.stdout)
	}
	second := b.exec(`{"message_ids":["`+messageID+`"]}`, "message", "ack", "--session", sb)
	if second.stdout != first.stdout {
		t.Errorf("ack is not idempotent:\n%s\n%s", first.stdout, second.stdout)
	}
	if n := len(receiveLive(t, b, sb)); n != 0 {
		t.Errorf("receive after ack: %d messages, want none (4.5.3)", n)
	}
}

// TestIntegrationMessageIdempotency: one key, one logical message; the
// same key with another body or another recipient is conflict (C-21,
// C-22, 4.5.4).
func TestIntegrationMessageIdempotency(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-9-idem"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
	_, sb2 := registerLive(t, b, regDoc(liveName(t, "sb2")))
	key := liveName(t, "k")

	doc := `{"sender_session_id":"` + sa + `","recipient_session_id":"` + sb +
		`","body":"once","idempotency_key":"` + key + `"}`
	first := sendLive(t, a, doc)
	second := sendLive(t, a, doc)
	if first["message_id"] != second["message_id"] {
		t.Errorf("the same key produced two message ids")
	}
	if dup, _ := second["duplicate"].(bool); !dup {
		t.Errorf("the second send: duplicate %v, want true", second["duplicate"])
	}
	if n := len(receiveLive(t, b, sb)); n != 1 {
		t.Errorf("receive: %d messages, want exactly one (4.5.4)", n)
	}

	changed := a.fails("conflict", 7, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+
		`","body":"changed","idempotency_key":"`+key+`"}`, "message", "send")
	if details(t, changed.stdout)["reason"] != "idempotency_key" {
		t.Errorf("same key, other body: details %v, want reason idempotency_key", details(t, changed.stdout))
	}
	a.fails("conflict", 7, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb2+
		`","body":"once","idempotency_key":"`+key+`"}`, "message", "send")
	if n := len(receiveLive(t, b, sb2)); n != 0 {
		t.Errorf("the refused send reached the second recipient")
	}
}

// TestIntegrationMessageIsolation: a sender the caller does not own, a
// recipient outside the team, an unknown uuid and an id that is not a
// uuid at all all answer ONE not_found envelope (C-24, C-25, 4.5.6,
// 4.5.7). invalid_input is the positive control.
func TestIntegrationMessageIsolation(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-9-iso"))
	b := liveJoin(t, secret)
	c, _ := liveTeam(t, liveName(t, "p2-9-iso-t2"))
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
	_, sc := registerLive(t, c, regDoc(liveName(t, "sc")))

	uniform := errorJSON(t, newFailure(t, errNotFound()))
	for name, got := range map[string]outcome{
		// B naming A's session as the sender (impersonation).
		"foreign sender": b.fails("not_found", 6, `{"sender_session_id":"`+sa+
			`","recipient_session_id":"`+sb+`","body":"x"}`, "message", "send"),
		// A random sender id.
		"unknown sender": b.fails("not_found", 6, `{"sender_session_id":"`+randomUUID+
			`","recipient_session_id":"`+sb+`","body":"x"}`, "message", "send"),
		// A sender id that is not a uuid at all (refused locally).
		"malformed sender": b.fails("not_found", 6, `{"sender_session_id":"`+notAUUID+
			`","recipient_session_id":"`+sb+`","body":"x"}`, "message", "send"),
		// C (team T2) naming B's session as the recipient.
		"foreign recipient": c.fails("not_found", 6, `{"sender_session_id":"`+sc+
			`","recipient_session_id":"`+sb+`","body":"x"}`, "message", "send"),
		// A random recipient id.
		"unknown recipient": c.fails("not_found", 6, `{"sender_session_id":"`+sc+
			`","recipient_session_id":"`+randomUUID+`","body":"x"}`, "message", "send"),
		// Receiving on another member's session.
		"foreign inbox": b.fails("not_found", 6, "", "message", "receive", "--session", sa),
	} {
		if errorJSON(t, got.stdout) != uniform {
			t.Errorf("%s: envelope differs from the uniform not_found:\n%s", name, got.stdout)
		}
	}
	if n := len(receiveLive(t, b, sb)); n != 0 {
		t.Errorf("receive: %d messages reached B from the refused sends", n)
	}

	// Positive control: a send to itself is invalid_input naming the
	// backend's own member name (5.11).
	self := a.fails("invalid_input", 3, `{"sender_session_id":"`+sa+
		`","recipient_session_id":"`+sa+`","body":"x"}`, "message", "send")
	if details(t, self.stdout)["field"] != "recipient_is_self" {
		t.Errorf("send to self: details %v, want field recipient_is_self", details(t, self.stdout))
	}
}

// TestIntegrationMessageHops: reply_to increments hop_count by one, an
// unlabelled answer within the implicit window does the same, and a
// reply_to naming a message the sender never received is the uniform
// not_found (C-29, C-29b, 4.5.12).
func TestIntegrationMessageHops(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-9-hops"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	m0 := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"m0"}`)
	if hop, _ := m0["hop_count"].(float64); hop != 0 {
		t.Errorf("m0: hop_count %v, want 0", m0["hop_count"])
	}
	id0, _ := m0["message_id"].(string)

	// Explicit: B answers the message it received.
	m1 := sendLive(t, b, `{"sender_session_id":"`+sb+`","recipient_session_id":"`+sa+
		`","body":"m1","reply_to":"`+id0+`"}`)
	if hop, _ := m1["hop_count"].(float64); hop != 1 {
		t.Errorf("m1 (reply_to m0): hop_count %v, want 1", m1["hop_count"])
	}
	// Implicit: A answers with no reply_to at all.
	m2 := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"m2"}`)
	if hop, _ := m2["hop_count"].(float64); hop != 2 {
		t.Errorf("m2 (no reply_to, within the implicit window): hop_count %v, want 2", m2["hop_count"])
	}

	// A sent m0 and never received it; and a reply_to that is not a uuid
	// at all. Both are the uniform not_found (no existence oracle).
	sent := a.fails("not_found", 6, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+
		`","body":"x","reply_to":"`+id0+`"}`, "message", "send")
	malformed := a.fails("not_found", 6, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+
		`","body":"x","reply_to":"`+notAUUID+`"}`, "message", "send")
	if errorJSON(t, sent.stdout) != errorJSON(t, malformed.stdout) {
		t.Fatalf("an unreceived reply_to differs from a malformed one:\n%s\n%s", sent.stdout, malformed.stdout)
	}
	if errorJSON(t, sent.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("reply_to not_found differs from the uniform one: %s", sent.stdout)
	}
}

// TestIntegrationMessageClosedSessions: a send FROM a closed session is
// conflict sender_closed; a send TO a closed but owned recipient is
// accepted and the message waits, and receive and ack still work on it
// (C-31, 4.5.8).
func TestIntegrationMessageClosedSessions(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-9-closed"))
	b := liveJoin(t, secret)
	_, closedSender := registerLive(t, a, regDoc(liveName(t, "cs")))
	_, open := registerLive(t, b, regDoc(liveName(t, "open")))
	a.ok("session", "close", "--session", closedSender)
	got := a.fails("conflict", 7, `{"sender_session_id":"`+closedSender+`","recipient_session_id":"`+open+
		`","body":"x"}`, "message", "send")
	if details(t, got.stdout)["reason"] != "sender_closed" {
		t.Errorf("send from a closed session: details %v, want reason sender_closed", details(t, got.stdout))
	}

	_, sender := registerLive(t, a, regDoc(liveName(t, "sender")))
	_, closedRecipient := registerLive(t, b, regDoc(liveName(t, "cr")))
	b.ok("session", "close", "--session", closedRecipient)
	res := sendLive(t, a, `{"sender_session_id":"`+sender+`","recipient_session_id":"`+closedRecipient+
		`","body":"parked"}`)
	messages := receiveLive(t, b, closedRecipient)
	if len(messages) != 1 || messages[0]["message_id"] != res["message_id"] {
		t.Fatalf("receive on a closed owned session: %v (4.5.8: the message waits)", messages)
	}
	id, _ := res["message_id"].(string)
	if ack := b.exec(`{"message_ids":["`+id+`"]}`, "message", "ack", "--session", closedRecipient); ack.code != 0 {
		t.Errorf("ack on a closed owned session: exit %d, %s", ack.code, ack.stdout)
	}
}

// TestIntegrationMessageLimit: --limit bounds the page, and the answer is
// always the `messages` array, [] when empty (4.1, JSON convention 3).
func TestIntegrationMessageLimit(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-9-limit"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
	for i := 0; i < 3; i++ {
		sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"m`+
			string(rune('0'+i))+`"}`)
	}
	got := b.exec("", "message", "receive", "--session", sb, "--limit", "2")
	if got.code != 0 {
		t.Fatalf("receive --limit 2: exit %d, %s", got.code, got.stdout)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if items, _ := result["messages"].([]any); len(items) != 2 {
		t.Errorf("receive --limit 2: %d messages, want 2", len(items))
	}
	if n := len(receiveLive(t, a, sa)); n != 0 {
		t.Errorf("A's inbox: %d messages, want none", n)
	}
	if empty := a.exec("", "message", "receive", "--session", sa); !strings.Contains(empty.stdout, `"messages":[]`) {
		t.Errorf("an empty inbox is not [] on the wire: %s", empty.stdout)
	}
}

// TestIntegrationRevocationClosesTheInboxAtOnce is I-16's Phase 2 half
// through the verbs (D22): the moment `team leave` revokes a membership,
// the principal's own inbox is closed to it. owned_active_session checks
// ownership and then ACTIVE membership, so `message receive` and
// `message ack` on a session the principal still owns answer the uniform
// `unauthorized` — byte-identical to the adapter's answer for a team it
// never belonged to — with no realtime lag anywhere in the path (the
// realtime half of I-16 is Phase 5). The profile is rebound by hand
// after the leave: an unbound profile would answer `config` locally and
// the RPC would never be reached, which would prove nothing.
func TestIntegrationRevocationClosesTheInboxAtOnce(t *testing.T) {
	a, secret := liveTeam(t, liveName(t, "p2-11-revoked"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
	teamRef := str(t, b.ok("profile", "status"), "team_ref")

	sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"before the leave"}`)
	if n := len(receiveLive(t, b, sb)); n != 1 {
		t.Fatalf("the inbox holds %d messages before the leave, want 1", n)
	}

	left := b.ok("team", "leave")
	if left["left"] != true {
		t.Fatalf("team leave = %v", left)
	}
	b.bindTeam(teamRef, "ops")

	want := newFailure(t, errNotMember())
	receive := b.fails("unauthorized", 5, "", "message", "receive", "--session", sb)
	if receive.stdout != want {
		t.Fatalf("receive after the leave: %q, want the uniform unauthorized %q", receive.stdout, want)
	}
	ack := b.fails("unauthorized", 5, `{"message_ids":["`+randomUUID+`"]}`, "message", "ack", "--session", sb)
	if ack.stdout != want {
		t.Fatalf("ack after the leave: %q, want %q", ack.stdout, want)
	}
	send := b.fails("unauthorized", 5,
		`{"sender_session_id":"`+sb+`","recipient_session_id":"`+sa+`","body":"still talking"}`, "message", "send")
	if send.stdout != want {
		t.Fatalf("send after the leave: %q, want %q", send.stdout, want)
	}
}
