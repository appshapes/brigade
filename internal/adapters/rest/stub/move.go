package stub

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/protocol"
)

// The record of where a team went (the contract's section 3c; work item
// 341, ADR 0010): the capability `team.move`, which a server may leave
// out, and its two endpoints.
//
//   - `/v1/team/move`, for the team's owner only. It records where the
//     team went, the destination of 4.4.10 (adapter, url, certificate) and
//     the move ticket that the backend the team went to admits a member
//     with, or with `clear` alone it clears the record. A second record
//     takes the place of the first. The owner is checked before the request
//     is looked at, so every other caller reads the one refusal whatever
//     the request held.
//   - `/v1/team/moved`, for every active member. It answers the record,
//     with the ticket only when the request asks for it.
//
// The record changes nothing else: nobody is put out, and no join and no
// session is refused for it. The fs adapter has the two commands of its
// own, for the principal that created a team; a team on this stub has an
// owner that a transfer changes, so the record is the stub's own, kept in
// memory beside the owner and the move ticket (admin.go, takein.go). The
// ticket is kept as it was given, because a member is handed it.

// capTeamMove is the capability /v1/info lists for these endpoints.
const capTeamMove = "team.move"

// maxMoveTicketChars is the cap this stub holds a recorded ticket to, in
// code points: the built server's own (the contract's section 3c).
const maxMoveTicketChars = 1024

// movedTo is the record of where a team went.
type movedTo struct {
	adapter     string
	url         string
	certificate string
	ticket      string
	at          time.Time
}

// result is 4.4.10's result for a team: `team_ref` and `moved`, and for a
// recorded move where the team went and when. `certificate` is there when
// the record has one, and `ticket` only when withTicket says so.
func (m *movedTo) result(teamRef string, withTicket bool) map[string]any {
	out := map[string]any{"team_ref": teamRef, "moved": m != nil}
	if m == nil {
		return out
	}
	out["adapter"], out["url"], out["recorded_at"] = m.adapter, m.url, m.at
	if m.certificate != "" {
		out["certificate"] = m.certificate
	}
	if withTicket {
		out["ticket"] = m.ticket
	}
	return out
}

// boolean reads an optional boolean member the contract adds; absent and
// null are false.
func boolean(doc map[string]jsontext.Value, name string) (bool, *protocol.Error) {
	raw, present := doc[name]
	if !present {
		return false, nil
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, invalid(name, "invalid_value", name+" must be a boolean")
	}
	return value, nil
}

// hasValue reports whether a member is present and is not null.
func hasValue(doc map[string]jsontext.Value, name string) bool {
	raw, present := doc[name]
	return present && raw.Kind() != 'n'
}

// capped refuses a member that is longer than limit code points.
func capped(name, value string, limit int) *protocol.Error {
	count := utf8.RuneCountInString(value)
	if count <= limit {
		return nil
	}
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: name + " exceeds the protocol cap of " + strconv.Itoa(limit) + " codepoints",
		Details: map[string]string{
			"field": name, "reason": "too_long", "limit": strconv.Itoa(limit), "actual": strconv.Itoa(count), "unit": "codepoints",
		},
	}
}

// notAMoveTicket refuses a ticket that is not a join secret of the team it
// is recorded for, or is longer than a server keeps. Fixed text: the
// ticket is a secret.
func notAMoveTicket() *protocol.Error {
	return invalid("ticket", "invalid_value",
		"ticket is not a move ticket of this team; expected the join secret the backend the team went to made when its move was opened, brg1.<team_ref>.<secret>")
}

// teamMove records where the caller's team went, or clears that record.
// The gate comes before the request is looked at, and the members are read
// in the order the built server reads them: `clear`, which goes with no
// member of a record, then the adapter, the url and the certificate, each
// within 4.4.10's cap, and the ticket last.
func (s *Server) teamMove(c *call) (any, *protocol.Error) {
	s.membership.Lock()
	defer s.membership.Unlock()
	o, perr := s.asOwner(c)
	if perr != nil {
		return nil, perr
	}
	clearing, perr := boolean(c.doc, "clear")
	if perr != nil {
		return nil, perr
	}
	if clearing {
		for _, name := range []string{"adapter", "url", "certificate", "ticket"} {
			if hasValue(c.doc, name) {
				return nil, invalid("clear", "invalid_value",
					"clear must be given with team_ref alone: a request records where a team went or clears the record")
			}
		}
		s.mu.Lock()
		o.team.moved = nil
		s.mu.Unlock()
		return o.team.moved.result(o.ref, false), nil
	}
	record := &movedTo{}
	for _, member := range []struct {
		name     string
		into     *string
		required bool
		limit    int
	}{
		{"adapter", &record.adapter, true, protocol.MaxMoveAdapterChars},
		{"url", &record.url, true, protocol.MaxMoveURLChars},
		{"certificate", &record.certificate, false, protocol.MaxMoveCertificateChars},
	} {
		value, perr := text(c.doc, member.name, member.required)
		if perr != nil {
			return nil, perr
		}
		if perr := capped(member.name, value, member.limit); perr != nil {
			return nil, perr
		}
		*member.into = value
	}
	ticket, perr := text(c.doc, "ticket", true)
	if perr != nil {
		return nil, perr
	}
	parsed, err := protocol.ParseJoinSecret(ticket)
	if err != nil || parsed.TeamRef() != o.ref || utf8.RuneCountInString(ticket) > maxMoveTicketChars {
		return nil, notAMoveTicket()
	}
	record.ticket = ticket
	s.mu.Lock()
	record.at = s.now().UTC()
	o.team.moved = record
	s.mu.Unlock()
	return record.result(o.ref, false), nil
}

// teamMoved answers the record of where the caller's team went, to an
// active member of it: anyone else is the uniform `unauthorized`. The
// ticket is in the answer only when the request asks for it.
func (s *Server) teamMoved(c *call) (any, *protocol.Error) {
	if perr := s.inTeam(c); perr != nil {
		return nil, perr
	}
	teamRef, perr := text(c.doc, "team_ref", true)
	if perr != nil {
		return nil, perr
	}
	withTicket, perr := boolean(c.doc, "with_ticket")
	if perr != nil {
		return nil, perr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var record *movedTo
	if t := s.teams[teamRef]; t != nil {
		record = t.moved
	}
	return record.result(teamRef, withTicket), nil
}
