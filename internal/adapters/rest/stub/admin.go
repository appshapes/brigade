package stub

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The owner and the administration endpoints: the contract's section 3a.
// The fs adapter knows a team's creator and one join secret and nothing of
// owners, secret versions or bans, and it is not given them: it has no
// team administration (4.7). So what section 3a adds to a team is kept
// here, in memory, like the stub's credentials, and the fs adapter is
// driven to do the two things that touch its store.
//
//   - The secret. The fs store keeps the hash of the secret `team create`
//     printed and admits whoever presents that secret. After a rotation
//     the stub is the one that checks a join: it holds the hash of the
//     team's current secret, and for a join that presents it the fs
//     adapter is handed the secret the store knows, which the stub kept.
//     Keeping a secret in memory is what a test fixture may do and a
//     server must not.
//   - A revocation. The stub ends another member's membership the way that
//     member would end its own: it runs `team leave` for it, which revokes
//     the membership, closes its sessions in the team and unbinds the
//     directory that is the principal. From then on every request of that
//     principal about the team is the uniform `unauthorized`, a waiting
//     receive included (receive.go asks again every poll interval), and a
//     join with the secret re-activates the same membership.
//
// A team the stub did not see created has no owner here, and every
// administration request about it is refused (section 3a: a team that has
// no owner yet).

// capTeamAdmin is the capability /v1/info lists for these endpoints.
const capTeamAdmin = "team.admin"

// A team is what the stub keeps about one team beside the fs store.
type team struct {
	// owner is the principal_ref of the owner: the creator, until a
	// transfer names another.
	owner string
	// version counts the team's secrets from 1.
	version int
	// created is the secret the fs store admits: the one `team create`
	// printed.
	created string
	// current is the hash of the secret that joins now. It is read only
	// once the team has rotated; until then the fs store decides.
	current [sha256.Size]byte
	// joined is, per principal_ref, the version of the secret it last
	// joined with: every principal that was ever a member has an entry.
	joined map[string]int
	// banned holds the principals whose join is the rejected join.
	banned map[string]bool
}

// founded records a team that was just created: the caller is its owner
// and joined with version 1.
func (s *Server) founded(created *protocol.TeamCreateResult, who *principal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	who.ref = created.PrincipalRef
	s.byRef[who.ref] = who
	s.teams[created.TeamRef] = &team{
		owner: who.ref, version: 1, created: created.JoinSecret,
		joined: map[string]int{who.ref: 1}, banned: map[string]bool{},
	}
}

// admission decides a join before the fs adapter is asked, and returns the
// secret to hand it. A banned principal and a secret that was rotated away
// are the rejected join, the same bytes as a wrong secret. A join that
// presents a rotated team's current secret is handed the secret the store
// knows. Everything else, a secret that does not parse and a team the stub
// did not see created among it, is the fs adapter's to answer.
func (s *Server) admission(who *principal, presented string) (string, *protocol.Error) {
	secret, err := protocol.ParseJoinSecret(presented)
	if err != nil {
		return presented, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.teams[secret.TeamRef()]
	if t == nil {
		return presented, nil
	}
	if who != nil && t.banned[who.ref] {
		return "", secretRejected()
	}
	if t.version == 1 {
		return presented, nil
	}
	sum := sha256.Sum256([]byte(presented))
	if subtle.ConstantTimeCompare(sum[:], t.current[:]) != 1 {
		return "", secretRejected()
	}
	return t.created, nil
}

// admitted records a join that succeeded: the version of the secret the
// member joined with (section 3a).
func (s *Server) admitted(joined *protocol.TeamJoinResult, who *principal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	who.ref = joined.PrincipalRef
	s.byRef[who.ref] = who
	if t := s.teams[joined.TeamRef]; t != nil {
		t.joined[who.ref] = t.version
	}
}

// withJoinedVersions adds `joined_secret_version` to each member of a
// roster the fs adapter answered.
func (s *Server) withJoinedVersions(result jsontext.Value) (any, *protocol.Error) {
	var roster map[string]jsontext.Value
	var members []map[string]jsontext.Value
	if protocol.Unmarshal(result, &roster) != nil || protocol.Unmarshal(roster["members"], &members) != nil {
		return nil, internal()
	}
	var teamRef string
	if json.Unmarshal(roster["team_ref"], &teamRef) != nil {
		return nil, internal()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.teams[teamRef]
	for _, m := range members {
		var ref string
		if json.Unmarshal(m["principal_ref"], &ref) != nil {
			return nil, internal()
		}
		if t != nil && t.joined[ref] > 0 {
			m["joined_secret_version"] = jsontext.Value(strconv.Itoa(t.joined[ref]))
		}
	}
	if members == nil {
		members = []map[string]jsontext.Value{}
	}
	encoded, err := json.Marshal(members)
	if err != nil {
		return nil, internal()
	}
	roster["members"] = encoded
	return roster, nil
}

// An owned team is an administration request that passed the gate: the
// team it names and what the caller's own directory says.
type owned struct {
	ref    string
	team   *team
	status *profileStatus
}

// asOwner is the gate of section 3a: the caller must be the owner of the
// team the request names and an active member of it. Anything else is the
// uniform `unauthorized` of 4.5.7, the same bytes for a team that does not
// exist, a caller that is not an active member of it and an active member
// that is not its owner.
func (s *Server) asOwner(c *call) (*owned, *protocol.Error) {
	teamRef, perr := text(c.doc, "team_ref", true)
	if perr != nil {
		return nil, perr
	}
	status, perr := s.status(c.who)
	if perr != nil {
		return nil, perr
	}
	if status.TeamRef == "" || status.TeamRef != teamRef {
		return nil, notMember()
	}
	s.mu.Lock()
	t := s.teams[teamRef]
	isOwner := t != nil && t.owner == status.PrincipalRef
	s.mu.Unlock()
	if !isOwner {
		return nil, notMember()
	}
	return &owned{ref: teamRef, team: t, status: status}, nil
}

// teamRotateSecret gives the team a new join secret: the same team_ref,
// 128 random bits, the next version. No membership changes; the previous
// secret no longer joins, because admission checks every later join
// against the new one.
func (s *Server) teamRotateSecret(c *call) (any, *protocol.Error) {
	s.membership.Lock()
	defer s.membership.Unlock()
	o, perr := s.asOwner(c)
	if perr != nil {
		return nil, perr
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, internal()
	}
	secret := protocol.JoinSecretPrefix + o.ref + "." + hex.EncodeToString(random[:])
	s.mu.Lock()
	o.team.version++
	o.team.current = sha256.Sum256([]byte(secret))
	version := o.team.version
	s.mu.Unlock()
	return map[string]any{
		"team_ref": o.ref, "team_name": o.status.TeamName, "secret_version": version,
		"rotated_at": time.Now().UTC(), "join_secret": secret,
	}, nil
}

// teamRevokeMember is BAP/1's `team revoke-member` in its two forms
// (4.4.10). The gate comes before the request is looked at, so a caller
// that is not the owner learns nothing about what it sent.
func (s *Server) teamRevokeMember(c *call) (any, *protocol.Error) {
	s.membership.Lock()
	defer s.membership.Unlock()
	o, perr := s.asOwner(c)
	if perr != nil {
		return nil, perr
	}
	req := &protocol.TeamRevokeMemberRequest{}
	if perr := decode(c.body, req); perr != nil {
		return nil, perr
	}
	open, perr := s.openSessions(c.who)
	if perr != nil {
		return nil, perr
	}
	if req.JoinedSecretVersionLTE != nil {
		return s.revokeByVersion(c, o, *req.JoinedSecretVersionLTE, open)
	}
	if req.PrincipalRef == o.status.PrincipalRef {
		return nil, invalid("principal_ref", "invalid_value", "the owner cannot revoke itself; team leave is the way out")
	}
	asked := protocol.MembershipRevoked
	if req.Ban {
		asked = protocol.MembershipBanned
	}
	out := &protocol.TeamRevokeMemberResult{TeamRef: o.ref, PrincipalRef: req.PrincipalRef, Status: asked}
	target, was := s.standing(o, req.PrincipalRef)
	if was == "" || was == asked {
		return out, nil
	}
	if was == standingActive {
		if perr := s.expel(target); perr != nil {
			return nil, perr
		}
		out.SessionsClosed = open[req.PrincipalRef]
	}
	s.mu.Lock()
	if req.Ban {
		o.team.banned[req.PrincipalRef] = true
	} else {
		delete(o.team.banned, req.PrincipalRef)
	}
	s.mu.Unlock()
	out.Changed = true
	return out, nil
}

// standingActive is the standing of a member whose membership is active;
// the other two are the protocol's `revoked` and `banned`.
const standingActive = "active"

// standing says what a principal is to the team: active, revoked, banned,
// or "" for one that never was a member, which a revocation leaves alone.
// It returns the principal too, for the leave an active one is given.
func (s *Server) standing(o *owned, ref string) (*principal, string) {
	s.mu.Lock()
	target := s.byRef[ref]
	_, everJoined := o.team.joined[ref]
	banned := o.team.banned[ref]
	s.mu.Unlock()
	switch {
	case target == nil || !everJoined:
		return nil, ""
	case banned:
		return target, protocol.MembershipBanned
	}
	if status, perr := s.status(target); perr == nil && status.TeamRef == o.ref {
		return target, standingActive
	}
	return target, protocol.MembershipRevoked
}

// expel ends a member's membership as the member itself would: `team
// leave`, run for it.
func (s *Server) expel(target *principal) *protocol.Error {
	_, perr := s.fs(target, nil, "team", "leave")
	return perr
}

// revokeByVersion revokes every active member that joined with a secret of
// version n or lower, except the owner. It never bans, and a banned member
// is not active, so it is left as it is.
func (s *Server) revokeByVersion(c *call, o *owned, n int, open map[string]int) (any, *protocol.Error) {
	result, perr := s.fs(c.who, nil, "team", "members")
	if perr != nil {
		return nil, perr
	}
	active, perr := principalRefs(result, "members")
	if perr != nil {
		return nil, perr
	}
	out := &protocol.TeamRevokeByVersionResult{TeamRef: o.ref, MaxVersion: n}
	for _, ref := range active {
		s.mu.Lock()
		target, version := s.byRef[ref], o.team.joined[ref]
		s.mu.Unlock()
		if ref == o.status.PrincipalRef || target == nil || version > n {
			continue
		}
		if perr := s.expel(target); perr != nil {
			return nil, perr
		}
		out.Revoked++
		out.SessionsClosed += open[ref]
	}
	return out, nil
}

// teamTransfer makes an active member of the team its owner. A principal
// that is not one is `invalid_input` naming principal_ref; the owner
// naming itself is accepted and changes nothing.
func (s *Server) teamTransfer(c *call) (any, *protocol.Error) {
	s.membership.Lock()
	defer s.membership.Unlock()
	o, perr := s.asOwner(c)
	if perr != nil {
		return nil, perr
	}
	req := &protocol.TeamTransferRequest{}
	if perr := decode(c.body, req); perr != nil {
		return nil, perr
	}
	result, perr := s.fs(c.who, nil, "team", "members")
	if perr != nil {
		return nil, perr
	}
	active, perr := principalRefs(result, "members")
	if perr != nil {
		return nil, perr
	}
	isMember := false
	for _, ref := range active {
		isMember = isMember || ref == req.PrincipalRef
	}
	if !isMember {
		return nil, invalid("principal_ref", "invalid_value", "principal_ref is not an active member of this team")
	}
	s.mu.Lock()
	o.team.owner = req.PrincipalRef
	s.mu.Unlock()
	return &protocol.TeamTransferResult{TeamRef: o.ref, PrincipalRef: req.PrincipalRef, Transferred: true}, nil
}

// openSessions counts, per principal, the sessions of the caller's team
// that are not offline, which is what `session list` answers without
// --include-offline. They are the sessions a revocation of that principal
// closes; one whose lease already ran out is offline and is not counted.
func (s *Server) openSessions(who *principal) (map[string]int, *protocol.Error) {
	result, perr := s.fs(who, nil, "session", "list")
	if perr != nil {
		return nil, perr
	}
	refs, perr := principalRefs(result, "sessions")
	if perr != nil {
		return nil, perr
	}
	open := map[string]int{}
	for _, ref := range refs {
		open[ref]++
	}
	return open, nil
}

// principalRefs reads the principal_ref of every element of one array
// member of an fs result: the roster's `members`, a list's `sessions`.
func principalRefs(result jsontext.Value, member string) ([]string, *protocol.Error) {
	var object map[string]jsontext.Value
	var rows []struct {
		PrincipalRef string `json:"principal_ref"`
	}
	if protocol.Unmarshal(result, &object) != nil || protocol.Unmarshal(object[member], &rows) != nil {
		return nil, internal()
	}
	refs := make([]string, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, row.PrincipalRef)
	}
	return refs, nil
}
