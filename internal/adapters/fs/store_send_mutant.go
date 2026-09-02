//go:build mutant_trustsender

package fs

import (
	"encoding/json/v2"

	"github.com/appshapes/brigade/internal/protocol"
)

// This file is a DELIBERATE DEFECT, compiled only with -tags
// mutant_trustsender. P1-6's mutants_test.go asserts that this build fails
// exactly C-23 (the forbidden members of 4.4.6 are not rejected) and C-24
// (the sender-ownership rule of 4.5.7 is not enforced) and no other case.
// The normal build contains none of this code — see store_send.go.

// forgedSender is the shape the mutant trusts out of the request's
// forbidden `sender` member.
type forgedSender struct {
	SessionID string `json:"session_id"`
}

// decodeSendRequest accepts the forbidden members instead of rejecting
// them, and reports the forged sender session id it found.
func decodeSendRequest(data []byte) (*protocol.SendRequest, string, error) {
	req := &protocol.SendRequest{}
	if err := protocol.Unmarshal(data, req); err != nil {
		return nil, "", err
	}
	forged := ""
	if len(req.ForbiddenSender) > 0 {
		var s forgedSender
		if err := json.Unmarshal(req.ForbiddenSender, &s); err == nil {
			forged = s.SessionID
		}
	}
	req.ForbiddenSender = nil
	req.ForbiddenPrincipalRef = nil
	req.ForbiddenHumanLabel = nil
	req.ForbiddenTeamRef = nil
	req.ForbiddenCreatedAt = nil
	req.ForbiddenHopCount = nil
	if err := req.Validate(); err != nil {
		return nil, "", err
	}
	return req, forged, nil
}

// resolveSenderSession trusts the caller: it uses the forged sender when
// one was supplied and never checks that the session belongs to the
// calling principal.
func resolveSenderSession(s *store, team, _ string, req *protocol.SendRequest, forged string) (*sessionFile, error) {
	id := req.SenderSessionID
	if forged != "" {
		id = forged
	}
	f, ok, err := s.loadSession(team, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound()
	}
	return f, nil
}
