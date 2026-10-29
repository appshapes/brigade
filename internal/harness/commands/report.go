package commands

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/gitfacts"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// ReportOptions are the flags of `brigade report configure`.
type ReportOptions struct {
	// Manager is the manager's roster label; Recipients the recipients
	// in precedence, separated by `|`; Every the period as a duration;
	// At and TZ a daily digest's time and zone.
	Manager    string
	Recipients string
	Every      string
	At         string
	TZ         string
}

// reportResult is the --json result of `brigade report`.
type reportResult struct {
	SelfSessionID string `json:"self_session_id"`
	// Reports is the settings in force after a configure, or read for a
	// show or a send.
	Reports *state.Reports `json:"reports,omitzero"`
	// Digest is the digest a show or a send built: its body and summary.
	Digest *reportDigest `json:"digest,omitzero"`
	// Sent says how a send went out.
	Sent *digest.Delivery `json:"sent,omitzero"`
	Note string           `json:"note"`
}

// reportDigest is the --json form of a built digest.
type reportDigest struct {
	Body          string `json:"body"`
	Summary       string `json:"summary"`
	DecisionsOpen int    `json:"decisions_open"`
	Commits       int    `json:"commits"`
	Active        int    `json:"active"`
}

// The fixed texts of `brigade report` (card 162, plan §5.2 and §5.5).
const (
	// ReportUsage is the whole `usage` message for a bad verb.
	ReportUsage = "report takes one verb: configure (--manager, --recipients, --every, --at, --tz), show, or send"
	// ReportConfiguredNote is the note of a --json configure; ReportShowNote
	// of a show; ReportSentNote of a send.
	ReportConfiguredNote = "written to reports.json in the project's state folder, which syncs to every teammate's checkout; the session's watchers produce the digest on the period and brigade decision sends its asks to these recipients"
	ReportShowNote       = "the digest the producer would send now, built from the state folder and the roster; nothing was sent and the marker was not written"
	ReportSentNote       = "sent to the manager now and the marker written; the next digest on the period starts from this one"
	// ReportNotIgnored is the `config` message of the address rule
	// (ruling 9): an address goes into the folder only when git ignores
	// it.
	ReportNotIgnored    = "an email address goes into reports.json only when git ignores the state folder, and this checkout does not ignore it: add the folder to .gitignore first (a Slack handle or channel is fine either way)"
	ReasonNotIgnored    = "folder_not_ignored"
	ReasonNoGit         = "no_git"
	ReasonBadEvery      = "every_not_a_duration"
	ReasonBadAt         = "at_not_a_time"
	ReasonBadTZ         = "tz_unknown"
	ReasonNoRecipient   = "no_recipient"
	ReasonManagerNeeded = "manager_needed"
	// ReportMaxEvery bounds the period; ReportMinEvery the floor.
	ReportMinEvery = time.Minute
	ReportMaxEvery = 30 * 24 * time.Hour
	// reportCheckIgnoreBudget is the one `git check-ignore`'s budget.
	reportCheckIgnoreBudget = 3 * time.Second
)

// Report implements `brigade report configure|show|send [--json]` (card
// 162, plan §5.2 and §5.5). `configure` writes the manager's settings
// into reports.json in the state folder — the manager's roster label,
// the recipients in precedence (a Slack handle or channel, an address),
// the period — refusing an address unless `git check-ignore` says the
// folder is ignored (ruling 9); `show` prints the digest the producer
// would send now, the ad hoc report; `send` produces and sends one now
// and writes the marker, so the period starts afresh. Every verb runs
// inside a session: the folder is the session's map's.
func Report(inv Invocation, opts ReportOptions) error {
	if len(inv.Args) != 1 {
		return usage(ReportUsage)
	}
	verb := inv.Args[0]
	switch verb {
	case "configure", "show", "send":
	default:
		return usage(ReportUsage)
	}
	if !inv.inSession() {
		return notInSession("report")
	}
	if verb == "configure" {
		return reportConfigure(inv, opts)
	}
	t, err := inv.resolveSession("")
	if err != nil {
		return err
	}
	s, err := stateStoreOf(t.session, t.stateDir)
	if err != nil {
		return err
	}
	reports, err := state.ReadReports(s.Folder)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &protocol.Error{Code: protocol.CodeConfig, Message: DecisionNoManager, Details: map[string]string{"reason": ReasonNoManager}}
		}
		return err
	}
	if reports.Manager == "" {
		return &protocol.Error{Code: protocol.CodeConfig, Message: DecisionNoManager, Details: map[string]string{"reason": ReasonNoManager}}
	}
	list, err := callWithin(RosterTimeout, func(ctx context.Context) (*adapterclient.ListResult, error) {
		return t.client.ListSessions(ctx, false)
	})
	if err != nil {
		return err
	}
	now := inv.Deps.now()
	marker, merr := digest.ReadMarker(s.Folder)
	if merr != nil && !errors.Is(merr, fs.ErrNotExist) {
		inv.logger().Debug("report: the marker is unreadable; read as none", log.Err(merr))
	}
	since := time.Time{}
	if marker != nil {
		since = marker.ProducedAt
	}
	m := t.session
	repo := m.SyncScope
	if repo == "" {
		repo = m.WorkspaceLabel
	}
	out := digest.Build(digest.Input{
		Team: m.TeamName, Repository: repo, Producer: m.SessionName, Now: now, Since: since,
		Entries: state.ReadFolder(s.Folder), Decisions: state.ReadDecisions(s.Folder), Roster: list.Sessions,
	})
	built := &reportDigest{Body: out.Body, Summary: out.Summary, DecisionsOpen: out.DecisionsOpen, Commits: out.Commits, Active: out.Active}
	if verb == "show" {
		if inv.JSON {
			return writeJSON(inv.Out, reportResult{SelfSessionID: m.BrigadeSessionID, Reports: reports, Digest: built, Note: ReportShowNote})
		}
		return writeLines(inv.Out, out.Body)
	}
	sent, err := callWithin(adapterclient.DefaultTimeout, func(ctx context.Context) (digest.Delivery, error) {
		return digest.Deliver(ctx, t.client, m.BrigadeSessionID, reports, list.Sessions, out.Body, out.Summary, now)
	})
	if err != nil {
		if errors.Is(err, digest.ErrNoRecipient) {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "no gateway of the recipients is on the roster, and the manager has no session that can receive a message; nothing was sent", Details: map[string]string{"reason": ReasonNoRecipient}}
		}
		return err
	}
	sent.MessageID = sanitizeID(sent.MessageID)
	if err := digest.WriteMarker(s.Folder, &digest.Marker{
		ProducedAt: now, SessionID: m.BrigadeSessionID, SessionName: m.SessionName,
		DecisionsOpen: out.DecisionsOpen, Commits: out.Commits, Active: out.Active,
	}); err != nil {
		inv.logger().Warn("report: the marker was not written", log.Err(err))
	}
	if inv.JSON {
		return writeJSON(inv.Out, reportResult{SelfSessionID: m.BrigadeSessionID, Reports: reports, Digest: built, Sent: &sent, Note: ReportSentNote})
	}
	line := "sent: " + out.Summary + " to " + sent.To + " through the " + sent.How + " gateway, message " + idLine(sent.MessageID)
	if sent.How == state.SentSession {
		line = "sent: " + out.Summary + " to the manager's session " + shortSession(sent.SessionID) + " as a team message (no gateway on the roster), message " + idLine(sent.MessageID)
	}
	return writeLines(inv.Out, line)
}

// reportConfigure is the configure verb: the flags checked, the address
// rule applied, the file written.
func reportConfigure(inv Invocation, opts ReportOptions) error {
	home, token := adapterkit.HomeDir(inv.Environ), adapterkit.Getenv(inv.Environ, envMessagingToken)
	manager, reason := doing.CleanText(opts.Manager, home, protocol.MaxHumanLabelChars, token)
	if reason != "" {
		if reason == doing.ReasonEmpty {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--manager names the manager by their roster label", Details: map[string]string{"field": "manager", "reason": ReasonManagerNeeded}}
		}
		return textError("manager", reason, protocol.MaxHumanLabelChars)
	}
	var recipients []string
	hasAddress := false
	for part := range strings.SplitSeq(opts.Recipients, "|") {
		r := strings.TrimSpace(part)
		if r == "" {
			continue
		}
		kind := state.RecipientKind(r)
		if kind == "" {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "a recipient is a Slack handle (@name), a channel (#name) or an email address", Details: map[string]string{"field": "recipients", "reason": "recipient_shape"}}
		}
		if kind == state.RecipientEmail {
			hasAddress = true
		}
		recipients = append(recipients, r)
	}
	if len(recipients) == 0 {
		// The manager's label is a recipient when it has a recipient's shape
		// — an email address, as a roster label is by default (owner
		// 2026-10-29) — so naming the manager is enough.
		if state.RecipientKind(manager) == "" {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--recipients names at least one recipient, separated by |; it may be left out only when --manager is an email address, which is then the recipient", Details: map[string]string{"field": "recipients", "reason": ReasonNoRecipient}}
		}
		recipients = []string{manager}
		hasAddress = state.RecipientKind(manager) == state.RecipientEmail
	}
	if len(recipients) > state.MaxRecipients {
		return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "at most " + itoa(state.MaxRecipients) + " recipients", Details: map[string]string{"field": "recipients", "reason": "too_many_recipients"}}
	}
	every := strings.TrimSpace(opts.Every)
	if every != "" {
		d, err := time.ParseDuration(every)
		if err != nil || d < ReportMinEvery || d > ReportMaxEvery {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--every is a duration from 1m to 720h, such as 24h or 4h", Details: map[string]string{"field": "every", "reason": ReasonBadEvery}}
		}
		every = d.String()
	}
	at := strings.TrimSpace(opts.At)
	if at != "" {
		if _, err := time.Parse("15:04", at); err != nil {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--at is a time of day, HH:MM", Details: map[string]string{"field": "at", "reason": ReasonBadAt}}
		}
	}
	tz := strings.TrimSpace(opts.TZ)
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--tz is an IANA zone name, such as America/Los_Angeles", Details: map[string]string{"field": "tz", "reason": ReasonBadTZ}}
		}
	}
	m, stateDir, err := inv.sessionMap()
	if err != nil {
		return err
	}
	s, err := stateStoreOf(m, stateDir)
	if err != nil {
		return err
	}
	if hasAddress {
		ignored, err := callWithin(reportCheckIgnoreBudget, func(ctx context.Context) (bool, error) {
			return gitfacts.Runner{Top: m.SyncRoot, Environ: inv.Environ, Timeout: reportCheckIgnoreBudget}.Ignored(ctx, s.Folder)
		})
		if err != nil {
			return &protocol.Error{Code: protocol.CodeConfig, Message: "git could not say whether the state folder is ignored, so no address was written: " + ReportNotIgnored, Details: map[string]string{"reason": ReasonNoGit}}
		}
		if !ignored {
			return &protocol.Error{Code: protocol.CodeConfig, Message: ReportNotIgnored, Details: map[string]string{"reason": ReasonNotIgnored}}
		}
	}
	r := &state.Reports{Version: state.Version, Manager: manager, Recipients: recipients, Every: every, At: at, TZ: tz}
	if err := state.WriteReports(s.Folder, r); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "reports.json could not be written to the state folder"}
	}
	inv.logger().Info("report configured")
	if inv.JSON {
		return writeJSON(inv.Out, reportResult{SelfSessionID: m.BrigadeSessionID, Reports: r, Note: ReportConfiguredNote})
	}
	period := "every " + r.Period().String()
	if r.At != "" {
		period = "daily at " + r.At
		if r.TZ != "" {
			period += " " + r.TZ
		}
	}
	return writeLines(inv.Out, "configured: manager "+manager+", recipients "+strings.Join(recipients, " | ")+", "+period)
}
