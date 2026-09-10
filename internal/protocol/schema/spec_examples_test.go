package schema_test

import (
	"bytes"
	jsonv1 "encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// docs/protocol-v1.md, JSON convention 7, says the examples in the
// specification ARE the files under testdata/examples, which the tests in
// this package validate against the schema and round-trip through the Go
// types. This file makes that sentence mechanical, in both directions:
//
//   - every ```json block in the specification whose shape has a Go type
//     is byte-identical (modulo the trailing newline) to one example file,
//     so an edit to either side without the other fails here — the P1-2
//     pass found that a one-character drift between prose and code
//     survives reading, and the frozen spec is the one place where a
//     drifted "verbatim" example does real damage;
//   - every example file appears in the specification, so no file can be
//     validated by the suite while the document shows something else;
//   - the four composite results that have NO Go type (the `session
//     register`, `session list`, `message receive` and `team members`
//     results, described member by member in 4.4) are classified by their
//     exact top-level member set and their typed parts are validated
//     through the schema and through Validate(), so the spec's only
//     examples that cannot round-trip whole are still checked piecewise.
//
// The number of composite blocks is pinned: a fifth composite means a new
// wire shape that either needs a Go type or a deliberate entry below.

const specPath = "../../../docs/protocol-v1.md"

// A specBlock is one fenced ```json block of the specification.
type specBlock struct {
	line int    // 1-based line of the opening fence
	text string // the block's content, without the fences
}

// specJSONBlocks extracts every ```json block in document order.
func specJSONBlocks(t *testing.T) []specBlock {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read specification: %v", err)
	}
	open := "```json\n"
	closeFence := "```"
	doc := string(raw)
	var blocks []specBlock
	for pos := 0; ; {
		start := strings.Index(doc[pos:], open)
		if start < 0 {
			break
		}
		start += pos
		bodyStart := start + len(open)
		end := strings.Index(doc[bodyStart:], closeFence)
		if end < 0 {
			t.Fatalf("unterminated ```json fence at byte %d", start)
		}
		end += bodyStart
		blocks = append(blocks, specBlock{
			line: strings.Count(doc[:start], "\n") + 1,
			text: doc[bodyStart:end],
		})
		pos = end + len(closeFence)
	}
	return blocks
}

// canonicalJSON re-encodes a document with sorted member names, so two
// documents compare equal exactly when they are the same JSON value.
func canonicalJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := jsonv1.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	out, err := jsonv1.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// topLevelMembers returns the sorted member names of a JSON object.
func topLevelMembers(t *testing.T, raw []byte) []string {
	t.Helper()
	var obj map[string]jsonv1.RawMessage
	if err := jsonv1.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// wireMembers returns the sorted JSON member names of a wire struct.
func wireMembers(v any) []string {
	rt := reflect.TypeOf(v)
	var names []string
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// validateTyped checks one JSON value both ways: against its $defs entry
// in the committed schema and through the Go type's Validate().
func validateTyped(t *testing.T, where string, raw []byte, def string, fresh protocol.Validator) {
	t.Helper()
	inst, err := jsonUnmarshalAny(raw)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	if err := compileDef(t, def).Validate(inst); err != nil {
		t.Errorf("%s does not validate against $defs/%s:\n%v", where, def, err)
	}
	if err := protocol.Decode(raw, fresh); err != nil {
		var perr *protocol.Error
		_ = errors.As(err, &perr)
		t.Errorf("%s is rejected by %T.Validate(): %v (details %v)", where, fresh, err, perr.Details)
	}
}

func jsonUnmarshalAny(raw []byte) (any, error) {
	var v any
	if err := jsonv1.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// mustRFC3339 asserts a member is a real RFC 3339 timestamp (decision 6:
// no placeholder in a typed member).
func mustRFC3339(t *testing.T, where, member string, v any) {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Errorf("%s: %s = %v, want an RFC 3339 string", where, member, v)
		return
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("%s: %s = %q is not RFC 3339: %v", where, member, s, err)
	}
}

// compositeChecks maps the exact top-level member set of each composite
// result to its piecewise validation. Keys are the sorted member names
// joined by ",".
func compositeChecks(t *testing.T) map[string]func(t *testing.T, where string, raw []byte) {
	t.Helper()
	recordMembers := wireMembers(protocol.SessionRecord{})
	registerMembers := append(append([]string{}, recordMembers...), "resumed", "lease_seconds", "server_time")
	sort.Strings(registerMembers)

	return map[string]func(t *testing.T, where string, raw []byte){
		// session register result (4.4.2): a SessionRecord plus resumed,
		// lease_seconds and server_time.
		strings.Join(registerMembers, ","): func(t *testing.T, where string, raw []byte) {
			t.Helper()
			validateTyped(t, where+" (as SessionRecord)", raw, "SessionRecord", &protocol.SessionRecord{})
			var extra struct {
				Resumed      *bool `json:"resumed"`
				LeaseSeconds *int  `json:"lease_seconds"`
				ServerTime   any   `json:"server_time"`
			}
			if err := jsonv1.Unmarshal(raw, &extra); err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			if extra.Resumed == nil {
				t.Errorf("%s: resumed is not a boolean", where)
			}
			if extra.LeaseSeconds == nil || *extra.LeaseSeconds < protocol.LeaseMinSeconds || *extra.LeaseSeconds > protocol.LeaseMaxSeconds {
				t.Errorf("%s: lease_seconds = %v, want an integer within %d..%d", where, extra.LeaseSeconds, protocol.LeaseMinSeconds, protocol.LeaseMaxSeconds)
			}
			mustRFC3339(t, where, "server_time", extra.ServerTime)
		},
		// session list result (4.4.3).
		"server_time,sessions,team_name,team_ref,truncated": func(t *testing.T, where string, raw []byte) {
			t.Helper()
			var list struct {
				TeamRef    string              `json:"team_ref"`
				TeamName   string              `json:"team_name"`
				ServerTime any                 `json:"server_time"`
				Sessions   []jsonv1.RawMessage `json:"sessions"`
				Truncated  *bool               `json:"truncated"`
			}
			if err := jsonv1.Unmarshal(raw, &list); err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			if list.TeamRef == "" || list.TeamName == "" {
				t.Errorf("%s: team_ref and team_name must be non-empty", where)
			}
			mustRFC3339(t, where, "server_time", list.ServerTime)
			if list.Truncated == nil {
				t.Errorf("%s: truncated is not a boolean", where)
			}
			if len(list.Sessions) == 0 {
				t.Errorf("%s: sessions is empty; the example should show a record", where)
			}
			for i, s := range list.Sessions {
				at := where + " sessions[" + strconv.Itoa(i) + "]"
				validateTyped(t, at, s, "SessionRecord", &protocol.SessionRecord{})
				// Like the register result above, a listed record shows the
				// WHOLE SessionRecord, optional members included (the nullable
				// ones as null), so a member added to the type — model and
				// context_used_tokens, C-44 — cannot be left out of the 4.4.3
				// example: Validate alone passes a record without them.
				if got := topLevelMembers(t, s); !reflect.DeepEqual(got, recordMembers) {
					t.Errorf("%s has members %v, want exactly the SessionRecord members %v", at, got, recordMembers)
				}
			}
		},
		// message receive result (4.4.5).
		"messages": func(t *testing.T, where string, raw []byte) {
			t.Helper()
			var recv struct {
				Messages []jsonv1.RawMessage `json:"messages"`
			}
			if err := jsonv1.Unmarshal(raw, &recv); err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			if len(recv.Messages) == 0 {
				t.Errorf("%s: messages is empty; the example should show an envelope", where)
			}
			for i, m := range recv.Messages {
				validateTyped(t, where+" messages["+strconv.Itoa(i)+"]", m, "MessageEnvelope", &protocol.MessageEnvelope{})
			}
		},
		// team members result (4.4.10): no Go type at any level, so the
		// member shape is pinned here from the 4.2 row.
		"members,server_time,team_name,team_ref": func(t *testing.T, where string, raw []byte) {
			t.Helper()
			var roster struct {
				TeamRef    string           `json:"team_ref"`
				TeamName   string           `json:"team_name"`
				ServerTime any              `json:"server_time"`
				Members    []map[string]any `json:"members"`
			}
			if err := jsonv1.Unmarshal(raw, &roster); err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			if roster.TeamRef == "" || roster.TeamName == "" {
				t.Errorf("%s: team_ref and team_name must be non-empty", where)
			}
			mustRFC3339(t, where, "server_time", roster.ServerTime)
			want := []string{"human_label", "joined_at", "last_seen_at", "principal_ref", "session_count", "status"}
			for i, m := range roster.Members {
				got := make([]string, 0, len(m))
				for k := range m {
					got = append(got, k)
				}
				sort.Strings(got)
				at := where + " members[" + strconv.Itoa(i) + "]"
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s has members %v, want exactly %v (4.2 `team members`)", at, got, want)
					continue
				}
				mustRFC3339(t, at, "joined_at", m["joined_at"])
				if m["last_seen_at"] != nil {
					mustRFC3339(t, at, "last_seen_at", m["last_seen_at"])
				}
				if _, ok := m["session_count"].(float64); !ok {
					t.Errorf("%s: session_count = %v, want a number", at, m["session_count"])
				}
				if s, _ := m["principal_ref"].(string); s == "" {
					t.Errorf("%s: principal_ref must be a non-empty string", at)
				}
			}
		},
	}
}

// TestSpecExamplesAreTheTestdataFiles is the mechanical form of JSON
// convention 7.
func TestSpecExamplesAreTheTestdataFiles(t *testing.T) {
	t.Parallel()
	blocks := specJSONBlocks(t)
	if len(blocks) < 31 {
		t.Fatalf("found %d ```json blocks in the specification, expected at least 31; the extractor saw too little", len(blocks))
	}

	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}
	byCanonical := map[string]string{} // canonical JSON → example file name
	rawByName := map[string][]byte{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(examplesDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		c := canonicalJSON(t, raw)
		if other, dup := byCanonical[c]; dup {
			t.Fatalf("examples %s and %s are the same JSON value; the mapping back to the spec would be ambiguous", other, e.Name())
		}
		byCanonical[c] = e.Name()
		rawByName[e.Name()] = raw
	}

	composites := compositeChecks(t)
	matched := map[string]int{}
	compositeCount := 0
	for _, b := range blocks {
		where := "spec line " + strconv.Itoa(b.line)
		raw := []byte(b.text)
		if name, ok := byCanonical[canonicalJSON(t, raw)]; ok {
			matched[name]++
			if want, got := strings.TrimRight(string(rawByName[name]), "\n"), strings.TrimRight(b.text, "\n"); want != got {
				t.Errorf("%s is the same JSON value as %s but not the same bytes; convention 7 says the examples ARE the files\n--- file\n%s\n--- spec\n%s", where, name, want, got)
			}
			continue
		}
		key := strings.Join(topLevelMembers(t, raw), ",")
		check, ok := composites[key]
		if !ok {
			t.Errorf("%s matches no example file and no known composite result (members: %s); add a testdata example or a composite entry", where, key)
			continue
		}
		compositeCount++
		check(t, where, raw)
	}

	if compositeCount != 4 {
		t.Errorf("%d composite (untyped) examples in the specification, want exactly 4: session register, session list, message receive, team members", compositeCount)
	}
	for _, e := range entries {
		if matched[e.Name()] == 0 {
			t.Errorf("example %s appears nowhere in the specification; convention 7 says the spec's examples are these files", e.Name())
		}
	}
}

// TestSpecExamplesCanFail is the positive control: a spec block that
// drifted from its file by one byte inside a typed member, and a typed
// member carrying a placeholder, must each be caught by the same
// machinery the test above uses, or that test proves nothing.
func TestSpecExamplesCanFail(t *testing.T) {
	t.Parallel()
	t.Run("typed member with a placeholder is rejected by Validate", func(t *testing.T) {
		t.Parallel()
		raw := bytes.Replace(rawExample(t, "session_record.json"),
			[]byte(`"created_at": "2026-08-30T11:55:00Z"`), []byte(`"created_at": "…"`), 1)
		if bytes.Equal(raw, rawExample(t, "session_record.json")) {
			t.Fatal("the mutation did not apply; the control is inert")
		}
		if err := protocol.Decode(raw, &protocol.SessionRecord{}); err == nil {
			t.Fatal("a placeholder timestamp round-tripped; decision 6 cannot be checked by this instrument")
		}
	})
	t.Run("one-byte drift changes the canonical form", func(t *testing.T) {
		t.Parallel()
		raw := rawExample(t, "watch_ready.json")
		drifted := bytes.Replace(raw, []byte(`"mode"`), []byte(`"Mode"`), 1)
		if bytes.Equal(raw, drifted) {
			t.Fatal("the mutation did not apply; the control is inert")
		}
		if canonicalJSON(t, raw) == canonicalJSON(t, drifted) {
			t.Fatal("a member-name case drift has the same canonical form; the matcher is blind to it")
		}
	})
}

func rawExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(examplesDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
