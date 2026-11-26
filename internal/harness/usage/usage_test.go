package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestParseStatus reads the example document of Claude Code's status line
// page (https://code.claude.com/docs/en/statusline.md): the cost, the
// window, the context in use and the three windows of rate_limits.
func TestParseStatus(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "statusline.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, err := ParseStatus(data, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "abc123" || *s.CostUSD != 0.01234 || *s.ContextWindow != 200000 || *s.ContextUsed != 15500 || !s.HasLimits || !s.At.Equal(now) {
		t.Fatalf("status: %+v", s)
	}
	if len(s.Limits) != 3 || s.Limits[0].Name != LimitShort || s.Limits[0].UsedPercent != 23.5 || s.Limits[0].WindowMinutes != 300 ||
		!s.Limits[0].ResetsAt.Equal(time.Unix(1738425600, 0)) || s.Limits[1].Name != LimitWeekly || s.Limits[2].Name != "spend" {
		t.Fatalf("limits: %+v", s.Limits)
	}

	// A plan with no rate_limits, a context after /compact, a member of
	// the wrong type: each left out, none an error.
	s, err = ParseStatus([]byte(`{"session_id":"x","cost":{"total_cost_usd":"a lot"},"context_window":{"context_window_size":-1,"current_usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`), now)
	if err != nil || s.CostUSD != nil || s.ContextWindow != nil || s.ContextUsed != nil || s.HasLimits {
		t.Fatalf("partial: %+v %v", s, err)
	}
	for _, bad := range []string{`{}`, `not json`, `{"session_id":""}`} {
		if _, err := ParseStatus([]byte(bad), now); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
}

// TestWriteReadList: a record is written whole and read back by its
// session's id; a status likewise by Claude Code's own id; List takes
// only records under their own key, the latest read first.
func TestWriteReadList(t *testing.T) {
	dir := t.TempDir()
	a := &Record{SessionID: "a", Harness: "codex", ReadAt: time.Unix(100, 0).UTC(), Model: Of("gpt"), Cost: Missing[Cost](NotGiven)}
	b := &Record{SessionID: "b", Harness: "grok", ReadAt: time.Unix(200, 0).UTC(), Cost: Of(Cost{USD: 1.5, Kind: KindUnknown})}
	for _, r := range []*Record{a, b} {
		if err := Write(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(dir, "a")
	if err != nil || got == nil || got.Model.Or("") != "gpt" || got.Cost.Unavailable != NotGiven || got.Cost.Known() {
		t.Fatalf("read a: %+v %v", got, err)
	}
	if none, err := Read(dir, "nobody"); none != nil || err != nil {
		t.Fatalf("read nobody: %+v %v", none, err)
	}
	if fi, err := os.Stat(Path(dir, "a")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi.Mode(), err)
	}
	// A copy under another name is not that session's.
	data, _ := os.ReadFile(Path(dir, "a"))
	if err := os.WriteFile(filepath.Join(dir, Dir, Key("c")+".json"), data, 0o600); err != nil { //nolint:gosec // G703: the test's own temporary directory
		t.Fatal(err)
	}
	list, err := List(dir)
	if err != nil || len(list) != 2 || list[0].SessionID != "b" || list[1].SessionID != "a" {
		t.Fatalf("list: %+v %v", list, err)
	}

	s := &Status{Version: Version, SessionID: "native", At: time.Unix(300, 0).UTC(), Limits: []Limit{}}
	if err := WriteStatus(dir, s); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadStatus(dir, "native"); err != nil || got == nil || got.SessionID != "native" {
		t.Fatalf("status: %+v %v", got, err)
	}
	if got, _ := ReadStatus(dir, "other"); got != nil {
		t.Fatalf("another session's status: %+v", got)
	}
}

// TestReadFollowsNoSymlink: a record that is a symlink is not read.
func TestReadFollowsNoSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, &Record{SessionID: "a", Harness: "codex"}); err != nil {
		t.Fatal(err)
	}
	link := Path(dir, "b")
	if err := os.Symlink(Path(dir, "a"), link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if r, err := Read(dir, "b"); r != nil || err == nil {
		t.Fatalf("read through a symlink: %+v %v", r, err)
	}
}

// TestPrune removes what no watcher wrote since the cut, and nothing else.
func TestPrune(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"old", "new"} {
		if err := Write(dir, &Record{SessionID: id, Harness: "codex"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteStatus(dir, &Status{Version: Version, SessionID: "old-native", Limits: []Limit{}}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-40 * 24 * time.Hour)
	for _, p := range []string{Path(dir, "old"), StatusPath(dir, "old-native")} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if n := Prune(dir, time.Now().Add(-30*24*time.Hour)); n != 2 {
		t.Fatalf("pruned %d", n)
	}
	if r, _ := Read(dir, "new"); r == nil {
		t.Fatal("a recent record was removed")
	}
	if r, _ := Read(dir, "old"); r != nil {
		t.Fatal("an old record was kept")
	}
}
