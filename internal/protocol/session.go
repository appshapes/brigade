package protocol

import "time"

// The `activity` values a harness reports (4.4.2, 4.5.8).
const (
	ActivityBusy = "busy"
	ActivityIdle = "idle"
)

// The `inbound` policy values (4.4.2).
const (
	InboundAccept = "accept"
	InboundHold   = "hold"
	InboundRefuse = "refuse"
)

// The computed session `state` values (4.5.8). The adapter computes them
// from lease_until, closed_at and activity with its own clock; the harness
// never does.
const (
	SessionStateActive  = "active"
	SessionStateIdle    = "idle"
	SessionStateOffline = "offline"
)

// ResumeRef is the optional `resume` member of SessionRegistration
// (4.4.2, capability session.resume).
type ResumeRef struct {
	SessionID string `json:"session_id"`
}

// SessionRegistration is the `session register` request (4.4.2,
// harness → adapter). It deliberately has no member for a native session
// id, cwd, hostname, username or transcript path (threat model T10):
// adding one is a spec change, not a convenience.
type SessionRegistration struct {
	Harness            string     `json:"harness"`
	HarnessVersion     string     `json:"harness_version"`
	SessionName        string     `json:"session_name"`
	SessionDescription *string    `json:"session_description,omitzero"`
	Activity           string     `json:"activity"`
	Inbound            string     `json:"inbound"`
	LeaseSeconds       *int       `json:"lease_seconds,omitzero"`
	WorkspaceLabel     *string    `json:"workspace_label,omitzero"`
	Resume             *ResumeRef `json:"resume,omitzero"`
}

// Validate implements Validator.
func (r *SessionRegistration) Validate() error {
	if err := requireString("harness", r.Harness); err != nil {
		return err
	}
	if err := requireString("harness_version", r.HarnessVersion); err != nil {
		return err
	}
	if err := requireString("session_name", r.SessionName); err != nil {
		return err
	}
	if err := capRunes("session_name", r.SessionName, MaxSessionNameCodepoints); err != nil {
		return err
	}
	if r.SessionDescription != nil {
		if err := optionalText("session_description", *r.SessionDescription, MaxDescriptionChars); err != nil {
			return err
		}
	}
	if err := oneOf("activity", r.Activity, ActivityBusy, ActivityIdle); err != nil {
		return err
	}
	if err := oneOf("inbound", r.Inbound, InboundAccept, InboundHold, InboundRefuse); err != nil {
		return err
	}
	if err := leaseSecondsInRange("lease_seconds", r.LeaseSeconds); err != nil {
		return err
	}
	if r.WorkspaceLabel != nil {
		if err := optionalText("workspace_label", *r.WorkspaceLabel, MaxWorkspaceLabelChars); err != nil {
			return err
		}
	}
	if r.Resume != nil {
		if err := requireString("resume.session_id", r.Resume.SessionID); err != nil {
			return err
		}
	}
	return nil
}

// SessionRecord is one session as the adapter reports it (4.4.3,
// adapter → harness). human_label is unverified and every consumer MUST
// present it as such; it and session_name are untrusted input at every
// layer (4.5.11).
type SessionRecord struct {
	SessionID          string    `json:"session_id"`
	SessionName        string    `json:"session_name"`
	SessionDescription *string   `json:"session_description,omitzero"`
	PrincipalRef       string    `json:"principal_ref"`
	HumanLabel         string    `json:"human_label,omitzero"`
	State              string    `json:"state"`
	Activity           string    `json:"activity"`
	Inbound            string    `json:"inbound"`
	LastSeenAt         time.Time `json:"last_seen_at"`
	LeaseUntil         time.Time `json:"lease_until"`
	Harness            string    `json:"harness,omitzero"`
	HarnessVersion     string    `json:"harness_version,omitzero"`
	WorkspaceLabel     *string   `json:"workspace_label,omitzero"`
	CreatedAt          time.Time `json:"created_at"`
	IsSelf             bool      `json:"is_self"`
}

// Validate implements Validator.
func (s *SessionRecord) Validate() error {
	if err := requireString("session_id", s.SessionID); err != nil {
		return err
	}
	if err := requireString("session_name", s.SessionName); err != nil {
		return err
	}
	if err := capRunes("session_name", s.SessionName, MaxSessionNameCodepoints); err != nil {
		return err
	}
	if s.SessionDescription != nil {
		if err := optionalText("session_description", *s.SessionDescription, MaxDescriptionChars); err != nil {
			return err
		}
	}
	if err := requireString("principal_ref", s.PrincipalRef); err != nil {
		return err
	}
	if err := optionalText("human_label", s.HumanLabel, MaxHumanLabelChars); err != nil {
		return err
	}
	if err := oneOf("state", s.State, SessionStateActive, SessionStateIdle, SessionStateOffline); err != nil {
		return err
	}
	if err := oneOf("activity", s.Activity, ActivityBusy, ActivityIdle); err != nil {
		return err
	}
	if err := oneOf("inbound", s.Inbound, InboundAccept, InboundHold, InboundRefuse); err != nil {
		return err
	}
	if err := requireTime("last_seen_at", s.LastSeenAt); err != nil {
		return err
	}
	if err := requireTime("lease_until", s.LeaseUntil); err != nil {
		return err
	}
	if s.WorkspaceLabel != nil {
		if err := optionalText("workspace_label", *s.WorkspaceLabel, MaxWorkspaceLabelChars); err != nil {
			return err
		}
	}
	return requireTime("created_at", s.CreatedAt)
}

// HeartbeatRequest is the `session heartbeat` request (4.4.4). Every
// member is optional — the session comes from --session — and an absent
// member means "unchanged", which is why each is a pointer.
type HeartbeatRequest struct {
	Activity           *string `json:"activity,omitzero"`
	SessionName        *string `json:"session_name,omitzero"`
	SessionDescription *string `json:"session_description,omitzero"`
	Inbound            *string `json:"inbound,omitzero"`
	LeaseSeconds       *int    `json:"lease_seconds,omitzero"`
}

// Validate implements Validator.
func (h *HeartbeatRequest) Validate() error {
	if h.Activity != nil {
		if err := oneOf("activity", *h.Activity, ActivityBusy, ActivityIdle); err != nil {
			return err
		}
	}
	if h.SessionName != nil {
		if err := requireString("session_name", *h.SessionName); err != nil {
			return err
		}
		if err := capRunes("session_name", *h.SessionName, MaxSessionNameCodepoints); err != nil {
			return err
		}
	}
	if h.SessionDescription != nil {
		if err := optionalText("session_description", *h.SessionDescription, MaxDescriptionChars); err != nil {
			return err
		}
	}
	if h.Inbound != nil {
		if err := oneOf("inbound", *h.Inbound, InboundAccept, InboundHold, InboundRefuse); err != nil {
			return err
		}
	}
	return leaseSecondsInRange("lease_seconds", h.LeaseSeconds)
}

// HeartbeatResult is the `session heartbeat` result (4.4.4).
type HeartbeatResult struct {
	SessionID  string    `json:"session_id"`
	State      string    `json:"state"`
	LeaseUntil time.Time `json:"lease_until"`
	ServerTime time.Time `json:"server_time"`
}

// Validate implements Validator.
func (h *HeartbeatResult) Validate() error {
	if err := requireString("session_id", h.SessionID); err != nil {
		return err
	}
	if err := oneOf("state", h.State, SessionStateActive, SessionStateIdle, SessionStateOffline); err != nil {
		return err
	}
	if err := requireTime("lease_until", h.LeaseUntil); err != nil {
		return err
	}
	return requireTime("server_time", h.ServerTime)
}
