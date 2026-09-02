//go:build !mutant_trustsender

package fs

import "github.com/appshapes/brigade/internal/protocol"

// decodeSendRequest parses a 4.4.6 request. protocol.Decode runs the
// shape's Validate, which is where the C-23 rejection lives: a request
// carrying `sender`, `principal_ref`, `human_label`, `team_ref`,
// `created_at` or `hop_count` — with any value, null included — is
// invalid_input with details.field naming the member, before any store
// access. The second return value is the caller-supplied sender session
// id, which a conforming adapter NEVER has: it is always empty here.
//
// Its twin in store_send_mutant.go strips the forbidden members instead
// and honours the forged one. That mutant must fail exactly C-23 and C-24.
func decodeSendRequest(data []byte) (*protocol.SendRequest, string, error) {
	req := &protocol.SendRequest{}
	if err := protocol.Decode(data, req); err != nil {
		return nil, "", err
	}
	return req, "", nil
}

// resolveSenderSession resolves `sender_session_id` to a session that
// exists in this team AND is owned by this principal (4.5.7): a caller
// cannot impersonate a session by supplying its id, and the refusal is the
// uniform not_found (C-24).
func resolveSenderSession(s *store, team, principal string, req *protocol.SendRequest, _ string) (*sessionFile, error) {
	return s.ownedSession(team, principal, req.SenderSessionID)
}
