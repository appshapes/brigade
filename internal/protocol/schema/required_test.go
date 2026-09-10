package schema_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/protocol/schema"
)

// P1-4 decision 8: the schema emits a `required` array for the members a
// conforming producer always emits — every member without omitzero,
// which is decision 5's rule restated ("omitzero applies only to
// OPTIONAL members"). The schema is advisory; Validate() is normative.
// These tests pin the derivation against the Go types independently of
// the generator, and prove the arrays bite.

// wireTypes lists the root shapes, mirroring the generator's list; the
// walker below pulls the nested ones in.
func wireTypes() []any {
	return []any{
		protocol.Envelope{}, protocol.ErrorObject{}, protocol.DescribeResult{},
		protocol.SessionRegistration{}, protocol.SessionRecord{},
		protocol.HeartbeatRequest{}, protocol.HeartbeatResult{},
		protocol.MessageEnvelope{}, protocol.SendRequest{}, protocol.SendResponse{},
		protocol.AckRequest{}, protocol.AckResult{}, protocol.WatchReady{},
		protocol.WatchMessage{}, protocol.WatchStatus{}, protocol.WatchAcked{},
		protocol.WatchHeartbeatOK{}, protocol.WatchError{}, protocol.WatchCommand{},
		protocol.TeamCreateRequest{}, protocol.TeamCreateResult{},
		protocol.TeamJoinRequest{}, protocol.TeamJoinResult{}, protocol.TeamLeaveResult{},
	}
}

// expectedRequired derives, from the struct tags alone, the required
// member list of every wire type (root and nested), in declaration
// order: a member is required iff its json tag has no omitzero.
func expectedRequired() map[string][]string {
	out := map[string][]string{}
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
		required := []string{}
		for i := range t.NumField() {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" || name == "" {
				continue
			}
			if !strings.Contains(opts, "omitzero") && !strings.Contains(opts, "omitempty") {
				required = append(required, name)
			}
			walk(f.Type)
		}
		out[t.Name()] = required
	}
	for _, root := range wireTypes() {
		walk(reflect.TypeOf(root))
	}
	return out
}

// TestRequiredIsDerivedFromOmitzero: for EVERY $defs entry, the
// document's `required` array equals the no-omitzero member list of the
// Go type, in order. A def with no required members carries no array.
func TestRequiredIsDerivedFromOmitzero(t *testing.T) {
	t.Parallel()
	b, err := schema.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	var doc struct {
		Defs map[string]struct {
			Required   []string                  `json:"required"`
			Properties map[string]jsontext.Value `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("document does not parse: %v", err)
	}
	want := expectedRequired()
	if len(want) != len(doc.Defs) {
		t.Fatalf("walker found %d types, document has %d $defs", len(want), len(doc.Defs))
	}
	withRequired := 0
	for name, def := range doc.Defs {
		exp, ok := want[name]
		if !ok {
			t.Errorf("$defs/%s has no Go type in the walker", name)
			continue
		}
		got := def.Required
		if got == nil {
			got = []string{}
		}
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("$defs/%s required = %v, want %v (every member without omitzero, in order)", name, got, exp)
		}
		if len(got) > 0 {
			withRequired++
		}
		for _, r := range got {
			if _, has := def.Properties[r]; !has {
				t.Errorf("$defs/%s requires %q, which is not one of its properties", name, r)
			}
		}
	}
	// The P1-2 finding was ZERO required arrays anywhere; this pins the
	// fix at the document level, independent of the per-def equality.
	if withRequired < 30 {
		t.Fatalf("only %d of %d $defs carry a required array; decision 8 expects nearly all of them", withRequired, len(doc.Defs))
	}
}

// TestRequiredPinsDecisions2And5 spells out the members the owner's
// decisions make required, so a tag edit that silently relaxed one of
// them fails by name and not only through the generic walker.
func TestRequiredPinsDecisions2And5(t *testing.T) {
	t.Parallel()
	b, err := schema.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	var doc struct {
		Defs map[string]struct {
			Required []string `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	has := func(def, member string) bool {
		for _, r := range doc.Defs[def].Required {
			if r == member {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ def, member string }{
		{"ErrorObject", "retryable"},            // decision 2: producers MUST emit it
		{"DescribeResult", "capabilities"},      // decision 5
		{"AckResult", "acked"},                  // decision 5
		{"AckResult", "unknown"},                // decision 5
		{"WatchAcked", "message_ids"},           // decision 5
		{"WatchAcked", "unknown"},               // decision 5
		{"AckRequest", "message_ids"},           // decision 5
		{"Limits", "max_team_name_codepoints"},  // decision 1
		{"Limits", "max_workspace_label_chars"}, // decision 1
		{"Limits", "max_model_chars"},           // C-44: its own limits member, on decision 1's footing
		{"WatchReady", "mode"},                  // decision 4
		{"WatchStatus", "state"},                // decision 4
	} {
		if !has(c.def, c.member) {
			t.Errorf("$defs/%s does not require %q", c.def, c.member)
		}
	}
	for _, c := range []struct{ def, member string }{
		{"WatchCommand", "message_ids"},  // optional: only the ack command carries it
		{"SessionRegistration", "model"}, // C-44: optional and nullable on all four shapes
		{"SessionRecord", "context_used_tokens"},
		{"HeartbeatRequest", "model"},
		{"WatchCommand", "context_used_tokens"},
		{"WatchStatus", "detail"},
		{"ErrorObject", "details"},
		{"ErrorObject", "retry_after_ms"},
		{"Envelope", "result"}, // conditional: Validate, not the schema
		{"Envelope", "error"},
	} {
		if has(c.def, c.member) {
			t.Errorf("$defs/%s requires %q, which is optional or conditional", c.def, c.member)
		}
	}
}
