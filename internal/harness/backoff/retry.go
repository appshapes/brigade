package backoff

import (
	"errors"

	"github.com/appshapes/brigade/internal/protocol"
)

// Retryable reports whether a failed adapter call with this code may be
// retried (4.3, 4.6, U-16): only rate_limited and unavailable. Every other
// code — invalid_input, unauthorized, loop_detected, and the rest — is
// terminal, and so is a code this harness does not recognise (4.6 maps an
// unknown code to internal). The switch is exhaustive over protocol.Code
// so a code added to the taxonomy must be classified here on purpose.
func Retryable(code protocol.Code) bool {
	switch code {
	case protocol.CodeRateLimited, protocol.CodeUnavailable:
		return true
	case protocol.CodeInternal,
		protocol.CodeUsage,
		protocol.CodeInvalidInput,
		protocol.CodeUnauthenticated,
		protocol.CodeUnauthorized,
		protocol.CodeNotFound,
		protocol.CodeConflict,
		protocol.CodeProtocolMismatch,
		protocol.CodeConfig,
		protocol.CodeLoopDetected:
		return false
	default:
		return false
	}
}

// RetryableError is Retryable over an error: a *protocol.Error anywhere in
// the chain is classified by its code (adapterkit.Spawn maps a timeout, a
// signal death and a missing executable to unavailable, so those retry),
// and any other error — including nil — is not retried, because an error
// the harness cannot classify is a bug, not a transient.
func RetryableError(err error) bool {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		return Retryable(perr.Code)
	}
	return false
}

// RetryableExit reports whether a `message watch` child that ended with
// this exit status should be restarted (6.6: "restart ... on exit codes 8
// and 9 or a crash; stop on 4, 5, 10, 11"). An exit status that is a 4.6
// code's is classified as that code (so 8 and 9 restart; 1, 2, 3, 4, 5, 6,
// 7, 10, 11 and 12 stop); 0 is a clean end and is not a failure to retry;
// anything else — a signal death (-1 from exec.ExitError.ExitCode(), or
// 128+n from a shell), 126/127, or a status no code owns — is a crash and
// restarts. The caller's give-up rule (10 consecutive failures inside 5
// minutes) bounds every loop this can start.
func RetryableExit(exit int) bool {
	if exit == protocol.ExitOK {
		return false
	}
	if code, ok := codeForExit(exit); ok {
		return Retryable(code)
	}
	return true
}

// codeForExit is the inverse of protocol.Code.Exit for the twelve codes.
func codeForExit(exit int) (protocol.Code, bool) {
	for _, code := range allCodes {
		if code.Exit() == exit {
			return code, true
		}
	}
	return "", false
}

// allCodes is the frozen taxonomy in exit-code order (4.6).
var allCodes = []protocol.Code{
	protocol.CodeInternal,
	protocol.CodeUsage,
	protocol.CodeInvalidInput,
	protocol.CodeUnauthenticated,
	protocol.CodeUnauthorized,
	protocol.CodeNotFound,
	protocol.CodeConflict,
	protocol.CodeRateLimited,
	protocol.CodeUnavailable,
	protocol.CodeProtocolMismatch,
	protocol.CodeConfig,
	protocol.CodeLoopDetected,
}
