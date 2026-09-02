package protocol

import (
	"encoding/json/jsontext"
	"time"
	"unicode/utf8"
)

// KindText is the only message kind of protocol v1 (4.5.11).
const KindText = "text"

// The `delivery_state` values of a MessageEnvelope: accepted (persisted,
// 4.5.1), injected (the terminal adapter state in v1) and processed
// (reserved).
const (
	DeliveryStateAccepted  = "accepted"
	DeliveryStateInjected  = "injected"
	DeliveryStateProcessed = "processed"
)

// SendStatusAccepted is the only `status` of a SendResponse (4.4.7): the
// message is persisted such that a later receive returns it. The product
// never says "delivered" (4.5.1).
const SendStatusAccepted = "accepted"

// Sender is the adapter-stamped `sender` member of a MessageEnvelope
// (4.4.5). Every member comes from the adapter's own authority, never
// from the sending caller (4.5.5).
type Sender struct {
	PrincipalRef string `json:"principal_ref"`
	HumanLabel   string `json:"human_label,omitzero"`
	SessionID    string `json:"session_id"`
	SessionName  string `json:"session_name"`
}

// MessageEnvelope is the only representation of a message anywhere in the
// protocol (4.4.5, adapter → harness). body, summary and every sender
// string are untrusted input at every layer (4.5.11).
type MessageEnvelope struct {
	ProtocolVersion    string    `json:"protocol_version"`
	Kind               string    `json:"kind"`
	MessageID          string    `json:"message_id"`
	TeamRef            string    `json:"team_ref"`
	Sender             Sender    `json:"sender"`
	RecipientSessionID string    `json:"recipient_session_id"`
	Summary            string    `json:"summary,omitzero"`
	Body               string    `json:"body"`
	ReplyTo            *string   `json:"reply_to,omitzero"`
	HopCount           int       `json:"hop_count"`
	CreatedAt          time.Time `json:"created_at"`
	DeliveryState      string    `json:"delivery_state"`
}

// Validate implements Validator.
func (m *MessageEnvelope) Validate() error { return m.validate("") }

// validate checks the envelope with every reported member name prefixed
// by at: "" when the envelope is the whole document, "message." when it
// is the `message` member of a watch event — details.field is the wire
// path, dotted for nesting (P1-4 decision 3).
func (m *MessageEnvelope) validate(at string) error {
	if err := requireString(at+"protocol_version", m.ProtocolVersion); err != nil {
		return err
	}
	if err := oneOf(at+"kind", m.Kind, KindText); err != nil {
		return err
	}
	if err := requireString(at+"message_id", m.MessageID); err != nil {
		return err
	}
	if err := requireString(at+"team_ref", m.TeamRef); err != nil {
		return err
	}
	if err := requireString(at+"sender.principal_ref", m.Sender.PrincipalRef); err != nil {
		return err
	}
	if err := optionalText(at+"sender.human_label", m.Sender.HumanLabel, MaxHumanLabelChars); err != nil {
		return err
	}
	if err := requireString(at+"sender.session_id", m.Sender.SessionID); err != nil {
		return err
	}
	if err := requireString(at+"sender.session_name", m.Sender.SessionName); err != nil {
		return err
	}
	if err := capRunes(at+"sender.session_name", m.Sender.SessionName, MaxSessionNameCodepoints); err != nil {
		return err
	}
	if err := requireString(at+"recipient_session_id", m.RecipientSessionID); err != nil {
		return err
	}
	if err := optionalText(at+"summary", m.Summary, MaxSummaryChars); err != nil {
		return err
	}
	if err := requireString(at+"body", m.Body); err != nil {
		return err
	}
	if err := capBytes(at+"body", m.Body, MaxBodyBytes); err != nil {
		return err
	}
	if m.ReplyTo != nil && *m.ReplyTo == "" {
		return errRequired(at + "reply_to")
	}
	if err := hopCountInRange(at+"hop_count", m.HopCount); err != nil {
		return err
	}
	if err := requireTime(at+"created_at", m.CreatedAt); err != nil {
		return err
	}
	return oneOf(at+"delivery_state", m.DeliveryState,
		DeliveryStateAccepted, DeliveryStateInjected, DeliveryStateProcessed)
}

// SendRequest is the `message send` request (4.4.6, harness → adapter).
//
// It is the one shape where loose parsing has a named exception (C-23):
// the adapter stamps the sender identity from its own authority (4.5.5),
// so a request carrying `sender`, `principal_ref`, `human_label`,
// `team_ref`, `created_at` or `hop_count` is rejected with invalid_input
// rather than silently ignored — ignoring a forged sender field would
// hide a client bug. The forbidden members are declared as jsontext.Value
// so their mere presence (any value, including null) is observable.
type SendRequest struct {
	SenderSessionID    string `json:"sender_session_id"`
	RecipientSessionID string `json:"recipient_session_id"`
	Body               string `json:"body"`
	Summary            string `json:"summary,omitzero"`
	ReplyTo            string `json:"reply_to,omitzero"`
	IdempotencyKey     string `json:"idempotency_key,omitzero"`

	// The forbidden members of C-23. Never set these when building a
	// request; Validate rejects a non-empty one.
	ForbiddenSender       jsontext.Value `json:"sender,omitzero"`
	ForbiddenPrincipalRef jsontext.Value `json:"principal_ref,omitzero"`
	ForbiddenHumanLabel   jsontext.Value `json:"human_label,omitzero"`
	ForbiddenTeamRef      jsontext.Value `json:"team_ref,omitzero"`
	ForbiddenCreatedAt    jsontext.Value `json:"created_at,omitzero"`
	ForbiddenHopCount     jsontext.Value `json:"hop_count,omitzero"`
}

// Validate implements Validator.
func (r *SendRequest) Validate() error {
	for _, f := range []struct {
		field string
		value jsontext.Value
	}{
		{"sender", r.ForbiddenSender},
		{"principal_ref", r.ForbiddenPrincipalRef},
		{"human_label", r.ForbiddenHumanLabel},
		{"team_ref", r.ForbiddenTeamRef},
		{"created_at", r.ForbiddenCreatedAt},
		{"hop_count", r.ForbiddenHopCount},
	} {
		if len(f.value) > 0 {
			return errForbiddenMember(f.field)
		}
	}
	if err := requireString("sender_session_id", r.SenderSessionID); err != nil {
		return err
	}
	if err := requireString("recipient_session_id", r.RecipientSessionID); err != nil {
		return err
	}
	if err := requireString("body", r.Body); err != nil {
		return err
	}
	if err := capBytes("body", r.Body, MaxBodyBytes); err != nil {
		return err
	}
	if err := optionalText("summary", r.Summary, MaxSummaryChars); err != nil {
		return err
	}
	if r.ReplyTo != "" && !utf8.ValidString(r.ReplyTo) {
		return errNotUTF8("reply_to")
	}
	return optionalText("idempotency_key", r.IdempotencyKey, MaxIdempotencyKeyChars)
}

// SendResponse is the `message send` result (4.4.7). Duplicate true means
// the idempotency key matched an existing message and MessageID is that
// message's id (4.5.4).
type SendResponse struct {
	Status             string    `json:"status"`
	MessageID          string    `json:"message_id"`
	RecipientSessionID string    `json:"recipient_session_id"`
	CreatedAt          time.Time `json:"created_at"`
	Duplicate          bool      `json:"duplicate"`
	HopCount           int       `json:"hop_count"`
}

// Validate implements Validator.
func (r *SendResponse) Validate() error {
	if err := oneOf("status", r.Status, SendStatusAccepted); err != nil {
		return err
	}
	if err := requireString("message_id", r.MessageID); err != nil {
		return err
	}
	if err := requireString("recipient_session_id", r.RecipientSessionID); err != nil {
		return err
	}
	if err := requireTime("created_at", r.CreatedAt); err != nil {
		return err
	}
	return hopCountInRange("hop_count", r.HopCount)
}

// AckRequest is the `message ack` request (4.4.8); the session comes from
// --session.
type AckRequest struct {
	MessageIDs []string `json:"message_ids"`
}

// Validate implements Validator.
func (r *AckRequest) Validate() error {
	return requireIDs("message_ids", r.MessageIDs)
}

// AckResult is the `message ack` result (4.4.8). An already-acknowledged
// owned id counts as acked; ids not owned or not found are unknown, never
// errors. Both members are always emitted, as [] when empty.
type AckResult struct {
	Acked   []string `json:"acked"`
	Unknown []string `json:"unknown"`
}

// Validate implements Validator. Empty lists are valid; a present id must
// be a non-empty string.
func (r *AckResult) Validate() error {
	for _, field := range []struct {
		name string
		ids  []string
	}{{"acked", r.Acked}, {"unknown", r.Unknown}} {
		for _, id := range field.ids {
			if id == "" || !utf8.ValidString(id) {
				return errInvalidValue(field.name, "non-empty message ids")
			}
		}
	}
	return nil
}
