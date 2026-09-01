package adapterkit

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"

	"github.com/appshapes/brigade/internal/protocol"
)

// This file is one of the only places under internal/ permitted to name
// os.Stdout (plan 7.3; the .golangci.yml forbidigo exclusion is anchored
// to this exact path). Everything else takes an io.Writer.

// staticInternalEnvelope is the envelope of last resort: emitted when even
// marshalling a failing envelope failed. It is a compile-time constant so
// that this path cannot itself fail.
const staticInternalEnvelope = `{"ok":false,"protocol_version":"` + protocol.ProtocolVersion +
	`","error":{"code":"internal","message":"internal error","retryable":false}}` + "\n"

// WriteResult writes exactly one successful 4.3 envelope carrying result
// to w, followed by a newline, and returns the process exit status: 0, or
// the `internal` status when the envelope could not be marshalled or
// written (an exit 0 with no parseable envelope on stdout would be a lie
// the harness cannot detect).
func WriteResult(w io.Writer, result any) int {
	raw, err := json.Marshal(result)
	if err != nil {
		return WriteError(w, &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"})
	}
	env := protocol.Envelope{
		OK:              true,
		ProtocolVersion: protocol.ProtocolVersion,
		Result:          jsontext.Value(raw),
	}
	body, err := json.Marshal(&env)
	if err != nil {
		return WriteError(w, &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"})
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		return protocol.CodeInternal.Exit()
	}
	return protocol.ExitOK
}

// WriteError writes exactly one failing 4.3 envelope for err to w,
// followed by a newline, and returns the exit status of its 4.6 code.
//
// A *protocol.Error (anywhere in err's chain) keeps its code, message and
// details. Anything else — including nil, which is a caller bug — becomes
// `internal` with the fixed message "internal error": err.Error() text is
// deliberately never echoed onto stdout, because an unclassified error can
// carry raw server text, paths or token material, and stdout reaches the
// model. Callers log the underlying error to stderr through the redacting
// logger instead.
func WriteError(w io.Writer, err error) int {
	perr := asProtocolError(err)
	env := protocol.Envelope{
		OK:              false,
		ProtocolVersion: protocol.ProtocolVersion,
		Error:           perr.Object(),
	}
	body, merr := json.Marshal(&env)
	if merr != nil {
		_, _ = io.WriteString(w, staticInternalEnvelope)
		return protocol.CodeInternal.Exit()
	}
	_, _ = w.Write(append(body, '\n'))
	return perr.Code.Exit()
}

// PrintResult is WriteResult on the process stdout. It is the printer the
// stdout discipline of 7.3 names.
func PrintResult(result any) int {
	return WriteResult(os.Stdout, result)
}

// PrintError is WriteError on the process stdout: failures are protocol
// output too (4.3), and stderr stays free for diagnostics.
func PrintError(err error) int {
	return WriteError(os.Stdout, err)
}

// asProtocolError maps any error to the *protocol.Error the envelope is
// built from.
func asProtocolError(err error) *protocol.Error {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		return perr
	}
	return &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"}
}
