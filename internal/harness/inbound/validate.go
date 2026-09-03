package inbound

import (
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/protocol"
)

// MaxMessageIDBytes caps a message_id the pipeline will handle (6.8 step 1).
const MaxMessageIDBytes = 200

// The reasons a message is rejected at step 1 (U-18). Fixed tokens; the
// offending value is never echoed.
const (
	ReasonMissingID        = "missing_message_id"
	ReasonIDTooLong        = "message_id_too_long"
	ReasonIDNotUTF8        = "message_id_not_utf8"
	ReasonMissingSender    = "missing_sender_session_id"
	ReasonMissingPrincipal = "missing_sender_principal_ref"
	ReasonEmptyBody        = "empty_body"
	ReasonBodyNotUTF8      = "body_not_utf8"
	ReasonBodyTooLarge     = "body_too_large"
)

// A ValidationError names the member and the rule a message failed. Its
// text is fixed: the member's wire name and the reason token, never the
// value.
type ValidationError struct {
	Field  string
	Reason string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return "inbound: message rejected (" + e.Field + ": " + e.Reason + ")"
}

// Validate is the loose validation of 6.8 step 1 (U-18): message_id a
// non-empty UTF-8 string of at most MaxMessageIDBytes bytes; body a
// non-empty, valid UTF-8 string of at most protocol.MaxBodyBytes BYTES (the
// wire cap, 4.4.5; the sanitiser truncates, but an over-cap body is a
// protocol violation the pipeline refuses rather than repairs);
// sender.session_id and sender.principal_ref non-empty. Nothing else is
// checked — kind, team_ref, hop_count, timestamps and delivery_state are
// the adapter's business and an unknown value there must not lose a
// message. Type-level failures (a non-string id, a nested object where a
// string belongs, invalid UTF-8 in the document) never reach here: the
// codec rejects them (protocol.Unmarshal), and a line over 1 MiB is
// dropped by the NDJSON reader before decoding (B-6).
func Validate(m *protocol.MessageEnvelope) error {
	switch {
	case m.MessageID == "":
		return &ValidationError{Field: "message_id", Reason: ReasonMissingID}
	case len(m.MessageID) > MaxMessageIDBytes:
		return &ValidationError{Field: "message_id", Reason: ReasonIDTooLong}
	case !utf8.ValidString(m.MessageID):
		return &ValidationError{Field: "message_id", Reason: ReasonIDNotUTF8}
	case m.Sender.SessionID == "":
		return &ValidationError{Field: "sender.session_id", Reason: ReasonMissingSender}
	case m.Sender.PrincipalRef == "":
		return &ValidationError{Field: "sender.principal_ref", Reason: ReasonMissingPrincipal}
	case m.Body == "":
		return &ValidationError{Field: "body", Reason: ReasonEmptyBody}
	case len(m.Body) > protocol.MaxBodyBytes:
		return &ValidationError{Field: "body", Reason: ReasonBodyTooLarge}
	case !utf8.ValidString(m.Body):
		return &ValidationError{Field: "body", Reason: ReasonBodyNotUTF8}
	}
	return nil
}
