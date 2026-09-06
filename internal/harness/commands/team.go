package commands

import (
	"context"
	"strconv"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/protocol"
)

// The verbs of `brigade team`: the four of 6.4 and the three
// administrative pass-throughs of P5-2 (capability team.admin).
var teamVerbs = []string{"create", "join", "leave", "members", "rotate-secret", "revoke-member", "transfer", "status", "reset", "revoke-credentials", "list"}

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

// Team implements `brigade team create|join|leave|members|rotate-secret|
// revoke-member|transfer …` (6.4; P5-2). `members` is a harness command
// with the documented layout; every other verb is the terminal
// pass-through with inherited stdio, so the user sees the adapter's own
// envelope (`--json` is parsed here and has no effect on them). `create`,
// `join` and the three administrative verbs refuse to run inside a Claude
// Code session in the one refusal shape of 6.4 (`usage`, exit 2, reason
// in_session) with a per-family line: `create`, `join` and `rotate-secret`
// with RefusalInSession, because each handles the join secret;
// `revoke-member` and `transfer` with RefusalAdminInSession, because they
// are destructive administrative acts that a session reading untrusted
// teammate text must not be talked into (4.5 rule 15).
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
	case "list":
		if len(raw.Rest) > 0 || raw.Profile != "" {
			return usage("team list takes no arguments")
		}
		return inv.teamList()
	case "create", "join", "rotate-secret":
		if inv.inSession() {
			return refuseInSession()
		}
	case "revoke-member", "transfer":
		if inv.inSession() {
			return refuseAdminInSession()
		}
	}
	// The repo-file paths of P7-5: `create` and `join` WITHOUT --profile
	// run the rebuilt flows; the --profile forms below are the one-task
	// bridge P7-7 deletes, kept so the e2e rig and team.txtar hold.
	if raw.Profile == "" {
		switch verb {
		case "create":
			return inv.teamCreate(raw.Rest)
		case "join":
			if inv.Deps.isTerminal(inv.In) {
				return inv.teamJoin(raw.Rest)
			}
		}
	}
	switch verb {
	case "status", "reset", "revoke-credentials":
		// The adapter's frozen profile verbs, driven under the resolved
		// team key (pin → sole team → --team; --profile bridge wins).
		return inv.passThroughProfileVerb(verb, raw)
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
