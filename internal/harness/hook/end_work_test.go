//go:build !windows

package hook

import (
	"testing"

	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestSessionEndStopsTheWorkItem is work item 346's session that ends
// without a `brigade work-item stop`: the SessionEnd hook stops its span,
// with the session's end as the reason. A `clear` or a `resume` goes on
// with the same session, so its span stays open.
func TestSessionEndStopsTheWorkItem(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		open   bool
	}{
		{"other", false},
		{"prompt_input_exit", false},
		{"clear", true},
		{"resume", true},
	} {
		t.Run("reason="+tc.reason, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			registered(t, f, map[string][]fakeadapter.Response{"session close": {okResp(closeDoc())}})
			m := f.mustMap()
			m.SyncAdapter, m.SyncRoot, m.BoardFolder = "syncthing", t.TempDir(), t.TempDir()
			if err := f.store().WriteByPID(m); err != nil {
				t.Fatal(err)
			}
			store := workitem.Store{Folder: m.BoardFolder}
			board, err := workitem.Read(store.Folder)
			if err != nil {
				t.Fatal(err)
			}
			title, list := "First", board.Lists[0].Key
			id, err := store.Add(board, workitem.Fields{Title: &title, List: &list}, workitem.Author{})
			if err != nil {
				t.Fatal(err)
			}
			board, _ = workitem.Read(store.Folder)
			if err := store.StartWork(&workitem.SessionWork{SessionID: m.BrigadeSessionID}, board.ByID(id), workitem.Author{}); err != nil {
				t.Fatal(err)
			}

			if exit, out, errOut := f.run(SubSessionEnd, f.endDoc(tc.reason)); exit != 0 || out != "" {
				t.Fatalf("exit %d out %q err %q", exit, out, errOut)
			}
			w, err := workitem.ReadSessionWork(store.Folder, m.BrigadeSessionID)
			if err != nil {
				t.Fatal(err)
			}
			if len(w.Spans) != 1 {
				t.Fatalf("spans: %+v", w.Spans)
			}
			if got := w.Spans[0].Open(); got != tc.open {
				t.Fatalf("reason %q: span open = %v, want %v", tc.reason, got, tc.open)
			}
			if !tc.open && w.Spans[0].End != workitem.EndSessionEnd {
				t.Fatalf("reason %q: span ended %q, want %q", tc.reason, w.Spans[0].End, workitem.EndSessionEnd)
			}
		})
	}
}
