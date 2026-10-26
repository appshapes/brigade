package webhook

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/protocol"
)

// fakeBackend is an in-memory Backend: one inbox for the gateway, a
// roster, and a record of every Send and Ack.
type fakeBackend struct {
	mu        sync.Mutex
	nextID    int
	sessions  []protocol.SessionRecord
	inbox     []protocol.MessageEnvelope
	sent      []protocol.SendRequest
	keys      map[string]*protocol.SendResponse
	acked     []string
	registers []*protocol.SessionRegistration
	closed    []string
	// failures injected per verb, consumed one per call.
	registerErr  []error
	heartbeatErr []error
	sendErr      []error
}

func newFake() *fakeBackend {
	return &fakeBackend{keys: map[string]*protocol.SendResponse{}}
}

func perr(code protocol.Code) error { return &protocol.Error{Code: code, Message: string(code)} }

func pop(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	e := (*errs)[0]
	*errs = (*errs)[1:]
	return e
}

func (f *fakeBackend) Describe(context.Context) (*protocol.DescribeResult, error) {
	return &protocol.DescribeResult{Lease: protocol.Lease{MinSeconds: 30, MaxSeconds: 600, DefaultSeconds: 90}}, nil
}

func (f *fakeBackend) Register(_ context.Context, reg *protocol.SessionRegistration) (*adapterclient.RegisterResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers = append(f.registers, reg)
	if err := pop(&f.registerErr); err != nil {
		return nil, err
	}
	f.nextID++
	id := "gw-session-" + strconv.Itoa(f.nextID)
	if reg.Resume != nil {
		id = reg.Resume.SessionID
	}
	return &adapterclient.RegisterResult{SessionRecord: protocol.SessionRecord{SessionID: id}}, nil
}

func (f *fakeBackend) Heartbeat(context.Context, string, *protocol.HeartbeatRequest) (*protocol.HeartbeatResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := pop(&f.heartbeatErr); err != nil {
		return nil, err
	}
	return &protocol.HeartbeatResult{}, nil
}

func (f *fakeBackend) Close(_ context.Context, id string) (*adapterclient.CloseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	return &adapterclient.CloseResult{SessionID: id, State: protocol.SessionStateOffline}, nil
}

func (f *fakeBackend) ListSessions(_ context.Context, includeOffline bool) (*adapterclient.ListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []protocol.SessionRecord
	for _, s := range f.sessions {
		if includeOffline || s.State != protocol.SessionStateOffline {
			out = append(out, s)
		}
	}
	return &adapterclient.ListResult{Sessions: out}, nil
}

func (f *fakeBackend) Send(_ context.Context, req *protocol.SendRequest) (*protocol.SendResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := pop(&f.sendErr); err != nil {
		return nil, err
	}
	if prev, ok := f.keys[req.IdempotencyKey]; ok {
		dup := *prev
		dup.Duplicate = true
		return &dup, nil
	}
	f.sent = append(f.sent, *req)
	f.nextID++
	res := &protocol.SendResponse{MessageID: "msg-" + strconv.Itoa(f.nextID), RecipientSessionID: req.RecipientSessionID}
	f.keys[req.IdempotencyKey] = res
	return res, nil
}

func (f *fakeBackend) Receive(context.Context, string, int) (*adapterclient.ReceiveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &adapterclient.ReceiveResult{Messages: append([]protocol.MessageEnvelope(nil), f.inbox...)}, nil
}

func (f *fakeBackend) Ack(_ context.Context, _ string, req *protocol.AckRequest) (*protocol.AckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, req.MessageIDs...)
	keep := f.inbox[:0]
	for _, m := range f.inbox {
		if !contains(req.MessageIDs, m.MessageID) {
			keep = append(keep, m)
		}
	}
	f.inbox = keep
	return &protocol.AckResult{}, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (f *fakeBackend) deliver(m protocol.MessageEnvelope) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbox = append(f.inbox, m)
}

func (f *fakeBackend) snapshot() (sent []protocol.SendRequest, acked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.SendRequest(nil), f.sent...), append([]string(nil), f.acked...)
}

// clock is a settable time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var testSecret = []byte("0123456789abcdef0123456789abcdef-test")

func message(id, from string) protocol.MessageEnvelope {
	return protocol.MessageEnvelope{
		MessageID: id, Body: "hello from " + from, Summary: "hi",
		Sender: protocol.Sender{
			SessionID: from, SessionName: "claude-main", PrincipalRef: "11111111-aaaa-bbbb-cccc-000000000001",
			HumanLabel: "alice@example.com",
		},
		CreatedAt: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
	}
}

// startGateway builds a started gateway over fb that POSTs to target.
func startGateway(t *testing.T, fb *fakeBackend, target string, clk *clock, statePath string) *Gateway {
	t.Helper()
	g, err := New(Config{
		TargetURL: target, Secret: testSecret, StatePath: statePath, MaxAttempts: 3, Now: clk.Now,
	}, fb)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return g
}
