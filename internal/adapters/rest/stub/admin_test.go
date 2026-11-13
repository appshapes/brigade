//go:build !windows

package stub

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The owner and the administration endpoints: the contract's section 3a,
// asked as a server is asked, with no adapter in between.

// neverIssued is a well-formed join secret for team that no server made.
func neverIssued(team string) string {
	return protocol.JoinSecretPrefix + team + "." + strings.Repeat("0", 32)
}

// rejoin joins the team of secret as m, a principal the server knows.
func (h *harness) rejoin(m member, secret string) answer {
	h.t.Helper()
	return h.post(m.credential, "/v1/team/join", `{"join_secret":"`+secret+`"}`)
}

// rotate rotates m's team and returns the new secret and its version.
func (h *harness) rotate(m member) (string, float64) {
	h.t.Helper()
	r := h.must(m.credential, "/v1/team/rotate-secret", `{"team_ref":"`+m.team+`"}`)
	version, _ := r["secret_version"].(float64)
	return text2(h.t, r, "join_secret"), version
}

// joinedVersions reads joined_secret_version by principal_ref out of the
// roster m sees.
func (h *harness) joinedVersions(m member) map[string]float64 {
	h.t.Helper()
	out := map[string]float64{}
	members, _ := h.must(m.credential, "/v1/team/members", `{"team_ref":"`+m.team+`"}`)["members"].([]any)
	for _, row := range members {
		object, _ := row.(map[string]any)
		version, present := object["joined_secret_version"].(float64)
		if !present {
			h.t.Errorf("a member of the roster carries no joined_secret_version: %v", object)
		}
		out[text2(h.t, object, "principal_ref")] = version
	}
	return out
}

// TestInfoListsTeamAdmin: a server that carries the administration
// endpoints says so in /v1/info, and one that leaves them out lists
// nothing, answers the three paths as it answers any unknown path, and
// adds nothing to the roster.
func TestInfoListsTeamAdmin(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	capabilities, _ := h.must("", "/v1/info", `{}`)["capabilities"].([]any)
	if len(capabilities) != 1 || capabilities[0] != "team.admin" {
		t.Errorf("/v1/info: capabilities %v, want [team.admin]", capabilities)
	}

	without := newHarness(t, Options{WithoutTeamAdmin: true})
	if _, present := without.must("", "/v1/info", `{}`)["capabilities"]; present {
		t.Errorf("/v1/info of a server without the endpoints has a capabilities member")
	}
	alice := without.create("ops")
	unknown := without.post(alice.credential, "/v1/nosuch", `{}`)
	for path, ep := range endpoints {
		if !ep.admin {
			continue
		}
		got := without.post(alice.credential, path, `{"team_ref":"`+alice.team+`"}`)
		without.refused(got, "not_found", http.StatusNotFound)
		if got.raw != unknown.raw {
			t.Errorf("%s on a server without it and an unknown path differ:\n%s%s", path, got.raw, unknown.raw)
		}
	}
	members, _ := without.must(alice.credential, "/v1/team/members", `{"team_ref":"`+alice.team+`"}`)["members"].([]any)
	if object, _ := members[0].(map[string]any); object["joined_secret_version"] != nil {
		t.Errorf("the roster of a server without the endpoints carries joined_secret_version: %v", object)
	}
}

// TestOnlyTheOwnerIsAnswered: every administration request of a principal
// that is not the team's owner, or is its owner and not an active member,
// is the uniform `unauthorized`, the same bytes for a member, a
// non-member, a team that does not exist and an owner that left; a request
// that names no team is invalid; and the gate comes before the request is
// read, so a refused caller learns nothing about what it sent.
func TestOnlyTheOwnerIsAnswered(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	carol := h.create("other")
	bodies := map[string]string{
		"/v1/team/rotate-secret": ``,
		"/v1/team/revoke-member": `,"principal_ref":"` + bob.principal + `"`,
		"/v1/team/transfer":      `,"principal_ref":"` + bob.principal + `"`,
	}
	reference := h.post(carol.credential, "/v1/team/members", `{"team_ref":"`+alice.team+`"}`)
	h.refused(reference, "unauthorized", http.StatusForbidden)
	for path, rest := range bodies {
		for who, m := range map[string]member{"a member that is not the owner": bob, "a principal of another team": carol} {
			got := h.post(m.credential, path, `{"team_ref":"`+alice.team+`"`+rest+`}`)
			if got.raw != reference.raw {
				t.Errorf("%s by %s: %s, want the uniform unauthorized", path, who, got.raw)
			}
		}
		random := h.post(alice.credential, path, `{"team_ref":"0123456789abcdef0123456789abcdef"`+rest+`}`)
		if random.raw != reference.raw {
			t.Errorf("%s for a team that does not exist: %s, want the uniform unauthorized", path, random.raw)
		}
		h.refused(h.post(alice.credential, path, `{`+strings.TrimPrefix(rest, ",")+`}`), "invalid_input", http.StatusBadRequest)
	}
	// A request that would be invalid from the owner is still only refused.
	malformed := h.post(bob.credential, "/v1/team/revoke-member", `{"team_ref":"`+alice.team+`","principal_ref":"x","joined_secret_version_lte":1}`)
	if malformed.raw != reference.raw {
		t.Errorf("a malformed revocation by a member that is not the owner: %s, want the uniform unauthorized", malformed.raw)
	}

	// The owner leaves: it is the owner still, and not an active member.
	h.must(alice.credential, "/v1/team/leave", `{"team_ref":"`+alice.team+`"}`)
	for path, rest := range bodies {
		if got := h.post(alice.credential, path, `{"team_ref":"`+alice.team+`"`+rest+`}`); got.raw != reference.raw {
			t.Errorf("%s by an owner that left: %s, want the uniform unauthorized", path, got.raw)
		}
	}
	// It comes back with the secret and administers again.
	if !h.rejoin(alice, alice.secret).ok {
		t.Fatalf("the owner could not come back")
	}
	if _, version := h.rotate(alice); version != 2 {
		t.Errorf("the rotation of an owner that came back: version %v, want 2", version)
	}
}

// TestRotateSecret: a new secret of BAP/1's format with the same team_ref,
// the next version, the server's time; the previous secret is the rejected
// join, with the bytes of a wrong secret and of an unknown team; no
// membership changes; and a join records the version it used.
func TestRotateSecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	before := time.Now().Add(-time.Second)
	r := h.must(alice.credential, "/v1/team/rotate-secret", `{"team_ref":"`+alice.team+`"}`)
	if len(r) != 5 || r["team_ref"] != alice.team || r["team_name"] != "ops" || r["secret_version"] != float64(2) {
		t.Errorf("rotate-secret: %v, want exactly {team_ref, team_name, secret_version 2, rotated_at, join_secret}", r)
	}
	rotatedAt, err := time.Parse(time.RFC3339Nano, text2(t, r, "rotated_at"))
	if err != nil || rotatedAt.Before(before) || rotatedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("rotated_at %v (%v) is not the server's time", r["rotated_at"], err)
	}
	rotated := text2(t, r, "join_secret")
	parsed, err := protocol.ParseJoinSecret(rotated)
	if err != nil || parsed.TeamRef() != alice.team || rotated == alice.secret {
		t.Errorf("the rotated secret does not parse, names another team or is the previous one (%v)", err)
	}
	if tail := strings.TrimPrefix(rotated, protocol.JoinSecretPrefix+alice.team+"."); len(tail) < 32 {
		t.Errorf("the rotated secret's own part has %d characters; 128 random bits need 32 hex digits", len(tail))
	}

	// One rejected join, whatever was wrong with the secret.
	previous := h.post("", "/v1/team/join", `{"join_secret":"`+alice.secret+`"}`)
	h.refused(previous, "unauthorized", http.StatusForbidden)
	for what, secret := range map[string]string{
		"a secret never issued":          neverIssued(alice.team),
		"a team that does not exist":     neverIssued("0123456789abcdef0123456789abcdef"),
		"a secret that was never a hash": protocol.JoinSecretPrefix + alice.team + ".x",
	} {
		if got := h.post("", "/v1/team/join", `{"join_secret":"`+secret+`"}`); got.raw != previous.raw {
			t.Errorf("a join with the previous secret and with %s differ:\n%s%s", what, previous.raw, got.raw)
		}
	}
	if n := principalDirs(t, h.dir); n != 2 {
		t.Errorf("%d principal directories after the rejected joins, want the two members'", n)
	}

	// Nobody was revoked, and each member joined with what it joined with.
	carol := h.join(rotated)
	joined := h.joinedVersions(bob)
	if len(joined) != 3 || joined[alice.principal] != 1 || joined[bob.principal] != 1 || joined[carol.principal] != 2 {
		t.Errorf("the roster after a rotation and a join: versions %v, want alice 1, bob 1, carol 2", joined)
	}
	// A member that leaves and comes back through the new secret is at 2.
	h.must(bob.credential, "/v1/team/leave", `{"team_ref":"`+bob.team+`"}`)
	h.refused(h.rejoin(bob, alice.secret), "unauthorized", http.StatusForbidden)
	if back := h.rejoin(bob, rotated); !back.ok || back.result["rejoined"] != true {
		t.Errorf("a rejoin with the rotated secret: %s", back.raw)
	}
	if joined := h.joinedVersions(alice); joined[bob.principal] != 2 {
		t.Errorf("after bob's rejoin: versions %v, want bob at 2", joined)
	}
	// A second rotation: the first rotation's secret is dead too.
	second, version := h.rotate(alice)
	if version != 3 || second == rotated {
		t.Errorf("a second rotation: version %v, want 3 and another secret", version)
	}
	if got := h.post("", "/v1/team/join", `{"join_secret":"`+rotated+`"}`); got.raw != previous.raw {
		t.Errorf("a join with a secret two rotations old: %s", got.raw)
	}
}

// TestRevokeMember is the principal form of /v1/team/revoke-member: the
// membership ends, the sessions close, a waiting receive is answered
// `unauthorized` within a second, the member comes back with the secret
// unless it was banned, and a ban is lifted by a revocation without one.
func TestRevokeMember(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	b1, b2 := h.register(bob, "b1"), h.register(bob, "b2")
	h.must(bob.credential, "/v1/session/close", `{"session_id":"`+b2+`"}`)
	h.register(bob, "b3")
	revoke := func(extra string) answer {
		return h.post(alice.credential, "/v1/team/revoke-member", `{"team_ref":"`+alice.team+`","principal_ref":"`+bob.principal+`"`+extra+`}`)
	}
	expect := func(what string, a answer, status string, changed bool, closed float64) {
		t.Helper()
		if !a.ok {
			t.Fatalf("%s: %s", what, a.raw)
		}
		r := a.result
		if len(r) != 5 || r["team_ref"] != alice.team || r["principal_ref"] != bob.principal ||
			r["status"] != status || r["changed"] != changed || r["sessions_closed"] != closed {
			t.Errorf("%s: %v, want {team_ref, principal_ref, status %s, changed %v, sessions_closed %v}", what, r, status, changed, closed)
		}
	}

	waiting := make(chan answer, 1)
	go func() {
		waiting <- h.post(bob.credential, "/v1/message/receive", `{"session_id":"`+b1+`","wait_seconds":30}`)
	}()
	time.Sleep(100 * time.Millisecond)
	expect("a revocation", revoke(``), "revoked", true, 2)
	// The membership had ended by the time the revocation was answered, so
	// the second the contract gives the waiting receive is counted from here.
	ended := time.Now()
	select {
	case got := <-waiting:
		h.refused(got, "unauthorized", http.StatusForbidden)
		if took := time.Since(ended); took > time.Second {
			t.Errorf("the waiting receive was answered %s after the revocation; the contract gives it one second", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting receive of a revoked member was not answered")
	}

	// Refused as a member that left is refused, on every endpoint.
	reference := h.post(alice.credential, "/v1/team/members", `{"team_ref":"0123456789abcdef0123456789abcdef"}`)
	for path, body := range map[string]string{
		"/v1/team/members":      `{"team_ref":"` + bob.team + `"}`,
		"/v1/session/list":      `{"team_ref":"` + bob.team + `"}`,
		"/v1/session/heartbeat": `{"session_id":"` + b1 + `","heartbeat":{}}`,
		"/v1/message/receive":   `{"session_id":"` + b1 + `"}`,
		"/v1/message/send":      `{"sender_session_id":"` + b1 + `","recipient_session_id":"` + b1 + `","body":"x"}`,
	} {
		if got := h.post(bob.credential, path, body); got.raw != reference.raw {
			t.Errorf("%s by a revoked member: %s, want the uniform unauthorized", path, got.raw)
		}
	}
	if sessions, _ := h.must(alice.credential, "/v1/session/list", `{"team_ref":"`+alice.team+`","include_offline":true}`)["sessions"].([]any); len(sessions) != 0 {
		t.Errorf("the team still lists %d sessions of the revoked member", len(sessions))
	}
	expect("a second revocation", revoke(`,"ban":false`), "revoked", false, 0)

	// It comes back: the same membership.
	back := h.rejoin(bob, alice.secret)
	if !back.ok || back.result["rejoined"] != true || back.result["principal_ref"] != bob.principal {
		t.Errorf("a revoked member's join: %s", back.raw)
	}
	if _, has := back.result["credential"]; has {
		t.Errorf("a known principal's rejoin was issued a credential it did not need")
	}

	// A ban is the rejected join, and only a revocation without ban lifts it.
	stranger := h.post("", "/v1/team/join", `{"join_secret":"`+neverIssued(alice.team)+`"}`)
	expect("a ban", revoke(`,"ban":true`), "banned", true, 0)
	if got := h.rejoin(bob, alice.secret); got.raw != stranger.raw {
		t.Errorf("a banned member's join with the secret: %s, want the rejected join", got.raw)
	}
	expect("a second ban", revoke(`,"ban":true`), "banned", false, 0)
	rotated, _ := h.rotate(alice)
	if got := h.rejoin(bob, rotated); got.raw != stranger.raw {
		t.Errorf("a banned member's join with a rotated secret: %s, want the rejected join", got.raw)
	}
	h.must(alice.credential, "/v1/team/revoke-member", `{"team_ref":"`+alice.team+`","joined_secret_version_lte":2147483647}`)
	if got := h.rejoin(bob, rotated); got.raw != stranger.raw {
		t.Errorf("a version sweep lifted a ban: %s", got.raw)
	}
	expect("a revocation of a banned member", revoke(``), "revoked", true, 0)
	if got := h.rejoin(bob, rotated); !got.ok || got.result["rejoined"] != true {
		t.Errorf("the join of a member whose ban was lifted: %s", got.raw)
	}

	// A member that left and is then banned was moved: revoked to banned.
	h.must(bob.credential, "/v1/team/leave", `{"team_ref":"`+bob.team+`"}`)
	expect("a ban of a member that left", revoke(`,"ban":true`), "banned", true, 0)
}

// TestRevokeMemberRequests: the two forms are exclusive, `ban` belongs to
// the first, the version is 0 to 2147483647, the owner cannot name itself,
// and a principal that never was a member is no error.
func TestRevokeMemberRequests(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	carol := h.create("other")
	post := func(rest string) answer {
		return h.post(alice.credential, "/v1/team/revoke-member", `{"team_ref":"`+alice.team+`"`+rest+`}`)
	}
	for rest, field := range map[string]string{
		``:            "principal_ref",
		`,"ban":true`: "principal_ref",
		`,"principal_ref":"` + bob.principal + `","joined_secret_version_lte":1`: "principal_ref",
		`,"joined_secret_version_lte":1,"ban":true`:                              "ban",
		`,"joined_secret_version_lte":-1`:                                        "joined_secret_version_lte",
		`,"joined_secret_version_lte":2147483648`:                                "joined_secret_version_lte",
		`,"principal_ref":"` + alice.principal + `"`:                             "principal_ref",
		`,"principal_ref":"` + alice.principal + `","ban":true`:                  "principal_ref",
	} {
		got := post(rest)
		h.refused(got, "invalid_input", http.StatusBadRequest)
		if details, _ := got.err["details"].(map[string]any); details["field"] != field {
			t.Errorf("%s: details %v, want field %s", rest, got.err["details"], field)
		}
	}
	h.refused(post(`,"joined_secret_version_lte":"1"`), "invalid_input", http.StatusBadRequest)
	if joined := h.joinedVersions(alice); len(joined) != 2 {
		t.Errorf("the refused requests changed the roster: %v", joined)
	}

	// Nobody the team ever had: no error, nothing moved, banned or not. A
	// principal of another team is such a one here, and stays in its own.
	for what, ref := range map[string]string{"a principal nobody has": strings.Repeat("0", 32), "a principal of another team": carol.principal} {
		for _, ban := range []bool{false, true} {
			got := post(`,"principal_ref":"` + ref + `","ban":` + strconv.FormatBool(ban))
			if !got.ok || got.result["changed"] != false || got.result["sessions_closed"] != float64(0) || got.result["principal_ref"] != ref {
				t.Errorf("a revocation of %s (ban %v): %s, want changed false", what, ban, got.raw)
			}
		}
	}
	if !h.post(carol.credential, "/v1/team/members", `{"team_ref":"`+carol.team+`"}`).ok {
		t.Errorf("a revocation in one team took a principal out of another")
	}
	// A ban of a principal that never was a member bans nobody: it may join.
	dave := h.create("third")
	h.must(dave.credential, "/v1/team/leave", `{"team_ref":"`+dave.team+`"}`)
	post(`,"principal_ref":"` + dave.principal + `","ban":true`)
	if got := h.rejoin(dave, alice.secret); !got.ok || got.result["rejoined"] != false {
		t.Errorf("the join of a principal that was banned before it ever was a member: %s", got.raw)
	}
}

// TestRevokeByVersion is the second form: every active member that joined
// with a secret of version n or lower, never the owner, never a ban, with
// the count of members and of the sessions that were closed.
func TestRevokeByVersion(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob, carol := h.join(alice.secret), h.join(alice.secret)
	h.register(alice, "a")
	h.register(bob, "b")
	h.register(carol, "c1")
	h.register(carol, "c2")
	rotated, _ := h.rotate(alice)
	dave := h.join(rotated)
	h.register(dave, "d")
	sweep := func(n string) map[string]any {
		t.Helper()
		r := h.must(alice.credential, "/v1/team/revoke-member", `{"team_ref":"`+alice.team+`","joined_secret_version_lte":`+n+`}`)
		if len(r) != 4 || r["team_ref"] != alice.team {
			t.Errorf("a sweep of version %s: %v, want exactly {team_ref, max_version, revoked, sessions_closed}", n, r)
		}
		return r
	}
	if r := sweep("0"); r["max_version"] != float64(0) || r["revoked"] != float64(0) || r["sessions_closed"] != float64(0) {
		t.Errorf("a sweep of version 0: %v, want nobody", r)
	}
	if r := sweep("1"); r["max_version"] != float64(1) || r["revoked"] != float64(2) || r["sessions_closed"] != float64(3) {
		t.Errorf("a sweep of version 1: %v, want bob and carol and their three sessions", r)
	}
	if joined := h.joinedVersions(alice); len(joined) != 2 || joined[alice.principal] != 1 || joined[dave.principal] != 2 {
		t.Errorf("the roster after the sweep: versions %v, want the owner at 1 and dave at 2", joined)
	}
	h.refused(h.post(bob.credential, "/v1/session/list", `{"team_ref":"`+bob.team+`"}`), "unauthorized", http.StatusForbidden)
	// Swept, not banned: bob comes back through the new secret.
	if back := h.rejoin(bob, rotated); !back.ok || back.result["rejoined"] != true {
		t.Errorf("a swept member's join with the new secret: %s", back.raw)
	}
	if r := sweep("1"); r["revoked"] != float64(0) {
		t.Errorf("a second sweep of version 1: %v, want nobody: bob is at 2 now", r)
	}
	if r := sweep("2"); r["revoked"] != float64(2) || r["sessions_closed"] != float64(1) {
		t.Errorf("a sweep of version 2: %v, want bob and dave and dave's session", r)
	}
	if joined := h.joinedVersions(alice); len(joined) != 1 {
		t.Errorf("the roster at the end: %v, want the owner alone", joined)
	}
}

// TestTransfer: an active member becomes the owner, the previous owner
// stays a member and is refused, a principal that is not an active member
// is `invalid_input`, and the owner naming itself changes nothing.
func TestTransfer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob, carol := h.join(alice.secret), h.join(alice.secret)
	outsider := h.create("other")
	transfer := func(m member, ref string) answer {
		return h.post(m.credential, "/v1/team/transfer", `{"team_ref":"`+alice.team+`","principal_ref":"`+ref+`"}`)
	}
	h.must(carol.credential, "/v1/team/leave", `{"team_ref":"`+carol.team+`"}`)
	for what, ref := range map[string]string{
		"a member that left":          carol.principal,
		"a principal of another team": outsider.principal,
		"a principal nobody has":      strings.Repeat("0", 32),
	} {
		got := transfer(alice, ref)
		h.refused(got, "invalid_input", http.StatusBadRequest)
		if details, _ := got.err["details"].(map[string]any); details["field"] != "principal_ref" {
			t.Errorf("a transfer to %s: details %v, want field principal_ref", what, got.err["details"])
		}
	}
	h.refused(h.post(alice.credential, "/v1/team/transfer", `{"team_ref":"`+alice.team+`"}`), "invalid_input", http.StatusBadRequest)

	self := transfer(alice, alice.principal)
	if !self.ok || len(self.result) != 3 || self.result["team_ref"] != alice.team || self.result["principal_ref"] != alice.principal || self.result["transferred"] != true {
		t.Errorf("the owner naming itself: %s", self.raw)
	}
	h.refused(transfer(bob, bob.principal), "unauthorized", http.StatusForbidden)

	moved := transfer(alice, bob.principal)
	if !moved.ok || moved.result["principal_ref"] != bob.principal || moved.result["transferred"] != true {
		t.Errorf("a transfer: %s", moved.raw)
	}
	h.refused(transfer(alice, alice.principal), "unauthorized", http.StatusForbidden)
	h.refused(h.post(alice.credential, "/v1/team/rotate-secret", `{"team_ref":"`+alice.team+`"}`), "unauthorized", http.StatusForbidden)
	if !h.post(alice.credential, "/v1/session/list", `{"team_ref":"`+alice.team+`"}`).ok {
		t.Errorf("the previous owner is no longer a member")
	}
	if _, version := h.rotate(bob); version != 2 {
		t.Errorf("the new owner's rotation: version %v, want 2", version)
	}
	// The new owner may hand the team back.
	if back := transfer(bob, alice.principal); !back.ok {
		t.Errorf("the new owner's transfer: %s", back.raw)
	}
	if _, version := h.rotate(alice); version != 3 {
		t.Errorf("the rotation of the owner the team came back to: version %v, want 3", version)
	}
}
