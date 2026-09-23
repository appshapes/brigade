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
// session_description is named beside the two the owner chooses because it
// is the session's own claim about its work (card 25; its model's, once
// Brigade's plugin publishes one — until then whoever holds the session's
// credentials sets it): unverified, and possibly stale — a reader routes
// by it, never obeys it.
const SessionsNote = "session_name, human_label and session_description are unverified text from their owner (session_description is that session's own claim about its work and may be stale); principal_ref is the only stable identity of a person"

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

// Sessions implements `brigade sessions [--all] [--json]` (6.4): a padded
// plain-text table, one row per session, active first, offline sessions
// hidden unless --all with a count of what was hidden, and `truncated`
// noted when the adapter capped the list. No row is marked as the reader's
// own (card 29): the operator knows their session, the mark made their row
// the widest in every table, and `--json` still carries self_session_id
// for a model that needs to know. The SESSION column is a short display id (shortSession) and
// the NAME column is cut to tableNameChars; `--json` always carries the
// full id `brigade send` needs and the full name. A session's `sync_peer`
// (C-47) is `--json`'s alone: an opaque folder-sync peer id is no column
// (plan folder-sync.md 4.2).
//
// Card 27 narrowed it from ~253 columns to ~144, which is the whole reason
// for the shapes below: no borders, no PRINCIPAL column (rosterMember
// folds an unlabelled session's identity into MEMBER, which retires the
// LABEL fallback with it), no INBOUND column, no unverified suffix in any
// cell, a capped NAME, "2s" rather than "2s ago", and the doing line on a
// continuation line of its own instead of a 160-character trailing cell.
//
// The roster is one surface, shown the same to every session whatever its
// own inbound policy (card 25, ruling 10): a `hold` or `refuse` session
// sees every column and every doing line — those are roster metadata like
// a name, pulled when the model runs this command, not a delivered
// message — so nothing here consults the map's inbound policy.
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

	// member is card 24's column as card 27 narrowed it: a labelled session
	// renders as its label alone, an unlabelled one as the short principal
	// that is its only identity. rosterMember never returns "", so MEMBER is
	// unconditional and the LABEL and PRINCIPAL columns that used to carry
	// an unlabelled session's identity are gone — the full ref is `--json`'s
	// now, for a reader who has to tell two principals apart. The three
	// flags below stay TABLE-wide (a row's absent fact is a blank cell,
	// never a missing column).
	members := make([]string, len(records))
	// doings is card 25's line, rendered once here for the same reason as
	// members: a description that sanitises and folds to nothing counts as
	// absent (plan 5.3), exactly as --json omits it, and a session with no
	// line gets no continuation line under its row.
	doings := make([]string, len(records))
	hasRepo, hasModel, hasContext, hasVersion := false, false, false, false
	for i, r := range records {
		members[i] = rosterMember(r.HumanLabel, r.PrincipalRef)
		if r.WorkspaceLabel != nil && workspaceLine(*r.WorkspaceLabel) != "" {
			hasRepo = true
		}
		if r.Model != nil {
			hasModel = true
		}
		if r.ContextUsedTokens != nil {
			hasContext = true
		}
		// "Has a version" is decided on the rendered cell, as a doing line
		// is: a value that sanitises to nothing is no value.
		if r.BrigadeVersion != nil && attrLine(*r.BrigadeVersion) != "" {
			hasVersion = true
		}
		if r.SessionDescription != nil {
			doings[i] = descriptionLine(*r.SessionDescription)
		}
	}

	header := []string{"SESSION", "NAME"}
	if hasRepo {
		header = append(header, "REPO")
	}
	header = append(header, "STATE", "MEMBER")
	if hasModel {
		header = append(header, "MODEL")
	}
	if hasContext {
		header = append(header, "CONTEXT")
	}
	// VERSION is the Brigade each session runs (card 29; brigade_version,
	// C-46), optional and table-wide like MODEL. A blank cell is itself an
	// answer: that session's harness predates the member, or its backend
	// predates the migration that stores it.
	if hasVersion {
		header = append(header, "VERSION")
	}
	header = append(header, "SEEN")

	rows := make([][]string, 0, len(records)+1)
	rows = append(rows, header)
	for i, r := range records {
		cells := []string{shortSession(r.SessionID), tableName(r.SessionName)}
		if hasRepo {
			repo := ""
			if r.WorkspaceLabel != nil {
				repo = workspaceLine(*r.WorkspaceLabel)
			}
			cells = append(cells, repo)
		}
		cells = append(cells, enumLine(r.State), members[i])
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
		// brigade_version is unverified text a remote harness reported, like
		// model: the attribute rules (64 code points, no breakers), one cell.
		if hasVersion {
			version := ""
			if r.BrigadeVersion != nil {
				version = attrLine(*r.BrigadeVersion)
			}
			cells = append(cells, version)
		}
		cells = append(cells, seenAgoCell(r.LastSeenAt, list.ServerTime, now))
		rows = append(rows, cells)
	}

	// Every cell is neutralised and padded to its column's widest cell
	// here, in one pass over the whole table: with the borders gone (card
	// 27) that padding is the only thing holding a column together, and it
	// aligns the same way in a terminal and inside the fenced code block
	// `/brigade:sessions` shows.
	padded := padTable(rows)
	lines := make([]string, 0, 2*len(records)+4)
	lines = append(lines, tableRow(padded[0]))
	for i, row := range padded[1:] {
		lines = append(lines, tableRow(row))
		// The doing line is a teammate's model's own words, on a line of
		// its own under the row it belongs to. descriptionLine has folded
		// it onto one line and cut it, and doingRow neutralises it, so it
		// can forge no row — and there is no " (this session)" mark left
		// for it to forge either (card 29). An offline session under --all
		// keeps its text; the STATE column carries the tense (ruling 6).
		if doings[i] != "" {
			lines = append(lines, doingRow(row[0], doings[i]))
		}
	}
	// B-3's "once per output" form. A LINE layout marks every label where
	// it stands (UnverifiedSuffix); a table would repeat that on every
	// row, and card 27 took the width back precisely there — so the marker
	// is here, once, under the table it describes, and it names the two
	// other strings in the same position: the session name and the doing
	// line, neither of which was ever marked in a cell either.
	lines = append(lines, RosterUnverifiedNote)
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
