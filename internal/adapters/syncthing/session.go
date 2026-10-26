package syncthing

import (
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// sessionRegisterResult is the `session register` result: the 4.4.3
// SessionRecord plus the three members 4.4.2 adds.
type sessionRegisterResult struct {
	protocol.SessionRecord
	Resumed      bool      `json:"resumed"`
	LeaseSeconds int       `json:"lease_seconds"`
	ServerTime   time.Time `json:"server_time"`
}

// sessionListResult is the `session list` result (4.4.3).
type sessionListResult struct {
	TeamRef    string                   `json:"team_ref"`
	TeamName   string                   `json:"team_name"`
	ServerTime time.Time                `json:"server_time"`
	Sessions   []protocol.SessionRecord `json:"sessions"`
	Truncated  bool                     `json:"truncated"`
}

// sessionCloseResult is the `session close` result (4.4.3).
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

// requireSessionFlag reads the --session id every session and message verb
// but `register` and `list` needs.
func (c *command) requireSessionFlag(id string) (string, error) {
	if id == "" {
		return "", errUsage("--session <id> is required for this command")
	}
	return id, nil
}

// sessionRegister implements 4.4.2. The session file is this principal's
// own; nothing of the engine is touched — the watch attaches it.
func (c *command) sessionRegister() (any, error) {
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
	req := &protocol.SessionRegistration{}
	if err := protocol.Decode(data, req); err != nil {
		return nil, err
	}
	lease := adapterLease()
	if err := lease.CheckSeconds("lease_seconds", req.LeaseSeconds); err != nil {
		return nil, err
	}
	seconds := lease.DefaultSeconds
	if req.LeaseSeconds != nil {
		seconds = *req.LeaseSeconds
	}
	if err := c.authorize(); err != nil {
		return nil, err
	}
	teamRef, principal := c.profile.TeamRef, c.cred.PrincipalRef
	now := c.st.now().UTC()
	var f *sessionFile
	resumed := false
	if req.Resume != nil {
		if f, err = c.st.ownedSession(teamRef, principal, req.Resume.SessionID); err != nil {
			return nil, err
		}
		if f.ClosedAt == nil && !now.After(f.LeaseUntil) {
			return nil, errConflict("that session is open with a valid lease", reasonSessionLive)
		}
		f.ClosedAt, resumed = nil, true
	} else {
		id, err := newRef()
		if err != nil {
			return nil, err
		}
		f = &sessionFile{SessionID: id, PrincipalRef: principal, CreatedAt: now}
	}
	f.SessionName, f.Activity, f.Inbound = req.SessionName, req.Activity, req.Inbound
	f.SessionDescription, f.WorkspaceLabel = req.SessionDescription, req.WorkspaceLabel
	f.Model, f.ContextUsedTokens = req.Model, req.ContextUsedTokens
	f.BrigadeVersion, f.SyncPeer = req.BrigadeVersion, req.SyncPeer
	f.Harness, f.HarnessVersion = req.Harness, req.HarnessVersion
	f.LeaseSeconds = seconds
	f.LastSeenAt = now
	f.LeaseUntil = now.Add(time.Duration(seconds) * time.Second)
	if err := c.st.saveSession(teamRef, f); err != nil {
		return nil, err
	}
	if err := c.st.adoptHumanLabel(teamRef, principal, req.HumanLabel); err != nil {
		return nil, err
	}
	record, err := c.st.record(teamRef, f, false)
	if err != nil {
		return nil, err
	}
	return &sessionRegisterResult{
		SessionRecord: record, Resumed: resumed, LeaseSeconds: seconds, ServerTime: now,
	}, nil
}

// sessionHeartbeat implements 4.4.4. Every member of the request is
// optional and absent means unchanged.
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
	data, err := adapterkit.ReadInput(c.stdin)
	if err != nil {
		return nil, err
	}
	req := &protocol.HeartbeatRequest{}
	if err := protocol.Decode(data, req); err != nil {
		return nil, err
	}
	if err := adapterLease().CheckSeconds("lease_seconds", req.LeaseSeconds); err != nil {
		return nil, err
	}
	if err := c.authorize(); err != nil {
		return nil, err
	}
	return c.st.heartbeat(c.profile.TeamRef, c.cred.PrincipalRef, id, req)
}

// heartbeat applies a 4.4.4 request to one owned session. A closed session
// is `conflict`; an unknown, foreign or not-owned id is the uniform
// not_found. It is shared with the watch's `heartbeat` stdin command.
func (s *store) heartbeat(team, principal, id string, req *protocol.HeartbeatRequest) (*protocol.HeartbeatResult, error) {
	f, err := s.ownedSession(team, principal, id)
	if err != nil {
		return nil, err
	}
	if f.ClosedAt != nil {
		return nil, errConflict("that session is closed", reasonSessionClosed)
	}
	if req.Activity != nil {
		f.Activity = *req.Activity
	}
	if req.SessionName != nil {
		f.SessionName = *req.SessionName
	}
	if req.SessionDescription != nil {
		f.SessionDescription = req.SessionDescription
	}
	if req.Inbound != nil {
		f.Inbound = *req.Inbound
	}
	if req.LeaseSeconds != nil {
		f.LeaseSeconds = *req.LeaseSeconds
	}
	// model, context_used_tokens, brigade_version and sync_peer follow the
	// absent-means-unchanged rule, and a harness never CLEARS them (4.4.4).
	if req.Model != nil {
		f.Model = req.Model
	}
	if req.ContextUsedTokens != nil {
		f.ContextUsedTokens = req.ContextUsedTokens
	}
	if req.BrigadeVersion != nil {
		f.BrigadeVersion = req.BrigadeVersion
	}
	if req.SyncPeer != nil {
		f.SyncPeer = req.SyncPeer
	}
	now := s.now().UTC()
	f.LastSeenAt = now
	f.LeaseUntil = now.Add(time.Duration(f.LeaseSeconds) * time.Second)
	if err := s.saveSession(team, f); err != nil {
		return nil, err
	}
	return &protocol.HeartbeatResult{
		SessionID: f.SessionID, State: sessionState(f, now), LeaseUntil: f.LeaseUntil, ServerTime: now,
	}, nil
}

// sessionList implements 4.4.3: every session of the team's active
// members on this machine's replica. It reads no input at all (4.1).
func (c *command) sessionList() (any, error) {
	fs := newFlags()
	session := fs.String("session", "", "mark this session is_self")
	includeOffline := fs.Bool("include-offline", false, "include offline sessions")
	if err := c.parse(fs); err != nil {
		return nil, err
	}
	if err := c.teamScoped(); err != nil {
		return nil, err
	}
	teamRef := c.profile.TeamRef
	found, err := c.st.sessionsOfTeam(teamRef)
	if err != nil {
		return nil, err
	}
	now := c.st.now()
	records := make([]protocol.SessionRecord, 0, len(found))
	for _, f := range found {
		if !*includeOffline && sessionState(f, now) == protocol.SessionStateOffline {
			continue
		}
		record, err := c.st.record(teamRef, f, f.SessionID == *session && *session != "")
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	teamName := c.profile.TeamName
	if team, ok, err := c.st.loadTeam(teamRef); err != nil {
		return nil, err
	} else if ok {
		teamName = team.TeamName
	}
	return &sessionListResult{
		TeamRef: teamRef, TeamName: teamName, ServerTime: now.UTC(),
		Sessions: records, Truncated: false,
	}, nil
}

// sessionClose implements 4.4.3: idempotent, and an already-closed session
// is still a success.
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
	if err := c.teamScoped(); err != nil {
		return nil, err
	}
	if err := c.st.closeSession(c.profile.TeamRef, c.cred.PrincipalRef, id); err != nil {
		return nil, err
	}
	return &sessionCloseResult{SessionID: id, State: protocol.SessionStateOffline}, nil
}

// closeSession sets closed_at on an owned session. Closing a session that
// is already closed changes nothing and is not an error. It is shared with
// the watch's `close` stdin command.
func (s *store) closeSession(team, principal, id string) error {
	f, err := s.ownedSession(team, principal, id)
	if err != nil {
		return err
	}
	if f.ClosedAt != nil {
		return nil
	}
	now := s.now().UTC()
	f.ClosedAt = &now
	return s.saveSession(team, f)
}
