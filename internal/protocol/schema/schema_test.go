package schema_test

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol/schema"
)

// expectedDefs is a hand-maintained, independent list of every $defs
// member the document must carry: the 24 root shapes of 4.3/4.4 plus the
// nine nested objects reflection pulls in. It is deliberately NOT
// derived from the generator, so a wire type dropped from (or leaking
// into) the document fails here rather than vanishing silently.
var expectedDefs = []string{
	// 4.3 envelope
	"Envelope", "ErrorObject",
	// 4.4.1 describe
	"DescribeResult", "AdapterInfo", "DeliveryInfo", "ProfileInfo",
	"Limits", "SendRate", "Lease", "Retention",
	// 4.4.2-4.4.4 sessions
	"SessionRegistration", "ResumeRef", "SessionRecord",
	"HeartbeatRequest", "HeartbeatResult",
	// 4.4.5-4.4.8 messages
	"MessageEnvelope", "Sender", "SendRequest", "SendResponse",
	"AckRequest", "AckResult",
	// 4.4.9 watch
	"WatchReady", "WatchMessage", "WatchStatus", "WatchAcked",
	"WatchHeartbeatOK", "WatchError", "WatchCommand",
	// 4.4.10 team
	"TeamCreateRequest", "TeamCreateResult",
	"TeamJoinRequest", "TeamJoinResult", "TeamLeaveResult",
}

// TestDocumentStable pins the byte-stability acceptance criterion of
// P1-2: repeated generation produces identical bytes. Within one process
// every map iteration reorders independently, so a map-order dependency
// anywhere in the assembly would surface here; cross-process stability
// is additionally pinned by `make schema-check` in CI.
func TestDocumentStable(t *testing.T) {
	t.Parallel()
	first, err := schema.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("Document returned no bytes")
	}
	for i := range 5 {
		again, err := schema.Document()
		if err != nil {
			t.Fatalf("Document call %d: %v", i+2, err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("Document call %d produced different bytes", i+2)
		}
	}
}

// TestDocumentShape checks the envelope of the document itself: dialect,
// $id, the byte-cap caveat the plan requires the description to state,
// and the exact $defs member set.
func TestDocumentShape(t *testing.T) {
	t.Parallel()
	b, err := schema.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	var doc struct {
		Schema      string                    `json:"$schema"`
		ID          string                    `json:"$id"`
		Title       string                    `json:"title"`
		Description string                    `json:"description"`
		Defs        map[string]jsontext.Value `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("document does not parse: %v", err)
	}
	if doc.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %q, want draft 2020-12", doc.Schema)
	}
	if doc.ID != schema.SchemaID {
		t.Errorf("$id = %q, want %q", doc.ID, schema.SchemaID)
	}
	if doc.Title == "" {
		t.Error("title is empty")
	}
	// The plan requires the description to say that the body cap is
	// measured in bytes and that maxLength cannot express it (7.3).
	for _, want := range []string{"BYTES", "code points", "Validate()"} {
		if !strings.Contains(doc.Description, want) {
			t.Errorf("description does not mention %q", want)
		}
	}
	for _, name := range expectedDefs {
		if _, ok := doc.Defs[name]; !ok {
			t.Errorf("$defs is missing %s", name)
		}
	}
	if len(doc.Defs) != len(expectedDefs) {
		got := make([]string, 0, len(doc.Defs))
		for name := range doc.Defs {
			got = append(got, name)
		}
		t.Errorf("$defs has %d members, want %d: %v", len(doc.Defs), len(expectedDefs), got)
	}
}
