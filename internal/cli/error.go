package cli

import (
	"encoding/json/v2"
	"errors"
	"io"

	"github.com/appshapes/brigade/internal/protocol"
)

// An Error is a command failure carrying the 4.6 code that decides both the
// process exit status and the `error` object of the JSON envelope.
type Error struct {
	// Code selects the exit status and appears as `error.code`.
	Code Code
	// Message is short, human-readable and safe to show to a model: no raw
	// server text, no SQL, no tokens (4.3).
	Message string
	// RetryAfterMS is emitted only when non-zero (rate_limited).
	RetryAfterMS int
	// Details is optional and adapter-specific (4.3).
	Details map[string]string
	// Command names the command for the one-line stderr form. Empty when
	// the failure happened before a command was resolved.
	Command string
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

// usagef builds a `usage` Error. Callers must never interpolate a flag
// value into msg: argv can carry a join secret (4.5.14) and this text
// reaches both stderr and stdout.
func usagef(command, msg string) *Error {
	return &Error{Code: CodeUsage, Message: msg, Command: command}
}

// errorObject is the `error` member of the 4.3 envelope.
type errorObject struct {
	Code         Code              `json:"code"`
	Message      string            `json:"message"`
	Retryable    bool              `json:"retryable"`
	RetryAfterMS int               `json:"retry_after_ms,omitzero"`
	Details      map[string]string `json:"details,omitzero"`
}

// envelope is the 4.3 result envelope. Exactly one of Result and Err is set.
type envelope struct {
	OK              bool         `json:"ok"`
	ProtocolVersion string       `json:"protocol_version"`
	Result          any          `json:"result,omitzero"`
	Err             *errorObject `json:"error,omitzero"`
}

// writeJSON writes one envelope followed by a newline. Indentation is
// deliberately absent: this is protocol output, not a document.
func writeJSON(w io.Writer, e envelope) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n")
	return err
}

// WriteResult writes a successful 4.3 envelope carrying result.
func WriteResult(w io.Writer, result any) error {
	return writeJSON(w, envelope{OK: true, ProtocolVersion: ProtocolVersion, Result: result})
}

// WriteError writes a failing 4.3 envelope for err.
func WriteError(w io.Writer, err *Error) error {
	return writeJSON(w, envelope{
		OK:              false,
		ProtocolVersion: ProtocolVersion,
		Err: &errorObject{
			Code:         err.Code,
			Message:      err.Message,
			Retryable:    err.Code.Retryable(),
			RetryAfterMS: err.RetryAfterMS,
			Details:      err.Details,
		},
	})
}

// humanError renders the one-line stderr form of 6.4:
//
//	brigade <command> failed (<code>): <message>
//
// with the command word omitted when no command was resolved.
func humanError(err *Error) string {
	if err.Command == "" {
		return Program + " failed (" + string(err.Code) + "): " + err.Message + "\n"
	}
	return Program + " " + err.Command + " failed (" + string(err.Code) + "): " + err.Message + "\n"
}

// report writes err to the right stream and returns its exit status: the
// JSON envelope on stdout for a machine caller, one line on stderr for a
// human (7.3, 6.4). Nothing but the envelope ever reaches stdout.
func report(s Streams, jsonMode bool, err *Error) int {
	if jsonMode {
		if werr := WriteError(s.Out, err); werr != nil {
			_, _ = io.WriteString(s.Err, humanError(err))
		}
		return err.Code.Exit()
	}
	_, _ = io.WriteString(s.Err, humanError(err))
	return err.Code.Exit()
}

// asError maps any error returned by a command to an *Error. A command that
// already knows its code keeps it — a *cli.Error as it is, a
// *protocol.Error (what the harness library and the commands package
// return) with its code, message, retry-after and details carried over;
// anything else is an unclassified bug and becomes `internal` (exit 1),
// never a silent success.
func asError(command string, err error) *Error {
	var cerr *Error
	if errors.As(err, &cerr) {
		if cerr.Command == "" {
			cerr.Command = command
		}
		return cerr
	}
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return &Error{
			Code:         perr.Code,
			Message:      perr.Message,
			RetryAfterMS: perr.RetryAfterMS,
			Details:      perr.Details,
			Command:      command,
		}
	}
	return &Error{Code: CodeInternal, Message: err.Error(), Command: command}
}
