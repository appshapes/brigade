//go:build !windows

package boardcarry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/workitem"
)

var (
	alice = workitem.Author{Member: "alice@example.com", Session: "alice-web"}
	carry = workitem.Author{Member: "bob@example.com", Session: "the-carry-over"}
)

func ptr[T any](v T) *T { return &v }

// board builds a board as a team used it: three work items of which one
// was moved, commented on and closed, the lists named, and one hidden.
func board(t *testing.T) string {
	t.Helper()
	folder := filepath.Join(t.TempDir(), "boards", "earlier")
	s := workitem.Store{Folder: folder}
	read := func() *workitem.Board {
		t.Helper()
		b, err := workitem.Read(folder)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	lists := []workitem.List{{Key: "hibernating", Name: "Hibernating"}, {Key: "wanting", Name: "Wanting"}, {Key: "doing", Name: "Doing"}, {Key: "done", Name: "Done"}}
	if err := s.SetLists(read(), lists, alice); err != nil {
		t.Fatal(err)
	}
	if err := s.HideList(read(), "hibernating", true, alice); err != nil {
		t.Fatal(err)
	}
	for number, title := range map[int]string{265: "Port the tracker's skills", 273: "Keep the join secret", 274: "The admin commands"} {
		if _, err := s.AddNumbered(read(), number, workitem.Fields{Title: ptr(title), List: ptr("wanting"), Description: ptr("What " + title + " asks for.")}, alice); err != nil {
			t.Fatal(err)
		}
	}
	it := read().ByNumber(274)[0]
	if err := s.Change(it, &workitem.Fields{List: ptr("done")}, "Landed on master.", alice); err != nil {
		t.Fatal(err)
	}
	if err := s.Change(read().ByNumber(274)[0], &workitem.Fields{Closed: ptr(true)}, "", alice); err != nil {
		t.Fatal(err)
	}
	// What a sync engine leaves in the folder is no change: its marker,
	// and a conflict copy beside a work item's changes.
	if err := os.MkdirAll(filepath.Join(folder, ".stfolder"), 0o700); err != nil {
		t.Fatal(err)
	}
	conflict := filepath.Join(folder, workitem.ItemsDir, it.ID, "000000001-1794572034865-9300a9e7.sync-conflict-20261113-071916-GU35KLL.json")
	if err := os.WriteFile(conflict, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And a file with a change's name that no reader takes for one.
	if err := os.WriteFile(filepath.Join(folder, workitem.ItemsDir, it.ID, "000000009-1794572034865-0badc0de.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return folder
}

// TestCopyCarriesABoardOverAsItsReadersSeeIt: every work item arrives
// with its id, number, list, comments and history, and the lists with
// their names and what is hidden; a sync engine's own files are left
// behind; a second run writes nothing; and the second folder's files are
// private.
func TestCopyCarriesABoardOverAsItsReadersSeeIt(t *testing.T) {
	t.Parallel()
	from := board(t)
	to := filepath.Join(t.TempDir(), "boards", "now")
	src, err := workitem.Read(from)
	if err != nil {
		t.Fatal(err)
	}
	done, err := Copy(from, to)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if done.Items != 3 || done.Changes != 7 || done.Written != 7 || done.Left != 2 {
		t.Errorf("copied = %+v, want 3 work items in 7 changes, all written, and the two files that are no change left behind", done)
	}
	got, err := workitem.Read(to)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != len(src.Items) || got.Skipped != 0 || got.NextNumber() != src.NextNumber() {
		t.Fatalf("the board carried over: %d work items, %d skipped, next number %d; want %d, none and %d", len(got.Items), got.Skipped, got.NextNumber(), len(src.Items), src.NextNumber())
	}
	for i, want := range src.Items {
		it := got.Items[i]
		if it.ID != want.ID || it.Number != want.Number || it.Title != want.Title || it.Description != want.Description || it.List != want.List ||
			it.Closed != want.Closed || !it.CreatedAt.Equal(want.CreatedAt) || len(it.Comments) != len(want.Comments) || len(it.History) != len(want.History) {
			t.Errorf("work item %d as carried = %+v, want %+v", want.Number, it, want)
		}
	}
	closed := got.ByNumber(274)[0]
	if !closed.Closed || closed.List != "done" || len(closed.Comments) != 1 || closed.Comments[0].Text != "Landed on master." || closed.CreatedBy != alice.Member || len(closed.History) != 3 {
		t.Errorf("work item 274 as carried = %+v", closed)
	}
	if len(got.Lists) != 4 || !got.Lists[0].Hidden || got.Lists[1].Name != "Wanting" {
		t.Errorf("lists as carried = %+v", got.Lists)
	}
	if _, err := os.Stat(filepath.Join(to, ".stfolder")); !os.IsNotExist(err) {
		t.Errorf("the sync engine's marker was carried over: %v", err)
	}
	if copies, _ := filepath.Glob(filepath.Join(to, workitem.ItemsDir, "*", "*sync-conflict*")); len(copies) != 0 {
		t.Errorf("a conflict copy was carried over: %v", copies)
	}
	for rel := range mustChanges(t, to) {
		if info, err := os.Stat(filepath.Join(to, rel)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v, want mode 0600", rel, info, err)
		}
	}
	again, err := Copy(from, to)
	if err != nil || again.Written != 0 || again.Changes != 7 {
		t.Errorf("a second run: %+v %v, want nothing written", again, err)
	}
}

func mustChanges(t *testing.T, folder string) map[string][]byte {
	t.Helper()
	files, err := changes(folder)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestCopyWritesNothingIntoABoardWithChangesOfItsOwn: a folder that holds
// a work item the first board does not, or a change that differs, is left
// exactly as it was, and so is one that is the first folder itself, a
// relative path, or an empty board to carry over.
func TestCopyWritesNothingIntoABoardWithChangesOfItsOwn(t *testing.T) {
	t.Parallel()
	from := board(t)
	t.Run("a work item of its own", func(t *testing.T) {
		t.Parallel()
		to := filepath.Join(t.TempDir(), "now")
		s := workitem.Store{Folder: to}
		b, err := workitem.Read(to)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(b, workitem.Fields{Title: ptr("Added on the new team's board"), List: ptr("todo")}, alice); err != nil {
			t.Fatal(err)
		}
		before := mustChanges(t, to)
		if _, err := Copy(from, to); err == nil || !strings.Contains(err.Error(), "a change of its own") {
			t.Fatalf("copy: %v, want a refusal", err)
		}
		if after := mustChanges(t, to); len(after) != len(before) {
			t.Errorf("the refused copy wrote %d files", len(after)-len(before))
		}
	})
	for name, tc := range map[string]struct {
		rewrite func(data []byte) []byte
		refusal string
	}{
		"a change that differs": {
			rewrite: func(data []byte) []byte {
				return []byte(strings.Replace(string(data), alice.Member, "mallory@example.com", 1))
			},
			refusal: "a change of its own",
		},
		"a file that is no change": {
			rewrite: func([]byte) []byte { return []byte("{}\n") },
			refusal: "no change",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			to := filepath.Join(t.TempDir(), "now")
			if _, err := Copy(from, to); err != nil {
				t.Fatal(err)
			}
			for rel, data := range mustChanges(t, to) {
				rewritten := tc.rewrite(data)
				if string(rewritten) == string(data) {
					t.Fatalf("%s was not rewritten", rel)
				}
				if err := os.WriteFile(filepath.Join(to, rel), rewritten, 0o600); err != nil {
					t.Fatal(err)
				}
				break
			}
			if _, err := Copy(from, to); err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("copy: %v, want a refusal that says %q", err, tc.refusal)
			}
		})
	}
	for name, pair := range map[string][2]string{
		"the same folder":   {from, from + string(filepath.Separator)},
		"a relative path":   {from, "boards/now"},
		"nothing to carry":  {filepath.Join(t.TempDir(), "empty"), filepath.Join(t.TempDir(), "now")},
		"a relative source": {"boards/earlier", filepath.Join(t.TempDir(), "now")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if done, err := Copy(pair[0], pair[1]); err == nil {
				t.Fatalf("copy: %+v, want a refusal", done)
			}
		})
	}
	// A directory of either board that is a link: nothing is read through
	// one and nothing written, into the folder or where the link leads.
	for _, which := range []string{workitem.ItemsDir, workitem.ListsDir} {
		t.Run("the second board's "+which+" is a link", func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			to, elsewhere := filepath.Join(dir, "now"), filepath.Join(dir, "elsewhere")
			for _, d := range []string{to, elsewhere} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(elsewhere, filepath.Join(to, which)); err != nil {
				t.Fatal(err)
			}
			if done, err := Copy(from, to); err == nil {
				t.Fatalf("copy: %+v, want a refusal", done)
			}
			for _, d := range []string{to, elsewhere} {
				entries, err := os.ReadDir(d)
				if err != nil {
					t.Fatal(err)
				}
				if d == elsewhere && len(entries) != 0 || d == to && len(entries) != 1 {
					t.Errorf("after the refusal %s holds %d entries: something was written", filepath.Base(d), len(entries))
				}
			}
		})
		t.Run("the first board's "+which+" is a link", func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			linked, to := filepath.Join(dir, "earlier"), filepath.Join(dir, "now")
			if err := os.MkdirAll(linked, 0o700); err != nil {
				t.Fatal(err)
			}
			other := workitem.ItemsDir
			if which == other {
				other = workitem.ListsDir
			}
			if err := os.Symlink(filepath.Join(from, which), filepath.Join(linked, which)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(from, other), filepath.Join(linked, other)); err != nil {
				t.Fatal(err)
			}
			if done, err := Copy(linked, to); err == nil {
				t.Fatalf("copy: %+v, want a refusal", done)
			}
			if _, err := os.Stat(to); !os.IsNotExist(err) {
				t.Errorf("after the refusal the second folder exists: %v", err)
			}
		})
	}
}

// TestAddCarriesCardsUnderTheNumbersTheyHad: a card whose number the
// board has free keeps it; one whose number is taken gets the next free
// number above both sequences and says where it came from on the first
// line of its description; the comments follow in order; a description
// longer than one holds goes on as comments; and a second run writes
// nothing.
func TestAddCarriesCardsUnderTheNumbersTheyHad(t *testing.T) {
	t.Parallel()
	folder := board(t)
	long := strings.Repeat(strings.Repeat("a line of a long specification ", 4)+"\n", 90)
	cards := []Card{
		{Number: 281, Title: "The newest card", List: "Wanting", Tags: []string{"cavalry"}},
		{Number: 198, Title: "An old card", Description: "What the old card asks for.", List: "hibernating", Comments: []string{"dana, 2026-10-02:\nfirst", "erin, 2026-10-03:\nsecond"}},
		{Number: 273, Title: "A card whose number is taken", Description: "Its own description."},
		{Number: 265, Title: "Another whose number is taken", Description: long},
	}
	done, err := Add(folder, cards, "Trello card", carry)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	want := []Carried{
		{From: 198, Number: 198, Title: "An old card", Written: true, Comments: 2},
		{From: 265, Number: 282, Title: "Another whose number is taken", Written: true},
		{From: 273, Number: 283, Title: "A card whose number is taken", Written: true},
		{From: 281, Number: 281, Title: "The newest card", Written: true},
	}
	if len(done) != len(want) {
		t.Fatalf("carried = %+v", done)
	}
	for i := range want {
		// The long description's overflow is comments, however many.
		if i == 1 {
			want[i].Comments = done[i].Comments
		}
		if done[i] != want[i] {
			t.Errorf("carried[%d] = %+v, want %+v", i, done[i], want[i])
		}
	}
	b, err := workitem.Read(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 7 || len(b.Clashes()) != 0 || b.NextNumber() != 284 {
		t.Fatalf("the board: %d work items, %d clashes, next number %d; want 7, none and 284", len(b.Items), len(b.Clashes()), b.NextNumber())
	}
	old := b.ByNumber(198)[0]
	if old.List != "hibernating" || old.Description != "What the old card asks for." || old.CreatedBy != carry.Member ||
		len(old.Comments) != 2 || old.Comments[0].Text != "dana, 2026-10-02:\nfirst" || old.Comments[1].Text != "erin, 2026-10-03:\nsecond" {
		t.Errorf("card 198 as carried = %+v", old)
	}
	if newest := b.ByNumber(281)[0]; newest.List != "wanting" || len(newest.Tags) != 1 || newest.Tags[0] != "cavalry" || newest.Description != "" {
		t.Errorf("card 281 as carried = %+v", newest)
	}
	if taken := b.ByNumber(283)[0]; taken.Description != "Trello card 273\n\nIts own description." || taken.List != "wanting" || taken.History[0].What != "created in Wanting" {
		t.Errorf("card 273 as carried = %+v", taken)
	}
	if kept := b.ByNumber(273)[0]; kept.Title != "Keep the join secret" {
		t.Errorf("the board's own 273 = %+v", kept)
	}
	whole := b.ByNumber(282)[0]
	if !strings.HasPrefix(whole.Description, "Trello card 265\n\na line of") || len(whole.Comments) == 0 {
		t.Fatalf("card 265 as carried: a description of %d characters and %d comments", len(whole.Description), len(whole.Comments))
	}
	texts := []string{whole.Description}
	for _, c := range whole.Comments {
		texts = append(texts, c.Text)
	}
	if got, want := strings.Join(strings.Fields(strings.Join(texts, "\n")), " "), strings.Join(strings.Fields("Trello card 265 "+long), " "); got != want {
		t.Errorf("card 265's description and comments together are %d characters of text, want the card's %d", len(got), len(want))
	}

	again, err := Add(folder, cards, "Trello card", carry)
	if err != nil {
		t.Fatalf("a second run: %v", err)
	}
	for i, c := range again {
		if c.Written || c.Comments != 0 || c.Number != want[i].Number {
			t.Errorf("a second run: carried[%d] = %+v, want it found as %d and nothing written", i, c, want[i].Number)
		}
	}
	if b, err := workitem.Read(folder); err != nil || len(b.Items) != 7 {
		t.Errorf("after a second run: %d work items %v, want 7", len(b.Items), err)
	}
}

// TestAddWritesNoCardWhenOneIsRefused: every card is checked before any
// is written.
func TestAddWritesNoCardWhenOneIsRefused(t *testing.T) {
	t.Parallel()
	good := Card{Number: 198, Title: "A card the board would take"}
	for name, bad := range map[string]Card{
		"no title":            {Number: 199},
		"a list of no board":  {Number: 199, Title: "x", List: "Nowhere"},
		"a number of no item": {Number: 0, Title: "x"},
		"a number twice":      {Number: 198, Title: "x"},
		"a credential's shape": {Number: 199, Title: "x",
			Description: "token: " + strings.Join([]string{"ghp", strings.Repeat("A1b2", 9)}, "_")},
		"a tag too long": {Number: 199, Title: "x", Tags: []string{strings.Repeat("t", workitem.MaxTagChars+1)}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			folder := board(t)
			if done, err := Add(folder, []Card{good, bad}, "Trello card", carry); err == nil {
				t.Fatalf("add: %+v, want a refusal", done)
			}
			if b, err := workitem.Read(folder); err != nil || len(b.Items) != 3 {
				t.Errorf("after the refusal: %d work items %v, want the board's three", len(b.Items), err)
			}
		})
	}
}

// TestAddGivesTheNextNumberAboveTheBoardsToo: the number a card whose
// number is taken gets is above the board's highest when that is the
// higher of the two sequences.
func TestAddGivesTheNextNumberAboveTheBoardsToo(t *testing.T) {
	t.Parallel()
	folder := board(t)
	done, err := Add(folder, []Card{{Number: 198, Title: "A free number"}, {Number: 265, Title: "A taken number"}}, "Trello card", carry)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 || done[0].Number != 198 || done[1].Number != 275 {
		t.Errorf("carried = %+v, want 198 kept and 265 as 275, one above the board's 274", done)
	}
}

// TestAddGivesACardWhatARunBeforeDidNotGetTo: a run that wrote a card and
// only some of its comments is finished by the next: the comments it
// lacks are written, once, and none twice, for a card that kept its
// number and for one that was given another.
func TestAddGivesACardWhatARunBeforeDidNotGetTo(t *testing.T) {
	t.Parallel()
	folder := board(t)
	whole := []Card{
		{Number: 198, Title: "Kept its number", Comments: []string{"first", "second", "third"}},
		{Number: 273, Title: "Was given another", Description: "Its description.", Comments: []string{"one", "two"}},
	}
	// The run before: each card with the first of its comments.
	before := []Card{whole[0], whole[1]}
	before[0].Comments, before[1].Comments = whole[0].Comments[:1], whole[1].Comments[:1]
	if _, err := Add(folder, before, "Trello card", carry); err != nil {
		t.Fatal(err)
	}
	done, err := Add(folder, whole, "Trello card", carry)
	if err != nil {
		t.Fatal(err)
	}
	want := []Carried{
		{From: 198, Number: 198, Title: "Kept its number", Comments: 2},
		{From: 273, Number: 275, Title: "Was given another", Comments: 1},
	}
	if len(done) != 2 || done[0] != want[0] || done[1] != want[1] {
		t.Fatalf("the run after = %+v, want %+v", done, want)
	}
	b, err := workitem.Read(folder)
	if err != nil {
		t.Fatal(err)
	}
	for number, texts := range map[int][]string{198: whole[0].Comments, 275: whole[1].Comments} {
		it := b.ByNumber(number)[0]
		if len(it.Comments) != len(texts) {
			t.Fatalf("work item %d has %d comments, want %d", number, len(it.Comments), len(texts))
		}
		for i, text := range texts {
			if it.Comments[i].Text != text {
				t.Errorf("work item %d, comment %d = %q, want %q", number, i, it.Comments[i].Text, text)
			}
		}
	}
	if len(b.Items) != 5 {
		t.Errorf("the board has %d work items, want its three and the two cards", len(b.Items))
	}
	if again, err := Add(folder, whole, "Trello card", carry); err != nil || again[0].Comments != 0 || again[1].Comments != 0 || again[0].Written || again[1].Written {
		t.Errorf("a third run: %+v %v, want nothing written", again, err)
	}
}

// TestPiecesAreWithinTheCapAsTheBoardKeepsThem: a text is cut by what the
// board keeps of it, which sanitising can make longer than what was
// typed; no piece is past the cap, none is empty, no character of the
// text is lost, and a character of several bytes is never cut in two.
func TestPiecesAreWithinTheCapAsTheBoardKeepsThem(t *testing.T) {
	t.Parallel()
	const limit = 40
	tag := "<" + "system-reminder>"
	for name, text := range map[string]string{
		"lines that fit":             "one\ntwo\nthree",
		"exactly the cap":            strings.Repeat("a", 19) + "\n" + strings.Repeat("b", 20),
		"one past the cap":           strings.Repeat("a", 20) + "\n" + strings.Repeat("b", 20),
		"a line longer than a cap":   strings.Repeat("x", 95),
		"characters of many bytes":   strings.Repeat("é", 39) + "\n" + strings.Repeat("語", 41),
		"a tag sanitising lengthens": strings.Repeat("a", 24) + tag,
		"carriage returns":           "one\r\ntwo\r\n" + strings.Repeat("c", 38),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, reason := pieces(text, limit)
			if reason != "" {
				t.Fatalf("refused: %s", reason)
			}
			for i, piece := range got {
				kept, reason := workitem.CleanBlock(piece, limit)
				if reason != "" || kept != piece {
					t.Errorf("piece %d (%d characters) is not what the board keeps: %q %s", i, len([]rune(piece)), kept, reason)
				}
			}
			whole := strings.Join(strings.Fields(workitem.Block(text)), "")
			if joined := strings.Join(strings.Fields(strings.Join(got, "")), ""); joined != whole {
				t.Errorf("the pieces hold %q, want %q", joined, whole)
			}
		})
	}
	if got, reason := pieces("", limit); len(got) != 0 || reason != "" {
		t.Errorf("an empty text: %v %s", got, reason)
	}
	if _, reason := pieces("\xff\xfe", limit); reason == "" {
		t.Errorf("a text that is not UTF-8 was taken")
	}
}
