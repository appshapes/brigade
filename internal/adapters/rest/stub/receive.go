package stub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// /v1/message/receive: BAP/1's `message receive` plus the contract's
// section 4. The fs adapter knows nothing of cursors or of waiting, so
// both are kept here.
//
// The cursor. For every session the stub numbers each pending message the
// first time it sees it, in the order the fs adapter lists them (oldest
// first). A cursor is the highest number given out when it was issued, so
// "accepted after the cursor was issued" is "numbered above it". Every
// receive reads the session's WHOLE pending set (the fs adapter's page of
// 200; an inbox holds 60 at most), so nothing accepted is left unnumbered.
// The cursor string carries a nonce of this server and a tag of its
// session: one from another server or another session is not recognised
// and, as the contract says, is treated as absent.
//
// The wait. When the answer would be empty and `wait_seconds` is not
// zero, the stub reads the inbox again every poll interval until there is
// something to answer, the time has passed, or the caller has gone. A
// failure met while waiting (the principal left its team, the credential
// was revoked) is answered at once: that is what ends a member's running
// watch within the five seconds C-08 gives it.

// The contract's bounds (section 4).
const (
	defaultLimit   = 50
	maxLimit       = 200
	maxWaitSeconds = 30
)

// An inbox is what the stub remembers about one session's pending
// messages: the number each was given, and the last number given out.
type inbox struct {
	seq  map[string]uint64
	last uint64
}

// receiveResult is the result of /v1/message/receive.
type receiveResult struct {
	Messages []jsontext.Value `json:"messages"`
	Cursor   string           `json:"cursor"`
}

// messageReceive answers one receive, holding it while the answer would be
// empty and the request asked to wait.
func (s *Server) messageReceive(c *call) (any, *protocol.Error) {
	id, perr := text(c.doc, "session_id", true)
	if perr != nil {
		return nil, perr
	}
	limit, perr := number(c.doc, "limit", defaultLimit, 1, maxLimit)
	if perr != nil {
		return nil, perr
	}
	wait, perr := number(c.doc, "wait_seconds", 0, 0, maxWaitSeconds)
	if perr != nil {
		return nil, perr
	}
	after, perr := text(c.doc, "after", false)
	if perr != nil {
		return nil, perr
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		if s.lookup(c.key) != c.who {
			return nil, unknownCredential()
		}
		result, perr := s.fs(c.who, nil, "message", "receive", "--session="+id, "--limit="+strconv.Itoa(maxLimit))
		if perr != nil {
			return nil, perr
		}
		var pending struct {
			Messages []jsontext.Value `json:"messages"`
		}
		if protocol.Unmarshal(result, &pending) != nil {
			return nil, internal()
		}
		answer, perr := s.page(id, pending.Messages, after, limit)
		if perr != nil {
			return nil, perr
		}
		if len(answer.Messages) > 0 || !time.Now().Before(deadline) {
			return answer, nil
		}
		select {
		case <-c.r.Context().Done():
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the caller abandoned the request"}
		case <-time.After(s.poll):
		}
	}
}

// page numbers the pending messages it has not seen, forgets the ones that
// are no longer pending, and returns the ones above the cursor, oldest
// first, with the session's new cursor.
func (s *Server) page(session string, pending []jsontext.Value, after string, limit int) (*receiveResult, *protocol.Error) {
	ids := make([]string, len(pending))
	for i, raw := range pending {
		var message struct {
			MessageID string `json:"message_id"`
		}
		if protocol.Unmarshal(raw, &message) != nil || message.MessageID == "" {
			return nil, internal()
		}
		ids[i] = message.MessageID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	box := s.inboxes[session]
	if box == nil {
		box = &inbox{seq: map[string]uint64{}}
		s.inboxes[session] = box
	}
	still := make(map[string]uint64, len(ids))
	for _, id := range ids {
		n, seen := box.seq[id]
		if !seen {
			box.last++
			n = box.last
		}
		still[id] = n
	}
	box.seq = still
	floor := s.floor(session, after, box.last)
	out := &receiveResult{Messages: []jsontext.Value{}, Cursor: s.cursor(session, box.last)}
	for i, id := range ids {
		if box.seq[id] > floor && len(out.Messages) < limit {
			out.Messages = append(out.Messages, pending[i])
		}
	}
	return out, nil
}

// cursor renders "everything numbered up to n" for a session.
func (s *Server) cursor(session string, n uint64) string {
	return s.nonce + "." + sessionTag(session) + "." + strconv.FormatUint(n, 10)
}

// floor reads a cursor back. One this server did not issue for this
// session, or one that names a number it has not reached, is not
// recognised: the floor is zero, so the answer is the whole pending set.
func (s *Server) floor(session, cursor string, last uint64) uint64 {
	rest, ok := strings.CutPrefix(cursor, s.nonce+"."+sessionTag(session)+".")
	if !ok {
		return 0
	}
	n, err := strconv.ParseUint(rest, 10, 64)
	if err != nil || n > last {
		return 0
	}
	return n
}

// sessionTag is a short name of a session for the cursor: enough to tell
// one session's cursor from another's, and nothing a reader can turn back
// into the id.
func sessionTag(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:6])
}
