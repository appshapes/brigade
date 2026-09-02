// Package cases holds the conformance cases of plan 9.2, one file per
// case, coded against the T API of internal/conformance. All returns them
// in id order; the binary and the suite tests run them from there.
package cases

import "github.com/appshapes/brigade/internal/conformance"

// All returns every case in id order: C-01, C-02, C-03, C-03b, C-04, …,
// C-19, C-19b, …, C-29, C-29b, …, C-43.
func All() []conformance.Case {
	return []conformance.Case{
		c01Describe(),
		c02Usage(),
		c03TeamCreate(),
		c03bProfileBound(),
		c04TeamJoin(),
		c05Secrets(),
		c06Unbound(),
		c07ProfileState(),
		c08Leave(),
		c10Register(),
		c11Names(),
		c12List(),
		c13Heartbeat(),
		c14Expiry(),
		c15Close(),
		c16Caps(),
		c17Unknown(),
		c18Describe(),
		c19Resume(),
		c19bResumeLive(),
		c20Send(),
		c21Idempotency(),
		c22IdempotencyConflict(),
		c23Forbidden(),
		c24SenderOwnership(),
		c25RecipientIsolation(),
		c26TeamIsolation(),
		c27Sizes(),
		c28RateLimits(),
		c29Hops(),
		c29bImplicitHops(),
		c30Ack(),
		c31Closed(),
		c32Order(),
		c33WatchReady(),
		c34Catchup(),
		c35Live(),
		c36Redelivery(),
		c37WatchForeign(),
		c38WatchExit(),
		c39OneLine(),
		c40Paging(),
		c41StdinCommands(),
		c42Inbound(),
		c43Roster(),
	}
}
