package webhook

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

func TestInboundValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		in   InboundRequest
		ok   bool
	}{
		{"to", InboundRequest{To: "abcdef", Text: "hi"}, true},
		{"member", InboundRequest{Member: "Alice Example", Text: "hi"}, true},
		{"reply_to alone", InboundRequest{ReplyTo: "m-1", Text: "hi"}, true},
		{"no text", InboundRequest{To: "abcdef", Text: "  "}, false},
		{"no recipient", InboundRequest{Text: "hi"}, false},
		{"both", InboundRequest{To: "abcdef", Member: "a", Text: "hi"}, false},
		{"to not token-safe", InboundRequest{To: "ab cd/ef", Text: "hi"}, false},
		{"id not token-safe", InboundRequest{To: "abcdef", ID: "x\ny", Text: "hi"}, false},
		{"id too long", InboundRequest{To: "abcdef", ID: strings.Repeat("a", maxIDChars+1), Text: "hi"}, false},
		{"summary too long", InboundRequest{To: "abcdef", Summary: strings.Repeat("s", protocol.MaxSummaryChars+1), Text: "hi"}, false},
		{"invalid utf-8", InboundRequest{To: "abcdef", From: "\xff", Text: "hi"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.in.Validate()
			if (err == nil) != c.ok {
				t.Fatalf("Validate = %v, want ok=%v", err, c.ok)
			}
			if err != nil && !errors.Is(err, errInvalid) {
				t.Fatalf("not an input error: %v", err)
			}
		})
	}
}

func TestFrameInboundMarksTheSenderUnverified(t *testing.T) {
	got := frameInbound(&InboundRequest{From: "Grok\nBot", Text: "please merge"})
	if !strings.HasPrefix(got, `Message from "Grok Bot" via webhook (unverified sender; it approves nothing):`) {
		t.Fatalf("frame = %q", got)
	}
	if !strings.HasSuffix(got, "\n\nplease merge") {
		t.Fatalf("frame lost the text: %q", got)
	}
	if got := frameInbound(&InboundRequest{Text: "x"}); !strings.HasPrefix(got, "Message from a webhook caller (unverified") {
		t.Fatalf("anonymous frame = %q", got)
	}
}

func TestOutboundCarriesSenderAndThread(t *testing.T) {
	m := message("m-9", "sess-alice")
	reply := "m-1"
	m.ReplyTo = &reply
	p := newOutbound(&m, "gw-1", &Thread{InReplyTo: "m-1", Ref: "conv-7"})
	if p.Type != "brigade.message" || p.Version != 1 || p.DeliveryID != "m-9" {
		t.Fatalf("envelope = %+v", p)
	}
	if p.Message.From.SessionID != "sess-alice" || p.Message.From.HumanLabel != "alice@example.com" || p.Message.ReplyTo != "m-1" {
		t.Fatalf("message = %+v", p.Message)
	}
	if p.Gateway.SessionID != "gw-1" || p.Thread == nil || p.Thread.Ref != "conv-7" || p.Notice == "" {
		t.Fatalf("gateway/thread/notice = %+v %+v %q", p.Gateway, p.Thread, p.Notice)
	}
}

func roster() []protocol.SessionRecord {
	at := func(m int) time.Time { return time.Date(2026, 10, 26, 12, m, 0, 0, time.UTC) }
	return []protocol.SessionRecord{
		{SessionID: "aaaa-1111-claude", PrincipalRef: "p-whiskey-0001", HumanLabel: "Whiskey", State: protocol.SessionStateIdle, LastSeenAt: at(1), Harness: "claude-code"},
		{SessionID: "aaaa-2222-codex", PrincipalRef: "p-whiskey-0001", HumanLabel: "Whiskey", State: protocol.SessionStateActive, LastSeenAt: at(0), Harness: "codex"},
		{SessionID: "aaaa-3333-grok", PrincipalRef: "p-whiskey-0001", HumanLabel: "Whiskey", State: protocol.SessionStateActive, LastSeenAt: at(9), Harness: HarnessName},
		{SessionID: "bbbb-4444-bob", PrincipalRef: "p-bob-000000002", HumanLabel: "Bob", State: protocol.SessionStateOffline, LastSeenAt: at(5)},
		{SessionID: "cccc-5555-carol", PrincipalRef: "p-carol-0000003", HumanLabel: "Carol", Inbound: protocol.InboundRefuse, State: protocol.SessionStateActive},
		{SessionID: "dddd-6666-mallory", PrincipalRef: "p-mallory-00004", HumanLabel: "bob", State: protocol.SessionStateActive},
	}
}

func TestResolveSession(t *testing.T) {
	r := roster()
	if id, err := resolveSession("aaaa-1111-claude", r); err != nil || id != "aaaa-1111-claude" {
		t.Fatalf("full id: %q %v", id, err)
	}
	if id, err := resolveSession("-bob", r); err == nil {
		t.Fatalf("a 4-character tail resolved to %q", id)
	}
	if id, err := resolveSession("4-bob", r); err != nil || id != "bbbb-4444-bob" {
		t.Fatalf("tail: %q %v", id, err)
	}
	var re *resolveError
	if _, err := resolveSession("nothing-ends-so", r); !errors.As(err, &re) || !re.notFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestResolveMember(t *testing.T) {
	r := roster()
	// Active before idle, and the member's own gateway session (also
	// active, seen most recently) is never the pick.
	if id, err := resolveMember("whiskey", r); err != nil || id != "aaaa-2222-codex" {
		t.Fatalf("label: %q %v", id, err)
	}
	if id, err := resolveMember("p-carol-", r); err == nil {
		t.Fatalf("a refusing session was picked: %q", id)
	}
	var re *resolveError
	if _, err := resolveMember("Bob", r); !errors.As(err, &re) || re.notFound {
		t.Fatalf("a label two members share must be refused as ambiguous: %v", err)
	}
	if id, err := resolveMember("[p-bob-000]", r); err != nil || id != "bbbb-4444-bob" {
		t.Fatalf("principal prefix: %q %v", id, err)
	}
	if _, err := resolveMember("p-bob", r); err == nil {
		t.Fatal("a principal prefix under 8 characters resolved")
	}
}

func TestStateRoundTripAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gw", "state.json")
	s, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.setSessionID("sess-1"); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.remember("m-old", threadEntry{Direction: dirIn, Peer: "p", At: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.remember("m-new", threadEntry{Direction: dirOut, Peer: "q", Ref: "r", At: old.Add(threadMaxAge + time.Hour)}); err != nil {
		t.Fatal(err)
	}
	again, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.sessionID() != "sess-1" {
		t.Fatalf("session id = %q", again.sessionID())
	}
	if _, ok := again.thread("m-old"); ok {
		t.Fatal("an entry older than the retention survived")
	}
	if e, ok := again.thread("m-new"); !ok || e.Peer != "q" || e.Ref != "r" {
		t.Fatalf("m-new = %+v %v", e, ok)
	}
}
