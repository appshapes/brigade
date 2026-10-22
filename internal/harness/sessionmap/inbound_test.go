package sessionmap_test

import (
	"testing"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestTightenInbound pins the ordering accept < hold < refuse (card 126):
// a request only tightens, "" and accept are no request, an unknown word
// changes nothing, and the result is always one of the three.
func TestTightenInbound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ decided, request, want string }{
		{"accept", "", "accept"},
		{"accept", "accept", "accept"},
		{"accept", "hold", "hold"},
		{"accept", "refuse", "refuse"},
		{"hold", "", "hold"},
		{"hold", "hold", "hold"},
		{"hold", "refuse", "refuse"},
		{"hold", "accept", "hold"},
		{"refuse", "", "refuse"},
		{"refuse", "hold", "refuse"},
		{"refuse", "refuse", "refuse"},
		{"refuse", "accept", "refuse"},
		{"accept", "auto", "accept"},
		{"hold", "HOLD", "hold"},
		{"", "", "accept"},
		{"", "hold", "hold"},
		{"nonsense", "", "accept"},
	} {
		if got := sessionmap.TightenInbound(tc.decided, tc.request); got != tc.want {
			t.Errorf("TightenInbound(%q, %q) = %q, want %q", tc.decided, tc.request, got, tc.want)
		}
	}
}

// TestInboundRequestOnTheMap: SetInboundRequest and SetInboundDecision
// keep the three members consistent, Decided reads Inbound for a map
// from before the member existed, and such a map gains the member on the
// first request.
func TestInboundRequestOnTheMap(t *testing.T) {
	t.Parallel()
	m := validByPID()
	if m.InboundDecided != "" || m.Decided() != protocol.InboundAccept {
		t.Fatalf("a map without the member: decided %q, Decided() %q", m.InboundDecided, m.Decided())
	}
	m.SetInboundRequest(protocol.InboundHold)
	if m.InboundDecided != protocol.InboundAccept || m.InboundRequest != protocol.InboundHold || m.Inbound != protocol.InboundHold {
		t.Fatalf("after a hold request: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate after the request: %v", err)
	}
	// The hook re-decides refuse: the effective policy is refuse, the
	// request stays recorded.
	m.SetInboundDecision(protocol.InboundRefuse)
	if m.Inbound != protocol.InboundRefuse || m.InboundRequest != protocol.InboundHold {
		t.Fatalf("after a refuse decision: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	// Back to accept: the request holds again.
	m.SetInboundDecision(protocol.InboundAccept)
	if m.Inbound != protocol.InboundHold {
		t.Fatalf("after an accept decision: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	// The request lifted: the decision alone.
	m.SetInboundRequest("")
	if m.Inbound != protocol.InboundAccept || m.InboundRequest != "" || m.InboundDecided != protocol.InboundAccept {
		t.Fatalf("after the lift: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
}

// TestInboundMembersValidate: the decided member is one of the three or
// absent, the request hold, refuse or absent — never accept — and, with
// the decided member present, the effective value is exactly the stronger
// of the two, so a written map can neither loosen a request nor carry one
// the hook did not derive the policy from.
func TestInboundMembersValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		mutate    func(m *sessionmap.ByPID)
		wantField string
	}{
		{"decided accept, no request", func(m *sessionmap.ByPID) { m.InboundDecided = "accept" }, ""},
		{"decided hold, inbound hold", func(m *sessionmap.ByPID) { m.InboundDecided = "hold"; m.Inbound = "hold" }, ""},
		{"decided accept, request hold, inbound hold", func(m *sessionmap.ByPID) { m.InboundDecided = "accept"; m.InboundRequest = "hold"; m.Inbound = "hold" }, ""},
		{"decided refuse, request hold, inbound refuse", func(m *sessionmap.ByPID) {
			m.InboundDecided = "refuse"
			m.InboundRequest = "hold"
			m.Inbound = "refuse"
		}, ""},
		{"no decided member, request hold, inbound hold (a map the command filled in part)", func(m *sessionmap.ByPID) { m.InboundRequest = "hold"; m.Inbound = "hold" }, ""},
		{"decided unknown", func(m *sessionmap.ByPID) { m.InboundDecided = "auto" }, "inbound_decided"},
		{"decided wrongly cased", func(m *sessionmap.ByPID) { m.InboundDecided = "Accept" }, "inbound_decided"},
		{"request accept", func(m *sessionmap.ByPID) { m.InboundDecided = "accept"; m.InboundRequest = "accept" }, "inbound_request"},
		{"request unknown", func(m *sessionmap.ByPID) { m.InboundDecided = "accept"; m.InboundRequest = "quiet" }, "inbound_request"},
		{"a loosened request: decided accept, request hold, inbound accept", func(m *sessionmap.ByPID) {
			m.InboundDecided = "accept"
			m.InboundRequest = "hold"
			m.Inbound = "accept"
		}, "inbound"},
		{"a loosened decision: decided hold, inbound accept", func(m *sessionmap.ByPID) { m.InboundDecided = "hold"; m.Inbound = "accept" }, "inbound"},
		{"a tightened effective nobody asked for: decided accept, inbound refuse", func(m *sessionmap.ByPID) { m.InboundDecided = "accept"; m.Inbound = "refuse" }, "inbound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := validByPID()
			tc.mutate(&m)
			err := m.Validate()
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			perr, ok := err.(*protocol.Error) //nolint:errorlint // the value itself is returned
			if !ok || perr.Code != protocol.CodeConfig || perr.Details["field"] != tc.wantField {
				t.Fatalf("Validate = %v, want config with field %q", err, tc.wantField)
			}
		})
	}
}
