package cases

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c54TeamOpenMove (cap team.take_in): on a team the case took in itself, O
// its owner and M a member. `team open-move` with no --ticket-file, with
// one that is not an absolute path, and with --ticket-days 0 or 366 is
// `usage`; a --ticket-file in a directory that does not exist is `config`
// with details.reason ticket_file_unwritable; M, who is not the owner, is
// `unauthorized`; and none of them writes a file or opens a move, because
// O's open-move that follows answers replaced false. That open-move
// answers {team_ref, team_name, ends_at, replaced, ticket_file} and no
// ticket, with ends_at thirty days away; the ticket is in the file (whose
// mode is exactly 0600 where the platform has modes), is a join secret of
// the 4.4.10 format with the team's team_ref, is not the join secret, and
// appears on neither stdout nor stderr. A principal that presents the
// ticket comes in as a member that does not own: its take-in answers
// taken_in false and owner false, its own open-move is `unauthorized`,
// and where team.join is advertised a `team join` with the ticket joins
// the team. A second open-move, with --ticket-days 7, answers replaced
// true and an ends_at seven days away; the first ticket then admits
// nobody, and the second does. The move is closed before the case ends
// (4.4.10).
func c54TeamOpenMove() conformance.Case {
	return conformance.Case{
		ID:    "C-54",
		Rule:  "4.4.10 team open-move",
		Title: "the owner's open-move puts a move ticket in the file and in no output; the ticket admits a member that never owns, until another open-move takes its place",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.take_in")},
		Run:   runC54,
	}
}

// relativeOpenMoveTicketFile is the --ticket-file C-54 gives that is not an
// absolute path, which an adapter must refuse.
const relativeOpenMoveTicketFile = "brigade-conformance-c54-relative-ticket"

// day is the unit a move ticket's life is counted in.
const day = 24 * time.Hour

// openMove runs `team open-move` for p.
func openMove(t *conformance.T, p *conformance.Principal, args ...string) *conformance.Result {
	return t.Exec(p, nil, append([]string{"team", "open-move"}, args...)...)
}

// endsInDays asserts that a move's ends_at is the given number of days
// from now, a day either way: the suite's clock is not the backend's.
func endsInDays(t *conformance.T, what string, endsAt time.Time, days int) {
	if left := endsAt.Sub(t.Now()); left < time.Duration(days-1)*day || left > time.Duration(days+1)*day {
		t.Errorf("%s: ends_at is %s from now, want %d days (4.4.10)", what, left.Round(time.Hour), days)
	}
}

// ticketRefused asserts that ticket admits nobody at the team: a fresh
// principal's take-in with it is the conflict of another secret, and where
// team.join is advertised its `team join` is the rejected join, byte for
// byte what a secret that was never issued gets.
func ticketRefused(t *conformance.T, team *takenTeam, what, scratch, ticket string) {
	p := t.Scratch(scratch)
	if e := t.Fail(t.Exec(p, takeInDocument(t, ticket, team.name, ""), "team", "take-in"), protocol.CodeConflict); e.Details["reason"] != "other_join_secret" {
		t.Errorf("team take-in with %s: details.reason %q, want other_join_secret (4.4.10)", what, e.Details["reason"])
	}
	if !t.HasCap("team.join") {
		return
	}
	dead := t.Exec(p, joinDocument(t, ticket), "team", "join")
	t.Fail(dead, protocol.CodeUnauthorized)
	never := t.Exec(p, joinDocument(t, syntheticSecret(team.teamRef)), "team", "join")
	t.Fail(never, protocol.CodeUnauthorized)
	t.SameBytes("team join with "+what+" vs with a secret that was never issued (4.4.10)", dead, never)
}

func runC54(t *conformance.T) {
	team := takenInTeam(t, "c54")
	o, m := team.owner, team.member
	// Whatever the case finds, it leaves no move open behind it.
	defer func() { _ = t.Exec(o, nil, "team", "close-move") }()

	// ---- what is refused before a move is opened ----
	noFile := openMove(t, o)
	t.Fail(noFile, protocol.CodeUsage)
	t.Fail(openMove(t, o, "--ticket-file", relativeOpenMoveTicketFile), protocol.CodeUsage)
	if os.Remove(relativeOpenMoveTicketFile) == nil {
		t.Errorf("team open-move --ticket-file with a relative path: the file was written (4.4.10)")
	}
	path := t.SecretFile("ticket")
	for _, days := range []string{"0", "366"} {
		t.Fail(openMove(t, o, "--ticket-file", path, "--ticket-days", days), protocol.CodeUsage)
	}
	nowhere := filepath.Join(filepath.Dir(path), "c54-no-such-directory", "ticket")
	if e := t.Fail(openMove(t, o, "--ticket-file", nowhere), protocol.CodeConfig); e.Details["reason"] != "ticket_file_unwritable" {
		t.Errorf("team open-move --ticket-file in a directory that does not exist: details.reason %q, want ticket_file_unwritable (4.4.10)", e.Details["reason"])
	}
	refused := t.SecretFile("member")
	notOwner := openMove(t, m, "--ticket-file", refused)
	t.Fail(notOwner, protocol.CodeUnauthorized)
	t.DifferentBytes("unauthorized vs usage (positive control)", notOwner, noFile)
	for _, written := range []string{path, refused} {
		if _, err := os.Stat(written); err == nil {
			t.Errorf("a refused team open-move wrote the ticket file (4.4.10)")
		}
	}

	// ---- the owner opens the move ----
	r := openMove(t, o, "--ticket-file", path)
	var opened protocol.TeamOpenMoveResult
	t.OK(r, &opened)
	result := t.OKRaw(r)
	for _, key := range []string{"team_ref", "team_name", "ends_at", "replaced", "ticket_file"} {
		if _, has := result[key]; !has {
			t.Errorf("team open-move: result has no `%s`; all five members are required (4.4.10)", key)
		}
	}
	if _, has := result["ticket"]; has {
		t.Errorf("team open-move: result has a `ticket` member; the ticket goes to the file and to no output (4.4.10)")
	}
	if opened.TeamRef != team.teamRef || opened.TeamName != team.name || opened.Replaced || opened.TicketFile != path {
		t.Errorf("team open-move: {team_ref match %v, team_name match %v, replaced %v, ticket_file match %v}; want the team's own, false on a first open (a refused one opens nothing) and the path given (4.4.10)",
			opened.TeamRef == team.teamRef, opened.TeamName == team.name, opened.Replaced, opened.TicketFile == path)
	}
	endsInDays(t, "team open-move with no --ticket-days", opened.EndsAt, protocol.MoveTicketDefaultDays)
	ticket, mode0600 := t.TakeSecretFile(path)
	if !mode0600 {
		t.Errorf("team open-move: the ticket file is not a regular file of mode 0600 exactly (4.4.10)")
	}
	if parsed, err := protocol.ParseJoinSecret(ticket); err != nil {
		t.Errorf("team open-move: the ticket file does not hold one join secret of the 4.4.10 format")
	} else if parsed.TeamRef() != team.teamRef {
		t.Errorf("team open-move: the ticket names another team_ref; want the team's (4.4.10)")
	}
	if ticket == team.secret {
		t.Errorf("team open-move: the ticket in the file is the team's join secret (4.4.10)")
	}
	if ticket != "" && (bytes.Contains(r.Stdout, []byte(ticket)) || bytes.Contains(r.Stderr, []byte(ticket))) {
		t.Errorf("team open-move: the ticket is on stdout or stderr; it goes to the file and to no output (4.4.10, 4.5.14)")
	}

	// ---- the ticket admits a member, and never an owner ----
	f := t.Scratch("follower")
	followed := takeIn(t, f, ticket, team.name, "c54-follower@example.com")
	if followed.TakenIn || followed.Owner || followed.TeamRef != team.teamRef {
		t.Errorf("team take-in with the move ticket: {team_ref match %v, taken_in %v, owner %v}; want the team, false and false: a ticket never makes an owner (4.4.10)",
			followed.TeamRef == team.teamRef, followed.TakenIn, followed.Owner)
	}
	t.List(f, "", false)
	t.Fail(openMove(t, f, "--ticket-file", refused), protocol.CodeUnauthorized)
	if _, err := os.Stat(refused); err == nil {
		t.Errorf("team open-move by a member the ticket let in: the ticket file was written (4.4.10)")
	}
	if t.HasCap("team.join") {
		j := t.Scratch("joiner")
		if in := joinTeam(t, j, ticket, "c54-joiner@example.com"); in.TeamRef != team.teamRef || in.Rejoined {
			t.Errorf("team join with the move ticket: {team_ref match %v, rejoined %v}; want the team and false (4.4.10)",
				in.TeamRef == team.teamRef, in.Rejoined)
		}
		t.Fail(t.Exec(j, nil, "team", "close-move"), protocol.CodeUnauthorized)
	}

	// ---- a second open-move takes the place of the first ----
	again := t.SecretFile("again")
	var second protocol.TeamOpenMoveResult
	t.OK(openMove(t, o, "--ticket-file", again, "--ticket-days", "7"), &second)
	if !second.Replaced || second.TeamRef != team.teamRef || second.TicketFile != again {
		t.Errorf("a second team open-move: {team_ref match %v, replaced %v, ticket_file match %v}; want the team, true and the path given (4.4.10)",
			second.TeamRef == team.teamRef, second.Replaced, second.TicketFile == again)
	}
	endsInDays(t, "team open-move --ticket-days 7", second.EndsAt, 7)
	next, _ := t.TakeSecretFile(again)
	if next == ticket || next == team.secret {
		t.Errorf("a second team open-move: the ticket in the file is the one before it, or the join secret (4.4.10)")
	}
	ticketRefused(t, team, "a ticket another open-move replaced", "late", ticket)
	if in := takeIn(t, t.Scratch("second-follower"), next, team.name, ""); in.TakenIn || in.Owner || in.TeamRef != team.teamRef {
		t.Errorf("team take-in with the second ticket: {team_ref match %v, taken_in %v, owner %v}; want the team, false and false (4.4.10)",
			in.TeamRef == team.teamRef, in.TakenIn, in.Owner)
	}
	// Every member a ticket let in is still one.
	t.List(f, "", false)
}
