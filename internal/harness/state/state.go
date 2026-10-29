// Package state is the session state entry of the manager digest (card
// 162, .context/plans/manager-digest.md §5.1): one JSON file per session
// that the session's own machine writes into the repository's state
// folder — the folder the team file's `sync.state` names, kept in step
// with every teammate's checkout by folder sync — carrying the facts the
// hooks know (member, session name, repository, branch, times, the
// commits made since the session started) and what the model publishes
// on purpose (its doing lines, its notes). Nothing from the transcript:
// no prompt, no answer, no tool call, no model, no token count.
//
// Every entry has two copies. The CANONICAL one lives under Brigade's
// private state directory, 0600, and is the one the writer reads back
// and adds to; every write projects it into the folder, whole-file, so
// an entry a teammate's machine deleted from the folder is healed at
// the writer's next hook, and one writer per file means the sync engine
// never produces a conflict copy. A folder entry is a teammate's text:
// the readers here cap and sanitise every string of it and skip a file
// whose name, size or shape is not an entry's.
//
// Retention (ruling 2, 2026-10-29): a live entry while it lives, an
// ended one EndedRetention after its end; an entry whose session never
// ended — its machine never came back — StaleRetention after its last
// write. The writer prunes its own canonical entries at session start
// (Store.PruneOwn) and any watcher prunes the folder (PruneFolder).
package state

import (
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// Version is the entry shape this package writes and reads.
const Version = 1

// The folder's layout (plan §5.1): the entries, the decisions, the
// producer's marker and the manager's settings.
const (
	SessionsDir  = "sessions"
	DecisionsDir = "decisions"
	DigestsDir   = "digests"
	ReportsFile  = "reports.json"
	// entriesDir is the canonical copies' directory under the state
	// directory's `state/`.
	entriesDir = "entries"
)

// The caps an entry keeps to, whoever wrote it.
const (
	// MaxEntryBytes caps a file a reader will decode.
	MaxEntryBytes = 64 << 10
	// MaxCommits, MaxDoing and MaxNotes cap the three lists: the newest
	// stay.
	MaxCommits = 100
	MaxDoing   = 50
	MaxNotes   = 20
	// MaxSubjectChars caps a commit subject; MaxDoingChars a doing line
	// (the wire's own cap, protocol.MaxDescriptionChars); MaxNoteChars a
	// note (plan §5.2: `brigade note` reads at most 1,000 bytes); the
	// others the short fields. All in code points.
	MaxSubjectChars = 200
	MaxDoingChars   = protocol.MaxDescriptionChars
	MaxNoteChars    = 1000
	MaxShortChars   = 128
)

// The retention terms (plan §5.1, ruling 2).
const (
	EndedRetention = 14 * 24 * time.Hour
	StaleRetention = 14 * 24 * time.Hour
)

// An Entry is one session's state.
type Entry struct {
	Version int `json:"version"`
	// Member is the member's label as the session registered it (the
	// account email by default); "" when the session sent none.
	Member string `json:"member,omitzero"`
	// SessionName is the session's name as the roster shows it; SessionID
	// the Brigade session id, also the file's name.
	SessionName string `json:"session_name"`
	SessionID   string `json:"session_id"`
	// Repository is the repository's name (teamfile.RepoName), Branch the
	// branch HEAD was on at the last write ("" when detached or unknown).
	Repository string `json:"repository,omitzero"`
	Branch     string `json:"branch,omitzero"`
	// Harness is the session's host (claude-code, muse, codex, grok).
	Harness string `json:"harness"`
	// StartedAt is when the entry was created; LastPromptAt the last
	// prompt the hooks saw; EndedAt and EndReason the session's end, zero
	// and "" while it lives.
	StartedAt    time.Time `json:"started_at"`
	LastPromptAt time.Time `json:"last_prompt_at,omitzero"`
	EndedAt      time.Time `json:"ended_at,omitzero"`
	EndReason    string    `json:"end_reason,omitzero"`
	// Inbound is the session's effective inbound policy (accept, hold,
	// refuse).
	Inbound string `json:"inbound"`
	// StartHead is HEAD when the entry was created and Head HEAD at the
	// last write; Commits the commits reachable from Head and not from
	// StartHead, by the session's author, newest first, at most
	// MaxCommits.
	StartHead string   `json:"start_head,omitzero"`
	Head      string   `json:"head,omitzero"`
	Commits   []Commit `json:"commits"`
	// Doing is the doing-line history (`brigade doing`, newest last, at
	// most MaxDoing); Notes the notes (`brigade note`, newest last, at
	// most MaxNotes).
	Doing []Stamped `json:"doing"`
	Notes []Stamped `json:"notes"`
	// UpdatedAt is the last write.
	UpdatedAt time.Time `json:"updated_at"`
}

// A Commit is one commit the session made: its hash, its subject, the
// card number the subject starts with (0 for none) and its time.
type Commit struct {
	Hash    string    `json:"hash"`
	Subject string    `json:"subject"`
	Card    int       `json:"card,omitzero"`
	At      time.Time `json:"at"`
}

// A Stamped is one line the model published, and when.
type Stamped struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// hexHash is a full SHA-1 or SHA-256 object name.
var hexHash = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// harnessName is a harness id's shape (claude-code, gateway-slack, …).
var harnessName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// CheckID reports whether id can name an entry file: the by-native map's
// own rule (letters, digits, dash, underscore, at most 80), so a session
// id from any adapter is a safe path component or is refused.
func CheckID(id string) error { return sessionmap.CheckNativeID(id) }

// New is a fresh entry for a session, with the two lists that are never
// nil and the version set.
func New(sessionID, sessionName string, startedAt time.Time) *Entry {
	return &Entry{
		Version: Version, SessionID: sessionID, SessionName: sessionName,
		StartedAt: startedAt, UpdatedAt: startedAt,
		Commits: []Commit{}, Doing: []Stamped{}, Notes: []Stamped{},
	}
}

// Validate checks what every entry written must carry: the version and
// a session id that can be a file name.
func (e *Entry) Validate() error {
	if e.Version != Version {
		return errors.New("state: the entry's version is not this package's")
	}
	return CheckID(e.SessionID)
}

// Expired reports whether the entry is past its retention term at now:
// an ended one EndedRetention after its end, a live one StaleRetention
// after its last write (its machine never came back to end it).
func (e *Entry) Expired(now time.Time) bool {
	if !e.EndedAt.IsZero() {
		return now.Sub(e.EndedAt) > EndedRetention
	}
	return e.UpdatedAt.IsZero() || now.Sub(e.UpdatedAt) > StaleRetention
}

// AddDoing appends a doing line at the end, keeping the newest MaxDoing.
func (e *Entry) AddDoing(text string, at time.Time) {
	e.Doing = appendCapped(e.Doing, Stamped{Text: text, At: at}, MaxDoing)
}

// AddNote appends a note at the end, keeping the newest MaxNotes.
func (e *Entry) AddNote(text string, at time.Time) {
	e.Notes = appendCapped(e.Notes, Stamped{Text: text, At: at}, MaxNotes)
}

// appendCapped appends and drops the oldest past the cap.
func appendCapped(list []Stamped, s Stamped, limit int) []Stamped {
	list = append(list, s)
	if len(list) > limit {
		list = list[len(list)-limit:]
	}
	return list
}

// Sanitize brings an entry read from the folder — a teammate's text —
// within the caps and the character rules of every field: a string that
// cannot be made to fit is dropped, a list is cut to its cap (the newest
// kept), a commit with no hash is dropped, and the two lists are never
// nil. The writer runs it too before writing, so both copies obey it.
func (e *Entry) Sanitize() {
	e.Member = protocol.SanitizeLabel(line(e.Member))
	e.SessionName = protocol.SanitizeName(line(e.SessionName))
	e.Repository = protocol.SanitizeLabel(line(e.Repository))
	e.Branch = protocol.TruncateRunes(line(e.Branch), MaxShortChars)
	e.EndReason = protocol.TruncateRunes(line(e.EndReason), MaxShortChars)
	if !harnessName.MatchString(e.Harness) {
		e.Harness = ""
	}
	switch e.Inbound {
	case protocol.InboundAccept, protocol.InboundHold, protocol.InboundRefuse:
	default:
		e.Inbound = ""
	}
	if !hexHash.MatchString(e.StartHead) {
		e.StartHead = ""
	}
	if !hexHash.MatchString(e.Head) {
		e.Head = ""
	}
	commits := make([]Commit, 0, len(e.Commits))
	for _, c := range e.Commits {
		if !hexHash.MatchString(c.Hash) {
			continue
		}
		c.Subject = protocol.TruncateRunes(line(c.Subject), MaxSubjectChars)
		if c.Card < 0 || c.Card > 999999 {
			c.Card = 0
		}
		commits = append(commits, c)
		if len(commits) == MaxCommits {
			break
		}
	}
	e.Commits = commits
	e.Doing = sanitizeStamped(e.Doing, MaxDoing, MaxDoingChars)
	e.Notes = sanitizeStamped(e.Notes, MaxNotes, MaxNoteChars)
}

// sanitizeStamped keeps the newest limit lines, each folded onto one
// line and cut to chars; an empty one is dropped.
func sanitizeStamped(list []Stamped, limit, chars int) []Stamped {
	out := make([]Stamped, 0, len(list))
	for _, s := range list {
		if s.Text = protocol.TruncateRunes(line(s.Text), chars); s.Text != "" {
			out = append(out, s)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// line folds s onto one sanitised line: protocol.Sanitize (repair, NFC,
// controls and the frame's tags), then whitespace runs to one space.
func line(s string) string {
	return strings.Join(strings.Fields(protocol.Sanitize(s)), " ")
}

// A Store is where one session's entries live: the private state
// directory for the canonical copies, the state folder for the
// projections. StateDir must be absolute; Folder is "" for a session
// whose project shares no state, and then Save writes the canonical copy
// alone.
type Store struct {
	StateDir string
	Folder   string
}

// CanonicalDir is ${stateDir}/state/entries.
func (s Store) CanonicalDir() string { return filepath.Join(s.StateDir, "state", entriesDir) }

// CanonicalPath is the canonical copy of the entry for id.
func (s Store) CanonicalPath(id string) (string, error) {
	if !filepath.IsAbs(s.StateDir) {
		return "", errors.New("state: the state directory must be absolute")
	}
	if err := CheckID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.CanonicalDir(), id+".json"), nil
}

// FolderPath is the projection of the entry for id in the folder, or ""
// with no error when the Store has no folder.
func (s Store) FolderPath(id string) (string, error) {
	if s.Folder == "" {
		return "", nil
	}
	if err := CheckID(id); err != nil {
		return "", err
	}
	return EntryPath(s.Folder, id), nil
}

// EntryPath is <folder>/sessions/<id>.json, for an id CheckID accepts.
func EntryPath(folder, id string) string {
	return filepath.Join(folder, SessionsDir, id+".json")
}

// Load reads the canonical entry for id through the strict private-file
// reader. A missing entry satisfies errors.Is(err, fs.ErrNotExist).
func (s Store) Load(id string) (*Entry, error) {
	path, err := s.CanonicalPath(id)
	if err != nil {
		return nil, err
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return nil, err
	}
	e, err := decode(data)
	if err != nil {
		return nil, err
	}
	if e.SessionID != id {
		return nil, errors.New("state: the canonical entry names another session")
	}
	return e, nil
}

// Save sanitises and validates e, writes the canonical copy (0600 in the
// private directory chain) and projects it into the folder. A folder
// that cannot be written is the error; the canonical copy stands, and
// the next Save projects it again.
func (s Store) Save(e *Entry) error {
	e.Sanitize()
	if err := e.Validate(); err != nil {
		return err
	}
	canonical, err := s.CanonicalPath(e.SessionID)
	if err != nil {
		return err
	}
	data, err := Marshal(e)
	if err != nil {
		return err
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(canonical)); err != nil {
		return err
	}
	if err := adapterkit.WriteAtomic(canonical, data); err != nil {
		return err
	}
	return s.project(e.SessionID, data)
}

// Project writes the canonical entry for id into the folder again, from
// the canonical copy: the heal of a folder entry a teammate's machine
// deleted. Nothing when the Store has no folder or no canonical copy.
func (s Store) Project(id string) error {
	path, err := s.CanonicalPath(id)
	if err != nil {
		return err
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		return err
	}
	return s.project(id, data)
}

// project writes data as the folder entry for id, creating the sessions
// directory; the folder itself is the sync engine's (and the hook's) to
// create, and an ordinary checkout directory.
func (s Store) project(id string, data []byte) error {
	path, err := s.FolderPath(id)
	if err != nil || path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // G301: a checkout tree is an ordinary directory
		return err
	}
	return adapterkit.WriteAtomic(path, data)
}

// Remove deletes both copies of the entry for id; a missing copy is not
// an error.
func (s Store) Remove(id string) error {
	canonical, err := s.CanonicalPath(id)
	if err != nil {
		return err
	}
	projected, err := s.FolderPath(id)
	if err != nil {
		return err
	}
	for _, p := range []string{canonical, projected} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// PruneOwn removes every canonical entry and decision past its term at
// now, with its projection, and reports how many. Best effort: a file
// that cannot be read or removed is left for the next time.
func (s Store) PruneOwn(now time.Time) int {
	removed := 0
	for _, id := range idsIn(s.CanonicalDir()) {
		e, err := s.Load(id)
		if err != nil || !e.Expired(now) {
			continue
		}
		if s.Remove(id) == nil {
			removed++
		}
	}
	for _, id := range idsIn(s.DecisionsDir()) {
		d, err := s.LoadDecision(id)
		if err != nil || !d.Expired(now) {
			continue
		}
		if s.RemoveDecision(id) == nil {
			removed++
		}
	}
	return removed
}

// idsIn lists the ids of the `<id>.json` files in dir that CheckID
// accepts, in the directory's order; none for a directory that is not
// there.
func idsIn(dir string) []string {
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, d := range list {
		id, ok := strings.CutSuffix(d.Name(), ".json")
		if !ok || d.IsDir() || CheckID(id) != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// ReadFolder reads every entry in the folder's sessions directory, each
// sanitised: a file whose name is not an id, whose size is past
// MaxEntryBytes, whose content is not an entry or names another session
// than its file is skipped. The order is the directory's (by name).
func ReadFolder(folder string) []*Entry {
	dir := filepath.Join(folder, SessionsDir)
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*Entry
	for _, d := range list {
		id, ok := strings.CutSuffix(d.Name(), ".json")
		if !ok || d.IsDir() || CheckID(id) != nil {
			continue
		}
		e, err := readFolderEntry(filepath.Join(dir, d.Name()))
		if err != nil || e.SessionID != id {
			continue
		}
		e.Sanitize()
		out = append(out, e)
	}
	return out
}

// PruneFolder removes every folder entry and decision past its term at
// now and reports how many: a teammate's machine that never came back to
// end its session, or to prune its own, is covered by whichever watcher
// runs.
func PruneFolder(folder string, now time.Time) int {
	removed := 0
	for _, e := range ReadFolder(folder) {
		if !e.Expired(now) {
			continue
		}
		if err := os.Remove(EntryPath(folder, e.SessionID)); err == nil {
			removed++
		}
	}
	for _, d := range ReadDecisions(folder) {
		if !d.Expired(now) {
			continue
		}
		if err := os.Remove(DecisionPath(folder, d.ID)); err == nil {
			removed++
		}
	}
	return removed
}

// readFolderEntry reads one folder file as an entry.
func readFolderEntry(path string) (*Entry, error) {
	data, err := ReadFolderFile(path)
	if err != nil {
		return nil, err
	}
	return decode(data)
}

// ReadFolderFile reads one file of the folder — a teammate's — within
// the rules every folder read keeps: a regular file, no symlink followed,
// at most MaxEntryBytes. The digest's marker is read through it too.
func ReadFolderFile(path string) ([]byte, error) {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxEntryBytes {
		return nil, errors.New("state: not a state file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxEntryBytes+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Marshal and Unmarshal are the one JSON spelling of every file of the
// folder, the digest's marker included.
func Marshal(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Unmarshal decodes what Marshal wrote.
func Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// decode parses an entry and refuses another version or a size past the
// cap; the lists are never nil afterwards.
func decode(data []byte) (*Entry, error) {
	if len(data) > MaxEntryBytes {
		return nil, errors.New("state: the entry is larger than an entry can be")
	}
	var e Entry
	if err := Unmarshal(data, &e); err != nil {
		return nil, errors.New("state: the entry is not a JSON document of the expected shape")
	}
	if e.Version != Version {
		return nil, errors.New("state: the entry's version is not this package's")
	}
	if e.Commits == nil {
		e.Commits = []Commit{}
	}
	if e.Doing == nil {
		e.Doing = []Stamped{}
	}
	if e.Notes == nil {
		e.Notes = []Stamped{}
	}
	return &e, nil
}
