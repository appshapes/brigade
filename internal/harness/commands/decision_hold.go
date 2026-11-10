// decision_hold.go is `brigade decision`'s half of the hold (card 236,
// internal/harness/state/hold.go): an ask for the asking session's own
// user is recorded and not sent — the question is asked in the
// conversation, the host's own way — and the session's watcher sends it
// only when the member's wait has passed with no prompt there and the
// decision still open. An ask for anyone else goes out at once, as
// before: no conversation of this session reaches them.

package commands

import (
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

// The fixed texts of a held ask.
const (
	// DecisionHeldNote is the note of a --json result for a held ask.
	DecisionHeldNote = "recorded in the project's state folder and held: the person it is for is this session's own user, so ask them in the conversation; the session's watcher sends it — through the first gateway of their subscription's recipients that is on the roster, else to another session of theirs — only when their wait has passed with no prompt in this conversation and the decision still open, and brigade decision --close records the outcome"
	// decisionHeldTime is the spelling of the time a held ask goes out.
	decisionHeldTime = "15:04 UTC"
)

// decisionHeld is the --json account of a hold: when the ask goes out
// if nothing ends the hold first, and the wait it was held for.
type decisionHeld struct {
	Until time.Time `json:"until"`
	Wait  string    `json:"wait"`
}

// holdFor answers how long the ask for `to` is held, zero for an ask
// that goes out at once. It is held when three things hold: the label
// names this session's own member (ownAddressee, on the roster the caller
// read; with none, the entry's own label decides); the session has a
// conversation to ask in and a watcher that will send the ask later — an
// interactive session whose watcher runs and is this build's, since one
// from before card 236 knows no hold and is replaced only at the next
// prompt; and the member's wait is not zero (their subscription's,
// state.DefaultWait with none). member is the label the session's own
// entry carries.
func (inv Invocation) holdFor(t *target, sub *state.Subscriber, to, member string, sessions []protocol.SessionRecord) time.Duration {
	wait := sub.HoldFor()
	if wait <= 0 || t.session.NonInteractive {
		return 0
	}
	if w, alive := inv.liveWatcher(t); !alive || w.Version != buildinfo.String() {
		return 0
	}
	if !ownAddressee(to, member, t.session.BrigadeSessionID, sessions) {
		return 0
	}
	return wait
}

// ownAddressee reports whether the label names the asking session's own
// member, and nobody else: no roster session under it — the gateways
// left out — belongs to another principal, and it is the label the
// session's own entry carries or one a roster session of the asking
// session's principal carries. The entry's label and the roster's can
// differ for one person (a second Claude profile on a machine registers
// under the label of the profile that joined, card 191), so both count.
// With no roster to read, the entry's label decides.
func ownAddressee(to, member, self string, sessions []protocol.SessionRecord) bool {
	principal := ""
	for i := range sessions {
		if sessions[i].SessionID == self {
			principal = sessions[i].PrincipalRef
			break
		}
	}
	onRoster := false
	for i := range sessions {
		s := &sessions[i]
		if isGatewayHarness(s.Harness) || !strings.EqualFold(oneLine(protocol.SanitizeLabel(s.HumanLabel)), to) {
			continue
		}
		if principal == "" || s.PrincipalRef != principal {
			return false // a label another member carries is not this member's alone
		}
		onRoster = true
	}
	return onRoster || (member != "" && strings.EqualFold(to, member))
}

// liveWatcher is the pidfile entry of this session's watcher and whether
// that process is alive: the one process that can deliver to the session
// and send a held ask later.
func (inv Invocation) liveWatcher(t *target) (pidfile.Entry, bool) {
	lookup := inv.Deps.Lookup
	if lookup == nil {
		lookup = procutil.Lookup
	}
	v, err := pidfile.Check(pidfile.Path(t.stateDir, t.session.ClaudePID), lookup)
	if err != nil || !v.Found || !v.Alive || v.Entry.BrigadeSessionID != t.session.BrigadeSessionID {
		return pidfile.Entry{}, false
	}
	return v.Entry, true
}

// heldAskLine is the line of a held ask: who it is for, what the session
// does now, and when the ask goes out if nothing ends the hold.
func heldAskLine(d *state.Decision, wait time.Duration) string {
	return "held: " + d.To + " is this session's own user, so ask them here in the conversation; it is sent to them at " +
		d.HeldUntil.UTC().Format(decisionHeldTime) + " (in " + wait.String() + ") only if no prompt arrives here and it is still open — close it with the outcome when they answer"
}
