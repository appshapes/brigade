package cases

import (
	"fmt"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c40Paging: every message accepted while no watch runs is emitted after
// the watch starts, past any receive page (4.5.2, 4.4.9). An extra principal
// P registers as many sessions as the per-pair cap requires and fills a
// fresh B session to max_unacked_per_recipient (60 > the 50 default receive
// page); the watch then emits all 60 ids within 10 s, any order, duplicates
// tolerated. ACK-FREE. Plan correction: 9.2 says 100, which no conforming
// adapter can accept — the frozen recipient cap is 60.
func c40Paging() conformance.Case {
	return conformance.Case{
		ID:    "C-40",
		Rule:  "4.5.2 catch-up paging",
		Title: "60 messages sent while no watch runs are all emitted after the watch starts",
		Tags:  []string{conformance.TagCore},
		Run:   runC40,
	}
}

func runC40(t *conformance.T) {
	lim := t.Describe().Limits
	pair, inbox := lim.MaxUnackedPerSenderRecipient, lim.MaxUnackedPerRecipient
	if inbox > lim.PrincipalSendRate.PerMinute || pair > lim.SendRate.PerMinute {
		t.Fatalf("C-40's schedule assumes the frozen v1 limits (4.4.1): got max_unacked_per_recipient %d, max_unacked_per_sender_recipient %d, send_rate.per_minute %d, principal_send_rate.per_minute %d",
			inbox, pair, lim.SendRate.PerMinute, lim.PrincipalSendRate.PerMinute)
	}
	p := t.JoinPrincipal("p40")
	b := t.B()
	_, watched := t.Register(b, nameFor(t, "C-40", "watched"), nil)

	want := map[string]bool{}
	for i := 1; len(want) < inbox; i++ {
		_, s := t.Register(p, nameFor(t, "C-40", fmt.Sprintf("s%d", i)), nil)
		for j := 0; j < pair && len(want) < inbox; j++ {
			want[t.Send(p, s, watched, fmt.Sprintf("page me %d/%d", i, j), nil).MessageID] = true
		}
	}

	w := t.Watch(b, watched)
	w.Expect(protocol.EventReady, t.PushDeadline())
	if missing := collectMessageIDs(w, want, 10*time.Second); len(missing) > 0 {
		t.Errorf("message watch: %d of %d pending messages were not emitted within 10 s (4.5.2)", len(missing), inbox)
	}
}
