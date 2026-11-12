package rest

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// The adapter's OWN failure vocabulary: what it answers without the
// server, or about a server that did not answer as the contract says.
// Every message here is fixed text. A server's own error envelope is the
// other source of failures; it is passed on as the contract's section 1
// says (response.go) and never rebuilt here.

// The details.reason tokens this adapter emits (4.6; informative, 4.3.1).
const (
	reasonProfileBound        = "profile_bound"
	reasonProfileMissing      = "profile_missing"
	reasonProfileExists       = "profile_exists"
	reasonBackendUnconfigured = "backend_unconfigured"
	reasonBackendURL          = "backend_url"
	reasonBackendCertificate  = "backend_certificate"
	// reasonCertificateWithoutTLS: a pin given with an http url.
	reasonCertificateWithoutTLS = "certificate_without_https"
	// reasonCertificateMismatch: the server presented a certificate other
	// than the pinned one.
	reasonCertificateMismatch = "certificate_mismatch"
	reasonCredentialMissing   = "credential_missing" //nolint:gosec // G101: a details.reason token, not a credential
	reasonNoTeamBound         = "no_team_bound"
	reasonInvalidArguments    = "invalid_arguments"
	reasonInvalidValue        = "invalid_value"
	reasonTLS                 = "tls"
	reasonOffline             = "offline"
	reasonTimeout             = "timeout"
	reasonUnreachable         = "unreachable"
	reasonBackendError        = "backend_error"
	reasonBackendResponse     = "unexpected_response"
	reasonSecretFile          = "secret_file_unwritable" //nolint:gosec // G101: a details.reason token, not a credential
	reasonCreateTokenFile     = "create_token_file"      //nolint:gosec // G101: a details.reason token, not a credential
)

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

// errInvalidField refuses one member of a stdin document or one flag
// value with invalid_input, naming the member and the reason, never its
// value.
func errInvalidField(field, reason, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: message,
		Details: map[string]string{"field": field, "reason": reason},
	}
}

// errInternal is an unclassified failure of the adapter itself (4.6 exit
// 1). The underlying error is logged to stderr through the redacting
// logger; it never reaches stdout.
func errInternal(message string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInternal, Message: message}
}

// errUnexpectedResponse is the contract's `internal`: the server answered,
// with a status below 500, something that is not a valid result envelope,
// or a result this adapter could not read as the command's shape.
func errUnexpectedResponse(message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: message,
		Details: map[string]string{"reason": reasonBackendResponse},
	}
}

// errUnavailable is the contract's `unavailable`: the request failed in
// transport, or the status was 5xx and the body was not an envelope.
// Retryable by code.
func errUnavailable(message, reason string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: message,
		Details: map[string]string{"reason": reason},
	}
}

// errTLS is a TLS verification failure: `unavailable` with details.reason
// "tls" and the SSL_CERT_FILE hint.
func errTLS() *protocol.Error {
	return errUnavailable("certificate verification failed; install ca-certificates or set SSL_CERT_FILE", reasonTLS)
}

// errCertificateMismatch is a server that presented a certificate other
// than the one the team file pins: `unavailable`, like every other failure
// to reach the backend the profile means, with its own details.reason. It
// is what a renewed certificate looks like before the team file names the
// new one, and what an impostor looks like; the message says the first and
// never asks anyone to trust what was presented.
func errCertificateMismatch() *protocol.Error {
	return errUnavailable("the server's certificate is not the one this team's file pins; if the server's certificate was replaced, its operator gives the team the new fingerprint", reasonCertificateMismatch)
}

// The fixed messages of the authentication ladder (4.6; C-06, C-08):
// `config` for "no profile", "no backend" and "no team bound";
// `unauthenticated` for a missing credential.
func errNoProfile() *protocol.Error {
	return errConfig("profile is not configured; run `profile init` first", reasonProfileMissing)
}

func errNoBackend() *protocol.Error {
	return errConfig("profile names no backend; run `profile init --url …`", reasonBackendUnconfigured)
}

func errNoCredential() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUnauthenticated,
		Message: "no usable credential for this profile; run `brigade team join` again",
		Details: map[string]string{"reason": reasonCredentialMissing},
	}
}

func errNoTeam() *protocol.Error {
	return errConfig("profile is not bound to a team", reasonNoTeamBound)
}

// asProtocolError is the *protocol.Error in err's chain, or `internal`
// with the fixed message: an unclassified error can carry raw text and
// never reaches stdout.
func asProtocolError(err error) *protocol.Error {
	var typed *protocol.Error
	if errors.As(err, &typed) && typed != nil {
		return typed
	}
	return &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"}
}

// mapTransportError classifies a failed HTTP exchange, every arm of it
// `unavailable` (the contract's section 1: refused, timed out, TLS): a
// certificate the verifier rejects is reason "tls" with the SSL_CERT_FILE
// hint, a dial refused by BRIGADE_TEST_OFFLINE is "offline", a deadline is
// "timeout", and everything else (DNS, connection refused, a reset) is
// "unreachable".
func mapTransportError(err error) *protocol.Error {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		return perr
	}
	if errors.Is(err, errOffline) {
		return errUnavailable("network access is disabled by BRIGADE_TEST_OFFLINE", reasonOffline)
	}
	if errors.Is(err, errPinnedCertificate) {
		return errCertificateMismatch()
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return errTLS()
	}
	var roots *x509.SystemRootsError
	if errors.As(err, &roots) {
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
