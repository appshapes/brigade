package sessionmap_test

import (
	"encoding/json/v2"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// evilMarker is planted in every hostile value so a test can prove the
// value never reaches an error message or its details.
const evilMarker = "EVILMARKER"

func validByPID() sessionmap.ByPID {
	return sessionmap.ByPID{
		ClaudePID:        4242,
		ClaudeSessionID:  "beee3690-1111-4222-8333-444455556666",
		BrigadeSessionID: "6f0f2b41-5a3c-49d7-b8e2-0c7a4f1e6d33",
		TeamRef:          "team-ref-1",
		TeamName:         "ops",
		SessionName:      "payments-api",
		PermissionMode:   "default",
		NonInteractive:   false,
		Inbound:          protocol.InboundAccept,
		SocketPath:       "/tmp/cc-socks/4242.sock",
		Profile:          "default",
		ConfigDir:        "/home/u/.config/brigade",
		AdapterCommand:   []string{"/opt/brigade/adapter-fs", "--root", "/srv/store"},
		PluginBin:        "/home/u/.local/share/brigade/bin/brigade-0.0.0-darwin-arm64",
		HarnessVersion:   "2.1.259",
		RegisteredAt:     time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 9, 2, 12, 0, 5, 0, time.UTC),
	}
}

func asProtocol(t *testing.T, err error) *protocol.Error {
	t.Helper()
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("error is %T (%v), want *protocol.Error", err, err)
	}
	return perr
}

// assertConfig checks the shape every refusal in this package shares — the
// config code, exit 11, the reason detail, no echo of the marker — and
// returns the details for the caller to inspect further.
func assertConfig(t *testing.T, err error, reason string) map[string]string {
	t.Helper()
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeConfig {
		t.Fatalf("code = %q, want config", perr.Code)
	}
	if exit := perr.Code.Exit(); exit != 11 {
		t.Fatalf("exit = %d, want 11", exit)
	}
	if got := perr.Details["reason"]; got != reason {
		t.Fatalf("details.reason = %q, want %q (details %v)", got, reason, perr.Details)
	}
	if strings.Contains(perr.Message, evilMarker) {
		t.Fatalf("message echoes the value: %q", perr.Message)
	}
	for k, v := range perr.Details {
		if strings.Contains(v, evilMarker) {
			t.Fatalf("details[%q] echoes the value: %q", k, v)
		}
	}
	return perr.Details
}

func TestByPIDValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		mutate    func(m *sessionmap.ByPID)
		wantField string // "" means valid
	}{
		{name: "the observed shape is valid", mutate: func(*sessionmap.ByPID) {}},
		{name: "bundled adapter (empty argv) is valid", mutate: func(m *sessionmap.ByPID) { m.AdapterCommand = nil }},
		{name: "empty socket path is valid (a host without an inbox socket)", mutate: func(m *sessionmap.ByPID) { m.SocketPath = "" }},
		{name: "refuse is valid", mutate: func(m *sessionmap.ByPID) { m.Inbound = protocol.InboundRefuse }},
		{name: "empty optional strings are valid", mutate: func(m *sessionmap.ByPID) {
			m.TeamRef, m.TeamName, m.SessionName, m.PermissionMode, m.PluginBin, m.HarnessVersion, m.ClaudeSessionID = "", "", "", "", "", "", ""
		}},
		{name: "pid zero", mutate: func(m *sessionmap.ByPID) { m.ClaudePID = 0 }, wantField: "claude_pid"},
		{name: "pid negative", mutate: func(m *sessionmap.ByPID) { m.ClaudePID = -4242 }, wantField: "claude_pid"},
		{name: "no brigade session id", mutate: func(m *sessionmap.ByPID) { m.BrigadeSessionID = "" }, wantField: "brigade_session_id"},
		{name: "traversing profile", mutate: func(m *sessionmap.ByPID) { m.Profile = "../" + evilMarker }, wantField: "profile"},
		{name: "empty profile", mutate: func(m *sessionmap.ByPID) { m.Profile = "" }, wantField: "profile"},
		{name: "relative config dir", mutate: func(m *sessionmap.ByPID) { m.ConfigDir = "rel/" + evilMarker }, wantField: "config_dir"},
		{name: "empty config dir", mutate: func(m *sessionmap.ByPID) { m.ConfigDir = "" }, wantField: "config_dir"},
		{name: "inbound empty", mutate: func(m *sessionmap.ByPID) { m.Inbound = "" }, wantField: "inbound"},
		{name: "inbound auto (no such shape, D18)", mutate: func(m *sessionmap.ByPID) { m.Inbound = "auto" }, wantField: "inbound"},
		{name: "inbound wrongly cased", mutate: func(m *sessionmap.ByPID) { m.Inbound = "ACCEPT" }, wantField: "inbound"},
		{name: "relative adapter executable", mutate: func(m *sessionmap.ByPID) { m.AdapterCommand = []string{"bin/" + evilMarker} }, wantField: "adapter_command"},
		{name: "empty adapter argv element", mutate: func(m *sessionmap.ByPID) { m.AdapterCommand = []string{"/opt/a", ""} }, wantField: "adapter_command"},
		{name: "relative socket path", mutate: func(m *sessionmap.ByPID) { m.SocketPath = evilMarker + ".sock" }, wantField: "socket_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := validByPID()
			tc.mutate(&m)
			err := m.Validate()
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate: %v, want nil", err)
				}
				return
			}
			details := assertConfig(t, err, sessionmap.ReasonMapInvalid)
			if got := details["field"]; got != tc.wantField {
				t.Fatalf("details.field = %q, want %q", got, tc.wantField)
			}
		})
	}
}

// byPIDMembers is the 3.2 member list, exactly.
var byPIDMembers = []string{
	"claude_pid", "claude_session_id", "brigade_session_id", "team_ref", "team_name", "session_name",
	"permission_mode", "non_interactive", "inbound", "socket_path", "profile", "config_dir", "adapter_command",
	"plugin_bin", "harness_version", "registered_at", "updated_at",
}

func TestByPIDWireShapeIsExactlyThePlanListAndNeverATokenMember(t *testing.T) {
	t.Parallel()
	m := validByPID()
	data, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	got := slices.Sorted(maps.Keys(raw))
	want := slices.Sorted(slices.Values(byPIDMembers))
	if !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	for _, k := range got {
		if strings.Contains(strings.ToLower(k), "token") {
			t.Fatalf("member %q: the map must never carry the messaging token (3.2)", k)
		}
	}
	var back sessionmap.ByPID
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.RegisteredAt.Equal(m.RegisteredAt) || !back.UpdatedAt.Equal(m.UpdatedAt) {
		t.Fatalf("times did not round-trip: %v / %v", back.RegisteredAt, back.UpdatedAt)
	}
	back.RegisteredAt, back.UpdatedAt = m.RegisteredAt, m.UpdatedAt
	if !equalByPID(back, m) {
		t.Fatalf("round trip changed the map:\n got %+v\nwant %+v", back, m)
	}
}

// equalByPID compares two maps field by field, times by Equal (a round
// trip through RFC 3339 keeps the instant, not the monotonic reading).
func equalByPID(a, b sessionmap.ByPID) bool {
	if !a.RegisteredAt.Equal(b.RegisteredAt) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	a.RegisteredAt, b.RegisteredAt = time.Time{}, time.Time{}
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	if !slices.Equal(a.AdapterCommand, b.AdapterCommand) {
		return false
	}
	a.AdapterCommand, b.AdapterCommand = nil, nil
	return reflect.DeepEqual(a, b)
}

func TestCheckAdapterCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		argv []string
		ok   bool
	}{
		{"nil is the bundled adapter", nil, true},
		{"empty is the bundled adapter", []string{}, true},
		{"absolute executable", []string{"/opt/adapter"}, true},
		{"absolute executable with fixed args", []string{"/opt/adapter", "--root", "/x"}, true},
		{"relative executable", []string{"adapter"}, false},
		{"dot-relative executable", []string{"./adapter"}, false},
		{"empty element", []string{"/opt/adapter", ""}, false},
		{"empty executable", []string{""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := sessionmap.CheckAdapterCommand(tc.argv)
			if (err == nil) != tc.ok {
				t.Fatalf("CheckAdapterCommand(%q) = %v, want ok=%v", tc.argv, err, tc.ok)
			}
		})
	}
}

// TestValidateAcceptsEveryPolicy pins D18's value set after P5-9: accept,
// hold and refuse are the three values a map may carry, hold included.
func TestValidateAcceptsEveryPolicy(t *testing.T) {
	t.Parallel()
	for _, v := range []string{protocol.InboundAccept, protocol.InboundHold, protocol.InboundRefuse} {
		m := validByPID()
		m.Inbound = v
		if err := m.Validate(); err != nil {
			t.Errorf("inbound %q: %v", v, err)
		}
	}
}
