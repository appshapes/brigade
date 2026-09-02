package schema_test

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	santhosh "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/protocol/schema"
)

// The tests below validate against the COMMITTED schema, not a freshly
// generated one: the committed file is what adapter authors consume, and
// `make schema-check` separately pins it against the generator.
const committedSchemaPath = "../../../docs/protocol-v1.schema.json"

const examplesDir = "../testdata/examples"

// exampleDefs maps every example file (basename without .json) to the
// $defs entry it instantiates. TestExamplesValidate fails on an example
// file with no mapping and on a mapping with no file, so neither side
// can silently fall out of coverage.
var exampleDefs = map[string]string{
	"ack_request":             "AckRequest",
	"ack_result":              "AckResult",
	"describe_result":         "DescribeResult",
	"envelope_error":          "Envelope",
	"envelope_ok":             "Envelope",
	"heartbeat_request":       "HeartbeatRequest",
	"heartbeat_result":        "HeartbeatResult",
	"message_envelope":        "MessageEnvelope",
	"send_request":            "SendRequest",
	"send_response":           "SendResponse",
	"session_record":          "SessionRecord",
	"session_registration":    "SessionRegistration",
	"team_create_request":     "TeamCreateRequest",
	"team_create_result":      "TeamCreateResult",
	"team_join_request":       "TeamJoinRequest",
	"team_join_result":        "TeamJoinResult",
	"team_leave_result":       "TeamLeaveResult",
	"watch_acked":             "WatchAcked",
	"watch_command_ack":       "WatchCommand",
	"watch_command_close":     "WatchCommand",
	"watch_command_heartbeat": "WatchCommand",
	"watch_error":             "WatchError",
	"watch_heartbeat_ok":      "WatchHeartbeatOK",
	"watch_message":           "WatchMessage",
	"watch_ready":             "WatchReady",
	"watch_status_live":       "WatchStatus",
	"watch_status_polling":    "WatchStatus",
}

// compileDef compiles one $defs entry of the committed schema document.
func compileDef(t *testing.T, name string) *santhosh.Schema {
	t.Helper()
	raw, err := os.ReadFile(committedSchemaPath)
	if err != nil {
		t.Fatalf("read committed schema: %v", err)
	}
	doc, err := santhosh.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("committed schema is not JSON: %v", err)
	}
	c := santhosh.NewCompiler()
	if err := c.AddResource(schema.SchemaID, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile(schema.SchemaID + "#/$defs/" + name)
	if err != nil {
		t.Fatalf("compile $defs/%s: %v", name, err)
	}
	return sch
}

// readInstance parses one JSON document the way the validator wants it.
func readInstance(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	inst, err := santhosh.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	return inst
}

// TestExamplesValidate validates EVERY example in testdata/examples
// against its $defs entry in the committed schema (P1-2 acceptance).
func TestExamplesValidate(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("examples dir is empty; nothing was validated")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		def, ok := exampleDefs[name]
		if !ok {
			t.Fatalf("example %s has no $defs mapping; add it to exampleDefs", entry.Name())
		}
		seen[name] = true
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sch := compileDef(t, def)
			inst := readInstance(t, filepath.Join(examplesDir, name+".json"))
			if err := sch.Validate(inst); err != nil {
				t.Errorf("%s does not validate against $defs/%s:\n%v", name, def, err)
			}
		})
	}
	for name := range exampleDefs {
		if !seen[name] {
			t.Errorf("exampleDefs names %s but no such example file exists", name)
		}
	}
}

// TestErrorObjectExampleValidates covers the one root shape with no
// example file of its own: the `error` member of envelope_error.json is
// the 4.3 error object.
func TestErrorObjectExampleValidates(t *testing.T) {
	t.Parallel()
	inst := readInstance(t, filepath.Join(examplesDir, "envelope_error.json"))
	envelope, ok := inst.(map[string]any)
	if !ok {
		t.Fatalf("envelope_error.json is %T, want an object", inst)
	}
	errMember, ok := envelope["error"]
	if !ok {
		t.Fatal("envelope_error.json has no error member")
	}
	sch := compileDef(t, "ErrorObject")
	if err := sch.Validate(errMember); err != nil {
		t.Errorf("error member does not validate against $defs/ErrorObject:\n%v", err)
	}
}

// causeAt reports whether err (a validation failure) carries a cause of
// errorKind's dynamic type at the single-member instance location
// `member`. It is how the negative tests check an instance failed for
// the RIGHT reason rather than for being malformed.
func causeAt(err error, member string, errorKind any) bool {
	var ve *santhosh.ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	var walk func(e *santhosh.ValidationError) bool
	walk = func(e *santhosh.ValidationError) bool {
		if len(e.InstanceLocation) == 1 && e.InstanceLocation[0] == member {
			switch errorKind.(type) {
			case *kind.FalseSchema:
				if _, ok := e.ErrorKind.(*kind.FalseSchema); ok {
					return true
				}
			case *kind.Type:
				if _, ok := e.ErrorKind.(*kind.Type); ok {
					return true
				}
			case *kind.MaxLength:
				if _, ok := e.ErrorKind.(*kind.MaxLength); ok {
					return true
				}
			}
		}
		for _, c := range e.Causes {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(ve)
}

// TestValidationCanFail is the positive control for the whole example
// suite: with additionalProperties never false, a validator wired to the
// wrong document could pass everything. Prove the instruments bite: a
// wrong member type, an over-cap name and a missing required member
// (P1-4 decision 8) must each fail, each for its own reason.
func TestValidationCanFail(t *testing.T) {
	t.Parallel()
	t.Run("missing required member", func(t *testing.T) {
		t.Parallel()
		inst := readInstance(t, filepath.Join(examplesDir, "envelope_ok.json"))
		envelope := inst.(map[string]any)
		delete(envelope, "protocol_version")
		err := compileDef(t, "Envelope").Validate(envelope)
		if err == nil {
			t.Fatal("an envelope without protocol_version validated; decision 8 requires it")
		}
		if !missingRequired(err, "protocol_version") {
			t.Fatalf("failed, but not with a required error naming protocol_version:\n%v", err)
		}
	})
	t.Run("wrong type", func(t *testing.T) {
		t.Parallel()
		inst := readInstance(t, filepath.Join(examplesDir, "envelope_ok.json"))
		envelope := inst.(map[string]any)
		envelope["ok"] = "yes" // boolean member as a string
		err := compileDef(t, "Envelope").Validate(envelope)
		if err == nil {
			t.Fatal("a string-valued ok validated against Envelope")
		}
		if !causeAt(err, "ok", &kind.Type{}) {
			t.Fatalf("ok: \"yes\" failed, but not with a type error at /ok:\n%v", err)
		}
	})
	t.Run("over-cap name", func(t *testing.T) {
		t.Parallel()
		inst := readInstance(t, filepath.Join(examplesDir, "session_registration.json"))
		reg := inst.(map[string]any)
		reg["session_name"] = strings.Repeat("n", 65) // cap is 64 code points
		err := compileDef(t, "SessionRegistration").Validate(reg)
		if err == nil {
			t.Fatal("a 65-code-point session_name validated against SessionRegistration")
		}
		if !causeAt(err, "session_name", &kind.MaxLength{}) {
			t.Fatalf("over-cap session_name failed, but not with maxLength at /session_name:\n%v", err)
		}
	})
}

// TestC23ForgedSenderMembersFail checks the C-23 half of the P1-2
// acceptance: a SendRequest carrying any adapter-stamped member fails
// validation, and fails BECAUSE of that member's `false` property schema
// (kind.FalseSchema at exactly that member) — not because the instance
// is otherwise malformed, which the base-instance subtest pins.
func TestC23ForgedSenderMembersFail(t *testing.T) {
	t.Parallel()
	base := readInstance(t, filepath.Join(examplesDir, "send_request.json")).(map[string]any)

	t.Run("base instance validates", func(t *testing.T) {
		t.Parallel()
		if err := compileDef(t, "SendRequest").Validate(base); err != nil {
			t.Fatalf("send_request.json itself does not validate; the forged-member failures below would be meaningless:\n%v", err)
		}
	})

	forged := map[string]any{
		"sender":        map[string]any{"principal_ref": "spoofed", "session_id": "spoofed"},
		"principal_ref": "spoofed",
		"human_label":   "alice@example.com",
		"team_ref":      "spoofed",
		"created_at":    "2026-08-30T12:00:00Z",
		"hop_count":     3.0,
	}
	for member, value := range forged {
		t.Run(member, func(t *testing.T) {
			t.Parallel()
			for _, v := range []any{value, nil} { // C-23: any value, null included
				inst := map[string]any{}
				for k, val := range base {
					inst[k] = val
				}
				inst[member] = v
				err := compileDef(t, "SendRequest").Validate(inst)
				if err == nil {
					t.Fatalf("a SendRequest carrying %s=%v validated; C-23 requires it to fail", member, v)
				}
				if !causeAt(err, member, &kind.FalseSchema{}) {
					t.Fatalf("%s=%v failed, but not via the false schema at /%s:\n%v", member, v, member, err)
				}
			}
		})
	}
}

// missingRequired reports whether err carries a kind.Required cause that
// names member among the missing ones.
func missingRequired(err error, member string) bool {
	var ve *santhosh.ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	var walk func(e *santhosh.ValidationError) bool
	walk = func(e *santhosh.ValidationError) bool {
		if r, ok := e.ErrorKind.(*kind.Required); ok && slices.Contains(r.Missing, member) {
			return true
		}
		for _, c := range e.Causes {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(ve)
}

// exampleTypes maps each example to a fresh instance of its Go type, for
// the cross-check below.
var exampleTypes = map[string]func() protocol.Validator{
	"ack_request":             func() protocol.Validator { return &protocol.AckRequest{} },
	"ack_result":              func() protocol.Validator { return &protocol.AckResult{} },
	"describe_result":         func() protocol.Validator { return &protocol.DescribeResult{} },
	"envelope_error":          func() protocol.Validator { return &protocol.Envelope{} },
	"envelope_ok":             func() protocol.Validator { return &protocol.Envelope{} },
	"heartbeat_request":       func() protocol.Validator { return &protocol.HeartbeatRequest{} },
	"heartbeat_result":        func() protocol.Validator { return &protocol.HeartbeatResult{} },
	"message_envelope":        func() protocol.Validator { return &protocol.MessageEnvelope{} },
	"send_request":            func() protocol.Validator { return &protocol.SendRequest{} },
	"send_response":           func() protocol.Validator { return &protocol.SendResponse{} },
	"session_record":          func() protocol.Validator { return &protocol.SessionRecord{} },
	"session_registration":    func() protocol.Validator { return &protocol.SessionRegistration{} },
	"team_create_request":     func() protocol.Validator { return &protocol.TeamCreateRequest{} },
	"team_create_result":      func() protocol.Validator { return &protocol.TeamCreateResult{} },
	"team_join_request":       func() protocol.Validator { return &protocol.TeamJoinRequest{} },
	"team_join_result":        func() protocol.Validator { return &protocol.TeamJoinResult{} },
	"team_leave_result":       func() protocol.Validator { return &protocol.TeamLeaveResult{} },
	"watch_acked":             func() protocol.Validator { return &protocol.WatchAcked{} },
	"watch_command_ack":       func() protocol.Validator { return &protocol.WatchCommand{} },
	"watch_command_close":     func() protocol.Validator { return &protocol.WatchCommand{} },
	"watch_command_heartbeat": func() protocol.Validator { return &protocol.WatchCommand{} },
	"watch_error":             func() protocol.Validator { return &protocol.WatchError{} },
	"watch_heartbeat_ok":      func() protocol.Validator { return &protocol.WatchHeartbeatOK{} },
	"watch_message":           func() protocol.Validator { return &protocol.WatchMessage{} },
	"watch_ready":             func() protocol.Validator { return &protocol.WatchReady{} },
	"watch_status_live":       func() protocol.Validator { return &protocol.WatchStatus{} },
	"watch_status_polling":    func() protocol.Validator { return &protocol.WatchStatus{} },
}

// TestRequiredMembersAgreeWithValidate is the decision-8 cross-check
// between the advisory schema and the normative Validate(): for every
// example and every `required` member of its $defs entry, deleting that
// member from the example (a) fails schema validation with a required
// error naming it, and (b) when the member is a string or an object —
// the kinds whose absence loose parsing can observe — fails Validate()
// with invalid_input. Booleans, numbers and arrays are required in the
// schema because a producer always emits them (decisions 2 and 5), but
// their absence is indistinguishable from a zero value to a loose parser,
// so (b) is skipped for them by construction rather than by exception.
func TestRequiredMembersAgreeWithValidate(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(committedSchemaPath)
	if err != nil {
		t.Fatalf("read committed schema: %v", err)
	}
	var doc struct {
		Defs map[string]struct {
			Required []string `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("committed schema does not parse: %v", err)
	}
	checked := 0
	for name, def := range exampleDefs {
		fresh, ok := exampleTypes[name]
		if !ok {
			t.Fatalf("example %s has no Go type mapping; add it to exampleTypes", name)
		}
		for _, member := range doc.Defs[def].Required {
			t.Run(name+"/"+member, func(t *testing.T) {
				t.Parallel()
				base := readInstance(t, filepath.Join(examplesDir, name+".json")).(map[string]any)
				value, present := base[member]
				if !present {
					t.Fatalf("example %s lacks required member %q; the example itself is non-conforming", name, member)
				}
				delete(base, member)
				if serr := compileDef(t, def).Validate(base); serr == nil {
					t.Fatalf("schema accepted %s without %q", name, member)
				} else if !missingRequired(serr, member) {
					t.Fatalf("schema rejected %s without %q, but not for that reason:\n%v", name, member, serr)
				}
				switch value.(type) {
				case string, map[string]any:
				default:
					return // bool, number, array: absence is a zero value to a loose parser
				}
				mutated, err := json.Marshal(base)
				if err != nil {
					t.Fatal(err)
				}
				verr := protocol.Decode(mutated, fresh())
				if verr == nil {
					t.Fatalf("Validate() accepted %s without %q; the schema requires it and Validate() is normative — one of them is wrong", name, member)
				}
				var perr *protocol.Error
				if !errors.As(verr, &perr) || perr.Code != protocol.CodeInvalidInput {
					t.Fatalf("Validate() failed with %v, want invalid_input", verr)
				}
				if field := perr.Details["field"]; field != member && !strings.HasPrefix(field, member+".") {
					t.Fatalf("Validate() named %q, want %q or a member nested under it", field, member)
				}
			})
			checked++
		}
	}
	if checked < 60 {
		t.Fatalf("only %d required members were checked across the examples; the instrument saw too little", checked)
	}
}
