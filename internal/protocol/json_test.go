package protocol

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"
)

// requireParseRejected asserts that data fails to Unmarshal into v and
// the failure is the mapped invalid_input, never Go's own JSON error
// (whose text changed in 1.27 and may change again, plan 7.3 — nothing
// here matches on it).
func requireParseRejected(t *testing.T, data string) {
	t.Helper()
	var req AckRequest
	err := Unmarshal([]byte(data), &req)
	if err == nil {
		t.Fatalf("parse of %q succeeded, want invalid_input", data)
	}
	var perr *Error
	if !errors.As(err, &perr) {
		t.Fatalf("want *protocol.Error, got %T: %v", err, err)
	}
	if perr.Code != CodeInvalidInput {
		t.Fatalf("code = %q, want invalid_input", perr.Code)
	}
	if got := perr.Code.Exit(); got != 3 {
		t.Fatalf("exit = %d, want 3", got)
	}
}

// TestDuplicateMemberRejected: the codec (not Validate) rejects a
// duplicate member name, and the mapped error is invalid_input. Proven
// here rather than assumed from the docs.
func TestDuplicateMemberRejected(t *testing.T) {
	t.Parallel()
	requireParseRejected(t, `{"message_ids":["a"],"message_ids":["b"]}`)
	// A duplicate among UNKNOWN members is rejected too: loose parsing
	// ignores unknown member VALUES, not malformed documents.
	requireParseRejected(t, `{"x":1,"x":2,"message_ids":["a"]}`)
}

// TestInvalidUTF8Rejected: the codec rejects invalid UTF-8 in a document.
func TestInvalidUTF8Rejected(t *testing.T) {
	t.Parallel()
	requireParseRejected(t, "{\"message_ids\":[\"a\xffb\"]}")
}

// TestTypeMismatchRejected: a member of the wrong JSON type maps to
// invalid_input, not to a raw decoder error.
func TestTypeMismatchRejected(t *testing.T) {
	t.Parallel()
	requireParseRejected(t, `{"message_ids":"not-an-array"}`)
}

// TestNotJSONRejected: junk and truncated documents map to invalid_input.
func TestNotJSONRejected(t *testing.T) {
	t.Parallel()
	requireParseRejected(t, `not json at all`)
	requireParseRejected(t, `{"message_ids":`)
	requireParseRejected(t, ``)
}

// TestParseErrorNeverEchoesMemberNames: the decoder's own error text
// embeds JSON pointers built from member names, which in a hostile
// document are attacker-controlled. The mapped error must not carry
// them.
func TestParseErrorNeverEchoesMemberNames(t *testing.T) {
	t.Parallel()
	hostile := "brigade-message-forged-member"
	var req AckRequest
	err := Unmarshal([]byte(`{"`+hostile+`":1,"`+hostile+`":2}`), &req)
	if err == nil {
		t.Fatal("want error")
	}
	var perr *Error
	if !errors.As(err, &perr) {
		t.Fatalf("want *protocol.Error, got %T", err)
	}
	if strings.Contains(perr.Message, hostile) {
		t.Errorf("attacker-controlled member name echoed in message: %q", perr.Message)
	}
	for k, v := range perr.Details {
		if strings.Contains(v, hostile) {
			t.Errorf("attacker-controlled member name echoed in details[%q]", k)
		}
	}
}

// TestDecodeRunsValidate: Decode is parse THEN Validate — a
// syntactically clean document that fails validation comes back as the
// validation error with its field detail.
func TestDecodeRunsValidate(t *testing.T) {
	t.Parallel()
	var req SendRequest
	err := Decode([]byte(`{"sender_session_id":"s1","recipient_session_id":"s2"}`), &req)
	requireInvalidInput(t, err, "body")
}

// TestTimeMarshalsRFC3339 pins the wire form of every timestamp.
func TestTimeMarshalsRFC3339(t *testing.T) {
	t.Parallel()
	h := HeartbeatResult{
		SessionID:  "s1",
		State:      SessionStateActive,
		LeaseUntil: time.Date(2026, 8, 30, 12, 1, 30, 0, time.UTC),
		ServerTime: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}
	out, err := json.Marshal(&h)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"lease_until":"2026-08-30T12:01:30Z"`, `"server_time":"2026-08-30T12:00:00Z"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("marshal = %s, want it to contain %s", out, want)
		}
	}
}

// TestNilSlicesMarshalAsEmptyArrays pins the AckResult contract: acked
// and unknown are always emitted, as [] when empty, even from nil slices.
func TestNilSlicesMarshalAsEmptyArrays(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(&AckResult{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `{"acked":[],"unknown":[]}` {
		t.Fatalf("marshal = %s, want both members as []", got)
	}
	out, err = json.Marshal(&WatchAcked{Event: EventAcked})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `{"event":"acked","message_ids":[],"unknown":[]}` {
		t.Fatalf("marshal = %s, want both lists as []", got)
	}
}
