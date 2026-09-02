package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c14Expiry (slow): a session registered with `lease.min_seconds` is,
// min + 2 s later, absent from `session list` and `offline` with
// --include-offline — expiry is authoritative (4.5.8).
func c14Expiry() conformance.Case {
	return conformance.Case{
		ID:    "C-14",
		Rule:  "4.5.8 lease expiry",
		Title: "a session past its lease is absent without --include-offline and offline with it",
		Tags:  []string{conformance.TagCore, conformance.TagSlow},
		Run:   runC14,
	}
}

func runC14(t *conformance.T) {
	a := t.A()
	minLease := t.Describe().Lease.MinSeconds
	record, id := t.Register(a, "c14-"+t.RunID(), func(r *protocol.SessionRegistration) { r.LeaseSeconds = &minLease })
	if got, ok := rawInt(record, "lease_seconds"); !ok || got != minLease {
		t.Errorf("session register: lease_seconds %v, want the requested %d (4.4.2)", record["lease_seconds"], minLease)
	}
	t.Sleep(time.Duration(minLease)*time.Second + 2*time.Second)
	if listHas(t, a, false, id) {
		t.Errorf("session list: the expired session is still listed without --include-offline (4.5.8)")
	}
	sessions, _ := t.List(a, "", true)
	found := false
	for _, s := range sessions {
		if s.SessionID == id {
			found = true
			if s.State != protocol.SessionStateOffline {
				t.Errorf("session list --include-offline: the expired session is %q, want offline (4.5.8)", s.State)
			}
		}
	}
	if !found {
		t.Errorf("session list --include-offline: the expired session is absent (4.4.3)")
	}
}
