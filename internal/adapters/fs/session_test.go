package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// registration is the 4.4.2 document the session tests send.
const registration = `{"harness":"claude-code","harness_version":"2.1.251",` +
	`"session_name":"payments-api","activity":"busy","inbound":"accept"}`

// TestRegisterMintsASession covers C-10: an adapter-assigned id, state
// active while activity is busy, lease_until = now + the granted lease,
// and resumed false.
func TestRegisterMintsASession(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	result := r.ok(registration, "--profile", "alice", "session", "register")
	if str(t, result, "session_id") == "" {
		t.Fatalf("no session id: %v", result)
	}
	if str(t, result, "state") != protocol.SessionStateActive {
		t.Fatalf("state = %v, want active for activity busy", result["state"])
	}
	if result["resumed"] != false || result["lease_seconds"] != float64(90) {
		t.Fatalf("result = %v", result)
	}
	leaseUntil, err := time.Parse(time.RFC3339Nano, str(t, result, "lease_until"))
	if err != nil {
		t.Fatal(err)
	}
	if !leaseUntil.Equal(r.now.Add(90 * time.Second)) {
		t.Fatalf("lease_until = %v, want %v", leaseUntil, r.now.Add(90*time.Second))
	}
	if str(t, result, "human_label") != "alice@example.com" {
		t.Fatalf("human_label = %v", result["human_label"])
	}
	if result["is_self"] != false {
		t.Fatalf("is_self = %v, want false outside session list", result["is_self"])
	}
}

// TestTwoRegistrationsWithOneNameAreTwoSessions covers C-11.
func TestTwoRegistrationsWithOneNameAreTwoSessions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	first := r.register("alice", "payments-api")
	second := r.register("alice", "payments-api")
	if first == second {
		t.Fatal("two registrations with one name produced one session")
	}
	sessions, _ := r.ok("", "--profile", "alice", "session", "list")["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("sessions = %v, want both", sessions)
	}
}

// TestListIsTeamScopedAndMarksSelf covers C-12 and 4.5.6.
func TestListIsTeamScopedAndMarksSelf(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bobSession := r.register("bob", "bob-1")
	r.team("carol", "other")
	carol := r.register("carol", "carol-1")

	result := r.ok("", "--profile", "alice", "session", "list", "--session", alice)
	if str(t, result, "server_time") == "" {
		t.Fatalf("no server_time: %v", result)
	}
	sessions, _ := result["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("sessions = %v, want alice's and bob's", sessions)
	}
	for _, s := range sessions {
		entry, _ := s.(map[string]any)
		id := str(t, entry, "session_id")
		if id == carol {
			t.Fatal("a session of another team is listed (C-12)")
		}
		if entry["is_self"] != (id == alice) {
			t.Fatalf("is_self = %v for %s", entry["is_self"], id)
		}
		if str(t, entry, "human_label") == "" {
			t.Fatalf("no human_label on %v", entry)
		}
	}
	if bobSession == "" {
		t.Fatal("bob has no session")
	}
	// An unknown --session id is not an error; it just marks nothing.
	for _, s := range mustSessions(t, r.ok("", "--profile", "alice", "session", "list", "--session", "nosuch")) {
		if s["is_self"] != false {
			t.Fatalf("is_self set for an unknown id: %v", s)
		}
	}
}

// TestLeaseExpiryIsAuthoritative covers C-14 with an injected clock: after
// the lease runs out the session is offline, listed only with
// --include-offline, and `session close` was only ever an optimisation.
func TestLeaseExpiryIsAuthoritative(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	r.ok(`{"harness":"h","harness_version":"1","session_name":"short","activity":"busy",`+
		`"inbound":"accept","lease_seconds":1}`, "--profile", "alice", "session", "register")

	if got := mustSessions(t, r.ok("", "--profile", "alice", "session", "list")); len(got) != 1 {
		t.Fatalf("before expiry: %v", got)
	}
	r.now = r.now.Add(3 * time.Second)
	if got := mustSessions(t, r.ok("", "--profile", "alice", "session", "list")); len(got) != 0 {
		t.Fatalf("after expiry without the flag: %v", got)
	}
	offline := mustSessions(t, r.ok("", "--profile", "alice", "session", "list", "--include-offline"))
	if len(offline) != 1 || str(t, offline[0], "state") != protocol.SessionStateOffline {
		t.Fatalf("after expiry with the flag: %v", offline)
	}
}

// TestHeartbeatOwnershipAndRenewal covers C-13: another member's heartbeat
// is not_found, byte-identical to an unknown id, and the owner's heartbeat
// renews the lease and applies the new metadata.
func TestHeartbeatOwnershipAndRenewal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")

	foreign := r.fails("not_found", 6, "{}", "--profile", "bob", "session", "heartbeat", "--session", alice)
	unknown := r.fails("not_found", 6, "{}", "--profile", "bob", "session", "heartbeat", "--session", "nosuchsession")
	if errorJSON(t, foreign.stdout) != errorJSON(t, unknown.stdout) {
		t.Fatalf("a foreign id and an unknown id differ:\n%s\n%s", foreign.stdout, unknown.stdout)
	}

	r.now = r.now.Add(10 * time.Second)
	result := r.ok(`{"activity":"idle","session_name":"renamed","inbound":"hold","lease_seconds":120}`,
		"--profile", "alice", "session", "heartbeat", "--session", alice)
	if str(t, result, "state") != protocol.SessionStateIdle {
		t.Fatalf("state = %v, want idle after activity idle", result)
	}
	leaseUntil, err := time.Parse(time.RFC3339Nano, str(t, result, "lease_until"))
	if err != nil {
		t.Fatal(err)
	}
	if !leaseUntil.Equal(r.now.Add(120 * time.Second)) {
		t.Fatalf("lease_until = %v", leaseUntil)
	}
	listed := mustSessions(t, r.ok("", "--profile", "alice", "session", "list"))
	if str(t, listed[0], "session_name") != "renamed" || str(t, listed[0], "inbound") != "hold" {
		t.Fatalf("heartbeat did not apply the new members: %v", listed[0])
	}
}

// TestCloseIsIdempotentAndBlocksHeartbeat covers C-15.
func TestCloseIsIdempotentAndBlocksHeartbeat(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	id := r.register("alice", "alice-1")

	for range 2 {
		result := r.ok("", "--profile", "alice", "session", "close", "--session", id)
		if str(t, result, "state") != protocol.SessionStateOffline || str(t, result, "session_id") != id {
			t.Fatalf("close = %v", result)
		}
	}
	got := r.fails("conflict", 7, "{}", "--profile", "alice", "session", "heartbeat", "--session", id)
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	details, _ := object["details"].(map[string]any)
	if details["reason"] != reasonSessionClosed {
		t.Fatalf("details = %v", details)
	}
}

// TestRegistrationCapsAndUnknownMembers covers C-16 and C-17: a value
// exactly at a cap is accepted, one past it is invalid_input naming the
// member, and unknown members are accepted and not echoed.
func TestRegistrationCapsAndUnknownMembers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	at := strings.Repeat("n", protocol.MaxSessionNameCodepoints)
	over := strings.Repeat("n", protocol.MaxSessionNameCodepoints+1)

	r.ok(`{"harness":"h","harness_version":"1","session_name":"`+at+
		`","activity":"busy","inbound":"accept"}`, "--profile", "alice", "session", "register")
	got := r.fails("invalid_input", 3, `{"harness":"h","harness_version":"1","session_name":"`+over+
		`","activity":"busy","inbound":"accept"}`, "--profile", "alice", "session", "register")
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	details, _ := object["details"].(map[string]any)
	if details["field"] != "session_name" {
		t.Fatalf("details = %v", details)
	}
	if strings.Contains(got.stdout, over) {
		t.Fatal("the offending value was echoed")
	}
	r.fails("invalid_input", 3, `{"harness":"h","harness_version":"1","session_name":"x",`+
		`"activity":"busy","inbound":"accept","session_description":"`+
		strings.Repeat("d", protocol.MaxDescriptionChars+1)+`"}`,
		"--profile", "alice", "session", "register")

	result := r.ok(`{"harness":"h","harness_version":"1","session_name":"loose","activity":"busy",`+
		`"inbound":"accept","not_a_member":{"nested":1},"another":"x"}`,
		"--profile", "alice", "session", "register")
	for _, key := range []string{"not_a_member", "another"} {
		if _, present := result[key]; present {
			t.Fatalf("an unknown member was echoed back: %v", result)
		}
	}
}

// TestLeaseRangeIsTheAdapters: the protocol shape requires only a positive
// value; the range comes from describe.lease and is checked with
// Lease.CheckSeconds.
func TestLeaseRangeIsTheAdapters(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	r.ok(`{"harness":"h","harness_version":"1","session_name":"one","activity":"busy",`+
		`"inbound":"accept","lease_seconds":1}`, "--profile", "alice", "session", "register")
	r.fails("invalid_input", 3, `{"harness":"h","harness_version":"1","session_name":"big",`+
		`"activity":"busy","inbound":"accept","lease_seconds":601}`,
		"--profile", "alice", "session", "register")
	r.fails("invalid_input", 3, `{"harness":"h","harness_version":"1","session_name":"zero",`+
		`"activity":"busy","inbound":"accept","lease_seconds":0}`,
		"--profile", "alice", "session", "register")
}

// TestInboundRoundTrip covers C-42.
func TestInboundRoundTrip(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	result := r.ok(`{"harness":"h","harness_version":"1","session_name":"in","activity":"busy",`+
		`"inbound":"refuse"}`, "--profile", "alice", "session", "register")
	id := str(t, result, "session_id")
	listed := mustSessions(t, r.ok("", "--profile", "alice", "session", "list"))
	if str(t, listed[0], "inbound") != protocol.InboundRefuse {
		t.Fatalf("inbound = %v", listed[0])
	}
	r.ok(`{"inbound":"hold"}`, "--profile", "alice", "session", "heartbeat", "--session", id)
	listed = mustSessions(t, r.ok("", "--profile", "alice", "session", "list"))
	if str(t, listed[0], "inbound") != protocol.InboundHold {
		t.Fatalf("inbound after heartbeat = %v", listed[0])
	}
}

// TestResumeKeepsTheInbox covers C-19: a closed session re-opens in place
// with its pending messages, a foreign id is not_found and an unknown id
// gives byte-identical JSON.
func TestResumeKeepsTheInbox(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")

	r.ok("", "--profile", "alice", "session", "close", "--session", alice)
	r.send("bob", bob, alice, "while you were out")

	result := r.ok(`{"harness":"h","harness_version":"1","session_name":"alice-1",`+
		`"activity":"busy","inbound":"accept","resume":{"session_id":"`+alice+`"}}`,
		"--profile", "alice", "session", "register")
	if str(t, result, "session_id") != alice || result["resumed"] != true {
		t.Fatalf("resume = %v", result)
	}
	if str(t, result, "state") == protocol.SessionStateOffline {
		t.Fatalf("a resumed session is still offline: %v", result)
	}
	messages, _ := r.ok("", "--profile", "alice", "message", "receive", "--session", alice)["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("the resumed session lost its inbox: %v", messages)
	}

	foreign := r.fails("not_found", 6, `{"harness":"h","harness_version":"1","session_name":"x",`+
		`"activity":"busy","inbound":"accept","resume":{"session_id":"`+alice+`"}}`,
		"--profile", "bob", "session", "register")
	unknown := r.fails("not_found", 6, `{"harness":"h","harness_version":"1","session_name":"x",`+
		`"activity":"busy","inbound":"accept","resume":{"session_id":"nosuchsession"}}`,
		"--profile", "bob", "session", "register")
	if errorJSON(t, foreign.stdout) != errorJSON(t, unknown.stdout) {
		t.Fatalf("resume of a foreign id and of an unknown id differ:\n%s\n%s", foreign.stdout, unknown.stdout)
	}
}

// TestResumeOfALiveSessionIsConflict covers C-19b: two processes must
// never drain one inbox, and the refused resume leaves the session file
// byte for byte as it was.
func TestResumeOfALiveSessionIsConflict(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, _ := r.team("alice", "ops")
	id := r.register("alice", "alice-1")
	path := filepath.Join(r.root, teamsDirName, teamRef, sessionsDirName, id+jsonExt)
	before, err := os.ReadFile(path) //nolint:gosec // a path built from this test's own temp root
	if err != nil {
		t.Fatal(err)
	}

	resume := `{"harness":"h","harness_version":"1","session_name":"changed","activity":"idle",` +
		`"inbound":"hold","resume":{"session_id":"` + id + `"}}`
	got := r.fails("conflict", 7, resume, "--profile", "alice", "session", "register")
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	details, _ := object["details"].(map[string]any)
	if details["reason"] != reasonSessionLive {
		t.Fatalf("details = %v", details)
	}
	after, err := os.ReadFile(path) //nolint:gosec // the same path
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the refused resume changed the session file:\n%s\n%s", before, after)
	}

	// After a close the same resume succeeds; so does one after the lease
	// has expired, which is what makes close an optimisation (4.5.8).
	r.ok("", "--profile", "alice", "session", "close", "--session", id)
	if result := r.ok(resume, "--profile", "alice", "session", "register"); result["resumed"] != true {
		t.Fatalf("resume after close = %v", result)
	}
	r.now = r.now.Add(200 * time.Second)
	if result := r.ok(resume, "--profile", "alice", "session", "register"); result["resumed"] != true {
		t.Fatalf("resume after expiry = %v", result)
	}
}

// TestMissingSessionFlagIsUsage: --session is required where 4.1 lists it.
func TestMissingSessionFlagIsUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.team("alice", "ops")
	for _, args := range [][]string{
		{"session", "heartbeat"},
		{"session", "close"},
		{"message", "receive"},
		{"message", "ack"},
	} {
		r.fails("usage", 2, "{}", append([]string{"--profile", "alice"}, args...)...)
	}
}
