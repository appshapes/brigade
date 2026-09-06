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
//     under Hold (P5-9) the message is recorded — sender, summary, when;
//     never the body — in the pending file through the injected
//     PendingStore, neither injected nor acked, and skips every later
//     step until the human releases it from a terminal (Release), when it
//     enters the queue directly at step 6;
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
	"fmt"
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
	// Policy is the effective inbound policy (policy.Decide): Accept, Hold
	// or Refuse. Anything else is an error from New (fail closed, loudly).
	Policy policy.Policy
	// Instruction selects the frame's instruction paragraph (P5-12): the
	// by-pid map's frame_level and frame_text, resolved once by the hook.
	// One that fails frame.Instruction.Validate is an error from New —
	// fail closed and loudly, never a silent fallback to a level the user
	// did not choose.
	Instruction frame.Instruction
	// SessionID is the Brigade session id; it is written into and checked
	// against the pending file by a FilePendingStore.
	SessionID string
	// Pending persists the messages held under Hold across restarts; nil
	// means hold is recorded in memory only (tests). A Hold policy with a
	// nil store is legal but logged once at Warn, because the held notice
	// would not survive a restart.
	Pending PendingStore
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
	// OutcomeHeld: recorded in the pending file under Hold (step 3), or
	// already held there; no injection, no ack, until Release.
	OutcomeHeld Outcome = "held"
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
	// held is the `hold` state (3.3): the pending entries oldest first,
	// each with its envelope once the server has delivered it to this
	// process, bounded at HoldCapacity; dropped counts the evictions.
	held    *orderedMap[string, *heldEntry]
	dropped int
}

// A heldEntry is one held message in memory: the pending-file record and,
// when this process has seen the envelope, the envelope itself, so a
// release can inject NOW rather than wait for the next restart (3.3). An
// entry loaded from the file has no envelope until the server redelivers
// the message (every unacknowledged id is re-emitted on each watch-child
// restart and returned on every poll).
type heldEntry struct {
	entry PendingEntry
	env   *protocol.MessageEnvelope
}

// New builds a Pipeline and loads the seen and pending stores. A store
// that cannot be loaded is logged and the pipeline starts empty; an
// invalid policy or frame instruction is an error.
func New(cfg Config) (*Pipeline, error) {
	if !cfg.Policy.Valid() {
		return nil, errors.New("inbound: policy must be accept, hold or refuse")
	}
	if err := cfg.Instruction.Validate(); err != nil {
		return nil, fmt.Errorf("inbound: frame instruction: %w", err)
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
		held:      newOrderedMap[string, *heldEntry](HoldCapacity),
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
	switch {
	case cfg.Pending != nil:
		f, err := cfg.Pending.Load()
		if err != nil {
			logger.Warn("pending file not loaded; starting with no held messages", log.Err(err))
		}
		for i := range f.Entries {
			p.held.put(f.Entries[i].MessageID, &heldEntry{entry: f.Entries[i]})
		}
		p.dropped = f.DroppedTotal
		logger.Debug("held messages loaded", slog.Int("count", p.held.size()), slog.Int("dropped_total", p.dropped))
	case cfg.Policy == policy.Hold:
		logger.Warn("hold policy with no pending store: held messages are recorded in memory only and the notice will not survive a restart")
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

// Instruction is the frame instruction later frames are built with.
func (p *Pipeline) Instruction() frame.Instruction {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Instruction
}

// SetInstruction changes the frame instruction for later frames, the way
// SetPolicy changes the policy: the hook rewrites the by-pid map on every
// SessionStart (a new session, /clear, /reload-plugins), which is when a
// changed frame option or an edited frame_file takes effect (P5-12). An
// invalid value is ignored.
func (p *Pipeline) SetInstruction(in frame.Instruction) {
	if in.Validate() != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Instruction = in
}

// Offer runs steps 1–6 for one message event and returns the Decision.
// Ack is true only for a duplicate; a queued message is acknowledged
// through Done after injection, and a held one never — Ack is false on
// every hold path, and that boolean is the only thing that makes a caller
// acknowledge.
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
	// An id already held stays held until the human releases it, whatever
	// the policy now says (a hold→accept flip is not a release); the
	// envelope is kept so a release can inject without a restart, and no
	// write happens, so a session held for a day does not rewrite its
	// pending file on every redelivery. A released id takes the accept
	// path below, skipping the rate bucket and the deferral (3.4).
	released := false
	if h, ok := p.held.get(id); ok {
		h.env = &m
		if !h.entry.Released() {
			p.log.Debug("message already held", slog.String("message_id", id))
			return Decision{Outcome: OutcomeHeld, MessageID: id, Reason: "already_held"}
		}
		released = true
	}
	switch {
	case p.cfg.Policy == policy.Hold && !released:
		return p.hold(m, now)
	case p.cfg.Policy != policy.Accept && p.cfg.Policy != policy.Hold:
		p.log.Info("message refused by policy", slog.String("message_id", id), slog.String("policy", p.cfg.Policy.String()))
		return Decision{Outcome: OutcomeRefused, MessageID: id, Reason: "policy_refuse"}
	case released:
		return p.enqueue(m, deferralKey(m.Sender.SessionID, m.Body), now, "released")
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
	return p.enqueue(m, key, now, "")
}

// enqueue is step 6 for one message: onto the bounded queue, the oldest
// dropped (unacknowledged) when it is full.
func (p *Pipeline) enqueue(m protocol.MessageEnvelope, key string, now time.Time, reason string) Decision {
	id := m.MessageID
	q := &queued{env: m, key: key, queuedAt: now}
	dropped := p.queue.push(q)
	p.pending[id] = q
	d := Decision{Outcome: OutcomeQueued, MessageID: id, Reason: reason}
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

// hold is step 3 under Hold (3.3): record the message — never its body —
// in memory and in the pending store, and decide OutcomeHeld with Ack
// false. Held messages skip the rate limiter, the deferral and the
// queue: they are not injections, and under hold the pipeline injects
// nothing at all, notices included; the user learns the count from the
// prompt hook's held notice. At HoldCapacity the OLDEST entry is dropped
// from memory and from the file and is NOT acknowledged, so the server
// keeps it and redelivers it on the next watch-child restart; the sender
// is never told it arrived.
func (p *Pipeline) hold(m protocol.MessageEnvelope, now time.Time) Decision {
	id := m.MessageID
	entry := PendingEntry{
		MessageID:       id,
		SenderSessionID: m.Sender.SessionID,
		SenderName:      protocol.SanitizeName(m.Sender.SessionName),
		SenderPrincipal: m.Sender.PrincipalRef,
		Summary:         protocol.SanitizeSummary(m.Summary),
		ReceivedAt:      now,
	}
	if evicted, ok := p.held.put(id, &heldEntry{entry: entry, env: &m}); ok {
		p.dropped++
		p.log.Warn("held messages at capacity; oldest entry dropped from the pending file, not acknowledged",
			slog.String("dropped_message_id", evicted),
			slog.Int("dropped_total", p.dropped))
	}
	p.savePending(now)
	p.log.Info("message held for review", slog.String("message_id", id), slog.String("sender_session_id", m.Sender.SessionID), slog.Int("held", p.held.size()))
	return Decision{Outcome: OutcomeHeld, MessageID: id, Reason: "policy_hold"}
}

// savePending persists the held entries through the pending store.
func (p *Pipeline) savePending(now time.Time) {
	if p.cfg.Pending == nil {
		return
	}
	if err := p.cfg.Pending.Save(PendingFile{Entries: p.heldEntries(), DroppedTotal: p.dropped, UpdatedAt: now}); err != nil {
		p.log.Warn("pending file not written", log.Err(err))
	}
}

// heldEntries copies the held entries oldest first.
func (p *Pipeline) heldEntries() []PendingEntry {
	out := make([]PendingEntry, 0, p.held.size())
	for _, id := range p.held.keys() {
		h, _ := p.held.get(id)
		out = append(out, h.entry)
	}
	return out
}

// A ReleaseResult is what Release did.
type ReleaseResult struct {
	// Stamped are the ids newly stamped released_at by this call.
	Stamped []string
	// Queued are the ids moved into the injection queue by this call.
	Queued []string
	// Waiting counts the released entries not queued by this call: the
	// queue had no room, this process has not yet seen the envelope (the
	// server redelivers it on the next restart or poll), or the policy is
	// Refuse; the next call takes them.
	Waiting int
	// Unknown are the ids that are not held here; they change nothing.
	Unknown []string
}

// Release stamps released_at on the named ids, persists the pending file,
// and moves as many released entries as the injection queue has room for
// into the queue, oldest first, directly at step 6 — skipping the
// per-sender bucket and the identical-body deferral, because the human's
// release IS the rate limit (3.4). ids may be empty, which means "re-queue
// whatever is already stamped": the watcher's liveness tick and the poll
// call it that way. A released id already in the seen LRU was injected
// before and is dropped from the held set (the server's redelivery is
// acknowledged as a duplicate); one already queued or handed out is left
// alone. Under Refuse nothing is queued (the stamps are kept, so the next
// policy change delivers them). Stamping is idempotent, so a crash between
// the pending save and the release file's deletion re-applies harmlessly.
func (p *Pipeline) Release(ids []string) ReleaseResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.cfg.Clock.Now()
	var res ReleaseResult
	for _, id := range ids {
		h, ok := p.held.get(id)
		if !ok {
			res.Unknown = append(res.Unknown, id)
			continue
		}
		if !h.entry.Released() {
			h.entry.ReleasedAt = now
			res.Stamped = append(res.Stamped, id)
		}
	}
	if len(res.Stamped) > 0 {
		p.savePending(now)
		p.log.Info("held messages released", slog.Int("stamped", len(res.Stamped)), slog.Int("unknown", len(res.Unknown)))
	}
	for _, id := range p.held.keys() {
		h, _ := p.held.get(id)
		if !h.entry.Released() {
			continue
		}
		if _, queued := p.pending[id]; queued {
			continue // already in the queue or handed out
		}
		switch {
		case p.seen.has(id):
			p.held.delete(id)
			p.savePending(now)
			p.log.Debug("released message was already injected; dropped from the held set", slog.String("message_id", id))
			continue
		case p.cfg.Policy == policy.Refuse, h.env == nil, p.queue.full():
			res.Waiting++
			continue
		}
		m := *h.env
		p.enqueue(m, deferralKey(m.Sender.SessionID, m.Body), now, "released")
		res.Queued = append(res.Queued, id)
	}
	return res
}

// Pending returns a copy of the held entries, oldest first, for the held
// notice and for `brigade inbox`.
func (p *Pipeline) Pending() []PendingEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.heldEntries()
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
	content := frame.Build(q.env, p.cfg.TeamName, p.cfg.Instruction)
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
	if p.held.delete(item.MessageID) {
		// The released message is delivered: out of the pending file in
		// the same step that put it in the seen file, so an id is never
		// both seen and pending (3.4).
		p.savePending(p.cfg.Clock.Now())
	}
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
	// Held is the number of messages held under the hold policy, released
	// but not yet delivered ones included.
	Held int
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
		Held:      p.held.size(),
	}
}
