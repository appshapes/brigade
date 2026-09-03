package adapterclient

import (
	"context"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// This file holds the typed command helpers (brief 2.2): each builds the
// argv, calls the adapter, and decodes and validates the `result` into the
// protocol types P3-3/P3-4/P3-5 consume. A helper returns the *protocol.Error
// Call built (whether the adapter spoke or broke); on success it returns the
// decoded result.

// RegisterResult is the `session register` result (4.4.2): a SessionRecord
// with the three registration-only members.
type RegisterResult struct {
	protocol.SessionRecord
	Resumed      bool      `json:"resumed"`
	LeaseSeconds int       `json:"lease_seconds"`
	ServerTime   time.Time `json:"server_time"`
}

// ListResult is the `session list` result (4.4.3).
type ListResult struct {
	TeamRef    string                   `json:"team_ref"`
	TeamName   string                   `json:"team_name"`
	ServerTime time.Time                `json:"server_time"`
	Sessions   []protocol.SessionRecord `json:"sessions"`
	Truncated  bool                     `json:"truncated"`
}

// CloseResult is the `session close` result (4.4.3).
type CloseResult struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// ReceiveResult is the `message receive` result (4.4.5).
type ReceiveResult struct {
	Messages []protocol.MessageEnvelope `json:"messages"`
}

// Member is one roster entry of the `team members` result (4.4.10).
type Member struct {
	PrincipalRef string     `json:"principal_ref"`
	HumanLabel   string     `json:"human_label,omitzero"`
	Status       string     `json:"status"`
	JoinedAt     time.Time  `json:"joined_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	SessionCount int        `json:"session_count"`
}

// MembersResult is the `team members` result (4.4.10, C-43).
type MembersResult struct {
	TeamRef    string    `json:"team_ref"`
	TeamName   string    `json:"team_name"`
	ServerTime time.Time `json:"server_time"`
	Members    []Member  `json:"members"`
}

// Register runs `session register` (SessionStart). The caller sets ctx's
// deadline (RegisterTimeout).
func (c *Client) Register(ctx context.Context, reg *protocol.SessionRegistration) (*RegisterResult, error) {
	env, err := c.Call(ctx, "session", "register", nil, reg)
	if err != nil {
		return nil, err
	}
	var out RegisterResult
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	if verr := out.Validate(); verr != nil {
		return nil, resultInvalid("session register")
	}
	return &out, nil
}

// Heartbeat runs `session heartbeat --session <id>`.
func (c *Client) Heartbeat(ctx context.Context, sessionID string, hb *protocol.HeartbeatRequest) (*protocol.HeartbeatResult, error) {
	env, err := c.Call(ctx, "session", "heartbeat", sessionFlag(sessionID), hb)
	if err != nil {
		return nil, err
	}
	var out protocol.HeartbeatResult
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	if verr := out.Validate(); verr != nil {
		return nil, resultInvalid("session heartbeat")
	}
	return &out, nil
}

// Close runs `session close --session <id>` (SessionEnd; CloseTimeout).
func (c *Client) Close(ctx context.Context, sessionID string) (*CloseResult, error) {
	env, err := c.Call(ctx, "session", "close", sessionFlag(sessionID), nil)
	if err != nil {
		return nil, err
	}
	var out CloseResult
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	if out.SessionID == "" {
		return nil, resultInvalid("session close")
	}
	return &out, nil
}

// ListSessions runs `session list [--include-offline]`.
func (c *Client) ListSessions(ctx context.Context, includeOffline bool) (*ListResult, error) {
	var flags []string
	if includeOffline {
		flags = []string{"--include-offline"}
	}
	env, err := c.Call(ctx, "session", "list", flags, nil)
	if err != nil {
		return nil, err
	}
	var out ListResult
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	for i := range out.Sessions {
		if verr := out.Sessions[i].Validate(); verr != nil {
			return nil, resultInvalid("session list")
		}
	}
	return &out, nil
}

// Send runs `message send` (the sender is a member of the request).
func (c *Client) Send(ctx context.Context, req *protocol.SendRequest) (*protocol.SendResponse, error) {
	env, err := c.Call(ctx, "message", "send", nil, req)
	if err != nil {
		return nil, err
	}
	var out protocol.SendResponse
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	if verr := out.Validate(); verr != nil {
		return nil, resultInvalid("message send")
	}
	return &out, nil
}

// Receive runs `message receive --session <id> [--limit <n>]`.
func (c *Client) Receive(ctx context.Context, sessionID string, limit int) (*ReceiveResult, error) {
	flags := sessionFlag(sessionID)
	if limit > 0 {
		flags = append(flags, "--limit", strconv.Itoa(limit))
	}
	env, err := c.Call(ctx, "message", "receive", flags, nil)
	if err != nil {
		return nil, err
	}
	var out ReceiveResult
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	for i := range out.Messages {
		if verr := out.Messages[i].Validate(); verr != nil {
			return nil, resultInvalid("message receive")
		}
	}
	return &out, nil
}

// Ack runs `message ack --session <id>`.
func (c *Client) Ack(ctx context.Context, sessionID string, req *protocol.AckRequest) (*protocol.AckResult, error) {
	env, err := c.Call(ctx, "message", "ack", sessionFlag(sessionID), req)
	if err != nil {
		return nil, err
	}
	var out protocol.AckResult
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	if verr := out.Validate(); verr != nil {
		return nil, resultInvalid("message ack")
	}
	return &out, nil
}

// TeamMembers runs `team members` (capability team.roster).
func (c *Client) TeamMembers(ctx context.Context) (*MembersResult, error) {
	env, err := c.Call(ctx, "team", "members", nil, nil)
	if err != nil {
		return nil, err
	}
	var out MembersResult
	if derr := decodeResult(env, &out); derr != nil {
		return nil, derr
	}
	return &out, nil
}

// decodeResult loosely parses an OK envelope's result into v. A malformed
// result is `internal`: the adapter broke its own output.
func decodeResult(env *protocol.Envelope, v any) error {
	if err := protocol.Unmarshal([]byte(env.Result), v); err != nil {
		return &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "the adapter result is not a valid document",
			Details: map[string]string{"reason": "result_malformed"},
		}
	}
	return nil
}

// resultInvalid is the failure when a decoded result does not satisfy its
// protocol shape. It is `internal`: the value the adapter returned is not a
// valid protocol document.
func resultInvalid(command string) error {
	return &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: "the adapter returned an invalid result for " + command,
		Details: map[string]string{"reason": "result_invalid"},
	}
}

// sessionFlag builds the --session argv flag.
func sessionFlag(id string) []string { return []string{"--session", id} }
