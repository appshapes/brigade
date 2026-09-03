package inbound

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

func validEnvelope() protocol.MessageEnvelope {
	return protocol.MessageEnvelope{
		ProtocolVersion:    protocol.ProtocolVersion,
		Kind:               protocol.KindText,
		MessageID:          "3c1a7d20-8f2e-4b91-a6c4-1e5b0d9f7a10",
		TeamRef:            "team-ref-opaque",
		Sender:             protocol.Sender{PrincipalRef: "9b2e", HumanLabel: "alice@example.com", SessionID: "6f0f", SessionName: "payments-api"},
		RecipientSessionID: "recipient",
		Body:               "The tenant_id migration has landed.",
		CreatedAt:          time.Date(2026, 8, 30, 12, 0, 5, 0, time.UTC),
		DeliveryState:      protocol.DeliveryStateAccepted,
	}
}

func TestValidateTable(t *testing.T) {
	t.Parallel()
	// Positive control first: the fixture passes, so every failing row
	// below fails for the mutation it makes and not for the fixture.
	m := validEnvelope()
	if err := Validate(&m); err != nil {
		t.Fatalf("fixture rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*protocol.MessageEnvelope)
		field  string
		reason string
	}{
		{"missing id", func(m *protocol.MessageEnvelope) { m.MessageID = "" }, "message_id", ReasonMissingID},
		{"id one over cap", func(m *protocol.MessageEnvelope) { m.MessageID = strings.Repeat("x", MaxMessageIDBytes+1) }, "message_id", ReasonIDTooLong},
		{"id not utf8", func(m *protocol.MessageEnvelope) { m.MessageID = "ab\xffcd" }, "message_id", ReasonIDNotUTF8},
		{"missing sender session", func(m *protocol.MessageEnvelope) { m.Sender.SessionID = "" }, "sender.session_id", ReasonMissingSender},
		{"missing principal", func(m *protocol.MessageEnvelope) { m.Sender.PrincipalRef = "" }, "sender.principal_ref", ReasonMissingPrincipal},
		{"empty body", func(m *protocol.MessageEnvelope) { m.Body = "" }, "body", ReasonEmptyBody},
		{"body one byte over cap", func(m *protocol.MessageEnvelope) { m.Body = strings.Repeat("x", protocol.MaxBodyBytes+1) }, "body", ReasonBodyTooLarge},
		{"body over cap in multibyte runes", func(m *protocol.MessageEnvelope) { m.Body = strings.Repeat("é", protocol.MaxBodyBytes/2+1) }, "body", ReasonBodyTooLarge},
		{"body not utf8", func(m *protocol.MessageEnvelope) { m.Body = "ok\xff\xfe" }, "body", ReasonBodyNotUTF8},
	}
	for _, tc := range cases {
		m := validEnvelope()
		tc.mutate(&m)
		err := Validate(&m)
		var verr *ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("%s: err = %v, want *ValidationError", tc.name, err)
			continue
		}
		if verr.Field != tc.field || verr.Reason != tc.reason {
			t.Errorf("%s: (%s, %s), want (%s, %s)", tc.name, verr.Field, verr.Reason, tc.field, tc.reason)
		}
		if strings.Contains(verr.Error(), m.Body) && m.Body != "" {
			t.Errorf("%s: error text echoes the body", tc.name)
		}
		if strings.Contains(verr.Error(), m.MessageID) && m.MessageID != "" {
			t.Errorf("%s: error text echoes the id", tc.name)
		}
	}
}

func TestValidateBoundariesAccepted(t *testing.T) {
	t.Parallel()
	m := validEnvelope()
	m.MessageID = strings.Repeat("i", MaxMessageIDBytes)
	m.Body = strings.Repeat("b", protocol.MaxBodyBytes)
	if err := Validate(&m); err != nil {
		t.Fatalf("at-cap id and body rejected: %v", err)
	}
	// A multibyte body exactly at the byte cap (not the rune cap).
	m.Body = strings.Repeat("é", protocol.MaxBodyBytes/2)
	if err := Validate(&m); err != nil {
		t.Fatalf("at-cap multibyte body rejected: %v", err)
	}
	// Looseness: kind, team_ref, hop_count, timestamps, delivery_state and
	// the summary are not checked, so an adapter's future value there
	// cannot lose a message.
	m = validEnvelope()
	m.Kind = "voice"
	m.TeamRef = ""
	m.HopCount = -7
	m.CreatedAt = time.Time{}
	m.DeliveryState = "whatever"
	m.Summary = strings.Repeat("s", 10000)
	m.Sender.SessionName = ""
	if err := Validate(&m); err != nil {
		t.Fatalf("loose members rejected: %v", err)
	}
}

// The failures that never reach Validate because the codec or the NDJSON
// reader refuses them first (U-18: non-string, nested, invalid UTF-8, a
// 2 MiB line).
func TestUpstreamRejectionsNeverReachThePipeline(t *testing.T) {
	t.Parallel()
	good := `{"event":"message","message":{"protocol_version":"1","kind":"text","message_id":"m1","team_ref":"t",` +
		`"sender":{"principal_ref":"p","session_id":"s","session_name":"n"},"recipient_session_id":"r","body":"hi",` +
		`"hop_count":0,"created_at":"2026-08-30T12:00:00Z","delivery_state":"accepted"}}`
	var w protocol.WatchMessage
	if err := protocol.Decode([]byte(good), &w); err != nil {
		t.Fatalf("positive control: valid line rejected: %v", err)
	}
	if err := Validate(&w.Message); err != nil {
		t.Fatalf("positive control: decoded envelope rejected: %v", err)
	}
	bad := map[string]string{
		"non-string id": strings.Replace(good, `"message_id":"m1"`, `"message_id":5`, 1),
		"nested body":   strings.Replace(good, `"body":"hi"`, `"body":{"text":"hi"}`, 1),
		"array sender":  strings.Replace(good, `"sender":{`, `"sender":[{`, 1),
		"invalid utf8":  strings.Replace(good, `"body":"hi"`, "\"body\":\"h\xffi\"", 1),
		"null id":       strings.Replace(good, `"message_id":"m1"`, `"message_id":null`, 1),
	}
	for name, line := range bad {
		var w protocol.WatchMessage
		err := protocol.Decode([]byte(line), &w)
		if err == nil {
			t.Errorf("%s: decoded and validated, must be refused upstream", name)
			continue
		}
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidInput {
			t.Errorf("%s: err %v, want invalid_input", name, err)
		}
	}
	// A 2 MiB line is dropped by the reader (B-6) and the next line is
	// delivered, so a huge event costs one warning, never the stream.
	huge := `{"event":"message","message":{"message_id":"` + strings.Repeat("x", 2<<20) + `"}}`
	r := protocol.NewLineReader(bytes.NewReader([]byte(huge + "\n" + good + "\n")))
	if _, err := r.Next(); !errors.Is(err, protocol.ErrLineTooLong) {
		t.Fatalf("2 MiB line: err %v, want ErrLineTooLong", err)
	}
	line, err := r.Next()
	if err != nil || string(line) != good {
		t.Fatalf("line after the drop: %q %v", line, err)
	}
}
