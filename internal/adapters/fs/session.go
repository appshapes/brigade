package fs

import (
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// sessionRegisterResult is the `session register` result: the 4.4.3
// SessionRecord plus the three members 4.4.2 adds. The record is embedded
// and json/v2 inlines it, so the wire document is one flat object.
type sessionRegisterResult struct {
	protocol.SessionRecord
	Resumed      bool      `json:"resumed"`
	LeaseSeconds int       `json:"lease_seconds"`
	ServerTime   time.Time `json:"server_time"`
}

// sessionListResult is the `session list` result (4.4.3). Sessions is
// always present and is [] when empty (JSON convention 3).
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

// sessionRegister implements 4.4.2 (C-10, C-11, C-16, C-17, C-19, C-19b,
// C-42). The lease is checked against THIS adapter's advertised range,
// which is not the protocol's example range (4.4.1), and an absent
// lease_seconds means lease.default_seconds.
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
	f.Harness, f.HarnessVersion = req.Harness, req.HarnessVersion
	f.LeaseSeconds = seconds
	f.LastSeenAt = now
	f.LeaseUntil = now.Add(time.Duration(seconds) * time.Second)
	if err := c.st.saveSession(teamRef, f); err != nil {
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

// sessionHeartbeat implements 4.4.4 (C-13, C-15, C-42). Every member of
// the request is optional and absent means unchanged.
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
// is `conflict` (4.5.8, C-15); an unknown, foreign or not-owned id is the
// uniform not_found (C-13). It is shared with the watch's `heartbeat`
// stdin command, which is defined to behave identically (4.4.9).
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
	// model and context_used_tokens follow the same absent-means-unchanged
	// rule, and 4.4.4 adds that a harness never CLEARS them: it omits them
	// and the stored values stand. A heartbeat that carries neither
	// therefore leaves both exactly as the last one that did left them
	// (C-44).
	if req.Model != nil {
		f.Model = req.Model
	}
	if req.ContextUsedTokens != nil {
		f.ContextUsedTokens = req.ContextUsedTokens
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

// sessionList implements 4.4.3 (C-08, C-11, C-12, C-14, C-26, C-42). It
// reads no input at all: a command that takes none MUST NOT read stdin
// (4.1, B-1), because the harness hands it the null device and a human
// running it by hand would otherwise hang.
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
	found, err := collectSessions(c.st, teamRef)
	if err != nil {
		return nil, err
	}
	now := c.st.now()
	records := make([]protocol.SessionRecord, 0, len(found))
	for _, ts := range found {
		if !*includeOffline && sessionState(ts.file, now) == protocol.SessionStateOffline {
			continue
		}
		record, err := c.st.record(ts.teamRef, ts.file, ts.file.SessionID == *session && *session != "")
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	teamName := ""
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

// sessionClose implements 4.4.3 (C-15): idempotent, and an already-closed
// session is still a success.
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
// is already closed changes nothing and is not an error (4.5.8, C-15). It
// is shared with the watch's `close` stdin command (4.4.9).
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
