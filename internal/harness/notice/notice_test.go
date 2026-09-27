package notice

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

var at = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func texts(ns []Notice) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Text)
	}
	return out
}

func same(got []string, want ...string) bool {
	return slices.Equal(got, want)
}

func put(t *testing.T, path, topic, text string) {
	t.Helper()
	if err := Put(path, topic, text, at); err != nil {
		t.Fatalf("put %s: %v", topic, err)
	}
}

// TestTopicsStandSideBySide is the defect the package exists for: a line
// of one topic no longer erases a line of another. Within a topic the
// newer line replaces the older and goes last.
func TestTopicsStandSideBySide(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 4242)
	if want := filepath.Join(filepath.Dir(filepath.Dir(path)), "state", "4242.notice"); path != want {
		t.Fatalf("path %q", path)
	}
	put(t, path, TopicWatcher, "not receiving")
	put(t, path, TopicSync, "2 folders, 1 of 3 peers")
	got, err := Read(path)
	if err != nil || !same(texts(got), "not receiving", "2 folders, 1 of 3 peers") {
		t.Fatalf("after two topics: %v %v", texts(got), err)
	}
	put(t, path, TopicWatcher, "receiving again")
	got, _ = Read(path)
	if !same(texts(got), "2 folders, 1 of 3 peers", "receiving again") {
		t.Fatalf("after the watcher's second line: %v", texts(got))
	}
	if got[1].Topic != TopicWatcher || !got[1].At.Equal(at) {
		t.Errorf("entry = %+v", got[1])
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v %v", info, err)
	}
}

// TestTheFileIsBounded: MaxNotices lines, and the oldest goes first.
func TestTheFileIsBounded(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 1)
	for i := range MaxNotices + 2 {
		put(t, path, "topic-"+strconv.Itoa(i), "line "+strconv.Itoa(i))
	}
	got, _ := Read(path)
	if !same(texts(got), "line 2", "line 3", "line 4", "line 5", "line 6") {
		t.Errorf("lines = %v", texts(got))
	}
}

// TestPutFoldsAndRefuses: a text is one line whatever it carried, and a
// notice with no topic or no text is not written.
func TestPutFoldsAndRefuses(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 1)
	put(t, path, TopicWatcher, "  one\nline\tof   text \n")
	if got, _ := Read(path); !same(texts(got), "one line of text") {
		t.Errorf("lines = %v", texts(got))
	}
	for name, err := range map[string]error{
		"no topic": Put(path, "", "text", at),
		"no text":  Put(path, TopicSync, " \n", at),
	} {
		if err == nil {
			t.Errorf("%s was written", name)
		}
	}
	if got, _ := Read(path); len(got) != 1 {
		t.Errorf("a refused notice changed the file: %v", texts(got))
	}
}

// TestTakeRemovesWhatItReturns: the hook gets every line once, the file
// and the name it was held under are gone, and a second take has nothing.
func TestTakeRemovesWhatItReturns(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 1)
	if got, err := Take(path); err != nil || got != nil {
		t.Fatalf("take of a missing file: %v %v", got, err)
	}
	put(t, path, TopicSync, "sync line")
	put(t, path, TopicWatcher, "watcher line")
	got, err := Take(path)
	if err != nil || !same(texts(got), "sync line", "watcher line") {
		t.Fatalf("take: %v %v", texts(got), err)
	}
	for _, p := range []string{path, path + takenSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there: %v", filepath.Base(p), err)
		}
	}
	if got, err := Take(path); err != nil || got != nil {
		t.Errorf("second take: %v %v", got, err)
	}
	// A line written after the take waits for the next one.
	put(t, path, TopicWatcher, "written later")
	if got, _ := Take(path); !same(texts(got), "written later") {
		t.Errorf("after a later put: %v", texts(got))
	}
}

// TestAnOlderWatchersFile: one line of plain text, of which the first line
// is the notice. Put keeps it beside the new line.
func TestAnOlderWatchersFile(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := "Brigade: watcher stopped: unauthorized; run `brigade team join` again\nsecond line never shown\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || !same(texts(got), "Brigade: watcher stopped: unauthorized; run `brigade team join` again") || got[0].Topic != "" {
		t.Fatalf("legacy read: %+v %v", got, err)
	}
	put(t, path, TopicSync, "sync line")
	if got, _ := Take(path); !same(texts(got), "Brigade: watcher stopped: unauthorized; run `brigade team join` again", "sync line") {
		t.Errorf("after a put: %v", texts(got))
	}
	for name, content := range map[string]string{"empty": "", "blank": "\n\n", "another version": `{"version":2,"notices":[{"topic":"sync","text":"x"}]}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Take(path)
		if name == "another version" {
			// Not this version's document: read as an older watcher's line.
			if err != nil || len(got) != 1 {
				t.Errorf("%s: %v %v", name, texts(got), err)
			}
			continue
		}
		if err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v", name, texts(got), err)
		}
	}
}

// TestAFileThatIsNotPrivate: removed unread by Take, replaced by Put.
func TestAFileThatIsNotPrivate(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func() {
		t.Helper()
		if err := os.WriteFile(path, []byte("planted line\n"), 0o644); err != nil { //nolint:gosec // G306: a file that is NOT private is the case
			t.Fatal(err)
		}
	}
	write()
	if got, err := Take(path); err == nil || got != nil {
		t.Errorf("take read a file that is not private: %v", texts(got))
	}
	for _, p := range []string{path, path + takenSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was left behind: %v", filepath.Base(p), err)
		}
	}
	write()
	put(t, path, TopicWatcher, "the watcher's own")
	got, err := Take(path)
	if err != nil || !same(texts(got), "the watcher's own") {
		t.Errorf("after a put over it: %v %v", texts(got), err)
	}
}

// TestConcurrentPutsLoseNothing: the watcher's two writers at once, each
// on its own topic, both lines present at the end.
func TestConcurrentPutsLoseNothing(t *testing.T) {
	t.Parallel()
	path := Path(t.TempDir(), 1)
	var wg sync.WaitGroup
	for _, topic := range []string{TopicWatcher, TopicSync} {
		wg.Go(func() {
			for i := range 50 {
				if err := Put(path, topic, topic+" "+strconv.Itoa(i), at); err != nil {
					t.Errorf("put: %v", err)
				}
			}
		})
	}
	wg.Wait()
	got, err := Read(path)
	if err != nil || len(got) != 2 {
		t.Fatalf("lines = %v %v", texts(got), err)
	}
	for _, n := range got {
		if n.Text != n.Topic+" 49" {
			t.Errorf("%s ends with %q, want its last line", n.Topic, n.Text)
		}
	}
}
