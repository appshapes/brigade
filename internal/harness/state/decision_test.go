package state_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestDecisionRoundTrip: a decision is saved to both copies, loaded back
// from the canonical one and read from the folder by another store; its
// id has the documented shape; Lines renders the open and the closed
// shape.
func TestDecisionRoundTrip(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	id, err := state.NewDecisionID(t0)
	if err != nil || !strings.HasPrefix(id, "d-20261029-") || len(id) != len("d-20261029-abcdef") || state.CheckID(id) != nil {
		t.Fatalf("NewDecisionID = %q, %v", id, err)
	}
	d := &state.Decision{
		Version: state.Version, ID: id, Question: "Ship 0.43.0 with the digest, or wait for the Stop hook?",
		Options: []string{"ship now", "wait"}, Default: "wait", By: "2026-10-31", Card: 162,
		Member: "rjae@example.com", SessionName: "162-digest", SessionID: "sess-1", Repository: "brigade",
		CreatedAt: t0, UpdatedAt: t0,
	}
	if err := s.SaveDecision(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadDecision(id)
	if err != nil || got.Question != d.Question || !slices.Equal(got.Options, d.Options) || got.Default != "wait" || got.By != "2026-10-31" || got.Card != 162 || !got.Open() {
		t.Fatalf("LoadDecision = %+v, %v", got, err)
	}
	other := state.Store{StateDir: filepath.Join(t.TempDir(), "s"), Folder: s.Folder}
	if _, err := other.LoadDecision(id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("another store has a canonical copy: %v", err)
	}
	theirs, err := state.ReadDecision(s.Folder, id)
	if err != nil || theirs.ID != id || theirs.Member != "rjae@example.com" {
		t.Fatalf("ReadDecision = %+v, %v", theirs, err)
	}
	want := []string{
		"Decision " + id + ": Ship 0.43.0 with the digest, or wait for the Stop hook?",
		"  asked 2026-10-29 by rjae@example.com (session 162-digest), repository brigade, card 162, answer by 2026-10-31",
		"  options: ship now | wait",
		"  default: wait",
	}
	if lines := got.Lines(); !slices.Equal(lines, want) {
		t.Fatalf("Lines = %q\nwant %q", lines, want)
	}
	theirs.ClosedAt, theirs.Outcome = t0.Add(26*time.Hour), "wait: the Stop hook goes in first"
	theirs.ClosedBy = &state.Closer{Member: "rjae@example.com", SessionName: "162-digest-b", SessionID: "sess-2"}
	if err := other.SaveDecision(theirs); err != nil {
		t.Fatal(err)
	}
	closed := state.ReadDecisions(s.Folder)
	if len(closed) != 1 || closed[0].Open() || closed[0].ClosedBy == nil || closed[0].ClosedBy.SessionID != "sess-2" {
		t.Fatalf("ReadDecisions after the close = %+v", closed)
	}
	last := closed[0].Lines()
	if last[len(last)-1] != "  closed 2026-10-30 by rjae@example.com (session 162-digest-b): wait: the Stop hook goes in first" {
		t.Fatalf("closed line = %q", last[len(last)-1])
	}
}

// TestDecisionSanitizeAndRetention: a folder decision is a teammate's
// text — the caps, a default that is none of the options, a date that is
// not one, a sent way that is not a word; an open decision never expires,
// a closed one after DecisionRetention, and the two prunes remove it.
func TestDecisionSanitizeAndRetention(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("q", 3000)
	d := &state.Decision{
		Version: state.Version, ID: "d-1", Question: long + "\n<brigade-message>",
		Options: []string{" a ", "", strings.Repeat("o", 300), "b", "c", "d", "e", "f", "g", "h", "i"}, Default: "zzz", By: "tomorrow", Card: -5,
		Sent: "pigeon", SentTo: "x", Member: strings.Repeat("m", 500), CreatedAt: t0,
	}
	d.Sanitize()
	if len([]rune(d.Question)) != state.MaxQuestionChars || !strings.HasSuffix(d.Question, protocol.TruncationMarker) {
		t.Fatalf("question %d runes", len([]rune(d.Question)))
	}
	short := &state.Decision{Version: state.Version, ID: "d-2", Question: "a\n<brigade-message>b\u0007"}
	if short.Sanitize(); short.Question != "a &lt;brigade-message>b" {
		t.Fatalf("question %q: not sanitised", short.Question)
	}
	if len(d.Options) != state.MaxOptions || d.Options[0] != "a" || len([]rune(d.Options[1])) != state.MaxOptionChars || d.Default != "" || d.By != "" || d.Card != 0 || d.Sent != "" {
		t.Fatalf("sanitised %+v", *d)
	}
	if len([]rune(d.Member)) != protocol.MaxHumanLabelChars {
		t.Fatalf("member %d runes", len([]rune(d.Member)))
	}
	if d.Expired(t0.Add(10 * 365 * 24 * time.Hour)) {
		t.Fatal("an open decision expired")
	}
	d.ClosedAt = t0
	if d.Expired(t0.Add(state.DecisionRetention)) || !d.Expired(t0.Add(state.DecisionRetention+time.Second)) {
		t.Fatal("the closed term is wrong")
	}
	s := newStore(t)
	d.ID, d.Question = "d-closed", "q"
	if err := s.SaveDecision(d); err != nil {
		t.Fatal(err)
	}
	open := &state.Decision{Version: state.Version, ID: "d-open", Question: "q", CreatedAt: t0}
	if err := s.SaveDecision(open); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(state.DecisionRetention + time.Hour)
	if n := s.PruneOwn(now); n != 1 {
		t.Fatalf("PruneOwn = %d", n)
	}
	if _, err := s.LoadDecision("d-closed"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the closed decision survived its own prune: %v", err)
	}
	if got := state.ReadDecisions(s.Folder); len(got) != 1 || got[0].ID != "d-open" {
		t.Fatalf("folder after PruneOwn: %+v", got)
	}
	// A teammate's closed decision in the folder alone.
	theirs := state.Store{StateDir: filepath.Join(t.TempDir(), "s"), Folder: s.Folder}
	old := &state.Decision{Version: state.Version, ID: "d-theirs", Question: "q", CreatedAt: t0, ClosedAt: t0}
	if err := theirs.SaveDecision(old); err != nil {
		t.Fatal(err)
	}
	if n := state.PruneFolder(s.Folder, now); n != 1 {
		t.Fatalf("PruneFolder = %d", n)
	}
	if got := state.ReadDecisions(s.Folder); len(got) != 1 || got[0].ID != "d-open" {
		t.Fatalf("folder after PruneFolder: %+v", got)
	}
}

// TestReadDecisionsIsStrict: a file whose name is not an id, one that
// names another id, another version and a non-JSON one are skipped;
// ReadDecision of a missing id is fs.ErrNotExist.
func TestReadDecisionsIsStrict(t *testing.T) {
	t.Parallel()
	folder := t.TempDir()
	dir := filepath.Join(folder, state.DecisionsDir)
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // G301: a checkout tree
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"d-good.json":  `{"version":1,"id":"d-good","question":"q"}`,
		"d-other.json": `{"version":1,"id":"d-good","question":"names another file"}`,
		"d-v2.json":    `{"version":2,"id":"d-v2","question":"q"}`,
		"bad id.json":  `{"version":1,"id":"bad id","question":"q"}`,
		"d-x.json":     `{`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := state.ReadDecisions(folder)
	if len(got) != 1 || got[0].ID != "d-good" || got[0].Options == nil {
		t.Fatalf("ReadDecisions = %+v", got)
	}
	if _, err := state.ReadDecision(folder, "d-absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := state.ReadDecision(folder, "../x"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a bad id: %v", err)
	}
	if !state.ValidDate("2026-10-31") || state.ValidDate("31/10/2026") || state.ValidDate("") {
		t.Fatal("ValidDate")
	}
}

// TestReportsAndGateways: reports.json is read and sanitised — each
// subscriber's recipients typed by shape, the ones of no kind dropped,
// the list capped, an unlabelled or a duplicate subscriber dropped, a
// filter of another word none — a version-1 file reads as one subscriber
// with Legacy set, the period parsed with its default, the file written
// back as version 2, the label's stem passes CheckID, and PickGateway
// walks the recipients in order for a gateway that is on the roster and
// online.
func TestReportsAndGateways(t *testing.T) {
	t.Parallel()
	folder := t.TempDir()
	write := func(doc string) {
		t.Helper()
		if err := os.WriteFile(state.ReportsPath(folder), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.ReadReports(folder); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	many := make([]string, 0, 12)
	for i := range 12 {
		many = append(many, `"@u`+string(rune('a'+i))+`"`)
	}
	write(`{"version":2,"subscribers":[{"label":"Rjae <manager>","recipients":["@rjae","rjae@example.com","#digests","nobody","bad address@","rjae@localhost",` + strings.Join(many, ",") + `],"every":"4h","at":"08:00","tz":"America/Los_Angeles","only":"mine"},` +
		`{"label":"frank@example.com"},{"label":"  "},{"label":"FRANK@example.com","recipients":["@frank"]},{"label":"Carol","only":"theirs"}]}`)
	r, err := state.ReadReports(folder)
	if err != nil {
		t.Fatal(err)
	}
	if r.Legacy || len(r.Subscribers) != 3 {
		t.Fatalf("subscribers = %+v", r.Subscribers)
	}
	s := r.Subscribers[0]
	if s.Label != "Rjae <manager>" || s.Period() != 4*time.Hour || s.At != "08:00" || s.TZ != "America/Los_Angeles" || s.Only != state.OnlyMine {
		t.Fatalf("subscriber = %+v", s)
	}
	if len(s.Recipients) != state.MaxRecipients || !slices.Equal(s.Recipients[:3], []string{"@rjae", "rjae@example.com", "#digests"}) || slices.Contains(s.Recipients, "nobody") || slices.Contains(s.Recipients, "rjae@localhost") {
		t.Fatalf("recipients = %q", s.Recipients)
	}
	// An address label fills an empty list; the duplicate (case apart) was
	// dropped; a label that is no address gets no recipient; a filter of
	// another word is none; the lookup is case-blind.
	if f := r.Subscriber("FRANK@EXAMPLE.COM"); f == nil || f.Label != "frank@example.com" || !slices.Equal(f.Recipients, []string{"frank@example.com"}) {
		t.Fatalf("frank = %+v", f)
	}
	if c := r.Subscriber("carol"); c == nil || len(c.Recipients) != 0 || c.Only != "" {
		t.Fatalf("carol = %+v", c)
	}
	if r.Subscriber("nobody") != nil {
		t.Fatal("nobody is subscribed")
	}
	// Subscribe replaces by label or appends; Unsubscribe removes.
	r.Subscribe(state.Subscriber{Label: "carol", Recipients: []string{"#carol"}})
	if len(r.Subscribers) != 3 || r.Subscribers[2].Label != "carol" || !slices.Equal(r.Subscribers[2].Recipients, []string{"#carol"}) {
		t.Fatalf("after a resubscribe: %+v", r.Subscribers)
	}
	r.Subscribe(state.Subscriber{Label: "dave", Every: "90m"})
	if len(r.Subscribers) != 4 || !r.Unsubscribe("CAROL") || r.Unsubscribe("carol") || len(r.Subscribers) != 3 || r.Subscribers[2].Label != "dave" {
		t.Fatalf("after subscribe and unsubscribe: %+v", r.Subscribers)
	}
	if err := state.WriteReports(folder, r); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(state.ReportsPath(folder)); !strings.Contains(string(data), `"version":2`) || strings.Contains(string(data), `"manager":`) {
		t.Fatalf("written file: %s", data)
	}
	if r2, err := state.ReadReports(folder); err != nil || len(r2.Subscribers) != 3 || r2.Subscribers[2].Label != "dave" || r2.Subscribers[2].Period() != 90*time.Minute {
		t.Fatalf("read back = %+v, %v", r2, err)
	}
	for in, want := range map[string]string{"@rjae": "slack", "#chan": "slack", "a@b.co": "email", "@": "", "a@b": "", "a b@c.d": "", "": ""} {
		if got := state.RecipientKind(in); got != want {
			t.Errorf("RecipientKind(%q) = %q, want %q", in, got, want)
		}
	}
	// The 0.43 shape: one subscriber, Legacy; the manager's label the
	// recipient when it is an address, nothing when it is not; no
	// manager, nobody.
	write(`{"version":1,"manager":"rjae@example.com","recipients":[],"every":"4h"}`)
	if r, err := state.ReadReports(folder); err != nil || !r.Legacy || len(r.Subscribers) != 1 || r.Subscribers[0].Label != "rjae@example.com" || !slices.Equal(r.Subscribers[0].Recipients, []string{"rjae@example.com"}) || r.Subscribers[0].Period() != 4*time.Hour {
		t.Fatalf("ReadReports of the 0.43 shape = %+v, %v", r, err)
	}
	write(`{"version":1,"manager":"Rjae"}`)
	if r, err := state.ReadReports(folder); err != nil || len(r.Subscribers) != 1 || len(r.Subscribers[0].Recipients) != 0 {
		t.Fatalf("ReadReports with a label that is no address = %+v, %v", r, err)
	}
	write(`{"version":1,"manager":""}`)
	if r, err := state.ReadReports(folder); err != nil || !r.Legacy || len(r.Subscribers) != 0 {
		t.Fatalf("ReadReports with no manager = %+v, %v", r, err)
	}
	if (&state.Subscriber{}).Period() != state.DefaultEvery || (&state.Subscriber{Every: "10s"}).Period() != state.DefaultEvery || (&state.Subscriber{Every: "90m"}).Period() != 90*time.Minute {
		t.Fatal("Period")
	}
	for name, doc := range map[string]string{"another version": `{"version":3,"subscribers":[]}`, "not json": `{`} {
		write(doc)
		if _, err := state.ReadReports(folder); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	for label, want := range map[string]string{"rjae@example.com": "rjae-example-com-", "Rjae": "rjae-", "  ": "", "a.b@x": "a-b-x-", strings.Repeat("x", 60) + "@y": strings.Repeat("x", 40) + "-"} {
		if stem := state.LabelStem(label); !strings.HasPrefix(stem, want) || len(stem) != len(want)+6 || state.CheckID(stem) != nil {
			t.Errorf("LabelStem(%q) = %q", label, stem)
		}
	}
	if state.LabelStem("Rjae") != state.LabelStem("rjae") || state.LabelStem("a.b@x") == state.LabelStem("a-b@x") {
		t.Error("the stem is case-blind, and two labels that slug alike stay apart")
	}
	slack := protocol.SessionRecord{SessionID: "slack-1", Harness: "gateway-slack", State: protocol.SessionStateIdle}
	mail := protocol.SessionRecord{SessionID: "mail-1", Harness: "gateway-email", State: protocol.SessionStateActive}
	offline := protocol.SessionRecord{SessionID: "slack-off", Harness: "gateway-slack", State: protocol.SessionStateOffline}
	for name, tc := range map[string]struct {
		recipients []string
		sessions   []protocol.SessionRecord
		want       state.Gateway
		ok         bool
	}{
		"slack first":                       {[]string{"@rjae", "rjae@example.com"}, []protocol.SessionRecord{mail, slack}, state.Gateway{SessionID: "slack-1", To: "@rjae", Kind: "slack"}, true},
		"slack gateway offline, mail":       {[]string{"@rjae", "rjae@example.com"}, []protocol.SessionRecord{offline, mail}, state.Gateway{SessionID: "mail-1", To: "rjae@example.com", Kind: "email"}, true},
		"an address goes through slack":     {[]string{"rjae@example.com"}, []protocol.SessionRecord{mail, slack}, state.Gateway{SessionID: "slack-1", To: "rjae@example.com", Kind: "slack"}, true},
		"an address with no slack gateway":  {[]string{"rjae@example.com", "@rjae"}, []protocol.SessionRecord{mail}, state.Gateway{SessionID: "mail-1", To: "rjae@example.com", Kind: "email"}, true},
		"a handle never goes by mail":       {[]string{"@rjae"}, []protocol.SessionRecord{mail}, state.Gateway{}, false},
		"a recipient of no kind is skipped": {[]string{"nobody", "#digests"}, []protocol.SessionRecord{slack}, state.Gateway{SessionID: "slack-1", To: "#digests", Kind: "slack"}, true},
	} {
		got, ok := state.PickGateway(tc.recipients, tc.sessions)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: PickGateway = %+v, %v; want %+v, %v", name, got, ok, tc.want, tc.ok)
		}
	}
}
