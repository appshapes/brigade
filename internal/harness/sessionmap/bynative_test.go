package sessionmap_test

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

func TestCheckNativeID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"a uuid", "beee3690-1111-4222-8333-444455556666", true},
		{"underscore and case", "Ab_c-9", true},
		{"exactly 80 characters", strings.Repeat("a", 80), true},
		{"81 characters", strings.Repeat("a", 81), false},
		{"empty", "", false},
		{"parent traversal", "../../" + evilMarker, false},
		{"slash", "a/b", false},
		{"dot", "a.json", false},
		{"dot only", ".", false},
		{"space", "a b", false},
		{"non-ascii", "sessión", false},
		{"newline", "a\nb", false},
		{"nul", "a\x00b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := sessionmap.CheckNativeID(tc.id)
			if tc.ok {
				if err != nil {
					t.Fatalf("CheckNativeID(%q) = %v, want nil", tc.id, err)
				}
				return
			}
			details := assertConfig(t, err, sessionmap.ReasonMapInvalid)
			if details["field"] != "claude_session_id" {
				t.Fatalf("details.field = %q, want claude_session_id", details["field"])
			}
		})
	}
}

func TestByNativeValidate(t *testing.T) {
	t.Parallel()
	m := sessionmap.ByNative{BrigadeSessionID: "6f0f", TeamRef: "t", SessionName: "n"}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	m.BrigadeSessionID = ""
	details := assertConfig(t, m.Validate(), sessionmap.ReasonMapInvalid)
	if details["field"] != "brigade_session_id" {
		t.Fatalf("details.field = %q", details["field"])
	}
}
