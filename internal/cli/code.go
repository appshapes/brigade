package cli

import "github.com/appshapes/brigade/internal/protocol"

// ProtocolVersion is the value of the `protocol_version` member of every
// result envelope (plan 4.3). It is owned by internal/protocol; this
// alias keeps the CLI's callers and tests reading naturally.
const ProtocolVersion = protocol.ProtocolVersion

// A Code is the machine-readable `error.code` of the plan's 4.6 taxonomy.
//
// The taxonomy — the Code type, its constants, Exit and Retryable — is
// owned by internal/protocol (the P1-2 row of the plan): adapters and the
// harness need it without importing the CLI. This file is the thin alias
// the P1-1 note in its place promised, not a second copy: the type alias
// means a protocol.Code and a cli.Code are the same type, so the exit-code
// map cannot fork.
type Code = protocol.Code

// The 4.6 error codes, in exit-code order.
const (
	CodeInternal         = protocol.CodeInternal
	CodeUsage            = protocol.CodeUsage
	CodeInvalidInput     = protocol.CodeInvalidInput
	CodeUnauthenticated  = protocol.CodeUnauthenticated
	CodeUnauthorized     = protocol.CodeUnauthorized
	CodeNotFound         = protocol.CodeNotFound
	CodeConflict         = protocol.CodeConflict
	CodeRateLimited      = protocol.CodeRateLimited
	CodeUnavailable      = protocol.CodeUnavailable
	CodeProtocolMismatch = protocol.CodeProtocolMismatch
	CodeConfig           = protocol.CodeConfig
	CodeLoopDetected     = protocol.CodeLoopDetected
)

// ExitOK is the exit status of a command that succeeded.
const ExitOK = protocol.ExitOK
