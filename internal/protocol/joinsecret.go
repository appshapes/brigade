package protocol

import (
	"encoding/json/v2"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"
)

// JoinSecretPrefix is the fixed, case-sensitive prefix of every v1 join
// secret (4.4.10, D5).
const JoinSecretPrefix = "brg1."

// A JoinSecret is a parsed `brg1.<team_ref>.<secret>` bearer secret. The
// zero value is invalid; obtain one through ParseJoinSecret. The type
// exists so the secret half cannot leak by accident: String, GoString,
// LogValue and JSON marshalling all redact it, and only the explicit
// Secret method returns the full value for the one wire call that needs
// it (`team join`).
type JoinSecret struct {
	teamRef string
	secret  string
}

// ParseJoinSecret parses and validates the `brg1.<team_ref>.<secret>`
// format. The team_ref is everything between the prefix and the LAST
// '.', because 4.8 deliberately does not freeze the team_ref's shape
// (an adapter may use refs containing '.'), while D5's secret half is
// a single token. Parsing is strict — no trimming; a TTY prompt layer
// trims before calling. A malformed secret is invalid_input (4.6), and
// neither the error message nor its details ever carry any part of the
// input (4.5.14: no secret in any error or log line).
func ParseJoinSecret(s string) (JoinSecret, error) {
	if !strings.HasPrefix(s, JoinSecretPrefix) {
		return JoinSecret{}, errMalformedJoinSecret()
	}
	rest := s[len(JoinSecretPrefix):]
	dot := strings.LastIndexByte(rest, '.')
	if dot <= 0 || dot == len(rest)-1 {
		// No separator, an empty team_ref, or an empty secret half.
		return JoinSecret{}, errMalformedJoinSecret()
	}
	teamRef, secret := rest[:dot], rest[dot+1:]
	if !validSecretText(teamRef) || !validSecretText(secret) {
		return JoinSecret{}, errMalformedJoinSecret()
	}
	return JoinSecret{teamRef: teamRef, secret: s}, nil
}

// validSecretText rejects whitespace and control or format characters
// anywhere in a secret component — the usual paste accidents and the
// characters that could forge log or frame structure — plus invalid
// UTF-8. It deliberately does not constrain the alphabet further: the
// protocol does not freeze the team_ref's shape (4.8).
func validSecretText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// errMalformedJoinSecret is constructed fresh per call so callers can
// never share or mutate one instance's details. Fixed text only: the
// secret must never appear in an error message (4.5.14).
func errMalformedJoinSecret() *Error {
	return &Error{
		Code:    CodeInvalidInput,
		Message: "join_secret is malformed; expected brg1.<team_ref>.<secret>",
		Details: map[string]string{"field": "join_secret", "reason": reasonInvalidValue},
	}
}

// TeamRef returns the team reference named inside the secret, for the
// local rejoin check of 4.4.10 (a secret naming the bound team is a
// rejoin). The team_ref is not itself secret — it appears in ordinary
// results.
func (s JoinSecret) TeamRef() string { return s.teamRef }

// Secret returns the full raw secret for the one legitimate use: the
// `team join` backend call. Every other rendering of the type redacts.
func (s JoinSecret) Secret() string { return s.secret }

// String implements fmt.Stringer with the secret half redacted, so a
// stray %v or %s can never leak it.
func (s JoinSecret) String() string {
	return JoinSecretPrefix + s.teamRef + ".[redacted]"
}

// GoString implements fmt.GoStringer so %#v redacts too.
func (s JoinSecret) GoString() string { return s.String() }

// LogValue implements slog.LogValuer so a JoinSecret handed to any
// slog logger renders redacted.
func (s JoinSecret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON redacts: a JoinSecret embedded in any marshalled value
// renders without its secret half. The `team join` request builds its
// join_secret member from Secret() explicitly — failing loudly at the
// backend beats leaking silently in a log or result.
func (s JoinSecret) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}
