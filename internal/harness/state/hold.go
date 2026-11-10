// hold.go is the hold of a decision's ask (card 236): a question for the
// asking session's own user is asked in the conversation, the host's own
// way, and `brigade decision` records it without sending it. The ask goes
// out — through the usual route, by the asking session's watcher — only
// when the member's wait has passed (Subscriber.HoldFor), the decision is
// still open, and no prompt arrived in that conversation since it was
// recorded. A prompt ends the hold for good; so does a close. A decision
// for anyone else is never held: no conversation of this session reaches
// them.

package state

import (
	"os"
	"strings"
	"time"
)

// The ways a hold ends without a send, Decision.HoldEnded: a prompt
// arrived in the asking conversation, so its user saw the question there;
// the asking session was not running when the wait ended; or the wait
// ended and neither a gateway of the member's recipients nor another
// session of theirs was on the roster. After any of them the decision is
// open and unsent, and the digest lists it.
const (
	HoldPrompt = "prompt"
	HoldMissed = "missed"
	HoldUnsent = "unsent"
)

// Hold marks the decision's ask as held until the time: the asking
// session's own user is asked in the conversation first.
func (d *Decision) Hold(until time.Time) { d.HeldUntil, d.HoldEnded = until, "" }

// Held reports whether the ask is held and nothing has ended the hold: the
// decision is open, nothing was sent, and no HoldEnded word is recorded.
// The asking session's watcher is the one that ends it.
func (d *Decision) Held() bool {
	return d.Open() && !d.HeldUntil.IsZero() && d.Sent == "" && d.HoldEnded == ""
}

// Holding reports whether the ask is held and its wait has not passed at
// now: the question is being asked in the conversation, and no reader
// outside it — a digest — has it yet.
func (d *Decision) Holding(now time.Time) bool { return d.Held() && now.Before(d.HeldUntil) }

// HeldBy is the held decisions the session asked, read from the canonical
// copies under the state directory — this machine's own, so the asking
// session's watcher finds its holds without the folder. The order is the
// directory's; a copy that cannot be read is left out.
func (s Store) HeldBy(sessionID string) []*Decision {
	list, err := os.ReadDir(s.DecisionsDir())
	if err != nil {
		return nil
	}
	var out []*Decision
	for _, e := range list {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || CheckID(id) != nil {
			continue
		}
		d, err := s.LoadDecision(id)
		if err != nil || d.SessionID != sessionID || !d.Held() {
			continue
		}
		out = append(out, d)
	}
	return out
}
