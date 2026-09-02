//go:build mutant_noack

package fs

import "github.com/appshapes/brigade/internal/protocol"

// This file is a DELIBERATE DEFECT, compiled only with -tags mutant_noack.
// P1-6's mutants_test.go asserts that this build fails exactly C-30 and
// C-36 (4.5.3: after acknowledgement a message must not be returned by
// `message receive` or re-emitted by `message watch`) and no other case.
// The normal build contains none of this code — see store_ack.go.

// ackMessages reports the right answer and persists nothing: an id present
// in the inbox is reported `acked` but is left in the inbox, so a later
// receive still returns it and a restarted watch re-emits it.
func ackMessages(s *store, team, session string, ids []string) (*protocol.AckResult, error) {
	out := &protocol.AckResult{Acked: []string{}, Unknown: []string{}}
	for _, id := range ids {
		_, _, ok, err := s.findMessageFile(team, session, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			out.Unknown = append(out.Unknown, id)
			continue
		}
		out.Acked = append(out.Acked, id)
	}
	return out, nil
}
