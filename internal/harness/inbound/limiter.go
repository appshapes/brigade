package inbound

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The receive-side bounds of plan 6.8 item 6 (D17). They are the harness's
// own backstop and, since E0-3 (d) measured that the native harness applies
// no rate limit and no dedupe to socket posts on any key, the ONLY
// protection a receiving session has.
const (
	// SenderRatePerMinute is how many messages from one sender session are
	// injected per RateWindow; the rest are held back unacknowledged
	// (U-14).
	SenderRatePerMinute = 10
	// RateWindow is the sliding window of the sender rate.
	RateWindow = time.Minute
	// NoticeWindow is how often, per sender (rate) and overall (queue
	// drops), one summarised notice is injected.
	NoticeWindow = 5 * time.Minute
	// DeferralWindow is how long an identical body from the same sender is
	// deferred (not injected, not acknowledged) after the first one.
	DeferralWindow = 60 * time.Second
	// MaxSenders bounds the per-sender states kept in memory; the least
	// recently active sender is forgotten first.
	MaxSenders = 1000
	// MaxDeferrals bounds the identical-body entries kept in memory.
	MaxDeferrals = 2000
)

// senderState is what the pipeline remembers per sender session: the
// accept times inside the rate window, the held-back count and notice
// window, and the latest sanitised name for the notice text.
type senderState struct {
	id            string
	name          string
	accepted      []time.Time
	held          int
	noticeAt      time.Time
	noticePending bool
	last          time.Time
}

// allow applies the sliding window: at most SenderRatePerMinute accepts
// whose time is within RateWindow of now. It records the accept.
func (s *senderState) allow(now time.Time) bool {
	cut := now.Add(-RateWindow)
	keep := s.accepted[:0]
	for _, t := range s.accepted {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	s.accepted = keep
	if len(s.accepted) >= SenderRatePerMinute {
		return false
	}
	s.accepted = append(s.accepted, now)
	return true
}

// hold counts one held-back message and reports whether a NEW notice must
// be queued: the first hold of a NoticeWindow opens a window and its
// notice; later holds in the same window only raise the count, and while a
// notice is still pending delivery every hold is folded into it whatever
// the window, so the count it finally reports is complete.
func (s *senderState) hold(now time.Time) bool {
	if s.noticePending {
		s.held++ // the notice not yet delivered will report this one too
		return false
	}
	if s.noticeAt.IsZero() || !now.Before(s.noticeAt.Add(NoticeWindow)) {
		s.noticeAt = now
		s.held = 1
		s.noticePending = true
		return true
	}
	s.held++
	return false
}

// A limiter owns the bounded sender states.
type limiter struct {
	senders *orderedMap[string, *senderState]
}

func newLimiter() *limiter {
	return &limiter{senders: newOrderedMap[string, *senderState](MaxSenders)}
}

// sender returns the state for a sender session, creating it as needed,
// touching it as the most recent, refreshing its display name, and
// forgetting senders idle for longer than NoticeWindow.
func (l *limiter) sender(id, name string, now time.Time) *senderState {
	l.senders.pruneOldest(func(_ string, s *senderState) bool {
		return now.Sub(s.last) > NoticeWindow
	})
	s, ok := l.senders.get(id)
	if !ok {
		s = &senderState{id: id}
	}
	s.name = noticeName(name)
	s.last = now
	l.senders.put(id, s)
	return s
}

// noticeWindow is the pipeline-wide drop notice: one per NoticeWindow.
type noticeWindow struct {
	count   int
	at      time.Time
	pending bool
}

// hit counts one drop and reports whether a new notice must be queued.
func (w *noticeWindow) hit(now time.Time) bool {
	if w.pending {
		w.count++
		return false
	}
	if w.at.IsZero() || !now.Before(w.at.Add(NoticeWindow)) {
		w.at = now
		w.count = 1
		w.pending = true
		return true
	}
	w.count++
	return false
}

// deferrals is the identical-body memory: (sender, body hash) → the time
// the first copy was queued.
type deferrals struct {
	m *orderedMap[string, time.Time]
}

func newDeferrals() *deferrals {
	return &deferrals{m: newOrderedMap[string, time.Time](MaxDeferrals)}
}

// deferralKey is sender session id + NUL + hex SHA-256 of the raw body.
func deferralKey(senderSessionID, body string) string {
	sum := sha256.Sum256([]byte(body))
	return senderSessionID + "\x00" + hex.EncodeToString(sum[:])
}

// deferred reports whether an identical body from the same sender was
// queued within DeferralWindow of now, after expiring older entries.
func (d *deferrals) deferred(key string, now time.Time) bool {
	d.m.pruneOldest(func(_ string, at time.Time) bool {
		return !now.Before(at.Add(DeferralWindow))
	})
	_, ok := d.m.get(key)
	return ok
}

// mark records that a body was queued now.
func (d *deferrals) mark(key string, now time.Time) { d.m.put(key, now) }

// clear forgets a body whose message was dropped or not injected, so its
// redelivery is not mistaken for a repeat.
func (d *deferrals) clear(key string) { d.m.delete(key) }

// noticeName renders a sender's session name for a notice line: the
// protocol sanitiser (rules 1–3, 64 code points), then folded onto one line
// (newlines and tabs, which the sanitiser keeps, become single spaces),
// with a fixed stand-in when nothing is left.
func noticeName(name string) string {
	s := strings.Join(strings.Fields(protocol.SanitizeName(name)), " ")
	if s == "" {
		return "an unnamed session"
	}
	return s
}

// RateNotice is the summarised notice injected once per NoticeWindow for a
// sender whose messages were held back (6.8 item 6, U-14). name must
// already be sanitised (the pipeline uses noticeName).
func RateNotice(n int, name string) string {
	return "Brigade: " + plural(n, "message") + " from " + name + " held back for rate limiting; they will be delivered later"
}

// DropNotice is the summarised notice injected once per NoticeWindow when
// the bounded queue dropped messages (6.8 item 6, U-15). The dropped
// messages stay unacknowledged on the server and are redelivered.
func DropNotice(n int) string {
	return "Brigade: " + plural(n, "message") + " dropped from the injection queue (limit " + strconv.Itoa(QueueCapacity) + "); they remain on the server and will be delivered later"
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
