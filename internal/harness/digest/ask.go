// ask.go is the road a decision's ask takes to the person it is for
// (card 162, plan §5.2; card 167, the addressee), shared by the two that
// send one: `brigade decision`, at once, and the asking session's watcher,
// which releases an ask held for the session's own user once their wait
// has passed (card 236, state/hold.go).

package digest

import (
	"strings"

	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// PickRoute is how a message for a member travels: through the first of
// their recipients whose gateway is on the roster and not offline — Slack
// before email whatever the recipient's shape — else to the session the
// caller picked for them, "" for none. false when neither is there.
func PickRoute(recipients []string, session string, roster []protocol.SessionRecord) (Delivery, bool) {
	if gw, ok := state.PickGateway(recipients, roster); ok {
		return Delivery{How: gw.Kind, To: gw.To, SessionID: gw.SessionID}, true
	}
	if session != "" {
		return Delivery{How: state.SentSession, To: session, SessionID: session}, true
	}
	return Delivery{}, false
}

// AskMessage is a decision's ask as it travels over the route (owner
// rulings 2026-11-09, card 193): the same text whichever way it goes
// (state.Decision.Ask), under the gateway's `to:` first line when a
// gateway carries it. Through a gateway the summary is the question alone
// — it becomes a mail's subject; to a session it also says what the
// message is and whom it is for, since a model reads the summary first
// and the body says neither.
func AskMessage(d *state.Decision, route Delivery) (body, summary string) {
	body, summary = d.Ask(), d.Question
	if route.How == state.SentSession {
		summary = "Decision for " + strings.Join(strings.Fields(d.To), " ") + ": " + summary
	} else {
		body = "to: " + route.To + "\n\n" + body
	}
	return body, protocol.TruncateRunes(summary, protocol.MaxSummaryChars)
}

// OtherSessionOf picks, for a held ask no gateway carries, the best
// session of the asking session's own member other than the asking one —
// whose conversation already has the question: the sessions of the same
// principal, the gateways and the ones that refuse left out, active before
// idle before offline, the most recently seen first. "" when the roster
// does not list the asking session, or its member has no other.
func OtherSessionOf(self string, roster []protocol.SessionRecord) string {
	principal := ""
	for i := range roster {
		if roster[i].SessionID == self {
			principal = roster[i].PrincipalRef
			break
		}
	}
	if principal == "" {
		return ""
	}
	return bestSession(roster, func(s *protocol.SessionRecord) bool {
		return s.SessionID != self && s.PrincipalRef == principal
	})
}
