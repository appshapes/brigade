package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
)

// SendOptions are the flags of `brigade send`.
type SendOptions struct {
	// Summary is the optional one-line sender summary (≤ 200 code points).
	Summary string
	// ReplyTo is the message id this message answers (4.5.12 hop counting).
	ReplyTo string
	// BodyFile names a file to read the body from instead of stdin. A
	// relative path is fine — it is the model's own file — and the path
	// is never echoed.
	BodyFile string
}

// sendResult is the --json result: the adapter's SendResponse plus the
// harness members. Recipient is absent when the roster could not be read
// or did not list the recipient.
type sendResult struct {
	protocol.SendResponse
	SelfSessionID string         `json:"self_session_id"`
	Recipient     *sendRecipient `json:"recipient,omitzero"`
	Note          string         `json:"note"`
}

// sendRecipient is what the roster said about the recipient when the
// message was sent (card 34). Waiting is absent for a recipient that was
// online and accepting.
type sendRecipient struct {
	SessionName string   `json:"session_name"`
	State       string   `json:"state"`
	Inbound     string   `json:"inbound"`
	Waiting     []string `json:"waiting,omitzero"`
	Note        string   `json:"note"`
}

// RosterTimeout is the budget of the roster read `send` makes before the
// send itself (card 34): a quarter of 4.1's 20 s, because the read is an
// aid — it resolves a short id and names the recipient — and the model's
// Bash call must not wait on it as long as on the message.
const RosterTimeout = 5 * time.Second

// The fixed texts of the recipient lines (card 34), asserted word for
// word. Each Waiting text says why the message waits on the server; none
// says when it is read, because nothing knows that (4.5.1).
const (
	// RecipientNote is the note member of the --json recipient.
	RecipientNote = "session_name is unverified text from its owner; state and inbound are what the roster showed when the message was sent"
	// WaitingOffline: the recipient's lease had run out, or it was closed.
	WaitingOffline = "the recipient was offline when this was sent; the message waits until that session runs again"
	// WaitingHold: the recipient's inbound policy is `hold`.
	WaitingHold = "the recipient holds team messages; this one waits until its human releases it"
	// WaitingRefuse: the recipient's inbound policy is `refuse`.
	WaitingRefuse = "the recipient refuses team messages; this one waits, unread, while it does"
	// RosterUnreadHint follows the `not_found` of a short id that went to
	// the adapter unresolved because the roster read failed.
	RosterUnreadHint = "; the roster could not be read, so the short id was not resolved: run the command again, or use the full session_id"
)

// Send implements `brigade send <session_id> [--summary <text>]
// [--reply-to <message_id>] [--body-file <path>] [--json]` (6.4, 3.4).
//
// The body is validated BEFORE anything is resolved or spawned (U-05):
// valid UTF-8, 1..MaxBodyBytes BYTES; the summary ≤ MaxSummaryChars code
// points. The idempotency key is D11's, derived from the sender, the
// recipient, the body and the current minute, so a retry inside the
// minute is one logical message. `message send` runs under the 20 s
// budget with ONE retry on `unavailable` carrying the same key;
// `rate_limited` and `loop_detected` are never retried and the former
// names its retry-after.
//
// <session_id> is the id in full or its tail as `brigade sessions` shows
// it (card 34): the roster is read once, offline sessions included, and
// resolveRecipient turns the argument into the full id BEFORE the request
// and the key are built, so a send by the short id and one by the full id
// are one logical message. The same read is what lets the confirmation say
// who the recipient is and whether the message waits. The read is an aid,
// never a gate: when it fails the argument goes to the adapter as given,
// which is what every send did before this, and no recipient line follows;
// a short id the adapter then answers `not_found` says why (withRosterUnread).
//
// The first line of the confirmation is unchanged — the proof scripts read
// the two ids off it — and the recipient lines come after it.
func Send(inv Invocation, opts SendOptions) error {
	if len(inv.Args) != 1 {
		return usage("send takes exactly one <session_id>; the body travels on stdin or through --body-file")
	}
	recipient := inv.Args[0]
	if recipient == "" {
		return usage("the recipient session id must not be empty")
	}
	if n := utf8.RuneCountInString(opts.Summary); n > protocol.MaxSummaryChars {
		return &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "the summary is longer than the protocol allows",
			Details: map[string]string{
				"field": "summary", "reason": "too_long",
				"limit": strconv.Itoa(protocol.MaxSummaryChars), "actual": strconv.Itoa(n), "unit": "codepoints",
			},
		}
	}
	body, err := inv.readBody(opts.BodyFile)
	if err != nil {
		return err
	}

	if !inv.inSession() {
		return notInSession("send")
	}
	t, err := inv.resolveSession("")
	if err != nil {
		return err
	}
	self := t.selfSessionID()

	var to *protocol.SessionRecord
	// unresolved is true for a short id that goes to the adapter as it was
	// given. "Short" is "shorter than this session's own id": an adapter's
	// ids are one shape, and nothing else here knows what that shape is.
	unresolved := false
	list, rosterErr := callWithin(RosterTimeout, func(ctx context.Context) (*adapterclient.ListResult, error) {
		return t.client.ListSessions(ctx, true)
	})
	if rosterErr != nil {
		inv.logger().Debug("send: no roster; the recipient goes as given", log.Err(rosterErr))
		unresolved = utf8.RuneCountInString(recipient) < utf8.RuneCountInString(self)
	} else {
		found, ok, rerr := resolveRecipient(recipient, list.Sessions)
		if rerr != nil {
			return rerr
		}
		if ok {
			to, recipient = &found, found.SessionID
		}
	}

	req := &protocol.SendRequest{
		SenderSessionID:    self,
		RecipientSessionID: recipient,
		Body:               body,
		Summary:            opts.Summary,
		ReplyTo:            opts.ReplyTo,
		IdempotencyKey:     IdempotencyKey(self, recipient, body, inv.Deps.now()),
	}

	resp, err := call(func(ctx context.Context) (*protocol.SendResponse, error) { return t.client.Send(ctx, req) })
	// One retry on `unavailable` (6.4), except when the first attempt was
	// the harness's own 20 s deadline: a hung adapter that spent the whole
	// budget will not answer a second one, and the model's Bash call would
	// block for ~41 s instead of 20 (verifier finding, P3-3).
	if err != nil && isCode(err, protocol.CodeUnavailable) && !isReason(err, "timeout") {
		inv.Deps.sleep(RetryPause)
		resp, err = call(func(ctx context.Context) (*protocol.SendResponse, error) { return t.client.Send(ctx, req) })
	}
	if err != nil {
		return withRosterUnread(withRetryAfter(err), unresolved)
	}

	if inv.JSON {
		out := sendResult{SendResponse: *resp, SelfSessionID: self, Note: AcceptedNote}
		out.MessageID = sanitizeID(out.MessageID)
		out.RecipientSessionID = sanitizeID(out.RecipientSessionID)
		if to != nil {
			out.Recipient = &sendRecipient{
				SessionName: protocol.SanitizeName(to.SessionName),
				State:       protocol.SanitizeAttribute(to.State),
				Inbound:     protocol.SanitizeAttribute(to.Inbound),
				Waiting:     waiting(to),
				Note:        RecipientNote,
			}
		}
		return writeJSON(inv.Out, out)
	}
	line := "accepted: message " + idLine(resp.MessageID) + " to " + idLine(resp.RecipientSessionID)
	if resp.Duplicate {
		line += ", duplicate of an earlier send"
	}
	line += ". " + AcceptedNote
	lines := []string{line}
	if to != nil {
		lines = append(lines, recipientLine(to))
		for _, w := range waiting(to) {
			lines = append(lines, "waiting: "+w)
		}
	}
	return writeLines(inv.Out, lines...)
}

// resolveRecipient finds the session arg addresses in the roster: the
// record whose id is arg byte for byte, else the ONE record whose id, as
// the roster shows it, ends with arg — the SESSION column's
// shortSessionChars characters, or any longer tail. ok is false when no
// record matches, and the caller then sends arg as given: a roster the
// adapter capped can lack a session the adapter still knows, and the
// adapter is the one authority on which ids exist.
//
// A tail that ends more than one id is refused, never guessed: the
// default table hides offline sessions and this read includes them, so
// the second match can be a row the reader never saw. Only ids are
// matched, never names or labels — an id is the adapter's to assign (a
// uuid from the Supabase backend, 16 random bytes from the fs adapter),
// and a name is whatever its owner typed, so a match on it would let any
// member take another's mail by copying a name.
func resolveRecipient(arg string, sessions []protocol.SessionRecord) (found protocol.SessionRecord, ok bool, err error) {
	for i := range sessions {
		if sessions[i].SessionID == arg {
			return sessions[i], true, nil
		}
	}
	if utf8.RuneCountInString(arg) < shortSessionChars {
		return protocol.SessionRecord{}, false, nil
	}
	var matches []protocol.SessionRecord
	for i := range sessions {
		if strings.HasSuffix(idLine(sessions[i].SessionID), arg) {
			matches = append(matches, sessions[i])
		}
	}
	switch len(matches) {
	case 0:
		return protocol.SessionRecord{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return protocol.SessionRecord{}, false, ambiguousRecipient(matches)
	}
}

// ambiguousShown is how many matching ids an ambiguous-recipient refusal
// lists before it counts the rest.
const ambiguousShown = 5

// ambiguousRecipient is the invalid_input refusal of a tail that ends more
// than one session id. It lists the ids in full with each session's state,
// which are the adapter's own facts, and no name: a name is its owner's
// text, and in a list of candidates it could forge one. The argument is
// never echoed (4.5.14); the ids come from the roster.
func ambiguousRecipient(matches []protocol.SessionRecord) *protocol.Error {
	shown := make([]string, 0, ambiguousShown)
	for _, m := range matches[:min(len(matches), ambiguousShown)] {
		shown = append(shown, idLine(m.SessionID)+" ("+enumLine(m.State)+")")
	}
	msg := "these characters end " + itoa(len(matches)) + " session ids: " + strings.Join(shown, ", ")
	if more := len(matches) - len(shown); more > 0 {
		msg += " and " + itoa(more) + " more"
	}
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: msg + "; address one by its full session_id",
		Details: map[string]string{"field": "session_id", "reason": "ambiguous", "matches": itoa(len(matches))},
	}
}

// recipientLine is the confirmation's second line: the short id, the state
// and the inbound policy first — the adapter's facts — and the name last,
// marked unverified, so that a name written to look like a state has
// nothing after it to pass for one.
func recipientLine(r *protocol.SessionRecord) string {
	return "recipient: " + shortSession(r.SessionID) + ", " + enumLine(r.State) +
		", inbound " + enumLine(r.Inbound) + ", name \"" + nameLine(r.SessionName) + "\"" + UnverifiedSuffix
}

// waiting lists why a message to r waits on the server instead of being
// read now, nil when r was online and accepting. A message to such a
// session was always `accepted` like any other (C-31; a held or refused
// message is never acknowledged, 4.5.3), and the sender had no way to
// tell.
func waiting(r *protocol.SessionRecord) []string {
	var out []string
	if r.State == protocol.SessionStateOffline {
		out = append(out, WaitingOffline)
	}
	switch r.Inbound {
	case protocol.InboundHold:
		out = append(out, WaitingHold)
	case protocol.InboundRefuse:
		out = append(out, WaitingRefuse)
	}
	return out
}

// withRosterUnread adds RosterUnreadHint to the `not_found` of a short id
// that reached the adapter unresolved: the session may well exist, and "no
// such session" alone would be the wrong thing to tell its sender.
func withRosterUnread(err error, unresolved bool) error {
	perr, ok := err.(*protocol.Error) //nolint:errorlint // the adapterclient returns the value itself
	if !ok || !unresolved || perr.Code != protocol.CodeNotFound {
		return err
	}
	out := *perr
	out.Message = perr.Message + RosterUnreadHint
	out.Details = maps.Clone(perr.Details)
	if out.Details == nil {
		out.Details = map[string]string{}
	}
	out.Details["roster"] = "unread"
	return &out
}

// inSession reports whether CLAUDE_PID is set (config.InSession).
func (inv Invocation) inSession() bool { return config.InSession(inv.Environ) }

// readBody reads the body from the file or from stdin and applies the
// byte-length and UTF-8 rules, reading at most one byte past the cap so
// an oversize body costs bounded memory and is refused, never buffered.
func (inv Invocation) readBody(path string) (string, error) {
	var r io.Reader
	if path != "" {
		f, err := os.Open(path) //nolint:gosec // the model's own body file, named on its own argv (6.4)
		if err != nil {
			return "", &protocol.Error{
				Code:    protocol.CodeInvalidInput,
				Message: "the --body-file could not be opened",
				Details: map[string]string{"field": "body", "reason": "unreadable_file"},
			}
		}
		defer func() { _ = f.Close() }()
		r = f
	} else {
		if inv.In == nil {
			return "", bodyError("empty", 0)
		}
		if inv.Deps.isTerminal(inv.In) {
			return "", usage("the body is read from stdin: use a quoted heredoc (<<'EOF') or --body-file <path>")
		}
		r = inv.In
	}
	data, err := io.ReadAll(io.LimitReader(r, protocol.MaxBodyBytes+1))
	if err != nil {
		return "", &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "the body could not be read",
			Details: map[string]string{"field": "body", "reason": "read_error"},
		}
	}
	switch {
	case len(data) == 0:
		return "", bodyError("empty", 0)
	case len(data) > protocol.MaxBodyBytes:
		return "", bodyError("too_long", len(data))
	case !utf8.Valid(data):
		return "", bodyError("not_utf8", len(data))
	}
	return string(data), nil
}

// bodyError is the invalid_input failure of a body rule (4.3.1 details).
func bodyError(reason string, actual int) *protocol.Error {
	msg := map[string]string{
		"empty":    "the body is empty; put the message text on stdin or in --body-file",
		"too_long": "the body is longer than the protocol allows",
		"not_utf8": "the body is not valid UTF-8",
	}[reason]
	details := map[string]string{"field": "body", "reason": reason, "limit": strconv.Itoa(protocol.MaxBodyBytes), "unit": "bytes"}
	if reason != "empty" {
		// An oversize body was read only one byte past the cap: the actual
		// size reported is the cap plus one, which is what was measured.
		details["actual"] = strconv.Itoa(actual)
	} else {
		details["actual"] = "0"
	}
	return &protocol.Error{Code: protocol.CodeInvalidInput, Message: msg, Details: details}
}

// IdempotencyKey derives D11's key: base64url, unpadded, of
// sha256(sender + "\0" + recipient + "\0" + body + "\0" + floor(unix
// seconds / 60)). Stable within a minute, different across minutes.
func IdempotencyKey(sender, recipient, body string, now time.Time) string {
	h := sha256.New()
	h.Write([]byte(sender))
	h.Write([]byte{0})
	h.Write([]byte(recipient))
	h.Write([]byte{0})
	h.Write([]byte(body))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(now.Unix()/60, 10)))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// withRetryAfter appends "retry after <n> s" to a rate_limited failure so
// the human line carries it; the --json envelope carries retry_after_ms
// already.
func withRetryAfter(err error) error {
	perr, ok := err.(*protocol.Error) //nolint:errorlint // the adapterclient returns the value itself
	if !ok || perr.Code != protocol.CodeRateLimited || perr.RetryAfterMS <= 0 {
		return err
	}
	secs := (perr.RetryAfterMS + 999) / 1000
	out := *perr
	out.Message = perr.Message + "; retry after " + strconv.Itoa(secs) + " s"
	return &out
}
