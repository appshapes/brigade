package protocol

import "encoding/json/jsontext"

// ErrorObject is the wire `error` member of a failing 4.3 envelope and of
// a watch `error` event (4.4.9).
//
// Retryable is a plain bool, and the rule is (P1-4 decision 2):
//
//   - producers MUST emit it — the member has no omitzero, so a marshalled
//     ErrorObject always carries "retryable": true|false;
//   - consumers treat an absent member as false, which under loose parsing
//     is what a plain bool yields, and which fails safe;
//   - consumers SHOULD derive retryability from Code.Retryable(), the
//     source of truth: only rate_limited (8) and unavailable (9) are ever
//     retryable, so the flag is advisory and Validate does not check it.
type ErrorObject struct {
	Code         Code              `json:"code"`
	Message      string            `json:"message"`
	Retryable    bool              `json:"retryable"`
	RetryAfterMS int               `json:"retry_after_ms,omitzero"`
	Details      map[string]string `json:"details,omitzero"`
}

// Validate implements Validator.
func (e *ErrorObject) Validate() error {
	if err := requireString("error.code", string(e.Code)); err != nil {
		return err
	}
	if err := requireString("error.message", e.Message); err != nil {
		return err
	}
	if e.RetryAfterMS < 0 {
		return errOutOfRange("error.retry_after_ms", 0, int(^uint(0)>>1))
	}
	return nil
}

// Envelope is the 4.3 result envelope: exactly one JSON document on
// stdout for every command except `message watch`. Exactly one of Result
// and Error is set.
type Envelope struct {
	OK              bool           `json:"ok"`
	ProtocolVersion string         `json:"protocol_version"`
	Result          jsontext.Value `json:"result,omitzero"`
	Error           *ErrorObject   `json:"error,omitzero"`
}

// Validate implements Validator.
func (e *Envelope) Validate() error {
	if err := requireString("protocol_version", e.ProtocolVersion); err != nil {
		return err
	}
	if e.OK {
		if len(e.Result) == 0 {
			return errRequired("result")
		}
		if e.Error != nil {
			return &Error{
				Code:    CodeInvalidInput,
				Message: "error is present on an ok envelope",
				Details: map[string]string{"field": "error", "reason": reasonInvalidValue},
			}
		}
		return nil
	}
	if e.Error == nil {
		return errRequired("error")
	}
	if len(e.Result) > 0 {
		return &Error{
			Code:    CodeInvalidInput,
			Message: "result is present on a failing envelope",
			Details: map[string]string{"field": "result", "reason": reasonInvalidValue},
		}
	}
	return e.Error.Validate()
}
