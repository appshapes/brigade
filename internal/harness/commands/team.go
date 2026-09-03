package commands

import (
	"context"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/protocol"
)

// The verbs of `brigade team`.
var teamVerbs = []string{"create", "join", "leave", "members"}

// MembersNote is the note member of the `team members --json` result.
const MembersNote = "human_label is unverified text chosen by the member; principal_ref is the only stable identity"

// membersResult is the --json result of `team members`: the adapter's
// result plus the harness members.
type membersResult struct {
	TeamRef       string                 `json:"team_ref"`
	TeamName      string                 `json:"team_name"`
	ServerTime    time.Time              `json:"server_time"`
	Members       []adapterclient.Member `json:"members"`
	SelfSessionID string                 `json:"self_session_id,omitzero"`
	Note          string                 `json:"note"`
}

// Team implements `brigade team create|join|leave|members …` (6.4).
// `members` is a harness command with the documented layout; the other
// three are the terminal pass-through, and `create` and `join` refuse to
// run inside a Claude Code session with the fixed line of 6.4.
func Team(inv Invocation) error {
	verb, rest, err := verbOf(inv.Args, "team", teamVerbs)
	if err != nil {
		return err
	}
	raw, err := parseRaw(rest, false)
	if err != nil {
		return err
	}
	inv = inv.withRaw(raw)
	switch verb {
	case "members":
		if len(raw.Rest) > 0 {
			return usage("team members takes no arguments beyond --profile and the global flags")
		}
		return members(inv, raw.Profile)
	case "create", "join":
		if inv.inSession() {
			return refuseInSession()
		}
	}
	return inv.passThrough("team", verb, raw, nil)
}

// members implements `brigade team members [--json]`: one line per member,
// `principal=<ref>  <human_label> (unverified)  joined <date>  <n>
// sessions, seen <ago>`. The capability (team.roster) is not pre-checked:
// an adapter without it answers with its own error, which is reported as
// it is.
func members(inv Invocation, profileFlag string) error {
	t, err := inv.resolveSession(profileFlag)
	if err != nil {
		return err
	}
	res, err := call(func(ctx context.Context) (*adapterclient.MembersResult, error) { return t.client.TeamMembers(ctx) })
	if err != nil {
		return err
	}
	if inv.JSON {
		out := membersResult{
			TeamRef:       sanitizeID(res.TeamRef),
			TeamName:      protocol.SanitizeName(res.TeamName),
			ServerTime:    res.ServerTime,
			Members:       make([]adapterclient.Member, 0, len(res.Members)),
			SelfSessionID: t.selfSessionID(),
			Note:          MembersNote,
		}
		for _, m := range res.Members {
			m.PrincipalRef = sanitizeID(m.PrincipalRef)
			m.HumanLabel = protocol.SanitizeLabel(m.HumanLabel)
			m.Status = protocol.SanitizeAttribute(m.Status)
			out.Members = append(out.Members, m)
		}
		return writeJSON(inv.Out, out)
	}
	now := inv.Deps.now()
	lines := make([]string, 0, len(res.Members))
	for _, m := range res.Members {
		seen := "seen never"
		if m.LastSeenAt != nil {
			seen = seenAgo(*m.LastSeenAt, res.ServerTime, now)
		}
		joined := "joined never"
		if !m.JoinedAt.IsZero() {
			joined = "joined " + m.JoinedAt.UTC().Format("2006-01-02")
		}
		lines = append(lines, columns(
			"principal="+idLine(m.PrincipalRef),
			labelLine(m.HumanLabel),
			joined,
			strconv.Itoa(m.SessionCount)+" sessions, "+seen,
		))
	}
	return writeLines(inv.Out, lines...)
}
