package watch

import (
	"log/slog"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/workitem"
)

// TestStopWorkWhenTheSessionIsGone is work item 346's session whose
// process died without its SessionEnd hook: the watcher's exit path writes
// the stop of its open work, with the session's end as the reason, and a
// session with no open work, or no board, gets no write.
func TestStopWorkWhenTheSessionIsGone(t *testing.T) {
	t.Parallel()
	board := t.TempDir()
	const sid = "0123456789abcdef0123456789abcdef"
	store := workitem.Store{Folder: board}
	b, err := workitem.Read(board)
	if err != nil {
		t.Fatal(err)
	}
	title, list := "First", b.Lists[0].Key
	id, err := store.Add(b, workitem.Fields{Title: &title, List: &list}, workitem.Author{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = workitem.Read(board)
	if err := store.StartWork(&workitem.SessionWork{SessionID: sid}, b.ByID(id), workitem.Author{}); err != nil {
		t.Fatal(err)
	}

	w := &watcher{
		sessionID: sid, sync: syncSetup{board: board}, state: &shared{name: "346-link"},
		deps: Deps{Clock: time.Now}, log: slog.New(slog.DiscardHandler),
	}
	w.stopWork()
	sw, err := workitem.ReadSessionWork(board, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(sw.Spans) != 1 || sw.Current() != nil || sw.Spans[0].End != workitem.EndSessionEnd {
		t.Fatalf("the watcher did not stop the work: %+v", sw.Spans)
	}

	// Nothing open: no second change.
	w.stopWork()
	if again, _ := workitem.ReadSessionWork(board, sid); len(again.Spans) != 1 || again.Spans[0].Stop != sw.Spans[0].Stop {
		t.Fatalf("a second stop changed the span: %+v", again.Spans)
	}
	// No board: nothing to do, and no panic.
	(&watcher{sessionID: sid, state: &shared{}, deps: Deps{Clock: time.Now}, log: slog.New(slog.DiscardHandler)}).stopWork()
}
