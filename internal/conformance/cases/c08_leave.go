package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c08Leave: on a scratch pair (D creates, E joins; D is fixture A when the
// adapter lacks team.create), E's `team leave` answers {team_ref,
// principal_ref, left true}; a watch E was running on its session S emits
// an `error` `unauthorized` and exits 5 (4.5.7: revocation applies on
// every verb that touches the team); `describe` is `not_member`; E's
// `session list` and `message receive` are `config` (exit 11); D no
// longer lists S even with --include-offline; D's send to S is the
// uniform `not_found`, byte-identical to a send to a random id (4.5.6);
// a second `team leave` is idempotent; `team join` with the secret
// rejoins with the same principal_ref; D then lists E's new session
// (4.2, 4.4.10).
func c08Leave() conformance.Case {
	return conformance.Case{
		ID:    "C-08",
		Rule:  "4.2 team leave and rejoin",
		Title: "team leave revokes membership everywhere (watch, list, send) and is idempotent; team join rejoins with the same principal_ref",
		Tags:  []string{conformance.TagCore, conformance.TagCap("team.join")},
		Run:   runC08,
	}
}

func runC08(t *conformance.T) {
	var d *conformance.Principal
	var secret string
	if t.HasCap("team.create") {
		d = t.Scratch("d")
		secret = createTeam(t, d, "c08-"+t.RunID(), "c08-d@example.com").JoinSecret
	} else {
		secret = t.JoinSecret()
		if secret == "" {
			t.Skip("needs team.create or a known join secret to provision the pair")
		}
		d = t.A()
	}
	e := t.Scratch("e")
	joined := joinTeam(t, e, secret, "c08-e@example.com")
	if joined.Rejoined {
		t.Errorf("team join: rejoined true on a first join, want false (4.4.10)")
	}

	_, sd := t.Register(d, "c08-d-"+t.RunID(), nil)
	_, s := t.Register(e, "c08-e-"+t.RunID(), nil)
	w := t.Watch(e, s)
	w.Expect(protocol.EventReady, t.PushDeadline())
	if !listHas(t, d, true, s) {
		t.Errorf("D session list: E's session is absent before the leave")
	}

	var left protocol.TeamLeaveResult
	t.OK(t.Exec(e, nil, "team", "leave"), &left)
	if left.TeamRef != joined.TeamRef || left.PrincipalRef != joined.PrincipalRef || !left.Left {
		t.Errorf("team leave: {team_ref, principal_ref, left} = {match %v, match %v, %v}; want the join's refs and true (4.4.10)",
			left.TeamRef == joined.TeamRef, left.PrincipalRef == joined.PrincipalRef, left.Left)
	}

	// The running watch: an unauthorized error event, retryable false and
	// present, then exit 5 within 2 s of it (4.5.7, 4.4.9). The event's own
	// deadline is the spec's push budget — 4.4.9's 5 s for anything accepted
	// while the watch runs — and not a tighter number of the suite's: the
	// leave is sent the instant `ready` is seen, and an adapter whose
	// realtime join is still completing at that moment (the websocket dial
	// and channel join through a gateway on a loaded CI runner) cannot
	// receive the revocation as a push and learns it from the drain that
	// follows the join. Measured: 2.24 s to the error event on a GitHub
	// runner (run 33678110011, the Supabase adapter) against a 2 s deadline
	// that passed in 0.5-1 s locally.
	ev := w.Expect(protocol.EventError, t.PushDeadline())
	if ev.Error != nil && ev.Error.Error.Code != protocol.CodeUnauthorized {
		t.Errorf("watch after team leave: error event code %s, want unauthorized (4.5.7)", ev.Error.Error.Code)
	}
	errObj, _ := ev.Raw["error"].(map[string]any)
	if rv, ok := errObj["retryable"]; !ok {
		t.Errorf("watch after team leave: error event lacks retryable (B-2)")
	} else if rb, isBool := rv.(bool); !isBool || rb {
		t.Errorf("watch after team leave: error.retryable %v, want false (4.6)", rv)
	}
	if exit, ok := w.Wait(2 * time.Second); !ok {
		t.Errorf("watch after team leave: still running 2 s after the error event; want exit 5 (4.4.9)")
	} else if exit != protocol.CodeUnauthorized.Exit() {
		t.Errorf("watch after team leave: exit %d, want %d (4.4.9)", exit, protocol.CodeUnauthorized.Exit())
	}

	if st := describe(t, e, false).Profile.State; st != protocol.ProfileStateNotMember {
		t.Errorf("describe after team leave: profile.state %q, want not_member (4.4.1)", st)
	}
	t.Fail(t.Exec(e, nil, "session", "list"), protocol.CodeConfig)
	t.Fail(t.Exec(e, nil, "message", "receive", "--session", s), protocol.CodeConfig)
	if listHas(t, d, true, s) {
		t.Errorf("D session list --include-offline still shows E's session after the leave (4.2, 4.5.6)")
	}

	// D cannot message the revoked member's session: the uniform not_found.
	toS := t.Exec(d, mustMarshal(t, &protocol.SendRequest{SenderSessionID: sd, RecipientSessionID: s, Body: "c08"}), "message", "send")
	t.Fail(toS, protocol.CodeNotFound)
	toRandom := t.Exec(d, mustMarshal(t, &protocol.SendRequest{SenderSessionID: sd, RecipientSessionID: randomRef(), Body: "c08"}), "message", "send")
	t.Fail(toRandom, protocol.CodeNotFound)
	t.SameBytes("send to a revoked member's session vs a random id (4.5.6)", toS, toRandom)
	_, closed := t.Register(d, "c08-d-closed-"+t.RunID(), nil)
	t.Close(d, closed)
	fromClosed := t.Exec(d, mustMarshal(t, &protocol.SendRequest{SenderSessionID: closed, RecipientSessionID: sd, Body: "c08"}), "message", "send")
	t.Fail(fromClosed, protocol.CodeConflict)
	t.DifferentBytes("not_found vs conflict (positive control)", toS, fromClosed)

	var again protocol.TeamLeaveResult
	r := t.Exec(e, nil, "team", "leave")
	t.OK(r, &again)
	if !again.Left {
		t.Errorf("second team leave: left %v, want true (idempotent, 4.4.10)", again.Left)
	}

	rejoined := joinTeam(t, e, secret, "c08-e@example.com")
	if !rejoined.Rejoined {
		t.Errorf("team join after leave: rejoined false, want true (4.4.10)")
	}
	if rejoined.PrincipalRef != joined.PrincipalRef {
		t.Errorf("team join after leave: principal_ref changed; want the same membership (4.4.10)")
	}
	_, s2 := t.Register(e, "c08-e2-"+t.RunID(), nil)
	if !listHas(t, d, false, s2) {
		t.Errorf("D session list: E's new session after the rejoin is absent (4.2)")
	}
}

// listHas reports whether p's `session list` (with --include-offline when
// asked) contains the session id.
func listHas(t *conformance.T, p *conformance.Principal, includeOffline bool, id string) bool {
	sessions, _ := t.List(p, "", includeOffline)
	for _, s := range sessions {
		if s.SessionID == id {
			return true
		}
	}
	return false
}
