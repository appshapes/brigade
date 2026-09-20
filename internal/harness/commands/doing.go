package commands

import (
	"context"
	"io"
	"log/slog"
	"strconv"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/protocol"
)

// DoingOptions are the flags of `brigade doing`.
type DoingOptions struct {
	// Clear removes the line: the heartbeat carries "" and stdin is not
	// read.
	Clear bool
}

// doingResult is the --json result of `brigade doing` (plan 5.1).
type doingResult struct {
	SelfSessionID string `json:"self_session_id"`
	// SessionDescription is the text as sent — the cleaned sentence, or
	// "" for --clear — whether or not it was published.
	SessionDescription string `json:"session_description"`
	Published          bool   `json:"published"`
	Cleared            bool   `json:"cleared"`
	// Reason is set only when nothing was published: not_shared (the
	// session's mode is off) or unsupported (the adapter has no
	// session.description).
	Reason string `json:"reason,omitzero"`
	Note   string `json:"note"`
}

// The fixed texts of `brigade doing` (plan 5.1) the tests assert word for
// word.
const (
	// DoingNote is the note of a --json result that published, and the
	// sentence a human line does not need: the roster shows the line as
	// unverified text, and the model is the one who keeps it current.
	// DoingClearedNote
	// is the note of a --json result that cleared.
	DoingNote        = "published to the team roster as unverified text; replace it with brigade doing when the work changes, remove it with brigade doing --clear"
	DoingClearedNote = "cleared from the team roster; publish a new line with brigade doing when there is work to name"
	// DoingUsage is the whole `usage` message for positional arguments:
	// a double-quoted argv sentence is shell-expanded before Brigade sees
	// it (`running `make test`` would execute), and argv shows in ps.
	DoingUsage = "doing takes no arguments: the sentence travels on stdin in a quoted heredoc (<<'EOF'), and --clear removes it"
	// DoingNotPublishedLine is printed, at exit 0, when the session's
	// mode is off or unsupported. It names no option and no setting on
	// purpose: a non-zero exit or a named option invites a bypass-mode
	// model to "fix" it by editing the user's plugin configuration.
	DoingNotPublishedLine = "not published: this session does not publish a doing line. Carry on with the work."
	// DoingClearedLine confirms a --clear.
	DoingClearedLine = "cleared: teammates no longer see what this session is doing"
	// DoingFailedSuffix closes an adapter failure the model should not
	// retry; DoingRetrySuffix the one it may try once more (unavailable,
	// the harness's own timeout included).
	DoingFailedSuffix = "; nothing was published. It is housekeeping: carry on with the work"
	DoingRetrySuffix  = "; nothing was published; it can be tried once more later. Carry on with the work"
	// The --json reasons of a line that was not published.
	DoingReasonNotShared   = "not_shared"
	DoingReasonUnsupported = "unsupported"
	// doingReadLimit bounds what the verb reads from stdin: a sentence is
	// at most doing.MaxChars code points, so 4 KiB is generous, and one
	// byte past it is refused too_long rather than silently cut (a
	// LimitReader alone would publish half a sentence).
	doingReadLimit = 4096
	// envMessagingToken is the NAME of the inbox token variable (6.5),
	// read here only so the credential rule can refuse its exact value.
	envMessagingToken = "CLAUDE_CODE_MESSAGING_TOKEN" //nolint:gosec // G101: a variable NAME, not a credential
)

// Doing implements `brigade doing [--clear] [--json]` (card 25, plan 5.1):
// one `session heartbeat --session <self>` carrying ONLY
// session_description, so teammates' `brigade sessions` shows what this
// session is working on.
//
// The order of work is U-05's, as `send` does it: flags, then the text
// read and cleaned (doing.Clean: valid UTF-8, sanitised, folded, non-empty,
// at most doing.MaxChars code points, no credential shape, no local path)
// BEFORE anything is resolved or spawned — zero spawns on any refusal —
// then the session, then the mode gate the hook froze into the by-pid
// map, then the one call under adapterclient.RegisterTimeout (8 s; call()
// would give it the 20 s DefaultTimeout), with no retry and nothing in
// the background. The watcher is not involved and not told. There is no
// local copy of the text, no no-op-on-unchanged and no verb-side rate
// bound (ruling 12): a local copy can disagree with the backend and then
// every consumer of it states a falsehood nothing heals, and re-sending
// the same text is idempotent. The text is never logged.
func Doing(inv Invocation, opts DoingOptions) error {
	if len(inv.Args) != 0 {
		return usage(DoingUsage)
	}
	text := ""
	if !opts.Clear {
		raw, err := inv.readSentence()
		if err != nil {
			return err
		}
		// HOME and the messaging token come from inv.Environ through
		// adapterkit.Getenv, as account.go reads HOME (forbidigo bans
		// os.Getenv); the token is the one exact value the credential rule
		// knows besides its prefix formats.
		cleaned, reason := doing.Clean(raw, adapterkit.Getenv(inv.Environ, "HOME"), adapterkit.Getenv(inv.Environ, envMessagingToken))
		if reason != "" {
			return doingError(reason)
		}
		text = cleaned
	}

	if !inv.inSession() {
		return notInSession("doing")
	}
	t, err := inv.resolveSession("")
	if err != nil {
		return err
	}
	self := t.selfSessionID()

	// The mode gate (plan 5.2): the map's resolved word. Absent — a map
	// written before the mode existed — publishes; off refuses a publish
	// but honours --clear, so opting out can retract at once; unsupported
	// refuses both, because the adapter would refuse the member.
	if reason := doingRefusal(t.session.DoingMode, opts.Clear); reason != "" {
		if inv.JSON {
			return writeJSON(inv.Out, doingResult{SelfSessionID: self, SessionDescription: text, Reason: reason, Note: DoingNotPublishedLine})
		}
		return writeLines(inv.Out, DoingNotPublishedLine)
	}

	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.RegisterTimeout)
	defer cancel()
	_, err = t.client.Heartbeat(ctx, self, &protocol.HeartbeatRequest{SessionDescription: &text})
	if err != nil {
		return doingFailure(err)
	}
	inv.logger().Debug("doing line sent", slog.Bool("described", text != ""))

	if inv.JSON {
		note := DoingNote
		if opts.Clear {
			note = DoingClearedNote
		}
		return writeJSON(inv.Out, doingResult{
			SelfSessionID: self, SessionDescription: text,
			Published: !opts.Clear, Cleared: opts.Clear, Note: note,
		})
	}
	if opts.Clear {
		return writeLines(inv.Out, DoingClearedLine)
	}
	return writeLines(inv.Out, "published: "+text)
}

// doingRefusal answers the --json reason for which the mode publishes
// nothing, or "" when the call goes ahead.
func doingRefusal(mode string, clearing bool) string {
	switch mode {
	case doing.ModeUnsupported:
		return DoingReasonUnsupported
	case doing.ModeOff:
		if !clearing {
			return DoingReasonNotShared
		}
	}
	return ""
}

// readSentence reads the sentence from stdin, at most one byte past
// doingReadLimit so an oversize input costs bounded memory and is refused,
// never cut. It is readBody's sibling rather than a parameter of it: the
// body's messages and details are test-pinned, and a sentence is not a
// body (its cap is in code points, applied by doing.Clean after the
// sanitiser).
func (inv Invocation) readSentence() (string, error) {
	if inv.In == nil {
		return "", doingError(doing.ReasonEmpty)
	}
	if inv.Deps.isTerminal(inv.In) {
		return "", usage("the sentence is read from stdin: use a quoted heredoc (<<'EOF'), or --clear to remove the line")
	}
	data, err := io.ReadAll(io.LimitReader(inv.In, doingReadLimit+1))
	if err != nil {
		return "", &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "the sentence could not be read",
			Details: map[string]string{"field": "session_description", "reason": "read_error"},
		}
	}
	if len(data) > doingReadLimit {
		return "", doingError(doing.ReasonTooLong)
	}
	return string(data), nil
}

// doingError is the invalid_input failure of a refused sentence (4.3.1
// details): details.field is session_description and details.reason one
// of doing.Clean's. The message is fixed text per reason; the sentence is
// never part of it.
func doingError(reason string) *protocol.Error {
	msg := map[string]string{
		doing.ReasonEmpty:        "the sentence is empty; put one line about the work on stdin",
		doing.ReasonTooLong:      "the sentence is longer than a doing line allows; say it in fewer words",
		doing.ReasonNotUTF8:      "the sentence is not valid UTF-8",
		doing.ReasonSecretShaped: "the sentence looks like it carries a credential; leave the token out",
		doing.ReasonLocalPath:    "the sentence names a path on this machine; describe the work, not the file",
	}[reason]
	details := map[string]string{"field": "session_description", "reason": reason}
	if reason == doing.ReasonTooLong {
		details["limit"] = strconv.Itoa(doing.MaxChars)
		details["unit"] = "codepoints"
	}
	return &protocol.Error{Code: protocol.CodeInvalidInput, Message: msg, Details: details}
}

// doingFailure passes an adapter failure through with the fixed suffix
// that tells the model what to do with it: nothing, or one more try
// later for `unavailable` (the harness's own timeout is one). A failure
// that is not a *protocol.Error is returned as it is.
func doingFailure(err error) error {
	perr, ok := err.(*protocol.Error) //nolint:errorlint // the adapterclient returns the value itself
	if !ok {
		return err
	}
	out := *perr
	if perr.Code == protocol.CodeUnavailable {
		out.Message = perr.Message + DoingRetrySuffix
	} else {
		out.Message = perr.Message + DoingFailedSuffix
	}
	return &out
}
