package fs

import (
	"errors"
	iofs "io/fs"
	"path/filepath"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// capRetryAfterMS is the retry hint for the two unacknowledged-message
// caps of 4.5.12. Unlike a rate window, a cap clears when the recipient
// acknowledges, which the adapter cannot predict; five seconds is a
// polite, positive value, and 4.6 requires only that it be > 0.
const capRetryAfterMS = 5000

// messageReceiveResult is the `message receive` result (4.4.5). Messages
// is always present and is [] when empty (JSON convention 3, C-30).
type messageReceiveResult struct {
	Messages []protocol.MessageEnvelope `json:"messages"`
}

// messageCommand dispatches the `message *` core group. `watch` is not
// here: it writes NDJSON rather than a 4.3 envelope, so run calls it
// directly.
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
// before the store is opened at all, so a forged sender member or an
// oversize body is refused without any store access (C-23).
func (c *command) messageSend() (any, error) {
	if err := c.parse(newFlags()); err != nil {
		return nil, err
	}
	if err := c.authenticate(true); err != nil {
		return nil, err
	}
	data, err := adapterkit.ReadInput(c.stdin)
	if err != nil {
		return nil, err
	}
	req, forged, err := decodeSendRequest(data)
	if err != nil {
		return nil, err
	}
	if err := c.authorize(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	sender, err := resolveSenderSession(c.st, teamRef, c.cred.PrincipalRef, req, forged)
	if err != nil {
		return nil, err
	}
	if sender.ClosedAt != nil {
		return nil, errConflict("the sending session is closed", reasonSessionClosed)
	}
	if err := c.st.requireRecipient(teamRef, req.RecipientSessionID); err != nil {
		return nil, err
	}
	fingerprint := sha256hex(req.RecipientSessionID + "\x00" + req.Body + "\x00" + req.Summary + "\x00" + req.ReplyTo)
	idemFile := ""
	if req.IdempotencyKey != "" {
		idemFile = filepath.Join(c.st.idemDir(teamRef, sender.SessionID), sha256hex(req.IdempotencyKey)+jsonExt)
		done, err := replayIdempotent(idemFile, fingerprint)
		if err != nil {
			return nil, err
		}
		if done != nil {
			return done, nil
		}
	}
	hop, err := c.st.hopCount(teamRef, sender.SessionID, req)
	if err != nil {
		return nil, err
	}
	if err := c.st.checkSendRate(teamRef, sender, req.RecipientSessionID); err != nil {
		return nil, err
	}
	return c.st.persist(teamRef, sender, req, hop, fingerprint, idemFile)
}

// replayIdempotent answers a repeated send (4.5.4, C-21, C-22): the same
// key with the same payload returns the ORIGINAL response with duplicate
// true and creates no second message; the same key with a different
// payload is `conflict`.
func replayIdempotent(path, fingerprint string) (*protocol.SendResponse, error) {
	var rec idemRecord
	err := readJSON(path, &rec)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.Fingerprint != fingerprint {
		return nil, errConflict("this idempotency key was used for a different message", reasonIdempotencyKeyReused)
	}
	return &protocol.SendResponse{
		Status:             protocol.SendStatusAccepted,
		MessageID:          rec.MessageID,
		RecipientSessionID: rec.RecipientSessionID,
		CreatedAt:          rec.CreatedAt,
		Duplicate:          true,
		HopCount:           rec.HopCount,
	}, nil
}

// hopCount implements the loop control of 4.5.12 (C-29, C-29b): one more
// than the hop count of the message named by `reply_to`, which MUST be one
// the sender session received; or, with no `reply_to`, one more than the
// most recent message the recipient sent to the sender within
// implicit_reply_window_seconds — an unlabelled answer is still an answer;
// or 0. A value past max_hop_count is `loop_detected`.
func (s *store) hopCount(team, sender string, req *protocol.SendRequest) (int, error) {
	received, err := s.receivedBy(team, sender)
	if err != nil {
		return 0, err
	}
	hop := 0
	if req.ReplyTo != "" {
		found := false
		for i := range received {
			if received[i].MessageID == req.ReplyTo {
				hop, found = received[i].HopCount+1, true
				break
			}
		}
		if !found {
			return 0, errNotFound()
		}
	} else {
		window := time.Duration(protocol.ImplicitReplyWindowSeconds) * time.Second
		cutoff := s.now().Add(-window)
		// received is in send order, so the LAST match is the most
		// recent one — which is exact even when two messages share a
		// created_at, as they do whenever the clock is frozen.
		var latest *storedMessage
		for i := range received {
			m := &received[i]
			if m.Sender.SessionID == req.RecipientSessionID && m.CreatedAt.After(cutoff) {
				latest = m
			}
		}
		if latest != nil {
			hop = latest.HopCount + 1
		}
	}
	if hop > protocol.MaxHopCount {
		return 0, &protocol.Error{
			Code:    protocol.CodeLoopDetected,
			Message: "the reply chain has reached the hop-count cap",
			Details: map[string]string{"reason": "max_hop_count"},
		}
	}
	return hop, nil
}

// checkSendRate applies the five budgets of 4.5.12 in the order the spec
// fixes (C-28): the sender session's minute then hour budget, the
// principal's minute then hour budget, then the per-pair unacknowledged
// cap BEFORE the recipient-wide one, so one sender cannot exhaust a
// recipient's inbox for everyone else.
func (s *store) checkSendRate(team string, sender *sessionFile, recipient string) error {
	all, err := s.teamMessages(team)
	if err != nil {
		return err
	}
	now := s.now()
	limits := protocol.DefaultLimits()
	for _, w := range []struct {
		reason    string
		limit     int
		window    time.Duration
		principal bool
	}{
		{reasonSendPerMinute, limits.SendRate.PerMinute, time.Minute, false},
		{reasonSendPerHour, limits.SendRate.PerHour, time.Hour, false},
		{reasonPrincipalPerMinute, limits.PrincipalSendRate.PerMinute, time.Minute, true},
		{reasonPrincipalPerHour, limits.PrincipalSendRate.PerHour, time.Hour, true},
	} {
		cutoff := now.Add(-w.window)
		count := 0
		var oldest time.Time
		for i := range all {
			m := &all[i]
			matches := m.Sender.SessionID == sender.SessionID
			if w.principal {
				matches = m.Sender.PrincipalRef == sender.PrincipalRef
			}
			if !matches || !m.CreatedAt.After(cutoff) {
				continue
			}
			count++
			if oldest.IsZero() || m.CreatedAt.Before(oldest) {
				oldest = m.CreatedAt
			}
		}
		if count >= w.limit {
			return errRateLimited(w.reason, int(oldest.Add(w.window).Sub(now).Milliseconds()))
		}
	}
	pending, err := s.inboxMessages(team, recipient)
	if err != nil {
		return err
	}
	fromSender := 0
	for i := range pending {
		if pending[i].Sender.SessionID == sender.SessionID {
			fromSender++
		}
	}
	if fromSender >= limits.MaxUnackedPerSenderRecipient {
		return errRateLimited(reasonSenderQuotaForRecipient, capRetryAfterMS)
	}
	if len(pending) >= limits.MaxUnackedPerRecipient {
		return errRateLimited(reasonRecipientInboxFull, capRetryAfterMS)
	}
	return nil
}

// persist writes the message (4.5.1: `ok: true` only after a later
// `message receive` would return it), then the idempotency record, then
// touches the sender's last_seen_at. Every identity member of the envelope
// is stamped from the SENDER SESSION FILE and its member record, never
// from the request (4.5.5, C-20).
func (s *store) persist(
	team string, sender *sessionFile, req *protocol.SendRequest,
	hop int, fingerprint, idemFile string,
) (*protocol.SendResponse, error) {
	id, err := newRef()
	if err != nil {
		return nil, err
	}
	label := ""
	if m, ok, err := s.loadMember(team, sender.PrincipalRef); err != nil {
		return nil, err
	} else if ok {
		label = m.HumanLabel
	}
	now := s.now().UTC()
	var replyTo *string
	if req.ReplyTo != "" {
		value := req.ReplyTo
		replyTo = &value
	}
	msg := &storedMessage{MessageEnvelope: protocol.MessageEnvelope{
		ProtocolVersion: protocol.ProtocolVersion,
		Kind:            protocol.KindText,
		MessageID:       id,
		TeamRef:         team,
		Sender: protocol.Sender{
			PrincipalRef: sender.PrincipalRef, HumanLabel: label,
			SessionID: sender.SessionID, SessionName: sender.SessionName,
		},
		RecipientSessionID: req.RecipientSessionID,
		Summary:            req.Summary,
		Body:               req.Body,
		ReplyTo:            replyTo,
		HopCount:           hop,
		CreatedAt:          now,
		DeliveryState:      protocol.DeliveryStateAccepted,
	}}
	if err := msg.Validate(); err != nil {
		return nil, err
	}
	seq, err := s.nextSeq(team, req.RecipientSessionID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.inboxDir(team, req.RecipientSessionID), seq+"."+id+jsonExt)
	if err := writeJSON(path, msg); err != nil {
		return nil, err
	}
	if idemFile != "" {
		rec := &idemRecord{
			MessageID: id, Fingerprint: fingerprint,
			RecipientSessionID: req.RecipientSessionID, HopCount: hop, CreatedAt: now,
		}
		if err := writeJSON(idemFile, rec); err != nil {
			return nil, err
		}
	}
	sender.LastSeenAt = now
	if err := s.saveSession(team, sender); err != nil {
		return nil, err
	}
	return &protocol.SendResponse{
		Status:             protocol.SendStatusAccepted,
		MessageID:          id,
		RecipientSessionID: req.RecipientSessionID,
		CreatedAt:          now,
		Duplicate:          false,
		HopCount:           hop,
	}, nil
}

// messageReceive implements 4.2's one-shot drain (C-19, C-20, C-21, C-30,
// C-32): the session's pending messages, oldest first, at most --limit of
// them, and it never acknowledges anything.
func (c *command) messageReceive() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "the session whose inbox to drain")
	limit := fs.Int("limit", 50, "how many messages at most (1-200)")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	id, err := c.requireSessionFlag(*session)
	if err != nil {
		return nil, err
	}
	if *limit < 1 || *limit > 200 {
		return nil, errInvalidRange("limit", 1, 200)
	}
	if err := c.teamScoped(); err != nil {
		return nil, err
	}
	if _, err := c.st.ownedSession(c.profile.TeamRef, c.cred.PrincipalRef, id); err != nil {
		return nil, err
	}
	pending, err := c.st.inboxMessages(c.profile.TeamRef, id)
	if err != nil {
		return nil, err
	}
	if len(pending) > *limit {
		pending = pending[:*limit]
	}
	out := make([]protocol.MessageEnvelope, 0, len(pending))
	for i := range pending {
		out = append(out, pending[i].MessageEnvelope)
	}
	return &messageReceiveResult{Messages: out}, nil
}

// messageAck implements 4.4.8 (C-30).
func (c *command) messageAck() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "the session acknowledging")
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
	data, err := adapterkit.ReadInput(c.stdin)
	if err != nil {
		return nil, err
	}
	req := &protocol.AckRequest{}
	if err := protocol.Decode(data, req); err != nil {
		return nil, err
	}
	if err := c.authorize(); err != nil {
		return nil, err
	}
	if _, err := c.st.ownedSession(c.profile.TeamRef, c.cred.PrincipalRef, id); err != nil {
		return nil, err
	}
	return ackMessages(c.st, c.profile.TeamRef, id, req.MessageIDs)
}
