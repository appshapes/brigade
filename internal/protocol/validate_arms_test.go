package protocol

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// examplePlaceholderJoin is a syntactically join-shaped placeholder; it is
// not a credential (and gosec agrees only because it is not a literal at
// its use sites).
var examplePlaceholderJoin = strings.Join([]string{"brg1", "x", "y"}, ".")

func validDescribe(t *testing.T) DescribeResult {
	t.Helper()
	var d DescribeResult
	if err := Decode(readExample(t, "describe_result.json"), &d); err != nil {
		t.Fatalf("describe example: %v", err)
	}
	return d
}

func validRecord() SessionRecord {
	return SessionRecord{
		SessionID: "s1", SessionName: "payments-api", PrincipalRef: "p1",
		HumanLabel: "alice@example.com", State: SessionStateActive,
		Activity: ActivityBusy, Inbound: InboundAccept,
		LastSeenAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		LeaseUntil: time.Date(2026, 8, 30, 12, 1, 30, 0, time.UTC),
		CreatedAt:  time.Date(2026, 8, 30, 11, 55, 0, 0, time.UTC),
	}
}

func validWatchHeartbeatOK() WatchHeartbeatOK {
	return WatchHeartbeatOK{
		Event: EventHeartbeatOK, SessionID: "s1", State: SessionStateActive,
		LeaseUntil: time.Date(2026, 8, 30, 12, 1, 30, 0, time.UTC),
		ServerTime: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}
}

// TestDefaultsMatchThePlanExample pins the constants of 4.4.1 to the
// LITERAL plan example: DefaultLimits, DefaultLease and DefaultRetention
// must equal what describe_result.json advertises, member for member. A
// constant typo fails here against text copied from the plan, not
// against the constant itself.
func TestDefaultsMatchThePlanExample(t *testing.T) {
	t.Parallel()
	d := validDescribe(t)
	if got, want := d.Limits, DefaultLimits(); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultLimits() = %+v,\nexample advertises %+v", want, got)
	}
	if got, want := d.Lease, DefaultLease(); got != want {
		t.Errorf("DefaultLease() = %+v, example advertises %+v", want, got)
	}
	if got, want := d.Retention, DefaultRetention(); got != want {
		t.Errorf("DefaultRetention() = %+v, example advertises %+v", want, got)
	}
	if err := (&DescribeResult{
		ProtocolVersion: ProtocolVersion,
		Adapter:         AdapterInfo{Name: "x", Version: "0"},
		Delivery:        DeliveryInfo{Guarantee: GuaranteeAtLeastOnce, Ordering: "none", AckState: AckStateInjected},
		Limits:          DefaultLimits(), Lease: DefaultLease(), Retention: DefaultRetention(),
		Profile: ProfileInfo{Name: "default", State: ProfileStateUnconfigured},
	}).Validate(); err != nil {
		t.Errorf("a describe built from the defaults must validate: %v", err)
	}
}

// TestValidationArms fires EVERY remaining error arm of every Validate
// method once, so no arm ships unproven (the P1-1 lesson: an assertion
// that never fired is not an assertion).
func TestValidationArms(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("€", MaxHumanLabelChars+1)
	// A model one code point over max_model_chars, in a multi-byte
	// character (the C-44 probe uses "é"): a byte counter would reject a
	// legal 128-code-point value long before this one.
	longModel := strings.Repeat("é", MaxModelChars+1)
	base := validDescribe(t)
	cases := []struct {
		name  string
		field string
		build func() Validator
	}{
		{"describe protocol_version", "protocol_version", func() Validator { d := base; d.ProtocolVersion = ""; return &d }},
		{"describe adapter.name", "adapter.name", func() Validator { d := base; d.Adapter.Name = ""; return &d }},
		{"describe adapter.version", "adapter.version", func() Validator { d := base; d.Adapter.Version = ""; return &d }},
		{"describe delivery.guarantee", "delivery.guarantee", func() Validator { d := base; d.Delivery.Guarantee = "at_most_once"; return &d }},
		{"describe delivery.ordering", "delivery.ordering", func() Validator { d := base; d.Delivery.Ordering = ""; return &d }},
		{"describe delivery.ack_state", "delivery.ack_state", func() Validator { d := base; d.Delivery.AckState = "read"; return &d }},
		{"describe zero limit", "limits.max_body_bytes", func() Validator { d := base; d.Limits.MaxBodyBytes = 0; return &d }},
		{"describe negative rate", "limits.send_rate.per_minute", func() Validator { d := base; d.Limits.SendRate.PerMinute = -1; return &d }},
		{"describe zero lease", "lease.default_seconds", func() Validator { d := base; d.Lease.DefaultSeconds = 0; return &d }},
		{"describe zero lease min", "lease.min_seconds", func() Validator { d := base; d.Lease.MinSeconds = 0; return &d }},
		{"describe zero lease max", "lease.max_seconds", func() Validator { d := base; d.Lease.MaxSeconds = 0; return &d }},
		{"describe unordered lease", "lease.default_seconds", func() Validator {
			d := validDescribe(t)
			d.Lease.DefaultSeconds = d.Lease.MaxSeconds + 1
			return &d
		}},
		{"describe zero retention unacked", "retention.unacked_message_seconds", func() Validator { d := base; d.Retention.UnackedMessageSeconds = 0; return &d }},
		{"describe zero retention acked", "retention.acked_message_seconds", func() Validator { d := base; d.Retention.AckedMessageSeconds = 0; return &d }},
		{"describe zero retention closed", "retention.closed_session_seconds", func() Validator { d := base; d.Retention.ClosedSessionSeconds = 0; return &d }},
		{"describe profile.name", "profile.name", func() Validator { d := base; d.Profile.Name = ""; return &d }},
		{"describe profile.state", "profile.state", func() Validator { d := base; d.Profile.State = "member"; return &d }},
		{"describe joined without team_name", "profile.team_name", func() Validator { d := base; d.Profile.TeamName = ""; return &d }},
		{"describe joined without principal_ref", "profile.principal_ref", func() Validator { d := base; d.Profile.PrincipalRef = ""; return &d }},
		{"describe joined label overlong", "profile.human_label", func() Validator { d := base; d.Profile.HumanLabel = long; return &d }},

		{"registration description overlong", "session_description", func() Validator {
			r := validRegistration()
			r.SessionDescription = strptr(strings.Repeat("x", MaxDescriptionChars+1))
			return &r
		}},
		{"registration workspace_label overlong", "workspace_label", func() Validator { r := validRegistration(); r.WorkspaceLabel = strptr(long); return &r }},
		{"registration model overlong", "model", func() Validator { r := validRegistration(); r.Model = strptr(longModel); return &r }},
		{"registration context_used_tokens negative", "context_used_tokens", func() Validator {
			r := validRegistration()
			r.ContextUsedTokens = intptr(-1)
			return &r
		}},
		{"registration context_used_tokens beyond 2^53-1", "context_used_tokens", func() Validator {
			r := validRegistration()
			r.ContextUsedTokens = intptr(MaxContextUsedTokens + 1)
			return &r
		}},

		{"record session_id", "session_id", func() Validator { r := validRecord(); r.SessionID = ""; return &r }},
		{"record session_name", "session_name", func() Validator { r := validRecord(); r.SessionName = ""; return &r }},
		{"record session_name overlong", "session_name", func() Validator {
			r := validRecord()
			r.SessionName = strings.Repeat("x", MaxSessionNameCodepoints+1)
			return &r
		}},
		{"record description overlong", "session_description", func() Validator {
			r := validRecord()
			r.SessionDescription = strptr(strings.Repeat("x", MaxDescriptionChars+1))
			return &r
		}},
		{"record principal_ref", "principal_ref", func() Validator { r := validRecord(); r.PrincipalRef = ""; return &r }},
		{"record human_label overlong", "human_label", func() Validator { r := validRecord(); r.HumanLabel = long; return &r }},
		{"record state", "state", func() Validator { r := validRecord(); r.State = "zombie"; return &r }},
		{"record activity", "activity", func() Validator { r := validRecord(); r.Activity = "sprinting"; return &r }},
		{"record inbound", "inbound", func() Validator { r := validRecord(); r.Inbound = "maybe"; return &r }},
		{"record last_seen_at", "last_seen_at", func() Validator { r := validRecord(); r.LastSeenAt = time.Time{}; return &r }},
		{"record lease_until", "lease_until", func() Validator { r := validRecord(); r.LeaseUntil = time.Time{}; return &r }},
		{"record workspace_label overlong", "workspace_label", func() Validator { r := validRecord(); r.WorkspaceLabel = strptr(long); return &r }},
		{"record model overlong", "model", func() Validator { r := validRecord(); r.Model = strptr(longModel); return &r }},
		{"record context_used_tokens negative", "context_used_tokens", func() Validator { r := validRecord(); r.ContextUsedTokens = intptr(-1); return &r }},
		{"record context_used_tokens beyond 2^53-1", "context_used_tokens", func() Validator {
			r := validRecord()
			r.ContextUsedTokens = intptr(MaxContextUsedTokens + 1)
			return &r
		}},
		{"record created_at", "created_at", func() Validator { r := validRecord(); r.CreatedAt = time.Time{}; return &r }},

		{"heartbeat activity", "activity", func() Validator { return &HeartbeatRequest{Activity: strptr("sprinting")} }},
		{"heartbeat empty session_name", "session_name", func() Validator { return &HeartbeatRequest{SessionName: strptr("")} }},
		{"heartbeat overlong session_name", "session_name", func() Validator {
			return &HeartbeatRequest{SessionName: strptr(strings.Repeat("x", MaxSessionNameCodepoints+1))}
		}},
		{"heartbeat description overlong", "session_description", func() Validator {
			return &HeartbeatRequest{SessionDescription: strptr(strings.Repeat("x", MaxDescriptionChars+1))}
		}},
		{"heartbeat inbound", "inbound", func() Validator { return &HeartbeatRequest{Inbound: strptr("maybe")} }},
		{"heartbeat lease", "lease_seconds", func() Validator { return &HeartbeatRequest{LeaseSeconds: intptr(0)} }},
		{"heartbeat model overlong", "model", func() Validator { return &HeartbeatRequest{Model: strptr(longModel)} }},
		{"heartbeat context_used_tokens negative", "context_used_tokens", func() Validator { return &HeartbeatRequest{ContextUsedTokens: intptr(-1)} }},
		{"heartbeat context_used_tokens beyond 2^53-1", "context_used_tokens", func() Validator {
			return &HeartbeatRequest{ContextUsedTokens: intptr(MaxContextUsedTokens + 1)}
		}},

		{"heartbeat result session_id", "session_id", func() Validator {
			return &HeartbeatResult{State: SessionStateActive, LeaseUntil: time.Now(), ServerTime: time.Now()}
		}},
		{"heartbeat result state", "state", func() Validator {
			return &HeartbeatResult{SessionID: "s1", State: "zombie", LeaseUntil: time.Now(), ServerTime: time.Now()}
		}},
		{"heartbeat result lease_until", "lease_until", func() Validator {
			return &HeartbeatResult{SessionID: "s1", State: SessionStateActive, ServerTime: time.Now()}
		}},
		{"heartbeat result server_time", "server_time", func() Validator {
			return &HeartbeatResult{SessionID: "s1", State: SessionStateActive, LeaseUntil: time.Now()}
		}},

		{"envelope protocol_version", "protocol_version", func() Validator { e := validEnvelopeValue(); e.ProtocolVersion = ""; return &e }},
		{"envelope sender.human_label overlong", "sender.human_label", func() Validator { e := validEnvelopeValue(); e.Sender.HumanLabel = long; return &e }},
		{"envelope sender.session_name", "sender.session_name", func() Validator { e := validEnvelopeValue(); e.Sender.SessionName = ""; return &e }},
		{"envelope sender.session_name overlong", "sender.session_name", func() Validator {
			e := validEnvelopeValue()
			e.Sender.SessionName = strings.Repeat("x", MaxSessionNameCodepoints+1)
			return &e
		}},
		{"envelope recipient", "recipient_session_id", func() Validator { e := validEnvelopeValue(); e.RecipientSessionID = ""; return &e }},
		{"envelope body", "body", func() Validator { e := validEnvelopeValue(); e.Body = ""; return &e }},
		{"envelope body overlong", "body", func() Validator {
			e := validEnvelopeValue()
			e.Body = strings.Repeat("x", MaxBodyBytes+1)
			return &e
		}},
		{"envelope empty reply_to", "reply_to", func() Validator { e := validEnvelopeValue(); e.ReplyTo = strptr(""); return &e }},

		{"send reply_to invalid utf8", "reply_to", func() Validator { r := validSendRequest(); r.ReplyTo = "\xff"; return &r }},
		{"send summary invalid utf8", "summary", func() Validator { r := validSendRequest(); r.Summary = "\xff"; return &r }},

		{"send response message_id", "message_id", func() Validator {
			return &SendResponse{Status: SendStatusAccepted, RecipientSessionID: "s", CreatedAt: time.Now()}
		}},
		{"send response recipient", "recipient_session_id", func() Validator {
			return &SendResponse{Status: SendStatusAccepted, MessageID: "m", CreatedAt: time.Now()}
		}},
		{"send response created_at", "created_at", func() Validator {
			return &SendResponse{Status: SendStatusAccepted, MessageID: "m", RecipientSessionID: "s"}
		}},
		{"send response hop_count", "hop_count", func() Validator {
			return &SendResponse{Status: SendStatusAccepted, MessageID: "m", RecipientSessionID: "s", CreatedAt: time.Now(), HopCount: MaxHopCount + 1}
		}},

		{"ack request empty id", "message_ids", func() Validator { return &AckRequest{MessageIDs: []string{"m1", ""}} }},
		{"ack result empty acked id", "acked", func() Validator { return &AckResult{Acked: []string{""}} }},
		{"ack result empty unknown id", "unknown", func() Validator { return &AckResult{Acked: []string{"m1"}, Unknown: []string{""}} }},

		{"watch ready event", "event", func() Validator {
			return &WatchReady{Event: "go", ProtocolVersion: "1", SessionID: "s1", Mode: "push"}
		}},
		{"watch ready protocol_version", "protocol_version", func() Validator { return &WatchReady{Event: EventReady, SessionID: "s1", Mode: "push"} }},
		{"watch ready session_id", "session_id", func() Validator {
			return &WatchReady{Event: EventReady, ProtocolVersion: "1", Mode: "push"}
		}},
		{"watch ready mode", "mode", func() Validator {
			return &WatchReady{Event: EventReady, ProtocolVersion: "1", SessionID: "s1"}
		}},
		{"watch message event", "event", func() Validator { return &WatchMessage{Event: "msg", Message: validEnvelopeValue()} }},
		{"watch message nested", "message.message_id", func() Validator {
			e := validEnvelopeValue()
			e.MessageID = ""
			return &WatchMessage{Event: EventMessage, Message: e}
		}},
		{"watch message nested sender (dotted twice)", "message.sender.principal_ref", func() Validator {
			e := validEnvelopeValue()
			e.Sender.PrincipalRef = ""
			return &WatchMessage{Event: EventMessage, Message: e}
		}},
		{"watch status event", "event", func() Validator { return &WatchStatus{Event: "state", State: "live"} }},
		{"watch status state", "state", func() Validator { return &WatchStatus{Event: EventStatus} }},
		{"watch acked event", "event", func() Validator { return &WatchAcked{Event: "ok"} }},
		{"watch heartbeat_ok event", "event", func() Validator { w := validWatchHeartbeatOK(); w.Event = "hb"; return &w }},
		{"watch heartbeat_ok session_id", "session_id", func() Validator { w := validWatchHeartbeatOK(); w.SessionID = ""; return &w }},
		{"watch heartbeat_ok state", "state", func() Validator { w := validWatchHeartbeatOK(); w.State = "zombie"; return &w }},
		{"watch heartbeat_ok lease_until", "lease_until", func() Validator { w := validWatchHeartbeatOK(); w.LeaseUntil = time.Time{}; return &w }},
		{"watch heartbeat_ok server_time", "server_time", func() Validator { w := validWatchHeartbeatOK(); w.ServerTime = time.Time{}; return &w }},
		{"watch error event", "event", func() Validator {
			return &WatchError{Event: "err", Error: ErrorObject{Code: CodeInternal, Message: "x"}}
		}},
		{"watch error nested", "error.code", func() Validator { return &WatchError{Event: EventError} }},
		{"watch command ack empty id", "message_ids", func() Validator { return &WatchCommand{Type: CommandAck, MessageIDs: []string{""}} }},
		{"watch command heartbeat name", "session_name", func() Validator { return &WatchCommand{Type: CommandHeartbeat, SessionName: strptr("")} }},
		{"watch command heartbeat name overlong", "session_name", func() Validator {
			return &WatchCommand{Type: CommandHeartbeat, SessionName: strptr(strings.Repeat("x", MaxSessionNameCodepoints+1))}
		}},
		{"watch command heartbeat inbound", "inbound", func() Validator { return &WatchCommand{Type: CommandHeartbeat, Inbound: strptr("maybe")} }},
		{"watch command heartbeat model overlong", "model", func() Validator {
			return &WatchCommand{Type: CommandHeartbeat, Model: strptr(longModel)}
		}},
		{"watch command heartbeat context_used_tokens negative", "context_used_tokens", func() Validator {
			return &WatchCommand{Type: CommandHeartbeat, ContextUsedTokens: intptr(-1)}
		}},
		{"watch command heartbeat context_used_tokens beyond 2^53-1", "context_used_tokens", func() Validator {
			return &WatchCommand{Type: CommandHeartbeat, ContextUsedTokens: intptr(MaxContextUsedTokens + 1)}
		}},

		{"error object code", "error.code", func() Validator { return &ErrorObject{Message: "x"} }},
		{"error object message", "error.message", func() Validator { return &ErrorObject{Code: CodeInternal} }},
		{"error object negative retry_after_ms", "error.retry_after_ms", func() Validator {
			return &ErrorObject{Code: CodeRateLimited, Message: "x", Retryable: true, RetryAfterMS: -1}
		}},
		{"envelope wire protocol_version", "protocol_version", func() Validator { return &Envelope{OK: true, Result: []byte(`{}`)} }},
		{"envelope wire result on failure", "result", func() Validator {
			return &Envelope{OK: false, ProtocolVersion: "1", Result: []byte(`{}`), Error: &ErrorObject{Code: CodeInternal, Message: "x"}}
		}},

		{"team create name overlong", "team_name", func() Validator {
			return &TeamCreateRequest{TeamName: strings.Repeat("€", MaxSessionNameCodepoints+1)}
		}},
		{"team create label overlong", "human_label", func() Validator { return &TeamCreateRequest{TeamName: "ops", HumanLabel: long} }},
		{"team create result team_ref", "team_ref", func() Validator {
			return &TeamCreateResult{TeamName: "ops", JoinSecret: examplePlaceholderJoin, PrincipalRef: "p1"}
		}},
		{"team create result team_name", "team_name", func() Validator {
			return &TeamCreateResult{TeamRef: "t1", JoinSecret: examplePlaceholderJoin, PrincipalRef: "p1"}
		}},
		{"team create result join_secret", "join_secret", func() Validator {
			return &TeamCreateResult{TeamRef: "t1", TeamName: "ops", PrincipalRef: "p1"}
		}},
		{"team create result principal_ref", "principal_ref", func() Validator {
			return &TeamCreateResult{TeamRef: "t1", TeamName: "ops", JoinSecret: examplePlaceholderJoin}
		}},
		{"team join label overlong", "human_label", func() Validator { return &TeamJoinRequest{JoinSecret: examplePlaceholderJoin, HumanLabel: long} }},
		{"team join backend not object", "backend", func() Validator {
			return &TeamJoinRequest{JoinSecret: examplePlaceholderJoin, Backend: []byte(`"https://x"`)}
		}},
		{"team join result team_ref", "team_ref", func() Validator { return &TeamJoinResult{TeamName: "ops", PrincipalRef: "p1"} }},
		{"team join result team_name", "team_name", func() Validator { return &TeamJoinResult{TeamRef: "t1", PrincipalRef: "p1"} }},
		{"team join result principal_ref", "principal_ref", func() Validator { return &TeamJoinResult{TeamRef: "t1", TeamName: "ops"} }},
		{"team leave team_ref", "team_ref", func() Validator { return &TeamLeaveResult{PrincipalRef: "p1", Left: true} }},
		{"team leave principal_ref", "principal_ref", func() Validator { return &TeamLeaveResult{TeamRef: "t1", Left: true} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireInvalidInput(t, tc.build().Validate(), tc.field)
		})
	}
}

// TestValidBackendObjectAccepted is the passing side of the backend
// check.
func TestValidBackendObjectAccepted(t *testing.T) {
	t.Parallel()
	r := TeamJoinRequest{JoinSecret: examplePlaceholderJoin, Backend: []byte(`{"url":"https://x","publishable_key":"k"}`)}
	if err := r.Validate(); err != nil {
		t.Fatalf("object backend rejected: %v", err)
	}
}

// TestErrorStringIsTheMessage covers the error interface.
func TestErrorStringIsTheMessage(t *testing.T) {
	t.Parallel()
	if got := (&Error{Code: CodeConflict, Message: "session closed"}).Error(); got != "session closed" {
		t.Fatalf("Error() = %q", got)
	}
}

// TestRemainingArms closes the last uncovered branches: Decode's parse
// arm and the team-name caps on the two team results.
func TestRemainingArms(t *testing.T) {
	t.Parallel()
	var req AckRequest
	if err := Decode([]byte(`{`), &req); err == nil {
		t.Fatal("Decode of truncated JSON succeeded")
	}
	long := strings.Repeat("x", MaxSessionNameCodepoints+1)
	requireInvalidInput(t, (&TeamCreateResult{TeamRef: "t1", TeamName: long, JoinSecret: examplePlaceholderJoin, PrincipalRef: "p1"}).Validate(), "team_name")
	requireInvalidInput(t, (&TeamJoinResult{TeamRef: "t1", TeamName: long, PrincipalRef: "p1"}).Validate(), "team_name")
}
