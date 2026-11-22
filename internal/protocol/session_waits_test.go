package protocol

import (
	"slices"
	"testing"
)

// TestWaitsNamesWhyAMessageWaits: the one rule `brigade send` and the
// webhook gateway both tell their caller with (work item 408). A message
// waits for a session that is offline, and for one that holds or refuses
// team messages; each reason is the roster's own word, the state first.
// Whether the session is busy or idle makes nothing wait.
func TestWaitsNamesWhyAMessageWaits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state, activity, inbound string
		want                     []string
	}{
		{SessionStateActive, ActivityBusy, InboundAccept, nil},
		{SessionStateIdle, ActivityIdle, InboundAccept, nil},
		{SessionStateActive, ActivityBusy, "", nil},
		{SessionStateOffline, ActivityIdle, InboundAccept, []string{"offline"}},
		{SessionStateActive, ActivityBusy, InboundHold, []string{"hold"}},
		{SessionStateIdle, ActivityIdle, InboundRefuse, []string{"refuse"}},
		{SessionStateOffline, ActivityIdle, InboundHold, []string{"offline", "hold"}},
		{SessionStateOffline, ActivityIdle, InboundRefuse, []string{"offline", "refuse"}},
	} {
		s := &SessionRecord{State: tc.state, Activity: tc.activity, Inbound: tc.inbound}
		if got := s.Waits(); !slices.Equal(got, tc.want) {
			t.Errorf("state %q, inbound %q: Waits() = %v, want %v", tc.state, tc.inbound, got, tc.want)
		}
	}
}
