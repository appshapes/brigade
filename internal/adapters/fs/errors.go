package fs

import (
	"strconv"

	"github.com/appshapes/brigade/internal/protocol"
)

// The adapter's failure vocabulary. Every message here is FIXED text: an
// error message reaches the model (4.3) and an interpolated value in a
// hostile document is attacker-controlled, so nothing a caller supplied is
// ever echoed — not a flag, not a member value, and above all not a join
// secret (4.5.14).

// errNotFound is the uniform not_found of 4.5.6 and 4.5.7. It carries NO
// details, so the envelope for an unknown id, a foreign-team id and a
// not-owned id is byte-identical and there is no existence oracle (C-13,
// C-19, C-24, C-25, C-26, C-29, C-37).
func errNotFound() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeNotFound, Message: "session or message not found"}
}

// errNotMember is the uniform unauthorized of 4.5.7: authenticated, but
// not an active member of this team. One fixed message and no details, so
// a missing team, a missing membership and a revoked membership cannot be
// told apart (C-26, C-43).
func errNotMember() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
}

// errSecretRejected is the uniform unauthorized of `team join` (4.4.10):
// byte-identical for a wrong secret, an unknown team and a banned
// principal (C-04).
func errSecretRejected() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnauthorized, Message: "join secret rejected"}
}

// errUsage refuses argv. The offending argument is never echoed: it could
// be a mis-pasted secret (C-05).
func errUsage(message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUsage,
		Message: message,
		Details: map[string]string{"reason": "invalid_arguments"},
	}
}

// errConfig refuses a local configuration the adapter cannot honour.
func errConfig(message, reason string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: message,
		Details: map[string]string{"reason": reason},
	}
}

// errConflict is the 4.6 conflict with its reason token.
func errConflict(message, reason string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConflict,
		Message: message,
		Details: map[string]string{"reason": reason},
	}
}

// errRateLimited reports a 4.5.12 budget with the reason token of 4.6 and
// a positive retry_after_ms (C-28).
func errRateLimited(reason string, retryAfter int) *protocol.Error {
	if retryAfter < 1 {
		retryAfter = 1
	}
	return &protocol.Error{
		Code:         protocol.CodeRateLimited,
		Message:      "the send rate limit for this " + rateSubject(reason) + " is exhausted",
		RetryAfterMS: retryAfter,
		Details:      map[string]string{"reason": reason},
	}
}

// rateSubject names, without echoing anything, what ran out of budget.
func rateSubject(reason string) string {
	switch reason {
	case reasonPrincipalPerMinute, reasonPrincipalPerHour:
		return "principal"
	case reasonSenderQuotaForRecipient, reasonRecipientInboxFull:
		return "recipient inbox"
	default:
		return "session"
	}
}

// errInternal is an unclassified failure of the adapter itself (4.6 exit
// 1). The underlying error is logged to stderr through the redacting
// logger; it never reaches stdout.
func errInternal(message string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInternal, Message: message}
}

// errInvalidRange refuses a flag value outside its bounds with `usage`,
// naming the bounds but not the value.
func errInvalidRange(flagName string, low, high int) *protocol.Error {
	return errUsage("--" + flagName + " must be between " + strconv.Itoa(low) + " and " + strconv.Itoa(high))
}

// The details.reason tokens of 4.6 this adapter emits.
const (
	reasonProfileBound            = "profile_bound"
	reasonProfileExists           = "profile_exists"
	reasonSessionLive             = "session_live"
	reasonSessionClosed           = "session_closed"
	reasonIdempotencyKeyReused    = "idempotency_key_reused"
	reasonSendPerMinute           = "send_per_minute"
	reasonSendPerHour             = "send_per_hour"
	reasonPrincipalPerMinute      = "principal_per_minute"
	reasonPrincipalPerHour        = "principal_per_hour"
	reasonSenderQuotaForRecipient = "sender_quota_for_recipient"
	reasonRecipientInboxFull      = "recipient_inbox_full"
)

// The fixed messages of the authentication ladder of section 5 of the
// adapter design. `config` for "no profile" and for "no team bound"
// (4.6: credential present, no team bound is exit 11); `unauthenticated`
// for a missing credential.
func errNoProfile() *protocol.Error {
	return errConfig("profile is not configured", "profile_missing")
}

func errNoCredential() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnauthenticated,
		Message: "no usable credential for this profile",
		Details: map[string]string{"reason": "credential_missing"},
	}
}

func errNoTeam() *protocol.Error {
	return errConfig("profile is not bound to a team", "no_team_bound")
}
