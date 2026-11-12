package rest

import (
	"github.com/appshapes/brigade/internal/protocol"
)

// The `session *` group of 4.4.2, 4.4.3 and 4.4.4. Each verb is exactly
// one request (the contract's section 2) and the server is the authority
// on ownership, membership, the lease and the computed state: this file
// only orders the local checks, builds the body and reads the result.
//
// The order of checks is the other adapters', which is what the suite
// asserts (C-02, C-06, C-13, C-19):
//
//  1. argv:   the flag set, then a missing --session (`usage`);
//  2. ladder: authenticate (no profile / no backend / no credential / no
//     team bound), so an unconfigured profile answers `config` or
//     `unauthenticated` even for a nonsense --session (C-06);
//  3. stdin:  the one document, its Validate and the lease range this
//     adapter advertises (`invalid_input`);
//  4. the request.

// sessionCommand dispatches the `session *` core group.
func (c *command) sessionCommand() (any, error) {
	switch c.verb {
	case "register":
		return c.sessionRegister()
	case "heartbeat":
		return c.sessionHeartbeat()
	case "list":
		return c.sessionList()
	case "close":
		return c.sessionClose()
	default:
		return nil, errUsage("unknown session verb")
	}
}

// sessionRegister implements 4.4.2 (C-10, C-11, C-16, C-17, C-19, C-19b,
// C-42, C-44..C-47). The body is the registration as it was validated,
// with the profile's team beside it: the team is what lets the server
// refuse a principal that is not an active member of the team its profile
// names (4.5.7, C-26).
func (c *command) sessionRegister() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	req := &protocol.SessionRegistration{}
	if err := c.readInput(req); err != nil {
		return nil, err
	}
	if err := protocol.DefaultLease().CheckSeconds("lease_seconds", req.LeaseSeconds); err != nil {
		return nil, err
	}
	out := &sessionRegisterResult{}
	body := &registerBody{SessionRegistration: *req, TeamRef: c.profile.TeamRef}
	if err := c.call(pathSessionRegister, body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// sessionHeartbeat implements 4.4.4 (C-13, C-15, C-42, C-44). Every member
// of the request is optional and absent means unchanged, so the document
// travels as it was given, under `heartbeat`, beside the session.
func (c *command) sessionHeartbeat() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "the session to renew")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	id, err := c.requireSessionFlag(*session)
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	req := &protocol.HeartbeatRequest{}
	if err := c.readInput(req); err != nil {
		return nil, err
	}
	if err := protocol.DefaultLease().CheckSeconds("lease_seconds", req.LeaseSeconds); err != nil {
		return nil, err
	}
	out := &protocol.HeartbeatResult{}
	if err := c.call(pathSessionHeartbeat, &heartbeatBody{SessionID: id, Heartbeat: req}, out); err != nil {
		return nil, err
	}
	return out, nil
}

// sessionList implements 4.4.3 (C-11, C-12, C-14, C-26, C-42). It reads no
// input at all: a command that takes none MUST NOT read stdin (4.1, B-1).
// `--session` travels as `session_id` and the server marks that record
// is_self.
func (c *command) sessionList() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "mark this session is_self")
	includeOffline := fs.Bool("include-offline", false, "include offline sessions")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	out := &sessionListResult{}
	body := &listBody{TeamRef: c.profile.TeamRef, SessionID: *session, IncludeOffline: *includeOffline}
	if err := c.call(pathSessionList, body, out); err != nil {
		return nil, err
	}
	return out, nil
}

// sessionClose implements 4.4.3 (C-15): idempotent, and closing a session
// that is already closed is still a success.
func (c *command) sessionClose() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "the session to close")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	id, err := c.requireSessionFlag(*session)
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	out := &sessionCloseResult{}
	if err := c.call(pathSessionClose, &sessionBody{SessionID: id}, out); err != nil {
		return nil, err
	}
	return out, nil
}
