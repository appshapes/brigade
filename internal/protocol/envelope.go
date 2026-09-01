package protocol

import "encoding/json/jsontext"

// ErrorObject is the wire `error` member of a failing 4.3 envelope and of
// a watch `error` event (4.4.9). Retryable is REQUIRED by 4.3, which is
// why it is a pointer: with loose parsing a plain bool could not tell an
// absent member from an explicit false.
type ErrorObject struct {
	Code         Code              `json:"code"`
	Message      string            `json:"message"`
	Retryable    *bool             `json:"retryable,omitzero"`
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
	if e.Retryable == nil {
		return errRequired("error.retryable")
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
