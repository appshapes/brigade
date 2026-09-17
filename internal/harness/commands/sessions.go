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

// Sessions implements `brigade sessions [--all] [--json]` (6.4): one line
// per session, active first, the session's own id marked, offline sessions
// hidden unless --all with a count of what was hidden, and `truncated`
// noted when the adapter capped the list.
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
	lines := make([]string, 0, len(records)+2)
	for _, r := range records {
		// The member column (card 24): a labelled session shows its label
		// once, where the opaque principal used to be, with the first
		// characters of that principal beside it; an unlabelled one keeps
		// the label column after the name and the full `principal=<ref>`,
		// so its line is byte for byte the line it has always had.
		member := memberLine(r.HumanLabel, r.PrincipalRef)
		fields := []string{idLine(r.SessionID), nameLine(r.SessionName)}
		if member == "" {
			fields = append(fields, labelLine(r.HumanLabel))
		}
		// The repository column appears only for sessions that registered
		// a workspace label (P11-5: the harness sends the repository name
		// by default), so an older harness's line keeps its shape. It sits
		// by the name because that is what a sender scans for.
		if r.WorkspaceLabel != nil {
			if repo := workspaceLine(*r.WorkspaceLabel); repo != "" {
				fields = append(fields, "repo="+repo)
			}
		}
		fields = append(fields, enumLine(r.State), "inbound="+enumLine(r.Inbound))
		if member != "" {
			fields = append(fields, member)
		}
		// The full ref: always for an unlabelled session, and under --all
		// for every session, because --all is the form a reader reaches for
		// when the short principal is not enough to tell two people apart.
		if member == "" || opts.All {
			fields = append(fields, "principal="+idLine(r.PrincipalRef))
		}
		// model and context_used_tokens are optional on the wire (4.4.3):
		// a harness that reports neither, and an adapter without the two
		// capabilities, leave the line exactly the shape it has always
		// had, so the columns appear only for the sessions that have the
		// facts. model is unverified text like session_name, capped and
		// stripped of the attribute breakers by modelLine (4.5.11).
		if r.Model != nil {
			fields = append(fields, "model="+modelLine(*r.Model))
		}
		if r.ContextUsedTokens != nil {
			fields = append(fields, "context="+tokensLine(*r.ContextUsedTokens))
		}
		fields = append(fields, seenAgo(r.LastSeenAt, list.ServerTime, now))
		line := columns(fields...)
		// The marker is keyed on the map's own id, not on the adapter's
		// is_self: is_self is true for every session of this PROFILE (a
		// second Claude window on the same profile included), while "this
		// session" is the one this process runs in.
		if self != "" && r.SessionID == self {
			line += " (this session)"
		}
		lines = append(lines, line)
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
