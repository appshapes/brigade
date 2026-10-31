package commands

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/protocol"
)

// RenameOptions are the flags of `brigade rename`.
type RenameOptions struct {
	// Clear drops a pending name; stdin is not read.
	Clear bool
}

// renameResult is the --json result of `brigade rename` (card 174).
type renameResult struct {
	SelfSessionID string `json:"self_session_id"`
	// Name is the name as recorded: cleaned, one line; "" for --clear.
	Name     string `json:"name"`
	Recorded bool   `json:"recorded"`
	Cleared  bool   `json:"cleared"`
	// Reason is set only when nothing was recorded: other_host (the
	// session's host has no hook that can set a title).
	Reason string `json:"reason,omitzero"`
	Note   string `json:"note"`
}

// The fixed texts of `brigade rename` (card 174) the tests assert word
// for word.
const (
	// RenameUsage is the whole `usage` message for positional arguments:
	// the name travels on stdin as a doing line does, so a shell never
	// expands it and ps never shows it.
	RenameUsage = "rename takes no arguments: the name travels on stdin in a quoted heredoc (<<'EOF'), and --clear drops a pending name"
	// RenameRecordedSuffix closes the human line of a recorded name;
	// RenameNote is the note of its --json result.
	RenameRecordedSuffix = " (Claude Code applies it at your user's next prompt; the team roster follows)"
	RenameNote           = "recorded for the prompt hook; Claude Code names the session when your user next submits a prompt, and the team roster follows within a heartbeat"
	// RenameClearedLine and RenameClearedNote answer --clear.
	RenameClearedLine = "cleared: no name is pending; the session keeps the name it has"
	RenameClearedNote = "the pending name was dropped; the session keeps the name it has"
	// RenameNotRecordedLine is printed, at exit 0, on a host whose hooks
	// cannot set a session title (Muse Code, Codex CLI, Grok Build). It
	// names no option and no setting, as DoingNotPublishedLine does not.
	RenameNotRecordedLine = "not recorded: this host cannot name its session from inside it; the session keeps the name its host gave it. Carry on with the work."
	// RenameReasonOtherHost is the --json reason of that line.
	RenameReasonOtherHost = "other_host"
	// renameField names the stdin text in the refusals' details.
	renameField = "name"
)

// Rename implements `brigade rename [--clear] [--json]` (card 174): one
// line on stdin, the name this Claude Code session should carry, recorded
// as a pending name the next `brigade hook prompt` answers Claude Code
// with as its output's sessionTitle — the same effect as the user typing
// /rename — after which Claude Code writes the name to its session
// registry and the watcher carries it to the team roster. Nothing goes to
// the backend and nothing is spawned: no describe, no adapter. The order
// is `note`'s: the text is read and cleaned (doing.CleanText: valid
// UTF-8, sanitised, one line, non-empty, at most
// config.MaxSessionNameChars, no credential shape, no local path) BEFORE
// the session is resolved. A host other than Claude Code records nothing
// and says so at exit 0: its hooks have no field that sets a title.
func Rename(inv Invocation, opts RenameOptions) error {
	if len(inv.Args) != 0 {
		return usage(RenameUsage)
	}
	name := ""
	if !opts.Clear {
		raw, err := inv.readLine(renameField, config.MaxSessionNameChars)
		if err != nil {
			return err
		}
		cleaned, reason := doing.CleanText(raw, adapterkit.HomeDir(inv.Environ), config.MaxSessionNameChars, adapterkit.Getenv(inv.Environ, envMessagingToken))
		if reason != "" {
			return textError(renameField, reason, config.MaxSessionNameChars)
		}
		name = cleaned
	}

	if !inv.inSession() {
		return notInSession("rename")
	}
	m, stateDir, err := inv.sessionMap()
	if err != nil {
		return err
	}
	if m.Harness != "" && m.Harness != string(config.HostClaude) {
		if inv.JSON {
			return writeJSON(inv.Out, renameResult{SelfSessionID: m.BrigadeSessionID, Name: name, Reason: RenameReasonOtherHost, Note: RenameNotRecordedLine})
		}
		return writeLines(inv.Out, RenameNotRecordedLine)
	}

	path := config.RenameStamp(stateDir, m.ClaudePID)
	if opts.Clear {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return &protocol.Error{Code: protocol.CodeInternal, Message: "the pending name could not be dropped"}
		}
		inv.logger().Debug("pending name cleared")
		if inv.JSON {
			return writeJSON(inv.Out, renameResult{SelfSessionID: m.BrigadeSessionID, Cleared: true, Note: RenameClearedNote})
		}
		return writeLines(inv.Out, RenameClearedLine)
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the state directory for the pending name could not be created"}
	}
	if err := adapterkit.WriteAtomic(path, []byte(m.ClaudeSessionID+" "+name+"\n")); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the pending name could not be recorded for the prompt hook"}
	}
	inv.logger().Debug("pending name recorded")
	if inv.JSON {
		return writeJSON(inv.Out, renameResult{SelfSessionID: m.BrigadeSessionID, Name: name, Recorded: true, Note: RenameNote})
	}
	return writeLines(inv.Out, "recorded: "+name+RenameRecordedSuffix)
}
