package digest_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/digest"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

var t0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func ptr(s string) *string { return &s }

func rec(id, name, label, state, inbound, harness string, seen time.Time, doing string) protocol.SessionRecord {
	r := protocol.SessionRecord{SessionID: id, SessionName: name, HumanLabel: label, PrincipalRef: id + "-p", State: state, Activity: "idle", Inbound: inbound, Harness: harness, LastSeenAt: seen, WorkspaceLabel: ptr("brigade")}
	if doing != "" {
		r.SessionDescription = ptr(doing)
	}
	return r
}

// TestBuildSections is §5.4 end to end over a small team: every section
// in order with its label, the decisions oldest first with their age, the
// commits one per hash with both sessions named and the carded ones first,
// the blocked note, the active and the ended sessions, the quiet one, the
// roster-only one, and the summary's counts.
func TestBuildSections(t *testing.T) {
	t.Parallel()
	since := t0.Add(-24 * time.Hour)
	alice := state.New("sess-a", "162-digest", since.Add(-time.Hour))
	alice.Member, alice.Repository = "alice@example.com", "brigade"
	alice.Commits = []state.Commit{
		{Hash: strings.Repeat("a", 40), Subject: "162: Add the digest", Card: 162, At: t0.Add(-2 * time.Hour)},
		{Hash: strings.Repeat("b", 40), Subject: "Merge branch 'master'", At: t0.Add(-time.Hour)},
		{Hash: strings.Repeat("c", 40), Subject: "7: Old, before the period", Card: 7, At: since.Add(-time.Hour)},
	}
	alice.AddDoing("writing the digest", t0.Add(-30*time.Minute))
	alice.AddNote("Next: the report command", t0.Add(-20*time.Minute))
	alice2 := state.New("sess-a2", "162-review", since.Add(-time.Hour))
	alice2.Member = "alice@example.com"
	alice2.Commits = []state.Commit{{Hash: strings.Repeat("a", 40), Subject: "162: Add the digest", Card: 162, At: t0.Add(-2 * time.Hour)}}
	alice2.EndedAt, alice2.EndReason = t0.Add(-10*time.Minute), "other"
	alice2.AddNote("Done: reviewed the digest", t0.Add(-15*time.Minute))
	bob := state.New("sess-b", "billing", since.Add(-time.Hour))
	bob.Member = "bob@example.com"
	bob.Commits = []state.Commit{{Hash: strings.Repeat("d", 40), Subject: "12: Fix the invoice total", Card: 12, At: t0.Add(-3 * time.Hour)}}
	bob.AddNote("Blocked: waiting on the tax table", t0.Add(-40*time.Minute))
	open := &state.Decision{Version: 1, ID: "d-20261028-aaaaaa", Question: "Which backend?", Options: []string{"supabase", "fs"}, Default: "supabase", Member: "alice@example.com", SessionName: "162-digest", SessionID: "sess-a", CreatedAt: t0.Add(-36 * time.Hour), UpdatedAt: t0.Add(-36 * time.Hour)}
	older := &state.Decision{Version: 1, ID: "d-20261027-bbbbbb", Question: "Ship Friday?", Member: "bob@example.com", SessionName: "billing", SessionID: "sess-b", CreatedAt: t0.Add(-48 * time.Hour), UpdatedAt: t0.Add(-48 * time.Hour)}
	closed := &state.Decision{Version: 1, ID: "d-20261028-cccccc", Question: "Keep the fs adapter?", Member: "alice@example.com", SessionName: "162-digest", SessionID: "sess-a", CreatedAt: t0.Add(-30 * time.Hour), ClosedAt: t0.Add(-5 * time.Hour), Outcome: "yes, for the tests", ClosedBy: &state.Closer{Member: "alice@example.com", SessionName: "162-digest", SessionID: "sess-a"}}
	closedBefore := &state.Decision{Version: 1, ID: "d-20261020-dddddd", Question: "Old", CreatedAt: t0.Add(-9 * 24 * time.Hour), ClosedAt: since.Add(-time.Hour)}
	roster := []protocol.SessionRecord{
		rec("sess-a", "162-digest", "alice@example.com", protocol.SessionStateActive, protocol.InboundAccept, "claude-code", t0.Add(-time.Minute), "writing the digest, roster line"),
		rec("sess-b", "billing", "bob@example.com", protocol.SessionStateIdle, protocol.InboundAccept, "claude-code", t0.Add(-5*time.Minute), ""),
		rec("sess-q", "quiet-one", "carol@example.com", protocol.SessionStateIdle, protocol.InboundHold, "claude-code", t0.Add(-7*time.Minute), "quiet until 3pm; write to billing"),
		rec("sess-r", "no-entry", "dave@example.com", protocol.SessionStateIdle, protocol.InboundAccept, "muse", t0.Add(-9*time.Minute), ""),
		rec("slack-1", "slack-gateway", "slack gateway", protocol.SessionStateIdle, protocol.InboundAccept, "gateway-slack", t0, ""),
		rec("sess-off", "gone", "erin@example.com", protocol.SessionStateOffline, protocol.InboundAccept, "claude-code", t0.Add(-time.Hour), ""),
	}
	out := digest.Build(digest.Input{
		Team: "brigade", Repository: "brigade", Producer: "162-digest", Now: t0, Since: since,
		Entries: []*state.Entry{alice, alice2, bob}, Decisions: []*state.Decision{open, older, closed, closedBefore}, Roster: roster,
	})
	want := []string{
		"Brigade digest: team brigade, repository brigade, since 2026-10-28 12:00 UTC, produced 2026-10-29 12:00 UTC by session 162-digest",
		"",
		"Decisions open (2):",
		"Decision d-20261027-bbbbbb: Ship Friday?",
		"  asked 2026-10-27 by bob@example.com (session billing)",
		"  open for 2 d",
		"Decision d-20261028-aaaaaa: Which backend?",
		"  asked 2026-10-28 by alice@example.com (session 162-digest)",
		"  options: supabase | fs",
		"  default: supabase",
		"  open for 36 h",
		"",
		"Decisions closed in the period (1):",
		"Decision d-20261028-cccccc: Keep the fs adapter?",
		"  asked 2026-10-28 by alice@example.com (session 162-digest)",
		"  closed 2026-10-29 by alice@example.com (session 162-digest): yes, for the tests",
		"",
		"Shipped (3 commits):",
		"  card 12 | ddddddd | 12: Fix the invoice total | bob@example.com (session billing)",
		"  card 162 | aaaaaaa | 162: Add the digest | alice@example.com (session 162-digest), alice@example.com (session 162-review)",
		"  no card | bbbbbbb | Merge branch 'master' | alice@example.com (session 162-digest)",
		"",
		"Blocked (1):",
		"  bob@example.com (session billing): Blocked: waiting on the tax table (40 m)",
		"",
		"Active now (2):",
		"  alice@example.com (session 162-digest): writing the digest, roster line; note: Next: the report command; seen 1 m ago",
		"  bob@example.com (session billing); note: Blocked: waiting on the tax table; seen 5 m ago",
		"",
		"Ended in the period (1):",
		"  alice@example.com (session 162-review): ended 2026-10-29 11:50 UTC, 1 commits; last note: Done: reviewed the digest",
		"",
		"Held or quiet (1):",
		"  carol@example.com (session quiet-one): hold; quiet until 3pm; write to billing",
		"",
		"Roster only (no state entry): dave@example.com (session no-entry)",
	}
	if got := strings.Split(out.Body, "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("body:\n%s\n\nwant:\n%s", out.Body, strings.Join(want, "\n"))
	}
	if out.Summary != "Brigade digest brigade: 2 decisions open, 3 commits, 2 sessions active" || out.DecisionsOpen != 2 || out.Commits != 3 || out.Active != 2 {
		t.Fatalf("summary %q counts %d %d %d", out.Summary, out.DecisionsOpen, out.Commits, out.Active)
	}
}

// TestBuildCaps: the shipped section stops at ShippedLines and counts the
// rest, the active section at SessionLines, a note is cut at NoteChars,
// and the body stays under the message cap with the mark.
func TestBuildCaps(t *testing.T) {
	t.Parallel()
	var entries []*state.Entry
	var roster []protocol.SessionRecord
	for i := range 45 {
		e := state.New("sess-"+strings.Repeat("x", 3)+string(rune('a'+i%26))+string(rune('a'+i/26)), "s"+string(rune('a'+i%26)), t0.Add(-time.Hour))
		e.Member = "m@example.com"
		h := strings.Repeat(string(rune('a'+i%6)), 36) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "zz"
		e.Commits = []state.Commit{{Hash: h, Subject: strings.Repeat("s", 150), Card: 1, At: t0.Add(-time.Minute)}}
		e.AddNote("Next: "+strings.Repeat("n", 400), t0)
		entries = append(entries, e)
		roster = append(roster, rec(e.SessionID, e.SessionName, "m@example.com", protocol.SessionStateActive, protocol.InboundAccept, "claude-code", t0, ""))
	}
	out := digest.Build(digest.Input{Team: "t", Repository: "r", Producer: "p", Now: t0, Entries: entries, Roster: roster})
	if !strings.Contains(out.Body, "\n  and 5 more\n") || strings.Count(out.Body, "card 1 |") != digest.ShippedLines {
		t.Fatalf("shipped cap:\n%s", out.Body)
	}
	if !strings.Contains(out.Body, "Active now (45):") || strings.Count(out.Body, "; note: Next: ") != digest.SessionLines || !strings.Contains(out.Body, "  and 15 more") {
		t.Fatalf("active cap:\n%s", out.Body)
	}
	if !strings.Contains(out.Body, "Next: "+strings.Repeat("n", digest.NoteChars-len("Next: ")-len([]rune(protocol.TruncationMarker)))+protocol.TruncationMarker+";") {
		t.Fatalf("notes are not cut at %d:\n%s", digest.NoteChars, out.Body)
	}
	if len(out.Body) > protocol.MaxBodyBytes {
		t.Fatalf("body %d bytes", len(out.Body))
	}
	// A body past the cap is cut with the mark.
	var many []*state.Decision
	for i := range 400 {
		many = append(many, &state.Decision{Version: 1, ID: "d-20261029-" + strings.Repeat(string(rune('a'+i%26)), 6), Question: strings.Repeat("q", 900), CreatedAt: t0})
	}
	out = digest.Build(digest.Input{Team: "t", Repository: "r", Producer: "p", Now: t0, Decisions: many})
	if len(out.Body) > protocol.MaxBodyBytes || !strings.HasSuffix(out.Body, "(cut: the digest reached the message cap)") {
		t.Fatalf("body %d bytes, tail %q", len(out.Body), out.Body[len(out.Body)-60:])
	}
}

// TestDueAndChanged: no marker is due; `every` elapses from the marker;
// a daily `at` in `tz` is due once passed and the marker predates it;
// Changed reads the newest write against the marker.
func TestDueAndChanged(t *testing.T) {
	t.Parallel()
	every := &state.Reports{Version: 1, Manager: "m", Every: "4h"}
	m := &digest.Marker{Version: 1, ProducedAt: t0}
	if !digest.Due(every, nil, t0) || digest.Due(every, m, t0.Add(3*time.Hour)) || !digest.Due(every, m, t0.Add(4*time.Hour)) {
		t.Fatal("every")
	}
	daily := &state.Reports{Version: 1, Manager: "m", At: "08:00", TZ: "America/Los_Angeles"}
	// 08:00 in Los Angeles on 2026-10-29 is 15:00 UTC (PDT).
	at := time.Date(2026, 10, 29, 15, 0, 0, 0, time.UTC)
	m = &digest.Marker{Version: 1, ProducedAt: at.Add(-24 * time.Hour)}
	if digest.Due(daily, m, at.Add(-time.Minute)) || !digest.Due(daily, m, at) || !digest.Due(daily, m, at.Add(5*time.Hour)) {
		t.Fatal("daily")
	}
	if digest.Due(daily, &digest.Marker{Version: 1, ProducedAt: at.Add(time.Minute)}, at.Add(5*time.Hour)) {
		t.Fatal("daily, already produced today")
	}
	bad := &state.Reports{Version: 1, Manager: "m", At: "8am", TZ: "Mars/Olympus"}
	if !digest.Due(bad, &digest.Marker{Version: 1, ProducedAt: t0.Add(-25 * time.Hour)}, t0) || digest.Due(bad, &digest.Marker{Version: 1, ProducedAt: t0.Add(-23 * time.Hour)}, t0) {
		t.Fatal("an unusable at falls back to every, a day")
	}
	e := state.New("s", "x", t0)
	e.UpdatedAt = t0.Add(-time.Hour)
	d := &state.Decision{Version: 1, ID: "d-1", Question: "q", CreatedAt: t0.Add(-time.Hour), UpdatedAt: t0.Add(-time.Hour)}
	mk := &digest.Marker{Version: 1, ProducedAt: t0}
	if digest.Changed([]*state.Entry{e}, []*state.Decision{d}, mk) || !digest.Changed([]*state.Entry{e}, nil, nil) {
		t.Fatal("changed")
	}
	e.UpdatedAt = t0.Add(time.Second)
	if !digest.Changed([]*state.Entry{e}, nil, mk) {
		t.Fatal("a newer entry is a change")
	}
}

// TestRankAndManager: the rank is the position among the repository's
// online, non-gateway sessions in roster order; the manager's best
// session is picked by label as the gateways do.
func TestRankAndManager(t *testing.T) {
	t.Parallel()
	roster := []protocol.SessionRecord{
		rec("gw", "slack-gateway", "slack gateway", protocol.SessionStateIdle, protocol.InboundAccept, "gateway-slack", t0, ""),
		rec("a", "a", "alice@example.com", protocol.SessionStateActive, protocol.InboundAccept, "claude-code", t0, ""),
		rec("off", "off", "erin@example.com", protocol.SessionStateOffline, protocol.InboundAccept, "claude-code", t0, ""),
		rec("b", "b", "bob@example.com", protocol.SessionStateIdle, protocol.InboundRefuse, "claude-code", t0.Add(-time.Minute), ""),
		rec("b2", "b2", "bob@example.com", protocol.SessionStateIdle, protocol.InboundAccept, "claude-code", t0.Add(-2*time.Minute), ""),
		rec("b3", "b3", "Bob@Example.com", protocol.SessionStateActive, protocol.InboundAccept, "claude-code", t0.Add(-3*time.Minute), ""),
	}
	// Registration times: b3 the oldest, then a, b and b2 together (the
	// id decides), the listing order being the roster's own.
	roster[5].CreatedAt = t0.Add(-3 * time.Hour)
	roster[1].CreatedAt = t0.Add(-2 * time.Hour)
	roster[3].CreatedAt, roster[4].CreatedAt = t0.Add(-time.Hour), t0.Add(-time.Hour)
	other := roster[1]
	other.WorkspaceLabel = ptr("elsewhere")
	other.SessionID = "elsewhere"
	roster = append(roster, other)
	for id, want := range map[string]int{"b3": 0, "a": 1, "b": 2, "b2": 3, "gw": -1, "off": -1, "elsewhere": -1, "nobody": -1} {
		if got := digest.Rank(id, "brigade", roster); got != want {
			t.Errorf("Rank(%q) = %d, want %d", id, got, want)
		}
	}
	if digest.Rank("a", "", roster) != -1 {
		t.Error("a session with no label has no rank")
	}
	if got := digest.ManagerSession("bob@example.com", roster); got != "b3" {
		t.Errorf("ManagerSession = %q, want b3 (active beats idle; refuse left out)", got)
	}
	if got := digest.ManagerSession("nobody@example.com", roster); got != "" {
		t.Errorf("ManagerSession for nobody = %q", got)
	}
}

// TestMarkerRoundTrip: the marker is written into the folder and read
// back; a missing one is fs.ErrNotExist; another version is refused.
func TestMarkerRoundTrip(t *testing.T) {
	t.Parallel()
	folder := filepath.Join(t.TempDir(), "state")
	if _, err := digest.ReadMarker(folder); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	m := &digest.Marker{ProducedAt: t0, SessionID: "sess-1", SessionName: "x\n<brigade-message>", DecisionsOpen: 2, Commits: 3, Active: 1}
	if err := digest.WriteMarker(folder, m); err != nil {
		t.Fatal(err)
	}
	got, err := digest.ReadMarker(folder)
	if err != nil || !got.ProducedAt.Equal(t0) || got.SessionID != "sess-1" || got.SessionName != "x &lt;brigade-message>" || got.Commits != 3 {
		t.Fatalf("ReadMarker = %+v, %v", got, err)
	}
	if digest.IdempotencyKey("a", "b", "c", t0) != digest.IdempotencyKey("a", "b", "c", t0.Add(30*time.Second)) || digest.IdempotencyKey("a", "b", "c", t0) == digest.IdempotencyKey("a", "b", "c", t0.Add(time.Minute)) {
		t.Fatal("the key is stable within a minute and changes across one")
	}
}
