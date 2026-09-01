package cli

// ProtocolVersion is the value of the `protocol_version` member of every
// result envelope (plan 4.3).
const ProtocolVersion = "1"

// A Code is the machine-readable `error.code` of the plan's 4.6 taxonomy.
//
// P1-1 note: 4.6 is the adapter protocol's taxonomy and P1-2 gives it a
// home in internal/protocol. It is declared here because internal/cli owns
// the exit-code mapping (7.3) and internal/protocol does not exist yet;
// when it does, this file should become a thin alias over it rather than a
// second copy.
type Code string

// The 4.6 error codes, in exit-code order.
const (
	CodeInternal         Code = "internal"
	CodeUsage            Code = "usage"
	CodeInvalidInput     Code = "invalid_input"
	CodeUnauthenticated  Code = "unauthenticated"
	CodeUnauthorized     Code = "unauthorized"
	CodeNotFound         Code = "not_found"
	CodeConflict         Code = "conflict"
	CodeRateLimited      Code = "rate_limited"
	CodeUnavailable      Code = "unavailable"
	CodeProtocolMismatch Code = "protocol_mismatch"
	CodeConfig           Code = "config"
	CodeLoopDetected     Code = "loop_detected"
)

// ExitOK is the exit status of a command that succeeded.
const ExitOK = 0

// Exit maps a code to its process exit status (plan 4.6). An unrecognised
// code maps to the `internal` status rather than to success, so a code
// added without updating this table cannot turn a failure into an exit 0.
func (c Code) Exit() int {
	switch c {
	case CodeInternal:
		return 1
	case CodeUsage:
		return 2
	case CodeInvalidInput:
		return 3
	case CodeUnauthenticated:
		return 4
	case CodeUnauthorized:
		return 5
	case CodeNotFound:
		return 6
	case CodeConflict:
		return 7
	case CodeRateLimited:
		return 8
	case CodeUnavailable:
		return 9
	case CodeProtocolMismatch:
		return 10
	case CodeConfig:
		return 11
	case CodeLoopDetected:
		return 12
	default:
		return 1
	}
}

// Retryable reports whether 4.6 marks the code as worth retrying.
func (c Code) Retryable() bool {
	switch c {
	case CodeRateLimited, CodeUnavailable:
		return true
	case CodeInternal, CodeUsage, CodeInvalidInput, CodeUnauthenticated,
		CodeUnauthorized, CodeNotFound, CodeConflict, CodeProtocolMismatch,
		CodeConfig, CodeLoopDetected:
		return false
	default:
		return false
	}
}
