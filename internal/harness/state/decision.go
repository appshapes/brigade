// decision.go is the decision file of the manager digest (card 162, plan
// §5.1 and §5.2): the question a session asks its manager, written by
// `brigade decision`, sent at once through a gateway, listed by every
// digest while it is open, and closed — by the asking session or another
// of its member's — with the outcome. Like an entry it has a canonical
// copy under the private state directory and a projection in the folder,
// and a folder copy is a teammate's text to the readers here.

package state

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The caps of a decision.
const (
	// MaxOptions caps the options offered; MaxOptionChars each one;
	// MaxQuestionChars the question and MaxOutcomeChars the outcome
	// (the note's cap, in code points).
	MaxOptions       = 8
	MaxOptionChars   = 200
	MaxQuestionChars = MaxNoteChars
	MaxOutcomeChars  = MaxNoteChars
	// DecisionRetention is how long a closed decision stays in the
	// folder; an open one stays for good (ruling 2).
	DecisionRetention = 30 * 24 * time.Hour
	// decisionsDir is the canonical copies' directory under `state/`.
	decisionsDir = "decisions"
	// dateLayout is the `by` member's shape.
	dateLayout = "2006-01-02"
)

// The ways a decision's ask went out, Decision.Sent: through the Slack or
// the mail gateway, to the manager's own session when no gateway was on
// the roster, or not at all.
const (
	SentSlack   = "slack"
	SentEmail   = "email"
	SentSession = "session"
)

// A Decision is one question put to the manager.
type Decision struct {
	Version  int    `json:"version"`
	ID       string `json:"id"`
	Question string `json:"question"`
	// Options are the answers offered, Default the one taken when the
	// manager says nothing, By the date an answer is needed by
	// (YYYY-MM-DD), Card the card the question belongs to; each optional.
	Options []string `json:"options"`
	Default string   `json:"default,omitzero"`
	By      string   `json:"by,omitzero"`
	Card    int      `json:"card,omitzero"`
	// Member, SessionName, SessionID and Repository say who asked.
	Member      string `json:"member,omitzero"`
	SessionName string `json:"session_name"`
	SessionID   string `json:"session_id"`
	Repository  string `json:"repository,omitzero"`
	// Sent is how the ask reached the manager (SentSlack, SentEmail,
	// SentSession), "" when it did not and the next digest carries it;
	// SentTo the recipient it went to (a Slack handle or channel, an
	// address, a session id).
	Sent   string `json:"sent,omitzero"`
	SentTo string `json:"sent_to,omitzero"`
	// CreatedAt is the ask; ClosedAt, Outcome and ClosedBy the close, zero
	// and absent while open.
	CreatedAt time.Time `json:"created_at"`
	ClosedAt  time.Time `json:"closed_at,omitzero"`
	Outcome   string    `json:"outcome,omitzero"`
	ClosedBy  *Closer   `json:"closed_by,omitzero"`
	UpdatedAt time.Time `json:"updated_at"`
}

// A Closer is who closed a decision.
type Closer struct {
	Member      string `json:"member,omitzero"`
	SessionName string `json:"session_name"`
	SessionID   string `json:"session_id"`
}

// NewDecisionID is a fresh id, `d-<YYYYMMDD>-<6 hex>`: readable in a
// message, unique enough for a team's folder, and a path component
// CheckID accepts.
func NewDecisionID(now time.Time) (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "d-" + now.UTC().Format("20060102") + "-" + hex.EncodeToString(b[:]), nil
}

// ValidDate reports whether s is a `by` date, YYYY-MM-DD.
func ValidDate(s string) bool {
	_, err := time.Parse(dateLayout, s)
	return err == nil
}

// Open reports whether the decision awaits its answer.
func (d *Decision) Open() bool { return d.ClosedAt.IsZero() }

// Expired reports whether the decision is past its term: closed more than
// DecisionRetention ago. An open one never is.
func (d *Decision) Expired(now time.Time) bool {
	return !d.ClosedAt.IsZero() && now.Sub(d.ClosedAt) > DecisionRetention
}

// Validate checks what every decision written must carry.
func (d *Decision) Validate() error {
	if d.Version != Version {
		return errors.New("state: the decision's version is not this package's")
	}
	if err := CheckID(d.ID); err != nil {
		return err
	}
	if d.Question == "" {
		return errors.New("state: a decision needs a question")
	}
	return nil
}

// Sanitize brings a decision read from the folder within the caps and
// character rules: the question, the options and the outcome folded and
// cut, the default dropped when it is none of the options, the date
// dropped when it is not one, the card bounded, the sent way one of the
// three words or "".
func (d *Decision) Sanitize() {
	d.Question = protocol.TruncateRunes(line(d.Question), MaxQuestionChars)
	options := make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		if o = protocol.TruncateRunes(line(o), MaxOptionChars); o != "" && len(options) < MaxOptions {
			options = append(options, o)
		}
	}
	d.Options = options
	d.Default = protocol.TruncateRunes(line(d.Default), MaxOptionChars)
	if d.Default != "" && !containsString(d.Options, d.Default) {
		d.Default = ""
	}
	if d.By != "" && !ValidDate(d.By) {
		d.By = ""
	}
	if d.Card < 0 || d.Card > 999999 {
		d.Card = 0
	}
	d.Member = protocol.SanitizeLabel(line(d.Member))
	d.SessionName = protocol.SanitizeName(line(d.SessionName))
	d.Repository = protocol.SanitizeLabel(line(d.Repository))
	switch d.Sent {
	case SentSlack, SentEmail, SentSession:
	default:
		d.Sent = ""
	}
	d.SentTo = protocol.TruncateRunes(line(d.SentTo), protocol.MaxSyncPeerChars)
	d.Outcome = protocol.TruncateRunes(line(d.Outcome), MaxOutcomeChars)
	if d.ClosedBy != nil {
		d.ClosedBy.Member = protocol.SanitizeLabel(line(d.ClosedBy.Member))
		d.ClosedBy.SessionName = protocol.SanitizeName(line(d.ClosedBy.SessionName))
		if CheckID(d.ClosedBy.SessionID) != nil {
			d.ClosedBy.SessionID = ""
		}
	}
	if CheckID(d.SessionID) != nil {
		d.SessionID = ""
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Lines is the labelled shape of a decision, the same in the ask the
// gateway carries and in the digest (plan §5.2): the id and the question
// first, then who asked and when, the options, the default, the date,
// the card; for a closed one the close and its outcome. Plain text, a
// stable label at the start of each line, nothing a reader has to parse.
func (d *Decision) Lines() []string {
	lines := []string{"Decision " + d.ID + ": " + d.Question}
	who := "asked " + d.CreatedAt.UTC().Format(dateLayout)
	if d.Member != "" {
		who += " by " + d.Member
	}
	if d.SessionName != "" {
		who += " (session " + d.SessionName + ")"
	}
	if d.Repository != "" {
		who += ", repository " + d.Repository
	}
	if d.Card > 0 {
		who += ", card " + itoa(d.Card)
	}
	if d.By != "" {
		who += ", answer by " + d.By
	}
	lines = append(lines, "  "+who)
	if len(d.Options) > 0 {
		lines = append(lines, "  options: "+strings.Join(d.Options, " | "))
	}
	if d.Default != "" {
		lines = append(lines, "  default: "+d.Default)
	}
	if !d.Open() {
		closed := "closed " + d.ClosedAt.UTC().Format(dateLayout)
		if d.ClosedBy != nil && (d.ClosedBy.Member != "" || d.ClosedBy.SessionName != "") {
			closed += " by " + d.ClosedBy.Member
			if d.ClosedBy.SessionName != "" {
				closed += " (session " + d.ClosedBy.SessionName + ")"
			}
		}
		if d.Outcome != "" {
			closed += ": " + d.Outcome
		}
		lines = append(lines, "  "+closed)
	}
	return lines
}

// itoa is strconv.Itoa without the import in this file's callers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// DecisionsDir is ${stateDir}/state/decisions, the canonical copies.
func (s Store) DecisionsDir() string { return filepath.Join(s.StateDir, "state", decisionsDir) }

// CanonicalDecisionPath is the canonical copy of the decision id.
func (s Store) CanonicalDecisionPath(id string) (string, error) {
	if !filepath.IsAbs(s.StateDir) {
		return "", errors.New("state: the state directory must be absolute")
	}
	if err := CheckID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.DecisionsDir(), id+".json"), nil
}

// DecisionPath is <folder>/decisions/<id>.json, for an id CheckID accepts.
func DecisionPath(folder, id string) string {
	return filepath.Join(folder, DecisionsDir, id+".json")
}

// LoadDecision reads the canonical copy of the decision id; a missing one
// satisfies errors.Is(err, fs.ErrNotExist).
func (s Store) LoadDecision(id string) (*Decision, error) {
	path, err := s.CanonicalDecisionPath(id)
	if err != nil {
		return nil, err
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return nil, err
	}
	d, err := decodeDecision(data)
	if err != nil {
		return nil, err
	}
	if d.ID != id {
		return nil, errors.New("state: the canonical decision names another id")
	}
	return d, nil
}

// SaveDecision sanitises and validates d, writes the canonical copy and
// projects it into the folder, as Save does for an entry.
func (s Store) SaveDecision(d *Decision) error {
	d.Sanitize()
	if err := d.Validate(); err != nil {
		return err
	}
	canonical, err := s.CanonicalDecisionPath(d.ID)
	if err != nil {
		return err
	}
	data, err := marshal(d)
	if err != nil {
		return err
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(canonical)); err != nil {
		return err
	}
	if err := adapterkit.WriteAtomic(canonical, data); err != nil {
		return err
	}
	if s.Folder == "" {
		return nil
	}
	path := DecisionPath(s.Folder, d.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // G301: a checkout tree is an ordinary directory
		return err
	}
	return adapterkit.WriteAtomic(path, data)
}

// RemoveDecision deletes both copies; a missing copy is not an error.
func (s Store) RemoveDecision(id string) error {
	canonical, err := s.CanonicalDecisionPath(id)
	if err != nil {
		return err
	}
	paths := []string{canonical}
	if s.Folder != "" {
		paths = append(paths, DecisionPath(s.Folder, id))
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// ReadDecision reads one decision from the folder, sanitised — the copy
// another session of the member reads to close it. A missing one
// satisfies errors.Is(err, fs.ErrNotExist).
func ReadDecision(folder, id string) (*Decision, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	d, err := readFolderDecision(DecisionPath(folder, id))
	if err != nil {
		return nil, err
	}
	if d.ID != id {
		return nil, errors.New("state: the decision names another id than its file")
	}
	d.Sanitize()
	return d, nil
}

// ReadDecisions reads every decision in the folder, each sanitised, with
// the strictness of ReadFolder; the order is the directory's.
func ReadDecisions(folder string) []*Decision {
	dir := filepath.Join(folder, DecisionsDir)
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*Decision
	for _, e := range list {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || CheckID(id) != nil {
			continue
		}
		d, err := readFolderDecision(filepath.Join(dir, e.Name()))
		if err != nil || d.ID != id {
			continue
		}
		d.Sanitize()
		out = append(out, d)
	}
	return out
}

// readFolderDecision reads one folder file within the entry rules.
func readFolderDecision(path string) (*Decision, error) {
	data, err := readFolderFile(path)
	if err != nil {
		return nil, err
	}
	return decodeDecision(data)
}

// decodeDecision parses a decision and refuses another version; the
// options are never nil afterwards.
func decodeDecision(data []byte) (*Decision, error) {
	if len(data) > MaxEntryBytes {
		return nil, errors.New("state: the decision is larger than one can be")
	}
	var d Decision
	if err := unmarshal(data, &d); err != nil {
		return nil, errors.New("state: the decision is not a JSON document of the expected shape")
	}
	if d.Version != Version {
		return nil, errors.New("state: the decision's version is not this package's")
	}
	if d.Options == nil {
		d.Options = []string{}
	}
	return &d, nil
}
