package protocol

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// examples maps every testdata/examples file to a fresh instance of its
// wire type. Every 4.4 shape appears at least once.
var examples = map[string]func() Validator{
	"describe_result.json":         func() Validator { return &DescribeResult{} },
	"session_registration.json":    func() Validator { return &SessionRegistration{} },
	"session_record.json":          func() Validator { return &SessionRecord{} },
	"heartbeat_request.json":       func() Validator { return &HeartbeatRequest{} },
	"heartbeat_result.json":        func() Validator { return &HeartbeatResult{} },
	"message_envelope.json":        func() Validator { return &MessageEnvelope{} },
	"send_request.json":            func() Validator { return &SendRequest{} },
	"send_response.json":           func() Validator { return &SendResponse{} },
	"ack_request.json":             func() Validator { return &AckRequest{} },
	"ack_result.json":              func() Validator { return &AckResult{} },
	"watch_ready.json":             func() Validator { return &WatchReady{} },
	"watch_message.json":           func() Validator { return &WatchMessage{} },
	"watch_status_live.json":       func() Validator { return &WatchStatus{} },
	"watch_status_polling.json":    func() Validator { return &WatchStatus{} },
	"watch_acked.json":             func() Validator { return &WatchAcked{} },
	"watch_heartbeat_ok.json":      func() Validator { return &WatchHeartbeatOK{} },
	"watch_error.json":             func() Validator { return &WatchError{} },
	"watch_command_ack.json":       func() Validator { return &WatchCommand{} },
	"watch_command_heartbeat.json": func() Validator { return &WatchCommand{} },
	"watch_command_close.json":     func() Validator { return &WatchCommand{} },
	"team_create_request.json":     func() Validator { return &TeamCreateRequest{} },
	"team_create_result.json":      func() Validator { return &TeamCreateResult{} },
	"team_join_request.json":       func() Validator { return &TeamJoinRequest{} },
	"team_join_result.json":        func() Validator { return &TeamJoinResult{} },
	"team_leave_result.json":       func() Validator { return &TeamLeaveResult{} },
	"envelope_ok.json":             func() Validator { return &Envelope{} },
	"envelope_error.json":          func() Validator { return &Envelope{} },
}

// normalize parses a JSON document into plain Go values and prunes every
// object member whose value is null, recursively. Explicit null and an
// absent member are the one equivalence this package's round trip is
// allowed: an optional member the plan prints as null marshals as absent
// (omitzero on a nil pointer). Everything else must survive byte-for-... —
// value-for-value.
func normalize(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return pruneNulls(v)
}

func pruneNulls(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		for k, m := range vv {
			if m == nil {
				delete(vv, k)
				continue
			}
			vv[k] = pruneNulls(m)
		}
		return vv
	case []any:
		for i, m := range vv {
			vv[i] = pruneNulls(m)
		}
		return vv
	default:
		return v
	}
}

func readExample(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "examples", name))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	return data
}

// TestEveryExampleFileIsCovered fails when a file exists without a table
// entry or a table entry without a file, so neither can silently rot.
func TestEveryExampleFileIsCovered(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(filepath.Join("testdata", "examples"))
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}
	var onDisk []string
	for _, e := range entries {
		onDisk = append(onDisk, e.Name())
		if _, ok := examples[e.Name()]; !ok {
			t.Errorf("example %s has no round-trip table entry", e.Name())
		}
	}
	sort.Strings(onDisk)
	for name := range examples {
		found := false
		for _, d := range onDisk {
			if d == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("table entry %s has no example file", name)
		}
	}
}

// TestExamplesRoundTrip is the core 4.4 conformance check: every literal
// plan example unmarshals into its Go type, validates, marshals back, and
// the re-marshalled document is semantically identical to the ORIGINAL
// file — original-vs-remarshal, not struct-vs-struct, so a member the
// struct silently drops changes the comparison and fails the test. The
// one accepted difference is explicit null vs absent (see normalize); the
// members the plan prints as null get non-null coverage in
// TestNullableMembersRoundTripWithValues.
func TestExamplesRoundTrip(t *testing.T) {
	t.Parallel()
	for name, fresh := range examples {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := readExample(t, name)
			v := fresh()
			if err := Unmarshal(data, v); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if err := v.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			want := normalize(t, data)
			got := normalize(t, out)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip lost or changed members:\nexample: %#v\nremarshal: %#v", want, got)
			}
		})
	}
}

// TestRoundTripComparatorCanFail is the positive control on the
// instrument above: for every non-null top-level member of every example,
// deleting it from the parsed example must make the comparison fail. If
// the Go type silently dropped that member the remarshal would lack it
// too, the two sides would agree, and THIS test — not just the round-trip
// test — goes red. A comparator that cannot fail proves nothing (the
// P1-1 tautology lesson).
func TestRoundTripComparatorCanFail(t *testing.T) {
	t.Parallel()
	for name, fresh := range examples {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := readExample(t, name)
			v := fresh()
			if err := Unmarshal(data, v); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			got := normalize(t, out)
			top, ok := normalize(t, data).(map[string]any)
			if !ok {
				t.Fatalf("example is not a JSON object")
			}
			for member := range top {
				mutated := normalize(t, data).(map[string]any)
				delete(mutated, member)
				if reflect.DeepEqual(mutated, got) {
					t.Errorf("deleting %q from the example changed nothing: the comparator cannot see that member (is it silently dropped by the Go type?)", member)
				}
			}
		})
	}
}

// TestNullableMembersRoundTripWithValues covers the members the plan's
// examples print as null, which the null-pruning comparator cannot see:
// each must survive a round trip when it carries a value.
func TestNullableMembersRoundTripWithValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		doc   string
		fresh func() Validator
	}{
		{
			name: "registration with description and workspace label",
			doc: `{"harness":"claude-code","harness_version":"2.1.251","session_name":"payments-api",` +
				`"session_description":"tenant migration","activity":"busy","inbound":"accept",` +
				`"lease_seconds":90,"workspace_label":"repo:payments"}`,
			fresh: func() Validator { return &SessionRegistration{} },
		},
		{
			name: "record with description and workspace label",
			doc: `{"session_id":"s1","session_name":"payments-api","session_description":"d",` +
				`"principal_ref":"p1","human_label":"alice@example.com","state":"active","activity":"busy",` +
				`"inbound":"accept","last_seen_at":"2026-08-30T12:00:00Z","lease_until":"2026-08-30T12:01:30Z",` +
				`"harness":"claude-code","harness_version":"2.1.251","workspace_label":"repo:payments",` +
				`"created_at":"2026-08-30T11:55:00Z","is_self":true}`,
			fresh: func() Validator { return &SessionRecord{} },
		},
		{
			// Zero is a value the comparator can see: an empty context is
			// 0 on the wire, never pruned like a null.
			name: "record with a zero context_used_tokens and no model",
			doc: `{"session_id":"s1","session_name":"payments-api","principal_ref":"p1",` +
				`"human_label":"alice@example.com","state":"idle","activity":"idle","inbound":"accept",` +
				`"last_seen_at":"2026-08-30T12:00:00Z","lease_until":"2026-08-30T12:01:30Z",` +
				`"context_used_tokens":0,"created_at":"2026-08-30T11:55:00Z","is_self":false}`,
			fresh: func() Validator { return &SessionRecord{} },
		},
		{
			name:  "heartbeat carrying only model and context_used_tokens",
			doc:   `{"model":"claude-sonnet-5","context_used_tokens":2048}`,
			fresh: func() Validator { return &HeartbeatRequest{} },
		},
		{
			name:  "watch heartbeat command carrying only model and context_used_tokens",
			doc:   `{"type":"heartbeat","model":"claude-sonnet-5","context_used_tokens":0}`,
			fresh: func() Validator { return &WatchCommand{} },
		},
		{
			name: "envelope with reply_to",
			doc: `{"protocol_version":"1","kind":"text","message_id":"m2","team_ref":"t1",` +
				`"sender":{"principal_ref":"p1","human_label":"alice@example.com","session_id":"s1","session_name":"payments-api"},` +
				`"recipient_session_id":"s2","summary":"Re: migration","body":"ack",` +
				`"reply_to":"m1","hop_count":1,"created_at":"2026-08-30T12:00:10Z","delivery_state":"accepted"}`,
			fresh: func() Validator { return &MessageEnvelope{} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := tc.fresh()
			if err := Decode([]byte(tc.doc), v); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			want := normalize(t, []byte(tc.doc))
			got := normalize(t, out)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip lost members:\nwant %#v\ngot  %#v", want, got)
			}
		})
	}
}

// TestUnknownMemberIsIgnoredNotFatal pins D16 loose parsing, measured
// rather than assumed: a document carrying an unknown member parses
// without error, the known members are intact, and the unknown member is
// GONE from the re-marshalled document — ignored by design, not
// preserved. (This is exactly why SendRequest must declare the C-23
// members explicitly: without a declared field, presence is unobservable.)
func TestUnknownMemberIsIgnoredNotFatal(t *testing.T) {
	t.Parallel()
	doc := `{"message_ids":["m1"],"a_member_from_protocol_2":{"nested":true}}`
	var req AckRequest
	if err := Decode([]byte(doc), &req); err != nil {
		t.Fatalf("Decode with unknown member: %v", err)
	}
	if len(req.MessageIDs) != 1 || req.MessageIDs[0] != "m1" {
		t.Fatalf("known member damaged by unknown neighbour: %#v", req)
	}
	out, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if _, present := back["a_member_from_protocol_2"]; present {
		t.Fatalf("unknown member was preserved; loose parsing ignores it by design (D16): %s", out)
	}
	if _, present := back["message_ids"]; !present {
		t.Fatalf("known member lost: %s", out)
	}
}

// TestWronglyCasedMemberIsZero pins the other half of D16's bargain:
// member names are case-sensitive, a wrongly-cased member is silently
// zero, and it is Validate — not the parser — that catches the resulting
// hole.
func TestWronglyCasedMemberIsZero(t *testing.T) {
	t.Parallel()
	var req AckRequest
	if err := Unmarshal([]byte(`{"Message_IDs":["m1"]}`), &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if req.MessageIDs != nil {
		t.Fatalf("wrongly-cased member matched: %#v", req.MessageIDs)
	}
	err := req.Validate()
	requireInvalidInput(t, err, "message_ids")
}
