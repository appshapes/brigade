package stub

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"time"

	"github.com/appshapes/brigade/internal/adapters/fs"
	"github.com/appshapes/brigade/internal/protocol"
)

// A team that is taken in by request, and the move of its members: the
// three endpoints of the contract's section 3b, behind the capability
// `team.take_in`, which a server may leave out (work item 328, ADR 0005).
//
//   - `/v1/team/take-in`. The caller presents the join secret a team has
//     on another backend. A team the stub does not hold is taken in under
//     the reference inside the secret, and the caller is its member and
//     its owner; one it holds under that secret is joined; one it holds
//     under another secret is a conflict. The fs adapter has no command
//     that takes a team in, so the stub writes the team's record through
//     fs.TakeIn and then joins the caller to it as it joins anyone. Like
//     the secret of a team that was created, the secret that was presented
//     is kept in memory, because a join after a rotation, or with a move
//     ticket, is handed it (admin.go): what a test fixture may do and a
//     server must not.
//   - `/v1/team/open-move` and `/v1/team/close-move`, for the team's owner
//     alone. The ticket is minted as a join secret is and shown once, in
//     the answer; the stub keeps its hash and its end, and `admission`
//     admits a join that presents it until then.
//
// The creation rule gates a take-in as it gates a creation, before the
// request's shape is looked at.

// capTeamTakeIn is the capability /v1/info lists for these endpoints.
const capTeamTakeIn = "team.take_in"

// A moveTicket is the move a team has: the hash of its ticket and the
// moment the ticket stops admitting.
type moveTicket struct {
	hash [sha256.Size]byte
	ends time.Time
}

// admits reports whether sum, the SHA-256 of what a join presented, is
// this ticket's and the ticket has not ended at now. A team that is not
// moving has a nil ticket, which admits nobody.
func (m *moveTicket) admits(sum [sha256.Size]byte, now time.Time) bool {
	return m != nil && subtle.ConstantTimeCompare(sum[:], m.hash[:]) == 1 && now.Before(m.ends)
}

// clock reads the server's clock. The field is read and set under mu, so
// a test may move the clock between two requests.
func (s *Server) clock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now()
}

// otherJoinSecret is the refusal of a take-in whose team is here under
// another join secret. A principal the team banned is answered the same,
// as its join is answered what a wrong secret is.
func otherJoinSecret() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConflict,
		Message: "a team of that reference is here under another join secret",
		Details: map[string]string{"reason": "other_join_secret"},
	}
}

// teamTakeIn takes in the team whose join secret the request presents, or
// joins the caller to it when it is here already.
func (s *Server) teamTakeIn(c *call) (any, *protocol.Error) {
	if perr := s.mayCreate(c); perr != nil {
		return nil, perr
	}
	req := &protocol.TeamTakeInRequest{}
	if perr := decode(c.body, req); perr != nil {
		return nil, perr
	}
	secret, err := protocol.ParseJoinSecret(req.JoinSecret)
	if err != nil {
		return nil, asError(err)
	}
	s.membership.Lock()
	defer s.membership.Unlock()
	s.mu.Lock()
	held := s.teams[secret.TeamRef()] != nil
	s.mu.Unlock()
	if held {
		return s.joinTakenIn(c, req)
	}
	// A principal of this stub is one fs profile, which is bound to one
	// team at most: a caller that is bound to another is refused here, as
	// the fs adapter would refuse its join, before anything is written.
	if c.who != nil {
		status, perr := s.status(c.who)
		if perr != nil {
			return nil, perr
		}
		if status.TeamRef != "" {
			return nil, &protocol.Error{
				Code: protocol.CodeConflict, Message: "this profile is already bound to another team",
				Details: map[string]string{"reason": "profile_bound"},
			}
		}
	}
	if _, err := fs.TakeIn(s.store, req.TeamName, req.JoinSecret, s.clock()); err != nil {
		if errors.Is(err, fs.ErrTeamRefUnstorable) {
			return nil, invalid("join_secret", "invalid_value", "the stand-in server keeps a team whose reference is letters, digits, - and _ only")
		}
		return nil, internal()
	}
	b, perr := s.bind(c, &protocol.TeamJoinRequest{JoinSecret: req.JoinSecret, HumanLabel: req.HumanLabel}, "team", "join")
	if perr != nil {
		return nil, perr
	}
	var joined protocol.TeamJoinResult
	if protocol.Decode(b.result, &joined) != nil {
		return nil, internal()
	}
	// The team is the caller's from the request that took it in: there is
	// no moment at which it is here and has no owner.
	s.mu.Lock()
	b.who.ref = joined.PrincipalRef
	s.byRef[b.who.ref] = b.who
	s.teams[joined.TeamRef] = &team{
		owner: b.who.ref, version: 1, created: req.JoinSecret,
		joined: map[string]int{b.who.ref: 1}, banned: map[string]bool{},
	}
	s.mu.Unlock()
	return s.takenIn(b, &joined, true, true)
}

// joinTakenIn is a take-in of a team the stub already holds: a join by
// the rules a join has. What a join is refused for its secret, a take-in
// is refused as a conflict: the team is here under another secret.
func (s *Server) joinTakenIn(c *call, req *protocol.TeamTakeInRequest) (any, *protocol.Error) {
	handed, perr := s.admission(c.who, req.JoinSecret)
	if perr != nil {
		return nil, otherJoinSecret()
	}
	b, perr := s.bind(c, &protocol.TeamJoinRequest{JoinSecret: handed, HumanLabel: req.HumanLabel}, "team", "join")
	if perr != nil {
		if perr.Code == protocol.CodeUnauthorized {
			return nil, otherJoinSecret()
		}
		return nil, perr
	}
	var joined protocol.TeamJoinResult
	if protocol.Decode(b.result, &joined) != nil {
		return nil, internal()
	}
	s.admitted(&joined, b.who)
	s.mu.Lock()
	t := s.teams[joined.TeamRef]
	owner := t != nil && t.owner == joined.PrincipalRef
	s.mu.Unlock()
	return s.takenIn(b, &joined, false, owner)
}

// takenIn is the answer of a take-in: the team, the caller, whether this
// request took the team in and whether the caller owns it, with a new
// principal's credential added.
func (s *Server) takenIn(b *binding, joined *protocol.TeamJoinResult, takenIn, owner bool) (any, *protocol.Error) {
	out := map[string]any{
		"team_ref": joined.TeamRef, "team_name": joined.TeamName, "principal_ref": joined.PrincipalRef,
		"taken_in": takenIn, "owner": owner,
	}
	if b.fresh {
		issued, err := s.issue(b.who)
		if err != nil {
			return nil, internal()
		}
		out["credential"] = issued
	}
	return out, nil
}

// teamOpenMove opens the move of the caller's team: a ticket, minted as a
// join secret is, that a join is admitted with for `ticket_days` days. The
// gate comes before the request is looked at. A move the team already had
// is replaced, and its ticket admits nobody from then on.
func (s *Server) teamOpenMove(c *call) (any, *protocol.Error) {
	s.membership.Lock()
	defer s.membership.Unlock()
	o, perr := s.asOwner(c)
	if perr != nil {
		return nil, perr
	}
	days, perr := number(c.doc, "ticket_days", protocol.MoveTicketDefaultDays, 1, protocol.MoveTicketMaxDays)
	if perr != nil {
		return nil, perr
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, internal()
	}
	ticket := protocol.JoinSecretPrefix + o.ref + "." + hex.EncodeToString(random[:])
	s.mu.Lock()
	// The end is a whole second, so the time that is answered is the time
	// that is kept.
	ends := s.now().UTC().Add(time.Duration(days) * 24 * time.Hour).Truncate(time.Second)
	replaced := o.team.ticket != nil
	o.team.ticket = &moveTicket{hash: sha256.Sum256([]byte(ticket)), ends: ends}
	s.mu.Unlock()
	return map[string]any{
		"team_ref": o.ref, "team_name": o.status.TeamName, "ticket": ticket, "ends_at": ends, "replaced": replaced,
	}, nil
}

// teamCloseMove closes the move of the caller's team: its ticket admits
// nobody from then on, and every member it admitted stays one. A team
// that is not moving is left as it is.
func (s *Server) teamCloseMove(c *call) (any, *protocol.Error) {
	s.membership.Lock()
	defer s.membership.Unlock()
	o, perr := s.asOwner(c)
	if perr != nil {
		return nil, perr
	}
	s.mu.Lock()
	closed := o.team.ticket != nil
	o.team.ticket = nil
	s.mu.Unlock()
	return map[string]any{"team_ref": o.ref, "team_name": o.status.TeamName, "closed": closed}, nil
}
