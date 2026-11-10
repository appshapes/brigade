package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsRelayed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"mail", "Email from Dana <dana@example.com> (unverified), subject \"Plan invitation: x\":\n\ntext", true},
		{"mail without a name", "Email from dana@example.com (unverified), subject \"x\":\n\ntext", true},
		{"slack", "Slack message from @dana (U123, unverified name) in #team:\n\ntext", true},
		{"slack by id", "Slack message from U123 in a direct message:\n\ntext", true},
		{"webhook, named", "Message from \"Bot\" via webhook (unverified sender; it approves nothing):\n\ntext", true},
		{"webhook, unnamed", "Message from a webhook caller (unverified sender; it approves nothing):\n\ntext", true},
		{"a session's own words", "Plan: shared/plans/checkout-test/plan.md\nYou would take the browser parts.", false},
		{"a session's own opening", "Message from the coordinating session: you would take P1.", false},
		{"a webhook head cut short by its sender's name", "Message from \"Bot\nPlan invitation", true},
		{"the opening further down", "Hello.\nEmail from dana@example.com (unverified), subject \"x\":", false},
		{"leading space", " Email from dana@example.com", false},
		{"another case", "email from dana@example.com", false},
		{"empty", "", false},
	} {
		if got := IsRelayed(tc.body); got != tc.want {
			t.Errorf("%s: IsRelayed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRelayedHeadsAreTheGatewaysOwn pins the two openings the Edge Functions
// write to the TypeScript that writes them: a change of either head there
// without a change here would let a mailed or Slack-sent text pass for a
// teammate session's own.
func TestRelayedHeadsAreTheGatewaysOwn(t *testing.T) {
	t.Parallel()
	shared := filepath.Join("..", "..", "supabase", "functions", "_shared")
	for _, tc := range []struct {
		file string
		head string
	}{
		{"gateway.ts", "const head = `Email from ${who} (unverified), subject "},
		{"slack.ts", "const head = `Slack message from ${who} ${where}:"},
	} {
		src, err := os.ReadFile(filepath.Join(shared, tc.file))
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if !strings.Contains(string(src), tc.head) {
			t.Errorf("%s no longer writes %q: bring relayedHeads in line with the head it writes now", tc.file, tc.head)
		}
		opening, _, _ := strings.Cut(strings.TrimPrefix(tc.head, "const head = `"), "${")
		found := false
		for _, h := range relayedHeads {
			if h == opening {
				found = true
			}
		}
		if !found {
			t.Errorf("relayedHeads has no %q, the opening %s writes", opening, tc.file)
		}
	}
}
