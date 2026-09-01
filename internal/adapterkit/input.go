package adapterkit

import (
	"io"
	"os"
	"strconv"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/protocol"
)

// MaxInputBytes caps the stdin document at 1 MiB (plan 4.1). The reader
// consumes at most one byte more than this, so a hostile or accidental
// multi-gigabyte stream costs bounded memory and is refused, never
// swallowed.
const MaxInputBytes = 1 << 20

// ReadInput reads the one JSON document a command takes on stdin (4.1):
// UTF-8, terminated by EOF, at most MaxInputBytes.
//
// Refusals, all as *protocol.Error:
//   - stdin is a terminal → usage (exit 2). A human ran an input-taking
//     command by hand; without this check the process would block forever
//     waiting for typed JSON. Detected with x/term.IsTerminal, which is
//     why entry points must pass os.Stdin itself, not a wrapper.
//   - more than MaxInputBytes → invalid_input (exit 3), read through
//     io.LimitReader so the excess is never buffered.
//   - empty stdin → invalid_input (4.6: "stdin missing").
//   - a read error → invalid_input, with fixed text (the underlying error
//     string is not echoed).
//
// The bytes are returned unparsed; protocol.Decode owns JSON and UTF-8
// validity.
func ReadInput(stdin io.Reader) ([]byte, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return nil, &protocol.Error{
			Code:    protocol.CodeUsage,
			Message: "stdin is a terminal; this command reads one JSON document from stdin (pipe it or use a heredoc)",
			Details: map[string]string{"field": "stdin", "reason": "terminal"},
		}
	}
	data, err := io.ReadAll(io.LimitReader(stdin, MaxInputBytes+1))
	if err != nil {
		return nil, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "stdin could not be read",
			Details: map[string]string{"field": "stdin", "reason": "read_error"},
		}
	}
	if len(data) > MaxInputBytes {
		return nil, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "stdin exceeds the protocol cap of " + strconv.Itoa(MaxInputBytes) + " bytes",
			Details: map[string]string{
				"field":  "stdin",
				"reason": "too_long",
				"limit":  strconv.Itoa(MaxInputBytes),
				"unit":   "bytes",
			},
		}
	}
	if len(data) == 0 {
		return nil, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "stdin is empty; this command reads one JSON document from stdin",
			Details: map[string]string{"field": "stdin", "reason": "required"},
		}
	}
	return data, nil
}
