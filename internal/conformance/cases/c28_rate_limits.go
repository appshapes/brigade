package cases

import (
	"fmt"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c28RateLimits: the five send budgets of 4.5.12 — the recipient-wide
// unacknowledged cap (`recipient_inbox_full`), the per-pair cap
// (`sender_quota_for_recipient`), the ORDER of those two (the per-pair cap
// is checked before the recipient-wide one, asserted with both caps at
// their limit at once), the per-session minute window (`send_per_minute`)
// and the per-principal minute window (`principal_per_minute`), each
// answered with `rate_limited`, exit 8, `retryable true` and
// `retry_after_ms > 0`. ACK-FREE by design, so mutant_noack does not fail
// it: every budget is reached by fresh sessions of three extra principals
// (P1, P2, P3) sending to fresh sessions of A and B, and nothing is ever
// acknowledged. The per-hour windows are not reachable within the suite's
// budget and are recorded as a Note.
func c28RateLimits() conformance.Case {
	return conformance.Case{
		ID:    "C-28",
		Rule:  "4.5.12 rate limits",
		Title: "recipient inbox, per-pair, per-session and per-principal budgets each refuse with rate_limited",
		Tags:  []string{conformance.TagCore},
		Run:   runC28,
	}
}

func runC28(t *conformance.T) {
	lim := t.Describe().Limits
	pair, inbox := lim.MaxUnackedPerSenderRecipient, lim.MaxUnackedPerRecipient
	perMinute, principalPerMinute := lim.SendRate.PerMinute, lim.PrincipalSendRate.PerMinute
	// The schedule below (15 + 1 + 20 + 20 + 4 = 60 accepted sends from P2,
	// 15 from P3, three sessions × 15 from P1) is arithmetic on the frozen
	// v1 limits of 4.4.1; an adapter advertising other numbers cannot be
	// scheduled blind.
	if inbox > principalPerMinute || pair > perMinute || perMinute/2 > pair || pair >= inbox {
		t.Fatalf("C-28's schedule assumes the frozen v1 limits (4.4.1): got max_unacked_per_recipient %d, max_unacked_per_sender_recipient %d, send_rate.per_minute %d, principal_send_rate.per_minute %d",
			inbox, pair, perMinute, principalPerMinute)
	}

	p1 := t.JoinPrincipal("p1")
	p2 := t.JoinPrincipal("p2")
	p3 := t.JoinPrincipal("p3")
	a, b := t.A(), t.B()
	_, r0 := t.Register(b, nameFor(t, "C-28", "r0"), nil)

	// (a) P3's T0 puts exactly the per-pair cap (15) on R0 first; P1 then
	// fills the rest of R0's inbox with as many fresh sessions as the
	// per-pair cap requires (three × 15 = 45), so both unacknowledged caps
	// sit at their limit at once. T0's next send must be refused for the
	// PER-PAIR cap: 4.5.12 checks it before the recipient-wide cap so that
	// one sender cannot exhaust a recipient's inbox for everyone else, and
	// an adapter checking them in the other order answers
	// recipient_inbox_full here (measured: without this arm that mutant
	// passed). P2's fresh Q1, with nothing on R0 and every window to spare,
	// is then refused for the inbox alone.
	_, t0 := t.Register(p3, nameFor(t, "C-28", "t0"), nil)
	for j := 0; j < pair; j++ {
		t.Send(p3, t0, r0, "pair-first", nil)
	}
	filled := pair
	for i := 1; filled < inbox; i++ {
		_, s := t.Register(p1, nameFor(t, "C-28", fmt.Sprintf("s%d", i)), nil)
		for j := 0; j < pair && filled < inbox; j++ {
			t.Send(p1, s, r0, "fill", nil)
			filled++
		}
	}
	refusedSend(t, p3, t0, r0, "sender_quota_for_recipient")
	_, q1 := t.Register(p2, nameFor(t, "C-28", "q1"), nil)
	refusedSend(t, p2, q1, r0, "recipient_inbox_full")

	// (b) Q1 → X: the per-pair cap, then the 16th is refused while a third
	// session (Q2) can still reach X.
	_, x := t.Register(a, nameFor(t, "C-28", "x"), nil)
	for i := 0; i < pair; i++ {
		t.Send(p2, q1, x, "pair", nil)
	}
	refusedSend(t, p2, q1, x, "sender_quota_for_recipient")
	_, q2 := t.Register(p2, nameFor(t, "C-28", "q2"), nil)
	t.Send(p2, q2, x, "third-session", nil)
	accepted := pair + 1 // P2's accepted sends so far

	// (c) Q3 spends its per-minute window over two recipients (10 + 10), so
	// neither unacknowledged cap can trip first; the 21st is refused.
	_, y := t.Register(a, nameFor(t, "C-28", "y"), nil)
	_, z := t.Register(a, nameFor(t, "C-28", "z"), nil)
	_, q3 := t.Register(p2, nameFor(t, "C-28", "q3"), nil)
	sendSplit(t, p2, q3, y, z, perMinute)
	accepted += perMinute
	refusedSend(t, p2, q3, y, "send_per_minute")

	// (d) Q4 spends another full window over Y and Z; Q5 tops P2 up to its
	// principal budget; Q6's first send is refused although Q6's own window
	// is untouched. Y ends at 10 + 10 + 4 and Z at 10 + 10, under both caps.
	_, q4 := t.Register(p2, nameFor(t, "C-28", "q4"), nil)
	sendSplit(t, p2, q4, y, z, perMinute)
	accepted += perMinute
	remaining := principalPerMinute - accepted
	if remaining <= 0 || remaining > pair || remaining > perMinute {
		t.Fatalf("C-28's schedule leaves %d sends for Q5; want between 1 and %d", remaining, min(pair, perMinute))
	}
	_, q5 := t.Register(p2, nameFor(t, "C-28", "q5"), nil)
	for i := 0; i < remaining; i++ {
		t.Send(p2, q5, y, "top-up", nil)
	}
	_, q6 := t.Register(p2, nameFor(t, "C-28", "q6"), nil)
	refusedSend(t, p2, q6, y, "principal_per_minute")

	t.Note("send_per_hour and principal_per_hour are not assertable within the suite's budget (%d and %d sends)",
		lim.SendRate.PerHour, lim.PrincipalSendRate.PerHour)
}

// sendSplit sends n messages from sender, the first half to y and the rest
// to z, all accepted.
func sendSplit(t *conformance.T, p *conformance.Principal, sender, y, z string, n int) {
	half := n / 2
	for i := 0; i < n; i++ {
		recipient := y
		if i >= half {
			recipient = z
		}
		t.Send(p, sender, recipient, fmt.Sprintf("split-%d", i), nil)
	}
}

// refusedSend sends one message and asserts the rate_limited refusal of
// 4.5.12: exit 8, `retryable` present and true (through Fail), the given
// details.reason and retry_after_ms > 0.
func refusedSend(t *conformance.T, p *conformance.Principal, sender, recipient, reason string) {
	r := t.SendRaw(p, mustMarshal(t, protocol.SendRequest{SenderSessionID: sender, RecipientSessionID: recipient, Body: "one more"}))
	e := t.Fail(r, protocol.CodeRateLimited)
	if got := e.Details["reason"]; got != reason {
		t.Errorf("message send: want details.reason %q, got %q (4.5.12)", reason, got)
	}
	if e.RetryAfterMS <= 0 {
		t.Errorf("message send (%s): retry_after_ms is %d, want > 0 (4.5.12)", reason, e.RetryAfterMS)
	}
}
