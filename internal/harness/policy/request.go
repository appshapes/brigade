package policy

import "github.com/appshapes/brigade/internal/harness/sessionmap"

// Tighten is the effective policy of card 126 as a Policy: the stronger
// of the hook's decision and a runtime request (sessionmap.TightenInbound
// holds the ordering, accept < hold < refuse, beside the map members it
// governs). A request of "" is none; one that is not hold or refuse
// changes nothing.
func Tighten(decided Policy, request string) Policy {
	return Policy(sessionmap.TightenInbound(decided.String(), request))
}

// ValidRequest reports whether s is a runtime request a session may
// record: hold or refuse. accept is not a request but the lifting of one,
// and `brigade inbound accept` clears the member instead.
func ValidRequest(s string) bool { return s == Hold.String() || s == Refuse.String() }
