package rest

import (
	"errors"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The request bodies and the results of the contract's section 2. A
// request body is the command's input, re-encoded from its typed shape so
// that an unknown member of the stdin document does not travel (JSON
// convention 2), with the value BAP/1 passes as a flag (`--session`) or
// that the profile holds (`team_ref`) beside it. A result is decoded into
// its typed shape and validated before it is printed: errMalformed from
// any Validate below is what makes a server's malformed result `internal`.

// errMalformed is the one failure of a result's own Validate. Its text is
// never printed: do() answers the fixed `internal` for it.
var errMalformed = errors.New("the result does not have the command's shape")

// ---- requests ----

// teamRequest is the body of the two team commands that take no input:
// the team the profile is bound to.
type teamRequest struct {
	TeamRef string `json:"team_ref"`
}

// teamCreateBody and teamJoinBody are the bodies of the binding commands.
// The `backend` member of the join's stdin document is the adapter's own
// configuration and is never sent.
type teamCreateBody struct {
	TeamName   string `json:"team_name"`
	HumanLabel string `json:"human_label,omitzero"`
}

type teamJoinBody struct {
	JoinSecret string `json:"join_secret"`
	HumanLabel string `json:"human_label,omitzero"`
}

// revokeMemberBody and transferBody are the bodies of the two
// administration commands that take input: the request of 4.4.10 as it
// was validated, with the profile's team beside it. Each request is
// embedded and json/v2 inlines it, so the wire document is one flat
// object. `team rotate-secret` takes no input and sends teamRequest.
type revokeMemberBody struct {
	protocol.TeamRevokeMemberRequest
	TeamRef string `json:"team_ref"`
}

type transferBody struct {
	protocol.TeamTransferRequest
	TeamRef string `json:"team_ref"`
}

// registerBody is the registration of 4.4.2 plus the profile's team. The
// registration is embedded and json/v2 inlines it, so the wire document is
// one flat object.
type registerBody struct {
	protocol.SessionRegistration
	TeamRef string `json:"team_ref"`
}

// heartbeatBody carries the session beside the request of 4.4.4.
type heartbeatBody struct {
	SessionID string                     `json:"session_id"`
	Heartbeat *protocol.HeartbeatRequest `json:"heartbeat"`
}

// listBody is the body of `session list`.
type listBody struct {
	TeamRef        string `json:"team_ref"`
	SessionID      string `json:"session_id,omitzero"`
	IncludeOffline bool   `json:"include_offline,omitzero"`
}

// sessionBody names one session: the body of `session close`.
type sessionBody struct {
	SessionID string `json:"session_id"`
}

// receiveBody is the body of `/v1/message/receive` (the contract's
// section 4): `after` and `wait_seconds` are the watch's.
type receiveBody struct {
	SessionID   string `json:"session_id"`
	Limit       int    `json:"limit,omitzero"`
	After       string `json:"after,omitzero"`
	WaitSeconds int    `json:"wait_seconds,omitzero"`
}

// ackBody is the body of `message ack`.
type ackBody struct {
	SessionID  string   `json:"session_id"`
	MessageIDs []string `json:"message_ids"`
}

// ---- results ----

// teamCreateAnswer is 4.4.10's create result plus the credential the
// server issued for a new principal (the contract's section 3). The
// credential is read here and never printed: the command prints the
// embedded result alone.
type teamCreateAnswer struct {
	protocol.TeamCreateResult
	Credential string `json:"credential,omitzero"`
}

// Validate implements protocol.Validator.
func (a *teamCreateAnswer) Validate() error {
	if a.TeamCreateResult.Validate() != nil || (a.Credential != "" && !validCredential(a.Credential)) {
		return errMalformed
	}
	return nil
}

// teamJoinAnswer is 4.4.10's join result plus the credential, present for
// a new principal and optional for a known one.
type teamJoinAnswer struct {
	protocol.TeamJoinResult
	Credential string `json:"credential,omitzero"`
}

// Validate implements protocol.Validator.
func (a *teamJoinAnswer) Validate() error {
	if a.TeamJoinResult.Validate() != nil || (a.Credential != "" && !validCredential(a.Credential)) {
		return errMalformed
	}
	return nil
}

// memberEntry is one row of the `team members` roster (4.4.10). LastSeenAt
// is a pointer so a member that has never registered a session renders as
// JSON null rather than as the zero time. JoinedSecretVersion is the
// version of the secret the member last joined with, which a server that
// carries the administration endpoints adds (the contract's section 3a).
// It counts from 1, so a server that does not answer it leaves a zero
// here and the member is then not printed.
type memberEntry struct {
	PrincipalRef        string     `json:"principal_ref"`
	HumanLabel          string     `json:"human_label,omitzero"`
	Status              string     `json:"status"`
	JoinedAt            time.Time  `json:"joined_at"`
	JoinedSecretVersion int        `json:"joined_secret_version,omitzero"`
	LastSeenAt          *time.Time `json:"last_seen_at"`
	SessionCount        int        `json:"session_count"`
}

// teamMembersResult is the `team members` result (4.4.10, C-43).
type teamMembersResult struct {
	TeamRef    string        `json:"team_ref"`
	TeamName   string        `json:"team_name"`
	ServerTime time.Time     `json:"server_time"`
	Members    []memberEntry `json:"members"`
}

// Validate implements protocol.Validator. Members is always emitted, as []
// when empty (JSON convention 3).
func (r *teamMembersResult) Validate() error {
	if r.TeamRef == "" || r.TeamName == "" || r.ServerTime.IsZero() {
		return errMalformed
	}
	if r.Members == nil {
		r.Members = []memberEntry{}
	}
	for i := range r.Members {
		m := &r.Members[i]
		if m.PrincipalRef == "" || m.Status == "" || m.JoinedAt.IsZero() || m.SessionCount < 0 || m.JoinedSecretVersion < 0 {
			return errMalformed
		}
	}
	return nil
}

// rotateSecretAnswer is the result of `/v1/team/rotate-secret` (the
// contract's section 3a). JoinSecret is the one place the server shows the
// new secret: it goes to the --secret-file and to nothing else, and the
// command prints protocol.TeamRotateSecretResult, which has no such
// member.
type rotateSecretAnswer struct {
	TeamRef       string    `json:"team_ref"`
	TeamName      string    `json:"team_name"`
	SecretVersion int       `json:"secret_version"`
	RotatedAt     time.Time `json:"rotated_at"`
	JoinSecret    string    `json:"join_secret"`
}

// Validate implements protocol.Validator. A rotation answers version 2 or
// more: 1 is the secret `team create` printed.
func (a *rotateSecretAnswer) Validate() error {
	if a.TeamRef == "" || a.TeamName == "" || a.SecretVersion < 2 || a.RotatedAt.IsZero() || a.JoinSecret == "" {
		return errMalformed
	}
	return nil
}

// serverInfo is the result of `/v1/info`: the contract version the server
// implements and the capabilities it lists beyond version 1's required
// surface, absent or empty for a server that has none.
type serverInfo struct {
	ContractVersion string   `json:"contract_version"`
	Capabilities    []string `json:"capabilities,omitzero"`
}

// Validate implements protocol.Validator.
func (i *serverInfo) Validate() error {
	if i.ContractVersion == "" {
		return errMalformed
	}
	return nil
}

// sessionRegisterResult is the `session register` result: the 4.4.3
// SessionRecord plus the three members 4.4.2 adds, one flat object.
type sessionRegisterResult struct {
	protocol.SessionRecord
	Resumed      bool      `json:"resumed"`
	LeaseSeconds int       `json:"lease_seconds"`
	ServerTime   time.Time `json:"server_time"`
}

// Validate implements protocol.Validator.
func (r *sessionRegisterResult) Validate() error {
	if r.SessionRecord.Validate() != nil || r.LeaseSeconds < 1 || r.ServerTime.IsZero() {
		return errMalformed
	}
	return nil
}

// sessionListResult is the `session list` result (4.4.3). Sessions is
// always emitted, as [] when empty (JSON convention 3).
type sessionListResult struct {
	TeamRef    string                   `json:"team_ref"`
	TeamName   string                   `json:"team_name"`
	ServerTime time.Time                `json:"server_time"`
	Sessions   []protocol.SessionRecord `json:"sessions"`
	Truncated  bool                     `json:"truncated"`
}

// Validate implements protocol.Validator.
func (r *sessionListResult) Validate() error {
	if r.TeamRef == "" || r.ServerTime.IsZero() {
		return errMalformed
	}
	if r.Sessions == nil {
		r.Sessions = []protocol.SessionRecord{}
	}
	for i := range r.Sessions {
		if r.Sessions[i].Validate() != nil {
			return errMalformed
		}
	}
	return nil
}

// sessionCloseResult is the `session close` result (4.4.3).
type sessionCloseResult struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// Validate implements protocol.Validator.
func (r *sessionCloseResult) Validate() error {
	if r.SessionID == "" || r.State != protocol.SessionStateOffline {
		return errMalformed
	}
	return nil
}

// maxCursorChars is the contract's bound on a cursor (section 4).
const maxCursorChars = 256

// receiveAnswer is the result of `/v1/message/receive`: the messages of
// 4.4.5 and the cursor of the contract's section 4.
type receiveAnswer struct {
	Messages []protocol.MessageEnvelope `json:"messages"`
	Cursor   string                     `json:"cursor"`
}

// Validate implements protocol.Validator.
func (r *receiveAnswer) Validate() error {
	if r.Cursor == "" || len(r.Cursor) > maxCursorChars {
		return errMalformed
	}
	if r.Messages == nil {
		r.Messages = []protocol.MessageEnvelope{}
	}
	for i := range r.Messages {
		if r.Messages[i].Validate() != nil {
			return errMalformed
		}
	}
	return nil
}

// messageReceiveResult is the `message receive` result (4.4.5): the
// messages alone. The cursor is the contract's, for the watch, and is not
// a member of BAP/1's result.
type messageReceiveResult struct {
	Messages []protocol.MessageEnvelope `json:"messages"`
}

// ackAnswer is the `message ack` result (4.4.8) with both arrays always
// emitted, as [] when empty (JSON convention 3).
type ackAnswer struct {
	protocol.AckResult
}

// Validate implements protocol.Validator.
func (r *ackAnswer) Validate() error {
	if r.AckResult.Validate() != nil {
		return errMalformed
	}
	if r.Acked == nil {
		r.Acked = []string{}
	}
	if r.Unknown == nil {
		r.Unknown = []string{}
	}
	return nil
}

// revokeResult is the result of `/v1/credential/revoke`.
type revokeResult struct {
	Revoked bool `json:"revoked"`
}

// Validate implements protocol.Validator.
func (r *revokeResult) Validate() error {
	if !r.Revoked {
		return errMalformed
	}
	return nil
}
