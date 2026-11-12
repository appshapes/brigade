//go:build !windows

package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// only answers requests to path with a canned answer and leaves the rest
// to the stub.
func only(path string, status int, body string) func(*seenRequest) *canned {
	return func(r *seenRequest) *canned {
		if r.Path != path {
			return nil
		}
		return &canned{status: status, body: body}
	}
}

// okEnvelope wraps a result as the server would.
func okEnvelope(result string) string {
	return `{"ok":true,"protocol_version":"1","result":` + result + `}`
}

// emptyList is a valid `session list` result.
const emptyList = `{"team_ref":"t","team_name":"ops","server_time":"2026-11-12T10:00:00Z","sessions":[],"truncated":false}`

// TestAValidEnvelopeIsTheAnswerWhateverTheStatus is the first row of the
// contract's section 1: the body decides, never the status.
func TestAValidEnvelopeIsTheAnswerWhateverTheStatus(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)

	for _, status := range []int{http.StatusOK, http.StatusInternalServerError, http.StatusNotFound, http.StatusTeapot} {
		b.answerWith(only(pathSessionList, status, okEnvelope(emptyList)))
		if got := p.alice.ok("", "session", "list"); got["team_name"] != "ops" {
			t.Errorf("status %d with an ok envelope: result %v, want the envelope's", status, got)
		}
	}

	failing := `{"ok":false,"protocol_version":"1","error":{"code":"conflict","message":"that session is closed","retryable":false,"details":{"reason":"session_closed"}}}`
	for _, status := range []int{http.StatusOK, http.StatusConflict, http.StatusBadGateway} {
		b.answerWith(only(pathSessionList, status, failing))
		got := p.alice.fails(protocol.CodeConflict, "", "session", "list")
		if details(t, got.stdout)["reason"] != "session_closed" || !strings.Contains(got.stdout, "that session is closed") {
			t.Errorf("status %d with a failing envelope: %s, want the server's own error", status, got.stdout)
		}
	}

	limited := `{"ok":false,"protocol_version":"1","error":{"code":"rate_limited","message":"slow down","retryable":true,"retry_after_ms":2500,"details":{"reason":"send_per_minute"}}}`
	b.answerWith(only(pathSessionList, http.StatusTooManyRequests, limited))
	got := p.alice.fails(protocol.CodeRateLimited, "", "session", "list")
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	if object["retry_after_ms"] != float64(2500) {
		t.Errorf("rate_limited: retry_after_ms %v, want the server's 2500", object["retry_after_ms"])
	}
}

// TestAnAnswerThatIsNotAnEnvelope is the second row: `unavailable` for a
// 5xx, `internal` otherwise, and nothing of the body on stdout or stderr.
func TestAnAnswerThatIsNotAnEnvelope(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	const marker = "RAW-SERVER-TEXT"
	for _, tc := range []struct {
		status int
		body   string
		want   protocol.Code
		reason string
	}{
		{http.StatusBadGateway, "<html>" + marker + "</html>", protocol.CodeUnavailable, reasonBackendError},
		{http.StatusServiceUnavailable, "", protocol.CodeUnavailable, reasonBackendError},
		{http.StatusInternalServerError, `{"message":"` + marker + `"}`, protocol.CodeUnavailable, reasonBackendError},
		{http.StatusGatewayTimeout, marker, protocol.CodeUnavailable, reasonBackendError},
		{http.StatusNotFound, marker, protocol.CodeInternal, reasonBackendResponse},
		{http.StatusBadRequest, `{"error":"` + marker + `"}`, protocol.CodeInternal, reasonBackendResponse},
		{http.StatusUnauthorized, marker, protocol.CodeInternal, reasonBackendResponse},
		{http.StatusOK, "<html>" + marker + "</html>", protocol.CodeInternal, reasonBackendResponse},
		{http.StatusOK, "", protocol.CodeInternal, reasonBackendResponse},
		{http.StatusNoContent, "", protocol.CodeInternal, reasonBackendResponse},
	} {
		b.answerWith(only(pathSessionList, tc.status, tc.body))
		got := p.alice.fails(tc.want, "", "session", "list")
		if details(t, got.stdout)["reason"] != tc.reason {
			t.Errorf("status %d: details %v, want reason %s", tc.status, details(t, got.stdout), tc.reason)
		}
		if strings.Contains(got.stdout, marker) {
			t.Errorf("status %d: the server's raw text reached stdout: %s", tc.status, got.stdout)
		}
	}
}

// TestATransportFailureIsUnavailable is the third row: a request that
// fails before any answer (refused here, and the dial BRIGADE_TEST_OFFLINE
// refuses) is `unavailable`, retryable.
func TestATransportFailureIsUnavailable(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)

	b.forget()
	got := p.alice.execEnv([]string{offlineVar + "=1"}, "", "session", "list")
	if got.code != protocol.CodeUnavailable.Exit() {
		t.Fatalf("offline: exit %d, want unavailable (%s)", got.code, got.stdout)
	}
	assertErrorCode(t, got.stdout, protocol.CodeUnavailable)
	if details(t, got.stdout)["reason"] != reasonOffline {
		t.Errorf("offline: details %v, want reason offline", details(t, got.stdout))
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("offline: the server saw %d requests, want none", n)
	}

	b.srv.Close()
	got = p.alice.fails(protocol.CodeUnavailable, "", "session", "list")
	if details(t, got.stdout)["reason"] != reasonUnreachable {
		t.Errorf("refused: details %v, want reason unreachable", details(t, got.stdout))
	}
}

// TestAMalformedResultIsInternal: a valid envelope whose result is not the
// command's shape is `internal`, for every command that prints a result.
func TestAMalformedResultIsInternal(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	const record = `"session_id":"s","session_name":"n","principal_ref":"p","state":"active","activity":"busy","inbound":"accept",` +
		`"last_seen_at":"2026-11-12T10:00:00Z","lease_until":"2026-11-12T10:01:30Z","created_at":"2026-11-12T10:00:00Z","is_self":false`
	for _, tc := range []struct {
		name   string
		path   string
		result string
		input  string
		args   []string
	}{
		{"register with no session_id", pathSessionRegister, `{"session_name":"x"}`, registration("x"), []string{"session", "register"}},
		{"register with no lease_seconds", pathSessionRegister, `{` + record + `,"resumed":false,"server_time":"2026-11-12T10:00:00Z"}`, registration("x"), []string{"session", "register"}},
		{"register with a state outside the enumeration", pathSessionRegister, `{` + strings.Replace(record, `"active"`, `"busy"`, 1) + `,"resumed":false,"lease_seconds":90,"server_time":"2026-11-12T10:00:00Z"}`, registration("x"), []string{"session", "register"}},
		{"list with a record that is not one", pathSessionList, `{"team_ref":"t","team_name":"ops","server_time":"2026-11-12T10:00:00Z","sessions":[{"session_id":"s"}],"truncated":false}`, "", []string{"session", "list"}},
		{"list with no team_ref", pathSessionList, `{"team_name":"ops","server_time":"2026-11-12T10:00:00Z","sessions":[]}`, "", []string{"session", "list"}},
		{"heartbeat with no lease_until", pathSessionHeartbeat, `{"session_id":"s","state":"active","server_time":"2026-11-12T10:00:00Z"}`, `{}`, []string{"session", "heartbeat", "--session", p.aliceSes}},
		{"close that is not offline", pathSessionClose, `{"session_id":"s","state":"active"}`, "", []string{"session", "close", "--session", p.aliceSes}},
		{"send that says delivered", pathMessageSend, `{"status":"delivered","message_id":"m","recipient_session_id":"r","created_at":"2026-11-12T10:00:00Z","duplicate":false,"hop_count":0}`,
			`{"sender_session_id":"` + p.aliceSes + `","recipient_session_id":"` + p.bobSes + `","body":"x"}`, []string{"message", "send"}},
		{"receive with no cursor", pathMessageReceive, `{"messages":[]}`, "", []string{"message", "receive", "--session", p.aliceSes}},
		{"receive with a message that is not one", pathMessageReceive, `{"messages":[{"message_id":"m"}],"cursor":"c"}`, "", []string{"message", "receive", "--session", p.aliceSes}},
		{"receive with a cursor over 256 characters", pathMessageReceive, `{"messages":[],"cursor":"` + strings.Repeat("c", 257) + `"}`, "", []string{"message", "receive", "--session", p.aliceSes}},
		{"ack with an empty id", pathMessageAck, `{"acked":[""],"unknown":[]}`, `{"message_ids":["m"]}`, []string{"message", "ack", "--session", p.aliceSes}},
		{"members with a member that has no principal_ref", pathTeamMembers, `{"team_ref":"t","team_name":"ops","server_time":"2026-11-12T10:00:00Z","members":[{"status":"active","joined_at":"2026-11-12T10:00:00Z","session_count":0}]}`, "", []string{"team", "members"}},
		{"leave that did not leave", pathTeamLeave, `{"team_ref":"t","principal_ref":"p","left":false}`, "", []string{"team", "leave"}},
	} {
		b.answerWith(only(tc.path, http.StatusOK, okEnvelope(tc.result)))
		got := p.alice.fails(protocol.CodeInternal, tc.input, tc.args...)
		if details(t, got.stdout)["reason"] != reasonBackendResponse {
			t.Errorf("%s: details %v, want reason %s", tc.name, details(t, got.stdout), reasonBackendResponse)
		}
	}
	// The refused leave left the binding alone.
	b.answerWith(nil)
	if state := p.alice.state(); state != protocol.ProfileStateJoined {
		t.Errorf("after a malformed leave result: state %q, want joined", state)
	}
}

// TestAResultIsPrintedInItsOwnShape: what reaches stdout is the typed
// result re-encoded. A member the shape does not know, the receive's
// cursor and an absent required array do not survive as the server wrote
// them (JSON conventions 2 and 3).
func TestAResultIsPrintedInItsOwnShape(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)

	b.answerWith(only(pathSessionList, http.StatusOK, okEnvelope(strings.Replace(emptyList, `"sessions":[],`, `"smuggled":"EXTRA-MEMBER",`, 1))))
	got := p.alice.exec("", "session", "list")
	if got.code != 0 || strings.Contains(got.stdout, "EXTRA-MEMBER") || !strings.Contains(got.stdout, `"sessions":[]`) {
		t.Errorf("session list: exit %d stdout %s; want no unknown member and sessions as []", got.code, got.stdout)
	}

	b.answerWith(only(pathMessageReceive, http.StatusOK, okEnvelope(`{"cursor":"CURSOR-VALUE"}`)))
	got = p.alice.exec("", "message", "receive", "--session", p.aliceSes)
	if got.code != 0 || strings.Contains(got.stdout, "CURSOR-VALUE") || !strings.Contains(got.stdout, `"messages":[]`) {
		t.Errorf("message receive: exit %d stdout %s; want BAP/1's result, messages as [] and no cursor", got.code, got.stdout)
	}

	b.answerWith(only(pathMessageAck, http.StatusOK, okEnvelope(`{}`)))
	got = p.alice.exec(`{"message_ids":["m"]}`, "message", "ack", "--session", p.aliceSes)
	if got.code != 0 || !strings.Contains(got.stdout, `"acked":[]`) || !strings.Contains(got.stdout, `"unknown":[]`) {
		t.Errorf("message ack: exit %d stdout %s; want both arrays present", got.code, got.stdout)
	}
}

// TestARedirectIsNotFollowed: a 3xx is an answer that is not an envelope,
// and the credential is never carried to the place it points at.
func TestARedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)

	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		b.answerWith(func(r *seenRequest) *canned {
			if r.Path != pathSessionList {
				return nil
			}
			return &canned{status: status, header: map[string]string{"Location": other.URL + pathSessionList}}
		})
		p.alice.fails(protocol.CodeInternal, "", "session", "list")
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the adapter followed a redirect %d times, want never", n)
	}
}

// TestEveryRequestIsTheContracts pins the request of each command against
// the table of the contract's section 2: a POST of one JSON object with
// the credential as a bearer token, whose body is the command's input and
// what BAP/1 passes beside it.
func TestEveryRequestIsTheContracts(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	teamRef := str(t, p.alice.ok("", "session", "list"), "team_ref")
	bearer := "Bearer " + p.alice.storedCredential()

	// last runs a command for alice and returns the one request it made.
	last := func(path, input string, args ...string) seenRequest {
		t.Helper()
		b.forget()
		if got := p.alice.exec(input, args...); got.code != 0 {
			t.Fatalf("%v: exit %d (%s)", args, got.code, got.stdout)
		}
		seen := b.seen("")
		if len(seen) != 1 || seen[0].Path != path {
			t.Fatalf("%v made %d requests (%v), want exactly one to %s", args, len(seen), seen, path)
		}
		r := seen[0]
		if r.Authorization != bearer {
			t.Errorf("%s: the request does not carry the credential as a bearer token", path)
		}
		if r.ContentType != "application/json" || !strings.HasPrefix(r.UserAgent, progName+"/") {
			t.Errorf("%s: Content-Type %q User-Agent %q", path, r.ContentType, r.UserAgent)
		}
		return r
	}
	// members asserts a body holds exactly these members.
	members := func(r seenRequest, want ...string) {
		t.Helper()
		if len(r.Body) != len(want) {
			t.Errorf("%s: body %s, want exactly the members %v", r.Path, r.Raw, want)
		}
		for _, name := range want {
			if _, present := r.Body[name]; !present {
				t.Errorf("%s: body %s lacks %q", r.Path, r.Raw, name)
			}
		}
	}

	r := last(pathSessionRegister, strings.Replace(registration("shape"), `{`, `{"unknown_member":1,"lease_seconds":null,`, 1), "session", "register")
	members(r, "harness", "harness_version", "session_name", "activity", "inbound", "team_ref")
	if r.Body["team_ref"] != teamRef {
		t.Errorf("session register: team_ref %v, want the profile's %s", r.Body["team_ref"], teamRef)
	}

	r = last(pathSessionList, "", "session", "list")
	members(r, "team_ref")
	r = last(pathSessionList, "", "session", "list", "--session", p.aliceSes, "--include-offline")
	members(r, "team_ref", "session_id", "include_offline")
	if r.Body["session_id"] != p.aliceSes || r.Body["include_offline"] != true {
		t.Errorf("session list: body %s", r.Raw)
	}

	r = last(pathSessionHeartbeat, `{"activity":"idle","session_description":"","nope":1}`, "session", "heartbeat", "--session", p.aliceSes)
	members(r, "session_id", "heartbeat")
	heartbeat, _ := r.Body["heartbeat"].(map[string]any)
	if len(heartbeat) != 2 || heartbeat["activity"] != "idle" || heartbeat["session_description"] != "" {
		t.Errorf("session heartbeat: heartbeat %v, want the request as given, an empty description included, and no unknown member", heartbeat)
	}

	r = last(pathMessageSend, `{"sender_session_id":"`+p.aliceSes+`","recipient_session_id":"`+p.bobSes+`","body":"hello","extra":true}`, "message", "send")
	members(r, "sender_session_id", "recipient_session_id", "body")

	r = last(pathMessageReceive, "", "message", "receive", "--session", p.aliceSes, "--limit", "7")
	members(r, "session_id", "limit")
	if r.Body["limit"] != float64(7) {
		t.Errorf("message receive: body %s, want limit 7 and neither after nor wait_seconds", r.Raw)
	}

	r = last(pathMessageAck, `{"message_ids":["m1","m2"]}`, "message", "ack", "--session", p.aliceSes)
	members(r, "session_id", "message_ids")

	r = last(pathTeamMembers, "", "team", "members")
	members(r, "team_ref")

	r = last(pathSessionClose, "", "session", "close", "--session", p.aliceSes)
	members(r, "session_id")

	r = last(pathTeamLeave, "", "team", "leave")
	members(r, "team_ref")
	if r.Body["team_ref"] != teamRef {
		t.Errorf("team leave: team_ref %v, want the profile's %s", r.Body["team_ref"], teamRef)
	}
}

// TestInputIsRefusedBeforeAnyRequest: a stdin document that fails its
// shape, a forged sender member above all (C-23), costs no request.
func TestInputIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	b.forget()
	send := func(extra string) string {
		return `{"sender_session_id":"` + p.aliceSes + `","recipient_session_id":"` + p.bobSes + `","body":"x"` + extra + `}`
	}
	for _, tc := range []struct {
		name  string
		field string
		input string
		args  []string
	}{
		{"a forged sender", "sender", send(`,"sender":{"session_id":"x"}`), []string{"message", "send"}},
		{"a forged team_ref", "team_ref", send(`,"team_ref":null`), []string{"message", "send"}},
		{"a forged hop_count", "hop_count", send(`,"hop_count":0`), []string{"message", "send"}},
		{"an oversize body", "body", `{"sender_session_id":"a","recipient_session_id":"b","body":"` + strings.Repeat("x", protocol.MaxBodyBytes+1) + `"}`, []string{"message", "send"}},
		{"a lease under the range", "lease_seconds", strings.Replace(registration("x"), `{`, `{"lease_seconds":29,`, 1), []string{"session", "register"}},
		{"a lease over the range", "lease_seconds", `{"lease_seconds":601}`, []string{"session", "heartbeat", "--session", p.aliceSes}},
		{"no message ids", "message_ids", `{"message_ids":[]}`, []string{"message", "ack", "--session", p.aliceSes}},
	} {
		got := p.alice.fails(protocol.CodeInvalidInput, tc.input, tc.args...)
		if details(t, got.stdout)["field"] != tc.field {
			t.Errorf("%s: details %v, want field %s", tc.name, details(t, got.stdout), tc.field)
		}
	}
	p.alice.fails(protocol.CodeUsage, "", "message", "receive", "--session", p.aliceSes, "--limit", "201")
	p.alice.fails(protocol.CodeUsage, "", "session", "close")
	p.alice.fails(protocol.CodeUsage, "", "session", "list", "--join-secret", "brg1.x.y")
	if n := len(b.seen("")); n != 0 {
		t.Errorf("the refused commands made %d requests, want none", n)
	}
}

// TestTheLeaseRangeIsTheProtocols: the adapter advertises BAP/1's 30..600
// and accepts a value at each end.
func TestTheLeaseRangeIsTheProtocols(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	p := newPair(t, b)
	for _, lease := range []string{"30", "600"} {
		got := p.alice.ok(strings.Replace(registration("lease-"+lease), `{`, `{"lease_seconds":`+lease+`,`, 1), "session", "register")
		if got["lease_seconds"] != map[string]float64{"30": 30, "600": 600}[lease] {
			t.Errorf("register with lease_seconds %s: granted %v", lease, got["lease_seconds"])
		}
	}
	lease, _ := p.alice.ok("", "describe")["lease"].(map[string]any)
	if lease["min_seconds"] != float64(30) || lease["default_seconds"] != float64(90) || lease["max_seconds"] != float64(600) {
		t.Errorf("describe: lease %v, want BAP/1's 30/90/600", lease)
	}
}
