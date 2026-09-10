package supabase

import (
	"encoding/json/v2"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// The `session *` verbs against the fake backend (P2-8): the arguments
// each RPC is called with, the shape of each result, and every local
// refusal that must never reach the network.

// testSessionID is a session id the fake backend issues.
const testSessionID = "cccccccc-dddd-4eee-8fff-000000000001"

// sessionRecordJSON is what brigade.session_record() answers, with the
// three members register_session adds.
func sessionRecordJSON(id, name, state string) string {
	return `{"session_id":"` + id + `","session_name":"` + name + `","session_description":null,` +
		`"principal_ref":"` + testUserID + `","human_label":"alice@example.com","state":"` + state + `",` +
		`"activity":"busy","inbound":"accept","last_seen_at":"2026-09-02T12:00:00.5+00:00",` +
		`"lease_until":"2026-09-02T12:01:30.5+00:00","harness":"brigade-conformance","harness_version":"0.0.0",` +
		`"workspace_label":null,"created_at":"2026-09-02T12:00:00.5+00:00"}`
}

// registerJSON is register_session's answer for testSessionID.
func registerJSON(name string, resumed bool) string {
	rec := sessionRecordJSON(testSessionID, name, protocol.SessionStateActive)
	tail := `,"resumed":false,"lease_seconds":90,"server_time":"2026-09-02T12:00:00.5+00:00"}`
	if resumed {
		tail = `,"resumed":true,"lease_seconds":90,"server_time":"2026-09-02T12:00:00.5+00:00"}`
	}
	return rec[:len(rec)-1] + tail
}

// registration is the standard stdin document of `session register`.
func registration(name string) string {
	return `{"harness":"brigade-conformance","harness_version":"0.0.0","session_name":"` + name +
		`","activity":"busy","inbound":"accept"}`
}

// captureRPC records the function name and arguments of every RPC and
// answers each with the body reply returns for it.
func (be *fakeBackend) captureRPC(reply func(fn string) (int, string)) *[]map[string]any {
	seen := &[]map[string]any{}
	be.onRPC = func(w http.ResponseWriter, _ *http.Request, fn, _ string, args map[string]any) {
		args["__fn"] = fn
		be.mu.Lock()
		*seen = append(*seen, args)
		be.mu.Unlock()
		status, body := reply(fn)
		writeJSON(w, status, body)
	}
	return seen
}

// only answers 200 with body for every RPC.
func only(body string) func(string) (int, string) {
	return func(string) (int, string) { return http.StatusOK, body }
}

// arg reads one recorded RPC argument.
func arg(t *testing.T, args map[string]any, name string) any {
	t.Helper()
	v, ok := args[name]
	if !ok {
		t.Fatalf("the RPC was called without %q (args %v)", name, args)
	}
	return v
}

func TestSessionRegisterArgumentsAndResult(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(registerJSON("s-1", false)))
	if got := r.exec(registration("s-1"), "session", "register"); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d RPCs, want exactly one", len(*seen))
	}
	args := (*seen)[0]
	if args["__fn"] != "register_session" {
		t.Fatalf("RPC %v, want register_session", args["__fn"])
	}
	for name, want := range map[string]any{
		"p_team_id":         testTeamID,
		"p_name":            "s-1",
		"p_activity":        "busy",
		"p_inbound":         "accept",
		"p_harness":         "brigade-conformance",
		"p_harness_version": "0.0.0",
		"p_lease_seconds":   float64(protocol.LeaseDefaultSeconds),
	} {
		if got := arg(t, args, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if got := arg(t, args, "p_description"); got != nil {
		t.Errorf("p_description = %v, want null", got)
	}
	if got := arg(t, args, "p_workspace_label"); got != nil {
		t.Errorf("p_workspace_label = %v, want null", got)
	}
	// model and context_used_tokens are the APPENDED parameters of
	// migration 20260910193200 and are omitted when absent (compat.go): a
	// call that does not name them matches the older signature on a
	// backend without the migration, which a registration must.
	for _, name := range sessionAppendedParams {
		if got, present := args[name]; present {
			t.Errorf("%s = %v was named on a registration with no value for it", name, got)
		}
	}
	if _, present := args["p_resume_session_id"]; present {
		t.Errorf("p_resume_session_id was sent for a registration with no resume")
	}
}

// The register result is one flat object: a valid SessionRecord plus
// resumed, lease_seconds and server_time (4.4.2), with is_self present.
func TestSessionRegisterResultShape(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.be.captureRPC(only(registerJSON("s-1", true)))
	got := r.exec(registration("s-1"), "session", "register")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	env := decode(t, got.stdout)
	result, _ := env["result"].(map[string]any)
	if resumed, _ := result["resumed"].(bool); !resumed {
		t.Errorf("resumed = %v, want true", result["resumed"])
	}
	if seconds, _ := result["lease_seconds"].(float64); seconds != 90 {
		t.Errorf("lease_seconds = %v, want 90", result["lease_seconds"])
	}
	if _, has := result["server_time"]; !has {
		t.Errorf("server_time is absent (4.4.2)")
	}
	isSelf, has := result["is_self"].(bool)
	if !has || isSelf {
		t.Errorf("is_self = %v, want a present false (4.4.3)", result["is_self"])
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var rec protocol.SessionRecord
	if err := protocol.Decode(raw, &rec); err != nil {
		t.Fatalf("the register result does not validate as a SessionRecord: %v", err)
	}
	if rec.SessionID != testSessionID || rec.State != protocol.SessionStateActive {
		t.Errorf("record = %+v", rec)
	}
}

// A lease outside THIS adapter's advertised range is invalid_input
// naming the member, and never reaches the backend (4.4.2).
func TestSessionRegisterLeaseRange(t *testing.T) {
	t.Parallel()
	for _, seconds := range []int{protocol.LeaseMinSeconds - 1, protocol.LeaseMaxSeconds + 1} {
		r := newRig(t)
		r.joined()
		doc := `{"harness":"h","harness_version":"1","session_name":"s","activity":"busy","inbound":"accept","lease_seconds":` +
			strconv.Itoa(seconds) + `}`
		got := r.fails("invalid_input", 3, doc, "session", "register")
		if details(t, got.stdout)["field"] != "lease_seconds" {
			t.Errorf("details = %v, want field lease_seconds", details(t, got.stdout))
		}
		if r.be.total() != 0 {
			t.Errorf("the backend was called for a lease outside the advertised range")
		}
	}
}

// A requested lease inside the range is sent verbatim, and so are the
// optional members — model with its [1m] suffix and context_used_tokens as
// a number (C-44).
func TestSessionRegisterLeaseHonoured(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(registerJSON("s", false)))
	doc := `{"harness":"h","harness_version":"1","session_name":"s","activity":"idle","inbound":"hold",` +
		`"session_description":"d","workspace_label":"w","lease_seconds":30,` +
		`"model":"claude-opus-5[1m]","context_used_tokens":189681}`
	if got := r.exec(doc, "session", "register"); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	args := (*seen)[0]
	if got := arg(t, args, "p_lease_seconds"); got != float64(30) {
		t.Errorf("p_lease_seconds = %v, want 30", got)
	}
	if got := arg(t, args, "p_description"); got != "d" {
		t.Errorf("p_description = %v, want d", got)
	}
	if got := arg(t, args, "p_workspace_label"); got != "w" {
		t.Errorf("p_workspace_label = %v, want w", got)
	}
	if got := arg(t, args, "p_inbound"); got != "hold" {
		t.Errorf("p_inbound = %v, want hold", got)
	}
	if got := arg(t, args, "p_model"); got != "claude-opus-5[1m]" {
		t.Errorf("p_model = %v, want claude-opus-5[1m]", got)
	}
	if got := arg(t, args, "p_context_used_tokens"); got != float64(189681) {
		t.Errorf("p_context_used_tokens = %v, want 189681", got)
	}
}

// model over max_model_chars and a negative context_used_tokens are
// invalid_input naming the member BEFORE any dial (C-44): the protocol's
// Validate runs in readInput, and the RPC's own raise is never reached.
func TestSessionRegisterModelAndTokensCaps(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	for _, tc := range []struct{ field, doc string }{
		{"model", `{"harness":"h","harness_version":"1","session_name":"s","activity":"idle","inbound":"hold","model":"` +
			strings.Repeat("é", protocol.MaxModelChars+1) + `"}`},
		{"context_used_tokens", `{"harness":"h","harness_version":"1","session_name":"s","activity":"idle","inbound":"hold","context_used_tokens":-1}`},
	} {
		got := r.fails("invalid_input", 3, tc.doc, "session", "register")
		if details(t, got.stdout)["field"] != tc.field {
			t.Errorf("details = %v, want field %s", details(t, got.stdout), tc.field)
		}
	}
	if r.be.total() != 0 {
		t.Errorf("the backend was called for a member the protocol refuses")
	}
}

// A resume id that cannot name a session is the uniform not_found before
// any dial, byte-identical to the backend's own not_found (C-19).
func TestSessionRegisterResumeNotAUUID(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	doc := `{"harness":"h","harness_version":"1","session_name":"s","activity":"busy","inbound":"accept",` +
		`"resume":{"session_id":"0123456789abcdef0123456789abcdef"}}`
	got := r.fails("not_found", 6, doc, "session", "register")
	if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("not_found envelope differs from the uniform one: %s", got.stdout)
	}
	if r.be.total() != 0 {
		t.Fatalf("the backend was called for a resume id that cannot name a session")
	}
}

// A uuid-shaped resume id is sent as p_resume_session_id, and the
// backend's PT404 for a foreign one is the SAME envelope as the local
// refusal above (4.5.7).
func TestSessionRegisterResumeForeign(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(func(string) (int, string) {
		return http.StatusNotFound, `{"code":"PT404","details":null,"hint":null,"message":"brigade:not_found"}`
	})
	doc := `{"harness":"h","harness_version":"1","session_name":"s","activity":"busy","inbound":"accept",` +
		`"resume":{"session_id":"` + testSessionID + `"}}`
	got := r.fails("not_found", 6, doc, "session", "register")
	if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("a foreign resume differs from the uniform not_found: %s", got.stdout)
	}
	if got := arg(t, (*seen)[0], "p_resume_session_id"); got != testSessionID {
		t.Errorf("p_resume_session_id = %v", got)
	}
}

// register_session's brigade: raises map to the codes of 4.6 with their
// reasons and fields, and the server's own text never reaches stdout.
func TestSessionRegisterBackendRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raise, code string
		exit              int
		detail, value     string
	}{
		{"live", "brigade:conflict:session_live", "conflict", 7, "reason", reasonSessionLive},
		{"harness cap", "brigade:invalid_input:harness", "invalid_input", 3, "field", "harness"},
		{"revoked", "brigade:unauthorized", "unauthorized", 5, "", ""},
		{"budget", "brigade:rate_limited:register_session:3600", "rate_limited", 8, "reason", "register_session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.joined()
			status := http.StatusInternalServerError
			r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
				postgrest(w, status, "P0001", tc.raise)
			}
			got := r.fails(tc.code, tc.exit, registration("s"), "session", "register")
			if tc.detail != "" && details(t, got.stdout)[tc.detail] != tc.value {
				t.Errorf("details = %v, want %s %s", details(t, got.stdout), tc.detail, tc.value)
			}
			if contains(got.stdout, "brigade:") {
				t.Errorf("the server's raise text reached stdout: %s", got.stdout)
			}
		})
	}
}

func TestSessionHeartbeat(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`{"session_id":"` + testSessionID + `","state":"active",` +
		`"lease_until":"2026-09-02T12:01:30+00:00","server_time":"2026-09-02T12:00:00+00:00"}`))
	got := r.exec(`{"activity":"idle","lease_seconds":120}`, "session", "heartbeat", "--session", testSessionID)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	args := (*seen)[0]
	if args["__fn"] != "session_heartbeat" {
		t.Fatalf("RPC %v", args["__fn"])
	}
	if v := arg(t, args, "p_activity"); v != "idle" {
		t.Errorf("p_activity = %v", v)
	}
	if v := arg(t, args, "p_lease_seconds"); v != float64(120) {
		t.Errorf("p_lease_seconds = %v", v)
	}
	// An absent member means unchanged, which is JSON null for the RPC's
	// coalesce() arguments (4.4.4). model and context_used_tokens are the
	// appended parameters of migration 20260910193200 and are OMITTED when
	// absent instead (compat.go: the call then matches the older signature
	// too); the RPC's default is null, so a heartbeat never clears them
	// either way (C-44).
	for _, name := range []string{"p_name", "p_description", "p_inbound"} {
		if v := arg(t, args, name); v != nil {
			t.Errorf("%s = %v, want null for an absent member", name, v)
		}
	}
	for _, name := range sessionAppendedParams {
		if v, present := args[name]; present {
			t.Errorf("%s = %v was named on a heartbeat with no value for it", name, v)
		}
	}
	var res protocol.HeartbeatResult
	env := decode(t, got.stdout)
	raw, err := json.Marshal(env["result"])
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Decode(raw, &res); err != nil {
		t.Fatalf("the heartbeat result does not validate: %v", err)
	}
}

// A heartbeat that carries model and context_used_tokens names them for
// the RPC verbatim (C-44); zero is a value, not absence.
func TestSessionHeartbeatModelAndTokens(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`{"session_id":"` + testSessionID + `","state":"idle",` +
		`"lease_until":"2026-09-02T12:01:30+00:00","server_time":"2026-09-02T12:00:00+00:00"}`))
	got := r.exec(`{"model":"claude-sonnet-5","context_used_tokens":0}`, "session", "heartbeat", "--session", testSessionID)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	args := (*seen)[0]
	if v := arg(t, args, "p_model"); v != "claude-sonnet-5" {
		t.Errorf("p_model = %v, want claude-sonnet-5", v)
	}
	if v := arg(t, args, "p_context_used_tokens"); v != float64(0) {
		t.Errorf("p_context_used_tokens = %v, want 0", v)
	}
	// Over the cap: invalid_input naming the member, no dial (C-44).
	e := r.fails("invalid_input", 3, `{"model":"`+strings.Repeat("m", protocol.MaxModelChars+1)+`"}`, "session", "heartbeat", "--session", testSessionID)
	if details(t, e.stdout)["field"] != "model" {
		t.Errorf("details = %v, want field model", details(t, e.stdout))
	}
	if len(*seen) != 1 {
		t.Errorf("%d RPCs, want the one accepted heartbeat only", len(*seen))
	}
}

// A missing --session is `usage` (argv, before the ladder); an id that is
// not uuid-shaped is the uniform not_found AFTER it (C-06 requires the
// ladder to answer first on an unconfigured profile).
func TestSessionHeartbeatSessionFlag(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.fails("usage", 2, `{}`, "session", "heartbeat")
	got := r.fails("not_found", 6, `{}`, "session", "heartbeat", "--session", "not-a-uuid")
	if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("envelope differs from the uniform not_found: %s", got.stdout)
	}
	if r.be.total() != 0 {
		t.Fatalf("the backend was called for an id that cannot name a session")
	}
	// The stdin document is validated before the id shape.
	e := r.fails("invalid_input", 3, `{"activity":"sleeping"}`, "session", "heartbeat", "--session", "not-a-uuid")
	if details(t, e.stdout)["field"] != "activity" {
		t.Errorf("details = %v, want field activity", details(t, e.stdout))
	}
}

// A closed session is `conflict` with the backend's reason; a foreign one
// is the uniform not_found (C-13, C-15).
func TestSessionHeartbeatBackendRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusInternalServerError, "P0001", "brigade:conflict:session_closed")
	}
	got := r.fails("conflict", 7, `{}`, "session", "heartbeat", "--session", testSessionID)
	if details(t, got.stdout)["reason"] != "session_closed" {
		t.Errorf("details = %v, want reason session_closed", details(t, got.stdout))
	}
}

func TestSessionList(t *testing.T) {
	t.Parallel()
	other := "cccccccc-dddd-4eee-8fff-000000000002"
	body := `{"team_ref":"` + testTeamID + `","team_name":"ops","server_time":"2026-09-02T12:00:00+00:00",` +
		`"truncated":false,"sessions":[` + sessionRecordJSON(testSessionID, "mine", "active") + `,` +
		sessionRecordJSON(other, "theirs", "idle") + `]}`
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(body))
	result := r.ok("session", "list", "--session", testSessionID, "--include-offline")

	args := (*seen)[0]
	if args["__fn"] != "list_sessions" {
		t.Fatalf("RPC %v", args["__fn"])
	}
	if v := arg(t, args, "p_team_id"); v != testTeamID {
		t.Errorf("p_team_id = %v", v)
	}
	if v := arg(t, args, "p_include_offline"); v != true {
		t.Errorf("p_include_offline = %v, want true", v)
	}
	if result["team_name"] != "ops" {
		t.Errorf("team_name = %v", result["team_name"])
	}
	if result["truncated"] != false {
		t.Errorf("truncated = %v", result["truncated"])
	}
	sessions, _ := result["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("%d sessions, want 2", len(sessions))
	}
	// is_self is the adapter's, computed from --session (C-12).
	for _, s := range sessions {
		rec, _ := s.(map[string]any)
		want := rec["session_id"] == testSessionID
		if got, _ := rec["is_self"].(bool); got != want {
			t.Errorf("session %v: is_self %v, want %v", rec["session_id"], got, want)
		}
	}
}

// Without --include-offline the RPC is asked for live sessions only, and
// an empty answer is an EMPTY ARRAY on the wire, never null (JSON
// convention 3).
func TestSessionListEmptyAndDefaults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`{"team_ref":"` + testTeamID + `","team_name":"ops",` +
		`"server_time":"2026-09-02T12:00:00+00:00","truncated":false,"sessions":[]}`))
	got := r.exec("", "session", "list")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if !contains(got.stdout, `"sessions":[]`) {
		t.Errorf("an empty session list is not [] on the wire: %s", got.stdout)
	}
	if v := arg(t, (*seen)[0], "p_include_offline"); v != false {
		t.Errorf("p_include_offline = %v, want false", v)
	}
}

func TestSessionClose(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(`{"session_id":"` + testSessionID + `","state":"offline"}`))
	first := r.exec("", "session", "close", "--session", testSessionID)
	if first.code != 0 {
		t.Fatalf("exit %d: %s", first.code, first.stdout)
	}
	second := r.exec("", "session", "close", "--session", testSessionID)
	if first.stdout != second.stdout {
		t.Errorf("close is not idempotent on the wire:\n%s\n%s", first.stdout, second.stdout)
	}
	args := (*seen)[0]
	if args["__fn"] != "close_session" {
		t.Fatalf("RPC %v", args["__fn"])
	}
	if v := arg(t, args, "p_session_id"); v != testSessionID {
		t.Errorf("p_session_id = %v", v)
	}
	if !contains(first.stdout, `"state":"offline"`) {
		t.Errorf("close result = %s", first.stdout)
	}
}

func TestSessionCloseRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.fails("usage", 2, "", "session", "close")
	got := r.fails("not_found", 6, "", "session", "close", "--session", "0123456789abcdef0123456789abcdef")
	if errorJSON(t, got.stdout) != errorJSON(t, newFailure(t, errNotFound())) {
		t.Fatalf("envelope differs from the uniform not_found: %s", got.stdout)
	}
	if r.be.total() != 0 {
		t.Fatalf("the backend was called for an id that cannot name a session")
	}
}

func TestSessionUnknownVerb(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	r.fails("usage", 2, "", "session", "frobnicate")
}

// A rejected JWT earns exactly one forced refresh and one retry inside
// c.rpc; the verb never sees it (5.1).
func TestSessionListRetriesOnceAfterPGRST303(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	calls := 0
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		calls++
		if calls == 1 {
			postgrest(w, http.StatusUnauthorized, "PGRST303", "JWT expired")
			return
		}
		writeJSON(w, http.StatusOK, `{"team_ref":"`+testTeamID+`","team_name":"ops",`+
			`"server_time":"2026-09-02T12:00:00+00:00","truncated":false,"sessions":[]}`)
	}
	if got := r.exec("", "session", "list"); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if calls != 2 {
		t.Errorf("%d RPC calls, want two (one refusal, one retry)", calls)
	}
	if r.be.calls("/auth/v1/token") != 1 {
		t.Errorf("%d refreshes, want exactly one", r.be.calls("/auth/v1/token"))
	}
}
