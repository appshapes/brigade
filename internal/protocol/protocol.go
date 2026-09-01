// Package protocol implements the wire layer of the Brigade Adapter
// Protocol, version 1 (BAP/1, plan section 4): a Go type for every JSON
// shape of 4.4 with a hand-written Validate method, the 4.6 error taxonomy
// with its exit-code map, and the protocol constants of 4.4.1.
//
// Parsing is deliberately loose (D16): unknown members are ignored, member
// names are case-sensitive, and a wrongly-cased member is silently zero —
// which is exactly why every wire type has a Validate method that checks
// required members, byte caps (len) and code-point caps
// (utf8.RuneCountInString) by hand. Duplicate member names and invalid
// UTF-8 are rejected by the codec itself; Unmarshal maps every such
// failure to invalid_input without echoing the decoder's own text, which
// can embed attacker-controlled member names.
//
// SendRequest is the one named exception to loose parsing (4.4.6, C-23): a
// request carrying any of the adapter-stamped sender members is rejected
// rather than silently ignored, because ignoring a forged sender field
// would hide a client bug.
package protocol

// ProtocolVersion is the value of the `protocol_version` member of every
// result envelope, DescribeResult, MessageEnvelope and watch `ready` event
// (plan 4.3, 4.4).
const ProtocolVersion = "1"

// ExitOK is the exit status of a command that succeeded.
const ExitOK = 0
