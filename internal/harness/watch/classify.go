package watch

import (
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/protocol"
)

// verdict is the supervisor's classification of an attempt.
type verdict struct {
	stop bool          // stop the watcher with code
	code protocol.Code // the stopping code (stop) or the failure's code (retry)
	why  string        // a fixed token for the log
}

// classify applies 6.6's restart/stop map through backoff.RetryableExit:
// a child that exited 8 or 9, died by a signal, exited with a status no
// code owns, exited 0 without being asked, or never became ready is
// restarted; 4, 5, 10, 11 and the other non-retryable codes stop the
// watcher. An `error` event's code is used when the child's exit status
// says nothing (0 or a signal death) or when the child had to be stopped
// after the event.
func classify(r attemptResult) verdict {
	if r.startErr != nil {
		code := codeOf(r.startErr)
		if backoff.Retryable(code) {
			return verdict{code: code, why: "start_failed"}
		}
		return verdict{stop: true, code: code, why: "start_refused"}
	}
	if r.fatalCode != "" {
		if backoff.Retryable(r.fatalCode) {
			return verdict{code: r.fatalCode, why: "error_event"}
		}
		return verdict{stop: true, code: r.fatalCode, why: "error_event"}
	}
	if r.readyTimeout {
		return verdict{code: protocol.CodeUnavailable, why: "ready_timeout"}
	}
	switch {
	case r.exitCode > 0:
		code := protocol.CodeInternal
		for _, c := range allCodes {
			if c.Exit() == r.exitCode {
				code = c
				break
			}
		}
		if backoff.RetryableExit(r.exitCode) {
			return verdict{code: code, why: "child_exit"}
		}
		return verdict{stop: true, code: code, why: "child_exit"}
	case r.exitCode < 0:
		return verdict{code: protocol.CodeUnavailable, why: "child_signal"}
	default:
		// Exit 0 without our close: the stream ended on the adapter's own
		// initiative (stdin EOF it should not have seen, a SIGTERM from
		// elsewhere). A restart is the safe reading; a non-retryable
		// error event just before it names the real reason.
		if r.lastErrorCode != "" && !backoff.Retryable(r.lastErrorCode) {
			return verdict{stop: true, code: r.lastErrorCode, why: "error_then_exit"}
		}
		return verdict{code: protocol.CodeUnavailable, why: "child_ended"}
	}
}

// allCodes is the 4.6 taxonomy, for mapping an exit status back to its
// code.
var allCodes = []protocol.Code{
	protocol.CodeInternal, protocol.CodeUsage, protocol.CodeInvalidInput,
	protocol.CodeUnauthenticated, protocol.CodeUnauthorized, protocol.CodeNotFound,
	protocol.CodeConflict, protocol.CodeRateLimited, protocol.CodeUnavailable,
	protocol.CodeProtocolMismatch, protocol.CodeConfig, protocol.CodeLoopDetected,
}

// pruneFailures drops the failures before cutoff.
func pruneFailures(failures []time.Time, cutoff time.Time) []time.Time {
	kept := failures[:0]
	for _, t := range failures {
		if !t.Before(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// stopNotice is the one line the next prompt hook prints for a stopped
// watcher (6.6).
func stopNotice(code protocol.Code) string {
	prefix := "Brigade: watcher stopped: " + string(code) + "; "
	if code == protocol.CodeUnauthenticated || code == protocol.CodeUnauthorized {
		return prefix + "run `brigade team join` again"
	}
	if code == protocol.CodeProtocolMismatch {
		return prefix + "the adapter speaks a different protocol version than this plugin; update one of them"
	}
	if code == protocol.CodeConfig {
		return prefix + "run `brigade profile status` in a terminal"
	}
	return prefix + "it will be restarted at your next prompt"
}

// giveUpNotice is the line for the give-up exit.
func giveUpNotice(n int, window time.Duration) string {
	return "Brigade: watcher gave up after " + strconv.Itoa(n) + " failures within " +
		window.Truncate(time.Second).String() + "; it will be restarted at your next prompt"
}
