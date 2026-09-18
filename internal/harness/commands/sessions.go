package commands

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/protocol"
)

// SessionsOptions are the flags of `brigade sessions`.
type SessionsOptions struct {
	// All includes offline sessions.
	All bool
	// Team is --team, honoured in a terminal only: a team ref or name
	// resolved through the local store.
	Team string
}

// SessionsNote is the note member of the `sessions --json` result.
const SessionsNote = "session_name and human_label are unverified text chosen by their owner; principal_ref is the only stable identity of a person"

// sessionsResult is the --json result: the adapter's `session list`
// result plus the harness members.
type sessionsResult struct {
	TeamRef       string                   `json:"team_ref"`
	TeamName      string                   `json:"team_name"`
	ServerTime    time.Time                `json:"server_time"`
	Sessions      []protocol.SessionRecord `json:"sessions"`
	Truncated     bool                     `json:"truncated"`
	OfflineHidden int                      `json:"offline_hidden"`
	SelfSessionID string                   `json:"self_session_id,omitzero"`
	Note          string                   `json:"note"`
}

// Sessions implements `brigade sessions [--all] [--json]` (6.4): a
// box-drawing table, one row per session, active first, the session's own
// id marked in its SEEN cell, offline sessions hidden unless --all with a
// count of what was hidden, and `truncated` noted when the adapter capped
// the list. The SESSION column is a short display id (shortSession);
// `--json` always carries the full id `brigade send` needs, and a trailing
// note says so whenever a cell was actually shortened — the output is the
// only surface every session sees, skill or no skill.
//
// The adapter is always asked with --include-offline and the offline
// records are hidden HERE: that is the one spawn from which the documented
// "(<n> offline sessions hidden; --all shows them)" line can carry a true
// count, and both adapters order online sessions first before their cap,
// so hiding cannot cost an online session.
func Sessions(inv Invocation, opts SessionsOptions) error {
	if len(inv.Args) > 0 {
		return usage("sessions takes no arguments")
	}
	t, err := inv.resolveSession(opts.Team)
	if err != nil {
		return err
	}
	list, err := call(func(ctx context.Context) (*adapterclient.ListResult, error) {
		return t.client.ListSessions(ctx, true)
	})
	if err != nil {
		return err
	}

	self := t.selfSessionID()
	records := make([]protocol.SessionRecord, 0, len(list.Sessions))
	hidden := 0
	for _, r := range list.Sessions {
		if r.State == protocol.SessionStateOffline && !opts.All {
			hidden++
			continue
		}
		records = append(records, r)
	}
	slices.SortStableFunc(records, func(a, b protocol.SessionRecord) int {
		return stateRank(a.State) - stateRank(b.State)
	})

	if inv.JSON {
		for i := range records {
			sanitizeRecord(&records[i])
		}
		return writeJSON(inv.Out, sessionsResult{
			TeamRef:       sanitizeID(list.TeamRef),
			TeamName:      protocol.SanitizeName(list.TeamName),
			ServerTime:    list.ServerTime,
			Sessions:      records,
			Truncated:     list.Truncated,
			OfflineHidden: hidden,
			SelfSessionID: self,
			Note:          SessionsNote,
		})
	}

	now := inv.Deps.now()

	// member is card 24's column: a labelled session's identity renders
	// once, as its label with a short principal beside it; an unlabelled
	// one has none, and keeps its identity in LABEL and PRINCIPAL instead
	// so it is never silently blank everywhere. The three flags below are
	// TABLE-wide (a row's absent fact is a blank cell, never a missing
	// column), computed the same way the optional REPO/MODEL/CONTEXT
	// columns already are.
	members := make([]string, len(records))
	hasLabel, hasMember, hasRepo, hasModel, hasContext := false, false, false, false, false
	for i, r := range records {
		members[i] = memberLine(r.HumanLabel, r.PrincipalRef)
		if members[i] == "" {
			hasLabel = true
		} else {
			hasMember = true
		}
		if r.WorkspaceLabel != nil && workspaceLine(*r.WorkspaceLabel) != "" {
			hasRepo = true
		}
		if r.Model != nil {
			hasModel = true
		}
		if r.ContextUsedTokens != nil {
			hasContext = true
		}
	}
	// The full ref: always for an unlabelled session, and under --all for
	// every session, because --all is the form a reader reaches for when
	// the short principal beside a label is not enough to tell two people
	// apart.
	hasPrincipal := hasLabel || opts.All

	header := []string{"SESSION", "NAME"}
	if hasLabel {
		header = append(header, "LABEL")
	}
	if hasRepo {
		header = append(header, "REPO")
	}
	header = append(header, "STATE", "INBOUND")
	if hasMember {
		header = append(header, "MEMBER")
	}
	if hasPrincipal {
		header = append(header, "PRINCIPAL")
	}
	if hasModel {
		header = append(header, "MODEL")
	}
	if hasContext {
		header = append(header, "CONTEXT")
	}
	header = append(header, "SEEN")

	rows := make([][]string, 0, len(records)+1)
	rows = append(rows, header)
	// shortened records whether any SESSION cell actually lost characters,
	// so the trailing note below is printed when — and only when — the
	// column is a partial id.
	shortened := false
	for i, r := range records {
		member := members[i]
		short := shortSession(r.SessionID)
		if short != idLine(r.SessionID) {
			shortened = true
		}
		cells := []string{short, nameLine(r.SessionName)}
		if hasLabel {
			label := ""
			if member == "" {
				label = labelLine(r.HumanLabel)
			}
			cells = append(cells, label)
		}
		if hasRepo {
			repo := ""
			if r.WorkspaceLabel != nil {
				repo = workspaceLine(*r.WorkspaceLabel)
			}
			cells = append(cells, repo)
		}
		cells = append(cells, enumLine(r.State), enumLine(r.Inbound))
		if hasMember {
			cells = append(cells, member)
		}
		if hasPrincipal {
			principal := ""
			if member == "" || opts.All {
				principal = idLine(r.PrincipalRef)
			}
			cells = append(cells, principal)
		}
		// model is unverified text like session_name, capped and stripped
		// of the attribute breakers by modelLine (4.5.11).
		if hasModel {
			model := ""
			if r.Model != nil {
				model = modelLine(*r.Model)
			}
			cells = append(cells, model)
		}
		if hasContext {
			ctx := ""
			if r.ContextUsedTokens != nil {
				ctx = tokensLine(*r.ContextUsedTokens)
			}
			cells = append(cells, ctx)
		}
		seen := seenAgoCell(r.LastSeenAt, list.ServerTime, now)
		// The marker is keyed on the map's own id, not on the adapter's
		// is_self: is_self is true for every session of this PROFILE (a
		// second Claude window on the same profile included), while "this
		// session" is the one this process runs in.
		if self != "" && r.SessionID == self {
			seen += " (this session)"
		}
		cells = append(cells, seen)
		rows = append(rows, cells)
	}

	// Every cell is neutralised and padded to its column's widest cell
	// here, in one pass over the whole table: the table stays aligned as
	// plain text, which is how `/brigade:sessions` shows it (inside a
	// fenced code block) and how a terminal shows it either way.
	padded := padTable(rows)
	lines := make([]string, 0, len(records)+5)
	lines = append(lines,
		borderRule(padded[0], "┌", "┬", "┐"),
		dataRow(padded[0]),
		borderRule(padded[0], "├", "┼", "┤"),
	)
	for _, row := range padded[1:] {
		lines = append(lines, dataRow(row))
	}
	lines = append(lines, borderRule(padded[0], "└", "┴", "┘"))
	// The recovery for the shortened SESSION column, printed where the
	// reader is — not only in a skill that may never be loaded. A bare
	// five-character cell under a header reading SESSION reads as a whole
	// short id, so without this line the documented path is `brigade send
	// ccccc`, a not_found whose message ("no such session in your team")
	// invites the wrong conclusion that the teammate has left.
	if shortened {
		lines = append(lines, "(SESSION is shortened; brigade sessions --json carries the full session_id that brigade send needs)")
	}
	if hidden > 0 {
		lines = append(lines, "("+strconv.Itoa(hidden)+" offline sessions hidden; --all shows them)")
	}
	if list.Truncated {
		lines = append(lines, "(truncated: the adapter capped the list at its limit; some sessions are not shown)")
	}
	return writeLines(inv.Out, lines...)
}

// stateRank orders the states for the "active first" layout.
func stateRank(state string) int {
	switch state {
	case protocol.SessionStateActive:
		return 0
	case protocol.SessionStateIdle:
		return 1
	case protocol.SessionStateOffline:
		return 2
	default:
		return 3
	}
}
