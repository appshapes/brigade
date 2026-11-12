package rest

import (
	"context"

	"github.com/appshapes/brigade/internal/protocol"
)

// The `message send|receive|ack` verbs of 4.4.6, 4.4.7, 4.4.5 and 4.4.8.
// `watch` is not here: it writes NDJSON rather than a 4.3 envelope, so run
// calls it directly (watch.go).
//
// Each verb is one request and the server owns every rule the model must
// not be able to bend: the sender identity is stamped by the server
// (4.5.5), the uniform not_found covers an unowned sender, a recipient
// outside the team and an unreceived reply_to alike (4.5.6, 4.5.7), and
// the budgets and hop_count of 4.5.12 are the server's. This file does the
// local half: the ladder first, then the stdin document, whose Validate is
// where a forged `sender`, `principal_ref`, `human_label`, `team_ref`,
// `created_at` or `hop_count` is refused BEFORE any request (C-23).

// Receive paging bounds (4.1 and the contract's section 4): default 50,
// maximum 200.
const (
	receiveDefaultLimit = 50
	receiveMaxLimit     = 200
)

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
// before anything is sent, so a forged sender member or an oversize body
// costs no request at all (C-23); what travels is the validated request
// re-encoded, which by then carries none of the forbidden members.
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
	out := &protocol.SendResponse{}
	if err := c.call(pathMessageSend, req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// messageReceive implements 4.2's one-shot drain (C-19, C-20, C-21, C-30,
// C-31, C-32): the session's unacknowledged messages, oldest first, at
// most --limit of them, and it never acknowledges anything. It reads no
// stdin (4.1, B-1). The result is BAP/1's: the cursor the server answers
// with is the watch's and is not printed.
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
	ctx, cancel := context.WithTimeout(c.ctx, requestTimeout)
	defer cancel()
	answer, err := c.receive(ctx, &receiveBody{SessionID: id, Limit: *limit})
	if err != nil {
		return nil, err
	}
	return &messageReceiveResult{Messages: answer.Messages}, nil
}

// receive is one `/v1/message/receive`: the one-shot command's, and every
// request of the watch.
func (c *command) receive(ctx context.Context, body *receiveBody) (*receiveAnswer, error) {
	out := &receiveAnswer{}
	if err := c.do(ctx, &request{path: pathMessageReceive, in: body, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// messageAck implements 4.4.8 (C-30): idempotent and never an error for a
// message id the session does not own. Such an id comes back under
// `unknown`.
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
	out := &ackAnswer{}
	if err := c.call(pathMessageAck, &ackBody{SessionID: id, MessageIDs: req.MessageIDs}, out); err != nil {
		return nil, err
	}
	return out, nil
}
