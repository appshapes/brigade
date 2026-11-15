//go:build !windows

package stub

import (
	"net/http"
	"strings"
	"testing"
)

// The record of where a team went (the contract's section 3c; work item
// 341, ADR 0010), as the stand-in keeps it: the same rules the built
// server's tests hold it to (server/crates/brigade-core/tests/rules.rs).

// ticketHalf is the secret half of the ticket these tests record: what no
// answer but the one that was asked for it may hold.
const ticketHalf = "the-secret-part-of-the-ticket"

func moveBody(team, url, ticket, rest string) string {
	return `{"team_ref":"` + team + `","adapter":"rest","url":"` + url + `","ticket":"` + ticket + `"` + rest + `}`
}

// TestInfoListsTeamMove: a server that keeps no record of where a team
// went does not list team.move and answers the two paths as it answers
// any unknown path, whoever asks. That is every server before 0.8.0.
func TestInfoListsTeamMove(t *testing.T) {
	t.Parallel()
	without := newHarness(t, Options{WithoutTeamMove: true})
	if kept, _ := without.must("", "/v1/info", `{}`)["capabilities"].([]any); len(kept) != 2 || kept[0] != "team.admin" || kept[1] != "team.take_in" {
		t.Errorf("/v1/info of a server without the record of a move: capabilities %v, want [team.admin team.take_in]", kept)
	}
	alice := without.create("ops")
	unknown := without.post(alice.credential, "/v1/nosuch", `{}`)
	for _, path := range []string{"/v1/team/move", "/v1/team/moved"} {
		for who, credential := range map[string]string{"the owner": alice.credential, "nobody": ""} {
			got := without.post(credential, path, `{"team_ref":"`+alice.team+`"}`)
			if got.raw != unknown.raw || got.status != http.StatusNotFound {
				t.Errorf("%s by %s on a server that leaves it out: %d %s, want what an unknown path answers", path, who, got.status, got.raw)
			}
		}
	}
}

// TestTheRecordOfAMoveIsItsOwnersToWriteAndEveryMembersToRead: the owner
// alone records where the team went and clears the record, every other
// caller reading the one refusal whatever the request held; every active
// member reads it, with the ticket only when it asks; a second record
// replaces the first; a clear is safe to repeat; and the team is as it
// was, with no join and no session refused for the record.
func TestTheRecordOfAMoveIsItsOwnersToWriteAndEveryMembersToRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	carol := h.create("other")
	ticket := secretOf(alice.team, ticketHalf)
	team := `{"team_ref":"` + alice.team + `"}`
	asked := `{"team_ref":"` + alice.team + `","with_ticket":true}`
	clearIt := `{"team_ref":"` + alice.team + `","clear":true}`
	record := moveBody(alice.team, "https://brigade.example.com", ticket, "")

	// No move recorded: two members and no more, with or without the ask.
	for who, m := range map[string]member{"the owner": alice, "a member": bob} {
		for _, body := range []string{team, asked} {
			got := h.must(m.credential, "/v1/team/moved", body)
			if len(got) != 2 || got["team_ref"] != alice.team || got["moved"] != false {
				t.Errorf("/v1/team/moved by %s with nothing recorded: %v, want the team and moved false alone", who, got)
			}
		}
	}

	// The uniform refusal: a principal of another team, and a team that
	// does not exist, for the read; and for the write a member that is not
	// the owner as well, for a record, for a clear and for a request that
	// would be invalid from the owner.
	reference := h.post(carol.credential, "/v1/team/moved", team)
	h.refused(reference, "unauthorized", http.StatusForbidden)
	if got := h.post(bob.credential, "/v1/team/moved", `{"team_ref":"0123456789abcdef0123456789abcdef"}`); got.raw != reference.raw {
		t.Errorf("/v1/team/moved for a team that does not exist: %s, want the uniform unauthorized", got.raw)
	}
	for what, body := range map[string]string{
		"a record": record, "a clear": clearIt,
		"a request that reads as neither": `{"team_ref":"` + alice.team + `","clear":"yes","ticket":7}`,
	} {
		for who, m := range map[string]member{"a member that is not the owner": bob, "a principal of another team": carol} {
			if got := h.post(m.credential, "/v1/team/move", body); got.raw != reference.raw {
				t.Errorf("%s by %s: %s, want the uniform unauthorized", what, who, got.raw)
			}
		}
	}
	h.refused(h.post("", "/v1/team/move", record), "unauthenticated", http.StatusUnauthorized)
	h.refused(h.post("", "/v1/team/moved", team), "unauthenticated", http.StatusUnauthorized)

	// The owner's own request is held to its shape, each refusal naming its
	// member and holding nothing of the ticket.
	long := func(n int) string { return strings.Repeat("u", n) }
	for what, tc := range map[string]struct{ body, field, reason string }{
		"no adapter":                 {`{"team_ref":"` + alice.team + `","url":"u","ticket":"` + ticket + `"}`, "adapter", "required"},
		"an adapter too long":        {`{"team_ref":"` + alice.team + `","adapter":"` + long(65) + `","url":"u","ticket":"` + ticket + `"}`, "adapter", "too_long"},
		"no url":                     {`{"team_ref":"` + alice.team + `","adapter":"rest","ticket":"` + ticket + `"}`, "url", "required"},
		"a url too long":             {moveBody(alice.team, long(2049), ticket, ""), "url", "too_long"},
		"a certificate too long":     {moveBody(alice.team, "u", ticket, `,"certificate":"`+long(129)+`"`), "certificate", "too_long"},
		"no ticket":                  {`{"team_ref":"` + alice.team + `","adapter":"rest","url":"u"}`, "ticket", "required"},
		"what is no join secret":     {moveBody(alice.team, "u", ticketHalf, ""), "ticket", "invalid_value"},
		"a ticket of another team":   {moveBody(alice.team, "u", secretOf(carol.team, ticketHalf), ""), "ticket", "invalid_value"},
		"a ticket too long":          {moveBody(alice.team, "u", secretOf(alice.team, strings.Repeat(ticketHalf, 40)), ""), "ticket", "invalid_value"},
		"a clear beside a record":    {moveBody(alice.team, "u", ticket, `,"clear":true`), "clear", "invalid_value"},
		"a clear that is no boolean": {`{"team_ref":"` + alice.team + `","clear":"true"}`, "clear", "invalid_value"},
	} {
		got := h.post(alice.credential, "/v1/team/move", tc.body)
		h.refused(got, "invalid_input", http.StatusBadRequest)
		details, _ := got.err["details"].(map[string]any)
		if details["field"] != tc.field || details["reason"] != tc.reason {
			t.Errorf("%s: details %v, want field %s and reason %s", what, details, tc.field, tc.reason)
		}
		if strings.Contains(got.raw, ticketHalf) {
			t.Errorf("%s: the refusal holds the ticket: %s", what, got.raw)
		}
	}
	if got := h.must(alice.credential, "/v1/team/moved", team); got["moved"] != false {
		t.Fatalf("a refused request recorded a move: %v", got)
	}

	// The record: 4.4.10's result, no certificate and no ticket.
	recorded := h.must(alice.credential, "/v1/team/move", record)
	if len(recorded) != 5 || recorded["team_ref"] != alice.team || recorded["moved"] != true || recorded["adapter"] != "rest" ||
		recorded["url"] != "https://brigade.example.com" || recorded["recorded_at"] == nil {
		t.Errorf("/v1/team/move: %v, want team_ref, moved, adapter, url and recorded_at", recorded)
	}
	for who, m := range map[string]member{"the owner": alice, "a member": bob} {
		plain := h.must(m.credential, "/v1/team/moved", team)
		if _, has := plain["ticket"]; has || plain["url"] != "https://brigade.example.com" {
			t.Errorf("/v1/team/moved by %s: %v, want the record and no ticket", who, plain)
		}
		with := h.must(m.credential, "/v1/team/moved", asked)
		if with["ticket"] != ticket {
			t.Errorf("/v1/team/moved with_ticket by %s: the answer does not carry the ticket the owner recorded", who)
		}
	}
	if got := h.post(carol.credential, "/v1/team/moved", asked); got.raw != reference.raw {
		t.Errorf("/v1/team/moved by a principal of another team, once a move is recorded: %s, want the uniform unauthorized", got.raw)
	}

	// Nothing is refused for the record: a session registers, the secret
	// joins, and a member that leaves reads nothing from then on.
	h.register(bob, "beta")
	dave := h.join(alice.secret)
	if got := h.must(dave.credential, "/v1/team/moved", team); got["moved"] != true {
		t.Errorf("a member that joined after the record reads %v, want the record", got)
	}
	h.must(dave.credential, "/v1/team/leave", team)
	if got := h.post(dave.credential, "/v1/team/moved", team); got.raw != reference.raw {
		t.Errorf("/v1/team/moved by a member that left: %s, want the uniform unauthorized", got.raw)
	}

	// A second record replaces the first, with its certificate.
	replaced := h.must(alice.credential, "/v1/team/move", moveBody(alice.team, "https://other.example.com", ticket, `,"certificate":"sha256:00ff"`))
	if replaced["url"] != "https://other.example.com" || replaced["certificate"] != "sha256:00ff" {
		t.Errorf("a second record: %v, want the new destination and its certificate", replaced)
	}
	if got := h.must(bob.credential, "/v1/team/moved", team); got["url"] != "https://other.example.com" || got["certificate"] != "sha256:00ff" {
		t.Errorf("/v1/team/moved after a second record: %v", got)
	}

	// The team's next owner has the record to clear, and the one before it
	// has not; a clear is safe to repeat.
	h.must(alice.credential, "/v1/team/transfer", `{"team_ref":"`+alice.team+`","principal_ref":"`+bob.principal+`"}`)
	if got := h.post(alice.credential, "/v1/team/move", clearIt); got.raw != reference.raw {
		t.Errorf("a clear by the owner before a transfer: %s, want the uniform unauthorized", got.raw)
	}
	for range 2 {
		cleared := h.must(bob.credential, "/v1/team/move", clearIt)
		if len(cleared) != 2 || cleared["moved"] != false {
			t.Errorf("/v1/team/move clear: %v, want the team and moved false alone", cleared)
		}
	}
	if got := h.must(alice.credential, "/v1/team/moved", asked); len(got) != 2 || got["moved"] != false {
		t.Errorf("/v1/team/moved after the clear: %v", got)
	}
}
