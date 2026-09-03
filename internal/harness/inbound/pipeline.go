// Package inbound is the receive-side pipeline of plan 6.8 — the ordered
// controls every `message` event passes before it is framed and handed to
// an injector — implemented once, PURE, so that P3-5's watcher (socket
// post or `--sink`) and P3-4's prompt-hook poll (6.3) run the same code:
//
//  1. loose validation (U-18): reject silently with a warn log;
//  2. dedupe (U-13): an id already injected — in the in-memory LRU of
//     SeenCapacity ids or the seen file loaded at start — is not injected
//     again but IS acknowledged (its earlier ack may have failed);
//  3. policy (D18): under Refuse nothing is injected and nothing acked;
//  4. the per-sender rate (U-14): beyond SenderRatePerMinute a message is
//     left unacknowledged and one summarised notice per NoticeWindow per
//     sender is queued;
//  5. identical-body deferral: the same sender and body within
//     DeferralWindow is neither injected nor acknowledged now; the server
//     redelivers it after the window and it is injected once;
//  6. the bounded queue (U-15, E2E-13): QueueCapacity pending injections,
//     oldest dropped (unacknowledged, redelivered) with one notice per
//     NoticeWindow;
//  7. sanitise and frame (frame.Build, then frame.Wrap for socket posts),
//     hand the content to the caller, and on the caller's report of
//     success remember the id and acknowledge; on failure neither.
//
// The pipeline never sleeps, never touches a socket or a file of its own
// (the seen file goes through the injected SeenStore) and reads the time
// from an injected Clock, so every time-based rule is exact under
// testing/synctest. Every decision comes back as a value. The caller's
// contract is Offer → Next → inject → Done: Offer runs steps 1–6 and says
// what became of the message (a duplicate is acknowledged from Offer's
// Decision alone), Next hands out the next pending injection — notices
// first, then messages oldest first — and Done reports whether the caller
// injected it, which is when an id becomes seen and acknowledgeable. Under
// `poll_on_prompt` the hook injects by printing, and reports "not
// printed" for whatever did not fit the hook-output cap, so only printed
// frames are acknowledged (6.3). Drain runs the Next/inject/Done loop for
// a synchronous caller. A Pipeline is safe for concurrent use: a reader
// goroutine may Offer while an injector goroutine drains.
//
// E0-3 (d) measured that the native harness applies no rate limit and no
// dedupe to socket posts on any key, so these controls are the only ones a
// receiving session has.
package inbound

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/protocol"
)

// A Clock supplies the pipeline's notion of now. Under testing/synctest the
// system clock is the bubble's fake clock, so tests rarely need their own.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

// Now implements Clock.
func (f ClockFunc) Now() time.Time { return f() }

// SystemClock is time.Now.
func SystemClock() Clock { return ClockFunc(time.Now) }

// Config builds a Pipeline.
type Config struct {
	// Policy is the effective inbound policy (policy.Decide): Accept or
	// Refuse. Anything else is an error from New (fail closed, loudly).
	Policy policy.Policy
	// TeamName is the human team name for the frame's team attribute (the
	// envelope carries only the opaque team_ref); the by-pid map has it.
	TeamName string
	// Wrap nests each frame in the variant-C wrapper (frame.Wrap) for a
	// socket post; the prompt-hook poll prints the bare frame and leaves
	// this false.
	Wrap bool
	// Clock is the time source; nil means SystemClock.
	Clock Clock
	// Seen persists injected ids across restarts; nil means memory only
	// (a restart may then inject an unacknowledged message a second time).
	Seen SeenStore
	// Logger receives scalar diagnostics; nil means discard. Bodies are
	// never logged.
	Logger *slog.Logger
}

// An Outcome is what a call decided for one message or notice.
type Outcome string

// The outcomes, in pipeline order.
const (
	// OutcomeRejected: step 1 failed (Reason names the rule); no ack.
	OutcomeRejected Outcome = "rejected"
	// OutcomeDuplicate: already injected (step 2); Ack is true, nothing
	// is queued.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomePending: the same id is already queued or handed out and
	// not yet Done; ignored, no ack.
	OutcomePending Outcome = "pending"
	// OutcomeRefused: the policy is Refuse (step 3); no injection, no ack.
	OutcomeRefused Outcome = "refused"
	// OutcomeRateLimited: over the sender's rate (step 4); left
	// unacknowledged; a notice may have been queued.
	OutcomeRateLimited Outcome = "rate_limited"
	// OutcomeDeferred: an identical body within the window (step 5); left
	// unacknowledged for the server to redeliver.
	OutcomeDeferred Outcome = "deferred"
	// OutcomeQueued: waiting for Next (step 6); Dropped names the oldest
	// message evicted to make room, if any.
	OutcomeQueued Outcome = "queued"
	// OutcomeInjected: Done reported success; Ack is true and the id is
	// remembered.
	OutcomeInjected Outcome = "injected"
	// OutcomeNotInjected: Done reported failure; no ack, not remembered,
	// the server redelivers.
	OutcomeNotInjected Outcome = "not_injected"
	// OutcomeNoticeDone: Done for a notice item, delivered or not.
	OutcomeNoticeDone Outcome = "notice_done"
	// OutcomeUnknown: Done for an item the pipeline did not hand out.
	OutcomeUnknown Outcome = "unknown"
)

// A Decision is the value every Offer and Done returns.
type Decision struct {
	// Outcome says what happened.
	Outcome Outcome
	// MessageID is the message concerned ("" for a notice or a message
	// whose id failed validation).
	MessageID string
	// Ack is true exactly when the caller must acknowledge MessageID:
	// a duplicate from Offer, or a successful injection from Done.
	Ack bool
	// Reason is a fixed token with detail (a validation rule, "sender_rate",
	// "identical_body", "inject_failed"); never a value.
	Reason string
	// Dropped is the id of the oldest queued message evicted by this
	// Offer, "" otherwise. It stays unacknowledged.
	Dropped string
}

// An ItemKind says what an Item carries.
type ItemKind int

// The item kinds.
const (
	// ItemMessage is a framed message; Done with a nil error acknowledges
	// it.
	ItemMessage ItemKind = iota + 1
	// ItemRateNotice is a per-sender rate-limit notice line.
	ItemRateNotice
	// ItemDropNotice is the queue-drop notice line.
	ItemDropNotice
)

// String names the kind for logs and sink records.
func (k ItemKind) String() string {
	switch k {
	case ItemMessage:
		return "message"
	case ItemRateNotice:
		return "rate_notice"
	case ItemDropNotice:
		return "drop_notice"
	default:
		return "unknown"
	}
}

// An Item is one pending injection handed out by Next. Content is what to
// inject: for a message the frame (wrapped when Config.Wrap is set), for a
// notice one plain line of Brigade's own text. Every Item handed out must
// be passed back to Done exactly once.
type Item struct {
	Kind ItemKind
	// MessageID and SenderSessionID identify a message (the `--sink`
	// record of 6.6 carries both); a notice has the sender id when it is
	// per sender.
	MessageID       string
	SenderSessionID string
	// SenderName is the sanitised, one-line session name.
	SenderName string
	// Content is the text to inject.
	Content string

	sender *senderState
}

// ErrNotInjected is the error a caller passes to Done for an item it chose
// not to inject (the prompt hook's output cap, 6.3): the message stays
// unacknowledged for the next poll or drain.
var ErrNotInjected = errors.New("inbound: item not injected")

// A Pipeline is the receive-side state of one session.
type Pipeline struct {
	mu        sync.Mutex
	cfg       Config
	log       *slog.Logger
	seen      *orderedMap[string, struct{}]
	pending   map[string]*queued
	queue     *queue
	limiter   *limiter
	deferrals *deferrals
	notices   []*senderState
	drops     noticeWindow
}

// New builds a Pipeline and loads the seen store. A store that cannot be
// loaded is logged and the pipeline starts empty; an invalid policy is an
// error.
func New(cfg Config) (*Pipeline, error) {
	if !cfg.Policy.Valid() {
		return nil, errors.New("inbound: policy must be accept or refuse")
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	p := &Pipeline{
		cfg:       cfg,
		log:       logger,
		seen:      newOrderedMap[string, struct{}](SeenCapacity),
		pending:   map[string]*queued{},
		queue:     newQueue(QueueCapacity),
		limiter:   newLimiter(),
		deferrals: newDeferrals(),
	}
	if cfg.Seen != nil {
		ids, err := cfg.Seen.Load()
		if err != nil {
			logger.Warn("seen file not loaded; starting with no seen ids", log.Err(err))
		}
		for _, id := range ids {
			p.seen.put(id, struct{}{})
		}
		logger.Debug("seen ids loaded", slog.Int("count", p.seen.size()))
	}
	return p, nil
}

// Policy is the current policy.
func (p *Pipeline) Policy() policy.Policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Policy
}

// SetPolicy changes the policy for later Offers (the watcher re-reads the
// by-pid map every heartbeat, 6.6). An invalid value is ignored.
func (p *Pipeline) SetPolicy(pol policy.Policy) {
	if !pol.Valid() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Policy = pol
}

// Offer runs steps 1–6 for one message event and returns the Decision.
// Ack is true only for a duplicate; a queued message is acknowledged
// through Done after injection.
func (p *Pipeline) Offer(m protocol.MessageEnvelope) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.cfg.Clock.Now()

	if err := Validate(&m); err != nil {
		var verr *ValidationError
		reason := "invalid"
		if errors.As(err, &verr) {
			reason = verr.Reason
		}
		p.log.Warn("message rejected",
			slog.String("reason", reason),
			slog.Int("message_id_bytes", len(m.MessageID)),
			slog.Int("body_bytes", len(m.Body)))
		return Decision{Outcome: OutcomeRejected, Reason: reason}
	}
	id := m.MessageID

	if p.seen.has(id) {
		p.log.Debug("duplicate message; acknowledging without injection", slog.String("message_id", id))
		return Decision{Outcome: OutcomeDuplicate, MessageID: id, Ack: true, Reason: "seen"}
	}
	if _, ok := p.pending[id]; ok {
		p.log.Debug("message already pending", slog.String("message_id", id))
		return Decision{Outcome: OutcomePending, MessageID: id, Reason: "pending"}
	}
	if p.cfg.Policy != policy.Accept {
		p.log.Info("message refused by policy", slog.String("message_id", id), slog.String("policy", p.cfg.Policy.String()))
		return Decision{Outcome: OutcomeRefused, MessageID: id, Reason: "policy_refuse"}
	}

	s := p.limiter.sender(m.Sender.SessionID, m.Sender.SessionName, now)
	if !s.allow(now) {
		if s.hold(now) {
			p.notices = append(p.notices, s)
		}
		p.log.Info("message held back for rate limiting",
			slog.String("message_id", id),
			slog.String("sender_session_id", m.Sender.SessionID),
			slog.Int("held", s.held))
		return Decision{Outcome: OutcomeRateLimited, MessageID: id, Reason: "sender_rate"}
	}

	key := deferralKey(m.Sender.SessionID, m.Body)
	if p.deferrals.deferred(key, now) {
		p.log.Info("identical body deferred",
			slog.String("message_id", id),
			slog.String("sender_session_id", m.Sender.SessionID))
		return Decision{Outcome: OutcomeDeferred, MessageID: id, Reason: "identical_body"}
	}
	p.deferrals.mark(key, now)

	q := &queued{env: m, key: key, queuedAt: now}
	dropped := p.queue.push(q)
	p.pending[id] = q
	d := Decision{Outcome: OutcomeQueued, MessageID: id}
	if dropped != nil {
		delete(p.pending, dropped.env.MessageID)
		p.deferrals.clear(dropped.key)
		p.drops.hit(now)
		d.Dropped = dropped.env.MessageID
		p.log.Warn("injection queue full; oldest message dropped",
			slog.String("dropped_message_id", dropped.env.MessageID),
			slog.Int("dropped_total", p.drops.count))
	}
	p.log.Debug("message queued", slog.String("message_id", id), slog.Int("queue_len", p.queue.size()))
	return d
}

// Next hands out the next pending injection: rate notices in the order
// they arose, then the drop notice, then the oldest queued message,
// framed. ok is false when nothing is pending. The caller injects
// Item.Content and then calls Done.
func (p *Pipeline) Next() (Item, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for len(p.notices) > 0 {
		s := p.notices[0]
		p.notices[0] = nil
		p.notices = p.notices[1:]
		if s.noticePending {
			return Item{
				Kind:            ItemRateNotice,
				SenderSessionID: s.id,
				SenderName:      s.name,
				Content:         RateNotice(s.held, s.name),
				sender:          s,
			}, true
		}
	}
	if p.drops.pending {
		return Item{Kind: ItemDropNotice, Content: DropNotice(p.drops.count)}, true
	}
	q, ok := p.queue.pop()
	if !ok {
		return Item{}, false
	}
	q.handed = true
	content := frame.Build(q.env, p.cfg.TeamName)
	if p.cfg.Wrap {
		content = frame.Wrap(content, q.env.Sender.SessionName)
	}
	return Item{
		Kind:            ItemMessage,
		MessageID:       q.env.MessageID,
		SenderSessionID: q.env.Sender.SessionID,
		SenderName:      noticeName(q.env.Sender.SessionName),
		Content:         content,
	}, true
}

// Done reports the result of injecting an item Next handed out. A nil err
// for a message remembers its id (memory and the seen store) and returns
// Ack true; a non-nil err (ErrNotInjected, a socketpost error, anything)
// leaves it unacknowledged and forgets it, so the server's redelivery is
// treated afresh. For a notice, Done closes it either way.
func (p *Pipeline) Done(item Item, err error) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch item.Kind {
	case ItemRateNotice:
		if item.sender != nil {
			item.sender.noticePending = false
		}
		return Decision{Outcome: OutcomeNoticeDone, Reason: item.Kind.String()}
	case ItemDropNotice:
		p.drops.pending = false
		return Decision{Outcome: OutcomeNoticeDone, Reason: item.Kind.String()}
	case ItemMessage:
	default:
		return Decision{Outcome: OutcomeUnknown, Reason: "unknown_item"}
	}

	q, ok := p.pending[item.MessageID]
	if !ok || !q.handed {
		p.log.Warn("Done for a message the pipeline did not hand out", slog.String("message_id", item.MessageID))
		return Decision{Outcome: OutcomeUnknown, MessageID: item.MessageID, Reason: "not_handed_out"}
	}
	delete(p.pending, item.MessageID)
	if err != nil {
		p.deferrals.clear(q.key)
		p.log.Warn("message not injected; left unacknowledged",
			slog.String("message_id", item.MessageID),
			log.Err(err))
		return Decision{Outcome: OutcomeNotInjected, MessageID: item.MessageID, Reason: "inject_failed"}
	}
	p.remember(item.MessageID)
	p.log.Debug("message injected", slog.String("message_id", item.MessageID))
	return Decision{Outcome: OutcomeInjected, MessageID: item.MessageID, Ack: true}
}

// remember adds an injected id to the LRU and persists the list.
func (p *Pipeline) remember(id string) {
	p.seen.put(id, struct{}{})
	if p.cfg.Seen == nil {
		return
	}
	if err := p.cfg.Seen.Save(p.seen.keys()); err != nil {
		p.log.Warn("seen file not written", log.Err(err))
	}
}

// Drain runs Next → inject → Done until nothing is pending and returns the
// ids to acknowledge. inject is called without the pipeline's lock held,
// so it may block on a socket. A notice's injection error is logged
// through Done and otherwise ignored.
func (p *Pipeline) Drain(inject func(Item) error) []string {
	var acks []string
	for {
		item, ok := p.Next()
		if !ok {
			return acks
		}
		d := p.Done(item, inject(item))
		if d.Ack {
			acks = append(acks, d.MessageID)
		}
	}
}

// Stats are the pipeline's sizes, for the watcher's log and for tests.
type Stats struct {
	// Queued is the number of messages waiting for Next.
	Queued int
	// Pending is Queued plus the messages handed out and not yet Done.
	Pending int
	// Senders, Deferrals and Seen are the sizes of the bounded maps.
	Senders   int
	Deferrals int
	Seen      int
	// Notices is the number of notices waiting for Next.
	Notices int
}

// Stats reports the current sizes.
func (p *Pipeline) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.notices)
	if p.drops.pending {
		n++
	}
	return Stats{
		Queued:    p.queue.size(),
		Pending:   len(p.pending),
		Senders:   p.limiter.senders.size(),
		Deferrals: p.deferrals.m.size(),
		Seen:      p.seen.size(),
		Notices:   n,
	}
}
