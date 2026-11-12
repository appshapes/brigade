//go:build !windows

// The stub runs the fs adapter, whose own suite is not run on Windows.

package stub

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hangCatcher bounds every positive wait of these tests.
const hangCatcher = 30 * time.Second

// A harness is one stub behind an httptest server.
type harness struct {
	t   *testing.T
	dir string
	srv *httptest.Server
}

func newHarness(t *testing.T, o Options) *harness {
	t.Helper()
	if o.Poll == 0 {
		o.Poll = 10 * time.Millisecond
	}
	dir := t.TempDir()
	server, err := New(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, srv: httptest.NewServer(server)}
	t.Cleanup(h.srv.Close)
	return h
}

// An answer is one response: the status and the envelope's two halves.
type answer struct {
	status int
	raw    string
	ok     bool
	result map[string]any
	code   string
	err    map[string]any
}

// post makes one request of the contract with credential ("" for none).
func (h *harness) post(credential, path, body string) answer {
	h.t.Helper()
	return h.do(h.t.Context(), http.MethodPost, credential, path, body, nil)
}

func (h *harness) do(ctx context.Context, method, credential, path, body string, header map[string]string) answer {
	h.t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		h.t.Errorf("%s: Content-Type %q, want application/json", path, got)
	}
	var env struct {
		OK              bool           `json:"ok"`
		ProtocolVersion string         `json:"protocol_version"`
		Result          map[string]any `json:"result"`
		Error           map[string]any `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		h.t.Fatalf("%s: the body is not one envelope: %v (%q)", path, err, raw)
	}
	if env.ProtocolVersion != "1" || env.OK == (env.Error != nil) {
		h.t.Fatalf("%s: not a BAP/1 envelope: %s", path, raw)
	}
	a := answer{status: resp.StatusCode, raw: string(raw), ok: env.OK, result: env.Result, err: env.Error}
	if env.Error != nil {
		a.code, _ = env.Error["code"].(string)
		if _, present := env.Error["retryable"].(bool); !present {
			h.t.Errorf("%s: the error object carries no retryable member: %s", path, raw)
		}
	}
	return a
}

// must is post for a request that has to succeed.
func (h *harness) must(credential, path, body string) map[string]any {
	h.t.Helper()
	a := h.post(credential, path, body)
	if !a.ok || a.status != http.StatusOK {
		h.t.Fatalf("%s: status %d, %s", path, a.status, a.raw)
	}
	return a.result
}

// refused asserts a failing answer's code and the status the contract
// pairs with it.
func (h *harness) refused(a answer, code string, status int) {
	h.t.Helper()
	if a.ok || a.code != code || a.status != status {
		h.t.Fatalf("status %d, %s; want %s with status %d", a.status, a.raw, code, status)
	}
}

// A member is one principal as a test holds it.
type member struct {
	credential string
	principal  string
	team       string
	secret     string
}

// create makes a team and returns its creator.
func (h *harness) create(name string) member {
	h.t.Helper()
	r := h.must("", "/v1/team/create", `{"team_name":"`+name+`","human_label":"creator@example.com"}`)
	return member{
		credential: text2(h.t, r, "credential"), principal: text2(h.t, r, "principal_ref"),
		team: text2(h.t, r, "team_ref"), secret: text2(h.t, r, "join_secret"),
	}
}

// join joins the team of secret as a new principal.
func (h *harness) join(secret string) member {
	h.t.Helper()
	r := h.must("", "/v1/team/join", `{"join_secret":"`+secret+`","human_label":"joiner@example.com"}`)
	return member{
		credential: text2(h.t, r, "credential"), principal: text2(h.t, r, "principal_ref"),
		team: text2(h.t, r, "team_ref"), secret: secret,
	}
}

// register registers a session for m and returns its id.
func (h *harness) register(m member, name string) string {
	h.t.Helper()
	r := h.must(m.credential, "/v1/session/register",
		`{"team_ref":"`+m.team+`","harness":"test","harness_version":"1","session_name":"`+name+`","activity":"busy","inbound":"accept"}`)
	return text2(h.t, r, "session_id")
}

// send sends body between two sessions and returns the message id.
func (h *harness) send(m member, from, to, body string) string {
	h.t.Helper()
	r := h.must(m.credential, "/v1/message/send", `{"sender_session_id":"`+from+`","recipient_session_id":"`+to+`","body":"`+body+`"}`)
	return text2(h.t, r, "message_id")
}

// receive asks for a session's messages and returns their ids and the
// cursor.
func (h *harness) receive(m member, session, extra string) ([]string, string) {
	h.t.Helper()
	r := h.must(m.credential, "/v1/message/receive", `{"session_id":"`+session+`"`+extra+`}`)
	messages, isArray := r["messages"].([]any)
	if !isArray {
		h.t.Fatalf("receive: messages is not an array in %v", r)
	}
	ids := make([]string, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, text2(h.t, m.(map[string]any), "message_id"))
	}
	return ids, text2(h.t, r, "cursor")
}

func text2(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok || value == "" {
		t.Fatalf("member %q is missing or empty in %v", key, object)
	}
	return value
}

// TestTransport pins the contract's section 1 on the server's side: a POST
// of one JSON object, an envelope for every answer, the status that
// follows the envelope, and /v1/info with no credential.
func TestTransport(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	info := h.must("", "/v1/info", `{}`)
	server, _ := info["server"].(map[string]any)
	if info["contract_version"] != "1" || server["name"] != serverName || server["version"] == "" {
		t.Errorf("/v1/info: %v", info)
	}

	h.refused(h.post("", "/v1/nosuch", `{}`), "not_found", http.StatusNotFound)
	h.refused(h.do(t.Context(), http.MethodGet, "", "/v1/info", "", nil), "invalid_input", http.StatusBadRequest)
	for _, body := range []string{``, `[]`, `null`, `"x"`, `{`, `{"a":1}{"b":2}`, `{"a":1,"a":2}`} {
		h.refused(h.post("", "/v1/info", body), "invalid_input", http.StatusBadRequest)
	}
	big := `{"pad":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	h.refused(h.post("", "/v1/info", big), "invalid_input", http.StatusBadRequest)

	// Every endpoint but /v1/info and the two that create a principal needs
	// a known credential.
	for path, ep := range endpoints {
		if ep.open {
			continue
		}
		h.refused(h.post("", path, `{}`), "unauthenticated", http.StatusUnauthorized)
		h.refused(h.post("never-issued-credential", path, `{}`), "unauthenticated", http.StatusUnauthorized)
	}
}

// TestStatusFollowsTheEnvelope walks the contract's status table with real
// refusals of the fs adapter behind the stub.
func TestStatusFollowsTheEnvelope(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	a, b := h.register(alice, "a"), h.register(bob, "b")

	h.refused(h.post(alice.credential, "/v1/session/register", `{"team_ref":"`+alice.team+`","session_name":"x"}`), "invalid_input", http.StatusBadRequest)
	h.refused(h.post("", "/v1/team/join", `{"join_secret":"brg1.`+alice.team+`.wrong"}`), "unauthorized", http.StatusForbidden)
	h.refused(h.post(alice.credential, "/v1/message/receive", `{"session_id":"`+b+`"}`), "not_found", http.StatusNotFound)
	h.must(alice.credential, "/v1/session/close", `{"session_id":"`+a+`"}`)
	h.refused(h.post(alice.credential, "/v1/session/heartbeat", `{"session_id":"`+a+`","heartbeat":{}}`), "conflict", http.StatusConflict)

	// rate_limited: the sixteenth unacknowledged message from one session to another.
	a2 := h.register(alice, "a2")
	var limited answer
	for i := 0; i < 16; i++ {
		limited = h.post(alice.credential, "/v1/message/send", `{"sender_session_id":"`+a2+`","recipient_session_id":"`+b+`","body":"x"}`)
	}
	h.refused(limited, "rate_limited", http.StatusTooManyRequests)
	if ms, _ := limited.err["retry_after_ms"].(float64); ms <= 0 {
		t.Errorf("rate_limited: retry_after_ms %v, want a positive number", limited.err["retry_after_ms"])
	}
}

// TestPrincipalsAndCredentials is the contract's section 3: a create or a
// join with no credential, or an unknown one, creates a principal and
// issues its credential; with a known one it acts for that principal; a
// rejected join creates nothing; a revoked credential is unknown and the
// membership it named is not changed.
func TestPrincipalsAndCredentials(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	if len(alice.credential) < 16 || len(alice.credential) > 512 || strings.Trim(alice.credential, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._~+/=-") != "" {
		t.Errorf("the issued credential is not of the contract's shape")
	}
	bob := h.join(alice.secret)
	if bob.principal == alice.principal || bob.credential == alice.credential || bob.team != alice.team {
		t.Errorf("a join with no credential did not create a principal of its own in the team")
	}
	// An unknown credential on a join creates a principal too, with a new credential.
	r := h.must("credential-this-server-never-issued", "/v1/team/join", `{"join_secret":"`+alice.secret+`"}`)
	if text2(t, r, "credential") == "credential-this-server-never-issued" || r["rejoined"] != false {
		t.Errorf("a join with an unknown credential: %v", r)
	}

	// Rejected joins: one fixed answer, and nothing left behind.
	before := principalDirs(t, h.dir)
	wrong := h.post("", "/v1/team/join", `{"join_secret":"brg1.`+alice.team+`.0123456789abcdef"}`)
	unknown := h.post("", "/v1/team/join", `{"join_secret":"brg1.0123456789abcdef0123456789abcdef.0123456789abcdef"}`)
	h.refused(wrong, "unauthorized", http.StatusForbidden)
	if wrong.raw != unknown.raw {
		t.Errorf("a wrong secret and an unknown team differ:\n%s%s", wrong.raw, unknown.raw)
	}
	h.refused(h.post("", "/v1/team/join", `{"join_secret":"nope"}`), "invalid_input", http.StatusBadRequest)
	h.refused(h.post("", "/v1/team/create", `{"team_name":""}`), "invalid_input", http.StatusBadRequest)
	if after := principalDirs(t, h.dir); after != before {
		t.Errorf("rejected joins and creates left %d principal directories behind", after-before)
	}

	// A known credential acts for its principal: leave, then the same
	// membership again, with no new credential.
	left := h.must(bob.credential, "/v1/team/leave", `{"team_ref":"`+bob.team+`"}`)
	if left["principal_ref"] != bob.principal || left["team_ref"] != bob.team || left["left"] != true {
		t.Errorf("team leave: %v", left)
	}
	again := h.must(bob.credential, "/v1/team/leave", `{"team_ref":"`+bob.team+`"}`)
	if again["left"] != true || again["principal_ref"] != bob.principal {
		t.Errorf("a second team leave: %v, want the same success", again)
	}
	rejoined := h.must(bob.credential, "/v1/team/join", `{"join_secret":"`+alice.secret+`"}`)
	if rejoined["rejoined"] != true || rejoined["principal_ref"] != bob.principal {
		t.Errorf("a join by a principal that left: %v, want rejoined true and the same principal_ref", rejoined)
	}
	if _, has := rejoined["credential"]; has {
		t.Errorf("a join by a known principal was handed a credential it did not need")
	}
	// A create by a known principal that left its team: the same principal.
	h.must(bob.credential, "/v1/team/leave", `{"team_ref":"`+bob.team+`"}`)
	created := h.must(bob.credential, "/v1/team/create", `{"team_name":"bobs"}`)
	if created["principal_ref"] != bob.principal {
		t.Errorf("a create by a known principal: %v, want its own principal_ref", created)
	}
	if _, has := created["credential"]; has {
		t.Errorf("a create by a known principal was handed a credential it did not need")
	}

	// Revocation: the credential is unknown afterwards, the membership stays.
	if r := h.must(alice.credential, "/v1/credential/revoke", `{}`); r["revoked"] != true {
		t.Errorf("/v1/credential/revoke: %v", r)
	}
	h.refused(h.post(alice.credential, "/v1/team/members", `{"team_ref":"`+alice.team+`"}`), "unauthenticated", http.StatusUnauthorized)
	h.refused(h.post(alice.credential, "/v1/credential/revoke", `{}`), "unauthenticated", http.StatusUnauthorized)
	carol := h.join(alice.secret)
	members, _ := h.must(carol.credential, "/v1/team/members", `{"team_ref":"`+carol.team+`"}`)["members"].([]any)
	found := false
	for _, m := range members {
		if m.(map[string]any)["principal_ref"] == alice.principal {
			found = true
		}
	}
	if !found {
		t.Errorf("the principal whose credential was revoked is no longer in the roster; revocation must not change the membership")
	}
}

// principalDirs counts the principal directories under the stub's own.
func principalDirs(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "principals"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return len(entries)
}

// TestTeamScope: a request that names a team the principal is not an
// active member of is the uniform `unauthorized`, the same bytes for a
// team that exists and one that does not; a request that names none is
// invalid; and leaving such a team is a success that changes nothing.
func TestTeamScope(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	carol := h.create("other")
	registration := `"harness":"test","harness_version":"1","session_name":"x","activity":"busy","inbound":"accept"`
	for _, tc := range []struct{ path, rest string }{
		{"/v1/team/members", ``},
		{"/v1/session/list", ``},
		{"/v1/session/register", `,` + registration},
	} {
		existing := h.post(carol.credential, tc.path, `{"team_ref":"`+alice.team+`"`+tc.rest+`}`)
		random := h.post(carol.credential, tc.path, `{"team_ref":"0123456789abcdef0123456789abcdef"`+tc.rest+`}`)
		h.refused(existing, "unauthorized", http.StatusForbidden)
		if existing.raw != random.raw {
			t.Errorf("%s: a team that exists and one that does not differ:\n%s%s", tc.path, existing.raw, random.raw)
		}
		h.refused(h.post(carol.credential, tc.path, `{`+strings.TrimPrefix(tc.rest, ",")+`}`), "invalid_input", http.StatusBadRequest)
		if !h.post(carol.credential, tc.path, `{"team_ref":"`+carol.team+`"`+tc.rest+`}`).ok {
			t.Errorf("%s: refused for the principal's own team", tc.path)
		}
	}

	left := h.must(carol.credential, "/v1/team/leave", `{"team_ref":"`+alice.team+`"}`)
	if left["team_ref"] != alice.team || left["principal_ref"] != carol.principal || left["left"] != true {
		t.Errorf("leaving a team the principal is not in: %v", left)
	}
	if !h.post(carol.credential, "/v1/team/members", `{"team_ref":"`+carol.team+`"}`).ok {
		t.Errorf("leaving another team took the principal out of its own")
	}
	h.refused(h.post(carol.credential, "/v1/team/leave", `{}`), "invalid_input", http.StatusBadRequest)

	// A principal bound to no team at all: the same uniform answer on the
	// endpoints that name a session.
	h.must(carol.credential, "/v1/team/leave", `{"team_ref":"`+carol.team+`"}`)
	none := h.post(carol.credential, "/v1/message/receive", `{"session_id":"0123456789abcdef"}`)
	h.refused(none, "unauthorized", http.StatusForbidden)
	outsider := h.post(alice.credential, "/v1/team/members", `{"team_ref":"`+carol.team+`"}`)
	if none.raw != outsider.raw {
		t.Errorf("the unauthorized of a principal with no team and of a non-member differ:\n%s%s", none.raw, outsider.raw)
	}
}

// TestReceiveCursor is the contract's section 4 without the wait: a cursor
// in every result; `after` answers only what was accepted since that
// cursor was issued; a cursor the server does not recognise is treated as
// absent; `limit` bounds the page, oldest first; receiving never
// acknowledges.
func TestReceiveCursor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	a, b := h.register(alice, "a"), h.register(bob, "b")

	ids, empty := h.receive(alice, a, "")
	if len(ids) != 0 || empty == "" || len(empty) > 256 {
		t.Fatalf("an empty inbox: ids %v cursor %q", ids, empty)
	}
	m1, m2 := h.send(bob, b, a, "one"), h.send(bob, b, a, "two")
	ids, c2 := h.receive(alice, a, "")
	if strings.Join(ids, ",") != m1+","+m2 || c2 == empty {
		t.Fatalf("receive: %v cursor %q; want both messages, oldest first, and a new cursor", ids, c2)
	}
	// Receiving never acknowledges: the same again.
	if again, _ := h.receive(alice, a, ""); strings.Join(again, ",") != m1+","+m2 {
		t.Errorf("a second receive: %v, want the same two messages", again)
	}
	// After the cursor: nothing, then only what came later.
	if after, c := h.receive(alice, a, `,"after":"`+c2+`"`); len(after) != 0 || c != c2 {
		t.Errorf("receive after the last cursor: %v cursor %q, want nothing and the same cursor", after, c)
	}
	m3 := h.send(bob, b, a, "three")
	after, c3 := h.receive(alice, a, `,"after":"`+c2+`"`)
	if strings.Join(after, ",") != m3 || c3 == c2 {
		t.Errorf("receive after the cursor: %v, want only the message accepted since", after)
	}
	// The cursor of the empty inbox still answers everything accepted since.
	if all, _ := h.receive(alice, a, `,"after":"`+empty+`"`); len(all) != 3 {
		t.Errorf("receive after the first cursor: %d messages, want all 3", len(all))
	}
	// Not recognised: another session's cursor, garbage, a number not reached.
	_, bobs := h.receive(bob, b, "")
	for _, cursor := range []string{bobs, "garbage", c3 + "9", ""} {
		if all, _ := h.receive(alice, a, `,"after":"`+cursor+`"`); len(all) != 3 {
			t.Errorf("receive after the unrecognised cursor %q: %d messages, want all 3", cursor, len(all))
		}
	}
	// limit: oldest first, and the cursor still stands for everything.
	page, c := h.receive(alice, a, `,"limit":2`)
	if strings.Join(page, ",") != m1+","+m2 || c != c3 {
		t.Errorf("receive with limit 2: %v cursor %q, want the two oldest and the whole inbox's cursor", page, c)
	}
	for _, bad := range []string{`,"limit":0`, `,"limit":201`, `,"limit":"2"`, `,"wait_seconds":-1`, `,"wait_seconds":31`, `,"after":7`} {
		h.refused(h.post(alice.credential, "/v1/message/receive", `{"session_id":"`+a+`"`+bad+`}`), "invalid_input", http.StatusBadRequest)
	}
	h.refused(h.post(alice.credential, "/v1/message/receive", `{}`), "invalid_input", http.StatusBadRequest)

	// An acknowledged message is gone, and its number is not given again.
	acked := h.must(alice.credential, "/v1/message/ack", `{"session_id":"`+a+`","message_ids":["`+m1+`","nope"]}`)
	if got, _ := acked["acked"].([]any); len(got) != 1 || got[0] != m1 {
		t.Errorf("ack: %v", acked)
	}
	if rest, _ := h.receive(alice, a, ""); strings.Join(rest, ",") != m2+","+m3 {
		t.Errorf("receive after an ack: %v, want the two unacknowledged", rest)
	}
	if none, _ := h.receive(alice, a, `,"after":"`+c3+`"`); len(none) != 0 {
		t.Errorf("receive after the last cursor, after an ack: %v, want nothing", none)
	}
	// Ownership is checked on every receive.
	h.refused(h.post(bob.credential, "/v1/message/receive", `{"session_id":"`+a+`"}`), "not_found", http.StatusNotFound)
	h.refused(h.post(alice.credential, "/v1/message/receive", `{"session_id":"0123456789abcdef"}`), "not_found", http.StatusNotFound)
}

// TestReceiveWaits is `wait_seconds`: an empty answer is held until a
// message is accepted, which is then in that answer within a second, or
// until the time has passed; an answer that is not empty is not held.
func TestReceiveWaits(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	a, b := h.register(alice, "a"), h.register(bob, "b")
	_, cursor := h.receive(alice, a, "")

	// Held, then answered by the acceptance.
	type held struct {
		ids  []string
		took time.Duration
	}
	waiting := make(chan held, 1)
	started := time.Now()
	go func() {
		ids, _ := h.receive(alice, a, `,"after":"`+cursor+`","wait_seconds":30`)
		waiting <- held{ids, time.Since(started)}
	}()
	select {
	case got := <-waiting:
		t.Fatalf("a receive with nothing to answer returned at once: %v", got.ids)
	case <-time.After(300 * time.Millisecond):
	}
	sent := time.Now()
	m := h.send(bob, b, a, "wake up")
	select {
	case got := <-waiting:
		if len(got.ids) != 1 || got.ids[0] != m {
			t.Errorf("the waiting receive answered %v, want the accepted message", got.ids)
		}
		if lag := time.Since(sent); lag > time.Second {
			t.Errorf("the waiting receive answered %s after the acceptance; the contract gives it one second", lag)
		}
	case <-time.After(hangCatcher):
		t.Fatalf("the waiting receive did not answer after an acceptance")
	}

	// Held for the time asked, then answered empty.
	started = time.Now()
	ids, _ := h.receive(alice, a, `,"after":"`+h.cursorOf(alice, a)+`","wait_seconds":1`)
	if took := time.Since(started); len(ids) != 0 || took < 900*time.Millisecond || took > 10*time.Second {
		t.Errorf("a receive with wait_seconds 1 and nothing to answer: %v after %s", ids, took)
	}
	// Not empty: answered at once, whatever the wait.
	started = time.Now()
	ids, _ = h.receive(alice, a, `,"wait_seconds":30`)
	if took := time.Since(started); len(ids) != 1 || took > 5*time.Second {
		t.Errorf("a receive with something to answer: %v after %s, want it at once", ids, took)
	}
}

// cursorOf is the session's current cursor.
func (h *harness) cursorOf(m member, session string) string {
	h.t.Helper()
	_, cursor := h.receive(m, session, "")
	return cursor
}

// TestAWaitingReceiveLearnsOfARefusal: a request that waits is answered at
// once when its principal leaves the team (`unauthorized`) and when its
// credential is revoked (`unauthenticated`), and is abandoned without a
// fuss when its caller goes away.
func TestAWaitingReceiveLearnsOfARefusal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	bob := h.join(alice.secret)
	carol := h.join(alice.secret)
	b, c := h.register(bob, "b"), h.register(carol, "c")

	wait := func(m member, session string) chan answer {
		out := make(chan answer, 1)
		go func() {
			out <- h.post(m.credential, "/v1/message/receive", `{"session_id":"`+session+`","wait_seconds":30}`)
		}()
		return out
	}
	expect := func(what string, waiting chan answer, code string, status int) {
		t.Helper()
		select {
		case got := <-waiting:
			h.refused(got, code, status)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the waiting receive was not answered within 5 s", what)
		}
	}
	bobs, carols := wait(bob, b), wait(carol, c)
	time.Sleep(100 * time.Millisecond)
	h.must(bob.credential, "/v1/team/leave", `{"team_ref":"`+bob.team+`"}`)
	expect("after a leave", bobs, "unauthorized", http.StatusForbidden)
	h.must(carol.credential, "/v1/credential/revoke", `{}`)
	expect("after a revocation", carols, "unauthenticated", http.StatusUnauthorized)

	// A caller that goes away: the handler ends, so the server can close.
	a := h.register(alice, "a")
	ctx, cancel := context.WithCancel(t.Context())
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.srv.URL+"/v1/message/receive",
			bytes.NewReader([]byte(`{"session_id":"`+a+`","wait_seconds":30}`)))
		req.Header.Set("Authorization", "Bearer "+alice.credential)
		if resp, err := h.srv.Client().Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-gone:
	case <-time.After(hangCatcher):
		t.Fatal("the abandoned request did not end")
	}
	closed := make(chan struct{})
	go func() { h.srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(hangCatcher):
		t.Fatal("the server could not close: the abandoned receive is still held")
	}
}

// TestTheServersRanges: the lease range every version 1 server implements
// is BAP/1's 30..600, although the fs adapter behind the stub takes 1.
func TestTheServersRanges(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	register := func(lease string) answer {
		return h.post(alice.credential, "/v1/session/register",
			`{"team_ref":"`+alice.team+`","harness":"test","harness_version":"1","session_name":"x","activity":"busy","inbound":"accept","lease_seconds":`+lease+`}`)
	}
	for _, lease := range []string{"1", "29", "601"} {
		got := register(lease)
		h.refused(got, "invalid_input", http.StatusBadRequest)
		if details, _ := got.err["details"].(map[string]any); details["field"] != "lease_seconds" {
			t.Errorf("lease_seconds %s: details %v", lease, got.err["details"])
		}
	}
	session := ""
	for _, lease := range []string{"30", "600"} {
		got := register(lease)
		if !got.ok {
			t.Fatalf("lease_seconds %s refused: %s", lease, got.raw)
		}
		session = text2(t, got.result, "session_id")
	}
	h.refused(h.post(alice.credential, "/v1/session/heartbeat", `{"session_id":"`+session+`","heartbeat":{"lease_seconds":5}}`), "invalid_input", http.StatusBadRequest)
	h.refused(h.post(alice.credential, "/v1/session/heartbeat", `{"session_id":"`+session+`"}`), "invalid_input", http.StatusBadRequest)
	if !h.post(alice.credential, "/v1/session/heartbeat", `{"session_id":"`+session+`","heartbeat":{"lease_seconds":120,"activity":"idle"}}`).ok {
		t.Errorf("a heartbeat inside the range was refused")
	}
}

// TestRequestMembers: an unknown member of a request is ignored, the
// forbidden members of a send are refused (4.4.6), and `session list`
// marks the session it is asked about.
func TestRequestMembers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{})
	alice := h.create("ops")
	a := h.register(alice, "a")
	a2 := h.register(alice, "a2")

	ok := h.post(alice.credential, "/v1/message/send", `{"sender_session_id":"`+a+`","recipient_session_id":"`+a2+`","body":"x","unknown":1}`)
	if !ok.ok || ok.result["status"] != "accepted" {
		t.Errorf("a send with an unknown member: %s", ok.raw)
	}
	for _, forbidden := range []string{`"sender":{}`, `"principal_ref":"p"`, `"human_label":null`, `"team_ref":"t"`, `"created_at":"x"`, `"hop_count":0`} {
		got := h.post(alice.credential, "/v1/message/send", `{"sender_session_id":"`+a+`","recipient_session_id":"`+a2+`","body":"x",`+forbidden+`}`)
		h.refused(got, "invalid_input", http.StatusBadRequest)
	}

	list := h.must(alice.credential, "/v1/session/list", `{"team_ref":"`+alice.team+`","session_id":"`+a+`","unknown":true}`)
	sessions, _ := list["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("session list: %d sessions, want 2", len(sessions))
	}
	for _, s := range sessions {
		record := s.(map[string]any)
		if record["is_self"] != (record["session_id"] == a) {
			t.Errorf("session list: is_self %v for %v", record["is_self"], record["session_id"])
		}
	}
	h.must(alice.credential, "/v1/session/close", `{"session_id":"`+a2+`"}`)
	if live, _ := h.must(alice.credential, "/v1/session/list", `{"team_ref":"`+alice.team+`"}`)["sessions"].([]any); len(live) != 1 {
		t.Errorf("session list without include_offline: %d sessions, want 1", len(live))
	}
	if all, _ := h.must(alice.credential, "/v1/session/list", `{"team_ref":"`+alice.team+`","include_offline":true}`)["sessions"].([]any); len(all) != 2 {
		t.Errorf("session list with include_offline: %d sessions, want 2", len(all))
	}
	h.refused(h.post(alice.credential, "/v1/session/list", `{"team_ref":"`+alice.team+`","include_offline":"yes"}`), "invalid_input", http.StatusBadRequest)
	h.refused(h.post(alice.credential, "/v1/message/ack", `{"session_id":"`+a+`"}`), "invalid_input", http.StatusBadRequest)
}

// TestCreateToken: with Options.CreateToken set, /v1/team/create needs the
// token in Brigade-Create-Token; a missing or wrong one is `unauthorized`.
func TestCreateToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{CreateToken: "operator-token"})
	body := `{"team_name":"ops"}`
	h.refused(h.post("", "/v1/team/create", body), "unauthorized", http.StatusForbidden)
	wrong := h.do(t.Context(), http.MethodPost, "", "/v1/team/create", body, map[string]string{createTokenHeader: "another"})
	h.refused(wrong, "unauthorized", http.StatusForbidden)
	right := h.do(t.Context(), http.MethodPost, "", "/v1/team/create", body, map[string]string{createTokenHeader: "operator-token"})
	if !right.ok {
		t.Fatalf("a create with the token was refused: %s", right.raw)
	}
	// Joining needs no token.
	h.join(text2(t, right.result, "join_secret"))
}
