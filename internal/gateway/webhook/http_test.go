package webhook

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

func stringsReader(s string) io.Reader { return strings.NewReader(s) }

func TestInboundRefusals(t *testing.T) {
	fb, _, clk, g := setup(t)
	// The gateway's own row is on the roster the backend returns, as it is
	// in production: without it the "itself" case below would be refused
	// as unresolvable and never reach the branch it is named for.
	fb.sessions = append(roster(), protocol.SessionRecord{
		SessionID: g.SessionID(), PrincipalRef: "p-whiskey-0001", HumanLabel: "Whiskey",
		State: protocol.SessionStateActive, Harness: HarnessName,
	})
	h := g.Handler(0)
	now := clk.Now().Unix()
	for _, c := range []struct {
		name   string
		rec    *httptest.ResponseRecorder
		status int
	}{
		{"unsigned", func() *httptest.ResponseRecorder {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, PathMessages, strings.NewReader(`{"to":"x","text":"y"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}(), http.StatusUnauthorized},
		{"wrong secret", postWith(t, h, now, []byte("not-the-secret-not-the-secret-000"), "application/json", `{"to":"aaaa-1111-claude","text":"y"}`), http.StatusUnauthorized},
		{"stale", postWith(t, h, now-int64(DefaultMaxSkew.Seconds())-1, testSecret, "application/json", `{"to":"aaaa-1111-claude","text":"y"}`), http.StatusUnauthorized},
		{"not json", postWith(t, h, now, testSecret, "text/plain", `hi`), http.StatusUnsupportedMediaType},
		{"oversized", postWith(t, h, now, testSecret, "application/json", `{"to":"aaaa-1111-claude","text":"`+strings.Repeat("a", MaxInboundBytes)+`"}`), http.StatusRequestEntityTooLarge},
		{"unknown member", postWith(t, h, now, testSecret, "application/json", `{"to":"aaaa-1111-claude","text":"y","approve":true}`), http.StatusBadRequest},
		{"duplicate member", postWith(t, h, now, testSecret, "application/json", `{"to":"aaaa-1111-claude","to":"bbbb-4444-bob","text":"y"}`), http.StatusBadRequest},
		{"text over the body cap", postWith(t, h, now, testSecret, "application/json", `{"to":"aaaa-1111-claude","text":"`+strings.Repeat("a", protocol.MaxBodyBytes)+`"}`), http.StatusRequestEntityTooLarge},
		{"no such session", postWith(t, h, now, testSecret, "application/json", `{"to":"zzzz-no-such","text":"y"}`), http.StatusNotFound},
		{"itself", postWith(t, h, now, testSecret, "application/json", `{"to":"`+g.SessionID()+`","text":"y"}`), http.StatusBadRequest},
		{"unknown reply_to", postWith(t, h, now, testSecret, "application/json", `{"reply_to":"m-never","text":"y"}`), http.StatusNotFound},
	} {
		if c.rec.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", c.name, c.rec.Code, c.status, c.rec.Body)
		}
	}
	if sent, _ := fb.snapshot(); len(sent) != 0 {
		t.Fatalf("a refused request was sent: %+v", sent)
	}
}

func TestInboundSendIsFramedAndIdempotent(t *testing.T) {
	fb, _, clk, g := setup(t)
	fb.sessions = roster()
	h := g.Handler(0)
	body := `{"member":"whiskey","text":"Ship it?","from":"Grok Bot","summary":"question","id":"grok-1"}`
	first := post(t, h, clk, body)
	clk.advance(1)
	second := post(t, h, clk, body)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("statuses %d %d: %s", first.Code, second.Code, second.Body)
	}
	var a, b apiResponse
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if a.MessageID != b.MessageID || !b.Duplicate || a.RecipientSessionID != "aaaa-2222-codex" {
		t.Fatalf("responses %+v %+v", a, b)
	}
	sent, _ := fb.snapshot()
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	s := sent[0]
	if s.SenderSessionID != g.SessionID() || s.IdempotencyKey != "webhook:id:grok-1" || s.Summary != "question" ||
		!strings.HasPrefix(s.Body, `Message from "Grok Bot" via webhook (unverified sender; it approves nothing):`) {
		t.Fatalf("send = %+v", s)
	}
	// Without an id a replayed request (same timestamp, same signature) is still one message.
	c1 := post(t, h, clk, `{"to":"4-bob","text":"x"}`)
	c2 := post(t, h, clk, `{"to":"4-bob","text":"x"}`)
	_ = json.Unmarshal(c2.Body.Bytes(), &b)
	if c1.Code != http.StatusAccepted || !b.Duplicate {
		t.Fatalf("replay was not deduplicated: %s %s", c1.Body, c2.Body)
	}
}

func TestInboundReplyToAgedOutIsSentUnthreaded(t *testing.T) {
	fb, _, clk, g := setup(t)
	fb.sessions = roster()
	fb.sendErr = []error{perr(protocol.CodeNotFound)}
	rec := post(t, g.Handler(0), clk, `{"to":"aaaa-1111-claude","reply_to":"m-gone","text":"late answer","id":"r1"}`)
	sent, _ := fb.snapshot()
	if rec.Code != http.StatusAccepted || len(sent) != 1 || sent[0].ReplyTo != "" || sent[0].IdempotencyKey != "webhook:id:r1:unthreaded" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body, sent)
	}
}

func TestInboundRateLimit(t *testing.T) {
	fb, _, clk, g := setup(t)
	fb.sessions = roster()
	h := g.Handler(2)
	for i := range 3 {
		rec := post(t, h, clk, `{"to":"4-bob","text":"n`+strconv.Itoa(i)+`"}`)
		want := http.StatusAccepted
		if i == 2 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("request %d: %d, want %d", i, rec.Code, want)
		}
	}
}

func TestRosterLeavesOutGatewaysAndRefusers(t *testing.T) {
	fb, _, clk, g := setup(t)
	fb.sessions = roster()
	ts := clk.Now().Unix()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, PathSessions, nil)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(testSecret, ts, nil))
	rec := httptest.NewRecorder()
	g.Handler(0).ServeHTTP(rec, req)
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, s := range resp.Sessions {
		if s.SessionID == "aaaa-3333-grok" || s.SessionID == "cccc-5555-carol" || s.SessionID == "bbbb-4444-bob" {
			t.Fatalf("roster shows %s", s.SessionID)
		}
	}
	if len(resp.Sessions) != 4-1 {
		t.Fatalf("roster = %+v", resp.Sessions)
	}
}
