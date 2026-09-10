package supabase

import (
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The `session *` group of 4.4.2, 4.4.3 and 4.4.4 (plan task P2-8). Each
// verb is exactly one RPC of the finished migrations — register_session,
// session_heartbeat, list_sessions, close_session — and the RPC is the
// authority on ownership, membership and the computed state: this file
// only orders the local checks, names the arguments and shapes the
// result.
//
// The order of checks is the fs adapter's, which is what the suite
// asserts (brief section 3, C-02, C-06, C-13, C-19):
//
//  1. argv:   the flag set, then a missing --session (`usage`);
//  2. ladder: authenticate (no profile / no backend / no credential / no
//     team bound), so an unconfigured profile answers `config` or
//     `unauthenticated` even for a nonsense --session (C-06);
//  3. stdin:  the one document, its Validate and this adapter's lease
//     range (`invalid_input`);
//  4. local:  an id that is not uuid-shaped can name no session, so it is
//     the uniform not_found here rather than a 22P02 at the backend
//     (the suite's randomRef is 32 hex characters, not a uuid);
//  5. the RPC.

// sessionRegisterResult is the `session register` result: the 4.4.3
// SessionRecord plus the three members 4.4.2 adds. The record is embedded
// and json/v2 inlines it, so the wire document is one flat object — and
// the same shape decodes register_session's jsonb, which is
// brigade.session_record() || {resumed, lease_seconds, server_time}.
type sessionRegisterResult struct {
	protocol.SessionRecord
	Resumed      bool      `json:"resumed"`
	LeaseSeconds int       `json:"lease_seconds"`
	ServerTime   time.Time `json:"server_time"`
}

// sessionListResult is the `session list` result (4.4.3) and the shape of
// list_sessions' jsonb. Sessions is always present and is [] when empty
// (JSON convention 3).
type sessionListResult struct {
	TeamRef    string                   `json:"team_ref"`
	TeamName   string                   `json:"team_name"`
	ServerTime time.Time                `json:"server_time"`
	Sessions   []protocol.SessionRecord `json:"sessions"`
	Truncated  bool                     `json:"truncated"`
}

// sessionCloseResult is the `session close` result (4.4.3) and the shape
// of close_session's jsonb.
type sessionCloseResult struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

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
// C-31, C-42, C-44). An absent lease_seconds means lease.default_seconds,
// which this adapter sends explicitly rather than leaning on the RPC's own
// default, so `describe` and the backend can never disagree. A resume id
// that is not uuid-shaped is the uniform not_found before any dial, which
// is byte-identical to the backend's PT404 for a foreign or unknown id
// (C-19). model and context_used_tokens (C-44) go as p_model and
// p_context_used_tokens, null when absent: the registration is the
// session's whole state, and readInput's Validate has already applied the
// protocol's caps (max_model_chars, 0..MaxContextUsedTokens), so the RPC's
// own checks are the belt beneath.
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
	lease := protocol.DefaultLease()
	if err := lease.CheckSeconds("lease_seconds", req.LeaseSeconds); err != nil {
		return nil, err
	}
	seconds := lease.DefaultSeconds
	if req.LeaseSeconds != nil {
		seconds = *req.LeaseSeconds
	}
	args := rpcArgs{
		"p_team_id":             c.profile.TeamRef,
		"p_name":                req.SessionName,
		"p_description":         req.SessionDescription,
		"p_activity":            req.Activity,
		"p_inbound":             req.Inbound,
		"p_harness":             req.Harness,
		"p_harness_version":     req.HarnessVersion,
		"p_workspace_label":     req.WorkspaceLabel,
		"p_lease_seconds":       seconds,
		"p_model":               req.Model,
		"p_context_used_tokens": req.ContextUsedTokens,
	}
	if req.Resume != nil {
		if !validUUID(req.Resume.SessionID) {
			return nil, errNotFound()
		}
		args["p_resume_session_id"] = req.Resume.SessionID
	}
	out := &sessionRegisterResult{}
	if err := c.rpcAppended(c.ctx, "register_session", args, sessionAppendedParams, out); err != nil {
		return nil, err
	}
	return out, nil
}

// sessionHeartbeat implements 4.4.4 (C-13, C-15, C-42, C-44). Every member
// of the request is optional and absent means unchanged, which is exactly
// what session_heartbeat's coalesce() arguments express, so a nil pointer
// is sent as JSON null rather than as the current value — model and
// context_used_tokens included, which is how a heartbeat never clears them.
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
	if !validUUID(id) {
		return nil, errNotFound()
	}
	out := &protocol.HeartbeatResult{}
	err = c.rpcAppended(c.ctx, "session_heartbeat", rpcArgs{
		"p_session_id":          id,
		"p_activity":            req.Activity,
		"p_name":                req.SessionName,
		"p_description":         req.SessionDescription,
		"p_inbound":             req.Inbound,
		"p_lease_seconds":       req.LeaseSeconds,
		"p_model":               req.Model,
		"p_context_used_tokens": req.ContextUsedTokens,
	}, sessionAppendedParams, out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sessionList implements 4.4.3 (C-11, C-12, C-14, C-26, C-42). It reads
// no input at all: a command that takes none MUST NOT read stdin (4.1,
// B-1), because the harness hands it the null device and a human running
// it by hand would otherwise hang.
//
// is_self is computed HERE and never asked of the backend: `--session` is
// the caller's own claim about which of its sessions this process is, the
// backend has no way to know it, and list_sessions deliberately does not
// take it (one plan-cached RPC answer therefore serves every caller).
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
	err := c.rpc(c.ctx, "list_sessions", rpcArgs{
		"p_team_id":         c.profile.TeamRef,
		"p_include_offline": *includeOffline,
	}, out)
	if err != nil {
		return nil, err
	}
	if out.Sessions == nil {
		out.Sessions = []protocol.SessionRecord{}
	}
	if *session != "" {
		for i := range out.Sessions {
			out.Sessions[i].IsSelf = out.Sessions[i].SessionID == *session
		}
	}
	return out, nil
}

// sessionClose implements 4.4.3 (C-15): idempotent, and closing a session
// that is already closed is still a success with the identical result —
// close_session's `closed_at = coalesce(closed_at, now())` keeps the
// first close's instant and the answer carries no timestamp at all, so
// the two envelopes are byte-identical.
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
	if !validUUID(id) {
		return nil, errNotFound()
	}
	out := &sessionCloseResult{}
	if err := c.rpc(c.ctx, "close_session", rpcArgs{"p_session_id": id}, out); err != nil {
		return nil, err
	}
	return out, nil
}
