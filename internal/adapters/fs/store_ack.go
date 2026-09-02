//go:build !mutant_noack

package fs

import (
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/protocol"
)

// ackMessages implements 4.5.3 (C-30): an id in the session's inbox is
// MOVED to its acked directory with acked_at stamped, an already-acked
// owned id counts as acked again, and anything else — unknown, or
// addressed to another session — is `unknown`, never an error. After
// acknowledgement the message is neither returned by `message receive` nor
// re-emitted by `message watch`.
//
// Its twin in store_ack_mutant.go reports the same answer and moves
// nothing. That mutant must fail exactly C-30 and C-36.
func ackMessages(s *store, team, session string, ids []string) (*protocol.AckResult, error) {
	out := &protocol.AckResult{Acked: []string{}, Unknown: []string{}}
	for _, id := range ids {
		dir, name, ok, err := s.findMessageFile(team, session, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			out.Unknown = append(out.Unknown, id)
			continue
		}
		if dir == s.inboxDir(team, session) {
			if err := s.moveToAcked(team, session, name); err != nil {
				return nil, err
			}
		}
		out.Acked = append(out.Acked, id)
	}
	return out, nil
}

// moveToAcked rewrites one message into the acked directory under the same
// file name, with acked_at added, and removes the inbox copy. The write is
// atomic and happens before the unlink, so a crash can duplicate a message
// (at-least-once, 4.5.2) but can never lose one.
func (s *store) moveToAcked(team, session, name string) error {
	src := filepath.Join(s.inboxDir(team, session), name)
	var m storedMessage
	if err := readJSON(src, &m); err != nil {
		return err
	}
	now := s.now().UTC()
	m.AckedAt = &now
	if err := writeJSON(filepath.Join(s.ackedDir(team, session), name), &m); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return errInternal("an acknowledged message could not be removed from the inbox")
	}
	return nil
}
