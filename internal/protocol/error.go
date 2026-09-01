package protocol

import "strconv"

// An Error is a protocol-level failure: a 4.6 code plus a short message
// that is safe to show to a model (no raw server text, no SQL, no tokens,
// and never an interpolated input value — member names and field values in
// a hostile document are attacker-controlled).
type Error struct {
	// Code selects the exit status and appears as `error.code`.
	Code Code
	// Message is short, human-readable and value-free.
	Message string
	// RetryAfterMS is set only for rate_limited.
	RetryAfterMS int
	// Details is optional and machine-readable (4.3). Validation failures
	// carry the offending member's JSON name under "field".
	Details map[string]string
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

// Object renders the error as the wire `error` member of a 4.3 envelope,
// with `retryable` filled in from the taxonomy.
func (e *Error) Object() *ErrorObject {
	r := e.Code.Retryable()
	return &ErrorObject{
		Code:         e.Code,
		Message:      e.Message,
		Retryable:    &r,
		RetryAfterMS: e.RetryAfterMS,
		Details:      e.Details,
	}
}

// The `reason` detail values validation failures use.
const (
	reasonRequired        = "required"
	reasonTooLong         = "too_long"
	reasonInvalidValue    = "invalid_value"
	reasonNotUTF8         = "not_utf8"
	reasonForbiddenMember = "forbidden_member"
	reasonOutOfRange      = "out_of_range"
	reasonMalformedJSON   = "malformed_json"
)

// errRequired reports a missing or empty required member.
func errRequired(field string) *Error {
	return &Error{
		Code:    CodeInvalidInput,
		Message: "required member " + field + " is missing or empty",
		Details: map[string]string{"field": field, "reason": reasonRequired},
	}
}

// errTooLong reports a cap violation. unit is "bytes" or "codepoints" —
// the two caps measure different things (4.4.1) and the details say which.
func errTooLong(field string, limit, actual int, unit string) *Error {
	return &Error{
		Code:    CodeInvalidInput,
		Message: field + " exceeds the protocol cap of " + strconv.Itoa(limit) + " " + unit,
		Details: map[string]string{
			"field":  field,
			"reason": reasonTooLong,
			"limit":  strconv.Itoa(limit),
			"actual": strconv.Itoa(actual),
			"unit":   unit,
		},
	}
}

// errInvalidValue reports a member outside its enumerated values. The
// offending value is deliberately not echoed; allowed is a fixed,
// spec-authored list so it is safe to print.
func errInvalidValue(field, allowed string) *Error {
	return &Error{
		Code:    CodeInvalidInput,
		Message: field + " must be one of: " + allowed,
		Details: map[string]string{"field": field, "reason": reasonInvalidValue, "allowed": allowed},
	}
}

// errNotUTF8 reports a text member that is not valid UTF-8. The codec
// already rejects invalid UTF-8 on the wire; this arm catches values built
// in-process.
func errNotUTF8(field string) *Error {
	return &Error{
		Code:    CodeInvalidInput,
		Message: field + " is not valid UTF-8",
		Details: map[string]string{"field": field, "reason": reasonNotUTF8},
	}
}

// errForbiddenMember reports a caller-supplied member the adapter stamps
// from its own authority (4.4.6, C-23).
func errForbiddenMember(field string) *Error {
	return &Error{
		Code:    CodeInvalidInput,
		Message: field + " is stamped by the adapter and must not be supplied by the caller",
		Details: map[string]string{"field": field, "reason": reasonForbiddenMember},
	}
}

// errOutOfRange reports a numeric member outside its protocol bounds.
func errOutOfRange(field string, minimum, maximum int) *Error {
	return &Error{
		Code: CodeInvalidInput,
		Message: field + " must be between " + strconv.Itoa(minimum) +
			" and " + strconv.Itoa(maximum),
		Details: map[string]string{
			"field":  field,
			"reason": reasonOutOfRange,
			"min":    strconv.Itoa(minimum),
			"max":    strconv.Itoa(maximum),
		},
	}
}
