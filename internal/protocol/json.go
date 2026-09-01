package protocol

import "encoding/json/v2"

// Unmarshal parses one protocol JSON document with the loose semantics of
// D16: unknown members are ignored, member names match case-sensitively,
// duplicate member names and invalid UTF-8 are rejected by the codec.
// Every parse failure maps to a single invalid_input *Error whose message
// is fixed text — the decoder's own error strings embed JSON pointers
// built from member names, which in a hostile document are
// attacker-controlled, so they are never echoed (and tests must never
// assert on them, plan 7.3).
func Unmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return &Error{
			Code:    CodeInvalidInput,
			Message: "input is not a valid protocol JSON document",
			Details: map[string]string{"reason": reasonMalformedJSON},
		}
	}
	return nil
}

// Decode parses one protocol JSON document into v and then validates it:
// the loose parse of Unmarshal followed by the shape's own Validate. This
// is the one entry point later phases should use for anything read off a
// wire.
func Decode(data []byte, v Validator) error {
	if err := Unmarshal(data, v); err != nil {
		return err
	}
	return v.Validate()
}
