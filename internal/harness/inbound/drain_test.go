package inbound

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// appendRecord appends one sink record, as the watcher writes it.
func appendRecord(t *testing.T, path, id, frame string) {
	t.Helper()
	data, err := json.Marshal(SinkRecord{TS: time.Unix(1, 0).UTC(), Frame: frame, MessageID: id})
	if err != nil {
		t.Fatal(err)
	}
	appendRaw(t, path, string(data)+"\n")
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func drain(t *testing.T, stateDir, session string) string {
	t.Helper()
	got, err := DrainNew(stateDir, session)
	if err != nil {
		t.Fatalf("DrainNew: %v", err)
	}
	return got
}

// TestDrainHandsEachRecordOverOnce: what is new is handed over once, a
// partial last line waits until it is whole, and no sink is nothing.
func TestDrainHandsEachRecordOverOnce(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s1"
	if got := drain(t, state, session); got != "" {
		t.Fatalf("no sink: %q", got)
	}
	sink := NewSinkPath(state, session, time.Unix(100, 0))
	appendRecord(t, sink, "m1", "first")
	appendRecord(t, sink, "m2", "second")
	appendRaw(t, sink, `{"ts":"1970-01-01T00:00:01Z","frame":"thi`)
	if !HasNew(state, session) {
		t.Fatal("HasNew is false with two records waiting")
	}
	got := drain(t, state, session)
	if !strings.Contains(got, "team message m1 follows") || !strings.Contains(got, "second") || strings.Contains(got, "thi") {
		t.Fatalf("first drain = %q", got)
	}
	if strings.Index(got, "m1") > strings.Index(got, "m2") {
		t.Fatalf("records out of order: %q", got)
	}
	if HasNew(state, session) {
		t.Fatal("HasNew is true with only a partial line left")
	}
	if got := drain(t, state, session); got != "" {
		t.Fatalf("a second drain hands over again: %q", got)
	}
	appendRaw(t, sink, `rd","message_id":"m3","sender_session_id":""}`+"\n")
	if got := drain(t, state, session); !strings.Contains(got, "team message m3 follows") || strings.Contains(got, "m1") {
		t.Fatalf("the completed line = %q", got)
	}
}

// TestDrainFinishesTheOldSinkBeforeTheNew: a replaced watcher's sink is
// read to its end, then the new one from its beginning.
func TestDrainFinishesTheOldSinkBeforeTheNew(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s2"
	old := NewSinkPath(state, session, time.Unix(100, 0))
	appendRecord(t, old, "m1", "one")
	_ = drain(t, state, session)
	appendRecord(t, old, "m2", "two")
	newer := NewSinkPath(state, session, time.Unix(200, 0))
	appendRecord(t, newer, "m3", "three")
	got := drain(t, state, session)
	if strings.Contains(got, "m1") || !strings.Contains(got, "m2") || !strings.Contains(got, "m3") {
		t.Fatalf("drain across sinks = %q", got)
	}
	if strings.Index(got, "m2") > strings.Index(got, "m3") {
		t.Fatalf("the new sink came first: %q", got)
	}
	if got := drain(t, state, session); got != "" {
		t.Fatalf("again: %q", got)
	}
}

// TestDrainStopsAtItsCap: more than one drain may carry is handed over in
// turns, whole records only, nothing twice and nothing lost.
func TestDrainStopsAtItsCap(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s3"
	sink := NewSinkPath(state, session, time.Unix(100, 0))
	body := strings.Repeat("x", 40<<10)
	const n = 12
	for i := range n {
		appendRecord(t, sink, "m"+string(rune('a'+i)), body)
	}
	seen := 0
	for range n + 2 {
		got := drain(t, state, session)
		seen += strings.Count(got, "follows (")
		if len(got) > DrainMaxBytes+(64<<10) {
			t.Fatalf("one drain carried %d bytes", len(got))
		}
	}
	if seen != n {
		t.Fatalf("handed over %d records, want %d", seen, n)
	}
}

// TestDrainCursorNamesOnlyASink: a cursor that names another file is no
// cursor, so the sink is read from its beginning.
func TestDrainCursorNamesOnlyASink(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s4"
	sink := NewSinkPath(state, session, time.Unix(100, 0))
	appendRecord(t, sink, "m1", "one")
	cursor := filepath.Join(SinkDir(state, session), drainCursorName)
	if err := os.WriteFile(cursor, []byte(`{"sink":"../../secret","offset":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := drain(t, state, session); !strings.Contains(got, "m1") {
		t.Fatalf("drain with a foreign cursor = %q", got)
	}
	info, err := os.Stat(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !adapterkit.IsPrivate(info) {
		t.Fatal("the cursor is not private")
	}
}

// TestDrainReadsEverySinkBetween: a cursor in the oldest sink, and two
// newer watchers' sinks written before the next drain: all three are read,
// oldest first, and none is skipped (review of work item 532). With no
// cursor at all, every sink is read.
func TestDrainReadsEverySinkBetween(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s5"
	a := NewSinkPath(state, session, time.Unix(100, 0))
	appendRecord(t, a, "m1", "one")
	if HasNew(state, session) != true {
		t.Fatal("HasNew with no cursor and one record")
	}
	_ = drain(t, state, session)
	appendRecord(t, a, "m2", "two")
	appendRecord(t, NewSinkPath(state, session, time.Unix(200, 0)), "m3", "three")
	appendRecord(t, NewSinkPath(state, session, time.Unix(300, 0)), "m4", "four")
	got := drain(t, state, session)
	i2, i3, i4 := strings.Index(got, "m2"), strings.Index(got, "m3"), strings.Index(got, "m4")
	if i2 < 0 || i3 < 0 || i4 < 0 || i2 > i3 || i3 > i4 || strings.Contains(got, "m1 ") {
		t.Fatalf("drain over three sinks = %q", got)
	}

	fresh, other := t.TempDir(), "s6"
	appendRecord(t, NewSinkPath(fresh, other, time.Unix(100, 0)), "n1", "one")
	appendRecord(t, NewSinkPath(fresh, other, time.Unix(200, 0)), "n2", "two")
	if got := drain(t, fresh, other); !strings.Contains(got, "n1") || !strings.Contains(got, "n2") {
		t.Fatalf("no cursor: %q", got)
	}
}

// TestABatchMovesTheCursorOnlyWhenCommitted: a released batch is handed
// over again, a committed one is not, and a second drain waits for the
// first's lock rather than read the same batch.
func TestABatchMovesTheCursorOnlyWhenCommitted(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s7"
	appendRecord(t, NewSinkPath(state, session, time.Unix(100, 0)), "m1", "one")
	b, err := TakeNew(state, session)
	if err != nil || !strings.Contains(b.Text, "m1") {
		t.Fatalf("take: %v %q", err, b.Text)
	}
	b.Release()
	b, err = TakeNew(state, session)
	if err != nil || !strings.Contains(b.Text, "m1") {
		t.Fatalf("after a release the batch is not there again: %v %q", err, b.Text)
	}
	done := make(chan string)
	go func() {
		second, err := TakeNew(state, session)
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- second.Text
		second.Release()
	}()
	select {
	case got := <-done:
		t.Fatalf("a second drain read while the first held the lock: %q", got)
	case <-time.After(200 * time.Millisecond):
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != "" {
		t.Fatalf("after the commit the second drain got %q", got)
	}
}

// TestDrainReadsAnOlderSinkAgain: each sink keeps its own offset, so a
// record that lands in an older sink after a newer sink was drained (the
// old watcher still writing, a line half written at the last drain, or a
// clock set back) is handed over in its turn (reviews of work item 532).
func TestDrainReadsAnOlderSinkAgain(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s8"
	old := NewSinkPath(state, session, time.Unix(100, 0))
	appendRecord(t, old, "m1", "one")
	appendRaw(t, old, `{"ts":"1970-01-01T00:00:01Z","frame":"hal`)
	newer := NewSinkPath(state, session, time.Unix(200, 0))
	appendRecord(t, newer, "m2", "two")
	got := drain(t, state, session)
	if !strings.Contains(got, "m1") || !strings.Contains(got, "m2") || strings.Contains(got, "hal") {
		t.Fatalf("first drain = %q", got)
	}
	appendRaw(t, old, `f","message_id":"m3","sender_session_id":""}`+"\n")
	appendRecord(t, old, "m4", "late")
	if !HasNew(state, session) {
		t.Fatal("HasNew misses the older sink's new records")
	}
	got = drain(t, state, session)
	if !strings.Contains(got, "m3") || !strings.Contains(got, "m4") || strings.Contains(got, "m1") || strings.Contains(got, "m2") {
		t.Fatalf("the older sink's late records = %q", got)
	}
	// A sink whose name sorts before every other, written after them.
	appendRecord(t, NewSinkPath(state, session, time.Unix(50, 0)), "m5", "back in time")
	if got := drain(t, state, session); !strings.Contains(got, "m5") {
		t.Fatalf("a sink named before the others = %q", got)
	}
}

// TestDrainPassesOverAnOverlongLine: a line over maxRecordBytes is no
// record; the drain moves past it, so the record after it comes, and a
// batch with no text but a moved cursor says so.
func TestDrainPassesOverAnOverlongLine(t *testing.T) {
	t.Parallel()
	state, session := t.TempDir(), "s9"
	sink := NewSinkPath(state, session, time.Unix(100, 0))
	appendRaw(t, sink, strings.Repeat("x", maxRecordBytes+DrainMaxBytes+10)+"\n")
	b, err := TakeNew(state, session)
	if err != nil {
		t.Fatal(err)
	}
	if b.Text != "" || !b.Moved() {
		t.Fatalf("an overlong line alone: text %q, moved %v", b.Text, b.Moved())
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	if HasNew(state, session) {
		t.Fatal("HasNew after the overlong line was passed over")
	}
	appendRecord(t, sink, "m1", "after the long line")
	if got := drain(t, state, session); !strings.Contains(got, "after the long line") {
		t.Fatalf("the record after = %q", got)
	}
}

// TestDrainHandsNothingOverWhenTheCursorCannotBeWritten: with the sink's
// directory read only (a full disk is the likely form), the drain is an
// error and hands nothing over, rather than the same message at every call
// (the owner's review of work item 532).
func TestDrainHandsNothingOverWhenTheCursorCannotBeWritten(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a read-only directory is a POSIX mode, and root writes through it")
	}
	state, session := t.TempDir(), "s10"
	appendRecord(t, NewSinkPath(state, session, time.Unix(100, 0)), "m1", "one")
	dir := SinkDir(state, session)
	lock, err := os.Create(filepath.Join(dir, drainLockName))
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // a directory: read and search, no write, the case under test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // a directory's own mode back, so TempDir can remove it
	for range 3 {
		if b, err := TakeNew(state, session); err == nil {
			b.Release()
			t.Fatalf("a drain with a cursor it cannot write handed over %q", b.Text)
		}
	}
}
