package cases

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c42Inbound (cap session.inbound): `inbound` set at registration is
// stored and returned by `session list`, and a heartbeat changes it (4.4.2,
// 4.4.3, 4.4.4, 4.7).
func c42Inbound() conformance.Case {
	return conformance.Case{
		ID:    "C-42",
		Rule:  "4.4.2 inbound policy",
		Title: "inbound hold at registration and refuse by heartbeat are returned by session list",
		Tags:  []string{conformance.TagCore, conformance.TagCap("session.inbound")},
		Run:   runC42,
	}
}

func runC42(t *conformance.T) {
	a := t.A()
	rec, s := t.Register(a, nameFor(t, "C-42", "hold"), func(reg *protocol.SessionRegistration) {
		reg.Inbound = protocol.InboundHold
	})
	if got, _ := rec["inbound"].(string); got != protocol.InboundHold {
		t.Errorf("session register: inbound %q, want hold (4.4.2)", got)
	}
	sessions, _ := t.List(a, "", false)
	if got := recordOf(t, sessions, s).Inbound; got != protocol.InboundHold {
		t.Errorf("session list: inbound %q, want hold (4.4.3)", got)
	}

	refuse := protocol.InboundRefuse
	t.Heartbeat(a, s, &protocol.HeartbeatRequest{Inbound: &refuse})
	sessions, _ = t.List(a, "", false)
	if got := recordOf(t, sessions, s).Inbound; got != protocol.InboundRefuse {
		t.Errorf("session list after heartbeat: inbound %q, want refuse (4.4.4)", got)
	}
}
