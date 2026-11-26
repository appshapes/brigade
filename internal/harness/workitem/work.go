// work.go is the link between a session and the work item it works on
// (work item 346, ADR 0035): which session worked on which work item, from
// when to when. It is part of the board, so every teammate's machine holds
// it whatever repository its sessions are in, and nothing of it goes to
// the backend.
//
// The link is written as the rest of the board is: changes, each written
// once and never rewritten. Each session has a directory of its own,
// `sessions/<key>/`, where key is SessionKey of its Brigade session id,
// and only that session writes there. A change there carries a Work
// member: Work.Item names the work item the session starts to work on,
// which stops the one it worked on before; an empty Work.Item stops the
// work, for the reason Work.End gives. One session works on one work item
// at a time (Frank's decision of 2026-11-26).

package workitem

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// WorkDir is the directory of the sessions' links, beside ItemsDir.
const WorkDir = "sessions"

// MaxSessionIDChars caps the Brigade session id a link carries. The
// protocol keeps an id opaque; Brigade's backends give 32 hex digits.
const MaxSessionIDChars = 128

// MaxSessions caps the sessions a reader folds, as MaxItems caps the work
// items.
const MaxSessions = 20000

// The reasons a span of work ended, Work.End of a change that stops it.
// A span that a later start ended has EndStarted, which no change
// carries.
const (
	// EndStopped is `brigade work-item stop`.
	EndStopped = "stopped"
	// EndSessionEnd is the session's end, written by its SessionEnd hook.
	EndSessionEnd = "session_end"
	// EndStarted is a span the session's next start ended.
	EndStarted = "started"
)

// Work is the member of a change in a session's directory: the session's
// id, and the work item it starts on, or the reason it stops.
type Work struct {
	// SessionID is the session's Brigade session id; the directory's name
	// is SessionKey of it.
	SessionID string `json:"session_id"`
	// Item is the id of the work item the session starts to work on, and
	// Number the number that work item had when it did. Both are empty in a
	// change that stops the work.
	Item   string `json:"item,omitzero"`
	Number int    `json:"number,omitzero"`
	// End is why the work stops: EndStopped or EndSessionEnd. Empty in a
	// start.
	End string `json:"end,omitzero"`
}

// A Span is one stretch of work of one session on one work item.
type Span struct {
	// SessionID is the session's Brigade session id, Session its name and
	// By its member's label, as the change that started the span wrote
	// them: the writer's own words.
	SessionID string `json:"session_id"`
	Session   string `json:"session,omitzero"`
	By        string `json:"by,omitzero"`
	// Item is the work item's id and Number the number it had when the
	// span started; Board.ByID gives the number it has now.
	Item   string `json:"item"`
	Number int    `json:"number"`
	// Start is when the span started; Stop when it stopped, zero for a span
	// that is still open, and End why.
	Start time.Time `json:"started_at"`
	Stop  time.Time `json:"stopped_at,omitzero"`
	End   string    `json:"end,omitzero"`
}

// Open reports whether the span has not stopped.
func (s Span) Open() bool { return s.Stop.IsZero() }

// SessionWork is every span of one session, oldest first, and what its
// directory holds for a next change.
type SessionWork struct {
	SessionID string
	Spans     []Span

	// seq is the highest sequence and changes the count of every change
	// the reader took from the directory, the session's own and any
	// other: the writer's caps are the reader's, so a change it writes is
	// one a reader reads. last is the latest boundary of a span, so a
	// clock set back between two changes never ends a span before it
	// started.
	seq     int
	changes int
	last    time.Time
}

// Current is the session's open span: its last one when that has not
// stopped, else nil.
func (w *SessionWork) Current() *Span {
	if w == nil || len(w.Spans) == 0 || !w.Spans[len(w.Spans)-1].Open() {
		return nil
	}
	return &w.Spans[len(w.Spans)-1]
}

// WorkLinks is the links of every session the folder holds.
type WorkLinks struct {
	// Sessions is each session's work, by session id.
	Sessions map[string]*SessionWork
	// Skipped counts what ReadWork left out.
	Skipped int
}

// Session is one session's work, nil when it has none.
func (l *WorkLinks) Session(id string) *SessionWork {
	if l == nil {
		return nil
	}
	return l.Sessions[id]
}

// OnItem is every span on the work item with that id, by start, oldest
// first.
func (l *WorkLinks) OnItem(id string) []Span {
	var out []Span
	if l == nil {
		return out
	}
	for _, w := range l.Sessions {
		for _, s := range w.Spans {
			if s.Item == id {
				out = append(out, s)
			}
		}
	}
	slices.SortFunc(out, func(x, y Span) int {
		return cmp.Or(x.Start.Compare(y.Start), strings.Compare(x.SessionID, y.SessionID))
	})
	return out
}

// SessionKey is the name of a session's directory: the first 32 hex
// digits of the SHA-256 of its id. An id is opaque, so it never names a
// path itself.
func SessionKey(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:16])
}

// validSessionID reports whether id can be a session's id in a link.
func validSessionID(id string) bool {
	return id != "" && utf8.ValidString(id) && utf8.RuneCountInString(id) <= MaxSessionIDChars && Line(id) == id && !strings.ContainsAny(id, " /\\")
}

// ReadWork folds every session's links in folder. A folder with none is
// no error; a directory that cannot be read is.
func ReadWork(folder string) (*WorkLinks, error) {
	l := &WorkLinks{Sessions: map[string]*SessionWork{}}
	dir := filepath.Join(folder, WorkDir)
	entries, skipped, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	l.Skipped += skipped
	for i, d := range entries {
		if i >= 2*MaxSessions || len(l.Sessions) >= MaxSessions {
			l.Skipped += len(entries) - i
			break
		}
		if !d.IsDir() || !ValidID32(d.Name()) {
			l.Skipped++
			continue
		}
		w, skipped, err := readSessionWork(filepath.Join(dir, d.Name()), d.Name())
		if err != nil {
			return nil, err
		}
		l.Skipped += skipped
		if w.SessionID != "" {
			l.Sessions[w.SessionID] = w
		}
	}
	return l, nil
}

// ReadSessionWork folds the links of the one session with that id: an
// empty SessionWork when it has none.
func ReadSessionWork(folder, sessionID string) (*SessionWork, error) {
	if !validSessionID(sessionID) {
		return &SessionWork{SessionID: sessionID}, nil
	}
	// The directory of every session must be a real one, as `items` must:
	// a look at it, not a listing of it.
	dir := filepath.Join(folder, WorkDir)
	switch fi, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		return &SessionWork{SessionID: sessionID}, nil
	case err != nil:
		return nil, err
	case !fi.IsDir():
		return nil, errNotADirectory
	}
	key := SessionKey(sessionID)
	w, _, err := readSessionWork(filepath.Join(dir, key), key)
	if err != nil {
		return nil, err
	}
	if w.SessionID != sessionID {
		// Changes of another session, or none: this one has no work, and
		// the directory's counts stay the writer's.
		w.SessionID, w.Spans = sessionID, nil
	}
	return w, nil
}

// ValidID32 reports whether name can be a session's directory: 32 hex
// digits.
func ValidID32(name string) bool {
	if len(name) != 32 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil && strings.ToLower(name) == name
}

// readSessionWork reads one session's directory, named key: the changes
// whose Work names a session id whose key is that name, folded into
// spans. Its SessionID is "" when the directory holds no such change.
func readSessionWork(dir, key string) (*SessionWork, int, error) {
	changes, skipped, err := readChanges(dir, "")
	if err != nil {
		return nil, 0, err
	}
	w := &SessionWork{changes: len(changes)}
	for _, c := range changes {
		w.seq = max(w.seq, c.Seq)
		wk := c.Work
		if wk == nil || SessionKey(wk.SessionID) != key || w.SessionID != "" && wk.SessionID != w.SessionID {
			skipped++
			continue
		}
		w.SessionID = wk.SessionID
		w.apply(c)
	}
	return w, skipped, nil
}

// apply folds one change into the spans: a start stops the open span and
// opens one; a stop stops the open span. A stop with no open span, and a
// start on the work item of the open span, change nothing.
func (w *SessionWork) apply(c *Change) {
	// The fold's order is the sequence; a time before the last boundary
	// is a clock that was set back, and the boundary holds.
	at := c.At
	if at.Before(w.last) {
		at = w.last
	}
	cur := w.Current()
	if c.Work.Item == "" {
		if cur != nil {
			cur.Stop, cur.End, w.last = at, c.Work.End, at
		}
		return
	}
	if cur != nil {
		if cur.Item == c.Work.Item {
			return
		}
		cur.Stop, cur.End = at, EndStarted
	}
	w.last = at
	w.Spans = append(w.Spans, Span{
		SessionID: w.SessionID, Session: c.Session, By: c.By,
		Item: c.Work.Item, Number: c.Work.Number, Start: at,
	})
}

// sanitizeWork drops a Work member that is not usable: a session id that
// is no id, a start that names no work item, a stop with no known reason.
func (c *Change) sanitizeWork() {
	wk := c.Work
	if wk == nil {
		return
	}
	switch {
	case !validSessionID(wk.SessionID):
		c.Work = nil
	case wk.Item != "":
		if !ValidID(wk.Item) || wk.Number < 1 || wk.Number > MaxNumber {
			c.Work = nil
			return
		}
		wk.End = ""
	case wk.End != EndStopped && wk.End != EndSessionEnd:
		c.Work = nil
	default:
		wk.Number = 0
	}
}

// errNoSession is a link asked for with a session id that cannot carry
// one.
var errNoSession = errors.New("workitem: the session's id cannot carry a link to a work item")

// StartWork writes that the session with that id starts to work on it,
// which stops what it worked on before. w is the session's work as the
// folder holds it now (ReadSessionWork); on success it holds the change
// too, so its spans are what this write made, whatever another writer
// did meanwhile.
func (s Store) StartWork(w *SessionWork, it *Item, who Author) error {
	if it == nil || !validSessionID(w.SessionID) {
		return errNoSession
	}
	return s.writeWork(w, &Work{SessionID: w.SessionID, Item: it.ID, Number: it.Number}, who)
}

// StopWork writes that the session with that id stops its work, for the
// reason end gives (EndStopped or EndSessionEnd), and folds the change
// into w as StartWork does.
func (s Store) StopWork(w *SessionWork, end string, who Author) error {
	if !validSessionID(w.SessionID) {
		return errNoSession
	}
	return s.writeWork(w, &Work{SessionID: w.SessionID, End: end}, who)
}

func (s Store) writeWork(w *SessionWork, wk *Work, who Author) error {
	if w.seq >= MaxSeq || w.changes >= MaxChanges {
		return ErrFull
	}
	c := &Change{Seq: w.seq + 1, By: who.Member, Session: who.Session, Work: wk}
	if err := s.write(c, WorkDir, SessionKey(w.SessionID)); err != nil {
		return err
	}
	w.seq, w.changes = c.Seq, w.changes+1
	w.apply(c)
	return nil
}

// SessionIDOK reports whether a Brigade session id can carry a link.
func SessionIDOK(id string) bool { return validSessionID(id) }
