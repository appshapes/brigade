package fs

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestSendStampsTheSender covers C-20 and 4.5.5: the identity members come
// from the adapter's own authority and a later receive returns the message.
func TestSendStampsTheSender(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	response := r.send("alice", alice, bob, "the migration has landed")
	if str(t, response, "status") != protocol.SendStatusAccepted ||
		response["duplicate"] != false || response["hop_count"] != float64(0) {
		t.Fatalf("response = %v", response)
	}

	messages := mustMessages(t, r.ok("", "--profile", "bob", "message", "receive", "--session", bob))
	if len(messages) != 1 {
		t.Fatalf("messages = %v", messages)
	}
	message := messages[0]
	if str(t, message, "message_id") != str(t, response, "message_id") ||
		str(t, message, "team_ref") != teamRef ||
		str(t, message, "delivery_state") != protocol.DeliveryStateAccepted ||
		str(t, message, "kind") != protocol.KindText ||
		message["hop_count"] != float64(0) || str(t, message, "created_at") == "" {
		t.Fatalf("message = %v", message)
	}
	sender, _ := message["sender"].(map[string]any)
	if str(t, sender, "session_id") != alice || str(t, sender, "session_name") != "alice-1" ||
		str(t, sender, "human_label") != "alice@example.com" || str(t, sender, "principal_ref") == "" {
		t.Fatalf("sender = %v", sender)
	}
}

// TestIdempotency covers C-21 and C-22.
func TestIdempotency(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")
	second := r.register("bob", "bob-2")

	request := `{"sender_session_id":"` + alice + `","recipient_session_id":"` + bob +
		`","body":"once","idempotency_key":"k-1"}`
	first := r.ok(request, "--profile", "alice", "message", "send")
	repeat := r.ok(request, "--profile", "alice", "message", "send")
	if str(t, repeat, "message_id") != str(t, first, "message_id") || repeat["duplicate"] != true {
		t.Fatalf("repeat = %v, want the first id with duplicate true (%v)", repeat, first)
	}
	if got := mustMessages(t, r.ok("", "--profile", "bob", "message", "receive", "--session", bob)); len(got) != 1 {
		t.Fatalf("the duplicate created a second message: %v", got)
	}

	for _, body := range []string{
		`{"sender_session_id":"` + alice + `","recipient_session_id":"` + bob +
			`","body":"different","idempotency_key":"k-1"}`,
		`{"sender_session_id":"` + alice + `","recipient_session_id":"` + second +
			`","body":"once","idempotency_key":"k-1"}`,
	} {
		got := r.fails("conflict", 7, body, "--profile", "alice", "message", "send")
		object, _ := decode(t, got.stdout)["error"].(map[string]any)
		details, _ := object["details"].(map[string]any)
		if details["reason"] != reasonIdempotencyKeyReused {
			t.Fatalf("details = %v", details)
		}
	}
}

// TestForbiddenSenderMembers covers C-23: the six members of 4.4.6 are
// rejected with details.field naming the member.
func TestForbiddenSenderMembers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	alice := r.register("alice", "alice-1")
	bob := r.register("alice", "alice-2")

	for _, member := range []string{"sender", "principal_ref", "human_label", "team_ref", "created_at", "hop_count"} {
		got := r.fails("invalid_input", 3,
			`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+
				`","body":"x","`+member+`":null}`,
			"--profile", "alice", "message", "send")
		object, _ := decode(t, got.stdout)["error"].(map[string]any)
		details, _ := object["details"].(map[string]any)
		if details["field"] != member {
			t.Fatalf("%s: details = %v", member, details)
		}
	}
}

// TestSenderAndRecipientOwnership covers C-24 and C-25: a session the
// caller does not own, and a recipient outside the team, are both the
// uniform not_found, byte-identical to a random id.
func TestSenderAndRecipientOwnership(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")
	r.team("carol", "other")
	carol := r.register("carol", "carol-1")

	impersonate := r.fails("not_found", 6,
		`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+`","body":"x"}`,
		"--profile", "bob", "message", "send")
	unknownSender := r.fails("not_found", 6,
		`{"sender_session_id":"nosuchsession","recipient_session_id":"`+bob+`","body":"x"}`,
		"--profile", "bob", "message", "send")
	if errorJSON(t, impersonate.stdout) != errorJSON(t, unknownSender.stdout) {
		t.Fatalf("impersonation and an unknown id differ:\n%s\n%s", impersonate.stdout, unknownSender.stdout)
	}

	foreign := r.fails("not_found", 6,
		`{"sender_session_id":"`+carol+`","recipient_session_id":"`+bob+`","body":"x"}`,
		"--profile", "carol", "message", "send")
	random := r.fails("not_found", 6,
		`{"sender_session_id":"`+carol+`","recipient_session_id":"nosuchsession","body":"x"}`,
		"--profile", "carol", "message", "send")
	if errorJSON(t, foreign.stdout) != errorJSON(t, random.stdout) {
		t.Fatalf("a foreign recipient and a random one differ:\n%s\n%s", foreign.stdout, random.stdout)
	}
}

// TestTeamIsolationHasNoExistenceOracle covers C-26: a foreign session is
// invisible and unreachable, and a profile rebound to a real team_ref is
// refused byte-identically to one rebound to a random one.
func TestTeamIsolationHasNoExistenceOracle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.join("bob", secret)
	bob := r.register("bob", "bob-1")
	r.team("carol", "other")

	if got := mustSessions(t, r.ok("", "--profile", "carol", "session", "list")); len(got) != 0 {
		t.Fatalf("carol sees T1 sessions: %v", got)
	}
	r.fails("not_found", 6, "", "--profile", "carol", "message", "receive", "--session", bob)

	rebind(t, r, "carol", teamRef)
	boundList := r.fails("unauthorized", 5, "", "--profile", "carol", "session", "list")
	boundRegister := r.fails("unauthorized", 5, registration, "--profile", "carol", "session", "register")
	rebind(t, r, "carol", "ffffffffffffffffffffffffffffffff")
	randomList := r.fails("unauthorized", 5, "", "--profile", "carol", "session", "list")
	randomRegister := r.fails("unauthorized", 5, registration, "--profile", "carol", "session", "register")

	// A real member whose profile was tampered with gets the same answer:
	// the refusal is about the membership, never about the team's
	// existence.
	rebind(t, r, "bob", "ffffffffffffffffffffffffffffffff")
	tampered := r.fails("unauthorized", 5, "", "--profile", "bob", "session", "list")
	if errorJSON(t, tampered.stdout) != errorJSON(t, randomList.stdout) {
		t.Fatalf("a tampered member and a stranger differ:\n%s\n%s", tampered.stdout, randomList.stdout)
	}

	if errorJSON(t, boundList.stdout) != errorJSON(t, randomList.stdout) {
		t.Fatalf("session list: a real team and a random one differ:\n%s\n%s", boundList.stdout, randomList.stdout)
	}
	if errorJSON(t, boundRegister.stdout) != errorJSON(t, randomRegister.stdout) {
		t.Fatalf("session register: they differ:\n%s\n%s", boundRegister.stdout, randomRegister.stdout)
	}
	// The positive control: `unauthorized` and `not_found` are NOT the
	// same bytes, so the comparisons above can fail.
	if errorJSON(t, boundList.stdout) == errorJSON(t, r.fails("not_found", 6, "",
		"--profile", "alice", "message", "receive", "--session", "nosuchsession").stdout) {
		t.Fatal("the byte-identity check cannot distinguish two different errors")
	}
}

// TestContentCaps covers C-27: a value exactly at a cap is accepted, one
// past it is invalid_input, and an empty body is refused.
func TestContentCaps(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	alice := r.register("alice", "alice-1")
	bob := r.register("alice", "alice-2")
	send := func(body, summary string) string {
		return `{"sender_session_id":"` + alice + `","recipient_session_id":"` + bob +
			`","body":"` + body + `","summary":"` + summary + `"}`
	}
	r.ok(send(strings.Repeat("a", protocol.MaxBodyBytes), "ok"), "--profile", "alice", "message", "send")
	r.fails("invalid_input", 3, send(strings.Repeat("a", protocol.MaxBodyBytes+1), "ok"),
		"--profile", "alice", "message", "send")
	r.fails("invalid_input", 3, send("body", strings.Repeat("s", protocol.MaxSummaryChars+1)),
		"--profile", "alice", "message", "send")
	r.fails("invalid_input", 3, send("", "ok"), "--profile", "alice", "message", "send")
}

// TestPerPairUnackedCap covers the half of C-28 that is cheap to drive end
// to end: the sixteenth unacknowledged message from one sender to one
// recipient is refused with sender_quota_for_recipient, while a different
// sender to the same recipient still succeeds.
func TestPerPairUnackedCap(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	other := r.register("alice", "alice-2")
	bob := r.register("bob", "bob-1")

	for i := range protocol.MaxUnackedPerSenderRecipient {
		r.send("alice", alice, bob, "m"+strconv.Itoa(i))
	}
	got := r.fails("rate_limited", 8,
		`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+`","body":"one too many"}`,
		"--profile", "alice", "message", "send")
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	details, _ := object["details"].(map[string]any)
	if details["reason"] != reasonSenderQuotaForRecipient {
		t.Fatalf("details = %v", details)
	}
	if retry, _ := object["retry_after_ms"].(float64); retry <= 0 {
		t.Fatalf("retry_after_ms = %v, want > 0", object["retry_after_ms"])
	}
	// Another sender to the same recipient is unaffected.
	r.send("alice", other, bob, "from the second session")
}

// TestExplicitReplyChain covers C-29: hop_count increments by exactly one
// per hop, the chain past max_hop_count is loop_detected, and a reply_to
// the sender never received is not_found.
func TestExplicitReplyChain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	r.fails("not_found", 6,
		`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+
			`","body":"x","reply_to":"nosuchmessage"}`,
		"--profile", "alice", "message", "send")

	from, to := "alice", "bob"
	fromID, toID := alice, bob
	previous := ""
	for hop := range protocol.MaxHopCount + 1 {
		body := `{"sender_session_id":"` + fromID + `","recipient_session_id":"` + toID + `","body":"h"`
		if previous != "" {
			body += `,"reply_to":"` + previous + `"`
		}
		result := r.ok(body+"}", "--profile", from, "message", "send")
		if result["hop_count"] != float64(hop) {
			t.Fatalf("hop %d: hop_count = %v", hop, result["hop_count"])
		}
		previous = str(t, result, "message_id")
		// Acknowledge so the per-pair unacked cap never trips: the chain
		// is about hop counting, not about inbox pressure.
		r.ok(`{"message_ids":["`+previous+`"]}`, "--profile", to, "message", "ack", "--session", toID)
		from, to = to, from
		fromID, toID = toID, fromID
	}
	r.fails("loop_detected", 12,
		`{"sender_session_id":"`+fromID+`","recipient_session_id":"`+toID+
			`","body":"one hop too far","reply_to":"`+previous+`"}`,
		"--profile", from, "message", "send")
}

// TestImplicitReplyChain covers C-29b: an unlabelled answer is still an
// answer, and a message sent after implicit_reply_window_seconds starts a
// new chain at 0.
func TestImplicitReplyChain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	from, to := "alice", "bob"
	fromID, toID := alice, bob
	for hop := range protocol.MaxHopCount + 1 {
		result := r.send(from, fromID, toID, "h")
		if result["hop_count"] != float64(hop) {
			t.Fatalf("hop %d: hop_count = %v", hop, result["hop_count"])
		}
		r.ok(`{"message_ids":["`+str(t, result, "message_id")+`"]}`,
			"--profile", to, "message", "ack", "--session", toID)
		from, to = to, from
		fromID, toID = toID, fromID
	}
	r.fails("loop_detected", 12,
		`{"sender_session_id":"`+fromID+`","recipient_session_id":"`+toID+`","body":"one too far"}`,
		"--profile", from, "message", "send")

	// Past the window the chain is forgotten and hop_count is 0 again.
	r.now = r.now.Add(time.Duration(protocol.ImplicitReplyWindowSeconds+1) * time.Second)
	if result := r.send(from, fromID, toID, "much later"); result["hop_count"] != float64(0) {
		t.Fatalf("after the window: hop_count = %v, want 0", result["hop_count"])
	}
}

// TestAckIsIdempotentAndScoped covers C-30.
func TestAckIsIdempotentAndScoped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")
	id := str(t, r.send("alice", alice, bob, "for bob"), "message_id")

	// A's ack of a message addressed to B reports it unknown, never an error.
	byA := r.ok(`{"message_ids":["`+id+`"]}`, "--profile", "alice", "message", "ack", "--session", alice)
	if len(mustStrings(t, byA, "acked")) != 0 || mustStrings(t, byA, "unknown")[0] != id {
		t.Fatalf("A's ack = %v", byA)
	}
	for range 2 {
		byB := r.ok(`{"message_ids":["`+id+`","nosuchmessage"]}`,
			"--profile", "bob", "message", "ack", "--session", bob)
		if mustStrings(t, byB, "acked")[0] != id || mustStrings(t, byB, "unknown")[0] != "nosuchmessage" {
			t.Fatalf("B's ack = %v", byB)
		}
	}
	if got := mustMessages(t, r.ok("", "--profile", "bob", "message", "receive", "--session", bob)); len(got) != 0 {
		t.Fatalf("an acknowledged message is still returned: %v", got)
	}
}

// TestClosedSessions covers C-31: sending FROM a closed session is
// conflict; sending TO one is accepted and the message waits.
func TestClosedSessions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	r.ok("", "--profile", "bob", "session", "close", "--session", bob)
	r.send("alice", alice, bob, "waiting for you")

	r.ok("", "--profile", "alice", "session", "close", "--session", alice)
	got := r.fails("conflict", 7,
		`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+`","body":"x"}`,
		"--profile", "alice", "message", "send")
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	details, _ := object["details"].(map[string]any)
	if details["reason"] != reasonSessionClosed {
		t.Fatalf("details = %v", details)
	}
}

// TestReceiveIsOldestFirstAndLimited covers C-32 and the --limit bounds.
func TestReceiveIsOldestFirstAndLimited(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	for i := range 10 {
		r.send("alice", alice, bob, "m"+strconv.Itoa(i))
		r.now = r.now.Add(time.Millisecond)
	}
	messages := mustMessages(t, r.ok("", "--profile", "bob", "message", "receive", "--session", bob))
	if len(messages) != 10 {
		t.Fatalf("messages = %d, want 10", len(messages))
	}
	for i, m := range messages {
		if str(t, m, "body") != "m"+strconv.Itoa(i) {
			t.Fatalf("message %d is %v; the batch is not oldest first", i, m)
		}
	}
	limited := mustMessages(t, r.ok("", "--profile", "bob", "message", "receive", "--session", bob, "--limit", "3"))
	if len(limited) != 3 || str(t, limited[0], "body") != "m0" {
		t.Fatalf("limited = %v", limited)
	}
	for _, value := range []string{"0", "201", "-1"} {
		r.fails("usage", 2, "", "--profile", "bob", "message", "receive", "--session", bob, "--limit", value)
	}
}

// TestSendToARevokedMembersSessionIsNotFound covers 4.5.6 for a recipient
// whose owner has left the team: a session outside the team "cannot be
// messaged", and a revoked member's sessions are outside the team by the
// same rule that already hides them from `session list` (C-08). The
// refusal is the uniform not_found, byte-identical to an id that never
// existed — anything else would be an existence oracle for a session the
// caller may no longer see. The accepted send before the leave is the
// positive control.
func TestSendToARevokedMembersSessionIsNotFound(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	// Positive control: while B is a member, the same send is accepted.
	r.send("alice", alice, bob, "while B is still a member")

	r.ok("", "--profile", "bob", "team", "leave")

	for _, entry := range mustSessions(t, r.ok("", "--profile", "alice", "session", "list", "--include-offline")) {
		if str(t, entry, "session_id") == bob {
			t.Fatal("a revoked member's session is still listed (C-08)")
		}
	}
	gone := r.fails("not_found", 6,
		`{"sender_session_id":"`+alice+`","recipient_session_id":"`+bob+`","body":"x"}`,
		"--profile", "alice", "message", "send")
	unknown := r.fails("not_found", 6,
		`{"sender_session_id":"`+alice+`","recipient_session_id":"ffffffffffffffffffffffffffffffff","body":"x"}`,
		"--profile", "alice", "message", "send")
	if errorJSON(t, gone.stdout) != errorJSON(t, unknown.stdout) {
		t.Fatalf("a revoked member's session and an unknown id differ:\n%s\n%s", gone.stdout, unknown.stdout)
	}
	// Nothing was parked in the dead inbox: the send never reached it.
	if got := mustMessages(t, r.ok("", "--profile", "alice", "message", "receive", "--session", alice)); len(got) != 0 {
		t.Fatalf("alice's own inbox changed: %v", got)
	}

	// The owner's own verbs on that session answer the ladder of section
	// 5 first — `team leave` unbound the profile, so every one of them is
	// `config` (exit 11) before ownership is ever consulted. This asserts
	// today's behaviour; it is not changed here.
	for _, args := range [][]string{
		{"--profile", "bob", "message", "receive", "--session", bob},
		{"--profile", "bob", "message", "ack", "--session", bob},
		{"--profile", "bob", "session", "heartbeat", "--session", bob},
		{"--profile", "bob", "session", "close", "--session", bob},
	} {
		r.fails("config", 11, `{"message_ids":[]}`, args...)
	}
	r.fails("config", 11, `{"harness":"claude-code","harness_version":"2.1.251","session_name":"bob-1",`+
		`"activity":"busy","inbound":"accept","resume":{"session_id":"`+bob+`"}}`,
		"--profile", "bob", "session", "register")
}
