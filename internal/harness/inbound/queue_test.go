package inbound

import (
	"strconv"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

func qm(id string) *queued {
	return &queued{env: protocol.MessageEnvelope{MessageID: id}, key: "k" + id}
}

func TestQueueFIFOAndOldestDrop(t *testing.T) {
	t.Parallel()
	q := newQueue(3)
	for _, id := range []string{"a", "b", "c"} {
		if d := q.push(qm(id)); d != nil {
			t.Fatalf("push %s dropped %s", id, d.env.MessageID)
		}
	}
	if q.size() != 3 {
		t.Fatalf("size %d", q.size())
	}
	d := q.push(qm("d"))
	if d == nil || d.env.MessageID != "a" || q.size() != 3 {
		t.Fatalf("push over capacity: dropped %v size %d", d, q.size())
	}
	var got []string
	for {
		x, ok := q.pop()
		if !ok {
			break
		}
		got = append(got, x.env.MessageID)
	}
	if len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "d" {
		t.Fatalf("popped %v", got)
	}
	if _, ok := q.pop(); ok || q.size() != 0 {
		t.Fatal("pop on empty")
	}
}

func TestQueueNeverExceedsCapacity(t *testing.T) {
	t.Parallel()
	q := newQueue(QueueCapacity)
	drops := 0
	for i := range 1000 {
		if q.push(qm(strconv.Itoa(i))) != nil {
			drops++
		}
		if q.size() > QueueCapacity {
			t.Fatalf("size %d after push %d", q.size(), i)
		}
	}
	if drops != 1000-QueueCapacity {
		t.Fatalf("drops %d", drops)
	}
	if newQueue(0).capacity != 1 {
		t.Fatal("capacity floor")
	}
}
