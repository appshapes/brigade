package inbound

import (
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// QueueCapacity bounds the pending injections; when a message is queued
// onto a full queue the OLDEST queued message is dropped (left
// unacknowledged; the server redelivers it) (6.8 item 6, U-15, E2E-13).
const QueueCapacity = 50

// A queued message waits for the caller to inject it.
type queued struct {
	env      protocol.MessageEnvelope
	key      string // the deferral key, cleared on drop or failure
	queuedAt time.Time
	handed   bool // true once Next handed it out, until Done
}

// A queue is the bounded FIFO of queued messages.
type queue struct {
	items    []*queued
	capacity int
}

func newQueue(capacity int) *queue {
	if capacity <= 0 {
		capacity = 1
	}
	return &queue{capacity: capacity}
}

// push appends q and, when the queue was already full, removes and returns
// the oldest item.
func (s *queue) push(q *queued) *queued {
	var dropped *queued
	if len(s.items) >= s.capacity {
		dropped = s.items[0]
		s.items[0] = nil
		s.items = s.items[1:]
	}
	s.items = append(s.items, q)
	return dropped
}

// pop removes and returns the oldest item.
func (s *queue) pop() (*queued, bool) {
	if len(s.items) == 0 {
		return nil, false
	}
	q := s.items[0]
	s.items[0] = nil
	s.items = s.items[1:]
	return q, true
}

// size is the number of queued items.
func (s *queue) size() int { return len(s.items) }

// full reports whether the next push would drop the oldest item.
func (s *queue) full() bool { return len(s.items) >= s.capacity }
