package supabase

import (
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The `session *` verbs against the REAL stack (P2-8, brief section 7):
// the paths a fake backend cannot show — the migrations' own argument
// names and raise texts, PostgreSQL's timestamp rendering, the computed
// state, and the ownership answers the RPCs give. Every test self-skips
// through testutil.RequireSupabase (inside liveRig) without the
// BRIGADE_TEST_LIVE opt-in or without the stack, mints its own
// anonymous principals and its own team, and never resets the database.

// liveTeam creates a team through the `team create` VERB on a fresh
// anonymous principal and returns the rig, bound to that team, and the
// join secret. P2-8 built the fixture out of the create_team RPC because
// `team create` did not exist yet; P2-11 switched it to the verb, so
// every test that needs a team exercises the real surface — the profile
// bootstrap from BRIGADE_SUPABASE_*, the sign-up, the RPC, the join
// secret's parse and the binding — instead of only the RPC beneath it.
func liveTeam(t *testing.T, name string) (*rig, string) {
	t.Helper()
	r := liveRig(t)
	created := createLive(t, r, name, "alice@example.com")
	secret := str(t, created, "join_secret")
	if _, err := protocol.ParseJoinSecret(secret); err != nil {
		t.Fatalf("team create answered a join secret that does not parse: %v", err)
	}
	return r, secret
}

// liveJoin mints a second anonymous principal in the same team through
// the `team join` verb. Its label is fixed: every test here needs exactly
// one second member, and a distinct label would prove nothing the roster
// cases do not already prove.
func liveJoin(t *testing.T, secret string) *rig {
	t.Helper()
	const label = "bob@example.com"
	r := liveRig(t)
	got := joinLive(t, r, secret, label)
	if got.code != 0 {
		t.Fatalf("team join: exit %d, stdout %s stderr %s", got.code, got.stdout, got.stderr)
	}
	joined, _ := decode(t, got.stdout)["result"].(map[string]any)
	if rejoined, _ := joined["rejoined"].(bool); rejoined {
		t.Fatalf("team join: rejoined true for a fresh principal (%v)", joined)
	}
	if !validUUID(str(t, joined, "team_ref")) {
		t.Fatalf("team join: team_ref %v", joined["team_ref"])
	}
	return r
}

// liveName is a session or team name unique to this run.
func liveName(t *testing.T, purpose string) string {
	t.Helper()
	return purpose + "-" + time.Now().UTC().Format("150405.000000")
}

// registerLive registers one session and returns the whole result and the
// session id.
func registerLive(t *testing.T, r *rig, doc string) (map[string]any, string) {
	t.Helper()
	got := r.exec(doc, "session", "register")
	if got.code != 0 {
		t.Fatalf("session register: exit %d, stdout %s stderr %s", got.code, got.stdout, got.stderr)
	}
	env := decode(t, got.stdout)
	result, _ := env["result"].(map[string]any)
	id, _ := result["session_id"].(string)
	if !validUUID(id) {
		t.Fatalf("session register: session_id %q is not a uuid", id)
	}
	return result, id
}

// TestIntegrationSessionLifecycle: register, list, heartbeat and close
// against the real RPCs (I-04, I-05, U-22, C-10..C-15, C-42, C-44). The
// record validates as a SessionRecord, human_label comes from the
// membership (C-12), the computed state follows activity and closure, and
// `inbound`, `model` and `context_used_tokens` round-trip through
// registration and heartbeat — the last two exactly (the [1m] suffix kept,
// the count as a number), and a heartbeat that omits them leaves them
// standing (4.4.4).
func TestIntegrationSessionLifecycle(t *testing.T) {
	r, _ := liveTeam(t, liveName(t, "p2-8-lifecycle"))
	name := liveName(t, "s")
	doc := `{"harness":"brigade-test","harness_version":"0.0.0","session_name":"` + name +
		`","activity":"busy","inbound":"hold","session_description":"d","workspace_label":"w",` +
		`"model":"claude-opus-5[1m]","context_used_tokens":189681}`
	result, id := registerLive(t, r, doc)

	if resumed, _ := result["resumed"].(bool); resumed {
		t.Errorf("session register: resumed true on a new session (4.4.2)")
	}
	if result["model"] != "claude-opus-5[1m]" || result["context_used_tokens"] != float64(189681) {
		t.Errorf("session register: model %v, context_used_tokens %v, want claude-opus-5[1m] and 189681 (C-44)",
			result["model"], result["context_used_tokens"])
	}
	if seconds, _ := result["lease_seconds"].(float64); int(seconds) != protocol.LeaseDefaultSeconds {
		t.Errorf("lease_seconds = %v, want %d", result["lease_seconds"], protocol.LeaseDefaultSeconds)
	}
	if state, _ := result["state"].(string); state != protocol.SessionStateActive {
		t.Errorf("state = %v, want active for activity busy (4.5.8)", result["state"])
	}
	serverTime := mustTime(t, result, "server_time")
	leaseUntil := mustTime(t, result, "lease_until")
	if d := leaseUntil.Sub(serverTime); d < 85*time.Second || d > 95*time.Second {
		t.Errorf("lease_until - server_time = %s, want about 90 s", d)
	}

	// list: the session is there, with the membership's human_label, and
	// is_self is the adapter's own (C-12).
	list := r.ok("session", "list", "--session", id)
	sessions, _ := list["sessions"].([]any)
	found := false
	for _, s := range sessions {
		rec, _ := s.(map[string]any)
		if rec["session_id"] != id {
			continue
		}
		found = true
		if rec["human_label"] != "alice@example.com" {
			t.Errorf("session list: human_label %v, want the membership's (C-12)", rec["human_label"])
		}
		if isSelf, _ := rec["is_self"].(bool); !isSelf {
			t.Errorf("session list --session: is_self false on the named session (4.4.3)")
		}
		if rec["inbound"] != protocol.InboundHold {
			t.Errorf("session list: inbound %v, want hold (C-42)", rec["inbound"])
		}
		if rec["session_description"] != "d" || rec["workspace_label"] != "w" {
			t.Errorf("session list: description %v, workspace_label %v", rec["session_description"], rec["workspace_label"])
		}
		if rec["model"] != "claude-opus-5[1m]" || rec["context_used_tokens"] != float64(189681) {
			t.Errorf("session list: model %v, context_used_tokens %v, want the registration's (C-44)", rec["model"], rec["context_used_tokens"])
		}
	}
	if !found {
		t.Fatalf("session list: the registered session is absent (%v)", list)
	}
	if list["team_name"] == "" {
		t.Errorf("session list: team_name is empty")
	}

	// heartbeat: renews, renames, changes inbound and reports a model switch
	// with a new occupancy (C-13, C-42, C-44).
	newName := liveName(t, "renamed")
	got := r.exec(`{"session_name":"`+newName+`","inbound":"refuse","activity":"idle","model":"claude-sonnet-5","context_used_tokens":2048}`,
		"session", "heartbeat", "--session", id)
	if got.code != 0 {
		t.Fatalf("session heartbeat: exit %d, %s", got.code, got.stdout)
	}
	hb, _ := decode(t, got.stdout)["result"].(map[string]any)
	if hb["state"] != protocol.SessionStateIdle {
		t.Errorf("heartbeat: state %v, want idle for activity idle (4.5.8)", hb["state"])
	}
	if !mustTime(t, hb, "lease_until").After(leaseUntil) {
		t.Errorf("heartbeat: lease_until did not move forward (4.4.4)")
	}
	list = r.ok("session", "list")
	for _, s := range list["sessions"].([]any) {
		rec, _ := s.(map[string]any)
		if rec["session_id"] == id && (rec["session_name"] != newName || rec["inbound"] != protocol.InboundRefuse) {
			t.Errorf("session list after heartbeat: name %v, inbound %v", rec["session_name"], rec["inbound"])
		}
		if rec["session_id"] == id && (rec["model"] != "claude-sonnet-5" || rec["context_used_tokens"] != float64(2048)) {
			t.Errorf("session list after heartbeat: model %v, context_used_tokens %v, want claude-sonnet-5 and 2048 (C-44)", rec["model"], rec["context_used_tokens"])
		}
	}
	// A heartbeat that omits both leaves them standing: absent means
	// unchanged, and the harness never clears them (4.4.4, C-44).
	if got := r.exec(`{"activity":"idle"}`, "session", "heartbeat", "--session", id); got.code != 0 {
		t.Fatalf("session heartbeat (bare): exit %d, %s", got.code, got.stdout)
	}
	for _, s := range r.ok("session", "list")["sessions"].([]any) {
		rec, _ := s.(map[string]any)
		if rec["session_id"] == id && (rec["model"] != "claude-sonnet-5" || rec["context_used_tokens"] != float64(2048)) {
			t.Errorf("session list after a bare heartbeat: model %v, context_used_tokens %v changed (4.4.4)", rec["model"], rec["context_used_tokens"])
		}
	}

	// close: idempotent to the byte, then offline, then heartbeat is a
	// conflict (C-15).
	first := r.exec("", "session", "close", "--session", id)
	if first.code != 0 {
		t.Fatalf("session close: exit %d, %s", first.code, first.stdout)
	}
	second := r.exec("", "session", "close", "--session", id)
	if first.stdout != second.stdout {
		t.Errorf("session close is not idempotent:\n%s\n%s", first.stdout, second.stdout)
	}
	e := r.fails("conflict", 7, `{}`, "session", "heartbeat", "--session", id)
	if details(t, e.stdout)["reason"] != "session_closed" {
		t.Errorf("heartbeat after close: details %v, want reason session_closed", details(t, e.stdout))
	}
	// The closed session is offline and only listed with --include-offline.
	if listHasSession(r.ok("session", "list"), id) {
		t.Errorf("session list: the closed session is listed without --include-offline (4.5.8)")
	}
	offline := r.ok("session", "list", "--include-offline")
	if !listHasSession(offline, id) {
		t.Errorf("session list --include-offline: the closed session is absent")
	}
	for _, s := range offline["sessions"].([]any) {
		rec, _ := s.(map[string]any)
		if rec["session_id"] == id && rec["state"] != protocol.SessionStateOffline {
			t.Errorf("closed session state %v, want offline", rec["state"])
		}
	}
}

// TestIntegrationSessionResume: resume of a live owned session is
// conflict session_live and changes nothing; after close the same resume
// succeeds with resumed true and the same id; a foreign or unknown id is
// the uniform not_found (C-19, C-19b).
func TestIntegrationSessionResume(t *testing.T) {
	r, secret := liveTeam(t, liveName(t, "p2-8-resume"))
	other := liveJoin(t, secret)
	name := liveName(t, "s")
	base := `{"harness":"h","harness_version":"1","session_name":"` + name + `","activity":"busy","inbound":"accept"`
	result, id := registerLive(t, r, base+`}`)
	leaseUntil := mustTime(t, result, "lease_until")

	resumeDoc := func(who, target string) string {
		return `{"harness":"h","harness_version":"1","session_name":"` + who +
			`","activity":"busy","inbound":"accept","resume":{"session_id":"` + target + `"}}`
	}
	live := r.fails("conflict", 7, resumeDoc(name+"-renamed", id), "session", "register")
	if details(t, live.stdout)["reason"] != reasonSessionLive {
		t.Errorf("resume of a live session: details %v, want reason session_live", details(t, live.stdout))
	}
	// Nothing changed (C-19b).
	for _, s := range r.ok("session", "list")["sessions"].([]any) {
		rec, _ := s.(map[string]any)
		if rec["session_id"] != id {
			continue
		}
		if rec["session_name"] != name {
			t.Errorf("the refused resume renamed the session")
		}
		if !mustTime(t, rec, "lease_until").Equal(leaseUntil) {
			t.Errorf("the refused resume moved lease_until")
		}
	}

	// Another member's resume of it, and a resume of an unknown uuid, are
	// the uniform not_found — byte-identical (4.5.7).
	byOther := other.fails("not_found", 6, resumeDoc(liveName(t, "steal"), id), "session", "register")
	unknown := r.fails("not_found", 6, resumeDoc(liveName(t, "unknown"), "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"), "session", "register")
	if errorJSON(t, byOther.stdout) != errorJSON(t, unknown.stdout) {
		t.Fatalf("a foreign resume and an unknown one differ:\n%s\n%s", byOther.stdout, unknown.stdout)
	}
	if errorJSON(t, unknown.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("resume not_found differs from the uniform one: %s", unknown.stdout)
	}

	// After close the same resume succeeds in place.
	r.ok("session", "close", "--session", id)
	resumed, sameID := registerLive(t, r, resumeDoc(name, id))
	if sameID != id {
		t.Errorf("resume: session_id %s, want %s", sameID, id)
	}
	if ok, _ := resumed["resumed"].(bool); !ok {
		t.Errorf("resume: resumed %v, want true", resumed["resumed"])
	}
	if state, _ := resumed["state"].(string); state == protocol.SessionStateOffline {
		t.Errorf("resume: state offline; the session must be re-opened")
	}
}

// TestIntegrationSessionOwnershipAndMembership: a heartbeat, close or
// list on another member's session, on an unknown uuid and on a foreign
// team all answer the uniform error of 4.5.6/4.5.7 — byte-identical, so
// there is no existence oracle (C-13, C-26).
func TestIntegrationSessionOwnership(t *testing.T) {
	r, secret := liveTeam(t, liveName(t, "p2-8-own"))
	other := liveJoin(t, secret)
	_, mine := registerLive(t, r,
		`{"harness":"h","harness_version":"1","session_name":"`+liveName(t, "s")+`","activity":"busy","inbound":"accept"}`)
	random := "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"

	onMine := other.fails("not_found", 6, `{}`, "session", "heartbeat", "--session", mine)
	onRandom := other.fails("not_found", 6, `{}`, "session", "heartbeat", "--session", random)
	if errorJSON(t, onMine.stdout) != errorJSON(t, onRandom.stdout) {
		t.Fatalf("heartbeat on a foreign session differs from an unknown id:\n%s\n%s", onMine.stdout, onRandom.stdout)
	}
	if errorJSON(t, onMine.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("heartbeat not_found differs from the uniform one: %s", onMine.stdout)
	}
	closeMine := other.fails("not_found", 6, "", "session", "close", "--session", mine)
	if errorJSON(t, closeMine.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("close not_found differs from the uniform one: %s", closeMine.stdout)
	}

	// Rebound to a team it does not belong to, and to a random uuid: the
	// uniform unauthorized, byte-identical (C-26).
	_, foreignSecret := liveTeam(t, liveName(t, "p2-8-own-t2"))
	foreignRef := teamRefOf(t, foreignSecret)
	other.bindTeam(foreignRef, "t2")
	listForeign := other.fails("unauthorized", 5, "", "session", "list")
	other.bindTeam(random, "t2")
	listRandom := other.fails("unauthorized", 5, "", "session", "list")
	if errorJSON(t, listForeign.stdout) != errorJSON(t, listRandom.stdout) {
		t.Fatalf("a foreign team differs from a random one:\n%s\n%s", listForeign.stdout, listRandom.stdout)
	}
	if errorJSON(t, listForeign.stdout) != errorJSON(t, newFailure(t, errNotMember())) {
		t.Fatalf("unauthorized differs from the uniform one: %s", listForeign.stdout)
	}
}

// TestIntegrationSessionBackendCaps: the backend caps `harness` and
// `harness_version` at 32 characters, which `limits` does not publish
// (5.11). The member name comes back as details.field so a caller can act
// on it, and the raise text never reaches stdout.
func TestIntegrationSessionBackendCaps(t *testing.T) {
	r, _ := liveTeam(t, liveName(t, "p2-8-caps"))
	long := strings.Repeat("h", 33)
	for member, doc := range map[string]string{
		"harness": `{"harness":"` + long + `","harness_version":"1","session_name":"s","activity":"busy","inbound":"accept"}`,
		"harness_version": `{"harness":"h","harness_version":"` + long +
			`","session_name":"s","activity":"busy","inbound":"accept"}`,
	} {
		got := r.fails("invalid_input", 3, doc, "session", "register")
		d := details(t, got.stdout)
		if d["field"] != member {
			t.Errorf("%s over 32 characters: details %v, want field %s", member, d, member)
		}
		if strings.Contains(got.stdout, "brigade:") {
			t.Errorf("the server's raise text reached stdout: %s", got.stdout)
		}
	}
}

// teamRefOf reads the team reference out of a join secret (4.4.10).
func teamRefOf(t *testing.T, secret string) string {
	t.Helper()
	parsed, err := protocol.ParseJoinSecret(secret)
	if err != nil {
		t.Fatalf("the backend issued a malformed join secret: %v", err)
	}
	return parsed.TeamRef()
}

// mustTime reads an RFC 3339 member out of a result object.
func mustTime(t *testing.T, object map[string]any, key string) time.Time {
	t.Helper()
	s, ok := object[key].(string)
	if !ok {
		t.Fatalf("member %q is absent or not a string in %v", key, object)
	}
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("member %q is not an RFC 3339 timestamp: %v", key, err)
	}
	return ts
}

// listHasSession reports whether a `session list` result carries id.
func listHasSession(result map[string]any, id string) bool {
	sessions, _ := result["sessions"].([]any)
	for _, s := range sessions {
		rec, _ := s.(map[string]any)
		if rec["session_id"] == id {
			return true
		}
	}
	return false
}
