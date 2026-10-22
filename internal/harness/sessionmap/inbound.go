package sessionmap

import "github.com/appshapes/brigade/internal/protocol"

// inboundRank orders the three policies by strength: accept, then hold,
// then refuse. An unknown word ranks as accept, the weakest, so that
// TightenInbound of a malformed request never loosens anything and never
// yields a word outside the three.
func inboundRank(p string) int {
	switch p {
	case protocol.InboundRefuse:
		return 2
	case protocol.InboundHold:
		return 1
	}
	return 0
}

// TightenInbound is the effective policy of card 126: the stronger of the
// hook's decision and the session's runtime request, accept < hold <
// refuse. A request of "" (none), accept or an unknown word leaves the
// decision as it is; a decision that is not one of the three reads as
// accept, which Validate refuses before any reader sees it.
func TightenInbound(decided, request string) string {
	if inboundRank(request) > inboundRank(decided) {
		return request
	}
	switch decided {
	case protocol.InboundHold, protocol.InboundRefuse:
		return decided
	}
	return protocol.InboundAccept
}

// Decided is the hook's decision — the ceiling — reading Inbound itself
// for a map written before InboundDecided existed (which carried the
// decision alone, no request being possible then).
func (m *ByPID) Decided() string {
	if m.InboundDecided != "" {
		return m.InboundDecided
	}
	return m.Inbound
}

// SetInboundDecision records the hook's decision and derives the
// effective policy from it and the request in force: what SessionStart
// does when it builds the map and the prompt hook at every re-decision.
func (m *ByPID) SetInboundDecision(decided string) {
	m.InboundDecided = decided
	m.Inbound = TightenInbound(decided, m.InboundRequest)
}

// SetInboundRequest records a runtime request — hold, refuse, or "" to
// lift it — and derives the effective policy: what `brigade inbound`
// does. A map without a decided member gains one here, from Inbound, so
// the file it writes is consistent under Validate.
func (m *ByPID) SetInboundRequest(request string) {
	m.InboundDecided = m.Decided()
	m.InboundRequest = request
	m.Inbound = TightenInbound(m.InboundDecided, request)
}
