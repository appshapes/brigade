package webhook

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// receiver is a webhook endpoint that verifies every POST and answers
// with the next scripted status (200 once the script runs out).
type receiver struct {
	mu       sync.Mutex
	statuses []int
	got      []OutboundPayload
	headers  []http.Header
	bad      int
	now      func() time.Time
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if Verify(testSecret, r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderSignature), body, rc.now(), DefaultMaxSkew) != nil {
		rc.bad++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var p OutboundPayload
	if err := json.Unmarshal(body, &p); err != nil || r.Header.Get(HeaderDelivery) != p.DeliveryID {
		rc.bad++
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rc.got = append(rc.got, p)
	rc.headers = append(rc.headers, r.Header.Clone())
	status := http.StatusOK
	if len(rc.statuses) > 0 {
		status, rc.statuses = rc.statuses[0], rc.statuses[1:]
	}
	w.WriteHeader(status)
}

func setup(t *testing.T, statuses ...int) (*fakeBackend, *receiver, *clock, *Gateway) {
	t.Helper()
	clk := &clock{now: time.Unix(1_800_000_000, 0)}
	rc := &receiver{statuses: statuses, now: clk.Now}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	fb := newFake()
	g := startGateway(t, fb, srv.URL, clk, filepath.Join(t.TempDir(), "state.json"))
	return fb, rc, clk, g
}

func TestDeliverAcksOnlyAfter2xx(t *testing.T) {
	fb, rc, _, g := setup(t, http.StatusOK)
	fb.deliver(message("m-1", "sess-alice"))
	n, err := g.Deliver(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("Deliver = %d, %v", n, err)
	}
	_, acked := fb.snapshot()
	if len(acked) != 1 || acked[0] != "m-1" || len(rc.got) != 1 || rc.bad != 0 {
		t.Fatalf("acked %v, posted %d, bad %d", acked, len(rc.got), rc.bad)
	}
	p := rc.got[0]
	if p.Message.From.SessionID != "sess-alice" || p.Gateway.SessionID != g.SessionID() || p.Message.Body != "hello from sess-alice" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestDeliverRetriesWithBackoffThenGivesUp(t *testing.T) {
	fb, rc, clk, g := setup(t, 503, 503, 503)
	fb.deliver(message("m-1", "sess-alice"))
	for i := range 3 {
		if n, err := g.Deliver(t.Context()); err != nil || (i < 2 && n != 0) {
			t.Fatalf("pass %d: Deliver = %d, %v", i, n, err)
		}
		// Before the backoff is due nothing is posted again.
		posted := len(rc.got)
		if _, err := g.Deliver(t.Context()); err != nil || len(rc.got) != posted {
			t.Fatalf("pass %d: posted during backoff", i)
		}
		clk.advance(backoffCap)
	}
	sent, acked := fb.snapshot()
	if len(acked) != 1 || len(sent) != 1 {
		t.Fatalf("after MaxAttempts: acked %v, sent %d", acked, len(sent))
	}
	n := sent[0]
	if n.RecipientSessionID != "sess-alice" || n.ReplyTo != "m-1" || n.IdempotencyKey != "gw-err:m-1" {
		t.Fatalf("notice = %+v", n)
	}
}

func TestDeliverGivesUpAtOnceOnA4xx(t *testing.T) {
	fb, _, _, g := setup(t, http.StatusUnprocessableEntity)
	fb.deliver(message("m-1", "sess-alice"))
	if n, err := g.Deliver(t.Context()); err != nil || n != 1 {
		t.Fatalf("Deliver = %d, %v", n, err)
	}
	sent, _ := fb.snapshot()
	if len(sent) != 1 || sent[0].IdempotencyKey != "gw-err:m-1" {
		t.Fatalf("notice = %+v", sent)
	}
}

func TestGiveUpKeepsTheMessageWhenTheNoticeHitsARetryableError(t *testing.T) {
	fb, rc, clk, g := setup(t, http.StatusBadRequest, http.StatusBadRequest)
	fb.sendErr = []error{perr(protocol.CodeUnavailable)}
	fb.deliver(message("m-1", "sess-alice"))
	if n, _ := g.Deliver(t.Context()); n != 0 {
		t.Fatalf("acked %d while the sender was not told", n)
	}
	// The endpoint refused it for good, so the message is not posted again
	// before the backoff is due — otherwise every poll would re-post it
	// for as long as the backend stayed unavailable.
	posted := len(rc.got)
	if n, _ := g.Deliver(t.Context()); n != 0 || len(rc.got) != posted {
		t.Fatalf("re-posted during the backoff: acked %d, %d posts became %d", n, posted, len(rc.got))
	}
	clk.advance(backoffCap)
	if n, _ := g.Deliver(t.Context()); n != 1 {
		t.Fatalf("the second attempt at the notice acked %d", n)
	}
	if sent, _ := fb.snapshot(); len(sent) != 1 || sent[0].IdempotencyKey != "gw-err:m-1" {
		t.Fatalf("notice = %+v", sent)
	}
}

func TestHeartbeatReRegistersALostSession(t *testing.T) {
	fb, _, clk, g := setup(t)
	first := g.SessionID()
	fb.heartbeatErr = []error{perr(protocol.CodeNotFound)}
	fb.registerErr = []error{perr(protocol.CodeNotFound)} // the resume is refused too
	if err := g.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if g.SessionID() == first || g.SessionID() == "" {
		t.Fatalf("session %q was not replaced", g.SessionID())
	}
	// The next heartbeat waits a third of the lease.
	before := len(fb.registers)
	clk.advance(time.Second)
	fb.heartbeatErr = []error{perr(protocol.CodeNotFound)}
	if err := g.Tick(t.Context()); err != nil || len(fb.registers) != before {
		t.Fatalf("heartbeat ran before it was due: %v", err)
	}
}

func TestStartResumesTheRememberedSession(t *testing.T) {
	clk := &clock{now: time.Unix(1_800_000_000, 0)}
	path := filepath.Join(t.TempDir(), "state.json")
	fb := newFake()
	g1 := startGateway(t, fb, "http://127.0.0.1:1", clk, path)
	g2 := startGateway(t, fb, "http://127.0.0.1:1", clk, path)
	last := fb.registers[len(fb.registers)-1]
	if last.Resume == nil || last.Resume.SessionID != g1.SessionID() || g2.SessionID() != g1.SessionID() {
		t.Fatalf("second start did not resume: %+v", last)
	}
	if last.Harness != HarnessName || last.Inbound != protocol.InboundAccept {
		t.Fatalf("registration = %+v", last)
	}
}

func TestRepliesCarryTheCallersThreadRef(t *testing.T) {
	fb, rc, clk, g := setup(t)
	fb.sessions = roster()
	h := g.Handler(0)
	rec := post(t, h, clk, `{"to":"aaaa-1111-claude","text":"can you look?","ref":"conv-7"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	var resp apiResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	answer := message("m-ans", "aaaa-1111-claude")
	answer.ReplyTo = &resp.MessageID
	fb.deliver(answer)
	if _, err := g.Deliver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(rc.got) != 1 || rc.got[0].Thread == nil || rc.got[0].Thread.Ref != "conv-7" || rc.got[0].Thread.InReplyTo != resp.MessageID {
		t.Fatalf("thread = %+v", rc.got)
	}
	// And reply_to alone routes back to the session that answered.
	rec = post(t, h, clk, `{"reply_to":"m-ans","text":"thanks"}`)
	sent, _ := fb.snapshot()
	if rec.Code != http.StatusAccepted || sent[len(sent)-1].RecipientSessionID != "aaaa-1111-claude" || sent[len(sent)-1].ReplyTo != "m-ans" {
		t.Fatalf("reply = %d %s %+v", rec.Code, rec.Body, sent[len(sent)-1])
	}
}

func post(t *testing.T, h http.Handler, clk *clock, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postWith(t, h, clk.Now().Unix(), testSecret, "application/json", body)
}

func postWith(t *testing.T, h http.Handler, ts int64, secret []byte, ctype, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, PathMessages, stringsReader(body))
	req.Header.Set("Content-Type", ctype)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(secret, ts, []byte(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A --header-file's headers ride on every POST beside the signature,
// which still verifies: the receiver's own key (a Grok Bot routine's
// Authorization header) is extra, never instead.
func TestDeliverSendsTheExtraHeaders(t *testing.T) {
	clk := &clock{now: time.Unix(1_800_000_000, 0)}
	rc := &receiver{now: clk.Now}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	fb := newFake()
	extra, err := parseHeaderFile([]byte("# the routine's key\nAuthorization: Bearer routine-key-0123456789\n\nx-routine-tag: brigade\n"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{TargetURL: srv.URL, Secret: testSecret, ExtraHeaders: extra, MaxAttempts: 3, Now: clk.Now}, fb)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	fb.deliver(message("m-1", "sess-alice"))
	if n, err := g.Deliver(t.Context()); err != nil || n != 1 {
		t.Fatalf("Deliver = %d, %v", n, err)
	}
	if rc.bad != 0 || len(rc.headers) != 1 {
		t.Fatalf("bad %d, posts %d", rc.bad, len(rc.headers))
	}
	h := rc.headers[0]
	if h.Get("Authorization") != "Bearer routine-key-0123456789" || h.Get("X-Routine-Tag") != "brigade" {
		t.Fatalf("extra headers not sent: %v", h)
	}
	if h.Get("Content-Type") != "application/json" || h.Get(HeaderDelivery) != "m-1" {
		t.Fatalf("the gateway's own headers changed: %v", h)
	}
}

// The gateway's own headers can never come from the file, in any case:
// not through the parser, and not through a Config built by hand.
func TestExtraHeadersCannotOverrideTheGatewaysOwn(t *testing.T) {
	for _, line := range []string{
		"Content-Type: text/plain", "content-type: text/plain", "HOST: evil.example", "User-Agent: x",
		"X-Brigade-Signature: v1=00", "x-brigade-timestamp: 1", "X-BRIGADE-DELIVERY: m-9", "X-Brigade-Anything: y",
	} {
		if _, err := parseHeaderFile([]byte(line + "\n")); err == nil {
			t.Errorf("parseHeaderFile accepted %q", line)
		}
	}
	for _, name := range []string{"Content-Type", "Host", "X-Brigade-Signature"} {
		_, err := New(Config{TargetURL: "https://example.com/hook", Secret: testSecret,
			ExtraHeaders: http.Header{name: {"x"}}}, newFake())
		if err == nil {
			t.Errorf("New accepted an extra %s header", name)
		}
	}
}

func TestParseHeaderFile(t *testing.T) {
	for _, c := range []struct {
		name string
		data string
		ok   bool
	}{
		{"one header", "Authorization: Bearer k\n", true},
		{"crlf and comments", "# key\r\nAuthorization: Bearer k\r\n\r\n", true},
		{"value with a colon", "X-Key: a:b:c\n", true},
		{"empty file", "", true},
		{"no colon", "Authorization Bearer k\n", false},
		{"empty name", ": k\n", false},
		{"bad name", "Auth orization: k\n", false},
		{"empty value", "Authorization:\n", false},
		{"control character", "Authorization: Bearer \x00k\n", false},
		{"duplicate, any case", "X-Key: a\nx-key: b\n", false},
	} {
		_, err := parseHeaderFile([]byte(c.data))
		if (err == nil) != c.ok {
			t.Errorf("%s: parseHeaderFile = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && strings.Contains(err.Error(), "Bearer") {
			t.Errorf("%s: the refusal quotes the file: %v", c.name, err)
		}
	}
	var many strings.Builder
	for i := range maxExtraHeaders + 1 {
		many.WriteString("X-H" + strconv.Itoa(i) + ": v\n")
	}
	if _, err := parseHeaderFile([]byte(many.String())); err == nil {
		t.Errorf("parseHeaderFile accepted %d headers", maxExtraHeaders+1)
	}
}

// Every value, and the credential inside an `Authorization: Bearer …`,
// is an exact token to the redacting logger.
func TestHeaderValuesAreRedacted(t *testing.T) {
	extra, err := parseHeaderFile([]byte("Authorization: Bearer routine-key-0123456789\nX-Key: other-secret\n"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	redactor := adapterlog.NewRedactor()
	redactor.Add(headerSecrets(extra)...)
	adapterlog.New(&buf, slog.LevelDebug, redactor).Error("upstream said: token routine-key-0123456789 refused; other-secret too")
	if out := buf.String(); strings.Contains(out, "routine-key-0123456789") || strings.Contains(out, "other-secret") {
		t.Fatalf("a header value reached the log: %s", out)
	}
}

// A value shorter than minRedactedHeaderValue is never registered: a
// redactor token is a plain substring replaced everywhere, so a tag or a
// version value would blank the gateway's own diagnostics.
func TestAShortHeaderValueIsNotRedacted(t *testing.T) {
	extra, err := parseHeaderFile([]byte("X-Routine-Tag: brigade\nX-Api-Version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if secrets := headerSecrets(extra); len(secrets) != 0 {
		t.Fatalf("headerSecrets registered a short value: %v", secrets)
	}
	var buf bytes.Buffer
	redactor := adapterlog.NewRedactor()
	redactor.Add(headerSecrets(extra)...)
	adapterlog.New(&buf, slog.LevelDebug, redactor).
		Error("post failed: brigade-webhook-gateway/0.38.0 attempt 1 of 24")
	out := buf.String()
	if !strings.Contains(out, "brigade-webhook-gateway/0.38.0") || !strings.Contains(out, "attempt 1 of 24") {
		t.Fatalf("a short header value redacted the gateway's own diagnostics: %s", out)
	}
}
