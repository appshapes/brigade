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
	// Options are the answers offered, separated by `|`; Default the one
	// taken when the manager says nothing; By the date an answer is
	// needed by (YYYY-MM-DD); Card the card the question belongs to.
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
	DecisionUsage = "decision takes no arguments: the question travels on stdin in a quoted heredoc (<<'EOF'); --options, --default, --by and --card describe it, --close <id> closes one with the outcome on stdin"
	// DecisionNote is the note of a --json result for an ask;
	// DecisionClosedNote for a close.
	DecisionNote       = "recorded in the project's state folder and sent to the manager through the first gateway of reports.json's recipients that is on the roster; the manager answers however they wish, and brigade decision --close records the outcome"
	DecisionClosedNote = "closed in the project's state folder; the next digest lists it under the decisions closed"
	// DecisionNoManager is the `config` message of a project whose
	// reports.json names no manager.
	DecisionNoManager = "no manager is configured for this project: brigade report configure writes reports.json in the state folder"
	ReasonNoManager   = "no_manager"
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

// Decision implements `brigade decision [--options <a | b>] [--default
// <option>] [--by <date>] [--card <n>] [--json]` and `brigade decision
// --close <id> [--json]` (card 162, plan §5.2). An ask writes
// `decisions/<id>.json` — the canonical copy under the state directory,
// the projection in the folder — and sends it at once, in the labelled
// shape the digest uses, through the first gateway of reports.json's
// recipients that is on the roster (Slack before email when the file
// lists them so); with no gateway there, to the manager's own best
// session; with neither, it is recorded and the line says so, and the
// next digest carries it. A close records the outcome and who closed it:
// the asking session, or another session of the member who asked. Every
// text is cleaned as a doing line is, before anything is resolved.
func Decision(inv Invocation, opts DecisionOptions) error {
	if len(inv.Args) != 0 {
		return usage(DecisionUsage)
	}
	if opts.Close != "" {
		return closeDecision(inv, opts.Close)
	}
	home, token := adapterkit.HomeDir(inv.Environ), adapterkit.Getenv(inv.Environ, envMessagingToken)
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
		inv.logger().Warn("decision: reports.json unreadable", log.Err(err))
	}
	if reports == nil || reports.Manager == "" {
		return &protocol.Error{Code: protocol.CodeConfig, Message: DecisionNoManager, Details: map[string]string{"reason": ReasonNoManager}}
	}
	now := inv.Deps.now()
	id, err := state.NewDecisionID(now)
	if err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "a decision id could not be made"}
	}
	d := &state.Decision{
		Version: state.Version, ID: id, Question: question, Options: options, Default: def, By: by, Card: opts.Card,
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

	sent, notSent := inv.sendDecision(t, reports, d)
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
		lines = append(lines, "sent: to the manager's session "+shortSession(sent.SessionID)+" as a team message (no gateway on the roster), message "+idLine(sent.MessageID))
	case sent != nil:
		lines = append(lines, "sent: to "+sent.To+" through the "+sent.How+" gateway, message "+idLine(sent.MessageID))
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

// sendDecision sends the ask: the roster is read once; the first
// gateway of the recipients that is on it takes the labelled lines under
// a `to:` first line (the gateway's own convention); with none, the
// manager's best session as a plain team message (resolveMember, the
// gateways left out). It answers what was sent, or why nothing was —
// the roster unreadable, no gateway and no session of the manager, the
// send refused — in one sentence for the line. A failed send is never an
// error of the command: the decision is recorded, and the digest lists it.
func (inv Invocation) sendDecision(t *target, reports *state.Reports, d *state.Decision) (*decisionSent, string) {
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
	if gw, ok := state.PickGateway(reports.Recipients, list.Sessions); ok {
		sent = decisionSent{How: gw.Kind, To: gw.To, SessionID: gw.SessionID}
		body = "to: " + gw.To + "\n\n" + askPreamble(t, reports) + body
	} else {
		pick, merr := resolveMember(reports.Manager, list.Sessions)
		if merr != nil {
			inv.logger().Info("decision: no gateway on the roster and no session of the manager", log.Err(merr))
			return nil, "no gateway of the recipients is on the roster, and the manager has no session that can receive a message"
		}
		sent = decisionSent{How: state.SentSession, To: pick.session.SessionID, SessionID: pick.session.SessionID}
		body = askPreamble(t, reports) + body
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

// askPreamble is the line before a decision's lines in the ask: the team,
// and what the manager may do with it.
func askPreamble(t *target, reports *state.Reports) string {
	team := ""
	if t.session != nil {
		team = nameLine(t.session.TeamName)
	}
	return "Brigade decision for " + oneLine(reports.Manager) + " from team " + team + ". Answer however you wish; the session acts on your answer and closes the decision.\n"
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
