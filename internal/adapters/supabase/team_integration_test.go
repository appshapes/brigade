package supabase

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// The team paths only the real stack shows (brief section 7; I-17..I-21):
// the backend-generated secret, the uniform invalid_secret body, the
// per-principal join limiter, the rejoin upsert and list_members' live
// roster. Every rig here is a fresh anonymous principal, so the limiter
// state one test burns never touches another; nothing resets the
// database.

// createLive runs `team create` on a live rig and returns the result.
func createLive(t *testing.T, r *rig, name, label string) map[string]any {
	t.Helper()
	got := r.exec(`{"team_name":"`+name+`","human_label":"`+label+`"}`, "team", "create")
	if got.code != 0 {
		t.Fatalf("team create: exit %d %s %s", got.code, got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	return result
}

// joinLive runs `team join` on a live rig with secret.
func joinLive(t *testing.T, r *rig, secret, label string) outcome {
	t.Helper()
	return r.exec(`{"join_secret":"`+secret+`","human_label":"`+label+`"}`, "team", "join")
}

// members runs `team members` on a live rig and returns the roster keyed
// by principal_ref.
func members(t *testing.T, r *rig) (map[string]any, map[string]map[string]any) {
	t.Helper()
	result := r.ok("team", "members")
	items, _ := result["members"].([]any)
	byRef := map[string]map[string]any{}
	for _, item := range items {
		m, _ := item.(map[string]any)
		byRef[str(t, m, "principal_ref")] = m
	}
	return result, byRef
}

// TestIntegrationTeamLifecycle: A creates a team (the secret is the
// backend's brg1.<uuid>.<32 hex>, parseable, naming the team), B joins
// (rejoined false, a distinct principal), the roster from both sides
// lists both with null last_seen_at and session_count 0 (no sessions
// yet); B leaves (revoked at the backend: A's roster drops B, B's own
// `team members` is `config` because the profile is unbound), a second
// leave is idempotent without a call, and B's rejoin re-activates the
// same membership with the same principal_ref (rejoined true).
func TestIntegrationTeamLifecycle(t *testing.T) {
	a := liveRig(t)
	created := createLive(t, a, "lifecycle-"+t.Name(), "alice@example.com")
	secret, err := protocol.ParseJoinSecret(str(t, created, "join_secret"))
	if err != nil || secret.TeamRef() != str(t, created, "team_ref") || !validUUID(secret.TeamRef()) {
		t.Fatalf("join_secret %v", err)
	}
	if !strings.HasPrefix(secret.Secret(), protocol.JoinSecretPrefix+secret.TeamRef()+".") || len(secret.Secret()) != len(protocol.JoinSecretPrefix)+36+1+32 {
		t.Fatalf("the secret is not brg1.<uuid>.<32 hex>")
	}
	teamRef, aRef := str(t, created, "team_ref"), str(t, created, "principal_ref")

	b := liveRig(t)
	got := joinLive(t, b, secret.Secret(), "bob@example.com")
	if got.code != 0 {
		t.Fatalf("join: exit %d %s %s", got.code, got.stdout, got.stderr)
	}
	joined, _ := decode(t, got.stdout)["result"].(map[string]any)
	bRef := str(t, joined, "principal_ref")
	if joined["rejoined"] != false || joined["team_ref"] != teamRef || bRef == aRef || !validUUID(bRef) {
		t.Fatalf("join result = %v", joined)
	}
	noSecretLeak(t, b, got, false)

	for _, r := range []*rig{a, b} {
		result, byRef := members(t, r)
		if result["team_ref"] != teamRef || len(byRef) != 2 {
			t.Fatalf("roster = %v", result)
		}
		for _, ref := range []string{aRef, bRef} {
			m := byRef[ref]
			if m == nil || m["status"] != "active" || m["session_count"] != float64(0) {
				t.Fatalf("member %s = %v", ref, m)
			}
			if v, present := m["last_seen_at"]; !present || v != nil {
				t.Fatalf("member %s last_seen_at = %v, want null without sessions", ref, v)
			}
		}
		if byRef[bRef]["human_label"] != "bob@example.com" || byRef[aRef]["human_label"] != "alice@example.com" {
			t.Fatalf("labels: %v", byRef)
		}
	}

	left := b.ok("team", "leave")
	if left["team_ref"] != teamRef || left["principal_ref"] != bRef || left["left"] != true {
		t.Fatalf("leave = %v", left)
	}
	if _, byRef := members(t, a); len(byRef) != 1 || byRef[aRef] == nil {
		t.Fatalf("A's roster after B left = %v", byRef)
	}
	b.fails("config", 11, "", "team", "members")
	again := b.ok("team", "leave")
	if again["team_ref"] != teamRef || again["left"] != true {
		t.Fatalf("second leave = %v", again)
	}

	got = joinLive(t, b, secret.Secret(), "bob@example.com")
	if got.code != 0 {
		t.Fatalf("rejoin: exit %d %s", got.code, got.stdout)
	}
	rejoined, _ := decode(t, got.stdout)["result"].(map[string]any)
	if rejoined["rejoined"] != true || rejoined["principal_ref"] != bRef {
		t.Fatalf("rejoin result = %v", rejoined)
	}
	if _, byRef := members(t, a); len(byRef) != 2 {
		t.Fatalf("A's roster after the rejoin = %v", byRef)
	}
}

// TestIntegrationJoinRefusalsAndLimiter (I-17, I-19): a wrong secret for
// a real team and a secret for an unknown team are `unauthorized` with
// byte-identical stdout; the fifth failure is still `unauthorized` and
// the sixth attempt — with the CORRECT secret — is `rate_limited` with a
// retry_after_ms of at least 60 s (D6), so the limiter never becomes an
// oracle; the profile stays unbound throughout.
func TestIntegrationJoinRefusalsAndLimiter(t *testing.T) {
	owner := liveRig(t)
	created := createLive(t, owner, "limiter-"+t.Name(), "owner@example.com")
	correct := str(t, created, "join_secret")
	teamRef := str(t, created, "team_ref")
	wrong := protocol.JoinSecretPrefix + teamRef + "." + strings.Repeat("0", 32)
	unknown := protocol.JoinSecretPrefix + otherTeamID + "." + strings.Repeat("0", 32)

	j := liveRig(t)
	rWrong := joinLive(t, j, wrong, "jo@example.com")
	rUnknown := joinLive(t, j, unknown, "jo@example.com")
	want := newFailure(t, errSecretRejected())
	if rWrong.code != 5 || rWrong.stdout != want || rUnknown.stdout != want {
		t.Fatalf("wrong %d %q unknown %q, want %q", rWrong.code, rWrong.stdout, rUnknown.stdout, want)
	}
	// A well-formed secret whose team_ref is not a uuid: the backend's
	// invalid_input result, the same fixed unauthorized here.
	if got := joinLive(t, j, protocol.JoinSecretPrefix+"team-0123456789abcdef.deadbeef", "jo@example.com"); got.stdout != want {
		t.Fatalf("non-uuid team_ref: %q", got.stdout)
	}
	// The malformed-shape refusal above records no attempt (the backend
	// answers before its limiter), so three more wrong secrets make five.
	for i := 0; i < 3; i++ {
		if got := joinLive(t, j, wrong, "jo@example.com"); got.code != 5 {
			t.Fatalf("attempt %d: exit %d %s", 3+i, got.code, got.stdout)
		}
	}
	got := joinLive(t, j, correct, "jo@example.com")
	if got.code != 8 {
		t.Fatalf("sixth attempt with the correct secret: exit %d %s (want rate_limited)", got.code, got.stdout)
	}
	errObj, _ := decode(t, got.stdout)["error"].(map[string]any)
	if ms, _ := errObj["retry_after_ms"].(float64); ms < 60000 || details(t, got.stdout)["reason"] != reasonJoinAttempts {
		t.Fatalf("rate_limited error = %v", errObj)
	}
	noSecretLeak(t, j, got, false)
	if state := str(t, j.ok("profile", "status"), "state"); state != protocol.ProfileStateNotMember {
		t.Fatalf("state = %q, want not_member", state)
	}
	if _, byRef := members(t, owner); len(byRef) != 1 {
		t.Fatalf("the refused principal appears in the roster: %v", byRef)
	}
}

// TestIntegrationMembersAsNonMember (I-21, C-43): C, member of its own
// team, rebound to A's team gets `unauthorized` from list_members with
// stdout byte-identical to a random uuid team_ref — the RPC raises one
// text for both — and to the adapter's local answer for a non-uuid ref.
func TestIntegrationMembersAsNonMember(t *testing.T) {
	a := liveRig(t)
	aTeam := str(t, createLive(t, a, "roster-a-"+t.Name(), "alice@example.com"), "team_ref")
	c := liveRig(t)
	createLive(t, c, "roster-c-"+t.Name(), "carol@example.com")
	want := newFailure(t, errNotMember())
	for _, ref := range []string{aTeam, otherTeamID, "team-0123456789abcdef"} {
		c.bindTeam(ref, "x")
		got := c.fails("unauthorized", 5, "", "team", "members")
		if got.stdout != want {
			t.Fatalf("team_ref %s: %q, want %q", ref, got.stdout, want)
		}
	}
	if _, byRef := members(t, a); len(byRef) != 1 {
		t.Fatalf("A's roster = %v", byRef)
	}
}
