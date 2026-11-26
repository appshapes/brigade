//go:build !windows

package watch

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/usage"
	"github.com/appshapes/brigade/internal/harness/workitem"
)

func usageFixture(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "usage", file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func val[T comparable](t *testing.T, what string, v usage.Value[T], want T) {
	t.Helper()
	if !v.Known() || *v.V != want {
		t.Fatalf("%s = %+v (%q), want %v", what, v.V, v.Unavailable, want)
	}
}

// TestCodexUsage reads a rollout (work item 347): the totals and the last
// call from token_usage_record, which a context Codex marked full does not
// reset, with the cached input as cache reads; the context, the window, the
// plan's own windows by their length, the plan and the credits; another
// quota bucket's snapshot is not the plan's, and a reset out of range is
// left out.
func TestCodexUsage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	putFacts(t, factsLog(root, config.HostCodex), usageFixture(t, "codex-rollout.jsonl"))
	r := hostFactsReader{host: config.HostCodex, root: root, id: factsID}
	f := r.refresh()
	use := f.use

	val(t, "total input", use.Total.Input, int64(32000))
	val(t, "total cache read", use.Total.CacheRead, int64(90000))
	val(t, "total output", use.Total.Output, int64(3100))
	val(t, "total reasoning", use.Total.Reasoning, int64(1500))
	val(t, "last input", use.Last.Input, int64(2000))
	val(t, "context window", use.ContextWindow, int64(272000))
	if !f.hasContext || f.tokens != 2000 {
		t.Fatalf("context: %+v", f)
	}
	if !use.Limits.Known() {
		t.Fatalf("no limits: %q", use.Limits.Unavailable)
	}
	l := *use.Limits.V
	if l.Plan != "plus" || len(l.Windows) != 2 || l.Windows[0].Name != usage.LimitShort || l.Windows[0].UsedPercent != 13 ||
		l.Windows[1].Name != usage.LimitWeekly || !l.Windows[1].ResetsAt.IsZero() {
		t.Fatalf("limits: %+v", l)
	}
	if !l.Windows[0].ResetsAt.Equal(time.Unix(1764158400, 0)) || !use.LimitsAt.Equal(time.Date(2026, 11, 26, 10, 4, 0, 0, time.UTC)) {
		t.Fatalf("times: %v %v", l.Windows[0].ResetsAt, use.LimitsAt)
	}
	if !use.Extra.Known() || use.Extra.V.Balance != "17.25" || !*use.Extra.V.HasCredits {
		t.Fatalf("extra: %+v", use.Extra)
	}

	// Up to the marker: the context is the full one Codex estimated, and
	// the totals are those before it.
	lines := usageFixture(t, "codex-rollout.jsonl")
	root2 := t.TempDir()
	putFacts(t, factsLog(root2, config.HostCodex), lines[:indexNth(lines, '\n', 4)+1])
	r2 := hostFactsReader{host: config.HostCodex, root: root2, id: factsID}
	f2 := r2.refresh()
	if f2.tokens != 149000 {
		t.Fatalf("context at the marker: %d", f2.tokens)
	}
	val(t, "total input at the marker", f2.use.Total.Input, int64(30000))

	// A rollout with no token_usage_record: token_count's totals, the
	// marker passed over.
	var noRecords []string
	for _, line := range strings.SplitAfter(lines, "\n") {
		if !strings.Contains(line, `"token_usage_record"`) {
			noRecords = append(noRecords, line)
		}
	}
	root3 := t.TempDir()
	putFacts(t, factsLog(root3, config.HostCodex), strings.Join(noRecords[:3], ""))
	r3 := hostFactsReader{host: config.HostCodex, root: root3, id: factsID}
	val(t, "token_count total input", r3.refresh().use.Total.Input, int64(30000))
}

// indexNth is the index of the nth occurrence of b in s.
func indexNth(s string, b byte, n int) int {
	for i := range len(s) {
		if s[i] == b {
			if n--; n == 0 {
				return i
			}
		}
	}
	return len(s) - 1
}

// TestMuseUsage sums the journal's model completions once each, at every
// poll and across appends, and leaves out another stream's.
func TestMuseUsage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := factsLog(root, config.HostMuse)
	journal := usageFixture(t, "muse-session.jsonl")
	putFacts(t, path, journal)
	r := hostFactsReader{host: config.HostMuse, root: root, id: factsID}
	for range 3 {
		use := r.refresh().use
		val(t, "input", use.Total.Input, int64(30849+32000-25000))
		val(t, "output", use.Total.Output, int64(1015))
		val(t, "cache read", use.Total.CacheRead, int64(25000))
		val(t, "reasoning", use.Total.Reasoning, int64(856))
		val(t, "calls", use.Calls, int64(2))
		val(t, "last output", use.Last.Output, int64(400))
	}
	more := `{"schema_version":1,"stream":{"kind":"session","id":"` + factsID + `"},"sequence":6,"payload_type":"runtime.session","payload":{"kind":"run","event":{"kind":"model_completed","usage":{"input_tokens":1000,"output_tokens":10,"cache_read_tokens":400}}}}` + "\n"
	putFacts(t, path, journal+more)
	use := r.refresh().use
	val(t, "input after an append", use.Total.Input, int64(30849+32000-25000+600))
	val(t, "calls after an append", use.Calls, int64(3))
	// A line still being written is not counted until it is whole.
	putFacts(t, path, journal+more+`{"payload_type":"runtime.session"`)
	val(t, "calls with a partial line", r.refresh().use.Calls, int64(3))
}

// TestGrokUsage reads usage.json and signals.json beside the summary: the
// session's totals with the cache reads out of the input, the model calls,
// the cost in dollars, and the context with its window. The sample is a
// real session's files with its ids and text replaced (work item 347).
func TestGrokUsage(t *testing.T) {
	t.Parallel()
	const id = "22222222-2222-4222-8222-222222222222"
	home := t.TempDir()
	dir := filepath.Join(home, ".grok", "sessions", GrokEncodedCwd("/work/project"), id)
	for _, f := range []string{"summary.json", "usage.json", "signals.json"} {
		putFacts(t, filepath.Join(dir, f), usageFixture(t, filepath.Join("grok", f)))
	}
	r := hostFactsReader{host: config.HostGrok, root: filepath.Join(home, ".grok"), id: id, cwd: "/work/project"}
	use := r.refresh().use
	val(t, "input", use.Total.Input, int64(19663836-17921280))
	val(t, "cache read", use.Total.CacheRead, int64(17921280))
	val(t, "output", use.Total.Output, int64(131965))
	val(t, "reasoning", use.Total.Reasoning, int64(96147))
	val(t, "calls", use.Calls, int64(159))
	val(t, "context used", use.ContextUsed, int64(147567))
	val(t, "context window", use.ContextWindow, int64(256000))
	if !use.Cost.Known() || use.Cost.V.USD != 13.907516 || use.Cost.V.Kind != usage.KindUnknown {
		t.Fatalf("cost: %+v", use.Cost.V)
	}
	if !use.Last.Output.Known() {
		t.Fatal("no last turn")
	}

	// A file the reader cannot use leaves none of the last one's numbers:
	// bad JSON, another session's id, a cache-read count larger than the
	// input, a window of zero. A file mended is read again.
	good := usageFixture(t, filepath.Join("grok", "usage.json"))
	// In this order, each with a time of its own (putNew): a file
	// rewritten with the same size and time reads as unchanged, which is
	// the reader's rule.
	for i, c := range []struct{ name, body string }{
		{"another session", strings.ReplaceAll(good, id, "44444444-4444-4444-8444-444444444444")},
		{"bad JSON", "{not json"},
		{"cache over input", strings.Replace(good, `"cachedReadTokens": 17921280`, `"cachedReadTokens": 99999999999`, 1)},
	} {
		name, body := c.name, c.body
		putNew(t, filepath.Join(dir, "usage.json"), body, i+1)
		use = r.refresh().use
		if use.Total.Input.Known() || use.Total.Input.Unavailable != usage.BadShape {
			t.Fatalf("%s: input %+v", name, use.Total.Input)
		}
		if name != "cache over input" && (use.Calls.Known() || use.Cost.Known()) {
			t.Fatalf("%s: calls %+v cost %+v", name, use.Calls, use.Cost)
		}
	}
	putNew(t, filepath.Join(dir, "usage.json"), good, 10)
	val(t, "input when mended", r.refresh().use.Total.Input, int64(19663836-17921280))
	signals := usageFixture(t, filepath.Join("grok", "signals.json"))
	putNew(t, filepath.Join(dir, "signals.json"), strings.Replace(signals, `"contextWindowTokens": 256000`, `"contextWindowTokens": 0`, 1), 11)
	if use = r.refresh().use; use.ContextWindow.Known() || use.ContextUsed.Known() {
		t.Fatalf("a window of zero: %+v %+v", use.ContextWindow, use.ContextUsed)
	}
}

// TestGrokUsageOfAnotherSession: usage.json and signals.json beside a
// summary that names another session are not this session's.
func TestGrokUsageOfAnotherSession(t *testing.T) {
	t.Parallel()
	const id = "22222222-2222-4222-8222-222222222222"
	home := t.TempDir()
	dir := filepath.Join(home, ".grok", "sessions", GrokEncodedCwd("/work/project"), id)
	summary := strings.ReplaceAll(usageFixture(t, filepath.Join("grok", "summary.json")), id, "44444444-4444-4444-8444-444444444444")
	putFacts(t, filepath.Join(dir, "summary.json"), summary)
	for _, f := range []string{"usage.json", "signals.json"} {
		putFacts(t, filepath.Join(dir, f), usageFixture(t, filepath.Join("grok", f)))
	}
	r := hostFactsReader{host: config.HostGrok, root: filepath.Join(home, ".grok"), id: id, cwd: "/work/project"}
	if use := r.refresh().use; use.ContextUsed.Known() || use.ContextWindow.Known() || use.Total.Input.Known() {
		t.Fatalf("another session's usage: %+v", use)
	}
}

// putNew writes a file the reader must see as changed: a time of its own,
// as a file rewritten with the same size can keep its inode number and,
// on a coarse clock, its time; the reader takes such a file as unchanged.
func putNew(t *testing.T, path, text string, n int) {
	t.Helper()
	putFacts(t, path, text)
	at := time.Date(2026, 11, 26, 12, 0, n, 0, time.UTC)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestMuseLimitsOf reads `muse usage --json`: a plan's two windows with
// the tier, or the reason Muse gave for none.
func TestMuseLimitsOf(t *testing.T) {
	t.Parallel()
	limits, at := museLimitsOf([]byte(usageFixture(t, "muse-usage.json")))
	// The fixture is what Muse Code 1.4.25 printed on 2026-11-26, with the
	// two percentages changed: asOfMs, and a window with no length.
	if !limits.Known() || limits.V.Plan != "Muse Code Everyday Usage" || len(limits.V.Windows) != 2 || !at.Equal(time.UnixMilli(1795711091896)) {
		t.Fatalf("limits: %+v %v", limits, at)
	}
	if w := limits.V.Windows; w[0].Name != usage.LimitShort || w[0].UsedPercent != 37 || w[0].WindowMinutes != 0 || w[1].UsedPercent != 112 {
		t.Fatalf("windows: %+v", w)
	}
	none, _ := museLimitsOf([]byte(usageFixture(t, "muse-usage-none.json")))
	if none.Known() || none.Unavailable != "Muse Code has none: server_sent_no_usage" {
		t.Fatalf("none: %+v", none)
	}
	if bad, _ := museLimitsOf([]byte("no")); bad.Unavailable != usage.CommandFailed {
		t.Fatalf("bad: %+v", bad)
	}
}

// TestRecordUsageWritesEveryValueOrItsReason is the record the watcher
// writes for a Muse session: every value there or with its reason, never a
// blank and never a zero for what is not known; the limits command runs
// once in its interval, and a record that did not change is not written
// again until it is old.
func TestRecordUsageWritesEveryValueOrItsReason(t *testing.T) {
	t.Parallel()
	root, state := t.TempDir(), t.TempDir()
	putFacts(t, factsLog(root, config.HostMuse), usageFixture(t, "muse-session.jsonl"))
	runs := 0
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	w := &watcher{
		sessionID: "brigade-1", harness: string(config.HostMuse), model: "muse-1",
		rc: runConfig{env: config.WatcherEnv{StateDir: state}}, log: slog.New(slog.DiscardHandler),
		deps: Deps{Clock: func() time.Time { return now }, UsageCommand: func(context.Context, []string, []string) ([]byte, error) {
			runs++
			return []byte(usageFixture(t, "muse-usage.json")), nil
		}},
		state: &shared{name: "347-muse"},
	}
	w.hostFacts = hostFactsReader{host: config.HostMuse, root: root, id: factsID}
	w.hostFacts.refresh()
	w.recordUsage(w.state.snapshot())
	rec, err := usage.Read(state, "brigade-1")
	if err != nil || rec == nil {
		t.Fatalf("no record: %v", err)
	}
	if rec.Session != "347-muse" || rec.Harness != "muse" || !rec.ReadAt.Equal(now) {
		t.Fatalf("record: %+v", rec)
	}
	val(t, "model", rec.Model, "muse-1")
	val(t, "calls", rec.Calls, int64(2))
	if rec.ContextWindow.Known() || rec.ContextWindow.Unavailable != usage.NotGiven || rec.Cost.Unavailable != usage.NotGiven {
		t.Fatalf("Muse's window and cost: %+v %+v", rec.ContextWindow, rec.Cost)
	}
	if !rec.Limits.Known() || len(rec.Limits.V.Windows) != 2 {
		t.Fatalf("limits: %+v", rec.Limits)
	}
	assertNoBlank(t, rec)

	// Within the interval: no second run, and an unchanged record is not
	// written again.
	now = now.Add(time.Minute)
	w.recordUsage(w.state.snapshot())
	if again, _ := usage.Read(state, "brigade-1"); runs != 1 || !again.ReadAt.Equal(rec.ReadAt) {
		t.Fatalf("runs %d, read_at %v", runs, again.ReadAt)
	}
	// After it: the command runs again, and a failure says so.
	now = now.Add(usageLimitsEvery)
	w.deps.UsageCommand = func(context.Context, []string, []string) ([]byte, error) { return nil, errors.New("no muse") }
	w.recordUsage(w.state.snapshot())
	if again, _ := usage.Read(state, "brigade-1"); again.Limits.Unavailable != usage.CommandFailed || !again.ReadAt.Equal(now) {
		t.Fatalf("after a failure: %+v", again.Limits)
	}
}

// TestRecordUsageClaudeCode is a Claude Code session's record: the tokens
// from the transcript, and the window, the cost and the limits from the
// status line, or the reason that names the step when it has none.
func TestRecordUsageClaudeCode(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	transcriptPath := filepath.Join(t.TempDir(), "t.jsonl")
	putFacts(t, transcriptPath,
		`{"type":"assistant","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}}`+"\n"+
			`{"type":"assistant","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}}`+"\n")
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	w := &watcher{
		sessionID: "brigade-2", rc: runConfig{env: config.WatcherEnv{StateDir: state}}, log: slog.New(slog.DiscardHandler),
		deps:  Deps{Clock: func() time.Time { return now }},
		state: &shared{name: "347-claude", transcriptPath: transcriptPath, nativeID: "native-2"},
	}
	w.transcriptFacts(transcriptPath)
	w.recordUsage(w.state.snapshot())
	rec, _ := usage.Read(state, "brigade-2")
	val(t, "calls", rec.Calls, int64(1))
	val(t, "cache read", rec.Total.CacheRead, int64(1000))
	val(t, "context", rec.ContextUsed, int64(1110))
	if rec.Cost.Unavailable != usage.StatusLineOnly || rec.Limits.Unavailable != usage.StatusLineOnly || rec.Total.Reasoning.Unavailable != "Claude Code counts thinking in the output" {
		t.Fatalf("without a status line: %+v %+v", rec.Cost, rec.Limits)
	}
	assertNoBlank(t, rec)

	st, err := usage.ParseStatus([]byte(`{"session_id":"native-2","cost":{"total_cost_usd":1.25},"context_window":{"context_window_size":1000000},"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600}}}`), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := usage.WriteStatus(state, st); err != nil {
		t.Fatal(err)
	}
	w.recordUsage(w.state.snapshot())
	rec, _ = usage.Read(state, "brigade-2")
	if !rec.Cost.Known() || rec.Cost.V.USD != 1.25 || rec.Cost.V.Kind != usage.KindEstimate {
		t.Fatalf("cost: %+v", rec.Cost)
	}
	val(t, "window", rec.ContextWindow, int64(1000000))
	if !rec.Limits.Known() || len(rec.Limits.V.Windows) != 1 || rec.Limits.V.Windows[0].UsedPercent != 23.5 {
		t.Fatalf("limits: %+v", rec.Limits)
	}
}

// assertNoBlank fails on a value that is neither there nor explained.
func assertNoBlank(t *testing.T, r *usage.Record) {
	t.Helper()
	check := func(what string, known bool, reason string) {
		if !known && reason == "" {
			t.Fatalf("%s is blank", what)
		}
	}
	check("model", r.Model.Known(), r.Model.Unavailable)
	check("context used", r.ContextUsed.Known(), r.ContextUsed.Unavailable)
	check("context window", r.ContextWindow.Known(), r.ContextWindow.Unavailable)
	check("calls", r.Calls.Known(), r.Calls.Unavailable)
	check("cost", r.Cost.Known(), r.Cost.Unavailable)
	check("limits", r.Limits.Known(), r.Limits.Unavailable)
	check("extra", r.Extra.Known(), r.Extra.Unavailable)
	for _, tk := range []usage.Tokens{r.Total, r.Last} {
		for _, v := range []usage.Value[int64]{tk.Input, tk.Output, tk.CacheRead, tk.CacheWrite, tk.Reasoning} {
			check("a token count", v.Known(), v.Unavailable)
		}
	}
}

// TestRecordUsageAddsToTheWorkItem is work item 349: at each heartbeat,
// what the session spent since the one before goes to the work item it
// works on then, priced at its model's price; what it spent before the
// watcher's first record is no work item's; a watcher that starts again
// carries the sums on.
func TestRecordUsageAddsToTheWorkItem(t *testing.T) {
	t.Parallel()
	state, board := t.TempDir(), t.TempDir()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	msg := func(id string, in int) string {
		return `{"type":"assistant","message":{"id":"` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":` + strconv.Itoa(in) +
			`,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}` + "\n"
	}
	putFacts(t, path, msg("m0", 500))
	store := workitem.Store{Folder: board}
	b, _ := workitem.Read(board)
	list := b.Lists[0].Key
	ids := map[string]string{}
	for _, title := range []string{"first", "second"} {
		ti := title
		id, err := store.Add(b, workitem.Fields{Title: &ti, List: &list}, workitem.Author{})
		if err != nil {
			t.Fatal(err)
		}
		ids[title] = id
		b, _ = workitem.Read(board)
	}
	start := func(title string) {
		t.Helper()
		sw, _ := workitem.ReadSessionWork(board, "brigade-9")
		if err := store.StartWork(sw, b.ByID(ids[title]), workitem.Author{}); err != nil {
			t.Fatal(err)
		}
	}
	newWatcher := func() *watcher {
		return &watcher{
			sessionID: "brigade-9", sync: syncSetup{board: board}, rc: runConfig{env: config.WatcherEnv{StateDir: state}},
			log: slog.New(slog.DiscardHandler), deps: Deps{Clock: time.Now},
			state: &shared{name: "349", transcriptPath: path, nativeID: "native-9"},
		}
	}
	beat := func(w *watcher) *usage.Record {
		t.Helper()
		w.transcriptFacts(path)
		w.usage.written = time.Time{} // write at once, as a changed record is
		w.recordUsage(w.state.snapshot())
		rec, err := usage.Read(state, "brigade-9")
		if err != nil || rec == nil {
			t.Fatalf("no record: %v", err)
		}
		return rec
	}
	w := newWatcher()
	start("first")
	if rec := beat(w); len(rec.WorkItems) != 0 {
		t.Fatalf("what was spent before the first record went to a work item: %+v", rec.WorkItems)
	}
	putFacts(t, path, msg("m0", 500)+msg("m1", 1000))
	rec := beat(w)
	first := rec.WorkItems[ids["first"]]
	if first == nil || first.Number != 1 || first.Tokens.Input != 1000 || first.CostUSD != 0.004 || first.Priced != 1 {
		t.Fatalf("first: %+v", first)
	}
	start("second")
	putFacts(t, path, msg("m0", 500)+msg("m1", 1000)+msg("m2", 2000))
	rec = beat(w)
	if second := rec.WorkItems[ids["second"]]; second == nil || second.Tokens.Input != 2000 || rec.WorkItems[ids["first"]].Tokens.Input != 1000 {
		t.Fatalf("second: %+v", rec.WorkItems)
	}

	// A new watcher takes the sums up from a recent record.
	w2 := newWatcher()
	putFacts(t, path, msg("m0", 500)+msg("m1", 1000)+msg("m2", 2000)+msg("m3", 4000))
	rec = beat(w2)
	if second := rec.WorkItems[ids["second"]]; second == nil || second.Tokens.Input != 6000 || rec.WorkItems[ids["first"]] == nil {
		t.Fatalf("after a new watcher: %+v", rec.WorkItems)
	}
}

// TestAddToWorkItemPassesOverAnUnknownTotal: a heartbeat whose total is
// not known (a record read in the middle of a write) is no reading, so the
// next known total is not taken for a reset and counted again.
func TestAddToWorkItemPassesOverAnUnknownTotal(t *testing.T) {
	t.Parallel()
	board := t.TempDir()
	store := workitem.Store{Folder: board}
	b, _ := workitem.Read(board)
	title, list := "one", b.Lists[0].Key
	id, err := store.Add(b, workitem.Fields{Title: &title, List: &list}, workitem.Author{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = workitem.Read(board)
	if err := store.StartWork(&workitem.SessionWork{SessionID: "brigade-7"}, b.ByID(id), workitem.Author{}); err != nil {
		t.Fatal(err)
	}
	w := &watcher{sessionID: "brigade-7", sync: syncSetup{board: board}, rc: runConfig{env: config.WatcherEnv{StateDir: t.TempDir()}}, log: slog.New(slog.DiscardHandler)}
	total := func(in int64) *usage.Record {
		return &usage.Record{Model: usage.Of("claude-opus-5-5"), Total: usage.Tokens{Input: usage.Of(in), Output: usage.Of(int64(0))}}
	}
	unknown := &usage.Record{Total: usage.Tokens{Input: usage.Missing[int64](usage.BadShape)}}
	for _, r := range []*usage.Record{unknown, total(2_000_000), total(2_000_100), unknown, total(2_000_300)} {
		w.addToWorkItem(r)
	}
	if u := w.usage.items[id]; u == nil || u.Tokens.Input != 300 {
		t.Fatalf("the work item's input: %+v", u)
	}
}

// TestAddToWorkItemDoesNotBridgeAnOldRecord: a new watcher carries the
// sums of a record older than usageBridge, but gives what was spent since
// that record to no work item.
func TestAddToWorkItemDoesNotBridgeAnOldRecord(t *testing.T) {
	t.Parallel()
	state, board := t.TempDir(), t.TempDir()
	store := workitem.Store{Folder: board}
	b, _ := workitem.Read(board)
	title, list := "one", b.Lists[0].Key
	id, err := store.Add(b, workitem.Fields{Title: &title, List: &list}, workitem.Author{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = workitem.Read(board)
	if err := store.StartWork(&workitem.SessionWork{SessionID: "brigade-8"}, b.ByID(id), workitem.Author{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	old := &usage.Record{SessionID: "brigade-8", Harness: "claude-code", ReadAt: now.Add(-time.Hour),
		Total:     usage.Tokens{Input: usage.Of(int64(1000)), Output: usage.Of(int64(0))},
		WorkItems: map[string]*usage.ItemUsage{id: {Number: 1, Tokens: usage.Counts{Input: 10}}}}
	if err := usage.Write(state, old); err != nil {
		t.Fatal(err)
	}
	w := &watcher{sessionID: "brigade-8", sync: syncSetup{board: board}, rc: runConfig{env: config.WatcherEnv{StateDir: state}},
		log: slog.New(slog.DiscardHandler), deps: Deps{Clock: func() time.Time { return now }}}
	r := func(in int64) *usage.Record {
		return &usage.Record{Total: usage.Tokens{Input: usage.Of(in), Output: usage.Of(int64(0))}}
	}
	w.addToWorkItem(r(900_000)) // the gap of an hour: no work item's
	w.addToWorkItem(r(900_050))
	if u := w.usage.items[id]; u == nil || u.Tokens.Input != 60 {
		t.Fatalf("the work item's input: %+v", u)
	}
}
