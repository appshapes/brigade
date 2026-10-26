package webhook

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/gateway"
	"github.com/appshapes/brigade/internal/protocol"
)

// HarnessName is the harness this gateway registers under. Every harness
// named "gateway-*" is a gateway (gateway.HarnessPrefix): never a
// --member target, never a recipient this gateway picks for a member.
const HarnessName = gateway.HarnessPrefix + "webhook"

const (
	minTailChars      = 5
	minPrincipalChars = 8
)

// IsGatewayHarness reports whether a roster row is a gateway. The prefix
// itself is spelled once, in gateway.HarnessPrefix.
func IsGatewayHarness(h string) bool { return gateway.IsHarness(h) }

// resolveError is a recipient refusal: notFound selects 404 over 400.
type resolveError struct {
	msg      string
	notFound bool
}

func (e *resolveError) Error() string { return e.msg }

// resolveSession turns `to` into a session id: the full id, else a tail
// of at least five characters that ends exactly one listed session.
func resolveSession(to string, sessions []protocol.SessionRecord) (string, error) {
	for i := range sessions {
		if sessions[i].SessionID == to {
			return to, nil
		}
	}
	if utf8.RuneCountInString(to) < minTailChars {
		return "", &resolveError{msg: "no session has that id; a tail must be at least 5 characters", notFound: true}
	}
	var hits []string
	for i := range sessions {
		if strings.HasSuffix(sessions[i].SessionID, to) {
			hits = append(hits, sessions[i].SessionID)
		}
	}
	switch len(hits) {
	case 0:
		return "", &resolveError{msg: "no session id ends with those characters", notFound: true}
	case 1:
		return hits[0], nil
	default:
		return "", &resolveError{msg: "those characters end more than one session id; give more of it"}
	}
}

// resolveMember picks a member's best session the way `brigade send
// --member` and the hosted gateways do: label (case-insensitive, whole)
// first, else a principal prefix of 8+ characters; a label two principals
// share is refused; gateways and inbound=refuse sessions are left out;
// active before idle before offline, most recently seen first.
func resolveMember(who string, sessions []protocol.SessionRecord) (string, error) {
	who = strings.TrimSpace(who)
	var mine []protocol.SessionRecord
	for i := range sessions {
		if l := strings.TrimSpace(protocol.SanitizeLabel(sessions[i].HumanLabel)); l != "" && strings.EqualFold(l, who) {
			mine = append(mine, sessions[i])
		}
	}
	if len(mine) == 0 {
		prefix := strings.TrimSuffix(strings.TrimPrefix(who, "["), "]")
		if utf8.RuneCountInString(prefix) >= minPrincipalChars {
			for i := range sessions {
				if strings.HasPrefix(sessions[i].PrincipalRef, prefix) {
					mine = append(mine, sessions[i])
				}
			}
		}
	}
	if len(mine) == 0 {
		return "", &resolveError{msg: "no member has that label or principal prefix", notFound: true}
	}
	principals := map[string]bool{}
	for i := range mine {
		principals[mine[i].PrincipalRef] = true
	}
	if len(principals) > 1 {
		return "", &resolveError{msg: "that label or prefix matches more than one member; use a longer principal prefix"}
	}
	var eligible []protocol.SessionRecord
	for i := range mine {
		if !IsGatewayHarness(mine[i].Harness) && mine[i].Inbound != protocol.InboundRefuse {
			eligible = append(eligible, mine[i])
		}
	}
	if len(eligible) == 0 {
		return "", &resolveError{msg: "none of that member's sessions can receive a message", notFound: true}
	}
	slices.SortStableFunc(eligible, func(a, b protocol.SessionRecord) int {
		if r := cmp.Compare(stateRank(a.State), stateRank(b.State)); r != 0 {
			return r
		}
		return b.LastSeenAt.Compare(a.LastSeenAt)
	})
	return eligible[0].SessionID, nil
}

func stateRank(s string) int {
	switch s {
	case protocol.SessionStateActive:
		return 0
	case protocol.SessionStateIdle:
		return 1
	default:
		return 2
	}
}
