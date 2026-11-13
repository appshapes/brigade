package adapterkit

import (
	"flag"
	"io"
	"strconv"

	"github.com/appshapes/brigade/internal/protocol"
)

// The requests of the team administration commands (4.4.10, capability
// team.admin). Every adapter that carries them reads a request the same
// way, the flags when one was given and the one stdin document otherwise,
// and refuses a malformed one with the same words. So the reading is here,
// once, and an adapter keeps only what is its own: the call to its
// backend. A principal_ref is a public identifier (it is in every roster
// row and every frame), so argv is fine for it; 4.5.14 is about secrets.

// usageError refuses argv the way every adapter does: `usage`, a fixed
// message that never echoes the argument, and details.reason
// invalid_arguments.
func usageError(message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeUsage,
		Message: message,
		Details: map[string]string{"reason": "invalid_arguments"},
	}
}

// flagsSeen reports which flags of a parsed set were given on argv.
func flagsSeen(fs *flag.FlagSet) map[string]bool {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	return seen
}

// TeamRevokeFlags is the flag form of `team revoke-member`: `--principal
// <principal_ref> [--ban]` or `--max-version <n>`.
type TeamRevokeFlags struct {
	set        *flag.FlagSet
	principal  *string
	ban        *bool
	maxVersion *int
}

// NewTeamRevokeFlags declares the three flags on fs, which the caller
// parses before it asks for the request.
func NewTeamRevokeFlags(fs *flag.FlagSet) *TeamRevokeFlags {
	return &TeamRevokeFlags{
		set:        fs,
		principal:  fs.String("principal", "", "the principal_ref to revoke (instead of stdin)"),
		ban:        fs.Bool("ban", false, "with --principal: also block a rejoin with the current secret"),
		maxVersion: fs.Int("max-version", 0, "revoke every member that joined at or below this secret version (instead of stdin)"),
	}
}

// Request resolves the request of a parsed `team revoke-member`: the flags
// first, stdin only when no flag was given (ReadInput refuses a terminal
// with `usage` instead of blocking). Both forms on argv, --ban without
// --principal, an empty --principal and a --max-version outside
// 0..2147483647 are `usage`; a stdin document that breaks the same rules
// is `invalid_input` naming the member (TeamRevokeMemberRequest.Validate).
func (f *TeamRevokeFlags) Request(stdin io.Reader) (*protocol.TeamRevokeMemberRequest, error) {
	seen := flagsSeen(f.set)
	switch {
	case seen["principal"] && seen["max-version"]:
		return nil, usageError("--principal and --max-version are two forms of one command; give one")
	case seen["ban"] && !seen["principal"]:
		return nil, usageError("--ban needs --principal (in the stdin form, set ban in the document)")
	case seen["principal"]:
		if *f.principal == "" {
			return nil, usageError("--principal was given an empty value")
		}
		return &protocol.TeamRevokeMemberRequest{PrincipalRef: *f.principal, Ban: *f.ban}, nil
	case seen["max-version"]:
		if *f.maxVersion < 0 || *f.maxVersion > protocol.MaxSecretVersion {
			return nil, usageError("--max-version must be between 0 and " + strconv.Itoa(protocol.MaxSecretVersion))
		}
		version := *f.maxVersion
		return &protocol.TeamRevokeMemberRequest{JoinedSecretVersionLTE: &version}, nil
	}
	data, err := ReadInput(stdin)
	if err != nil {
		return nil, err
	}
	req := &protocol.TeamRevokeMemberRequest{}
	if err := protocol.Decode(data, req); err != nil {
		return nil, err
	}
	return req, nil
}

// TeamTransferFlags is the flag form of `team transfer`: `--principal
// <principal_ref>`.
type TeamTransferFlags struct {
	set       *flag.FlagSet
	principal *string
}

// NewTeamTransferFlags declares --principal on fs, which the caller parses
// before it asks for the request.
func NewTeamTransferFlags(fs *flag.FlagSet) *TeamTransferFlags {
	return &TeamTransferFlags{
		set:       fs,
		principal: fs.String("principal", "", "the principal_ref of the new owner (instead of stdin)"),
	}
}

// Request resolves the request of a parsed `team transfer`: --principal
// when it was given (an empty value is `usage`), else the one stdin
// document, whose missing principal_ref is `invalid_input`.
func (f *TeamTransferFlags) Request(stdin io.Reader) (*protocol.TeamTransferRequest, error) {
	if flagsSeen(f.set)["principal"] {
		if *f.principal == "" {
			return nil, usageError("--principal was given an empty value")
		}
		return &protocol.TeamTransferRequest{PrincipalRef: *f.principal}, nil
	}
	data, err := ReadInput(stdin)
	if err != nil {
		return nil, err
	}
	req := &protocol.TeamTransferRequest{}
	if err := protocol.Decode(data, req); err != nil {
		return nil, err
	}
	return req, nil
}
