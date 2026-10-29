package commands

import (
	"errors"
	"io"
	"io/fs"
	"strconv"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// noteResult is the --json result of `brigade note` (card 162).
type noteResult struct {
	SelfSessionID string `json:"self_session_id"`
	// Text is the note as recorded: cleaned, one line.
	Text     string `json:"text"`
	Recorded bool   `json:"recorded"`
	Note     string `json:"note"`
}

// The fixed texts of `brigade note` (card 162, manager-digest plan §5.2).
const (
	// NoteUsage is the whole `usage` message for positional arguments.
	NoteUsage = "note takes no arguments: the line travels on stdin in a quoted heredoc (<<'EOF'), starting Done:, Next: or Blocked:"
	// NoteNote is the note of a --json result.
	NoteNote = "recorded in this session's state entry in the project's state folder, which syncs to every teammate's checkout and feeds the manager digest; nothing was sent to the backend"
	// NoteNoStateFolder is the `config` message of a project that shares
	// no state; NoteNoEntry that of a session whose entry is not there yet.
	NoteNoStateFolder = "this project shares no state folder, so there is nothing to record a note in: the team file's sync.state names one"
	NoteNoEntry       = "this session has no state entry yet (the next prompt creates it); run this again after your user's next prompt"
	// The details.reason values of those two.
	ReasonNoStateFolder = "no_state_folder"
	ReasonNoStateEntry  = "no_state_entry"
)

// Note implements `brigade note [--json]` (card 162, plan §5.2): one
// line about the work, on stdin, appended to the session's state entry
// — the folder's copy syncs to the teammates' checkouts and the digest
// reads it; nothing goes to the backend. The three labels the skill
// teaches (Done:, Next:, Blocked:) are a convention the digest reads,
// not a rule the command enforces. The text is cleaned as a doing line
// is (doing.CleanText: valid UTF-8, sanitised, one line, non-empty, at
// most state.MaxNoteChars, no credential shape, no local path) BEFORE
// anything is resolved, and the command spawns nothing at all: no
// describe, no adapter.
func Note(inv Invocation) error {
	if len(inv.Args) != 0 {
		return usage(NoteUsage)
	}
	raw, err := inv.readLine("note", state.MaxNoteChars)
	if err != nil {
		return err
	}
	text, reason := doing.CleanText(raw, adapterkit.HomeDir(inv.Environ), state.MaxNoteChars, adapterkit.Getenv(inv.Environ, envMessagingToken))
	if reason != "" {
		return textError("note", reason, state.MaxNoteChars)
	}
	if !inv.inSession() {
		return notInSession("note")
	}
	m, stateDir, err := inv.sessionMap()
	if err != nil {
		return err
	}
	s, err := stateStoreOf(m, stateDir)
	if err != nil {
		return err
	}
	e, err := s.Load(m.BrigadeSessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &protocol.Error{Code: protocol.CodeConfig, Message: NoteNoEntry, Details: map[string]string{"reason": ReasonNoStateEntry}}
		}
		return err
	}
	now := inv.Deps.now()
	e.AddNote(text, now)
	e.UpdatedAt = now
	if err := s.Save(e); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the note could not be written to the state entry"}
	}
	inv.logger().Debug("note recorded")
	if inv.JSON {
		return writeJSON(inv.Out, noteResult{SelfSessionID: m.BrigadeSessionID, Text: text, Recorded: true, Note: NoteNote})
	}
	return writeLines(inv.Out, "recorded: "+text)
}

// readLine reads one line of the digest's from stdin — a note, a
// question, an outcome — with readSentence's rules (a heredoc, never a
// terminal; at most one byte past doingReadLimit, refused rather than
// cut) and the field's own refusal.
func (inv Invocation) readLine(field string, limit int) (string, error) {
	if inv.In == nil {
		return "", textError(field, doing.ReasonEmpty, limit)
	}
	if inv.Deps.isTerminal(inv.In) {
		return "", usage("the " + field + " is read from stdin: use a quoted heredoc (<<'EOF')")
	}
	data, err := io.ReadAll(io.LimitReader(inv.In, doingReadLimit+1))
	if err != nil {
		return "", &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "the " + field + " could not be read",
			Details: map[string]string{"field": field, "reason": "read_error"},
		}
	}
	if len(data) > doingReadLimit {
		return "", textError(field, doing.ReasonTooLong, limit)
	}
	return string(data), nil
}

// sessionMap reads this session's by-pid map and the state directory,
// with no adapter spawned: for the commands that touch the state folder
// alone.
func (inv Invocation) sessionMap() (*sessionmap.ByPID, string, error) {
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return nil, "", err
	}
	m, err := config.Session(inv.Environ, stateDir)
	if err != nil {
		return nil, "", err
	}
	return m, stateDir, nil
}

// stateStoreOf is the session's entry store, or the `config` refusal of a
// project that shares no state.
func stateStoreOf(m *sessionmap.ByPID, stateDir string) (state.Store, error) {
	if m.StateFolder == "" {
		return state.Store{}, &protocol.Error{Code: protocol.CodeConfig, Message: NoteNoStateFolder, Details: map[string]string{"reason": ReasonNoStateFolder}}
	}
	return state.Store{StateDir: stateDir, Folder: m.StateFolder}, nil
}

// textError is the invalid_input failure of a refused line of the
// digest's (a note, a question, an option, an outcome): doing.CleanText's
// reason, the field, the cap. Fixed text per reason; the text is never
// part of it.
func textError(field, reason string, limit int) *protocol.Error {
	msg := map[string]string{
		doing.ReasonEmpty:        "the " + field + " is empty; put the text on stdin",
		doing.ReasonTooLong:      "the " + field + " is longer than " + strconv.Itoa(limit) + " characters; say it in fewer words",
		doing.ReasonNotUTF8:      "the " + field + " is not valid UTF-8",
		doing.ReasonSecretShaped: "the " + field + " looks like it carries a credential; leave the token out",
		doing.ReasonLocalPath:    "the " + field + " names a path on this machine; describe the work, not the file",
	}[reason]
	details := map[string]string{"field": field, "reason": reason}
	if reason == doing.ReasonTooLong {
		details["limit"] = strconv.Itoa(limit)
		details["unit"] = "codepoints"
	}
	return &protocol.Error{Code: protocol.CodeInvalidInput, Message: msg, Details: details}
}
