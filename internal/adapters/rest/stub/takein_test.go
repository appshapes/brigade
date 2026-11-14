//go:build !windows

package stub

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// A team that is taken in by request and the move its owner opens and
// closes, asked as a server is asked, with no adapter in between.

// elsewhere is the reference of a team on a backend that names its teams
// with UUIDs.
const elsewhere = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"

// secretOf is a join secret of team with the given secret half, put
// together here: no file of the repository holds the shape of one.
func secretOf(team, half string) string {
	return protocol.JoinSecretPrefix + team + "." + half
}

// takeIn asks the server to take in the team of secret, with credential
// ("" for none).
func (h *harness) takeIn(credential, secret, name string) answer {
	h.t.Helper()
	return h.post(credential, "/v1/team/take-in", `{"join_secret":"`+secret+`","team_name":"`+name+`","human_label":"taker@example.com"}`)
}

// taken is a take-in that has to succeed, as the member it made or joined.
func (h *harness) taken(credential, secret, name string) (member, map[string]any) {
	h.t.Helper()
	a := h.takeIn(credential, secret, name)
	if !a.ok || a.status != http.StatusOK {
		h.t.Fatalf("take-in: status %d, %s", a.status, a.raw)
	}
	m := member{credential: credential, principal: text2(h.t, a.result, "principal_ref"), team: text2(h.t, a.result, "team_ref"), secret: secret}
	if credential == "" {
		m.credential = text2(h.t, a.result, "credential")
	}
	return m, a.result
}

// setClock makes the server's clock read at.
func (h *harness) setClock(at time.Time) {
	server, _ := h.srv.Config.Handler.(*Server)
	server.mu.Lock()
	server.now = func() time.Time { return at }
	server.mu.Unlock()
}

// openMove opens the move of m's team and returns the answer's members.
func (h *harness) openMove(m member, extra string) map[string]any {
	h.t.Helper()
	return h.must(m.credential, "/v1/team/open-move", `{"team_ref":"`+m.team+`"`+extra+`}`)
}

// closeMove closes the move of m's team and reports `closed`.
func (h *harness) closeMove(m member) bool {
	h.t.Helper()
	r := h.must(m.credential, "/v1/team/close-move", `{"team_ref":"`+m.team+`"}`)
	if len(r) != 3 || r["team_ref"] != m.team || r["team_name"] == "" {
		h.t.Errorf("close-move: %v, want exactly {team_ref, team_name, closed}", r)
	}
	closed, _ := r["closed"].(bool)
	return closed
}

// TestInfoListsTeamTakeIn: a server that leaves the three endpoints out
// does not list team.take_in and answers their paths as it answers any
// unknown path, whoever asks.
func TestInfoListsTeamTakeIn(t *testing.T) {
	t.Parallel()
	without := newHarness(t, Options{WithoutTakeIn: true})
	if kept, _ := without.must("", "/v1/info", `{}`)["capabilities"].([]any); len(kept) != 1 || kept[0] != "team.admin" {
		t.Errorf("/v1/info of a server without the take-in endpoints: capabilities %v, want [team.admin]", kept)
	}
	alice := without.create("ops")
	unknown := without.post(alice.credential, "/v1/nosuch", `{}`)
	left := 0
	for path, ep := range endpoints {
		if !ep.takeIn {
			continue
		}
		left++
		got := without.post(alice.credential, path, `{"team_ref":"`+alice.team+`","join_secret":"`+alice.secret+`","team_name":"ops"}`)
		without.refused(got, "not_found", http.StatusNotFound)
		if got.raw != unknown.raw {
			t.Errorf("%s on a server without it and an unknown path differ:\n%s%s", path, got.raw, unknown.raw)
		}
	}
	if left != 3 {
		t.Errorf("%d endpoints are the take-in's, want take-in, open-move and close-move", left)
	}
}

// TestTakeIn: a team the server does not hold is taken in under the
// reference inside its secret, and the caller is its member and its owner
// in that request; the same request again, and anyone else with the
// secret, is a join; another secret for the reference is a conflict that
// makes nothing; and no answer holds the secret.
func TestTakeIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	secret := secretOf(elsewhere, "kept-from-the-other-backend")

	first := h.takeIn("", secret, "ops")
	if !first.ok || len(first.result) != 6 || first.result["team_ref"] != elsewhere || first.result["team_name"] != "ops" ||
		first.result["taken_in"] != true || first.result["owner"] != true {
		t.Fatalf("take-in: %s, want exactly {team_ref, team_name, principal_ref, taken_in true, owner true, credential}", first.raw)
	}
	alice := member{credential: text2(t, first.result, "credential"), principal: text2(t, first.result, "principal_ref"), team: elsewhere, secret: secret}
	// It is a team like any other: a session, a roster, and an owner.
	h.register(alice, "alpha")
	members, _ := h.must(alice.credential, "/v1/team/members", `{"team_ref":"`+elsewhere+`"}`)["members"].([]any)
	if len(members) != 1 {
		t.Errorf("the roster of a team that was just taken in: %d members, want the taker", len(members))
	}
	if h.closeMove(alice) {
		t.Errorf("close-move by the taker of a team that is not moving: closed true")
	}

	// Again, by the same principal, under whatever name: a join. Nothing is
	// taken in, the team keeps its name and its owner, and no credential is
	// issued.
	_, again := h.taken(alice.credential, secret, "renamed")
	if len(again) != 5 || again["taken_in"] != false || again["owner"] != true || again["team_name"] != "ops" || again["principal_ref"] != alice.principal {
		t.Errorf("the same take-in again: %v, want exactly {team_ref, team_name ops, the same principal_ref, taken_in false, owner true}", again)
	}

	// Anyone else with the secret joins, and does not own.
	bob, joined := h.taken("", secret, "ops")
	if len(joined) != 6 || joined["taken_in"] != false || joined["owner"] != false || bob.principal == alice.principal {
		t.Errorf("a second principal's take-in: %v, want another principal, taken_in false and owner false", joined)
	}
	reference := h.post(bob.credential, "/v1/team/rotate-secret", `{"team_ref":"`+elsewhere+`"}`)
	h.refused(reference, "unauthorized", http.StatusForbidden)
	for _, path := range []string{"/v1/team/open-move", "/v1/team/close-move"} {
		if got := h.post(bob.credential, path, `{"team_ref":"`+elsewhere+`"}`); got.raw != reference.raw {
			t.Errorf("%s by a member that joined through a take-in: %s, want the uniform unauthorized", path, got.raw)
		}
	}

	// Another secret for the same reference: a conflict, with or without a
	// credential, and no principal is made for it.
	dirs := principalDirs(t, h.dir)
	other := secretOf(elsewhere, "another-secret-for-the-reference")
	conflict := h.takeIn("", other, "ops")
	h.refused(conflict, "conflict", http.StatusConflict)
	details, _ := conflict.err["details"].(map[string]any)
	if details["reason"] != "other_join_secret" || conflict.err["message"] != "a team of that reference is here under another join secret" {
		t.Errorf("a take-in under another secret: %s, want reason other_join_secret and the fixed message", conflict.raw)
	}
	carol := h.create("other")
	if got := h.takeIn(alice.credential, other, "ops"); got.raw != conflict.raw {
		t.Errorf("a take-in under another secret by the team's owner: %s, want the same conflict", got.raw)
	}
	if n := principalDirs(t, h.dir); n != dirs+1 {
		t.Errorf("%d principal directories after two refused take-ins and one create, want %d", n, dirs+1)
	}
	for _, a := range []answer{first, conflict} {
		if strings.Contains(a.raw, "kept-from-the-other-backend") || strings.Contains(a.raw, "another-secret-for-the-reference") {
			t.Errorf("an answer holds a join secret: %s", a.raw)
		}
	}
	// The team opens to the secret it was taken in with, and to no other.
	h.refused(h.post("", "/v1/team/join", `{"join_secret":"`+other+`"}`), "unauthorized", http.StatusForbidden)
	h.join(secret)

	// A principal the owner banned is told what another secret is told,
	// although its secret is the team's.
	h.must(alice.credential, "/v1/team/revoke-member", `{"team_ref":"`+elsewhere+`","principal_ref":"`+bob.principal+`","ban":true}`)
	if got := h.takeIn(bob.credential, secret, "ops"); got.raw != conflict.raw {
		t.Errorf("a take-in by a banned principal: %s, want the conflict of another secret", got.raw)
	}

	// A team that was created here is held to the same: its creator's
	// take-in with its own secret is a join that changes nothing, and
	// another secret for its reference is a conflict.
	_, own := h.taken(carol.credential, carol.secret, "renamed")
	if own["taken_in"] != false || own["owner"] != true || own["team_name"] != "other" {
		t.Errorf("a take-in of a team that was created here, by its creator: %v", own)
	}
	h.refused(h.takeIn("", secretOf(carol.team, "forged"), "other"), "conflict", http.StatusConflict)
}

// TestTakeInRequests: the request's shape is checked member by member,
// the secret first and its format last, and nothing of a refused request
// is kept.
func TestTakeInRequests(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	secret := secretOf(elsewhere, "kept-from-the-other-backend")
	long := strings.Repeat("x", 300)
	for name, tc := range map[string]struct{ body, field string }{
		"nothing":                           {`{}`, "join_secret"},
		"no secret":                         {`{"team_name":"ops"}`, "join_secret"},
		"no name":                           {`{"join_secret":"` + secret + `"}`, "team_name"},
		"no name and a malformed secret":    {`{"join_secret":"nonsense"}`, "team_name"},
		"a long name":                       {`{"join_secret":"` + secret + `","team_name":"` + long + `"}`, "team_name"},
		"a long label":                      {`{"join_secret":"` + secret + `","team_name":"ops","human_label":"` + long + `"}`, "human_label"},
		"a malformed secret":                {`{"join_secret":"nonsense","team_name":"ops"}`, "join_secret"},
		"a secret with no secret half":      {`{"join_secret":"brg1.` + elsewhere + `.","team_name":"ops"}`, "join_secret"},
		"a reference the store cannot keep": {`{"join_secret":"` + secretOf("with.a.dot", "half") + `","team_name":"ops"}`, "join_secret"},
	} {
		got := h.post("", "/v1/team/take-in", tc.body)
		h.refused(got, "invalid_input", http.StatusBadRequest)
		if details, _ := got.err["details"].(map[string]any); details["field"] != tc.field {
			t.Errorf("%s: %s, want details.field %q", name, got.raw, tc.field)
		}
		if strings.Contains(got.raw, "nonsense") || strings.Contains(got.raw, "kept-from") {
			t.Errorf("%s: the refusal echoes the secret: %s", name, got.raw)
		}
	}
	if n := principalDirs(t, h.dir); n != 0 {
		t.Errorf("%d principal directories after refused take-ins, want none", n)
	}
	// Nothing was taken in: the secret is still that of a team the server does not hold.
	h.refused(h.post("", "/v1/team/join", `{"join_secret":"`+secret+`"}`), "unauthorized", http.StatusForbidden)

	// A principal that is bound to a team of this stub cannot be put in a
	// second: the take-in is refused before the team is written.
	alice := h.create("ops")
	h.refused(h.takeIn(alice.credential, secret, "moved"), "conflict", http.StatusConflict)
	h.refused(h.post("", "/v1/team/join", `{"join_secret":"`+secret+`"}`), "unauthorized", http.StatusForbidden)
}

// TestTakeInNeedsTheCreationToken: with Options.CreateToken set a take-in
// needs the token as a creation does, is refused with a creation's bytes,
// and is refused before its shape is looked at, whether or not the server
// holds the team.
func TestTakeInNeedsTheCreationToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{CreateToken: "operator-token"})
	secret := secretOf(elsewhere, "kept-from-the-other-backend")
	body := `{"join_secret":"` + secret + `","team_name":"ops"}`
	token := map[string]string{createTokenHeader: "operator-token"}
	create := h.post("", "/v1/team/create", `{"team_name":"ops"}`)
	h.refused(create, "unauthorized", http.StatusForbidden)
	for name, request := range map[string]answer{
		"no token":                  h.post("", "/v1/team/take-in", body),
		"another token":             h.do(t.Context(), http.MethodPost, "", "/v1/team/take-in", body, map[string]string{createTokenHeader: "another"}),
		"no token, nothing to read": h.post("", "/v1/team/take-in", `{}`),
	} {
		if request.raw != create.raw {
			t.Errorf("a take-in with %s: %s, want the refusal of a creation", name, request.raw)
		}
	}
	h.refused(h.post("", "/v1/team/join", `{"join_secret":"`+secret+`"}`), "unauthorized", http.StatusForbidden)

	right := h.do(t.Context(), http.MethodPost, "", "/v1/team/take-in", body, token)
	if !right.ok || right.result["taken_in"] != true {
		t.Fatalf("a take-in with the token: %s", right.raw)
	}
	// The team is here now, and a take-in of it is still a take-in: no token, no answer.
	if again := h.post("", "/v1/team/take-in", body); again.raw != create.raw {
		t.Errorf("a take-in of a team that is here, with no token: %s, want the refusal of a creation", again.raw)
	}
	if again := h.do(t.Context(), http.MethodPost, "", "/v1/team/take-in", body, token); !again.ok || again.result["taken_in"] != false {
		t.Errorf("a take-in of a team that is here, with the token: %s", again.raw)
	}
	// Joining needs no token, and neither does the owner's move.
	h.join(secret)
	owner := member{credential: text2(t, right.result, "credential"), team: elsewhere}
	h.openMove(owner, "")
	if !h.closeMove(owner) {
		t.Errorf("close-move after an open: closed false")
	}
}

// TestOnlyTheOwnerMovesItsTeam: open-move and close-move are refused to
// everyone but the team's owner with the uniform `unauthorized`, before
// the request is read; a request with no credential is `unauthenticated`;
// and none of it opens or closes anything.
func TestOnlyTheOwnerMovesItsTeam(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice, _ := h.taken("", secretOf(elsewhere, "kept-from-the-other-backend"), "ops")
	bob := h.join(alice.secret)
	carol := h.create("other")
	reference := h.post(carol.credential, "/v1/team/members", `{"team_ref":"`+alice.team+`"}`)
	h.refused(reference, "unauthorized", http.StatusForbidden)
	ticket := text2(t, h.openMove(alice, ""), "ticket")
	for _, path := range []string{"/v1/team/open-move", "/v1/team/close-move"} {
		for who, m := range map[string]member{"a member that is not the owner": bob, "a principal of another team": carol} {
			for _, rest := range []string{``, `,"ticket_days":0`, `,"ticket_days":"many"`} {
				if got := h.post(m.credential, path, `{"team_ref":"`+alice.team+`"`+rest+`}`); got.raw != reference.raw {
					t.Errorf("%s by %s (%s): %s, want the uniform unauthorized", path, who, rest, got.raw)
				}
			}
		}
		if random := h.post(alice.credential, path, `{"team_ref":"0123456789abcdef0123456789abcdef"}`); random.raw != reference.raw {
			t.Errorf("%s for a team that does not exist: %s, want the uniform unauthorized", path, random.raw)
		}
		h.refused(h.post("", path, `{"team_ref":"`+alice.team+`"}`), "unauthenticated", http.StatusUnauthorized)
		h.refused(h.post("not-a-credential-of-this-server", path, `{"team_ref":"`+alice.team+`"}`), "unauthenticated", http.StatusUnauthorized)
		named := h.post(alice.credential, path, `{}`)
		h.refused(named, "invalid_input", http.StatusBadRequest)
		if details, _ := named.err["details"].(map[string]any); details["field"] != "team_ref" {
			t.Errorf("%s with no team: %s, want details.field team_ref", path, named.raw)
		}
	}
	// The owner's own request is read: a number of days no ticket is given is told so.
	for _, days := range []string{`0`, `366`, `-1`, `"30"`, `1.5`} {
		got := h.post(alice.credential, "/v1/team/open-move", `{"team_ref":"`+alice.team+`","ticket_days":`+days+`}`)
		h.refused(got, "invalid_input", http.StatusBadRequest)
		details, _ := got.err["details"].(map[string]any)
		if details["field"] != "ticket_days" || details["reason"] != "out_of_range" || details["min"] != "1" || details["max"] != "365" {
			t.Errorf("open-move with ticket_days %s: %s, want ticket_days out_of_range 1..365", days, got.raw)
		}
	}
	// Nothing of all that replaced or closed the move the owner opened.
	dave := h.join(ticket)
	if dave.team != alice.team {
		t.Errorf("the ticket of the owner's move no longer admits after the refused requests")
	}
	if !h.closeMove(alice) {
		t.Errorf("close-move by the owner: closed false, although nothing closed the move")
	}
	// An owner that left is not an active member, and is refused like anyone.
	h.must(alice.credential, "/v1/team/leave", `{"team_ref":"`+alice.team+`"}`)
	for _, path := range []string{"/v1/team/open-move", "/v1/team/close-move"} {
		if got := h.post(alice.credential, path, `{"team_ref":"`+alice.team+`"}`); got.raw != reference.raw {
			t.Errorf("%s by an owner that left: %s, want the uniform unauthorized", path, got.raw)
		}
	}
}

// TestOpenAndCloseMove: the ticket is a join secret of the team, shown
// once in the answer; a join it admits makes a member that never owns; it
// admits until its end and not at it; a second open replaces it and says
// so; a close ends it and leaves every member a member; and a rotation of
// the join secret closes the move.
func TestOpenAndCloseMove(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	start := time.Date(2026, 11, 14, 12, 0, 0, 500_000_000, time.UTC)
	h.setClock(start)
	alice, _ := h.taken("", secretOf(elsewhere, "kept-from-the-other-backend"), "ops")
	rejected := h.post("", "/v1/team/join", `{"join_secret":"`+neverIssued(elsewhere)+`"}`)
	h.refused(rejected, "unauthorized", http.StatusForbidden)

	opened := h.openMove(alice, "")
	if len(opened) != 5 || opened["team_ref"] != elsewhere || opened["team_name"] != "ops" || opened["replaced"] != false {
		t.Errorf("open-move: %v, want exactly {team_ref, team_name, ticket, ends_at, replaced false}", opened)
	}
	ticket := text2(t, opened, "ticket")
	parsed, err := protocol.ParseJoinSecret(ticket)
	if err != nil || parsed.TeamRef() != elsewhere || ticket == alice.secret {
		t.Fatalf("the ticket does not parse, names another team or is the join secret (%v)", err)
	}
	if half := strings.TrimPrefix(ticket, protocol.JoinSecretPrefix+elsewhere+"."); len(half) != 32 || strings.Trim(half, "0123456789abcdef") != "" {
		t.Errorf("the ticket's own part is not 32 hex digits")
	}
	// Thirty days when the request names none, on the whole second.
	ends := start.Truncate(time.Second).Add(30 * 24 * time.Hour)
	if got, err := time.Parse(time.RFC3339Nano, text2(t, opened, "ends_at")); err != nil || !got.Equal(ends) {
		t.Errorf("ends_at %v (%v), want %s", opened["ends_at"], err, ends.Format(time.RFC3339))
	}

	// A member comes in with the ticket and is a member like any other, and never the owner.
	bob := h.join(ticket)
	if bob.team != elsewhere {
		t.Errorf("a join with the ticket joined %q", bob.team)
	}
	h.refused(h.post(bob.credential, "/v1/team/close-move", `{"team_ref":"`+elsewhere+`"}`), "unauthorized", http.StatusForbidden)
	// A take-in with the ticket is that join too: nothing is taken in and nothing is owned.
	_, followed := h.taken("", ticket, "ops")
	if followed["taken_in"] != false || followed["owner"] != false {
		t.Errorf("a take-in with the ticket: %v, want taken_in false and owner false", followed)
	}
	// The join secret admits as it always did.
	h.join(alice.secret)

	// The ticket admits until its end and not at it.
	h.setClock(ends.Add(-time.Second))
	h.join(ticket)
	for _, at := range []time.Time{ends, ends.Add(time.Second), ends.Add(30 * 24 * time.Hour)} {
		h.setClock(at)
		if got := h.post("", "/v1/team/join", `{"join_secret":"`+ticket+`"}`); got.raw != rejected.raw {
			t.Errorf("a join with the ticket at %s: %s, want the rejected join", at.Format(time.RFC3339), got.raw)
		}
	}

	// A move that has ended is still the team's move: another takes its
	// place and says so, for the days the request names.
	second := h.openMove(alice, `,"ticket_days":7`)
	next := text2(t, second, "ticket")
	if second["replaced"] != true || next == ticket {
		t.Errorf("a second open-move: %v, want replaced true and another ticket", second)
	}
	week := ends.Add(30 * 24 * time.Hour).Add(7 * 24 * time.Hour)
	if got, err := time.Parse(time.RFC3339Nano, text2(t, second, "ends_at")); err != nil || !got.Equal(week) {
		t.Errorf("ends_at of a move of seven days %v (%v), want %s", second["ends_at"], err, week.Format(time.RFC3339))
	}
	carol := h.join(next)
	// A third, while the second still admits: the second admits nobody from then on.
	third := h.openMove(alice, `,"ticket_days":365`)
	last := text2(t, third, "ticket")
	if third["replaced"] != true {
		t.Errorf("an open-move over one that still admits: replaced %v, want true", third["replaced"])
	}
	for what, old := range map[string]string{"the first ticket": ticket, "the ticket that was replaced": next} {
		if got := h.post("", "/v1/team/join", `{"join_secret":"`+old+`"}`); got.raw != rejected.raw {
			t.Errorf("a join with %s: %s, want the rejected join", what, got.raw)
		}
	}
	h.join(last)

	// The owner closes the move: its ticket admits nobody, every member it
	// let in is still one, and closing again changes nothing.
	if !h.closeMove(alice) {
		t.Errorf("close-move of a team that is moving: closed false")
	}
	if got := h.post("", "/v1/team/join", `{"join_secret":"`+last+`"}`); got.raw != rejected.raw {
		t.Errorf("a join with the ticket of a move that was closed: %s, want the rejected join", got.raw)
	}
	if h.closeMove(alice) {
		t.Errorf("a second close-move: closed true")
	}
	members, _ := h.must(carol.credential, "/v1/team/members", `{"team_ref":"`+elsewhere+`"}`)["members"].([]any)
	if len(members) != 7 {
		t.Errorf("%d members after the move was closed, want the seven that came in", len(members))
	}
	// An open after a close replaces nothing.
	if reopened := h.openMove(alice, ""); reopened["replaced"] != false {
		t.Errorf("an open-move after a close: replaced %v, want false", reopened["replaced"])
	}

	// A rotation ends everything that let anyone in before it, the move
	// with it; and a move opened after it admits with its ticket, although
	// the secret the store knows was rotated away.
	current := text2(t, h.openMove(alice, ""), "ticket")
	rotated, _ := h.rotate(alice)
	for what, old := range map[string]string{"the ticket": current, "the secret that was rotated away": alice.secret} {
		if got := h.post("", "/v1/team/join", `{"join_secret":"`+old+`"}`); got.raw != rejected.raw {
			t.Errorf("a join after a rotation with %s: %s, want the rejected join", what, got.raw)
		}
	}
	if h.closeMove(alice) {
		t.Errorf("close-move after a rotation: closed true, although the rotation closed the move")
	}
	h.join(rotated)
	after := h.openMove(alice, "")
	if after["replaced"] != false {
		t.Errorf("an open-move after a rotation: replaced %v, want false", after["replaced"])
	}
	h.join(text2(t, after, "ticket"))
	if !h.closeMove(alice) {
		t.Errorf("the last close-move: closed false")
	}
}
