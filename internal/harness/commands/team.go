package commands

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/teamfile"
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
// revoke-member|transfer …` (6.4; P5-2; P7-11). `members` is a harness
// command with the documented layout; every other verb is the terminal
// pass-through with inherited stdio, so the user sees the adapter's own
// envelope (`--json` is parsed here and has no effect on them). Two verbs
// refuse to run inside a Claude Code session, in the one refusal shape of
// 6.4 (`usage`, exit 2, reason in_session): `revoke-member` and `transfer`,
// destructive administrative acts that a session reading untrusted
// teammate text must not be talked into (4.5 rule 15). The three verbs
// that handle the join secret — `create`, `join`, `rotate-secret` — run
// anywhere since P7-11, because on every path the secret travels in a
// 0600 --secret-file outside the repository and never on a stream the
// chat sees; inside a session `brigade` is on PATH, which is the point.
func Team(inv Invocation) error {
	verb, rest, err := verbOf(inv.Args, "team", teamVerbs)
	if err != nil {
		return err
	}
	raw, err := parseRaw(rest, true)
	if err != nil {
		return err
	}
	inv = inv.withRaw(raw)
	switch verb {
	case "members":
		if len(raw.Rest) > 0 {
			return usage("team members takes no arguments beyond --team and the global flags")
		}
		return members(inv, raw.Team)
	case "list":
		if len(raw.Rest) > 0 {
			return usage("team list takes no arguments")
		}
		return inv.teamList()
	case "revoke-member", "transfer":
		if inv.inSession() {
			return refuseAdminInSession()
		}
	case "rotate-secret":
		if err := inv.checkForwardedSecretFile(raw.Rest); err != nil {
			return err
		}
	}
	// The repo-file paths (P7-5; bridge-free since P7-7): `create` always
	// runs the rebuilt flow; a `join` at a terminal or inside a session
	// reads the project's team file (in a session the secret comes from
	// --secret-file, P7-11); a non-TTY join outside a session stays the
	// fully explicit stdin pass-through (correction 7).
	switch verb {
	case "create":
		return inv.teamCreate(raw)
	case "join":
		if inv.Deps.isTerminal(inv.In) || inv.inSession() {
			return inv.teamJoin(raw.Rest)
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

// checkForwardedSecretFile applies create's outside-the-repository rule to
// the --secret-file that `rotate-secret` forwards to the adapter (P7-11):
// the adapter checks that the path is absolute and its directory exists,
// and nothing about the checkout — which inside a session is the cwd. The
// flag is read here, never consumed; a relative path is left to the
// adapter's own refusal, and outside any checkout there is nothing to be
// inside of.
func (inv Invocation) checkForwardedSecretFile(rest []string) error {
	value := ""
	for i, arg := range rest {
		switch {
		case arg == "--secret-file" || arg == "-secret-file":
			if i+1 < len(rest) {
				value = rest[i+1]
			}
		case strings.HasPrefix(arg, "--secret-file=") || strings.HasPrefix(arg, "-secret-file="):
			_, value, _ = strings.Cut(arg, "=")
		}
	}
	if strings.HasPrefix(value, protocol.JoinSecretPrefix) {
		// A join secret where a path should be would ride the child's argv
		// (C-05); the value is never echoed.
		return usage("join secrets must never be passed on the command line; --secret-file names a file, and the secret goes in it")
	}
	if value == "" || !filepath.IsAbs(value) {
		return nil
	}
	if top, ok := teamfile.Toplevel(inv.mustGetwd()); ok {
		return checkSecretFileOutside(value, top)
	}
	return nil
}

// members implements `brigade team members [--json]`: one line per member,
// `principal=<ref>  <human_label> (unverified)  joined <date>  <n>
// sessions, seen <ago>`. The capability (team.roster) is not pre-checked:
// an adapter without it answers with its own error, which is reported as
// it is.
func members(inv Invocation, teamFlag string) error {
	t, err := inv.resolveSession(teamFlag)
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
