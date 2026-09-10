package protocol

import "time"

// The `event` discriminator values of `message watch` output (4.4.9).
// Unknown event kinds MUST be ignored by the receiver — logged, never
// fatal.
const (
	EventReady       = "ready"
	EventMessage     = "message"
	EventStatus      = "status"
	EventAcked       = "acked"
	EventHeartbeatOK = "heartbeat_ok"
	EventError       = "error"
)

// The `type` discriminator values of `message watch` stdin commands
// (4.4.9, capability message.watch.stdin_commands). Unknown command types
// MUST be ignored by the receiver.
const (
	CommandAck       = "ack"
	CommandHeartbeat = "heartbeat"
	CommandClose     = "close"
)

// The `mode` values of the watch `ready` event (4.4.9, P1-4 decision 4).
// An adapter that omits the message.watch.push capability MUST send
// polling (C-33); mode is a closed set and Validate rejects anything
// else.
const (
	WatchModePush    = "push"
	WatchModePolling = "polling"
)

// The `state` values of the watch `status` event this protocol version
// defines (4.4.9, P1-4 decision 4). The set is OPEN on the wire: a reader
// MUST ignore an unknown state rather than reject it (the same rule as
// for unknown event kinds), so Validate requires a non-empty state and
// Known reports whether it is one of these.
const (
	StatusStateLive    = "live"
	StatusStatePolling = "polling"
)

// WatchReady is the one-time first watch event, emitted when the initial
// catch-up starts (4.4.9, C-33).
type WatchReady struct {
	Event           string `json:"event"`
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	Mode            string `json:"mode"`
}

// Validate implements Validator.
func (w *WatchReady) Validate() error {
	if err := oneOf("event", w.Event, EventReady); err != nil {
		return err
	}
	if err := requireString("protocol_version", w.ProtocolVersion); err != nil {
		return err
	}
	if err := requireString("session_id", w.SessionID); err != nil {
		return err
	}
	return oneOf("mode", w.Mode, WatchModePush, WatchModePolling)
}

// WatchMessage delivers one accepted, unacknowledged message (4.4.9). The
// same message_id MAY be emitted more than once (4.5.2).
type WatchMessage struct {
	Event   string          `json:"event"`
	Message MessageEnvelope `json:"message"`
}

// Validate implements Validator. A failure inside the nested envelope is
// reported at its wire path, `message.<member>` (decision 3).
func (w *WatchMessage) Validate() error {
	if err := oneOf("event", w.Event, EventMessage); err != nil {
		return err
	}
	return w.Message.validate("message.")
}

// WatchStatus is an informational transport-state event (4.4.9).
type WatchStatus struct {
	Event  string `json:"event"`
	State  string `json:"state"`
	Detail string `json:"detail,omitzero"`
}

// Validate implements Validator. State is required but deliberately not
// enumerated here: the event is informational and an unknown state is
// IGNORED by the reader, not rejected (decision 4) — check Known after
// Validate and drop unknown states with a log line, exactly as for an
// unknown command type.
func (w *WatchStatus) Validate() error {
	if err := oneOf("event", w.Event, EventStatus); err != nil {
		return err
	}
	return requireString("state", w.State)
}

// Known reports whether the status state is one this protocol version
// defines (live or polling). A receiver MUST ignore an unknown state
// (4.4.9, decision 4).
func (w *WatchStatus) Known() bool {
	switch w.State {
	case StatusStateLive, StatusStatePolling:
		return true
	default:
		return false
	}
}

// WatchAcked answers a stdin `ack` command (4.4.9). Both lists are always
// emitted, as [] when empty.
type WatchAcked struct {
	Event      string   `json:"event"`
	MessageIDs []string `json:"message_ids"`
	Unknown    []string `json:"unknown"`
}

// Validate implements Validator.
func (w *WatchAcked) Validate() error {
	return oneOf("event", w.Event, EventAcked)
}

// WatchHeartbeatOK answers a stdin `heartbeat` command (4.4.9).
type WatchHeartbeatOK struct {
	Event      string    `json:"event"`
	SessionID  string    `json:"session_id"`
	State      string    `json:"state"`
	LeaseUntil time.Time `json:"lease_until"`
	ServerTime time.Time `json:"server_time"`
}

// Validate implements Validator.
func (w *WatchHeartbeatOK) Validate() error {
	if err := oneOf("event", w.Event, EventHeartbeatOK); err != nil {
		return err
	}
	if err := requireString("session_id", w.SessionID); err != nil {
		return err
	}
	if err := oneOf("state", w.State, SessionStateActive, SessionStateIdle, SessionStateOffline); err != nil {
		return err
	}
	if err := requireTime("lease_until", w.LeaseUntil); err != nil {
		return err
	}
	return requireTime("server_time", w.ServerTime)
}

// WatchError is a fatal or transient watch failure (4.4.9). An error with
// retryable false is followed by process exit with the matching exit
// code.
type WatchError struct {
	Event string      `json:"event"`
	Error ErrorObject `json:"error"`
}

// Validate implements Validator.
func (w *WatchError) Validate() error {
	if err := oneOf("event", w.Event, EventError); err != nil {
		return err
	}
	return w.Error.Validate()
}

// WatchCommand is one NDJSON command on `message watch` stdin (4.4.9). A
// single struct covers all three types; the members beyond Type belong to
// the type Validate switches on. The heartbeat members are those of
// HeartbeatRequest (4.4.4) less session_description — model and
// context_used_tokens included (C-44), with the same rules.
type WatchCommand struct {
	Type              string   `json:"type"`
	MessageIDs        []string `json:"message_ids,omitzero"`
	Activity          *string  `json:"activity,omitzero"`
	SessionName       *string  `json:"session_name,omitzero"`
	Inbound           *string  `json:"inbound,omitzero"`
	LeaseSeconds      *int     `json:"lease_seconds,omitzero"`
	Model             *string  `json:"model,omitzero"`
	ContextUsedTokens *int     `json:"context_used_tokens,omitzero"`
}

// Known reports whether the command type is one this protocol version
// defines. A receiver MUST ignore an unknown type (4.4.9) — check Known
// after Validate and drop unknown commands with a log line.
func (c *WatchCommand) Known() bool {
	switch c.Type {
	case CommandAck, CommandHeartbeat, CommandClose:
		return true
	default:
		return false
	}
}

// Validate implements Validator. An unknown Type is VALID by design —
// 4.4.9 makes ignoring it the receiver's duty, so rejecting it here would
// turn forward compatibility into a fatal error. Only a missing type is
// invalid.
func (c *WatchCommand) Validate() error {
	if err := requireString("type", c.Type); err != nil {
		return err
	}
	switch c.Type {
	case CommandAck:
		return requireIDs("message_ids", c.MessageIDs)
	case CommandHeartbeat:
		if c.Activity != nil {
			if err := oneOf("activity", *c.Activity, ActivityBusy, ActivityIdle); err != nil {
				return err
			}
		}
		if c.SessionName != nil {
			if err := requireString("session_name", *c.SessionName); err != nil {
				return err
			}
			if err := capRunes("session_name", *c.SessionName, MaxSessionNameCodepoints); err != nil {
				return err
			}
		}
		if c.Inbound != nil {
			if err := oneOf("inbound", *c.Inbound, InboundAccept, InboundHold, InboundRefuse); err != nil {
				return err
			}
		}
		if err := leaseSecondsInRange("lease_seconds", c.LeaseSeconds); err != nil {
			return err
		}
		if c.Model != nil {
			if err := optionalText("model", *c.Model, MaxModelChars); err != nil {
				return err
			}
		}
		return contextUsedTokensInRange(c.ContextUsedTokens)
	case CommandClose:
		return nil
	default:
		return nil
	}
}
