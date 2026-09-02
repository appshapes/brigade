package cases

import (
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c10Register: `session register` on A answers a SessionRecord with an
// adapter-assigned session_id, `state active` for `activity busy`,
// `lease_until` within ±5 s of `server_time + lease.default_seconds`,
// `resumed false` and `lease_seconds == lease.default_seconds` (4.2,
// 4.4.2, 4.5.8).
func c10Register() conformance.Case {
	return conformance.Case{
		ID:    "C-10",
		Rule:  "4.2 session register",
		Title: "session register: adapter-assigned id, active, lease_until ≈ server_time + default lease, resumed false",
		Tags:  []string{conformance.TagCore},
		Run:   runC10,
	}
}

func runC10(t *conformance.T) {
	record, id := t.Register(t.A(), "c10-"+t.RunID(), nil)
	if id == "" {
		t.Errorf("session register: session_id is empty (4.4.3)")
	}
	if state, _ := record["state"].(string); state != protocol.SessionStateActive {
		t.Errorf("session register: state %q, want active for activity busy (4.5.8)", state)
	}
	if resumed, ok := record["resumed"].(bool); !ok || resumed {
		t.Errorf("session register: resumed %v, want false (4.4.2)", record["resumed"])
	}
	want := t.Describe().Lease.DefaultSeconds
	if got, ok := rawInt(record, "lease_seconds"); !ok || got != want {
		t.Errorf("session register: lease_seconds %v, want lease.default_seconds %d (4.4.2)", record["lease_seconds"], want)
	}
	serverTime, ok1 := rawTime(t, record, "server_time")
	leaseUntil, ok2 := rawTime(t, record, "lease_until")
	if ok1 && ok2 {
		expected := serverTime.Add(time.Duration(want) * time.Second)
		if diff := leaseUntil.Sub(expected); diff < -5*time.Second || diff > 5*time.Second {
			t.Errorf("session register: lease_until is %s from server_time + %d s; want within ±5 s (4.5.8)", diff, want)
		}
	}
}

// rawInt reads an integral JSON number from a loosely parsed object.
func rawInt(m map[string]any, key string) (int, bool) {
	f, ok := m[key].(float64)
	if !ok || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

// rawTime reads an RFC 3339 timestamp member from a loosely parsed
// object, recording a failure when it is absent or malformed.
func rawTime(t *conformance.T, m map[string]any, key string) (time.Time, bool) {
	s, ok := m[key].(string)
	if !ok {
		t.Errorf("result: %s is absent or not a string (JSON convention 7)", key)
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Errorf("result: %s is not an RFC 3339 timestamp (JSON convention 7)", key)
		return time.Time{}, false
	}
	return ts, true
}
