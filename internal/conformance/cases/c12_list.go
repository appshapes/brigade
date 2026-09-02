package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c12List: A's and B's `session list` hold only T1 sessions — every
// principal_ref belongs to a principal the suite put into T1 (A, B and the
// extras JoinPrincipal provisioned for earlier cases) and C's fixture
// session never appears —
// with `human_label` on each record and `server_time` on the result; a
// closed session is omitted without --include-offline and listed
// `offline` with it; `--session <A's fixture>` marks exactly that record
// `is_self` (4.2, 4.4.3, 4.5.6). B-1: `session list` with stdin held
// open returns.
func c12List() conformance.Case {
	return conformance.Case{
		ID:    "C-12",
		Rule:  "4.4 session list",
		Title: "session list: only the profile's team, human_label and server_time, offline filtering, is_self with --session; stdin not read",
		Tags:  []string{conformance.TagCore},
		Run:   runC12,
	}
}

func runC12(t *conformance.T) {
	a, b, c := t.A(), t.B(), t.C()
	self := t.Session(a)
	t1 := map[string]bool{}
	for _, ref := range t.T1Principals() {
		t1[ref] = true
	}
	for _, p := range []*conformance.Principal{a, b} {
		sessions, raw := t.List(p, "", false)
		if _, has := raw["server_time"]; !has {
			t.Errorf("%s session list: server_time absent (4.4.3)", p.Name)
		}
		for _, s := range sessions {
			if !t1[s.PrincipalRef] {
				t.Errorf("%s session list: session %s belongs to a principal outside team T1 (4.5.6)", p.Name, s.SessionID)
			}
			if s.SessionID == t.Session(c) {
				t.Errorf("%s session list: C's fixture session (team T2) is listed (4.5.6, C-12)", p.Name)
			}
			if s.HumanLabel == "" {
				t.Errorf("%s session list: session %s has no human_label (C-12)", p.Name, s.SessionID)
			}
		}
	}

	_, closed := t.Register(a, "c12-closed-"+t.RunID(), nil)
	t.Close(a, closed)
	if listHas(t, a, false, closed) {
		t.Errorf("session list: a closed session is listed without --include-offline (4.4.3)")
	}
	sessions, _ := t.List(a, "", true)
	found := false
	for _, s := range sessions {
		if s.SessionID == closed {
			found = true
			if s.State != protocol.SessionStateOffline {
				t.Errorf("session list --include-offline: the closed session is %q, want offline (4.5.8)", s.State)
			}
		}
	}
	if !found {
		t.Errorf("session list --include-offline: the closed session is absent (4.4.3)")
	}

	sessions, _ = t.List(a, self, false)
	selfSeen := false
	for _, s := range sessions {
		switch {
		case s.SessionID == self:
			selfSeen = true
			if !s.IsSelf {
				t.Errorf("session list --session: the named session has is_self false (4.4.3)")
			}
		case s.IsSelf:
			t.Errorf("session list --session: session %s has is_self true but was not named (4.4.3)", s.SessionID)
		}
	}
	if !selfSeen {
		t.Errorf("session list --session: A's fixture session is absent")
	}

	// B-1: a command that takes no input must not read stdin.
	t.OK(t.ExecStdinOpen(a, "session", "list"), nil)
}
