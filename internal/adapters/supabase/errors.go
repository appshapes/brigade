package supabase

import (
	"context"
	"crypto/x509"
	"encoding/json/v2"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// The adapter's failure vocabulary and the ONE mapping table (5.11, U-24).
// Every message here is FIXED text: an error message reaches the model
// (4.3), and raw server text, SQL, a token or an interpolated input value
// must never appear in it. The server's own words go to stderr at debug
// through the redacting logger, and nowhere else.

// The details.reason tokens this adapter emits (4.6; informative, 4.3.1).
const (
	reasonProfileMissing      = "profile_missing"
	reasonProfileExists       = "profile_exists"
	reasonBackendUnconfigured = "backend_unconfigured"
	reasonCredentialMissing   = "credential_missing" //nolint:gosec // G101: a details.reason token, not a credential
	reasonCredentialRevoked   = "credential_revoked" //nolint:gosec // G101: a details.reason token, not a credential
	reasonNoTeamBound         = "no_team_bound"
	reasonInvalidArguments    = "invalid_arguments"
	reasonMigrationDrift      = "migration_drift"
	reasonTLS                 = "tls"
	reasonOffline             = "offline"
	reasonTimeout             = "timeout"
	reasonUnreachable         = "unreachable"
	reasonBackendError        = "backend_error"
	reasonBackendResponse     = "unexpected_response"
	reasonAPIKeyRejected      = "api_key_rejected"
	reasonSignupDisabled      = "signup_disabled"
	reasonAuthRateLimit       = "auth_rate_limit"
	reasonRealtime            = "realtime"
	reasonSessionLive         = "session_live"
)

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
// told apart (C-26, C-43), and the same text ends a watch of a revoked
// member (C-08).
func errNotMember() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
}

// errSecretRejected is the uniform unauthorized of `team join` (4.4.10):
// byte-identical for a wrong secret, an unknown team and a banned
// principal — the backend already answers one 28-byte `invalid_secret`
// body for all three (C-04).
func errSecretRejected() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnauthorized, Message: "join secret rejected"}
}

// errUsage refuses argv. The offending argument is never echoed: it could
// be a mis-pasted secret (C-05).
func errUsage(message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUsage,
		Message: message,
		Details: map[string]string{"reason": reasonInvalidArguments},
	}
}

// errInvalidRange refuses a flag value outside its bounds with `usage`,
// naming the bounds but not the value.
func errInvalidRange(flagName string, low, high int) *protocol.Error {
	return errUsage("--" + flagName + " must be between " + strconv.Itoa(low) + " and " + strconv.Itoa(high))
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

// errInternal is an unclassified failure (4.6 exit 1). The underlying
// error is logged to stderr through the redacting logger; it never reaches
// stdout.
func errInternal(message string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInternal, Message: message}
}

// errUnexpectedResponse is an `internal` for an answer this adapter does
// not know how to read: details.reason "unexpected_response".
func errUnexpectedResponse(message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: message,
		Details: map[string]string{"reason": reasonBackendResponse},
	}
}

// errUnavailable is a 4.6 exit 9: the backend cannot be reached or did
// not answer usefully. Retryable by code.
func errUnavailable(message, reason string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: message,
		Details: map[string]string{"reason": reason},
	}
}

// errTLS is the TLS verification failure of 5.11: `unavailable` with
// details.reason "tls" and the SSL_CERT_FILE hint.
func errTLS() *protocol.Error {
	return errUnavailable("certificate verification failed; install ca-certificates or set SSL_CERT_FILE", reasonTLS)
}

// errRateLimited reports a backend budget with its reason token and a
// positive retry_after_ms (4.6, C-28).
func errRateLimited(reason string, retryAfterMS int) *protocol.Error {
	if retryAfterMS < 1 {
		retryAfterMS = 1
	}
	return &protocol.Error{
		Code:         protocol.CodeRateLimited,
		Message:      "a rate limit or unacknowledged-message cap is exhausted; wait before retrying",
		RetryAfterMS: retryAfterMS,
		Details:      map[string]string{"reason": reason},
	}
}

// The fixed messages of the authentication ladder (section 5 of the
// adapter design, 4.6; C-06, C-08): `config` for "no profile", "no
// backend" and "no team bound"; `unauthenticated` for a missing or
// revoked credential.
func errNoProfile() *protocol.Error {
	return errConfig("profile is not configured; run `profile init` first", reasonProfileMissing)
}

func errNoBackend() *protocol.Error {
	return errConfig("profile names no backend; run `profile init --url … --key …`", reasonBackendUnconfigured)
}

func errNoCredential() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnauthenticated,
		Message: "no usable credential for this profile; run `brigade team join` again",
		Details: map[string]string{"reason": reasonCredentialMissing},
	}
}

// errCredentialRevoked is the terminal credential error of 5.1: the
// refresh-token family is gone and session.json has been deleted.
func errCredentialRevoked() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnauthenticated,
		Message: "credential revoked; run `brigade team join` again",
		Details: map[string]string{"reason": reasonCredentialRevoked},
	}
}

// errBackendUnauthenticated is a 28000 or a JWT the backend would not
// honour even after a refresh: the local credential exists but does not
// authenticate.
func errBackendUnauthenticated() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnauthenticated,
		Message: "the backend did not accept this profile's credential; run `brigade team join` again",
	}
}

func errNoTeam() *protocol.Error {
	return errConfig("profile is not bound to a team", reasonNoTeamBound)
}

// postgrestError is PostgREST's error body: {code, message, details,
// hint}, each a string or null.
type postgrestError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Details *string `json:"details"`
	Hint    *string `json:"hint"`
}

// parsePostgrestError decodes body as a PostgREST error. A body without a
// string `code` is not one (a gateway page, an empty body, a GoTrue body).
func parsePostgrestError(body []byte) (postgrestError, bool) {
	var e postgrestError
	if err := json.Unmarshal(body, &e); err != nil || e.Code == "" {
		return postgrestError{}, false
	}
	return e, true
}

// jwtRejected reports whether a PostgREST code is the "JWT expired /
// invalid" pair that earns one refresh and one retry (5.1: PGRST303 is the
// cold-start path; the 90 s margin is the normal one).
func (e postgrestError) jwtRejected() bool {
	return e.Code == "PGRST301" || e.Code == "PGRST303"
}

// brigadePrefix is the raise convention of the finished migrations:
// 'brigade:<code>[:<reason>[:<retry_after_seconds>]]'.
const brigadePrefix = "brigade:"

// mapRPCFailure is the table of 5.11: the body first, whatever the HTTP
// status; the `brigade:` prefix first, the SQLSTATE second, the HTTP
// status only to split 42501 into "no usable JWT" (401) and "not a member"
// (403), and never to decide not_found — PT404 arrives as 404 and the
// legacy P0002 as 500, and both are the uniform not_found.
func mapRPCFailure(status int, body []byte) *protocol.Error {
	pg, ok := parsePostgrestError(body)
	if !ok {
		return mapNonPostgrest(status)
	}
	if strings.HasPrefix(pg.Message, brigadePrefix) {
		return mapBrigadeMessage(pg.Message)
	}
	switch pg.Code {
	case "28000":
		return errBackendUnauthenticated()
	case "42501":
		if status == 401 {
			return errBackendUnauthenticated()
		}
		return errNotMember()
	case "PT404", "P0002":
		return errNotFound()
	case "23505":
		return errConflict("the request conflicts with an existing record", "unique_violation")
	case "57014":
		return errUnavailable("the backend cancelled the request; try again", reasonBackendError)
	case "PGRST202", "PGRST205":
		return &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "migration drift between adapter and backend: the backend does not expose the function or table this adapter expects",
			Details: map[string]string{"reason": reasonMigrationDrift, "hint": "migration drift between adapter and backend"},
		}
	case "PGRST301", "PGRST303":
		return errBackendUnauthenticated()
	}
	if status >= 500 {
		return errUnavailable("the backend failed; try again later", reasonBackendError)
	}
	return errUnexpectedResponse("the backend refused the request for an unexpected reason")
}

// mapNonPostgrest classifies an answer whose body is not a PostgREST
// error: a gateway page, a Kong denial, an empty body.
func mapNonPostgrest(status int) *protocol.Error {
	switch {
	case status >= 500:
		return errUnavailable("the backend is unavailable; try again later", reasonBackendError)
	case status == 401 || status == 403:
		return errConfig("the backend rejected the profile's API key; check `profile init --key`", reasonAPIKeyRejected)
	default:
		return errUnexpectedResponse("the backend answered with an unexpected response")
	}
}

// mapBrigadeMessage maps 'brigade:<code>[:<reason>[:<retry>]]' to its
// 4.6 code. The reason goes to details.reason, an invalid_input reason is
// the offending member's name and goes to details.field (the backend
// caps harness/harness_version at 32, which `limits` does not publish),
// and a rate_limited reason carries retry_after_seconds last.
func mapBrigadeMessage(message string) *protocol.Error {
	parts := strings.Split(strings.TrimPrefix(message, brigadePrefix), ":")
	code := protocol.Code(parts[0])
	reason := ""
	if len(parts) > 1 {
		reason = parts[1]
	}
	switch code {
	case protocol.CodeUnauthenticated:
		return errBackendUnauthenticated()
	case protocol.CodeUnauthorized:
		return errNotMember()
	case protocol.CodeNotFound:
		return errNotFound()
	case protocol.CodeInvalidInput:
		details := map[string]string{"reason": "rejected_by_backend"}
		if reason != "" {
			details["field"] = reason
		}
		return &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "the backend rejected a member of the request",
			Details: details,
		}
	case protocol.CodeConflict:
		if reason == "" {
			reason = "conflict"
		}
		return errConflict("the request conflicts with the current state", reason)
	case protocol.CodeRateLimited:
		retry := 60
		if len(parts) > 2 {
			if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
				retry = n
			}
		}
		if reason == "" {
			reason = "backend"
		}
		return errRateLimited(reason, retry*1000)
	case protocol.CodeLoopDetected:
		return &protocol.Error{
			Code:    protocol.CodeLoopDetected,
			Message: "the reply chain has reached max_hop_count",
			Details: map[string]string{"reason": "max_hops"},
		}
	case protocol.CodeUnavailable:
		return errUnavailable("the backend is unavailable; try again later", reasonBackendError)
	case protocol.CodeConfig:
		return errConfig("the backend reports a configuration problem", "backend")
	case protocol.CodeInternal, protocol.CodeUsage, protocol.CodeProtocolMismatch:
		return errUnexpectedResponse("the backend raised an error this adapter does not expect")
	}
	return errUnexpectedResponse("the backend raised an error this adapter does not expect")
}

// mapTransportError classifies a failed HTTP exchange: a TLS verification
// failure is `unavailable`/tls with the SSL_CERT_FILE hint, a dial
// refused by BRIGADE_TEST_OFFLINE is `unavailable`/offline, a deadline is
// `unavailable`/timeout, and everything else — DNS, connection refused, a
// reset — is `unavailable`/unreachable (5.11).
func mapTransportError(err error) *protocol.Error {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		return perr
	}
	if errors.Is(err, errOffline) {
		return errUnavailable("network access is disabled by BRIGADE_TEST_OFFLINE", reasonOffline)
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return errTLS()
	}
	var certErr *x509.SystemRootsError
	if errors.As(err, &certErr) {
		return errTLS()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errUnavailable("the backend did not answer in time", reasonTimeout)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errUnavailable("the backend did not answer in time", reasonTimeout)
	}
	// A TLS alert or an x509 failure wrapped in text by the transport still
	// names the package; catch it so a corporate CA problem is reported as
	// such rather than as a generic outage.
	if strings.Contains(err.Error(), "x509:") || strings.Contains(err.Error(), "tls:") {
		return errTLS()
	}
	return errUnavailable("the backend could not be reached", reasonUnreachable)
}
