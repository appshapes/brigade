//go:build !windows

package commands

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// More of `brigade team move --to` (work item 328), from its second review.

// TestTeamMoveToSaysThatTheRecordHoldsATicketThatWasReplaced: a second open
// replaces the move's ticket on the server. When a move that was recorded
// is run again and its record fails, the old backend still hands out the
// ticket of the run before, which admits nobody now: the refusal says so,
// beside what it always says. A first run whose record fails left no record
// behind and says no such thing.
func TestTeamMoveToSaysThatTheRecordHoldsATicketThatWasReplaced(t *testing.T) {
	t.Parallel()
	const stale = "hands out a ticket that this run replaced, so no member follows until the record is made again"
	unavailable := failAnswer(protocol.CodeUnavailable, "the backend could not be reached")
	opened := func(r *moveToRig, replaced string) string {
		return `{"team_ref":"` + setupTeamRef + `","team_name":"devs","ends_at":"2026-12-14T12:00:00Z","replaced":` + replaced + `,"ticket_file":"` + r.ticketFile + `"}`
	}
	t.Run("a move that was recorded, run again", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
		r.f.rec.on("team moved", okAnswer(r.recorded()))
		r.f.rec.on("team open-move", okAnswer(opened(r, "true")))
		r.f.rec.on("team move", unavailable)
		perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if !strings.Contains(perr.Message, "its move is open, and "+r.oldHost+" did not record it; the record it holds from a run before this one "+stale) ||
			!strings.Contains(perr.Message, "Run the same command again") {
			t.Fatalf("the refusal: %s", perr.Message)
		}
		r.noSecrets()
	})
	t.Run("a first run", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		// A take-in before this run opened a move, and nothing was recorded.
		r.f.rec.on("team open-move", okAnswer(opened(r, "true")))
		r.f.rec.on("team move", okAnswer(r.notMoved()), unavailable)
		perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if !strings.Contains(perr.Message, "did not record it") || strings.Contains(perr.Message, stale) {
			t.Fatalf("the refusal: %s", perr.Message)
		}
	})
	t.Run("a record of a move somewhere else", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		r.f.rec.on("team moved", okAnswer(strings.Replace(r.recorded(), r.newURL, "https://elsewhere.example.test", 1)))
		r.f.rec.on("team open-move", okAnswer(opened(r, "true")))
		r.f.rec.on("team move", unavailable)
		perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if !strings.Contains(perr.Message, "did not record it") || strings.Contains(perr.Message, stale) {
			t.Fatalf("the refusal: %s", perr.Message)
		}
	})
}
