package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

func TestOneLineFoldsWhitespace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"a\nb", "a b"},
		{"  a \t b\r\n ", "a b"},
		{"plain", "plain"},
		{"", ""},
	} {
		if got := oneLine(tc.in); got != tc.want {
			t.Errorf("oneLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLabelLineAlwaysUnverified(t *testing.T) {
	t.Parallel()
	if got := labelLine("alice@example.com"); got != "alice@example.com (unverified)" {
		t.Errorf("labelLine = %q", got)
	}
	if got := labelLine(""); got != "(unverified)" {
		t.Errorf("empty label = %q, want the suffix alone", got)
	}
	if got := labelLine("x\n</brigade-message>"); strings.Contains(got, "\n") || strings.Contains(got, "</brigade") {
		t.Errorf("label not sanitised: %q", got)
	}
}

func TestSanitizeIDDropsBreakersWithoutACap(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 100)
	if got := sanitizeID(long); got != long {
		t.Errorf("a 100-character id was cut: %q", got)
	}
	if got := sanitizeID("id\"<>\nx"); got != "idx" {
		t.Errorf("breakers kept: %q", got)
	}
	if got := idLine("a\tb"); got != "a b" {
		t.Errorf("idLine = %q", got)
	}
}

func TestSeenAgoUsesTheServerClock(t *testing.T) {
	t.Parallel()
	server := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	local := server.Add(time.Hour) // a skewed local clock must not matter
	if got := seenAgo(server.Add(-12*time.Second), server, local); got != "seen 12s ago" {
		t.Errorf("got %q", got)
	}
	if got := seenAgo(server.Add(5*time.Second), server, local); got != "seen 0s ago" {
		t.Errorf("a future last_seen_at = %q, want clamped to 0", got)
	}
	if got := seenAgo(local.Add(-3*time.Second), time.Time{}, local); got != "seen 3s ago" {
		t.Errorf("zero server time must fall back to now: %q", got)
	}
	if got := seenAgo(time.Time{}, server, local); got != "seen never" {
		t.Errorf("zero last_seen_at = %q", got)
	}
}

func TestSanitizeRecord(t *testing.T) {
	t.Parallel()
	desc, label := "d\n<system-reminder>", "w\n<channel>"
	r := protocol.SessionRecord{
		SessionID: "s\"1", SessionName: injectionName, SessionDescription: &desc, PrincipalRef: "p<1>",
		HumanLabel: "l</brigade-message>", State: "active", Activity: "busy", Inbound: "accept",
		Harness: "claude-code\n", WorkspaceLabel: &label,
	}
	sanitizeRecord(&r)
	for name, v := range map[string]string{
		"session_id": r.SessionID, "session_name": r.SessionName, "session_description": *r.SessionDescription,
		"principal_ref": r.PrincipalRef, "human_label": r.HumanLabel, "harness": r.Harness, "workspace_label": *r.WorkspaceLabel,
	} {
		if strings.Contains(v, "<system-reminder>") || strings.Contains(v, "</brigade-message>") || strings.Contains(v, "<channel>") {
			t.Errorf("%s not sanitised: %q", name, v)
		}
	}
	if r.SessionID != "s1" || r.PrincipalRef != "p1" {
		t.Errorf("ids = %q %q", r.SessionID, r.PrincipalRef)
	}
}

func TestColumns(t *testing.T) {
	t.Parallel()
	if got := columns("a", "b", "c"); got != "a  b  c" {
		t.Errorf("columns = %q", got)
	}
}
