package webhook

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// Inbound paths.
const (
	PathMessages = "/v1/messages"
	PathSessions = "/v1/sessions"
	PathHealth   = "/healthz"
)

const (
	inboundTimeout = 30 * time.Second
	// DefaultInboundPerMinute bounds accepted POSTs, all callers together.
	DefaultInboundPerMinute = 60
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiResponse struct {
	OK                 bool         `json:"ok"`
	MessageID          string       `json:"message_id,omitzero"`
	RecipientSessionID string       `json:"recipient_session_id,omitzero"`
	Duplicate          bool         `json:"duplicate,omitzero"`
	HopCount           int          `json:"hop_count,omitzero"`
	Sessions           []rosterLine `json:"sessions,omitzero"`
	Error              *apiError    `json:"error,omitzero"`
}

// rosterLine is what GET /v1/sessions shows for one session: enough to
// address it, nothing more. Names and labels are unverified.
type rosterLine struct {
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	HumanLabel  string `json:"human_label,omitzero"`
	State       string `json:"state"`
	Activity    string `json:"activity"`
	Harness     string `json:"harness,omitzero"`
}

// Handler is the inbound HTTP API. perMinute <= 0 means the default.
func (g *Gateway) Handler(perMinute int) http.Handler {
	if perMinute <= 0 {
		perMinute = DefaultInboundPerMinute
	}
	lim := &limiter{per: time.Minute / time.Duration(perMinute), burst: perMinute, now: g.cfg.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		if g.SessionID() == "" {
			writeJSON(w, http.StatusServiceUnavailable, &apiResponse{Error: &apiError{Code: "unavailable", Message: "not registered yet"}})
			return
		}
		writeJSON(w, http.StatusOK, &apiResponse{OK: true})
	})
	mux.HandleFunc("POST "+PathMessages, func(w http.ResponseWriter, r *http.Request) {
		body, ok := g.readSigned(w, r, true)
		if !ok {
			return
		}
		if !lim.allow() {
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusTooManyRequests, &apiResponse{Error: &apiError{Code: "rate_limited", Message: "too many requests"}})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inboundTimeout)
		defer cancel()
		status, resp := g.handleSend(ctx, body, r.Header.Get(HeaderSignature))
		writeJSON(w, status, resp)
	})
	mux.HandleFunc("GET "+PathSessions, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.readSigned(w, r, false); !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inboundTimeout)
		defer cancel()
		status, resp := g.handleRoster(ctx)
		writeJSON(w, status, resp)
	})
	return mux
}

// readSigned reads at most MaxInboundBytes and verifies the signature
// before anything else looks at the body. It writes the refusal itself.
func (g *Gateway) readSigned(w http.ResponseWriter, r *http.Request, wantJSON bool) ([]byte, bool) {
	if wantJSON {
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, &apiResponse{Error: &apiError{Code: "invalid_input", Message: "Content-Type must be application/json"}})
			return nil, false
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxInboundBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, &apiResponse{Error: &apiError{Code: "invalid_input", Message: "request body too large"}})
		} else {
			writeJSON(w, http.StatusBadRequest, &apiResponse{Error: &apiError{Code: "invalid_input", Message: "request body unreadable"}})
		}
		return nil, false
	}
	if err := Verify(g.cfg.Secret, r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderSignature), body, g.cfg.Now(), g.cfg.MaxSkew); err != nil {
		g.log.Warn("inbound request refused", slog.String("reason", err.Error()))
		writeJSON(w, http.StatusUnauthorized, &apiResponse{Error: &apiError{Code: "unauthenticated", Message: err.Error()}})
		return nil, false
	}
	if g.SessionID() == "" {
		writeJSON(w, http.StatusServiceUnavailable, &apiResponse{Error: &apiError{Code: "unavailable", Message: "the gateway is not registered yet"}})
		return nil, false
	}
	return body, true
}

func (g *Gateway) handleSend(ctx context.Context, body []byte, signature string) (int, *apiResponse) {
	var in InboundRequest
	if err := json.Unmarshal(body, &in, json.RejectUnknownMembers(true)); err != nil {
		return fail(http.StatusBadRequest, "invalid_input", "the body is not a valid request document (unknown members are refused)")
	}
	if err := in.Validate(); err != nil {
		return fail(http.StatusBadRequest, "invalid_input", err.Error())
	}
	recipient, replyTo, status, resp := g.route(ctx, &in)
	if resp != nil {
		return status, resp
	}
	text := frameInbound(&in)
	if len(text) > protocol.MaxBodyBytes {
		return fail(http.StatusRequestEntityTooLarge, "invalid_input", "text is too long for one Brigade message")
	}
	key := "webhook:sig:" + signatureKey(signature)
	if in.ID != "" {
		key = "webhook:id:" + in.ID
	}
	self := g.SessionID()
	req := &protocol.SendRequest{
		SenderSessionID:    self,
		RecipientSessionID: recipient,
		Body:               text,
		Summary:            protocol.SanitizeSummary(in.Summary),
		ReplyTo:            replyTo,
		IdempotencyKey:     key,
	}
	res, err := g.be.Send(ctx, req)
	var pe *protocol.Error
	if req.ReplyTo != "" && errors.As(err, &pe) && pe.Code == protocol.CodeNotFound {
		// The answered message aged out of the backend: send unthreaded.
		req.ReplyTo, req.IdempotencyKey = "", key+":unthreaded"
		res, err = g.be.Send(ctx, req)
	}
	if err != nil {
		return backendFailure(err)
	}
	if rerr := g.st.remember(res.MessageID, threadEntry{
		Direction: dirIn, Peer: res.RecipientSessionID, Ref: in.Ref, At: g.cfg.Now().UTC(),
	}); rerr != nil {
		g.log.Warn("gateway state not saved", slog.String("error", rerr.Error()))
	}
	return http.StatusAccepted, &apiResponse{
		OK: true, MessageID: res.MessageID, RecipientSessionID: res.RecipientSessionID,
		Duplicate: res.Duplicate, HopCount: res.HopCount,
	}
}

// route decides the recipient and the reply_to to send with.
func (g *Gateway) route(ctx context.Context, in *InboundRequest) (recipient, replyTo string, status int, resp *apiResponse) {
	replyTo = in.ReplyTo
	if in.To == "" && in.Member == "" {
		e, ok := g.st.thread(in.ReplyTo)
		if !ok {
			status, resp = fail(http.StatusNotFound, "not_found", "reply_to is not a message this gateway carried; give to or member")
			return "", "", status, resp
		}
		return e.Peer, replyTo, 0, nil
	}
	list, err := g.be.ListSessions(ctx, true)
	if err != nil {
		status, resp = backendFailure(err)
		return "", "", status, resp
	}
	var rerr error
	if in.To != "" {
		recipient, rerr = resolveSession(in.To, list.Sessions)
	} else {
		recipient, rerr = resolveMember(in.Member, list.Sessions)
	}
	var re *resolveError
	if errors.As(rerr, &re) {
		code, st := "invalid_input", http.StatusBadRequest
		if re.notFound {
			code, st = "not_found", http.StatusNotFound
		}
		status, resp = fail(st, code, re.msg)
		return "", "", status, resp
	}
	if recipient == g.SessionID() {
		status, resp = fail(http.StatusBadRequest, "invalid_input", "the gateway does not message itself")
		return "", "", status, resp
	}
	return recipient, replyTo, 0, nil
}

func (g *Gateway) handleRoster(ctx context.Context) (int, *apiResponse) {
	list, err := g.be.ListSessions(ctx, false)
	if err != nil {
		return backendFailure(err)
	}
	out := &apiResponse{OK: true, Sessions: []rosterLine{}}
	for i := range list.Sessions {
		s := &list.Sessions[i]
		if IsGatewayHarness(s.Harness) || s.Inbound == protocol.InboundRefuse {
			continue
		}
		out.Sessions = append(out.Sessions, rosterLine{
			SessionID: s.SessionID, SessionName: protocol.SanitizeName(s.SessionName),
			HumanLabel: protocol.SanitizeLabel(s.HumanLabel), State: s.State, Activity: s.Activity, Harness: s.Harness,
		})
	}
	return http.StatusOK, out
}

// signatureKey shortens a verified signature header to an idempotency key
// suffix: the first 40 hex digits identify the (timestamp, body) pair.
func signatureKey(sig string) string {
	hex := sig[len(signatureScheme)+1:]
	return hex[:min(40, len(hex))]
}

func backendFailure(err error) (int, *apiResponse) {
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		return fail(http.StatusBadGateway, "unavailable", "the Brigade adapter failed")
	}
	switch pe.Code {
	case protocol.CodeInvalidInput:
		return fail(http.StatusBadRequest, string(pe.Code), pe.Message)
	case protocol.CodeNotFound:
		return fail(http.StatusNotFound, string(pe.Code), pe.Message)
	case protocol.CodeConflict, protocol.CodeLoopDetected:
		return fail(http.StatusConflict, string(pe.Code), pe.Message)
	case protocol.CodeRateLimited:
		return fail(http.StatusTooManyRequests, string(pe.Code), pe.Message)
	case protocol.CodeUnavailable:
		return fail(http.StatusServiceUnavailable, string(pe.Code), pe.Message)
	case protocol.CodeInternal, protocol.CodeUsage, protocol.CodeUnauthenticated, protocol.CodeUnauthorized,
		protocol.CodeProtocolMismatch, protocol.CodeConfig:
		return fail(http.StatusBadGateway, string(pe.Code), "the Brigade backend refused the gateway")
	default:
		return fail(http.StatusBadGateway, "internal", "the Brigade backend refused the gateway")
	}
}

func fail(status int, code, msg string) (int, *apiResponse) {
	return status, &apiResponse{Error: &apiError{Code: code, Message: msg}}
}

func writeJSON(w http.ResponseWriter, status int, v *apiResponse) {
	data, err := json.Marshal(v)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"ok":false}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	//nolint:gosec // G705: JSON from json.Marshal, served as application/json with nosniff to a loopback client
	_, _ = w.Write(append(data, '\n'))
}

// limiter is a token bucket shared by every caller.
type limiter struct {
	mu     sync.Mutex
	per    time.Duration
	burst  int
	tokens float64
	last   time.Time
	now    func() time.Time
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.last.IsZero() {
		l.tokens = float64(l.burst)
	} else {
		l.tokens = min(float64(l.burst), l.tokens+float64(now.Sub(l.last))/float64(l.per))
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
