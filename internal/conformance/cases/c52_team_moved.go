package cases

import (
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c52TeamMoved (cap team.move): on a team of the case's own, O its owner
// and M a member. With no move recorded `team moved` answers {team_ref,
// moved false} to both, and with --ticket-file it writes no file. A
// --ticket-file that is not an absolute path is `usage`, and one in a
// directory that does not exist is `config` with details.reason
// ticket_file_unwritable, each before anything is read. A principal of
// another team whose profile names this team is `unauthorized`,
// byte-identical to its answer for a team that does not exist. Once O has
// recorded a move, M's `team moved` answers the destination and no
// ticket_file, and writes nothing; with --ticket-file it answers that path
// as ticket_file, and the file is there, its mode exactly 0600 where the
// platform has modes, and holds the ticket O recorded. The ticket is on no
// stdout and no stderr. The record is cleared before the case ends
// (4.4.10).
func c52TeamMoved() conformance.Case {
	return conformance.Case{
		ID:    "C-52",
		Rule:  "4.4.10 team moved",
		Title: "every active member reads where the team went; the ticket goes to the file named, for its owner alone, and to no output; nobody else reads anything",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.move")},
		Run:   runC52,
	}
}

func runC52(t *conformance.T) {
	o, m, created := scratchTeam(t, "c52")
	run := moveRunner{t: t, teamRef: created.TeamRef}

	// ---- no move recorded ----
	run.none("team moved by the owner, nothing recorded", run.moved(o))
	unwritten := t.SecretFile("unwritten")
	run.none("team moved --ticket-file by a member, nothing recorded", run.moved(m, "--ticket-file", unwritten))
	if _, err := os.Stat(unwritten); err == nil {
		t.Errorf("team moved --ticket-file with no move recorded: a file was written (4.4.10)")
	}

	// ---- the path is checked before anything is read ----
	t.Fail(run.moved(m, "--ticket-file", relativeTicketFile), protocol.CodeUsage)
	if os.Remove(relativeTicketFile) == nil {
		t.Errorf("team moved --ticket-file with a relative path: the file was written (4.4.10)")
	}
	nowhere := filepath.Join(filepath.Dir(unwritten), "c52-no-such-directory", "ticket")
	if e := t.Fail(run.moved(m, "--ticket-file", nowhere), protocol.CodeConfig); e.Details["reason"] != "ticket_file_unwritable" {
		t.Errorf("team moved --ticket-file in a directory that does not exist: details.reason %q, want ticket_file_unwritable (4.4.10)", e.Details["reason"])
	}

	// ---- the owner records a move ----
	ticket := syntheticSecret(created.TeamRef)
	given := t.GiveSecretFile("ticket", ticket)
	destination := &protocol.TeamMoveRequest{Adapter: moveAdapter, URL: moveURL, Certificate: moveCertificate}
	run.recorded("team move", run.move(o, destination, "--ticket-file", given), moveURL, moveCertificate)
	// From here on the store holds the ticket: it is cleared whatever the
	// rest of the case finds, so the end-of-run scan reads a store without it.
	defer func() { run.none("team move --clear at the end of the case", run.move(o, nil, "--clear")) }()

	// ---- nobody but an active member reads it ----
	c := t.C()
	t.Rebind(c, created.TeamRef)
	defer t.Restore(c)
	foreign := run.moved(c, "--ticket-file", unwritten)
	t.Fail(foreign, protocol.CodeUnauthorized)
	if _, err := os.Stat(unwritten); err == nil {
		t.Errorf("team moved --ticket-file by a principal of another team: a file was written (4.5.7)")
	}
	t.Rebind(c, strangerLike(created.TeamRef))
	unknown := run.moved(c, "--ticket-file", unwritten)
	t.Fail(unknown, protocol.CodeUnauthorized)
	t.SameBytes("team moved for a team the caller is not a member of vs a team that does not exist (4.5.7)", foreign, unknown)

	// ---- a member reads the record, and the ticket only into a file ----
	plain := run.recorded("team moved by a member", run.moved(m), moveURL, moveCertificate)
	if plain.TicketFile != "" || plain.RecordedAt.IsZero() {
		t.Errorf("team moved without --ticket-file: {ticket_file present %v, recorded_at zero %v}; want none and a time (4.4.10)",
			plain.TicketFile != "", plain.RecordedAt.IsZero())
	}
	path := t.SecretFile("read")
	read := run.recorded("team moved --ticket-file by a member", run.moved(m, "--ticket-file", path), moveURL, moveCertificate)
	if read.TicketFile != path {
		t.Errorf("team moved --ticket-file: ticket_file is not the path given (4.4.10)")
	}
	got, mode0600 := t.TakeSecretFile(path)
	if got != ticket {
		t.Errorf("team moved --ticket-file: the file does not hold the ticket the owner recorded (4.4.10)")
	}
	if !mode0600 {
		t.Errorf("team moved --ticket-file: the file's mode is not exactly 0600 (4.4.10)")
	}
}
