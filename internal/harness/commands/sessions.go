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
		fields := []string{
			idLine(r.SessionID),
			nameLine(r.SessionName),
			labelLine(r.HumanLabel),
			enumLine(r.State),
			"inbound=" + enumLine(r.Inbound),
			"principal=" + idLine(r.PrincipalRef),
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
