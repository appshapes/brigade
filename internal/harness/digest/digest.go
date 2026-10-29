// Package digest assembles the digest from the team's state folder
// (card 162, .context/plans/manager-digest.md §5.4; card 167 sends it to
// every subscriber): the decisions waiting on a person and the meaningful
// contributions since the last digest, as plain text with a stable label
// at the start of every line and the caps that keep the body under the
// wire's 16 KiB. It reads the folder and the roster only, never git: the
// entries carry their commits. The producing watcher
// (internal/harness/watch) and `brigade report show|send`
// (internal/harness/commands) build the same text from the same inputs;
// the period, the per-subscriber marker and the idempotency key of a
// send live here too, so the two agree.
package digest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // a subscriber's `tz` must resolve on a machine with no zoneinfo (Windows)

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/gateway"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/protocol"
)

// The caps of §5.4 (ruling 3): a note is cut at NoteChars with a mark;
// the shipped section lists at most ShippedLines commits; the active and
// the ended sections at most SessionLines sessions each; the whole body
// stays under protocol.MaxBodyBytes.
const (
	NoteChars    = 200
	ShippedLines = 40
	SessionLines = 30
	// LegacyMarkerFile is the one marker of 0.43, `digests/last.json`:
	// read for the subscriber a version-1 reports file names until they
	// have their own, and carried over by the first write.
	LegacyMarkerFile = "last.json"
	// dateLayout and timeLayout are the digest's spellings.
	dateLayout = "2006-01-02"
	timeLayout = "2006-01-02 15:04 UTC"
)

// A Marker is `digests/<stem>.json`, one per subscriber (the stem is
// state.LabelStem of their label): when their last digest was produced,
// by which session, and its counts. The producers read it to know the
// period has elapsed and to stand down when another produced first.
type Marker struct {
	Version       int       `json:"version"`
	Label         string    `json:"label,omitzero"`
	ProducedAt    time.Time `json:"produced_at"`
	SessionID     string    `json:"session_id"`
	SessionName   string    `json:"session_name"`
	DecisionsOpen int       `json:"decisions_open"`
	Commits       int       `json:"commits"`
	Active        int       `json:"active"`
}

// MarkerPath is <folder>/digests/<stem>.json for the subscriber of the
// label.
func MarkerPath(folder, label string) string {
	return filepath.Join(folder, state.DigestsDir, state.LabelStem(label)+".json")
}

// LegacyMarkerPath is <folder>/digests/last.json.
func LegacyMarkerPath(folder string) string {
	return filepath.Join(folder, state.DigestsDir, LegacyMarkerFile)
}

// ReadMarker reads the subscriber's marker, a teammate's text: capped and
// checked. A missing one satisfies errors.Is(err, fs.ErrNotExist).
func ReadMarker(folder, label string) (*Marker, error) {
	return readMarker(MarkerPath(folder, label))
}

// MarkerOf is the marker the producer judges the subscriber by: their
// own, else — for the one subscriber of a version-1 file — the legacy
// marker. A missing one satisfies errors.Is(err, fs.ErrNotExist).
func MarkerOf(folder string, reports *state.Reports, s *state.Subscriber) (*Marker, error) {
	m, err := ReadMarker(folder, s.Label)
	if errors.Is(err, fs.ErrNotExist) && reports != nil && reports.Legacy {
		return readMarker(LegacyMarkerPath(folder))
	}
	return m, err
}

// AdoptLegacyMarker moves the legacy marker to the subscriber's own, with
// the label set, when the subscriber has none and the legacy one is
// there: the first write of a version-2 file calls it for the subscriber
// the version-1 file named, so their period runs on. Nothing to do is
// not an error.
func AdoptLegacyMarker(folder, label string) error {
	if _, err := ReadMarker(folder, label); err == nil {
		return nil
	}
	m, err := readMarker(LegacyMarkerPath(folder))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	m.Label = label
	if err := WriteMarker(folder, m); err != nil {
		return err
	}
	return os.Remove(LegacyMarkerPath(folder))
}

// readMarker reads one marker file within the folder rules.
func readMarker(path string) (*Marker, error) {
	data, err := state.ReadFolderFile(path)
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := state.Unmarshal(data, &m); err != nil {
		return nil, errors.New("digest: the marker is not a JSON document of the expected shape")
	}
	if m.Version != state.Version {
		return nil, errors.New("digest: the marker's version is not this package's")
	}
	m.SessionName = protocol.SanitizeName(strings.Join(strings.Fields(protocol.Sanitize(m.SessionName)), " "))
	m.Label = protocol.SanitizeLabel(strings.Join(strings.Fields(protocol.Sanitize(m.Label)), " "))
	if state.CheckID(m.SessionID) != nil {
		m.SessionID = ""
	}
	return &m, nil
}

// WriteMarker writes the subscriber's marker — the one of m.Label —
// into the folder, atomically.
func WriteMarker(folder string, m *Marker) error {
	m.Version = state.Version
	data, err := state.Marshal(m)
	if err != nil {
		return err
	}
	path := MarkerPath(folder, m.Label)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // G301: a checkout tree is an ordinary directory
		return err
	}
	return adapterkit.WriteAtomic(path, data)
}

// Due reports whether the subscriber's digest is due at now: with `at`
// set, a daily digest at that time in `tz` — due once today's `at` has
// passed and the marker predates it; otherwise every `every` (the
// default a day) since the marker. No marker is due.
func Due(s *state.Subscriber, m *Marker, now time.Time) bool {
	if m == nil || m.ProducedAt.IsZero() {
		return true
	}
	if at, ok := dailyAt(s, now); ok {
		return !now.Before(at) && m.ProducedAt.Before(at)
	}
	return now.Sub(m.ProducedAt) >= s.Period()
}

// dailyAt is today's `at` in `tz` as an instant, when the subscriber
// names a usable one: HH:MM, and a zone time.LoadLocation knows (UTC
// when none).
func dailyAt(s *state.Subscriber, now time.Time) (time.Time, bool) {
	if s.At == "" {
		return time.Time{}, false
	}
	hm, err := time.Parse("15:04", s.At)
	if err != nil {
		return time.Time{}, false
	}
	loc := time.UTC
	if s.TZ != "" {
		l, err := time.LoadLocation(s.TZ)
		if err != nil {
			return time.Time{}, false
		}
		loc = l
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), hm.Hour(), hm.Minute(), 0, 0, loc), true
}

// Changed reports whether any entry or decision was written since the
// marker: a quiet team reports nothing (ruling 2). With no marker,
// anything at all counts.
func Changed(entries []*state.Entry, decisions []*state.Decision, m *Marker) bool {
	since := time.Time{}
	if m != nil {
		since = m.ProducedAt
	}
	for _, e := range entries {
		if e.UpdatedAt.After(since) {
			return true
		}
	}
	for _, d := range decisions {
		if d.UpdatedAt.After(since) || d.CreatedAt.After(since) {
			return true
		}
	}
	return false
}

// Rank is this session's position among the repository's online sessions
// — the gateways left out, the repository told by the workspace label —
// ordered by registration time, the oldest first, then by id: an order
// every session derives alike from the roster's own facts, where the
// roster's listing order (active first, the freshest heartbeat first)
// would move between two ticks and let two sessions each see rank 0.
// -1 when this session is not among them (no label shared, or the roster
// does not list it). The producer with rank 0 produces at once; rank n
// waits n delays and stands down when a marker appeared.
func Rank(self, label string, sessions []protocol.SessionRecord) int {
	if label == "" {
		return -1
	}
	var candidates []protocol.SessionRecord
	for i := range sessions {
		s := &sessions[i]
		if s.State == protocol.SessionStateOffline || gateway.IsHarness(s.Harness) || s.WorkspaceLabel == nil || *s.WorkspaceLabel != label {
			continue
		}
		candidates = append(candidates, *s)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].SessionID < candidates[j].SessionID
	})
	for i := range candidates {
		if candidates[i].SessionID == self {
			return i
		}
	}
	return -1
}

// Input is what one digest is built from.
type Input struct {
	// Team and Repository name the header; Producer is the producing
	// session's name; Now the production time; Since the period's start
	// (the subscriber's last marker's time, zero for their first digest).
	Team, Repository, Producer string
	Now, Since                 time.Time
	Entries                    []*state.Entry
	Decisions                  []*state.Decision
	// Roster is the team's online sessions as the backend lists them.
	Roster []protocol.SessionRecord
	// For is the subscriber's label and Only their filter: with
	// state.OnlyMine the decision sections list only the decisions
	// addressed to For; the contribution sections stay the team's.
	For, Only string
}

// Output is the digest: the body, the summary carrying the counts, and
// the counts for the marker.
type Output struct {
	Body          string
	Summary       string
	DecisionsOpen int
	Commits       int
	Active        int
}

// Build assembles the digest: the header, then the sections of §5.4 in
// order, each omitted when empty, under the caps.
func Build(in Input) Output {
	var out Output
	since := in.Since
	period := "since the start"
	if !since.IsZero() {
		period = "since " + since.UTC().Format(timeLayout)
	}
	lines := []string{"Brigade digest: team " + in.Team + ", repository " + in.Repository + ", " + period + ", produced " + in.Now.UTC().Format(timeLayout) + " by session " + in.Producer}

	decisions, whose := in.Decisions, ""
	if in.Only == state.OnlyMine && in.For != "" {
		decisions, whose = decisionsFor(in.Decisions, in.For), " for "+in.For
	}
	open, closed := splitDecisions(decisions, since)
	out.DecisionsOpen = len(open)
	if len(open) > 0 {
		lines = append(lines, "", "Decisions open"+whose+" ("+strconv.Itoa(len(open))+"):")
		for _, d := range open {
			lines = append(lines, d.Lines()...)
			lines = append(lines, "  open for "+ageOf(in.Now, d.CreatedAt))
		}
	}
	if len(closed) > 0 {
		lines = append(lines, "", "Decisions closed in the period"+whose+" ("+strconv.Itoa(len(closed))+"):")
		for _, d := range closed {
			lines = append(lines, d.Lines()...)
		}
	}

	shipped := shippedCommits(in.Entries, since)
	out.Commits = len(shipped)
	if len(shipped) > 0 {
		lines = append(lines, "", "Shipped ("+strconv.Itoa(len(shipped))+" commits):")
		for i, c := range shipped {
			if i == ShippedLines {
				lines = append(lines, "  and "+strconv.Itoa(len(shipped)-ShippedLines)+" more")
				break
			}
			lines = append(lines, "  "+c.line())
		}
	}

	entryOf := map[string]*state.Entry{}
	for _, e := range in.Entries {
		entryOf[e.SessionID] = e
	}
	var blocked []string
	for _, e := range in.Entries {
		if n := latestNote(e); n != nil && strings.HasPrefix(n.Text, "Blocked:") {
			blocked = append(blocked, "  "+who(e)+": "+cutNote(n.Text)+" ("+ageOf(in.Now, n.At)+")")
		}
	}
	if len(blocked) > 0 {
		lines = append(lines, "", "Blocked ("+strconv.Itoa(len(blocked))+"):")
		lines = append(lines, blocked...)
	}

	online := map[string]*protocol.SessionRecord{}
	var active, held, rosterOnly []string
	for i := range in.Roster {
		s := &in.Roster[i]
		if s.State == protocol.SessionStateOffline || gateway.IsHarness(s.Harness) {
			continue
		}
		online[s.SessionID] = s
		if s.Inbound == protocol.InboundHold || s.Inbound == protocol.InboundRefuse {
			line := "  " + rosterWho(s) + ": " + s.Inbound
			if s.SessionDescription != nil && *s.SessionDescription != "" {
				line += "; " + cutNote(protocol.SanitizeDescription(*s.SessionDescription))
			}
			held = append(held, line)
			continue
		}
		e, ok := entryOf[s.SessionID]
		if !ok {
			rosterOnly = append(rosterOnly, rosterWho(s))
			continue
		}
		if !e.EndedAt.IsZero() {
			continue
		}
		line := "  " + who(e)
		if d := latestDoing(e, s); d != "" {
			line += ": " + cutNote(d)
		}
		if n := latestNote(e); n != nil {
			line += "; note: " + cutNote(n.Text)
		}
		line += "; seen " + ageOf(in.Now, s.LastSeenAt) + " ago"
		active = append(active, line)
	}
	out.Active = len(active)
	if len(active) > 0 {
		lines = append(lines, "", "Active now ("+strconv.Itoa(len(active))+"):")
		lines = append(lines, capLines(active, SessionLines)...)
	}

	var ended []string
	for _, e := range in.Entries {
		if e.EndedAt.IsZero() || !e.EndedAt.After(since) {
			continue
		}
		line := "  " + who(e) + ": ended " + e.EndedAt.UTC().Format(timeLayout) + ", " + strconv.Itoa(len(e.Commits)) + " commits"
		if n := latestNote(e); n != nil {
			line += "; last note: " + cutNote(n.Text)
		}
		ended = append(ended, line)
	}
	if len(ended) > 0 {
		lines = append(lines, "", "Ended in the period ("+strconv.Itoa(len(ended))+"):")
		lines = append(lines, capLines(ended, SessionLines)...)
	}
	if len(held) > 0 {
		lines = append(lines, "", "Held or quiet ("+strconv.Itoa(len(held))+"):")
		lines = append(lines, held...)
	}
	if len(rosterOnly) > 0 {
		lines = append(lines, "", "Roster only (no state entry): "+strings.Join(rosterOnly, "; "))
	}
	out.Body = capBody(strings.Join(lines, "\n"))
	out.Summary = protocol.TruncateRunes("Brigade digest "+in.Repository+": "+strconv.Itoa(out.DecisionsOpen)+" decisions open, "+
		strconv.Itoa(out.Commits)+" commits, "+strconv.Itoa(out.Active)+" sessions active", protocol.MaxSummaryChars)
	return out
}

// decisionsFor is the decisions addressed to the label, compared without
// case; a 0.43 decision, which names nobody, is for nobody here.
func decisionsFor(decisions []*state.Decision, label string) []*state.Decision {
	var out []*state.Decision
	for _, d := range decisions {
		if d.To != "" && strings.EqualFold(d.To, label) {
			out = append(out, d)
		}
	}
	return out
}

// splitDecisions is the open ones, oldest first, and the ones closed in
// the period.
func splitDecisions(decisions []*state.Decision, since time.Time) (open, closed []*state.Decision) {
	for _, d := range decisions {
		switch {
		case d.Open():
			open = append(open, d)
		case d.ClosedAt.After(since):
			closed = append(closed, d)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return open[i].CreatedAt.Before(open[j].CreatedAt) })
	sort.SliceStable(closed, func(i, j int) bool { return closed[i].ClosedAt.Before(closed[j].ClosedAt) })
	return open, closed
}

// A shippedCommit is one commit of the period with who made it: a commit
// two sessions of one member both list is one line naming both.
type shippedCommit struct {
	state.Commit
	who []string
}

func (c shippedCommit) line() string {
	card := "no card"
	if c.Card > 0 {
		card = "card " + strconv.Itoa(c.Card)
	}
	hash := c.Hash
	if len(hash) > 7 {
		hash = hash[:7]
	}
	return card + " | " + hash + " | " + c.Subject + " | " + strings.Join(c.who, ", ")
}

// shippedCommits is every commit of the period across the entries, one
// per hash, the carded ones first by card then time, the no-card ones
// last by time.
func shippedCommits(entries []*state.Entry, since time.Time) []shippedCommit {
	byHash := map[string]*shippedCommit{}
	var order []string
	for _, e := range entries {
		for _, c := range e.Commits {
			if !c.At.After(since) {
				continue
			}
			s, ok := byHash[c.Hash]
			if !ok {
				s = &shippedCommit{Commit: c}
				byHash[c.Hash] = s
				order = append(order, c.Hash)
			}
			if w := who(e); !slices.Contains(s.who, w) {
				s.who = append(s.who, w)
			}
		}
	}
	out := make([]shippedCommit, 0, len(order))
	for _, h := range order {
		out = append(out, *byHash[h])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case (a.Card > 0) != (b.Card > 0):
			return a.Card > 0
		case a.Card != b.Card:
			return a.Card < b.Card
		}
		return a.At.Before(b.At)
	})
	return out
}

// who names an entry's session: the member and the session name.
func who(e *state.Entry) string {
	if e.Member == "" {
		return "session " + e.SessionName
	}
	return e.Member + " (session " + e.SessionName + ")"
}

// rosterWho names a roster session the same way.
func rosterWho(s *protocol.SessionRecord) string {
	name := protocol.SanitizeName(strings.Join(strings.Fields(protocol.Sanitize(s.SessionName)), " "))
	label := protocol.SanitizeLabel(strings.Join(strings.Fields(protocol.Sanitize(s.HumanLabel)), " "))
	if label == "" {
		return "session " + name
	}
	return label + " (session " + name + ")"
}

// latestNote is the entry's newest note, nil for none.
func latestNote(e *state.Entry) *state.Stamped {
	if len(e.Notes) == 0 {
		return nil
	}
	return &e.Notes[len(e.Notes)-1]
}

// latestDoing is the session's doing line: the roster's, which is the
// line in force, else the entry's newest.
func latestDoing(e *state.Entry, s *protocol.SessionRecord) string {
	if s != nil && s.SessionDescription != nil && *s.SessionDescription != "" {
		return protocol.SanitizeDescription(*s.SessionDescription)
	}
	if len(e.Doing) == 0 {
		return ""
	}
	return e.Doing[len(e.Doing)-1].Text
}

// cutNote cuts a note or a line at NoteChars with the mark.
func cutNote(s string) string {
	return protocol.TruncateRunes(strings.Join(strings.Fields(s), " "), NoteChars)
}

// capLines keeps the first limit lines and says how many more there were.
func capLines(lines []string, limit int) []string {
	if len(lines) <= limit {
		return lines
	}
	return append(lines[:limit:limit], "  and "+strconv.Itoa(len(lines)-limit)+" more")
}

// capBody keeps the body under the wire's cap, cutting at a line and
// saying so.
func capBody(body string) string {
	const mark = "\n(cut: the digest reached the message cap)"
	if len(body) <= protocol.MaxBodyBytes {
		return body
	}
	keep := protocol.MaxBodyBytes - len(mark)
	if i := strings.LastIndex(body[:keep], "\n"); i > 0 {
		keep = i
	}
	return body[:keep] + mark
}

// ageOf is a short age: "3 m", "2 h", "5 d".
func ageOf(now, at time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + " d"
}

// SessionOf picks a member's best session on the roster by their label,
// the way the gateways and `send --member` do: the gateways and the
// sessions that refuse left out, active before idle before offline, the
// most recently seen first; "" when they have none.
func SessionOf(label string, sessions []protocol.SessionRecord) string {
	var best *protocol.SessionRecord
	for i := range sessions {
		s := &sessions[i]
		if !strings.EqualFold(strings.TrimSpace(s.HumanLabel), strings.TrimSpace(label)) || gateway.IsHarness(s.Harness) || s.Inbound == protocol.InboundRefuse {
			continue
		}
		if best == nil || stateRank(s.State) < stateRank(best.State) || (stateRank(s.State) == stateRank(best.State) && s.LastSeenAt.After(best.LastSeenAt)) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	return best.SessionID
}

// stateRank orders active before idle before offline.
func stateRank(state string) int {
	switch state {
	case protocol.SessionStateActive:
		return 0
	case protocol.SessionStateIdle:
		return 1
	}
	return 2
}

// IdempotencyKey derives the key of one send: base64url, unpadded, of
// sha256(sender NUL recipient NUL body NUL floor(unix seconds / 60)),
// stable within a minute, so a retry inside the minute is one message.
// `brigade send` (internal/harness/commands) derives its own from here.
func IdempotencyKey(sender, recipient, body string, now time.Time) string {
	h := sha256.New()
	h.Write([]byte(sender))
	h.Write([]byte{0})
	h.Write([]byte(recipient))
	h.Write([]byte{0})
	h.Write([]byte(body))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(now.Unix()/60, 10)))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Delivery is how a digest or a decision reached a subscriber: through
// which gateway or session, to whom, and the message id.
type Delivery struct {
	How       string
	To        string
	SessionID string
	MessageID string
}

// Deliver sends body to the subscriber (plan §5.5): through the first of
// their recipients whose gateway is on the roster and not offline, under
// the gateway's `to:` first line — Slack before email whatever the
// recipient's shape — else to their own best session as a plain team
// message. ErrNoRecipient when neither is there. The key is the
// digest's, so a retry inside the minute is one message.
func Deliver(ctx context.Context, client Sender, self string, s *state.Subscriber, roster []protocol.SessionRecord, body, summary string, now time.Time) (Delivery, error) {
	var d Delivery
	if gw, ok := state.PickGateway(s.Recipients, roster); ok {
		d = Delivery{How: gw.Kind, To: gw.To, SessionID: gw.SessionID}
		body = "to: " + gw.To + "\n\n" + body
	} else if id := SessionOf(s.Label, roster); id != "" {
		d = Delivery{How: state.SentSession, To: id, SessionID: id}
	} else {
		return Delivery{}, ErrNoRecipient
	}
	resp, err := client.Send(ctx, &protocol.SendRequest{
		SenderSessionID: self, RecipientSessionID: d.SessionID, Body: body, Summary: summary,
		IdempotencyKey: IdempotencyKey(self, d.SessionID, body, now),
	})
	if err != nil {
		return Delivery{}, err
	}
	d.MessageID = resp.MessageID
	return d, nil
}

// ErrNoRecipient is Deliver's answer when no recipient's gateway and no
// session of the subscriber is on the roster.
var ErrNoRecipient = errors.New("digest: no gateway of the recipients and no session of the subscriber is on the roster")

// A Sender sends one message: adapterclient.Client's Send.
type Sender interface {
	Send(ctx context.Context, req *protocol.SendRequest) (*protocol.SendResponse, error)
}
