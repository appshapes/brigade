package protocol

import (
	"strings"
	"time"
	"unicode/utf8"
)

// A Validator is a wire type with a hand-written Validate method. Every
// shape of 4.4 implements it; Decode runs it after a loose parse.
type Validator interface {
	// Validate checks required members, byte caps, code-point caps and
	// enumerated values, and returns a *Error with CodeInvalidInput
	// carrying the offending member's JSON name in Details["field"], or
	// nil when the value is a valid protocol document.
	Validate() error
}

// requireString checks a required text member: present, non-empty and
// valid UTF-8.
func requireString(field, value string) error {
	if value == "" {
		return errRequired(field)
	}
	if !utf8.ValidString(value) {
		return errNotUTF8(field)
	}
	return nil
}

// capBytes enforces a BYTE cap (len) on a text member.
func capBytes(field, value string, limit int) error {
	if len(value) > limit {
		return errTooLong(field, limit, len(value), "bytes")
	}
	return nil
}

// capRunes enforces a CODE-POINT cap (utf8.RuneCountInString) on a text
// member. It is not capBytes: 4.4.1 measures summaries, names, labels,
// descriptions and idempotency keys in code points and only the body in
// bytes.
func capRunes(field, value string, limit int) error {
	if n := utf8.RuneCountInString(value); n > limit {
		return errTooLong(field, limit, n, "codepoints")
	}
	return nil
}

// optionalText validates an optional text member: valid UTF-8 and within
// its code-point cap when present.
func optionalText(field, value string, limit int) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) {
		return errNotUTF8(field)
	}
	return capRunes(field, value, limit)
}

// oneOf checks an enumerated member against its fixed allowed values.
func oneOf(field, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return errInvalidValue(field, strings.Join(allowed, ", "))
}

// requireTime checks a required timestamp member.
func requireTime(field string, value time.Time) error {
	if value.IsZero() {
		return errRequired(field)
	}
	return nil
}

// requireIDs checks a required, non-empty id-list member whose elements
// are non-empty strings.
func requireIDs(field string, ids []string) error {
	if len(ids) == 0 {
		return errRequired(field)
	}
	for _, id := range ids {
		if id == "" || !utf8.ValidString(id) {
			return errInvalidValue(field, "non-empty message ids")
		}
	}
	return nil
}

// leaseSecondsInRange checks an optional lease_seconds member when present:
// it must be a positive integer. The RANGE a lease may take is the adapter's
// own — 4.4.1 defines `lease` as "the range of lease_seconds an adapter
// accepts" and 4.4.2/4.4.4 bound the member by lease.min_seconds..
// lease.max_seconds, values the harness learns from `describe`, never at
// compile time — so the adapter enforces it with Lease.CheckSeconds after
// this validation. The protocol layer refuses only what no adapter could
// honour. (P1-2 bounded the member by the 4.4.1 example's 30..600 here,
// which would have made the fs adapter's advertised min_seconds = 1 (plan
// P1-5) unreachable; corrected in P1-5.)
func leaseSecondsInRange(field string, v *int) error {
	if v == nil {
		return nil
	}
	if *v < 1 {
		return errNotPositive(field)
	}
	return nil
}

// hopCountInRange checks a hop_count member against 0..MaxHopCount.
func hopCountInRange(field string, v int) error {
	if v < 0 || v > MaxHopCount {
		return errOutOfRange(field, 0, MaxHopCount)
	}
	return nil
}
