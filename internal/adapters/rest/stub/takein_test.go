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

// A retry key as a client mints one: 32 random bytes in base64url, here the
// same in every run, and another of the same form. No file of the
// repository holds a real one.
const (
	retryKeyOfTheTest = "Zm9yLXRoZS10YWtlLWluLW9mLXRoZS10ZXN0LW9ubHk"
	anotherRetryKey   = "YW5vdGhlci1rZXktdGhhdC1ub2JvZHktbWludGVkLTE"
)

// takeInKeyed asks the server to take in the team of secret with a retry
// key, with credential ("" for none).
func (h *harness) takeInKeyed(credential, secret, key string) answer {
	h.t.Helper()
	return h.post(credential, "/v1/team/take-in",
		`{"join_secret":"`+secret+`","team_name":"ops","human_label":"taker@example.com","retry_key":"`+key+`"}`)
}

// knows reports whether the server knows the credential: a request about
// a team that is not there is refused either way, as a credential nobody
// has or as a principal that is no member of that team.
func (h *harness) knows(credential string) bool {
	h.t.Helper()
	got := h.post(credential, "/v1/team/members", `{"team_ref":"no-such-team"}`)
	if got.code == "unauthenticated" {
		return false
	}
	h.refused(got, "unauthorized", http.StatusForbidden)
	return true
}

// TestATakeInMadeAgainWithItsKey: the stand-in keeps the contract's rule
// of a take-in whose answer was lost. The request made again with the
// team's join secret, the key of the first and no credential the server
// knows is answered for the principal the first one made, which owns the
// team, with a new credential, and the credential before it is unknown;
// it is made again as often as its answer is lost. Another key and no key
// are the join of a new principal; the key with another secret is the
// conflict another secret is; a caller the server knows is the principal
// its credential names. Nobody else's request ends the key, and neither
// does what its owner reads. The owner's own request that is no read does:
// from then on the same request is a stranger's join.
func TestATakeInMadeAgainWithItsKey(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	secret := secretOf(elsewhere, "kept-from-the-other-backend")
	first := h.takeInKeyed("", secret, retryKeyOfTheTest)
	if !first.ok || first.result["taken_in"] != true || first.result["owner"] != true {
		t.Fatalf("the first take-in: %s", first.raw)
	}
	lost := text2(t, first.result, "credential")
	alice := member{credential: lost, principal: text2(t, first.result, "principal_ref"), team: elsewhere, secret: secret}
	// Positive control: the credential of the first answer is one the server knows.
	if !h.knows(alice.credential) {
		t.Fatalf("the credential the first answer carried is unknown before anything was asked again")
	}
	bob := h.join(secret)
	dirs := principalDirs(t, h.dir)

	again := h.takeInKeyed("", secret, retryKeyOfTheTest)
	if !again.ok || len(again.result) != 6 || again.result["principal_ref"] != alice.principal ||
		again.result["taken_in"] != false || again.result["owner"] != true || again.result["team_name"] != "ops" {
		t.Fatalf("the take-in made again with its key: %s, want the first principal, taken_in false, owner true and a credential", again.raw)
	}
	second := text2(t, again.result, "credential")
	if second == alice.credential || h.knows(alice.credential) || !h.knows(second) {
		t.Errorf("the credential of the lost answer is still known, or the new one is not")
	}
	if n := principalDirs(t, h.dir); n != dirs {
		t.Errorf("%d principal directories after the take-in was made again, want the %d there were: nobody new is made", n, dirs)
	}
	// The second answer may be lost as well. The request carries the
	// credential of the first, as a client that still had it in its file
	// would: a credential the server does not know is no credential.
	twice := h.takeInKeyed(lost, secret, retryKeyOfTheTest)
	if !twice.ok || twice.result["principal_ref"] != alice.principal || twice.result["owner"] != true {
		t.Fatalf("the take-in made again a second time: %s, want the first principal once more", twice.raw)
	}
	alice.credential = text2(t, twice.result, "credential")
	if h.knows(second) || !h.knows(alice.credential) {
		t.Errorf("the credential of the second lost answer is still known, or the new one is not")
	}

	// Nobody else's request ends the key, whatever it is: a member that
	// registers a session, leaves and joins again, and takes the team in
	// with the owner's key as the member it is; strangers with another key
	// and with none; the key with another secret.
	h.register(bob, "beta")
	h.must(bob.credential, "/v1/team/leave", `{"team_ref":"`+elsewhere+`"}`)
	if back := h.rejoin(bob, secret); !back.ok {
		t.Fatalf("the member joins again: %s", back.raw)
	}
	if member := h.takeInKeyed(bob.credential, secret, retryKeyOfTheTest); !member.ok || len(member.result) != 5 ||
		member.result["principal_ref"] != bob.principal || member.result["owner"] != false {
		t.Errorf("a member's take-in with the owner's key: %s, want the member it is and no credential", member.raw)
	}
	wrong := h.takeInKeyed("", secret, anotherRetryKey)
	none := h.takeIn("", secret, "ops")
	for name, got := range map[string]answer{"another key": wrong, "no key": none} {
		if !got.ok || len(got.result) != 6 || got.result["taken_in"] != false || got.result["owner"] != false || got.result["principal_ref"] == alice.principal {
			t.Errorf("a take-in with %s: %s, want a new principal that does not own", name, got.raw)
		}
	}
	other := secretOf(elsewhere, "another-secret-for-the-reference")
	conflict := h.takeInKeyed("", other, retryKeyOfTheTest)
	h.refused(conflict, "conflict", http.StatusConflict)
	if unkeyed := h.takeIn("", other, "ops"); unkeyed.raw != conflict.raw {
		t.Errorf("another secret with the key and without it differ:\n%s%s", conflict.raw, unkeyed.raw)
	}
	// Nor does what the owner reads with its credential, or is refused.
	if members, _ := h.must(alice.credential, "/v1/team/members", `{"team_ref":"`+elsewhere+`"}`)["members"].([]any); len(members) != 4 {
		t.Errorf("the roster the owner reads: %d members, want the four there are", len(members))
	}
	if sessions, _ := h.must(alice.credential, "/v1/session/list", `{"team_ref":"`+elsewhere+`","include_offline":true}`)["sessions"].([]any); len(sessions) != 1 {
		t.Errorf("the sessions the owner lists: %d, want the member's one", len(sessions))
	}
	h.refused(h.post(alice.credential, "/v1/team/open-move", `{"team_ref":"`+elsewhere+`","ticket_days":0}`), "invalid_input", http.StatusBadRequest)
	still := h.takeInKeyed("", secret, retryKeyOfTheTest)
	if !still.ok || still.result["principal_ref"] != alice.principal || still.result["owner"] != true {
		t.Fatalf("the take-in made again after all of it: %s, want the first principal, which owns the team", still.raw)
	}
	alice.credential = text2(t, still.result, "credential")

	// A key that is no key is refused by the name of its member, whatever
	// the server holds, and its text is in no answer.
	for name, key := range map[string]string{
		"an empty key":            `""`,
		"a short key":             `"` + strings.Repeat("k", 21) + `"`,
		"a long key":              `"` + strings.Repeat("k", 129) + `"`,
		"a key with a space":      `"a key with spaces in it, long enough"`,
		"a key that is no string": `22`,
	} {
		got := h.post("", "/v1/team/take-in", `{"join_secret":"`+secret+`","team_name":"ops","retry_key":`+key+`}`)
		h.refused(got, "invalid_input", http.StatusBadRequest)
		if details, _ := got.err["details"].(map[string]any); details["field"] != "retry_key" || details["reason"] != "invalid_value" {
			t.Errorf("%s: %s, want details.field retry_key", name, got.raw)
		}
		if strings.Contains(got.raw, "spaces in it") || strings.Contains(got.raw, "kkkk") {
			t.Errorf("%s: the refusal repeats the key: %s", name, got.raw)
		}
	}
	// `null` is no key, and a key at either end of its length is one.
	if got := h.post("", "/v1/team/take-in", `{"join_secret":"`+secret+`","team_name":"ops","retry_key":null}`); !got.ok || got.result["owner"] != false {
		t.Errorf("a take-in whose key is null: %s, want the join of a new principal", got.raw)
	}
	for _, key := range []string{strings.Repeat("k", 22), strings.Repeat("A-Za-z0-9._~+/=", 9)[:128]} {
		if got := h.takeInKeyed("", secret, key); !got.ok {
			t.Errorf("a take-in with a key of %d characters: %s", len(key), got.raw)
		}
	}

	// The owner opens the team's move: its own request, with the credential
	// it holds, and no read. That ends the key. The same request is a
	// stranger's join from then on, and the owner's credential is the one it
	// was; the ticket with the key is the join the ticket is.
	ticket := text2(t, h.openMove(alice, ""), "ticket")
	h.refused(h.post(bob.credential, "/v1/team/open-move", `{"team_ref":"`+elsewhere+`"}`), "unauthorized", http.StatusForbidden)
	ended := h.takeInKeyed("", secret, retryKeyOfTheTest)
	if !ended.ok || len(ended.result) != 6 || ended.result["owner"] != false || ended.result["principal_ref"] == alice.principal {
		t.Errorf("the key after its owner's own request: %s, want the join of a new principal", ended.raw)
	}
	if !h.knows(alice.credential) {
		t.Errorf("a key that ended replaced the owner's credential")
	}
	if followed := h.takeInKeyed("", ticket, retryKeyOfTheTest); !followed.ok || followed.result["owner"] != false || followed.result["principal_ref"] == alice.principal {
		t.Errorf("the ticket of the move with the key: %s, want the join of a new principal", followed.raw)
	}
	for _, a := range []answer{first, again, twice, wrong, conflict, still, ended} {
		if strings.Contains(a.raw, retryKeyOfTheTest) || strings.Contains(a.raw, anotherRetryKey) {
			t.Errorf("an answer holds a retry key: %s", a.raw)
		}
	}

	// A caller the server knows takes a team in as the principal it is and
	// holds its credential still: no credential rides the answer and no key
	// is kept, so the same key with no credential is a stranger's join.
	third := newHarness(t, Options{})
	carol := third.create("here")
	third.must(carol.credential, "/v1/team/leave", `{"team_ref":"`+carol.team+`"}`)
	if taken := third.takeInKeyed(carol.credential, secret, retryKeyOfTheTest); !taken.ok || len(taken.result) != 5 ||
		taken.result["taken_in"] != true || taken.result["principal_ref"] != carol.principal {
		t.Fatalf("a take-in by a principal the server knows: %s, want the principal it is and no credential", taken.raw)
	}
	if stranger := third.takeInKeyed("", secret, retryKeyOfTheTest); !stranger.ok || stranger.result["owner"] != false || stranger.result["principal_ref"] == carol.principal {
		t.Errorf("the key of a take-in that made no principal: %s, want the join of a new principal", stranger.raw)
	}
	if !third.knows(carol.credential) {
		t.Errorf("the key of a take-in that made no principal replaced its caller's credential")
	}
}

// TestARetryKeyEndsWithItsOwnersOwnRequest: each request the owner a
// take-in made asks with its own credential, and that is no read, ends the
// key: the owner holds a credential from then on, and the key has nothing
// left to stand in for. Before it the take-in is made again (the positive
// control); after it the same request, with the secret the team has and the
// key, is the join of a new principal, and nothing of the owner's is
// touched.
func TestARetryKeyEndsWithItsOwnersOwnRequest(t *testing.T) {
	t.Parallel()
	secret := secretOf(elsewhere, "kept-from-the-other-backend")
	team := `{"team_ref":"` + elsewhere + `"}`
	for name, ask := range map[string]func(h *harness, owner, other member) string{
		"registers a session": func(h *harness, owner, _ member) string {
			h.register(owner, "alpha")
			return secret
		},
		"makes its credential unknown": func(h *harness, owner, _ member) string {
			h.must(owner.credential, "/v1/credential/revoke", `{}`)
			return secret
		},
		"opens the team's move": func(h *harness, owner, _ member) string {
			h.openMove(owner, "")
			return secret
		},
		"revokes a member": func(h *harness, owner, other member) string {
			h.must(owner.credential, "/v1/team/revoke-member", `{"team_ref":"`+elsewhere+`","principal_ref":"`+other.principal+`"}`)
			return secret
		},
		"transfers the team": func(h *harness, owner, other member) string {
			h.must(owner.credential, "/v1/team/transfer", `{"team_ref":"`+elsewhere+`","principal_ref":"`+other.principal+`"}`)
			return secret
		},
		"leaves the team": func(h *harness, owner, _ member) string {
			h.must(owner.credential, "/v1/team/leave", team)
			return secret
		},
		"rotates the team's secret": func(h *harness, owner, _ member) string {
			rotated, _ := h.rotate(owner)
			return rotated
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, Options{})
			first := h.takeInKeyed("", secret, retryKeyOfTheTest)
			if !first.ok {
				t.Fatalf("the first take-in: %s", first.raw)
			}
			owner := member{principal: text2(t, first.result, "principal_ref"), team: elsewhere, secret: secret}
			other := h.join(secret)
			again := h.takeInKeyed("", secret, retryKeyOfTheTest)
			if !again.ok || again.result["principal_ref"] != owner.principal || again.result["owner"] != true {
				t.Fatalf("the take-in made again before the owner asked anything: %s", again.raw)
			}
			owner.credential = text2(t, again.result, "credential")

			now := ask(h, owner, other)
			ended := h.takeInKeyed("", now, retryKeyOfTheTest)
			if !ended.ok || len(ended.result) != 6 || ended.result["taken_in"] != false || ended.result["owner"] != false ||
				ended.result["principal_ref"] == owner.principal || ended.result["principal_ref"] == other.principal {
				t.Errorf("the same take-in after the owner's own request: %s, want the join of a new principal", ended.raw)
			}
			if known := h.knows(owner.credential); known != (name != "makes its credential unknown") {
				t.Errorf("the owner's credential is known: %v", known)
			}
		})
	}
}
