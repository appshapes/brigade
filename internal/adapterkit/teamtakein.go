package adapterkit

import (
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// The requests of the commands of capability team.take_in (4.4.10; work
// item 328, ADR 0006): `team take-in` and `team open-move`. `team
// close-move` takes nothing. Every adapter that carries them reads a
// request the same way and refuses a malformed one with the same words, as
// with the commands of teamadmin.go and teammove.go, so the reading is
// here, once. The join secret is a secret: it comes from the stdin
// document or from a file the caller names, never from argv, and nothing
// here puts any part of it in an error.

// maxSecretFileBytes bounds what ReadSecretFile reads: a secret file is
// one short line, and anything past this is not one.
const maxSecretFileBytes = 64 << 10

// The details.reason values of a secret file that cannot be read.
const (
	reasonSecretFileMissing    = "secret_file_missing"    //nolint:gosec // G101: a details.reason token, not a credential
	reasonSecretFileUnreadable = "secret_file_unreadable" //nolint:gosec // G101: a details.reason token, not a credential
)

// TeamTakeInFlags is the flag form of `team take-in`: `--secret-file
// <path>`, `--name <team_name>` and `--label <human_label>`, each of which
// takes the place of a member of the stdin document.
type TeamTakeInFlags struct {
	set        *flag.FlagSet
	secretFile *string
	name       *string
	label      *string
}

// NewTeamTakeInFlags declares the three flags on fs, which the caller
// parses before it asks for the request.
func NewTeamTakeInFlags(fs *flag.FlagSet) *TeamTakeInFlags {
	return &TeamTakeInFlags{
		set:        fs,
		secretFile: fs.String("secret-file", "", "the file whose first line is the team's join secret (instead of the document's join_secret)"),
		name:       fs.String("name", "", "the name the team gets here (instead of the document's team_name)"),
		label:      fs.String("label", "", "this member's human label"),
	}
}

// A TeamTakeIn is one parsed `team take-in`: the request as it was
// validated, and the join secret it carries, parsed.
type TeamTakeIn struct {
	Request *protocol.TeamTakeInRequest
	// Secret is the team's join secret. It goes to the backend and to
	// nothing else: no result, no log line, no error.
	Secret protocol.JoinSecret
}

// Request resolves a parsed `team take-in`. The one stdin document is read
// unless both `--secret-file` and `--name` were given, which leave it
// nothing to say that the command needs; a flag that was given takes the
// place of the document's member. `--secret-file` names an absolute path
// whose first line is the join secret. Argv that breaks a rule is `usage`;
// a secret file that is not there or cannot be read is `config`; a request
// that breaks a rule of its shape is `invalid_input` naming the member;
// and a join secret that is not one, from the document or from the file,
// is `invalid_input` with details.field `join_secret` and no part of it.
func (f *TeamTakeInFlags) Request(stdin io.Reader) (*TeamTakeIn, error) {
	seen := flagsSeen(f.set)
	if seen["secret-file"] {
		if *f.secretFile == "" {
			return nil, usageError("--secret-file was given an empty value")
		}
		if !filepath.IsAbs(*f.secretFile) {
			return nil, usageError("--secret-file must be an absolute path")
		}
	}
	if seen["name"] && *f.name == "" {
		return nil, usageError("--name was given an empty value")
	}
	req := &protocol.TeamTakeInRequest{}
	if !seen["secret-file"] || !seen["name"] {
		data, err := ReadInput(stdin)
		if err != nil {
			return nil, err
		}
		if err := protocol.Unmarshal(data, req); err != nil {
			return nil, err
		}
	}
	if seen["name"] {
		req.TeamName = *f.name
	}
	if *f.label != "" {
		req.HumanLabel = *f.label
	}
	if seen["secret-file"] {
		secret, err := ReadSecretFile(*f.secretFile)
		if err != nil {
			return nil, err
		}
		req.JoinSecret = secret
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	secret, err := protocol.ParseJoinSecret(req.JoinSecret)
	if err != nil {
		return nil, err
	}
	return &TeamTakeIn{Request: req, Secret: secret}, nil
}

// ReadSecretFile reads a join secret from the file `team take-in` was
// given: its first line, trimmed. Where the file is kept and with what
// mode is its holder's own business, as for the file `team create` wrote
// it to. A file that holds no line is answered as an empty secret, which
// the request's own rules refuse; nothing of the file is in any error.
func ReadSecretFile(path string) (string, error) {
	file, err := os.Open(path) //nolint:gosec // G304: the caller's own --secret-file, named on their own argv
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", secretFileError(reasonSecretFileMissing, "the file named by --secret-file does not exist")
		}
		return "", secretFileError(reasonSecretFileUnreadable, "the file named by --secret-file could not be read")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
	if err != nil {
		return "", secretFileError(reasonSecretFileUnreadable, "the file named by --secret-file could not be read")
	}
	if len(data) > maxSecretFileBytes {
		// Not a secret file: answered as the malformed secret it would be.
		_, malformed := protocol.ParseJoinSecret("")
		return "", malformed
	}
	first, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(first), nil
}

// secretFileError is one refusal of a secret file that cannot be used as a
// file: `config`, with fixed text.
func secretFileError(reason, message string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeConfig, Message: message, Details: map[string]string{"reason": reason}}
}

// TeamOpenMoveFlags is the flag form of `team open-move`: `--ticket-file
// <path>` and `--ticket-days <n>`. The command takes no stdin.
type TeamOpenMoveFlags struct {
	set        *flag.FlagSet
	ticketFile *string
	ticketDays *int
}

// NewTeamOpenMoveFlags declares the two flags on fs, which the caller
// parses before it asks for the request.
func NewTeamOpenMoveFlags(fs *flag.FlagSet) *TeamOpenMoveFlags {
	return &TeamOpenMoveFlags{
		set:        fs,
		ticketFile: fs.String("ticket-file", "", "write the move ticket to this path (0600); required"),
		ticketDays: fs.Int("ticket-days", 0, "how many days the ticket admits for, 1 to 365 (30 when not given)"),
	}
}

// A TeamOpenMove is one parsed `team open-move`: where the ticket goes,
// and for how many days it admits. TicketDays is 0 when the caller named
// none, which leaves the number to the backend's default,
// protocol.MoveTicketDefaultDays.
type TeamOpenMove struct {
	TicketFile string
	TicketDays int
}

// Request resolves a parsed `team open-move`, before any network call:
// `--ticket-file` is required and names an absolute path, and
// `--ticket-days`, when given, is within 1..protocol.MoveTicketMaxDays,
// each `usage` otherwise; a directory that does not exist, or that takes
// no file, is `config` with details.reason ticket_file_unwritable
// (CheckTicketFileTarget): the move is not opened for a ticket that could
// not be kept.
func (f *TeamOpenMoveFlags) Request() (*TeamOpenMove, error) {
	if *f.ticketFile == "" {
		return nil, usageError("--ticket-file <path> is required: the move ticket is written to a file and never to stdout")
	}
	if !filepath.IsAbs(*f.ticketFile) {
		return nil, usageError("--ticket-file must be an absolute path")
	}
	open := &TeamOpenMove{TicketFile: *f.ticketFile}
	if flagsSeen(f.set)["ticket-days"] {
		if *f.ticketDays < 1 || *f.ticketDays > protocol.MoveTicketMaxDays {
			return nil, usageError("--ticket-days must be between 1 and " + strconv.Itoa(protocol.MoveTicketMaxDays))
		}
		open.TicketDays = *f.ticketDays
	}
	if err := CheckTicketFileTarget(*f.ticketFile); err != nil {
		return nil, err
	}
	return open, nil
}
