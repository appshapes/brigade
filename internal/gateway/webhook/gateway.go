package webhook

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/protocol"
)

// Backend is the slice of BAP/1 the gateway uses. *adapterclient.Client
// implements it for any adapter; tests may substitute a fake.
type Backend interface {
	Describe(ctx context.Context) (*protocol.DescribeResult, error)
	Register(ctx context.Context, reg *protocol.SessionRegistration) (*adapterclient.RegisterResult, error)
	Heartbeat(ctx context.Context, sessionID string, hb *protocol.HeartbeatRequest) (*protocol.HeartbeatResult, error)
	Close(ctx context.Context, sessionID string) (*adapterclient.CloseResult, error)
	ListSessions(ctx context.Context, includeOffline bool) (*adapterclient.ListResult, error)
	Send(ctx context.Context, req *protocol.SendRequest) (*protocol.SendResponse, error)
	Receive(ctx context.Context, sessionID string, limit int) (*adapterclient.ReceiveResult, error)
	Ack(ctx context.Context, sessionID string, req *protocol.AckRequest) (*protocol.AckResult, error)
}

var _ Backend = (*adapterclient.Client)(nil)

// Defaults.
const (
	DefaultSessionName  = "webhook-gateway"
	DefaultLeaseSeconds = 600
	DefaultPoll         = 5 * time.Second
	DefaultMaxAttempts  = 24
	receiveLimit        = 50
	postTimeout         = 15 * time.Second
	maxResponseDrain    = 64 << 10
	backoffBase         = 5 * time.Second
	backoffCap          = 5 * time.Minute
)

// Config configures a Gateway. TargetURL and Secret are required.
type Config struct {
	TargetURL    string
	Secret       []byte
	SessionName  string
	Description  string
	Version      string
	LeaseSeconds int
	Poll         time.Duration
	MaxAttempts  int
	MaxSkew      time.Duration
	// ExtraHeaders are set on every POST to TargetURL before the
	// gateway's own headers (a receiver's credential, from
	// --header-file). They authenticate nothing to Brigade; the
	// signature still does. The names parseHeaderFile refuses are
	// refused here too.
	ExtraHeaders http.Header
	// StatePath is the local state file; "" keeps state in memory only.
	StatePath  string
	HTTPClient *http.Client
	Logger     *slog.Logger
	Now        func() time.Time
}

// Gateway is one running webhook gateway session.
type Gateway struct {
	cfg   Config
	be    Backend
	st    *state
	log   *slog.Logger
	lease int

	mu        sync.Mutex // guards the fields below
	sessionID string
	nextBeat  time.Time
	retries   map[string]*retry
}

type retry struct {
	attempts int
	next     time.Time
}

// New validates cfg and loads the local state.
func New(cfg Config, be Backend) (*Gateway, error) {
	if cfg.TargetURL == "" {
		return nil, errors.New("a target URL is required")
	}
	if len(cfg.Secret) < MinSecretBytes {
		return nil, errors.New("the shared secret must be at least 32 bytes")
	}
	for name := range cfg.ExtraHeaders {
		if err := checkExtraHeaderName(name); err != nil {
			return nil, err
		}
	}
	if cfg.SessionName == "" {
		cfg.SessionName = DefaultSessionName
	}
	if cfg.Version == "" {
		cfg.Version = "0"
	}
	if cfg.LeaseSeconds <= 0 {
		cfg.LeaseSeconds = DefaultLeaseSeconds
	}
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultPoll
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.MaxSkew <= 0 {
		cfg.MaxSkew = DefaultMaxSkew
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: postTimeout,
			// A redirect could carry a signed body somewhere unconfigured.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	st, err := loadState(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	return &Gateway{cfg: cfg, be: be, st: st, log: log, retries: map[string]*retry{}}, nil
}

// SessionID is the gateway's current session ("" before Start).
func (g *Gateway) SessionID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessionID
}

// Start checks the adapter and registers (or resumes) the gateway session.
func (g *Gateway) Start(ctx context.Context) error {
	d, err := g.be.Describe(ctx)
	if err != nil {
		return err
	}
	g.lease = min(max(g.cfg.LeaseSeconds, d.Lease.MinSeconds), d.Lease.MaxSeconds)
	return g.register(ctx)
}

func (g *Gateway) registration(resume string) *protocol.SessionRegistration {
	lease := g.lease
	reg := &protocol.SessionRegistration{
		Harness:        HarnessName,
		HarnessVersion: g.cfg.Version,
		SessionName:    g.cfg.SessionName,
		Activity:       protocol.ActivityIdle,
		Inbound:        protocol.InboundAccept,
		LeaseSeconds:   &lease,
	}
	if g.cfg.Description != "" {
		desc := g.cfg.Description
		reg.SessionDescription = &desc
	}
	if resume != "" {
		reg.Resume = &protocol.ResumeRef{SessionID: resume}
	}
	return reg
}

// register resumes the remembered session when it can; a remembered
// session that is still live (another copy of this gateway, or a restart
// inside the lease) is adopted; one the backend no longer knows is
// replaced by a fresh registration.
func (g *Gateway) register(ctx context.Context) error {
	prev := g.st.sessionID()
	res, err := g.be.Register(ctx, g.registration(prev))
	if prev != "" && err != nil {
		var pe *protocol.Error
		switch {
		case errors.As(err, &pe) && pe.Code == protocol.CodeConflict:
			g.log.Warn("remembered gateway session is still live; adopting it")
			return g.adopt(prev)
		case errors.As(err, &pe) && pe.Code == protocol.CodeNotFound:
			res, err = g.be.Register(ctx, g.registration(""))
		}
	}
	if err != nil {
		return err
	}
	return g.adopt(res.SessionID)
}

func (g *Gateway) adopt(id string) error {
	g.mu.Lock()
	g.sessionID = id
	g.nextBeat = time.Time{}
	g.mu.Unlock()
	g.log.Info("gateway session ready", slog.String("session_id", id))
	return g.st.setSessionID(id)
}

// heartbeat renews the lease at a third of its length. A session the
// backend lost (not_found) or closed (conflict) is registered again.
func (g *Gateway) heartbeat(ctx context.Context) error {
	now := g.cfg.Now()
	g.mu.Lock()
	id, due := g.sessionID, g.nextBeat
	g.mu.Unlock()
	if now.Before(due) {
		return nil
	}
	idle := protocol.ActivityIdle
	lease := g.lease
	_, err := g.be.Heartbeat(ctx, id, &protocol.HeartbeatRequest{Activity: &idle, LeaseSeconds: &lease})
	var pe *protocol.Error
	if errors.As(err, &pe) && (pe.Code == protocol.CodeNotFound || pe.Code == protocol.CodeConflict) {
		g.log.Warn("gateway session lost; registering again", slog.String("code", string(pe.Code)))
		err = g.register(ctx)
	}
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.nextBeat = now.Add(time.Duration(g.lease) * time.Second / 3)
	g.mu.Unlock()
	return nil
}

// Run heartbeats and delivers until ctx ends, then closes the session.
func (g *Gateway) Run(ctx context.Context) error {
	t := time.NewTicker(g.cfg.Poll)
	defer t.Stop()
	for {
		if err := g.Tick(ctx); err != nil && ctx.Err() == nil {
			g.log.Warn("gateway tick failed", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adapterclient.CloseTimeout*3)
			defer cancel()
			if _, err := g.be.Close(cctx, g.SessionID()); err != nil {
				g.log.Warn("gateway session close failed", slog.String("error", err.Error()))
			}
			return nil
		case <-t.C:
		}
	}
}

// Tick is one heartbeat-if-due plus one delivery pass.
func (g *Gateway) Tick(ctx context.Context) error {
	if err := g.heartbeat(ctx); err != nil {
		return err
	}
	_, err := g.Deliver(ctx)
	return err
}

// Deliver drains one batch of the inbox to the webhook and returns how
// many messages were acknowledged.
func (g *Gateway) Deliver(ctx context.Context) (int, error) {
	id := g.SessionID()
	res, err := g.be.Receive(ctx, id, receiveLimit)
	if err != nil {
		return 0, err
	}
	var acked []string
	for i := range res.Messages {
		m := &res.Messages[i]
		if !g.due(m.MessageID) {
			continue
		}
		if g.deliverOne(ctx, id, m) {
			acked = append(acked, m.MessageID)
		}
	}
	g.pruneRetries(res.Messages)
	if len(acked) == 0 {
		return 0, nil
	}
	if _, err := g.be.Ack(ctx, id, &protocol.AckRequest{MessageIDs: acked}); err != nil {
		// Unacknowledged means redelivered (at-least-once); the receiver
		// deduplicates on delivery_id.
		return 0, err
	}
	return len(acked), nil
}

// deliverOne POSTs one message and reports whether it may be acked.
func (g *Gateway) deliverOne(ctx context.Context, self string, m *protocol.MessageEnvelope) bool {
	var thread *Thread
	var inherited string
	if m.ReplyTo != nil {
		if e, ok := g.st.thread(*m.ReplyTo); ok {
			inherited = e.Ref
			if e.Direction == dirIn {
				thread = &Thread{InReplyTo: *m.ReplyTo, Ref: e.Ref}
			}
		}
	}
	status, err := g.post(ctx, newOutbound(m, self, thread))
	switch {
	case err == nil && status >= 200 && status < 300:
		g.clearRetry(m.MessageID)
		if rerr := g.st.remember(m.MessageID, threadEntry{
			Direction: dirOut, Peer: m.Sender.SessionID, Ref: inherited, At: g.cfg.Now().UTC(),
		}); rerr != nil {
			g.log.Warn("gateway state not saved", slog.String("error", rerr.Error()))
		}
		return true
	case err == nil && !retryableStatus(status):
		if g.giveUp(ctx, self, m, "the webhook endpoint refused it with HTTP "+strconv.Itoa(status)) {
			return true
		}
		// The notice itself hit a retryable backend error, so the message
		// stays in the inbox. Take the backoff anyway: without it the next
		// poll — every --poll, 5 s by default — re-POSTs the same message
		// to an endpoint that has already refused it for good, for as long
		// as the backend is unavailable.
		g.backoff(m.MessageID)
		return false
	}
	attempts := g.backoff(m.MessageID)
	reason := "HTTP " + strconv.Itoa(status)
	if err != nil {
		reason = "transport error"
	}
	g.log.Warn("webhook delivery failed; will retry",
		slog.String("message_id", m.MessageID), slog.String("reason", reason), slog.Int("attempts", attempts))
	if attempts >= g.cfg.MaxAttempts {
		return g.giveUp(ctx, self, m, "the webhook endpoint kept failing ("+reason+") after "+strconv.Itoa(attempts)+" attempts")
	}
	return false
}

func retryableStatus(s int) bool {
	return s == http.StatusRequestTimeout || s == http.StatusTooEarly || s == http.StatusTooManyRequests || s >= 500
}

// giveUp tells the sender once (idempotent on the message id) and reports
// whether the message may now be acked: not when the notice itself hit a
// retryable backend error, so the next pass tries again.
func (g *Gateway) giveUp(ctx context.Context, self string, m *protocol.MessageEnvelope, why string) bool {
	_, err := g.be.Send(ctx, &protocol.SendRequest{
		SenderSessionID:    self,
		RecipientSessionID: m.Sender.SessionID,
		Body: "The webhook gateway could not deliver your message " + m.MessageID + ": " + why +
			". It was not delivered and will not be retried.",
		Summary:        "webhook delivery failed",
		ReplyTo:        m.MessageID,
		IdempotencyKey: "gw-err:" + m.MessageID,
	})
	var pe *protocol.Error
	if err != nil && (!errors.As(err, &pe) || pe.Code.Retryable()) {
		return false
	}
	g.clearRetry(m.MessageID)
	g.log.Warn("webhook delivery abandoned", slog.String("message_id", m.MessageID))
	return true
}

func (g *Gateway) post(ctx context.Context, p *OutboundPayload) (int, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	ts := g.cfg.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.TargetURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for name, values := range g.cfg.ExtraHeaders {
		for _, v := range values {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "brigade-webhook-gateway/"+g.cfg.Version)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(g.cfg.Secret, ts, body))
	req.Header.Set(HeaderDelivery, p.DeliveryID)
	resp, err := g.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (g *Gateway) due(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.retries[id]
	return !ok || !g.cfg.Now().Before(r.next)
}

func (g *Gateway) backoff(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.retries[id]
	if !ok {
		r = &retry{}
		g.retries[id] = r
	}
	r.attempts++
	wait := backoffBase << min(r.attempts-1, 10)
	r.next = g.cfg.Now().Add(min(wait, backoffCap))
	return r.attempts
}

func (g *Gateway) clearRetry(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.retries, id)
}

// pruneRetries forgets retry state for messages no longer in the batch.
func (g *Gateway) pruneRetries(batch []protocol.MessageEnvelope) {
	if len(batch) >= receiveLimit {
		return // a full batch may hide older pending ids; keep their state
	}
	present := make(map[string]bool, len(batch))
	for i := range batch {
		present[batch[i].MessageID] = true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for id := range g.retries {
		if !present[id] {
			delete(g.retries, id)
		}
	}
}
