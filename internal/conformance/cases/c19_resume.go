package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c19Resume: after A closes session S and B sends to it, A's `session
// register` with `resume.session_id S` re-opens it in place — same id,
// `resumed true`, state not offline, the message still pending — while
// B's resume of S and A's resume of a random id are both `not_found`
// with byte-identical stdout (ownership, 4.4.2, 4.5.7, 4.5.8). The
// positive control is A's resume of a live session (`conflict`).
func c19Resume() conformance.Case {
	return conformance.Case{
		ID:    "C-19",
		Rule:  "4.5.8 resume",
		Title: "resume re-opens a closed owned session with its pending messages; resume of a foreign or unknown id ≡ not_found",
		Tags:  []string{conformance.TagCore, conformance.TagCap("session.resume")},
		Run:   runC19,
	}
}

func runC19(t *conformance.T) {
	a, b := t.A(), t.B()
	name := "c19-s-" + t.RunID()
	_, s := t.Register(a, name, nil)
	t.Close(a, s)
	_, sb := t.Register(b, "c19-b-"+t.RunID(), nil)
	sent := t.Send(b, sb, s, "c19: for the closed session", nil)

	resume := func(r *protocol.SessionRegistration) { r.Resume = &protocol.ResumeRef{SessionID: s} }
	record, id := t.Register(a, name, resume)
	if id != s {
		t.Errorf("session register with resume: session_id %s, want %s (4.5.8)", id, s)
	}
	if resumed, _ := record["resumed"].(bool); !resumed {
		t.Errorf("session register with resume: resumed false, want true (4.4.2)")
	}
	if state, _ := record["state"].(string); state == protocol.SessionStateOffline {
		t.Errorf("session register with resume: state offline; want the session re-opened (4.5.8)")
	}
	messages := t.Receive(a, s, 0)
	found := false
	for _, m := range messages {
		if m.MessageID == sent.MessageID {
			found = true
		}
	}
	if !found {
		t.Errorf("message receive after resume: the message sent while closed is missing; pending messages must be kept (4.5.8)")
	}

	byB := t.Exec(b, registrationWith(t, "c19-steal-"+t.RunID(), resume), "session", "register")
	t.Fail(byB, protocol.CodeNotFound)
	random := randomRef()
	unknown := t.Exec(a, registrationWith(t, "c19-unknown-"+t.RunID(), func(r *protocol.SessionRegistration) {
		r.Resume = &protocol.ResumeRef{SessionID: random}
	}), "session", "register")
	t.Fail(unknown, protocol.CodeNotFound)
	t.SameBytes("B resuming A's session vs A resuming a random id (4.5.7)", byB, unknown)

	live := t.Exec(a, registrationWith(t, name, resume), "session", "register")
	t.Fail(live, protocol.CodeConflict)
	t.DifferentBytes("not_found vs conflict (positive control)", byB, live)
}

// c19bResumeLive: a resume of an open session with a valid lease is
// `conflict` `session_live` and leaves the session unchanged; after
// `session close` the same resume succeeds with `resumed true` (4.2,
// 4.4.2, 4.5.8). The lease-expiry arm of the plan row needs --slow,
// which the T API does not expose to a case, and is noted rather than run.
func c19bResumeLive() conformance.Case {
	return conformance.Case{
		ID:    "C-19b",
		Rule:  "4.5.8 resume of a live session",
		Title: "resume of a live session → conflict session_live with no change; after close the same resume succeeds",
		Tags:  []string{conformance.TagCore, conformance.TagCap("session.resume")},
		Run:   runC19b,
	}
}

func runC19b(t *conformance.T) {
	a := t.A()
	name := "c19b-" + t.RunID()
	record, s := t.Register(a, name, nil)
	resume := func(r *protocol.SessionRegistration) { r.Resume = &protocol.ResumeRef{SessionID: s} }
	e := t.Fail(t.Exec(a, registrationWith(t, name+"-renamed", resume), "session", "register"), protocol.CodeConflict)
	if e.Details["reason"] != "session_live" {
		t.Errorf("resume of a live session: details.reason %q, want session_live (4.5.8)", e.Details["reason"])
	}
	sessions, _ := t.List(a, "", false)
	found := false
	for _, rec := range sessions {
		if rec.SessionID != s {
			continue
		}
		found = true
		if rec.SessionName != name {
			t.Errorf("session list after the refused resume: session_name changed (4.5.8)")
		}
		if want, ok := rawTime(t, record, "lease_until"); ok && !rec.LeaseUntil.Equal(want) {
			t.Errorf("session list after the refused resume: lease_until changed (4.5.8)")
		}
		if want, ok := rawTime(t, record, "created_at"); ok && !rec.CreatedAt.Equal(want) {
			t.Errorf("session list after the refused resume: created_at changed (4.5.8)")
		}
	}
	if !found {
		t.Errorf("session list after the refused resume: the session is absent")
	}

	t.Close(a, s)
	record, id := t.Register(a, name, resume)
	if id != s {
		t.Errorf("resume after close: session_id %s, want %s (4.5.8)", id, s)
	}
	if resumed, _ := record["resumed"].(bool); !resumed {
		t.Errorf("resume after close: resumed false, want true (4.4.2)")
	}
	if !t.Slow() {
		t.Note("lease-expiry arm (register with lease.min_seconds, wait, resume) runs only with --slow")
		return
	}
	// Slow arm: a session whose lease has expired is resumable like a
	// closed one (4.5.8: offline covers both).
	minLease := t.Describe().Lease.MinSeconds
	_, expired := t.Register(a, name+"-expiring", func(r *protocol.SessionRegistration) { r.LeaseSeconds = &minLease })
	t.Sleep(time.Duration(minLease)*time.Second + 2*time.Second)
	record, id = t.Register(a, name+"-expiring", func(r *protocol.SessionRegistration) { r.Resume = &protocol.ResumeRef{SessionID: expired} })
	if id != expired {
		t.Errorf("resume after lease expiry: session_id %s, want %s (4.5.8)", id, expired)
	}
	if resumed, _ := record["resumed"].(bool); !resumed {
		t.Errorf("resume after lease expiry: resumed false, want true (4.4.2)")
	}
}
