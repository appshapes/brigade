package adapterkit

import (
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// The requests of the team move commands (4.4.10, capability team.move;
// work item 273, ADR 0003). Every adapter that carries them reads a request
// the same way and refuses a malformed one with the same words, as with the
// administration commands of teamadmin.go, so the reading is here, once.
// The move ticket is a secret: it travels in a file the caller names and
// never on argv, and nothing here puts any part of it in an error.

// maxTicketFileBytes bounds what ReadTicketFile reads: a ticket file is one
// short line, and anything past this is not one.
const maxTicketFileBytes = 64 << 10

// The details.reason values of a ticket file that cannot be used.
const (
	reasonTicketFileMissing    = "ticket_file_missing"
	reasonTicketFileUnreadable = "ticket_file_unreadable"
	reasonTicketFileUnwritable = "ticket_file_unwritable"
)

// TeamMoveFlags is the flag form of `team move`: `--ticket-file <path>`
// with the destination as `--adapter <name> --url <url> [--certificate
// <pin>]` or as the one stdin document, or `--clear` alone.
type TeamMoveFlags struct {
	set         *flag.FlagSet
	adapter     *string
	url         *string
	certificate *string
	ticketFile  *string
	clear       *bool
}

// NewTeamMoveFlags declares the five flags on fs, which the caller parses
// before it asks for the request.
func NewTeamMoveFlags(fs *flag.FlagSet) *TeamMoveFlags {
	return &TeamMoveFlags{
		set:         fs,
		adapter:     fs.String("adapter", "", "the adapter of the backend the team went to (instead of stdin)"),
		url:         fs.String("url", "", "the url of the backend the team went to (instead of stdin)"),
		certificate: fs.String("certificate", "", "with --url: the certificate pin of that backend, when its team file names one"),
		ticketFile:  fs.String("ticket-file", "", "the file that holds the move ticket; required unless --clear"),
		clear:       fs.Bool("clear", false, "clear the recorded move"),
	}
}

// A TeamMove is one parsed `team move`: either Clear, or a destination and
// the ticket that admits a member there.
type TeamMove struct {
	Clear       bool
	Destination *protocol.TeamMoveRequest
	// Ticket is the move ticket as the file held it. It goes to the backend
	// and to nothing else: no result, no log line, no error.
	Ticket string
}

// Request resolves a parsed `team move`. `--clear` goes with no other
// flag and reads nothing. Otherwise `--ticket-file` is required and names
// an absolute path, the destination is the flags when one of them was
// given and the one stdin document when none was, and the ticket is read
// from the file last, once everything else is in order. Argv that breaks a
// rule is `usage`; a destination that breaks one is `invalid_input` naming
// the member; a ticket file that is not there or cannot be read is
// `config`; and a file that holds no ticket is `invalid_input` with
// details.field `ticket`.
func (f *TeamMoveFlags) Request(stdin io.Reader) (*TeamMove, error) {
	seen := flagsSeen(f.set)
	if *f.clear {
		if len(seen) > 1 {
			return nil, usageError("--clear clears the recorded move and goes with no other flag")
		}
		return &TeamMove{Clear: true}, nil
	}
	if *f.ticketFile == "" {
		return nil, usageError("--ticket-file <path> is required: the move ticket is read from a file and never passed on the command line")
	}
	if !filepath.IsAbs(*f.ticketFile) {
		return nil, usageError("--ticket-file must be an absolute path")
	}
	destination := &protocol.TeamMoveRequest{}
	if seen["adapter"] || seen["url"] || seen["certificate"] {
		if *f.adapter == "" || *f.url == "" {
			return nil, usageError("--adapter and --url go together, each with a value; --certificate is optional beside them")
		}
		destination.Adapter, destination.URL, destination.Certificate = *f.adapter, *f.url, *f.certificate
		if err := destination.Validate(); err != nil {
			return nil, err
		}
	} else {
		data, err := ReadInput(stdin)
		if err != nil {
			return nil, err
		}
		if err := protocol.Decode(data, destination); err != nil {
			return nil, err
		}
	}
	ticket, err := ReadTicketFile(*f.ticketFile)
	if err != nil {
		return nil, err
	}
	return &TeamMove{Destination: destination, Ticket: ticket}, nil
}

// ReadTicketFile reads a move ticket from the file `team move` was given:
// its first line, trimmed. Like a join secret's file, where it is kept and
// with what mode is its holder's own business. The ticket must have the
// form of a join secret, which is what it is at the backend the team went
// to; a file that holds anything else is refused with a message that
// carries no part of it.
func ReadTicketFile(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // G304: the caller's own --ticket-file, named on their own argv
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ticketFileError(reasonTicketFileMissing, "the file named by --ticket-file does not exist")
		}
		return "", ticketFileError(reasonTicketFileUnreadable, "the file named by --ticket-file could not be read")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxTicketFileBytes+1))
	if err != nil {
		return "", ticketFileError(reasonTicketFileUnreadable, "the file named by --ticket-file could not be read")
	}
	if len(data) > maxTicketFileBytes {
		return "", errNoTicket()
	}
	first, _, _ := strings.Cut(string(data), "\n")
	ticket := strings.TrimSpace(first)
	if _, err := protocol.ParseJoinSecret(ticket); err != nil {
		return "", errNoTicket()
	}
	return ticket, nil
}

// CheckMoveTicket checks a ticket against the team it is recorded for: a
// ticket is a join secret of that team at its new backend, so the team
// reference inside it is the team's. One that names another team admits
// nobody there, and recording it would only send every member's session to
// a refusal.
func CheckMoveTicket(ticket, teamRef string) error {
	parsed, err := protocol.ParseJoinSecret(ticket)
	if err != nil || parsed.TeamRef() != teamRef {
		return errNoTicket()
	}
	return nil
}

// CheckTicketFileTarget checks the path `team moved --ticket-file` is to
// write a ticket to, before any network call: absolute, in a directory
// that exists. An empty path is no path and passes.
func CheckTicketFileTarget(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		return usageError("--ticket-file must be an absolute path")
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return ticketFileError(reasonTicketFileUnwritable, "the directory of --ticket-file does not exist")
	}
	return nil
}

// WriteTicketFile writes a ticket to path for its owner alone (0600,
// atomically), a newline after it.
func WriteTicketFile(path, ticket string) error {
	if err := WriteAtomic(path, []byte(ticket+"\n")); err != nil {
		return ticketFileError(reasonTicketFileUnwritable, "the file named by --ticket-file could not be written")
	}
	return nil
}

// errNoTicket refuses a file, or a backend's answer, that holds no move
// ticket of the team. Fixed text: the ticket is a secret.
func errNoTicket() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: "the move ticket is not one of this team: expected the file the server's operator wrote when the move was opened",
		Details: map[string]string{"field": "ticket", "reason": "invalid_value"},
	}
}

// ticketFileError is one refusal of a ticket file that cannot be used as a
// file: `config`, with fixed text.
func ticketFileError(reason, message string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeConfig, Message: message, Details: map[string]string{"reason": reason}}
}
