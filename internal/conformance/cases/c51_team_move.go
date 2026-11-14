package cases

import (
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c51TeamMove (cap team.move): on a team of the case's own, O its owner and
// M a member. `team move` with no --ticket-file, with one that is not an
// absolute path, and with --clear beside another flag is `usage`; a ticket
// file that is not there is `config` with details.reason
// ticket_file_missing; a file that holds no join secret, and one that holds
// a join secret of another team, is `invalid_input` naming ticket; a
// destination with no url is `invalid_input` naming url; M, who is not the
// owner, is `unauthorized` for a record and, byte for byte, for a clear;
// and none of these records anything, because `team moved` still answers
// moved false. O's record answers {team_ref, moved true, adapter, url,
// recorded_at} and no certificate; a second record, as flags with a
// certificate, replaces the first, and M's `team moved` reads it. O's
// --clear answers {team_ref, moved false}, as does a second one, and then
// `team moved` answers the same. The ticket is on neither stdout nor
// stderr of any command (the launcher searches every spawn for it), and
// the record is cleared before the case ends, so nothing of it is left in
// the run directory (4.4.10).
func c51TeamMove() conformance.Case {
	return conformance.Case{
		ID:    "C-51",
		Rule:  "4.4.10 team move",
		Title: "the owner alone records where the team went, with a ticket read from a file and in no output; a second record replaces the first and --clear removes it",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.move")},
		Run:   runC51,
	}
}

// relativeTicketFile is the --ticket-file C-51 and C-52 give that is not an
// absolute path, which an adapter must refuse.
const relativeTicketFile = "brigade-conformance-relative-ticket"

// The destination the move cases record: a Brigade server that does not
// exist, under a name no test resolves.
const (
	moveAdapter     = "rest"
	moveURL         = "https://moved.brigade.example"
	moveOtherURL    = "https://moved-again.brigade.example"
	moveCertificate = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

// moveRunner is the three commands of a team's move as a case runs them.
type moveRunner struct {
	t       *conformance.T
	teamRef string
}

func (m moveRunner) move(p *conformance.Principal, stdin *protocol.TeamMoveRequest, args ...string) *conformance.Result {
	var doc []byte
	if stdin != nil {
		doc = mustMarshal(m.t, stdin)
	}
	return m.t.Exec(p, doc, append([]string{"team", "move"}, args...)...)
}

func (m moveRunner) moved(p *conformance.Principal, args ...string) *conformance.Result {
	return m.t.Exec(p, nil, append([]string{"team", "moved"}, args...)...)
}

// none asserts the answer of a team with no move recorded: the team, moved
// false, and no other member.
func (m moveRunner) none(what string, r *conformance.Result) {
	var res protocol.TeamMoveResult
	m.t.OK(r, &res)
	if res.TeamRef != m.teamRef || res.Moved {
		m.t.Errorf("%s: {team_ref match %v, moved %v}; want the team and false (4.4.10)", what, res.TeamRef == m.teamRef, res.Moved)
	}
	if raw := m.t.OKRaw(r); len(raw) != 2 {
		m.t.Errorf("%s: %d members beside a move that is not recorded, want team_ref and moved alone (4.4.10)", what, len(raw))
	}
}

// recorded asserts the answer of a recorded move and returns it.
func (m moveRunner) recorded(what string, r *conformance.Result, url, certificate string) *protocol.TeamMoveResult {
	var res protocol.TeamMoveResult
	m.t.OK(r, &res)
	if res.TeamRef != m.teamRef || !res.Moved || res.Adapter != moveAdapter || res.URL != url || res.Certificate != certificate {
		m.t.Errorf("%s: {team_ref match %v, moved %v, adapter %q, url match %v, certificate match %v}; want the team, true and the destination recorded (4.4.10)",
			what, res.TeamRef == m.teamRef, res.Moved, res.Adapter, res.URL == url, res.Certificate == certificate)
	}
	if _, has := m.t.OKRaw(r)["certificate"]; has != (certificate != "") {
		m.t.Errorf("%s: certificate present %v, want %v: it is present when the record has one (4.4.10)", what, has, certificate != "")
	}
	return &res
}

func runC51(t *conformance.T) {
	o, m, created := scratchTeam(t, "c51")
	run := moveRunner{t: t, teamRef: created.TeamRef}
	destination := &protocol.TeamMoveRequest{Adapter: moveAdapter, URL: moveURL}
	ticket := t.GiveSecretFile("ticket", syntheticSecret(created.TeamRef))

	// ---- what is refused before anything is recorded ----
	t.Fail(run.move(o, destination), protocol.CodeUsage)
	t.Fail(run.move(o, destination, "--ticket-file", relativeTicketFile), protocol.CodeUsage)
	t.Fail(run.move(o, nil, "--clear", "--ticket-file", ticket), protocol.CodeUsage)
	missing := filepath.Join(filepath.Dir(ticket), "c51-no-such-ticket")
	if e := t.Fail(run.move(o, destination, "--ticket-file", missing), protocol.CodeConfig); e.Details["reason"] != "ticket_file_missing" {
		t.Errorf("team move --ticket-file with a file that is not there: details.reason %q, want ticket_file_missing (4.4.10)", e.Details["reason"])
	}
	notATicket := t.SecretFile("not-a-ticket")
	if err := os.WriteFile(notATicket, []byte("this is not a move ticket\n"), 0o600); err != nil {
		t.Fatalf("write a file that holds no ticket: %v", err)
	}
	if e := t.Fail(run.move(o, destination, "--ticket-file", notATicket), protocol.CodeInvalidInput); e.Details["field"] != "ticket" {
		t.Errorf("team move with a file that holds no join secret: details.field %q, want ticket (4.4.10)", e.Details["field"])
	}
	foreign := t.GiveSecretFile("foreign-ticket", syntheticSecret(strangerLike(created.TeamRef)))
	if e := t.Fail(run.move(o, destination, "--ticket-file", foreign), protocol.CodeInvalidInput); e.Details["field"] != "ticket" {
		t.Errorf("team move with a join secret of another team: details.field %q, want ticket (4.4.10)", e.Details["field"])
	}
	if e := t.Fail(run.move(o, &protocol.TeamMoveRequest{Adapter: moveAdapter}, "--ticket-file", ticket), protocol.CodeInvalidInput); e.Details["field"] != "url" {
		t.Errorf("team move with no url: details.field %q, want url (4.4.10)", e.Details["field"])
	}

	// ---- the owner alone ----
	notOwner := run.move(m, destination, "--ticket-file", ticket)
	t.Fail(notOwner, protocol.CodeUnauthorized)
	notOwnerClear := run.move(m, nil, "--clear")
	t.Fail(notOwnerClear, protocol.CodeUnauthorized)
	t.SameBytes("team move by a member that is not the owner: a record vs a clear (4.4.10)", notOwner, notOwnerClear)
	run.none("team moved after every refusal", run.moved(o))

	// ---- a record, and one that replaces it ----
	first := run.recorded("team move", run.move(o, destination, "--ticket-file", ticket), moveURL, "")
	if first.RecordedAt.IsZero() || first.TicketFile != "" {
		t.Errorf("team move: {recorded_at zero %v, ticket_file present %v}; want a time and no ticket_file (4.4.10)",
			first.RecordedAt.IsZero(), first.TicketFile != "")
	}
	run.recorded("team move a second time, as flags", run.move(o, nil, "--ticket-file", ticket,
		"--adapter", moveAdapter, "--url", moveOtherURL, "--certificate", moveCertificate), moveOtherURL, moveCertificate)
	run.recorded("team moved by a member, after the second record", run.moved(m), moveOtherURL, moveCertificate)

	// ---- the owner clears it, as often as it likes ----
	run.none("team move --clear", run.move(o, nil, "--clear"))
	run.none("team move --clear a second time", run.move(o, nil, "--clear"))
	run.none("team moved after the clear", run.moved(m))
}
