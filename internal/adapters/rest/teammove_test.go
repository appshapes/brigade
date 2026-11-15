//go:build !windows

package rest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapters/rest/stub"
	"github.com/appshapes/brigade/internal/protocol"
)

// The record of where a team went, through the adapter (4.4.10, capability
// team.move; the contract's section 3c; work item 341, ADR 0010). The
// conformance cases C-51 and C-52 hold the two commands to the protocol
// against the stand-in and against the built server; these tests hold what
// the suite cannot see: what is asked of the server and in what order, what
// a server that keeps no record is asked, and what an answer that is not
// this command's does.

// moveTicketHalf is the secret half of the ticket these tests record.
const moveTicketHalf = "the-secret-part-of-the-move-ticket"

// teamOf is the team a rig's profile is bound to.
func teamOf(r *rig) string {
	r.t.Helper()
	return str(r.t, r.ok("", "profile", "status"), "team_ref")
}

// ticketFile writes a move ticket of team to a file and returns its path.
func ticketFile(t *testing.T, team string) (path, ticket string) {
	t.Helper()
	ticket = secretOf(team, moveTicketHalf)
	path = filepath.Join(t.TempDir(), "ops.ticket")
	if err := os.WriteFile(path, []byte(ticket+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, ticket
}

// noTicketOnAStream fails when anything a rig's commands wrote holds the
// ticket or its secret half.
func noTicketOnAStream(t *testing.T, ticket string, rigs ...*rig) {
	t.Helper()
	for _, r := range rigs {
		if out := r.output(); strings.Contains(out, ticket) || strings.Contains(out, half(ticket)) {
			t.Errorf("a command's output holds the move ticket")
		}
	}
}

// TestTeamMoveAndMoved: the owner records where the team went with one
// request that carries the destination and the ticket from the file; a
// member reads it with one that asks for no ticket, and with a file to
// write to asks for it and writes it, mode 0600, to that file and to no
// stream; a member that is not the owner is refused a record and a clear
// in the same bytes; and --clear clears, as often as it is run.
func TestTeamMoveAndMoved(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	team := teamOf(p.alice)
	given, ticket := ticketFile(t, team)
	destination := `{"adapter":"rest","url":"https://brigade.example.com","unknown":1}`

	// No move recorded: the team and moved false alone, and no file.
	unwritten := filepath.Join(t.TempDir(), "unwritten.ticket")
	for _, args := range [][]string{{"team", "moved"}, {"team", "moved", "--ticket-file", unwritten}} {
		got := p.bob.ok("", args...)
		if len(got) != 2 || got["team_ref"] != team || got["moved"] != false {
			t.Errorf("%v with nothing recorded: %v, want the team and moved false alone", args, got)
		}
	}
	if _, err := os.Stat(unwritten); !os.IsNotExist(err) {
		t.Errorf("team moved --ticket-file with nothing recorded wrote the file (%v)", err)
	}

	// A member that is not the owner: the server's one refusal, for a
	// record and for a clear.
	notOwner := p.bob.fails(protocol.CodeUnauthorized, destination, "team", "move", "--ticket-file", given)
	notOwnerClear := p.bob.fails(protocol.CodeUnauthorized, "", "team", "move", "--clear")
	if notOwner.stdout != notOwnerClear.stdout {
		t.Errorf("a record and a clear by a member that is not the owner differ:\n%s\n%s", notOwner.stdout, notOwnerClear.stdout)
	}

	// The record: /v1/info, then one request with the profile's team, the
	// destination as it was validated and the ticket the file held.
	b.forget()
	recorded := p.alice.ok(destination, "team", "move", "--ticket-file", given)
	if len(recorded) != 5 || recorded["team_ref"] != team || recorded["moved"] != true || recorded["adapter"] != "rest" ||
		recorded["url"] != "https://brigade.example.com" || recorded["recorded_at"] == nil {
		t.Errorf("team move: %v, want team_ref, moved, adapter, url and recorded_at", recorded)
	}
	seen := b.seen("")
	if len(seen) != 2 || seen[0].Path != pathInfo || seen[1].Path != pathTeamMove {
		t.Fatalf("team move made %d requests, want /v1/info and then /v1/team/move", len(seen))
	}
	sent := seen[1].Body
	if len(sent) != 4 || sent["team_ref"] != team || sent["adapter"] != "rest" || sent["url"] != "https://brigade.example.com" || sent["ticket"] != ticket {
		t.Errorf("the record's request holds %d members, want team_ref, adapter, url and the ticket", len(sent))
	}

	// A member reads it: no ticket is asked for without a file to keep it in.
	b.forget()
	plain := p.bob.ok("", "team", "moved")
	if len(plain) != 5 || plain["url"] != "https://brigade.example.com" || plain["ticket_file"] != nil {
		t.Errorf("team moved: %v, want the record and no ticket_file", plain)
	}
	if asked := b.seen(pathTeamMoved); len(asked) != 1 || len(asked[0].Body) != 1 || asked[0].Body["team_ref"] != team {
		t.Errorf("team moved asked %v, want the team alone", asked)
	}
	// With a file it asks for the ticket and writes it there.
	b.forget()
	path := filepath.Join(t.TempDir(), "read.ticket")
	read := p.bob.ok("", "team", "moved", "--ticket-file", path)
	if read["ticket_file"] != path || read["url"] != "https://brigade.example.com" {
		t.Errorf("team moved --ticket-file: %v, want the record and the path", read)
	}
	if asked := b.seen(pathTeamMoved); len(asked) != 1 || asked[0].Body["with_ticket"] != true {
		t.Errorf("team moved --ticket-file did not ask for the ticket: %v", asked)
	}
	if got := ticketIn(t, path, team); got != ticket {
		t.Errorf("team moved --ticket-file: the file does not hold the ticket the owner recorded")
	}

	// A second record, as flags with a certificate, replaces the first.
	replaced := p.alice.ok("", "team", "move", "--ticket-file", given, "--adapter", "rest", "--url", "https://other.example.com",
		"--certificate", "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if replaced["url"] != "https://other.example.com" || replaced["certificate"] == nil {
		t.Errorf("a second record: %v, want the new destination and its certificate", replaced)
	}
	if got := p.bob.ok("", "team", "moved"); got["url"] != "https://other.example.com" || got["certificate"] != replaced["certificate"] {
		t.Errorf("team moved after a second record: %v", got)
	}

	// --clear, as often as it is run: one request, `clear` and the team.
	b.forget()
	for range 2 {
		cleared := p.alice.ok("", "team", "move", "--clear")
		if len(cleared) != 2 || cleared["team_ref"] != team || cleared["moved"] != false {
			t.Errorf("team move --clear: %v, want the team and moved false alone", cleared)
		}
	}
	if asked := b.seen(pathTeamMove); len(asked) != 2 || len(asked[0].Body) != 2 || asked[0].Body["clear"] != true {
		t.Errorf("team move --clear asked %v, want the team and clear", asked)
	}
	if got := p.bob.ok("", "team", "moved"); got["moved"] != false {
		t.Errorf("team moved after the clear: %v", got)
	}
	noTicketOnAStream(t, ticket, p.alice, p.bob)
}

// TestTeamMoveRefusals: what the adapter refuses itself, before any
// request; and an answer that is not this command's, which is `internal`
// and writes no file.
func TestTeamMoveRefusals(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	team := teamOf(p.alice)
	given, ticket := ticketFile(t, team)
	destination := `{"adapter":"rest","url":"https://brigade.example.com"}`

	b.forget()
	p.alice.fails(protocol.CodeUsage, destination, "team", "move")
	p.alice.fails(protocol.CodeUsage, destination, "team", "move", "--ticket-file", "relative.ticket")
	p.alice.fails(protocol.CodeUsage, "", "team", "move", "--clear", "--ticket-file", given)
	p.alice.fails(protocol.CodeUsage, "", "team", "moved", "--ticket-file", "relative.ticket")
	missing := p.alice.fails(protocol.CodeConfig, destination, "team", "move", "--ticket-file", filepath.Join(t.TempDir(), "none"))
	if details(t, missing.stdout)["reason"] != "ticket_file_missing" {
		t.Errorf("a ticket file that is not there: details %v", details(t, missing.stdout))
	}
	nowhere := p.alice.fails(protocol.CodeConfig, "", "team", "moved", "--ticket-file", filepath.Join(t.TempDir(), "none", "t"))
	if details(t, nowhere.stdout)["reason"] != reasonTicketFile {
		t.Errorf("a ticket file in a directory that is not there: details %v", details(t, nowhere.stdout))
	}
	noURL := p.alice.fails(protocol.CodeInvalidInput, `{"adapter":"rest"}`, "team", "move", "--ticket-file", given)
	if details(t, noURL.stdout)["field"] != "url" {
		t.Errorf("a destination with no url: details %v", details(t, noURL.stdout))
	}
	// A ticket of another team would admit nobody where the team went: it
	// is refused here, by the name of its member, before the server is
	// asked whether it keeps a record at all.
	foreign := filepath.Join(t.TempDir(), "foreign.ticket")
	if err := os.WriteFile(foreign, []byte(secretOf(elsewhereRef, moveTicketHalf)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := p.alice.fails(protocol.CodeInvalidInput, destination, "team", "move", "--ticket-file", foreign)
	if details(t, other.stdout)["field"] != "ticket" {
		t.Errorf("a ticket of another team: details %v", details(t, other.stdout))
	}
	if seen := b.seen(""); len(seen) != 0 {
		t.Errorf("the refusals made %d requests, want none", len(seen))
	}

	// An answer that names another record than the one that was given, and
	// a clear that is answered a record, are not this command's.
	envelope := func(result string) *canned {
		return &canned{status: http.StatusOK, body: `{"ok":true,"protocol_version":"1","result":` + result + `}`}
	}
	elsewhere := `{"team_ref":"` + team + `","moved":true,"adapter":"rest","url":"https://somewhere.else.example","recorded_at":"2026-11-15T12:00:00Z"`
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path != pathTeamMove {
			return nil
		}
		return envelope(elsewhere + `}`)
	})
	p.alice.fails(protocol.CodeInternal, destination, "team", "move", "--ticket-file", given)
	p.alice.fails(protocol.CodeInternal, "", "team", "move", "--clear")
	// A read that is answered a ticket of another team, a ticket beside a
	// team that did not move, and a record of another team: `internal`,
	// and nothing is written to the file.
	path := filepath.Join(t.TempDir(), "read.ticket")
	for what, result := range map[string]string{
		"a ticket of another team":       elsewhere + `,"ticket":"` + secretOf(elsewhereRef, moveTicketHalf) + `"}`,
		"a ticket with no move":          `{"team_ref":"` + team + `","moved":false,"ticket":"` + ticket + `"}`,
		"a record of another team":       `{"team_ref":"` + elsewhereRef + `","moved":false}`,
		"a record with no time":          `{"team_ref":"` + team + `","moved":true,"adapter":"rest","url":"u","ticket":"` + ticket + `"}`,
		"a record with no ticket for it": elsewhere + `}`,
	} {
		b.answerWith(func(r *seenRequest) *canned {
			if r.Path != pathTeamMoved {
				return nil
			}
			return envelope(result)
		})
		p.bob.fails(protocol.CodeInternal, "", "team", "moved", "--ticket-file", path)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: the ticket file was written (%v)", what, err)
		}
	}
	b.answerWith(nil)
	noTicketOnAStream(t, ticket, p.alice, p.bob)
	noTicketOnAStream(t, secretOf(elsewhereRef, moveTicketHalf), p.alice, p.bob)
}

// TestAServerThatKeepsNoRecordOfAMove: a server that does not list
// team.move, which is every server before 0.8.0, is asked /v1/info and
// nothing else. A read is answered as a team that did not move, as 4.4.10
// has it for a backend that cannot say, and writes no file; a record and a
// clear are `config` server_lacks_team_move, in words that say what is
// missing and whose it is to mend. describe names the capability either
// way, because it never asks the server.
func TestAServerThatKeepsNoRecordOfAMove(t *testing.T) {
	t.Parallel()
	b := newBackendWith(t, stub.Options{WithoutTeamMove: true})
	p := newPair(t, b)
	team := teamOf(p.alice)
	given, ticket := ticketFile(t, team)
	path := filepath.Join(t.TempDir(), "read.ticket")

	for _, args := range [][]string{{"team", "moved"}, {"team", "moved", "--ticket-file", path}} {
		b.forget()
		got := p.bob.ok("", args...)
		if len(got) != 2 || got["team_ref"] != team || got["moved"] != false {
			t.Errorf("%v on a server that keeps no record: %v, want the team and moved false alone", args, got)
		}
		if seen := b.seen(""); len(seen) != 1 || seen[0].Path != pathInfo {
			t.Errorf("%v made %d requests, want the one to /v1/info", args, len(seen))
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("team moved --ticket-file on a server that keeps no record wrote the file (%v)", err)
	}
	for _, tc := range []struct {
		input string
		args  []string
	}{
		{`{"adapter":"rest","url":"https://brigade.example.com"}`, []string{"team", "move", "--ticket-file", given}},
		{"", []string{"team", "move", "--clear"}},
	} {
		b.forget()
		got := p.alice.fails(protocol.CodeConfig, tc.input, tc.args...)
		if details(t, got.stdout)["reason"] != reasonServerLacksTeamMove {
			t.Errorf("%v: details %v, want reason %s", tc.args, details(t, got.stdout), reasonServerLacksTeamMove)
		}
		if !strings.Contains(got.stdout, "keeps no record of where a team goes") || !strings.Contains(got.stdout, "0.8.0") || !strings.Contains(got.stdout, "operator") {
			t.Errorf("%v: the message does not say what is missing and whose it is to mend: %s", tc.args, got.stdout)
		}
		if seen := b.seen(""); len(seen) != 1 || seen[0].Path != pathInfo {
			t.Errorf("%v made %d requests, want the one to /v1/info", tc.args, len(seen))
		}
	}
	capabilities, _ := p.alice.ok("", "describe")["capabilities"].([]any)
	named := false
	for _, c := range capabilities {
		named = named || c == capTeamMove
	}
	if !named {
		t.Errorf("describe does not name %s; it never asks the server, so it names it either way", capTeamMove)
	}
	noTicketOnAStream(t, ticket, p.alice, p.bob)
}
