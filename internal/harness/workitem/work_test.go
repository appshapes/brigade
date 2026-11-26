package workitem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sessionWork is ReadSessionWork for a folder the test expects to be
// readable.
func sessionWork(t *testing.T, folder, id string) *SessionWork {
	t.Helper()
	w, err := ReadSessionWork(folder, id)
	if err != nil {
		t.Fatalf("read the work of %s: %v", id, err)
	}
	return w
}

func startWork(t *testing.T, s Store, sid, itemID string, who Author) {
	t.Helper()
	it := read(t, s.Folder).ByID(itemID)
	if err := s.StartWork(sessionWork(t, s.Folder, sid), it, who); err != nil {
		t.Fatalf("start %s on %s: %v", sid, itemID, err)
	}
}

func stopWork(t *testing.T, s Store, sid, end string, who Author) {
	t.Helper()
	if err := s.StopWork(sessionWork(t, s.Folder, sid), end, who); err != nil {
		t.Fatalf("stop %s: %v", sid, err)
	}
}

// TestWorkSetClearReplace is the link's life: a start opens a span, a
// second start stops it and opens another, a stop closes the open one, and
// the spans keep the times, the session and the member.
func TestWorkSetClearReplace(t *testing.T) {
	s := Store{Folder: t.TempDir(), Now: newClock().now}
	who := Author{Member: "ann@example.com", Session: "346-link"}
	a := add(t, s, "first", who)
	b := add(t, s, "second", who)
	const sid = "0123456789abcdef0123456789abcdef"

	if cur := sessionWork(t, s.Folder, sid).Current(); cur != nil {
		t.Fatalf("a session with no link has a current span: %+v", cur)
	}
	startWork(t, s, sid, a, who)
	cur := sessionWork(t, s.Folder, sid).Current()
	if cur == nil || cur.Item != a || cur.Number != 1 || cur.Session != "346-link" || cur.By != "ann@example.com" || cur.Start.IsZero() {
		t.Fatalf("after a start: %+v", cur)
	}

	startWork(t, s, sid, b, who)
	w := sessionWork(t, s.Folder, sid)
	if len(w.Spans) != 2 {
		t.Fatalf("a second start: want 2 spans, got %+v", w.Spans)
	}
	if first := w.Spans[0]; first.Open() || first.End != EndStarted || !first.Stop.Equal(w.Spans[1].Start) {
		t.Fatalf("the second start did not stop the first span at its own time: %+v", w.Spans)
	}
	if w.Current() == nil || w.Current().Item != b {
		t.Fatalf("the second start is not current: %+v", w.Spans)
	}

	// A start on the work item already current changes nothing.
	startWork(t, s, sid, b, who)
	if got := len(sessionWork(t, s.Folder, sid).Spans); got != 2 {
		t.Fatalf("a start on the current work item made a span: %d spans", got)
	}

	stopWork(t, s, sid, EndStopped, who)
	w = sessionWork(t, s.Folder, sid)
	if w.Current() != nil || w.Spans[1].End != EndStopped || w.Spans[1].Stop.IsZero() {
		t.Fatalf("after a stop: %+v", w.Spans)
	}
	// A stop with nothing open changes nothing.
	stopWork(t, s, sid, EndSessionEnd, who)
	if w = sessionWork(t, s.Folder, sid); w.Spans[1].End != EndStopped {
		t.Fatalf("a second stop changed the span: %+v", w.Spans)
	}
}

// TestWorkSessionEndWithoutStop is a session that ends without a stop:
// its SessionEnd hook writes the stop, and the span says so.
func TestWorkSessionEndWithoutStop(t *testing.T) {
	s := Store{Folder: t.TempDir(), Now: newClock().now}
	who := Author{Member: "ann@example.com"}
	a := add(t, s, "first", who)
	const sid = "session-a"
	startWork(t, s, sid, a, who)
	stopWork(t, s, sid, EndSessionEnd, who)
	w := sessionWork(t, s.Folder, sid)
	if w.Current() != nil || len(w.Spans) != 1 || w.Spans[0].End != EndSessionEnd {
		t.Fatalf("the session's end did not stop its span: %+v", w.Spans)
	}
}

// TestWorkLinksOnItemAndSessions reads every session's links: two sessions
// on one work item, each under its own directory.
func TestWorkLinksOnItemAndSessions(t *testing.T) {
	s := Store{Folder: t.TempDir(), Now: newClock().now}
	a := add(t, s, "first", Author{})
	b := add(t, s, "second", Author{})
	startWork(t, s, "session-a", a, Author{Session: "one"})
	startWork(t, s, "session-b", a, Author{Session: "two"})
	startWork(t, s, "session-b", b, Author{Session: "two"})

	l, err := ReadWork(s.Folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Sessions) != 2 || l.Skipped != 0 {
		t.Fatalf("want 2 sessions and nothing skipped, got %d and %d", len(l.Sessions), l.Skipped)
	}
	on := l.OnItem(a)
	if len(on) != 2 || on[0].SessionID != "session-a" || on[1].SessionID != "session-b" || on[1].Open() {
		t.Fatalf("the spans on the first work item: %+v", on)
	}
	if cur := l.Session("session-b").Current(); cur == nil || cur.Item != b {
		t.Fatalf("session-b's current span: %+v", cur)
	}
	if l.Session("nobody") != nil {
		t.Fatal("a session with no directory has work")
	}
}

// TestWorkReaderSkipsForeignFiles holds the reader to the board's rules: a
// change whose session id is not its directory's, a start with no work
// item, a stop with an unknown reason and a file that is no change are
// skipped, and an id that cannot name a session writes nothing.
func TestWorkReaderSkipsForeignFiles(t *testing.T) {
	s := Store{Folder: t.TempDir(), Now: newClock().now}
	a := add(t, s, "first", Author{})
	startWork(t, s, "session-a", a, Author{})
	dir := filepath.Join(s.Folder, WorkDir, SessionKey("session-a"))
	for name, body := range map[string]string{
		"000000002-0000000000001-00000001.json": `{"v":1,"seq":2,"at":"2026-11-11T10:00:00Z","work":{"session_id":"session-b","end":"stopped"}}`,
		"000000003-0000000000001-00000002.json": `{"v":1,"seq":3,"at":"2026-11-11T10:00:00Z","work":{"session_id":"session-a","number":4}}`,
		"000000004-0000000000001-00000003.json": `{"v":1,"seq":4,"at":"2026-11-11T10:00:00Z","work":{"session_id":"session-a","end":"whatever"}}`,
		"notes.txt":                             "not a change",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(s.Folder, WorkDir, "not-a-key"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := ReadWork(s.Folder)
	if err != nil {
		t.Fatal(err)
	}
	if l.Skipped != 5 {
		t.Fatalf("want 5 skipped, got %d", l.Skipped)
	}
	if cur := l.Session("session-a").Current(); cur == nil || cur.Item != a {
		t.Fatalf("a foreign file changed the span: %+v", cur)
	}
	it := read(t, s.Folder).ByID(a)
	for _, id := range []string{"", "a/b", "two words"} {
		if err := s.StartWork(&SessionWork{SessionID: id}, it, Author{}); err == nil {
			t.Fatalf("a start for the session id %q was written", id)
		}
	}
}

// TestWorkClockSetBack: a clock set back between a start and a later
// change never ends a span before it started; the order is the sequence.
func TestWorkClockSetBack(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	a := add(t, s, "first", Author{})
	b := add(t, s, "second", Author{})
	const sid = "session-a"
	startWork(t, s, sid, a, Author{})
	c.t = c.t.Add(-time.Hour)
	startWork(t, s, sid, b, Author{})
	c.t = c.t.Add(-time.Hour)
	stopWork(t, s, sid, EndStopped, Author{})
	w := sessionWork(t, s.Folder, sid)
	if len(w.Spans) != 2 {
		t.Fatalf("spans: %+v", w.Spans)
	}
	for _, sp := range w.Spans {
		if sp.Stop.Before(sp.Start) {
			t.Fatalf("a span ends before it starts: %+v", sp)
		}
	}
}

// TestWorkWriterSeesWhatItWrote: StartWork and StopWork fold their change
// into the SessionWork they were given, so a caller reports the change it
// made without reading the folder again; and a directory a teammate's
// machine filled counts against the writer's cap as against the reader's.
func TestWorkWriterSeesWhatItWrote(t *testing.T) {
	s := Store{Folder: t.TempDir(), Now: newClock().now}
	a := add(t, s, "first", Author{})
	w := sessionWork(t, s.Folder, "session-a")
	if err := s.StartWork(w, read(t, s.Folder).ByID(a), Author{}); err != nil {
		t.Fatal(err)
	}
	if w.Current() == nil || w.Current().Item != a {
		t.Fatalf("the start is not in w: %+v", w.Spans)
	}
	if err := s.StopWork(w, EndStopped, Author{}); err != nil {
		t.Fatal(err)
	}
	if w.Current() != nil || w.changes != 2 || w.seq != 2 {
		t.Fatalf("the stop is not in w: %+v changes %d seq %d", w.Spans, w.changes, w.seq)
	}

	// Another session's change in this directory counts too.
	dir := filepath.Join(s.Folder, WorkDir, SessionKey("session-a"))
	if err := os.WriteFile(filepath.Join(dir, "000000007-0000000000001-0000000a.json"),
		[]byte(`{"v":1,"seq":7,"at":"2026-11-11T10:00:00Z","work":{"session_id":"session-b","end":"stopped"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := sessionWork(t, s.Folder, "session-a"); again.changes != 3 || again.seq != 7 || len(again.Spans) != 1 {
		t.Fatalf("a foreign change is not counted: changes %d seq %d spans %+v", again.changes, again.seq, again.Spans)
	}
}

// TestWorkSessionsDirectoryIsNoSymlink: a `sessions` that is a symlink is
// a teammate's file: it is not followed by a reader, and nothing is
// written through it.
func TestWorkSessionsDirectoryIsNoSymlink(t *testing.T) {
	s := Store{Folder: t.TempDir(), Now: newClock().now}
	a := add(t, s, "first", Author{})
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(s.Folder, WorkDir)); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if _, err := ReadSessionWork(s.Folder, "session-a"); err == nil {
		t.Fatal("a symlinked sessions directory was read")
	}
	if err := s.StartWork(&SessionWork{SessionID: "session-a"}, read(t, s.Folder).ByID(a), Author{}); err == nil {
		t.Fatal("a start was written through a symlinked sessions directory")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("files were written through the symlink: %v", entries)
	}
	l, err := ReadWork(s.Folder)
	if err != nil || len(l.Sessions) != 0 || l.Skipped != 1 {
		t.Fatalf("ReadWork through a symlink: %+v %v", l, err)
	}
}
