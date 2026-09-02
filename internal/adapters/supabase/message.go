package supabase

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/appshapes/brigade/internal/protocol"
)

// The `message send|receive|ack` verbs of 4.4.6, 4.4.7, 4.4.5 and 4.4.8
// (plan task P2-9). `watch` is not here: it writes NDJSON rather than a
// 4.3 envelope, so run calls it directly (P2-10).
//
// Each verb is one RPC — send_message, fetch_inbox, ack_messages — and
// the RPC owns every rule the model must not be able to bend: the sender
// identity is stamped from auth.uid() and the sender session row (4.5.5),
// the uniform not_found covers an unowned sender, a recipient outside the
// team and an unreceived reply_to alike (4.5.6, 4.5.7), the five budgets
// of 4.5.12 are counted in one transaction, and hop_count is computed
// server-side. This file does the local half: the ladder first, then the
// stdin document — whose Validate is where a forged `sender`,
// `principal_ref`, `human_label`, `team_ref`, `created_at` or `hop_count`
// is refused, BEFORE any network call (C-23) — then the id shapes, then
// the call.

// Receive paging bounds (4.1): default 50, maximum 200. fetch_inbox caps
// its own p_limit at 200 as well, so the two agree.
const (
	receiveDefaultLimit = 50
	receiveMaxLimit     = 200
)

// messageReceiveResult is the `message receive` result (4.4.5). Messages
// is always present and is [] when empty (JSON convention 3, C-30).
type messageReceiveResult struct {
	Messages []protocol.MessageEnvelope `json:"messages"`
}

// messageCommand dispatches the `message *` core group.
func (c *command) messageCommand() (any, error) {
	switch c.verb {
	case "send":
		return c.messageSend()
	case "receive":
		return c.messageReceive()
	case "ack":
		return c.messageAck()
	default:
		return nil, errUsage("unknown message verb")
	}
}

// messageSend implements 4.4.6, 4.4.7 and 4.5.1/4.5.4/4.5.5/4.5.12
// (C-20..C-25, C-27..C-29b, C-31). The request is decoded and validated
// before any RPC is issued, so a forged sender member or an oversize body
// costs no network call at all (C-23), and an id that is not uuid-shaped
// is the uniform not_found here rather than a 22P02 at the backend — the
// suite's randomRef is 32 hex characters, and the envelope it produces
// must be byte-identical to the one a real but foreign id produces
// (C-24, C-25, C-29).
func (c *command) messageSend() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	req := &protocol.SendRequest{}
	if err := c.readInput(req); err != nil {
		return nil, err
	}
	if !validUUID(req.SenderSessionID) || !validUUID(req.RecipientSessionID) {
		return nil, errNotFound()
	}
	args := rpcArgs{
		"p_sender_session_id":    req.SenderSessionID,
		"p_recipient_session_id": req.RecipientSessionID,
		"p_body":                 req.Body,
		"p_summary":              nullable(req.Summary),
	}
	// 4.4.6 makes idempotency_key optional and send_message requires one:
	// a send with no key is its own logical message, so it gets a fresh
	// random key rather than a fingerprint of its payload — two identical
	// bodies sent deliberately twice are two messages (4.5.4 keys
	// idempotency on the CALLER's key, never on the content).
	key := req.IdempotencyKey
	if key == "" {
		fresh, err := newIdempotencyKey()
		if err != nil {
			return nil, err
		}
		key = fresh
	}
	args["p_idempotency_key"] = key
	if req.ReplyTo != "" {
		// A reply_to the sender never received is not_found (4.5.12);
		// one that cannot name a message at all is the same answer.
		if !validUUID(req.ReplyTo) {
			return nil, errNotFound()
		}
		args["p_reply_to"] = req.ReplyTo
	}
	out := &protocol.SendResponse{}
	if err := c.rpc(c.ctx, "send_message", args, out); err != nil {
		return nil, err
	}
	// send_message answers brigade.message_result(): the accepted status
	// of 4.4.7 is this adapter's to state, and it is the only status
	// protocol v1 defines (4.5.1: persisted, never "delivered").
	out.Status = protocol.SendStatusAccepted
	return out, nil
}

// messageReceive implements 4.2's one-shot drain (C-19, C-20, C-21, C-30,
// C-31, C-32): the session's unacknowledged messages, oldest first (seq),
// at most --limit of them, and it never acknowledges anything. It reads
// no stdin (4.1, B-1). A closed but owned session may still be drained
// (C-31): fetch_inbox checks ownership and an active membership, never
// closed_at.
func (c *command) messageReceive() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "the session whose inbox to drain")
	limit := fs.Int("limit", receiveDefaultLimit, "at most this many messages (1..200)")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	id, err := c.requireSessionFlag(*session)
	if err != nil {
		return nil, err
	}
	if *limit < 1 || *limit > receiveMaxLimit {
		return nil, errInvalidRange("limit", 1, receiveMaxLimit)
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	if !validUUID(id) {
		return nil, errNotFound()
	}
	var messages []protocol.MessageEnvelope
	if err := c.rpc(c.ctx, "fetch_inbox", rpcArgs{"p_session_id": id, "p_limit": *limit}, &messages); err != nil {
		return nil, err
	}
	if messages == nil {
		messages = []protocol.MessageEnvelope{}
	}
	return &messageReceiveResult{Messages: messages}, nil
}

// messageAck implements 4.4.8 (C-30): idempotent and never an error for a
// message id the session does not own — such an id comes back under
// `unknown`. An id that is not uuid-shaped is exactly that case, so it is
// answered locally instead of reaching a uuid[] argument as a 22P02, and
// the RPC still runs (with the uuid-shaped ids, possibly none) because
// ownership of the SESSION is checked there and must still be enforced.
func (c *command) messageAck() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "the session that acknowledges")
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
	req := &protocol.AckRequest{}
	if err := c.readInput(req); err != nil {
		return nil, err
	}
	if !validUUID(id) {
		return nil, errNotFound()
	}
	known := make([]string, 0, len(req.MessageIDs))
	unknown := make([]string, 0, len(req.MessageIDs))
	for _, m := range req.MessageIDs {
		if validUUID(m) {
			known = append(known, m)
		} else {
			unknown = append(unknown, m)
		}
	}
	out := &protocol.AckResult{}
	if err := c.rpc(c.ctx, "ack_messages", rpcArgs{"p_session_id": id, "p_message_ids": known}, out); err != nil {
		return nil, err
	}
	if out.Acked == nil {
		out.Acked = []string{}
	}
	out.Unknown = append(unknown, out.Unknown...)
	return out, nil
}

// nullable renders an optional text argument: an empty string is the
// backend's NULL, not an empty value, so `summary` absent from the
// request stays absent from the stored envelope.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// newIdempotencyKey mints the key a send with none of its own gets: 16
// random bytes as lowercase hex, well inside max_idempotency_key_chars.
func newIdempotencyKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errInternal("the adapter could not read random bytes")
	}
	return hex.EncodeToString(b[:]), nil
}
