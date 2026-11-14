package fs

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The two commands of capability team.move (4.4.10; work item 273, ADR
// 0003). A team that leaves this store for another backend has the move
// recorded here by its owner: where it went, and the move ticket that
// admits a member there. An active member's `team moved` reads the record.
// The owner is the principal that created the team; this adapter has no
// transfer.

// moveFileName is <root>/teams/<team_ref>/move.json.
const moveFileName = "move.json"

// moveFile is the record of a move. The ticket is kept as it was given,
// because a member's adapter has to present it at the new backend: whoever
// can read the store can read it, as they can read every message in it
// (this adapter is test-only; README.md).
type moveFile struct {
	Adapter     string    `json:"adapter"`
	URL         string    `json:"url"`
	Certificate string    `json:"certificate,omitzero"`
	Ticket      string    `json:"ticket"`
	RecordedBy  string    `json:"recorded_by"`
	RecordedAt  time.Time `json:"recorded_at"`
}

func (s *store) movePath(team string) string { return filepath.Join(s.teamDir(team), moveFileName) }

// loadMove reads a team's move record. ok is false when there is none.
func (s *store) loadMove(team string) (*moveFile, bool, error) {
	if !safeRef(team) {
		return nil, false, nil
	}
	var m moveFile
	err := readJSON(s.movePath(team), &m)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &m, true, nil
}

// teamMove implements `team move` (4.4.10, C-51): the team's owner records
// where the team went, with the ticket from --ticket-file, or clears the
// record with --clear. The request is read and checked before the store is
// touched, and a caller that is not the team's owner is the uniform
// unauthorized, with the bytes a stranger to the team reads.
func (c *command) teamMove() (any, error) {
	fs := newFlags()
	flags := adapterkit.NewTeamMoveFlags(fs)
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	move, err := flags.Request(c.stdin)
	if err != nil {
		return nil, err
	}
	if err := c.teamScoped(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	team, ok, err := c.st.loadTeam(teamRef)
	if err != nil {
		return nil, err
	}
	if !ok || team.CreatedBy != c.cred.PrincipalRef {
		return nil, errNotMember()
	}
	if move.Clear {
		if err := os.Remove(c.st.movePath(teamRef)); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return nil, errInternal("the move record could not be removed")
		}
		return &protocol.TeamMoveResult{TeamRef: teamRef}, nil
	}
	if err := adapterkit.CheckMoveTicket(move.Ticket, teamRef); err != nil {
		return nil, err
	}
	now := c.now().UTC()
	record := &moveFile{
		Adapter: move.Destination.Adapter, URL: move.Destination.URL, Certificate: move.Destination.Certificate,
		Ticket: move.Ticket, RecordedBy: c.cred.PrincipalRef, RecordedAt: now,
	}
	if err := writeJSON(c.st.movePath(teamRef), record); err != nil {
		return nil, err
	}
	return &protocol.TeamMoveResult{
		TeamRef: teamRef, Moved: true, Adapter: record.Adapter, URL: record.URL, Certificate: record.Certificate, RecordedAt: now,
	}, nil
}

// teamMoved implements `team moved [--ticket-file <path>]` (4.4.10, C-52):
// an active member reads whether the team moved, and where. With
// --ticket-file the ticket of a recorded move is written to that path for
// its owner alone; it is in no output.
func (c *command) teamMoved() (any, error) {
	fs := newFlags()
	ticketFile := fs.String("ticket-file", "", "write the move ticket to this path (0600)")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if err := adapterkit.CheckTicketFileTarget(*ticketFile); err != nil {
		return nil, err
	}
	if err := c.teamScoped(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	record, ok, err := c.st.loadMove(teamRef)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &protocol.TeamMoveResult{TeamRef: teamRef}, nil
	}
	result := &protocol.TeamMoveResult{
		TeamRef: teamRef, Moved: true, Adapter: record.Adapter, URL: record.URL, Certificate: record.Certificate, RecordedAt: record.RecordedAt,
	}
	if *ticketFile != "" {
		if err := adapterkit.WriteTicketFile(*ticketFile, record.Ticket); err != nil {
			return nil, err
		}
		result.TicketFile = *ticketFile
	}
	return result, nil
}
