package commands

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// DecisionOptions are the flags of `brigade decision`.
type DecisionOptions struct {
	// To is the person the decision is for, by their roster label
	// (required for an ask); Options are the answers offered, separated
	// by `|`; Default the one taken when they say nothing; By the date an
	// answer is needed by (YYYY-MM-DD); Card the card the question
	// belongs to.
	To      string
	Options string
	Default string
	By      string
	Card    int
	// Close is the id of a decision to close, with the outcome on stdin.
	Close string
}

// decisionResult is the --json result of `brigade decision`.
type decisionResult struct {
	SelfSessionID string          `json:"self_session_id"`
	Decision      *state.Decision `json:"decision"`
	// Sent says how the ask went out: through which gateway or session,
	// to whom, and the message id; absent for a close and when nothing
	// went out.
	Sent *decisionSent `json:"sent,omitzero"`
	// NotSent says why nothing went out, "" otherwise.
	NotSent string `json:"not_sent,omitzero"`
	Note    string `json:"note"`
}

// decisionSent is the --json account of the ask's delivery.
type decisionSent struct {
	How       string `json:"how"`
	To        string `json:"to"`
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
}

// The fixed texts of `brigade decision` (card 162, plan §5.2).
const (
	// DecisionUsage is the whole `usage` message for positional arguments.
	DecisionUsage = "decision takes no arguments: the question travels on stdin in a quoted heredoc (<<'EOF'); --to names the person it is for by their roster label; --options, --default, --by and --card describe it; --close <id> closes one with the outcome on stdin"
	// DecisionNote is the note of a --json result for an ask;
	// DecisionClosedNote for a close.
	DecisionNote       = "recorded in the project's state folder and sent to the person it is for: through the first gateway of their subscription's recipients that is on the roster, else to their best session as a team message; they answer however they wish, and brigade decision --close records the outcome"
	DecisionClosedNote = "closed in the project's state folder; the next digest lists it under the decisions closed"
	// DecisionNoTo is the `invalid_input` message of an ask with no --to
	// (owner ruling 2026-10-29: an error, not a guess).
	DecisionNoTo   = "--to names the person the decision is for, by their label as the roster shows it: brigade decision --to <label> <<'EOF' … EOF"
	ReasonToNeeded = "to_needed"
	// DecisionNotSentSuffix closes the line of an ask that was recorded
	// but reached nobody.
	DecisionNotSentSuffix = "; the next digest carries it"
	// The details.reason values of the flag refusals.
	ReasonTooManyOptions   = "too_many_options"
	ReasonDefaultNotOption = "default_not_an_option"
	ReasonByNotADate       = "by_not_a_date"
	ReasonCardNegative     = "card_negative"
	ReasonAlreadyClosed    = "already_closed"
	ReasonNotTheAsker      = "not_the_asker"
)

// Decision implements `brigade decision --to <label> [--options <a | b>]
// [--default <option>] [--by <date>] [--card <n>] [--json]` and `brigade
// decision --close <id> [--json]` (card 162, plan §5.2; card 167, the
// addressee). An ask writes `decisions/<id>.json` — the canonical copy
// under the state directory, the projection in the folder — and sends it
// at once, in the labelled shape the digest uses, to the person --to
// names: through the first gateway of their subscription's recipients
// that is on the roster (Slack before email whatever the shape); with no
// subscription or no gateway there, to their own best session as a team
// message; with neither, it is recorded and the line says so, and the
// next digest carries it. No --to is an error, never a guess (owner
// ruling 2026-10-29). A close records the outcome and who closed it: the
// asking session, or another session of the member who asked. Every text
// is cleaned as a doing line is, before anything is resolved.
func Decision(inv Invocation, opts DecisionOptions) error {
	if len(inv.Args) != 0 {
		return usage(DecisionUsage)
	}
	if opts.Close != "" {
		return closeDecision(inv, opts.Close)
	}
	home, token := adapterkit.HomeDir(inv.Environ), adapterkit.Getenv(inv.Environ, envMessagingToken)
	to, reason := doing.CleanText(opts.To, home, protocol.MaxHumanLabelChars, token)
	if reason == doing.ReasonEmpty {
		return &protocol.Error{Code: protocol.CodeInvalidInput, Message: DecisionNoTo, Details: map[string]string{"field": "to", "reason": ReasonToNeeded}}
	}
	if reason != "" {
		return textError("to", reason, protocol.MaxHumanLabelChars)
	}
	to = protocol.SanitizeLabel(to)
	options, err := decisionOptions(opts.Options, home, token)
	if err != nil {
		return err
	}
	def := ""
	if strings.TrimSpace(opts.Default) != "" {
		if def, _ = doing.CleanText(opts.Default, home, state.MaxOptionChars, token); def == "" || !containsString(options, def) {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--default must be one of the --options", Details: map[string]string{"field": "default", "reason": ReasonDefaultNotOption}}
		}
	}
	by := strings.TrimSpace(opts.By)
	if by != "" && !state.ValidDate(by) {
		return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--by must be a date, YYYY-MM-DD", Details: map[string]string{"field": "by", "reason": ReasonByNotADate}}
	}
	if opts.Card < 0 {
		return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--card must be a card number", Details: map[string]string{"field": "card", "reason": ReasonCardNegative}}
	}
	raw, err := inv.readLine("question", state.MaxQuestionChars)
	if err != nil {
		return err
	}
	question, reason := doing.CleanText(raw, home, state.MaxQuestionChars, token)
	if reason != "" {
		return textError("question", reason, state.MaxQuestionChars)
	}

	if !inv.inSession() {
		return notInSession("decision")
	}
	t, err := inv.resolveSession("")
	if err != nil {
		return err
	}
	m := t.session
	s, err := stateStoreOf(m, t.stateDir)
	if err != nil {
		return err
	}
	reports, err := state.ReadReports(m.StateFolder)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		inv.logger().Warn("decision: reports.json unreadable; the ask goes to the person's session", log.Err(err))
	}
	var sub *state.Subscriber
	if reports != nil {
		sub = reports.Subscriber(to)
	}
	now := inv.Deps.now()
	id, err := state.NewDecisionID(now)
	if err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "a decision id could not be made"}
	}
	d := &state.Decision{
		Version: state.Version, ID: id, Question: question, To: to, Options: options, Default: def, By: by, Card: opts.Card,
		SessionName: m.SessionName, SessionID: m.BrigadeSessionID, Repository: m.SyncScope,
		CreatedAt: now, UpdatedAt: now,
	}
	if d.Repository == "" {
		d.Repository = m.WorkspaceLabel
	}
	if e, lerr := s.Load(m.BrigadeSessionID); lerr == nil {
		d.Member = e.Member
	}
	if err := s.SaveDecision(d); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the decision could not be written to the state folder"}
	}

	sent, notSent := inv.sendDecision(t, sub, d)
	if sent != nil {
		d.Sent, d.SentTo, d.UpdatedAt = sent.How, sent.To, inv.Deps.now()
		if err := s.SaveDecision(d); err != nil {
			inv.logger().Warn("decision: the sent way was not recorded", log.Err(err))
		}
	}
	if inv.JSON {
		return writeJSON(inv.Out, decisionResult{SelfSessionID: m.BrigadeSessionID, Decision: d, Sent: sent, NotSent: notSent, Note: DecisionNote})
	}
	lines := []string{"recorded: decision " + d.ID}
	switch {
	case sent != nil && sent.How == state.SentSession:
		lines = append(lines, "sent: to "+d.To+" at their session "+shortSession(sent.SessionID)+" as a team message (not subscribed, or no gateway of their recipients on the roster), message "+idLine(sent.MessageID))
	case sent != nil:
		lines = append(lines, "sent: to "+d.To+" at "+sent.To+" through the "+sent.How+" gateway, message "+idLine(sent.MessageID))
	default:
		lines = append(lines, "not sent: "+notSent+DecisionNotSentSuffix)
	}
	return writeLines(inv.Out, lines...)
}

// decisionOptions parses `--options "a | b | c"`: split on `|`, each
// cleaned as a line, empty ones dropped, at most state.MaxOptions.
func decisionOptions(raw, home, token string) ([]string, error) {
	options := []string{}
	if strings.TrimSpace(raw) == "" {
		return options, nil
	}
	for part := range strings.SplitSeq(raw, "|") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		o, reason := doing.CleanText(part, home, state.MaxOptionChars, token)
		if reason != "" {
			return nil, textError("option", reason, state.MaxOptionChars)
		}
		options = append(options, o)
	}
	if len(options) > state.MaxOptions {
		return nil, &protocol.Error{Code: protocol.CodeInvalidInput, Message: "at most " + strconv.Itoa(state.MaxOptions) + " options can be offered", Details: map[string]string{"field": "options", "reason": ReasonTooManyOptions, "limit": strconv.Itoa(state.MaxOptions)}}
	}
	return options, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sendDecision sends the ask to the person it is for: the roster is read
// once; with a subscription, the first gateway of its recipients that is
// on the roster takes the labelled lines under a `to:` first line (the
// gateway's own convention); with none, their best session as a plain
// team message (resolveMember, the gateways left out; a label two
// members share is ambiguous and reaches nobody). It answers what was
// sent, or why nothing was — the roster unreadable, no gateway and no
// session of theirs, the send refused — in one sentence for the line. A
// failed send is never an error of the command: the decision is
// recorded, and the digest lists it.
func (inv Invocation) sendDecision(t *target, sub *state.Subscriber, d *state.Decision) (*decisionSent, string) {
	list, err := callWithin(RosterTimeout, func(ctx context.Context) (*adapterclient.ListResult, error) {
		return t.client.ListSessions(ctx, false)
	})
	if err != nil {
		inv.logger().Warn("decision: the roster could not be read", log.Err(err))
		return nil, "the roster could not be read (" + string(codeOf(err)) + ")"
	}
	body := strings.Join(d.Lines(), "\n")
	summary := protocol.TruncateRunes("Decision "+d.ID+": "+d.Question, protocol.MaxSummaryChars)
	var sent decisionSent
	var recipients []string
	if sub != nil {
		recipients = sub.Recipients
	}
	if gw, ok := state.PickGateway(recipients, list.Sessions); ok {
		sent = decisionSent{How: gw.Kind, To: gw.To, SessionID: gw.SessionID}
		body = "to: " + gw.To + "\n\n" + askPreamble(t, d.To) + body
	} else {
		pick, merr := resolveMember(d.To, list.Sessions)
		if merr != nil {
			inv.logger().Info("decision: no gateway of the recipients on the roster and no session of the person", log.Err(merr))
			why := "no gateway of their recipients is on the roster, and they have no session that can receive a message"
			if isReason(merr, "ambiguous") {
				why = "the label names more than one member, and nobody was picked"
			}
			return nil, why
		}
		sent = decisionSent{How: state.SentSession, To: pick.session.SessionID, SessionID: pick.session.SessionID}
		body = askPreamble(t, d.To) + body
	}
	req := &protocol.SendRequest{
		SenderSessionID: t.selfSessionID(), RecipientSessionID: sent.SessionID, Body: body, Summary: summary,
		IdempotencyKey: IdempotencyKey(t.selfSessionID(), sent.SessionID, body, inv.Deps.now()),
	}
	resp, err := call(func(ctx context.Context) (*protocol.SendResponse, error) { return t.client.Send(ctx, req) })
	if err != nil && isCode(err, protocol.CodeUnavailable) && !isReason(err, "timeout") {
		inv.Deps.sleep(RetryPause)
		resp, err = call(func(ctx context.Context) (*protocol.SendResponse, error) { return t.client.Send(ctx, req) })
	}
	if err != nil {
		inv.logger().Warn("decision: the ask was not sent", log.Err(err), slog.String("how", sent.How))
		return nil, "the send failed (" + string(codeOf(err)) + ")"
	}
	sent.MessageID = sanitizeID(resp.MessageID)
	return &sent, ""
}

// askPreamble is the line before a decision's lines in the ask: whom it
// is for, the team, and what they may do with it.
func askPreamble(t *target, to string) string {
	team := ""
	if t.session != nil {
		team = nameLine(t.session.TeamName)
	}
	return "Brigade decision for " + oneLine(to) + " from team " + team + ". Answer however you wish; the session acts on your answer and closes the decision.\n"
}

// codeOf is the protocol code of an error, `internal` for any other.
func codeOf(err error) protocol.Code {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return protocol.CodeInternal
}

// closeDecision is `brigade decision --close <id>`: the outcome on stdin,
// the decision read from the canonical copy — the asking session's — or
// the folder — another session of the member's — closed by the asking
// session or another of its member's, never by someone else.
func closeDecision(inv Invocation, id string) error {
	if err := state.CheckID(id); err != nil {
		return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--close takes a decision id as brigade decision printed it", Details: map[string]string{"field": "close", "reason": "invalid_id"}}
	}
	raw, err := inv.readLine("outcome", state.MaxOutcomeChars)
	if err != nil {
		return err
	}
	outcome, reason := doing.CleanText(raw, adapterkit.HomeDir(inv.Environ), state.MaxOutcomeChars, adapterkit.Getenv(inv.Environ, envMessagingToken))
	if reason != "" {
		return textError("outcome", reason, state.MaxOutcomeChars)
	}
	if !inv.inSession() {
		return notInSession("decision")
	}
	m, stateDir, err := inv.sessionMap()
	if err != nil {
		return err
	}
	s, err := stateStoreOf(m, stateDir)
	if err != nil {
		return err
	}
	d, err := s.LoadDecision(id)
	if errors.Is(err, fs.ErrNotExist) {
		d, err = state.ReadDecision(m.StateFolder, id)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "no decision has that id in the project's state folder", Details: map[string]string{"field": "close", "reason": "no_match"}}
		}
		return err
	}
	if !d.Open() {
		return &protocol.Error{Code: protocol.CodeConflict, Message: "decision " + d.ID + " is already closed", Details: map[string]string{"reason": ReasonAlreadyClosed}}
	}
	member := ""
	if e, lerr := s.Load(m.BrigadeSessionID); lerr == nil {
		member = e.Member
	}
	if d.SessionID != m.BrigadeSessionID && (d.Member == "" || member == "" || !strings.EqualFold(d.Member, member)) {
		return &protocol.Error{Code: protocol.CodeUnauthorized, Message: "decision " + d.ID + " was asked by another member; only the asking session, or another session of that member, closes it", Details: map[string]string{"reason": ReasonNotTheAsker}}
	}
	now := inv.Deps.now()
	d.ClosedAt, d.Outcome, d.UpdatedAt = now, outcome, now
	d.ClosedBy = &state.Closer{Member: member, SessionName: m.SessionName, SessionID: m.BrigadeSessionID}
	if err := s.SaveDecision(d); err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the decision could not be written to the state folder"}
	}
	inv.logger().Debug("decision closed")
	if inv.JSON {
		return writeJSON(inv.Out, decisionResult{SelfSessionID: m.BrigadeSessionID, Decision: d, Note: DecisionClosedNote})
	}
	return writeLines(inv.Out, "closed: decision "+d.ID+" ("+outcome+")")
}
