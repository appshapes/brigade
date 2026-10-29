package commands

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/gitfacts"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// ReportOptions are the flags of `brigade report`.
type ReportOptions struct {
	// Recipients are the subscriber's recipients in precedence, separated
	// by `|`; Every the period as a duration; At and TZ a daily digest's
	// time and zone; Only the filter (`mine`): the flags of `subscribe`.
	Recipients string
	Every      string
	At         string
	TZ         string
	Only       string
	// To is the subscriber a `show` or a `send` is for, by their label.
	To string
}

// reportResult is the --json result of `brigade report`.
type reportResult struct {
	SelfSessionID string `json:"self_session_id"`
	// Reports is the file in force after a subscribe or an unsubscribe,
	// or read for a show or a send.
	Reports *state.Reports `json:"reports,omitzero"`
	// Subscriber is the entry a subscribe wrote; Label the one an
	// unsubscribe removed.
	Subscriber *state.Subscriber `json:"subscriber,omitzero"`
	Label      string            `json:"label,omitzero"`
	// Digest is the digest a show built: its body and summary.
	Digest *reportDigest `json:"digest,omitzero"`
	// Sent says how a send went out, one entry per subscriber.
	Sent []reportSent `json:"sent,omitzero"`
	Note string       `json:"note"`
}

// reportDigest is the --json form of a built digest.
type reportDigest struct {
	Body          string `json:"body"`
	Summary       string `json:"summary"`
	DecisionsOpen int    `json:"decisions_open"`
	Commits       int    `json:"commits"`
	Active        int    `json:"active"`
}

// reportSent is one subscriber's share of a send: their digest and how it
// went, or why it did not.
type reportSent struct {
	Label     string        `json:"label"`
	How       string        `json:"how,omitzero"`
	To        string        `json:"to,omitzero"`
	SessionID string        `json:"session_id,omitzero"`
	MessageID string        `json:"message_id,omitzero"`
	NotSent   string        `json:"not_sent,omitzero"`
	Digest    *reportDigest `json:"digest,omitzero"`
}

// The fixed texts of `brigade report` (card 162, plan §5.2 and §5.5; card
// 167, the subscribers).
const (
	// ReportUsage is the whole `usage` message for a bad verb.
	ReportUsage = "report takes one verb: subscribe (--recipients, --every, --at, --tz, --only mine), unsubscribe, show [--to <label>], or send [--to <label>]"
	// The notes of the --json results.
	ReportSubscribedNote   = "written to reports.json in the project's state folder, which syncs to every teammate's checkout; the sessions' watchers produce your digest on the period, and a brigade decision --to your label reaches these recipients at once"
	ReportUnsubscribedNote = "removed from reports.json in the project's state folder; no digest comes, and a decision --to your label goes to your best session as a team message"
	ReportShowNote         = "the digest the producer would send now, built from the state folder and the roster; nothing was sent and no marker was written"
	ReportSentNote         = "sent now and the marker written for each subscriber it reached; their next digest on the period starts from this one"
	// ReportNotIgnored is the `config` message of the address rule
	// (ruling 9): an address goes into the folder only when git ignores
	// it.
	ReportNotIgnored = "an email address goes into reports.json only when git ignores the state folder, and this checkout does not ignore it: add the folder to .gitignore first (a Slack handle or channel is fine either way)"
	// ReportNoLabel is the `config` message of a session that has no
	// roster label to subscribe under.
	ReportNoLabel = "this session has no roster label to subscribe under: the plugin's label option names one (on Claude Code the account's email by default)"
	// ReportNobody is the `config` message of a send with nobody
	// subscribed.
	ReportNobody        = "nobody is subscribed to the digest: brigade report subscribe writes your entry"
	ReasonNotIgnored    = "folder_not_ignored"
	ReasonNoGit         = "no_git"
	ReasonBadEvery      = "every_not_a_duration"
	ReasonBadAt         = "at_not_a_time"
	ReasonBadTZ         = "tz_unknown"
	ReasonBadOnly       = "only_unknown"
	ReasonNoRecipient   = "no_recipient"
	ReasonLabelNeeded   = "label_needed"
	ReasonNoSubscriber  = "no_subscriber"
	ReasonNotSubscribed = "not_subscribed"
	ReasonReportsBroken = "reports_unreadable"
	// ReportMaxEvery bounds the period; ReportMinEvery the floor.
	ReportMinEvery = time.Minute
	ReportMaxEvery = 30 * 24 * time.Hour
	// reportCheckIgnoreBudget is the one `git check-ignore`'s budget.
	reportCheckIgnoreBudget = 3 * time.Second
)

// Report implements `brigade report subscribe|unsubscribe|show|send
// [--json]` (card 162, plan §5.2 and §5.5; card 167). `subscribe` writes
// the caller's own entry into reports.json in the state folder — under
// the roster label the session's state entry carries, with the
// recipients in precedence (a Slack handle or channel, an address), the
// period and the filter — refusing an address unless `git check-ignore`
// says the folder is ignored (ruling 9); nobody writes another's entry.
// `unsubscribe` removes it. `show` prints the digest a subscriber would
// get now, the ad hoc report; `send` produces and sends one now to one
// subscriber or to every one, each marker written, so their period
// starts afresh. Every verb runs inside a session: the folder is the
// session's map's.
func Report(inv Invocation, opts ReportOptions) error {
	if len(inv.Args) != 1 {
		return usage(ReportUsage)
	}
	verb := inv.Args[0]
	switch verb {
	case "subscribe", "unsubscribe", "show", "send":
	default:
		return usage(ReportUsage)
	}
	if !inv.inSession() {
		return notInSession("report")
	}
	switch verb {
	case "subscribe":
		return reportSubscribe(inv, opts)
	case "unsubscribe":
		return reportUnsubscribe(inv)
	}
	return reportDigests(inv, verb, opts)
}

// reportDigests is `show` and `send`.
func reportDigests(inv Invocation, verb string, opts ReportOptions) error {
	t, err := inv.resolveSession("")
	if err != nil {
		return err
	}
	s, err := stateStoreOf(t.session, t.stateDir)
	if err != nil {
		return err
	}
	m := t.session
	reports, err := state.ReadReports(s.Folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		reports = &state.Reports{Version: state.ReportsVersion}
	case err != nil:
		return &protocol.Error{Code: protocol.CodeConfig, Message: "reports.json in the state folder is not readable; a subscribe rewrites it", Details: map[string]string{"reason": ReasonReportsBroken}}
	}
	// The subscribers the verb is for: the one named, else for a show the
	// caller's own subscription (none: everything, since the start), and
	// for a send every subscriber.
	var targets []*state.Subscriber
	if to := strings.TrimSpace(opts.To); to != "" {
		sub := reports.Subscriber(to)
		if sub == nil {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "no subscriber has that label; brigade report show --json lists them", Details: map[string]string{"field": "to", "reason": ReasonNoSubscriber}}
		}
		targets = []*state.Subscriber{sub}
	} else if verb == "show" {
		if e, lerr := s.Load(m.BrigadeSessionID); lerr == nil && e.Member != "" {
			if sub := reports.Subscriber(e.Member); sub != nil {
				targets = []*state.Subscriber{sub}
			}
		}
		if len(targets) == 0 {
			targets = []*state.Subscriber{{}}
		}
	} else {
		for i := range reports.Subscribers {
			targets = append(targets, &reports.Subscribers[i])
		}
		if len(targets) == 0 {
			return &protocol.Error{Code: protocol.CodeConfig, Message: ReportNobody, Details: map[string]string{"reason": ReasonNoSubscriber}}
		}
	}
	list, err := callWithin(RosterTimeout, func(ctx context.Context) (*adapterclient.ListResult, error) {
		return t.client.ListSessions(ctx, false)
	})
	if err != nil {
		return err
	}
	now := inv.Deps.now()
	repo := m.SyncScope
	if repo == "" {
		repo = m.WorkspaceLabel
	}
	entries, decisions := state.ReadFolder(s.Folder), state.ReadDecisions(s.Folder)
	build := func(sub *state.Subscriber) digest.Output {
		since := time.Time{}
		if sub.Label != "" {
			marker, merr := digest.MarkerOf(s.Folder, reports, sub)
			if merr != nil && !errors.Is(merr, fs.ErrNotExist) {
				inv.logger().Debug("report: a marker is unreadable; read as none", log.Err(merr))
			}
			if marker != nil {
				since = marker.ProducedAt
			}
		}
		return digest.Build(digest.Input{
			Team: m.TeamName, Repository: repo, Producer: m.SessionName, Now: now, Since: since,
			Entries: entries, Decisions: decisions, Roster: list.Sessions, For: sub.Label, Only: sub.Only,
		})
	}
	if verb == "show" {
		out := build(targets[0])
		if inv.JSON {
			return writeJSON(inv.Out, reportResult{SelfSessionID: m.BrigadeSessionID, Reports: reports, Digest: digestOf(out), Note: ReportShowNote})
		}
		return writeLines(inv.Out, out.Body)
	}
	var sent []reportSent
	var lines []string
	reached := 0
	for _, sub := range targets {
		out := build(sub)
		r := reportSent{Label: sub.Label, Digest: digestOf(out)}
		d, err := callWithin(adapterclient.DefaultTimeout, func(ctx context.Context) (digest.Delivery, error) {
			return digest.Deliver(ctx, t.client, m.BrigadeSessionID, sub, list.Sessions, out.Body, out.Summary, now)
		})
		if err != nil {
			r.NotSent = "the send failed (" + string(codeOf(err)) + ")"
			if errors.Is(err, digest.ErrNoRecipient) {
				r.NotSent = "no gateway of their recipients is on the roster, and they have no session that can receive a message"
			} else {
				inv.logger().Warn("report: a digest was not sent", log.Err(err))
			}
			sent = append(sent, r)
			lines = append(lines, "not sent: "+sub.Label+": "+r.NotSent)
			continue
		}
		reached++
		r.How, r.To, r.SessionID, r.MessageID = d.How, d.To, d.SessionID, sanitizeID(d.MessageID)
		if err := digest.WriteMarker(s.Folder, &digest.Marker{
			Label: sub.Label, ProducedAt: now, SessionID: m.BrigadeSessionID, SessionName: m.SessionName,
			DecisionsOpen: out.DecisionsOpen, Commits: out.Commits, Active: out.Active,
		}); err != nil {
			inv.logger().Warn("report: a marker was not written", log.Err(err))
		}
		sent = append(sent, r)
		line := "sent: " + out.Summary + " to " + sub.Label + " at " + r.To + " through the " + r.How + " gateway, message " + idLine(r.MessageID)
		if r.How == state.SentSession {
			line = "sent: " + out.Summary + " to " + sub.Label + " at their session " + shortSession(r.SessionID) + " as a team message (no gateway on the roster), message " + idLine(r.MessageID)
		}
		lines = append(lines, line)
	}
	if reached == 0 {
		return &protocol.Error{Code: protocol.CodeNotFound, Message: "no subscriber could be reached: " + sent[0].NotSent + "; nothing was sent", Details: map[string]string{"reason": ReasonNoRecipient}}
	}
	if inv.JSON {
		return writeJSON(inv.Out, reportResult{SelfSessionID: m.BrigadeSessionID, Reports: reports, Sent: sent, Note: ReportSentNote})
	}
	return writeLines(inv.Out, lines...)
}

// digestOf is the --json form of a built digest.
func digestOf(out digest.Output) *reportDigest {
	return &reportDigest{Body: out.Body, Summary: out.Summary, DecisionsOpen: out.DecisionsOpen, Commits: out.Commits, Active: out.Active}
}

// reportSubscribe is the subscribe verb: the flags checked, the caller's
// label read from their state entry, the address rule applied, the file
// read, the entry put in, the file written as version 2 — and the
// marker of a version-1 file's manager carried over to theirs.
func reportSubscribe(inv Invocation, opts ReportOptions) error {
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
	only := strings.TrimSpace(opts.Only)
	if only != "" && only != state.OnlyMine {
		return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--only takes one value, mine: the digest's decision sections then list only the decisions addressed to you", Details: map[string]string{"field": "only", "reason": ReasonBadOnly}}
	}
	m, stateDir, err := inv.sessionMap()
	if err != nil {
		return err
	}
	s, err := stateStoreOf(m, stateDir)
	if err != nil {
		return err
	}
	label, err := ownLabel(s, m)
	if err != nil {
		return err
	}
	if len(recipients) == 0 && state.RecipientKind(label) == state.RecipientEmail {
		hasAddress = true // the label fills the empty list (Subscriber.Sanitize)
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
	reports, err := reportsForWrite(s.Folder)
	if err != nil {
		return err
	}
	legacy := ""
	if reports.Legacy && len(reports.Subscribers) == 1 {
		legacy = reports.Subscribers[0].Label
	}
	sub := state.Subscriber{Label: label, Recipients: recipients, Every: every, At: at, TZ: tz, Only: only}
	sub.Sanitize()
	reports.Subscribe(sub)
	if err := state.WriteReports(s.Folder, reports); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "reports.json could not be written to the state folder"}
	}
	if legacy != "" {
		if err := digest.AdoptLegacyMarker(s.Folder, legacy); err != nil {
			inv.logger().Warn("report: the 0.43 marker was not carried over", log.Err(err))
		}
	}
	inv.logger().Info("report subscribed")
	if inv.JSON {
		return writeJSON(inv.Out, reportResult{SelfSessionID: m.BrigadeSessionID, Reports: reports, Subscriber: &sub, Note: ReportSubscribedNote})
	}
	return writeLines(inv.Out, "subscribed: "+subscriberLine(&sub))
}

// reportUnsubscribe is the unsubscribe verb: the caller's entry removed.
func reportUnsubscribe(inv Invocation) error {
	m, stateDir, err := inv.sessionMap()
	if err != nil {
		return err
	}
	s, err := stateStoreOf(m, stateDir)
	if err != nil {
		return err
	}
	label, err := ownLabel(s, m)
	if err != nil {
		return err
	}
	reports, err := reportsForWrite(s.Folder)
	if err != nil {
		return err
	}
	legacy := ""
	if reports.Legacy && len(reports.Subscribers) == 1 {
		legacy = reports.Subscribers[0].Label
	}
	if !reports.Unsubscribe(label) {
		return &protocol.Error{Code: protocol.CodeNotFound, Message: "this session's member is not subscribed", Details: map[string]string{"reason": ReasonNotSubscribed}}
	}
	if err := state.WriteReports(s.Folder, reports); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "reports.json could not be written to the state folder"}
	}
	if legacy != "" && !strings.EqualFold(legacy, label) {
		if err := digest.AdoptLegacyMarker(s.Folder, legacy); err != nil {
			inv.logger().Warn("report: the 0.43 marker was not carried over", log.Err(err))
		}
	}
	inv.logger().Info("report unsubscribed")
	if inv.JSON {
		return writeJSON(inv.Out, reportResult{SelfSessionID: m.BrigadeSessionID, Reports: reports, Label: label, Note: ReportUnsubscribedNote})
	}
	return writeLines(inv.Out, "unsubscribed: "+label)
}

// ownLabel is the caller's roster label, read from their state entry —
// the one the hook registered — for the entry the subscribe writes.
func ownLabel(s state.Store, m *sessionmap.ByPID) (string, error) {
	e, err := s.Load(m.BrigadeSessionID)
	if err != nil {
		return "", &protocol.Error{Code: protocol.CodeConfig, Message: NoteNoEntry, Details: map[string]string{"reason": ReasonNoStateEntry}}
	}
	if e.Member == "" {
		return "", &protocol.Error{Code: protocol.CodeConfig, Message: ReportNoLabel, Details: map[string]string{"reason": ReasonLabelNeeded}}
	}
	return e.Member, nil
}

// reportsForWrite reads the file for a write: a missing one is empty, a
// broken one is refused rather than overwritten.
func reportsForWrite(folder string) (*state.Reports, error) {
	reports, err := state.ReadReports(folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &state.Reports{Version: state.ReportsVersion}, nil
	case err != nil:
		return nil, &protocol.Error{Code: protocol.CodeConfig, Message: "reports.json in the state folder is not readable, and this command does not overwrite a teammate's file: fix or remove it", Details: map[string]string{"reason": ReasonReportsBroken}}
	}
	return reports, nil
}

// subscriberLine is one subscriber as the human lines print them.
func subscriberLine(s *state.Subscriber) string {
	line := s.Label + ", recipients "
	if len(s.Recipients) == 0 {
		line += "your own session (no handle, channel or address)"
	} else {
		line += strings.Join(s.Recipients, " | ")
	}
	if s.At != "" {
		line += ", daily at " + s.At
		if s.TZ != "" {
			line += " " + s.TZ
		}
	} else {
		line += ", every " + s.Period().String()
	}
	if s.Only == state.OnlyMine {
		line += ", the decisions for you only"
	}
	return line
}
