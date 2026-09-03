package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

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
// harness members.
type sendResult struct {
	protocol.SendResponse
	SelfSessionID string `json:"self_session_id"`
	Note          string `json:"note"`
}

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
// names its retry-after. The confirmation names the recipient by id: the
// SendResponse carries no name, and a second spawn to fetch one is not
// worth the round trip.
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
		return withRetryAfter(err)
	}

	if inv.JSON {
		out := sendResult{SendResponse: *resp, SelfSessionID: self, Note: AcceptedNote}
		out.MessageID = sanitizeID(out.MessageID)
		out.RecipientSessionID = sanitizeID(out.RecipientSessionID)
		return writeJSON(inv.Out, out)
	}
	line := "accepted: message " + idLine(resp.MessageID) + " to " + idLine(resp.RecipientSessionID)
	if resp.Duplicate {
		line += ", duplicate of an earlier send"
	}
	line += ". " + AcceptedNote
	return writeLines(inv.Out, line)
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
