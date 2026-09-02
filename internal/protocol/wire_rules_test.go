package protocol

import (
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// This file pins the wire rules the owner settled for protocol v1 (P1-4
// decisions 1-6, .context/plans/brigade-execution-log.md). Every cap and
// key name below is written as a LITERAL where the decision names one,
// not as the constant it is expected to equal: a constant that drifts
// fails here against the decision's own number, not against itself.

// detailsOf returns the details map of a *Error, failing the test when
// err is not one.
func detailsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var perr *Error
	if !errors.As(err, &perr) {
		t.Fatalf("want *protocol.Error, got %T: %v", err, err)
	}
	if perr.Code != CodeInvalidInput {
		t.Fatalf("code = %q, want invalid_input", perr.Code)
	}
	return perr.Details
}

// ---------------------------------------------------------------------
// Decision 1: team_name and workspace_label have their OWN caps, 64 and
// 128 code points, published in `limits` as max_team_name_codepoints and
// max_workspace_label_chars.
// ---------------------------------------------------------------------

func TestDecision1TeamNameCapIs64Codepoints(t *testing.T) {
	t.Parallel()
	euro := "€" // 3 bytes, 1 code point: a byte counter would fail the 64 case
	at := strings.Repeat(euro, 64)
	over := strings.Repeat(euro, 65)
	shapes := []struct {
		name  string
		field string
		build func(teamName string) Validator
	}{
		{"team create request", "team_name", func(n string) Validator { return &TeamCreateRequest{TeamName: n} }},
		{"team create result", "team_name", func(n string) Validator {
			return &TeamCreateResult{TeamRef: "t1", TeamName: n, JoinSecret: examplePlaceholderJoin, PrincipalRef: "p1"}
		}},
		{"team join result", "team_name", func(n string) Validator {
			return &TeamJoinResult{TeamRef: "t1", TeamName: n, PrincipalRef: "p1"}
		}},
		{"describe profile (joined)", "profile.team_name", func(n string) Validator {
			d := validDescribe(t)
			d.Profile.TeamName = n
			return &d
		}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			if err := s.build(at).Validate(); err != nil {
				t.Fatalf("a 64-code-point team name was rejected: %v", err)
			}
			err := s.build(over).Validate()
			requireInvalidInput(t, err, s.field)
			d := detailsOf(t, err)
			if d["limit"] != "64" || d["unit"] != "codepoints" || d["actual"] != "65" || d["reason"] != "too_long" {
				t.Fatalf("details = %v, want limit 64 codepoints, actual 65, reason too_long", d)
			}
		})
	}
	if MaxTeamNameCodepoints != 64 {
		t.Fatalf("MaxTeamNameCodepoints = %d, decision 1 says 64", MaxTeamNameCodepoints)
	}
}

func TestDecision1WorkspaceLabelCapIs128Chars(t *testing.T) {
	t.Parallel()
	euro := "€"
	at := strings.Repeat(euro, 128)
	over := strings.Repeat(euro, 129)
	shapes := []struct {
		name  string
		build func(label string) Validator
	}{
		{"registration", func(l string) Validator { r := validRegistration(); r.WorkspaceLabel = strptr(l); return &r }},
		{"record", func(l string) Validator { r := validRecord(); r.WorkspaceLabel = strptr(l); return &r }},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			if err := s.build(at).Validate(); err != nil {
				t.Fatalf("a 128-code-point workspace label was rejected: %v", err)
			}
			err := s.build(over).Validate()
			requireInvalidInput(t, err, "workspace_label")
			d := detailsOf(t, err)
			if d["limit"] != "128" || d["unit"] != "codepoints" || d["actual"] != "129" {
				t.Fatalf("details = %v, want limit 128 codepoints, actual 129", d)
			}
		})
	}
	if MaxWorkspaceLabelChars != 128 {
		t.Fatalf("MaxWorkspaceLabelChars = %d, decision 1 says 128", MaxWorkspaceLabelChars)
	}
}

// TestDecision1LimitsPublishBothCaps: the two caps are wire members of
// `limits` under exactly these names, carry exactly these numbers, are
// advertised by the describe example, and are validated like every other
// limit (a zero is rejected naming the member).
func TestDecision1LimitsPublishBothCaps(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatal(err)
	}
	for member, want := range map[string]float64{
		"max_team_name_codepoints":  64,
		"max_workspace_label_chars": 128,
	} {
		got, ok := wire[member]
		if !ok {
			t.Errorf("limits has no %q member: %s", member, out)
			continue
		}
		if got != want {
			t.Errorf("limits.%s = %v, want %v", member, got, want)
		}
	}

	// The describe example — what an adapter author copies — advertises
	// both, read from the raw file so a struct that dropped the member
	// could not hide it.
	var example struct {
		Limits map[string]any `json:"limits"`
	}
	if err := json.Unmarshal(readExample(t, "describe_result.json"), &example); err != nil {
		t.Fatal(err)
	}
	if example.Limits["max_team_name_codepoints"] != 64.0 || example.Limits["max_workspace_label_chars"] != 128.0 {
		t.Errorf("describe_result.json limits = %v, want max_team_name_codepoints 64 and max_workspace_label_chars 128", example.Limits)
	}

	// Validated like every other limit.
	d := validDescribe(t)
	d.Limits.MaxTeamNameCodepoints = 0
	requireInvalidInput(t, d.Validate(), "limits.max_team_name_codepoints")
	d = validDescribe(t)
	d.Limits.MaxWorkspaceLabelChars = 0
	requireInvalidInput(t, d.Validate(), "limits.max_workspace_label_chars")
}

// ---------------------------------------------------------------------
// Decision 2: retryable is a plain bool. Producers MUST emit it;
// consumers treat absent as false; Code.Retryable() is the source of
// truth and the wire flag is advisory.
// ---------------------------------------------------------------------

func TestDecision2RetryableRule(t *testing.T) {
	t.Parallel()

	t.Run("absent parses as false and validates", func(t *testing.T) {
		t.Parallel()
		var env Envelope
		doc := `{"ok":false,"protocol_version":"1","error":{"code":"not_found","message":"no such session"}}`
		if err := Decode([]byte(doc), &env); err != nil {
			t.Fatalf("an envelope with retryable absent must validate (decision 2): %v", err)
		}
		if env.Error.Retryable {
			t.Fatal("absent retryable parsed as true; consumers treat absent as false")
		}
		var we WatchError
		if err := Decode([]byte(`{"event":"error","error":{"code":"unauthenticated","message":"x"}}`), &we); err != nil {
			t.Fatalf("a watch error with retryable absent must validate: %v", err)
		}
		if we.Error.Retryable {
			t.Fatal("absent retryable parsed as true on a watch error")
		}
		// A producer MUST NOT send null (the schema says boolean), but a
		// consumer that meets one still lands on false: the plain bool
		// takes its zero value, the same fail-safe as absence.
		if err := Decode([]byte(`{"ok":false,"protocol_version":"1","error":{"code":"conflict","message":"x","retryable":null}}`), &env); err != nil {
			t.Fatalf("retryable: null must parse (loose consumer): %v", err)
		}
		if env.Error.Retryable {
			t.Fatal("retryable: null parsed as true")
		}
	})

	t.Run("producers always emit it, false included", func(t *testing.T) {
		t.Parallel()
		out, err := json.Marshal((&Error{Code: CodeNotFound, Message: "no"}).Object())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"retryable":false`) {
			t.Fatalf("a false retryable was omitted from the wire: %s (producers MUST emit it)", out)
		}
		out, err = json.Marshal(&ErrorObject{Code: CodeInternal, Message: "boom"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"retryable":false`) {
			t.Fatalf("a zero-value ErrorObject omitted retryable: %s", out)
		}
	})

	t.Run("Code.Retryable is the source of truth", func(t *testing.T) {
		t.Parallel()
		all := []Code{
			CodeInternal, CodeUsage, CodeInvalidInput, CodeUnauthenticated,
			CodeUnauthorized, CodeNotFound, CodeConflict, CodeRateLimited,
			CodeUnavailable, CodeProtocolMismatch, CodeConfig, CodeLoopDetected,
		}
		retryable := 0
		for _, c := range all {
			if got := (&Error{Code: c, Message: "m"}).Object().Retryable; got != c.Retryable() {
				t.Errorf("%s: Object().Retryable = %v, Code.Retryable() = %v", c, got, c.Retryable())
			}
			if c.Retryable() {
				retryable++
				if c != CodeRateLimited && c != CodeUnavailable {
					t.Errorf("%s is retryable; only rate_limited and unavailable ever are", c)
				}
			}
		}
		if retryable != 2 {
			t.Errorf("%d retryable codes, want exactly 2 (rate_limited, unavailable)", retryable)
		}

		// The wire flag is advisory: a producer that lies is not rejected
		// by Validate (loose parsing cannot police a bool), and a consumer
		// that derives from the code gets the right answer anyway.
		var env Envelope
		if err := Decode([]byte(`{"ok":false,"protocol_version":"1","error":{"code":"not_found","message":"x","retryable":true}}`), &env); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if env.Error.Retryable != true || env.Error.Code.Retryable() != false {
			t.Fatalf("wire says %v, code says %v; want the flag carried as sent and the code to answer false", env.Error.Retryable, env.Error.Code.Retryable())
		}
		if err := Decode([]byte(`{"ok":false,"protocol_version":"1","error":{"code":"unavailable","message":"x","retryable":false}}`), &env); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if !env.Error.Code.Retryable() {
			t.Fatal("unavailable must derive as retryable whatever the wire flag says")
		}
	})
}

// ---------------------------------------------------------------------
// Decision 3: every invalid_input raised by validation carries
// details.field = the wire member name (JSON name, dotted for nesting);
// the companion keys are reason, limit, actual, unit. Those five key
// NAMES are frozen by the decision (spec 4.3.1): a typo in one is a
// protocol break. The reason TOKENS and the extra keys `allowed`, `min`
// and `max` are the reference implementation's and are informative, not
// frozen (4.3.1 says so explicitly); they are pinned below as a
// regression guard for THIS package, so a rename is a deliberate act, not
// because an adapter in another language must emit the same strings.
// ---------------------------------------------------------------------

// frozenDetailKeys is the closed set of keys a validation failure's
// details may carry: decision 3's five, plus the informative `allowed`,
// `min` and `max` the enumeration and range arms add.
var frozenDetailKeys = map[string]bool{
	"field": true, "reason": true, "limit": true, "actual": true, "unit": true,
	"allowed": true, "min": true, "max": true,
}

// frozenReasons is the closed vocabulary of details.reason values a
// validation failure in this package may carry (adapterkit's stdin
// reader adds `terminal` and `read_error`, outside this package).
var frozenReasons = map[string]bool{
	"required": true, "too_long": true, "invalid_value": true, "not_utf8": true,
	"forbidden_member": true, "out_of_range": true, "malformed_json": true,
}

// wireMemberPath is the grammar of details.field: JSON member names as
// written on the wire, lowercase snake case, dotted for nesting.
var wireMemberPath = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

func TestDecision3InvalidInputDetailsAreFrozen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  func() error
		want map[string]string // exact map, not a subset
	}{
		{"required member", func() error { return (&AckRequest{}).Validate() },
			map[string]string{"field": "message_ids", "reason": "required"}},
		{"required nested member (dotted)", func() error {
			r := validRegistration()
			r.Resume = &ResumeRef{}
			return r.Validate()
		}, map[string]string{"field": "resume.session_id", "reason": "required"}},
		{"byte cap", func() error {
			r := validSendRequest()
			r.Body = strings.Repeat("€", 5462) // 16386 bytes
			return r.Validate()
		}, map[string]string{"field": "body", "reason": "too_long", "limit": "16384", "actual": "16386", "unit": "bytes"}},
		{"code-point cap", func() error {
			r := validSendRequest()
			r.Summary = strings.Repeat("€", 201)
			return r.Validate()
		}, map[string]string{"field": "summary", "reason": "too_long", "limit": "200", "actual": "201", "unit": "codepoints"}},
		{"code-point cap nested (dotted)", func() error {
			e := validEnvelopeValue()
			e.Sender.SessionName = strings.Repeat("x", 65)
			return e.Validate()
		}, map[string]string{"field": "sender.session_name", "reason": "too_long", "limit": "64", "actual": "65", "unit": "codepoints"}},
		{"enumeration", func() error {
			return (&WatchReady{Event: EventReady, ProtocolVersion: "1", SessionID: "s1", Mode: "streaming"}).Validate()
		}, map[string]string{"field": "mode", "reason": "invalid_value", "allowed": "push, polling"}},
		{"not utf8", func() error {
			r := validSendRequest()
			r.ReplyTo = "\xff"
			return r.Validate()
		}, map[string]string{"field": "reply_to", "reason": "not_utf8"}},
		{"forbidden member (C-23)", func() error {
			var r SendRequest
			return Decode([]byte(`{"sender":null,"sender_session_id":"s1","recipient_session_id":"s2","body":"b"}`), &r)
		}, map[string]string{"field": "sender", "reason": "forbidden_member"}},
		{"out of range (adapter lease range)", func() error {
			return DefaultLease().CheckSeconds("lease_seconds", intptr(1))
		}, map[string]string{"field": "lease_seconds", "reason": "out_of_range", "min": "30", "max": "600"}},
		{"out of range (protocol positivity)", func() error {
			r := validRegistration()
			r.LeaseSeconds = intptr(0)
			return r.Validate()
		}, map[string]string{"field": "lease_seconds", "reason": "out_of_range", "min": "1"}},
		{"conditional presence", func() error {
			p := ProfileInfo{Name: "default", State: ProfileStateNotMember, TeamRef: "t1"}
			return p.validate()
		}, map[string]string{"field": "profile.team_ref", "reason": "invalid_value"}},
		{"envelope exclusivity", func() error {
			return (&Envelope{OK: true, ProtocolVersion: "1", Result: []byte(`{}`), Error: &ErrorObject{Code: CodeInternal, Message: "x"}}).Validate()
		}, map[string]string{"field": "error", "reason": "invalid_value"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := detailsOf(t, tc.err())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("details = %v, want exactly %v", got, tc.want)
			}
			for k := range got {
				if !frozenDetailKeys[k] {
					t.Errorf("details key %q is outside the frozen set", k)
				}
			}
			if !frozenReasons[got["reason"]] {
				t.Errorf("details.reason %q is outside the frozen vocabulary", got["reason"])
			}
			if !wireMemberPath.MatchString(got["field"]) {
				t.Errorf("details.field %q is not a wire member path", got["field"])
			}
		})
	}

	// The one invalid_input with NO field: a document that does not parse
	// has no member to name. It carries reason only.
	t.Run("malformed document has reason but no field", func(t *testing.T) {
		t.Parallel()
		var r AckRequest
		got := detailsOf(t, Unmarshal([]byte(`{`), &r))
		if !reflect.DeepEqual(got, map[string]string{"reason": "malformed_json"}) {
			t.Fatalf("details = %v, want exactly {reason: malformed_json}", got)
		}
	})
}

// TestDecision3EveryValidateFieldIsAWireMemberPath sweeps every
// validation failure the arms tests can raise and checks the field
// grammar on all of them, so a Go field name or a prose label can never
// leak into details.field.
func TestDecision3EveryValidateFieldIsAWireMemberPath(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 300)
	failures := []Validator{
		&DescribeResult{}, &SessionRegistration{}, &SessionRecord{},
		&HeartbeatRequest{Activity: strptr("x")}, &HeartbeatResult{},
		&MessageEnvelope{}, &SendRequest{}, &SendResponse{}, &AckRequest{},
		&AckResult{Acked: []string{""}}, &WatchReady{}, &WatchMessage{},
		&WatchStatus{}, &WatchAcked{}, &WatchHeartbeatOK{}, &WatchError{},
		&WatchCommand{}, &TeamCreateRequest{}, &TeamCreateResult{},
		&TeamJoinRequest{}, &TeamJoinResult{}, &TeamLeaveResult{},
		&Envelope{}, &ErrorObject{},
		&TeamCreateRequest{TeamName: long},
		&SessionRegistration{Harness: "h", HarnessVersion: "v", SessionName: "n", Activity: ActivityBusy, Inbound: InboundAccept, WorkspaceLabel: strptr(long)},
	}
	for i, v := range failures {
		err := v.Validate()
		if err == nil {
			t.Fatalf("failure %d (%T) validated; the sweep needs it to fail", i, v)
		}
		d := detailsOf(t, err)
		if !wireMemberPath.MatchString(d["field"]) {
			t.Errorf("%T: details.field %q is not a wire member path", v, d["field"])
		}
		for k := range d {
			if !frozenDetailKeys[k] {
				t.Errorf("%T: details key %q is outside the frozen set", v, k)
			}
		}
		if !frozenReasons[d["reason"]] {
			t.Errorf("%T: details.reason %q is outside the frozen vocabulary", v, d["reason"])
		}
	}
}

// ---------------------------------------------------------------------
// Decision 4: ready.mode is the closed set {push, polling}; status.state
// is {live, polling} with unknown values IGNORED by the reader, never
// rejected, like an unknown event kind.
// ---------------------------------------------------------------------

func TestDecision4WatchReadyModeIsClosed(t *testing.T) {
	t.Parallel()
	ready := func(mode string) *WatchReady {
		return &WatchReady{Event: EventReady, ProtocolVersion: "1", SessionID: "s1", Mode: mode}
	}
	for _, ok := range []string{"push", "polling"} {
		if err := ready(ok).Validate(); err != nil {
			t.Errorf("mode %q rejected: %v", ok, err)
		}
	}
	if WatchModePush != "push" || WatchModePolling != "polling" {
		t.Fatalf("mode constants are %q/%q, decision 4 says push/polling", WatchModePush, WatchModePolling)
	}
	for _, bad := range []string{"", "streaming", "Push", "POLLING", "push "} {
		err := ready(bad).Validate()
		requireInvalidInput(t, err, "mode")
		if bad != "" && detailsOf(t, err)["reason"] != "invalid_value" {
			t.Errorf("mode %q: reason = %q, want invalid_value", bad, detailsOf(t, err)["reason"])
		}
	}
	// And off the wire, where it matters.
	var w WatchReady
	err := Decode([]byte(`{"event":"ready","protocol_version":"1","session_id":"s1","mode":"long_poll"}`), &w)
	requireInvalidInput(t, err, "mode")
	if err := Decode([]byte(`{"event":"ready","protocol_version":"1","session_id":"s1","mode":"polling"}`), &w); err != nil {
		t.Fatalf("a polling ready line was rejected: %v", err)
	}
}

func TestDecision4WatchStatusStateIsOpen(t *testing.T) {
	t.Parallel()
	if StatusStateLive != "live" || StatusStatePolling != "polling" {
		t.Fatalf("state constants are %q/%q, decision 4 says live/polling", StatusStateLive, StatusStatePolling)
	}
	for _, known := range []string{"live", "polling"} {
		s := WatchStatus{Event: EventStatus, State: known}
		if err := s.Validate(); err != nil {
			t.Errorf("state %q rejected: %v", known, err)
		}
		if !s.Known() {
			t.Errorf("Known() = false for %q", known)
		}
	}
	// Unknown: accepted by Validate, reported by Known, so the reader
	// ignores it with a log line instead of dying on it.
	var s WatchStatus
	if err := Decode([]byte(`{"event":"status","state":"reconnecting","detail":"CHANNEL_ERROR"}`), &s); err != nil {
		t.Fatalf("an unknown status state must not be rejected (decision 4): %v", err)
	}
	if s.Known() {
		t.Fatal("Known() = true for an unknown state")
	}
	// Empty is still a missing required member.
	requireInvalidInput(t, (&WatchStatus{Event: EventStatus}).Validate(), "state")
}

// ---------------------------------------------------------------------
// Decision 5: a REQUIRED array is always present, [] when empty; omitzero
// applies only to OPTIONAL members.
// ---------------------------------------------------------------------

// wireRootTypes lists every root wire shape, mirroring the schema
// generator's list, so the sweep below sees every array member.
func wireRootTypes() []any {
	return []any{
		Envelope{}, ErrorObject{}, DescribeResult{}, SessionRegistration{},
		SessionRecord{}, HeartbeatRequest{}, HeartbeatResult{}, MessageEnvelope{},
		SendRequest{}, SendResponse{}, AckRequest{}, AckResult{}, WatchReady{},
		WatchMessage{}, WatchStatus{}, WatchAcked{}, WatchHeartbeatOK{},
		WatchError{}, WatchCommand{}, TeamCreateRequest{}, TeamCreateResult{},
		TeamJoinRequest{}, TeamJoinResult{}, TeamLeaveResult{},
	}
}

type arrayMember struct {
	holder reflect.Type
	name   string
}

func (m arrayMember) String() string { return m.holder.Name() + "." + m.name }

// arrayMembers walks the wire types and classifies every slice-typed
// member (jsontext.Value is a []byte and is skipped: it is a raw JSON
// value, not an array member) as required (no omitzero) or optional.
func arrayMembers() (required, optional []arrayMember) {
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) || seen[t] {
			return
		}
		seen[t] = true
		for i := range t.NumField() {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			tag := f.Tag.Get("json")
			name, opts, _ := strings.Cut(tag, ",")
			if name == "-" || name == "" {
				continue
			}
			if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() != reflect.Uint8 {
				m := arrayMember{t, name}
				if strings.Contains(opts, "omitzero") || strings.Contains(opts, "omitempty") {
					optional = append(optional, m)
				} else {
					required = append(required, m)
				}
			}
			walk(f.Type)
		}
	}
	for _, root := range wireRootTypes() {
		walk(reflect.TypeOf(root))
	}
	return required, optional
}

// fieldIndexByWireName finds the struct field carrying the given JSON
// member name.
func fieldIndexByWireName(t *testing.T, holder reflect.Type, name string) int {
	t.Helper()
	for i := range holder.NumField() {
		wire, _, _ := strings.Cut(holder.Field(i).Tag.Get("json"), ",")
		if wire == name {
			return i
		}
	}
	t.Fatalf("%s has no member %q", holder.Name(), name)
	return -1
}

func TestDecision5RequiredArraysAreAlwaysPresent(t *testing.T) {
	t.Parallel()
	required, optional := arrayMembers()
	names := func(ms []arrayMember) []string {
		out := make([]string, 0, len(ms))
		for _, m := range ms {
			out = append(out, m.String())
		}
		sort.Strings(out)
		return out
	}
	// The classification itself is pinned: a new array member must be
	// placed on one side deliberately, and moving one across is a wire
	// change.
	wantRequired := []string{
		"AckRequest.message_ids", "AckResult.acked", "AckResult.unknown",
		"DescribeResult.capabilities", "WatchAcked.message_ids", "WatchAcked.unknown",
	}
	wantOptional := []string{"WatchCommand.message_ids"} // only the ack command carries it
	if got := names(required); !reflect.DeepEqual(got, wantRequired) {
		t.Fatalf("required arrays = %v, want %v", got, wantRequired)
	}
	if got := names(optional); !reflect.DeepEqual(got, wantOptional) {
		t.Fatalf("optional arrays = %v, want %v", got, wantOptional)
	}

	// Every required array marshals as [] from BOTH a nil and an empty
	// slice: the key is present and its value is an empty array.
	for _, m := range required {
		t.Run(m.String(), func(t *testing.T) {
			t.Parallel()
			for _, variant := range []string{"nil", "empty"} {
				holder := reflect.New(m.holder)
				if variant == "empty" {
					f := holder.Elem().Field(fieldIndexByWireName(t, m.holder, m.name))
					f.Set(reflect.MakeSlice(f.Type(), 0, 0))
				}
				out, err := json.Marshal(holder.Interface())
				if err != nil {
					t.Fatalf("%s slice: marshal: %v", variant, err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out, &doc); err != nil {
					t.Fatalf("%s slice: re-parse: %v", variant, err)
				}
				v, present := doc[m.name]
				if !present {
					t.Fatalf("%s slice: member %q absent from %s; a required array is always present", variant, m.name, out)
				}
				arr, isArr := v.([]any)
				if !isArr || len(arr) != 0 {
					t.Fatalf("%s slice: member %q = %v, want []", variant, m.name, v)
				}
			}
		})
	}

	// The optional one is genuinely omitted when empty, which is what
	// makes the heartbeat and close commands well-formed.
	out, err := json.Marshal(&WatchCommand{Type: CommandClose})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "message_ids") {
		t.Fatalf("close command carries message_ids: %s", out)
	}
}

// ---------------------------------------------------------------------
// Decision 6: `…` appears in the examples only inside opaque strings;
// every typed member carries a real value (times are RFC 3339, which the
// round trip through time.Time already proves — this pins the placeholder
// side).
// ---------------------------------------------------------------------

// opaquePlaceholderMembers are the members whose value is an opaque
// identifier or free text, where the examples may print `…` in place of
// a real value.
var opaquePlaceholderMembers = map[string]bool{
	"message_id": true, "team_ref": true, "session_id": true, "principal_ref": true,
	"recipient_session_id": true, "sender_session_id": true,
	"message_ids": true, "acked": true, // arrays of message ids
	"message": true, // error.message: free text
	"body":    true, // send_request body: free text
}

func TestDecision6PlaceholdersOnlyInOpaqueStrings(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(filepath.Join("testdata", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	var walk func(file, member string, v any)
	walk = func(file, member string, v any) {
		switch vv := v.(type) {
		case map[string]any:
			for k, m := range vv {
				walk(file, k, m)
			}
		case []any:
			for _, m := range vv {
				walk(file, member, m)
			}
		case string:
			if strings.Contains(vv, "…") {
				if vv != "…" {
					t.Errorf("%s: %s = %q mixes a placeholder into a value", file, member, vv)
				}
				if !opaquePlaceholderMembers[member] {
					t.Errorf("%s: %s = %q; a placeholder is allowed only in an opaque member (decision 6)", file, member, vv)
				}
			}
			if strings.HasSuffix(member, "_at") || member == "lease_until" || member == "server_time" {
				if _, perr := time.Parse(time.RFC3339, vv); perr != nil {
					t.Errorf("%s: %s = %q is not RFC 3339 (decision 6)", file, member, vv)
				}
			}
		}
	}
	placeholders := 0
	for _, e := range entries {
		var doc any
		if err := json.Unmarshal(readExample(t, e.Name()), &doc); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if strings.Contains(string(readExample(t, e.Name())), "…") {
			placeholders++
		}
		walk(e.Name(), "", doc)
	}
	if placeholders == 0 {
		t.Fatal("no example carries a placeholder at all; the instrument saw nothing")
	}
}
