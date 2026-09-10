package transcript_test

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/transcript"
)

// The 2.1.267 record shapes, with the members Brigade never reads (uuid,
// timestamp, cwd, sessionId, version, …) present so the decoder is shown
// to skip them. The values are fixtures under t.TempDir(); nothing here
// touches $CLAUDE_CONFIG_DIR.
const (
	commonTail = `"uuid":"0f6b8c1e-1111-4222-8333-444455556666","parentUuid":null,"timestamp":"2026-09-10T09:00:00.000Z","cwd":"/work/project","sessionId":"beee3690-1111-4222-8333-444455556666","version":"2.1.267","gitBranch":"master","userType":"external"`
)

func attachmentLine(modelID string) string {
	return `{"type":"attachment","isSidechain":false,"attachment":{"type":"model","identity":{"modelId":"` + modelID + `","provider":"firstParty"}},` + commonTail + `}`
}

func assistantLine(model string, input, creation, read int, sidechain bool) string {
	sc := "false"
	if sidechain {
		sc = "true"
	}
	return `{"type":"assistant","isSidechain":` + sc + `,"requestId":"req_1","message":{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"ok"}],"stop_reason":null,"usage":{"input_tokens":` + itoa(input) + `,"cache_creation_input_tokens":` + itoa(creation) + `,"cache_read_input_tokens":` + itoa(read) + `,"output_tokens":9,"service_tier":"standard"}},` + commonTail + `}`
}

// assistantNoUsage is an assistant record without usage (Claude Code
// writes such records for some streamed turns).
func assistantNoUsage(model string) string {
	return `{"type":"assistant","isSidechain":false,"message":{"id":"msg_2","type":"message","role":"assistant","model":"` + model + `","content":[]},` + commonTail + `}`
}

func userLine() string {
	return `{"type":"user","isSidechain":false,"message":{"role":"user","content":"hello"},` + commonTail + `}`
}

func itoa(n int) string { return strconv.Itoa(n) }

func newPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "transcript.jsonl")
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	appendRaw(t, path, strings.Join(lines, "\n")+"\n")
}

func refresh(t *testing.T, r *transcript.Reader) transcript.Facts {
	t.Helper()
	f, err := r.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return f
}

func want(t *testing.T, got transcript.Facts, model string, tokens int, hasContext bool) {
	t.Helper()
	if got.Model != model || got.ContextUsedTokens != tokens || got.HasContext != hasContext {
		t.Fatalf("facts = %+v, want model %q tokens %d hasContext %v", got, model, tokens, hasContext)
	}
}

// TestModelRule pins the model rule across the shapes Claude Code writes:
// the attachment's full id wins over the assistant's bare id it prefixes;
// a later attachment (a /model switch) replaces it; a bare id the
// attachment does not prefix (a switch the attachment missed) wins; with
// no attachment the bare id is all there is.
func TestModelRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		lines []string
		model string
	}{
		{"attachment then assistants: the full id is kept", []string{
			attachmentLine("claude-opus-5[1m]"), userLine(), assistantLine("claude-opus-5", 4, 1000, 188677, false), assistantLine("claude-opus-5", 4, 1000, 188677, false),
		}, "claude-opus-5[1m]"},
		{"a second attachment after a switch", []string{
			attachmentLine("claude-opus-5[1m]"), assistantLine("claude-opus-5", 4, 1000, 188677, false),
			attachmentLine("claude-fable-5-1"), assistantLine("claude-fable-5-1", 4, 0, 2044, false),
		}, "claude-fable-5-1"},
		{"an assistant with another bare id after the attachment: the bare id wins", []string{
			attachmentLine("claude-opus-5[1m]"), assistantLine("claude-opus-5", 4, 1000, 188677, false), assistantLine("claude-fable-5-1", 4, 0, 2044, false),
		}, "claude-fable-5-1"},
		{"no attachment: the bare id", []string{
			userLine(), assistantLine("claude-sonnet-5", 4, 0, 2044, false),
		}, "claude-sonnet-5"},
		{"an assistant before the attachment does not override it", []string{
			assistantLine("claude-sonnet-5", 4, 0, 2044, false), attachmentLine("claude-opus-5[1m]"),
		}, "claude-opus-5[1m]"},
		{"an assistant without a model after the attachment keeps the attachment", []string{
			assistantLine("claude-sonnet-5", 4, 0, 2044, false), attachmentLine("claude-opus-5[1m]"), assistantNoUsage(""),
		}, "claude-opus-5[1m]"},
		{"an attachment of another type is not a model", []string{
			`{"type":"attachment","isSidechain":false,"attachment":{"type":"queued_command","identity":{"modelId":"not-a-model"}}}`, assistantLine("claude-sonnet-5", 4, 0, 2044, false),
		}, "claude-sonnet-5"},
		{"nothing seen", []string{userLine()}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := newPath(t)
			write(t, path, tc.lines...)
			if got := refresh(t, transcript.NewReader(path)); got.Model != tc.model {
				t.Fatalf("model = %q, want %q (facts %+v)", got.Model, tc.model, got)
			}
		})
	}
}

// TestContextIsTheLatestUsageSum: the three input counts of the latest
// assistant record with usage are summed; a record without usage leaves
// the sum alone; nothing seen means HasContext false.
func TestContextIsTheLatestUsageSum(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path, attachmentLine("claude-opus-5[1m]"), userLine())
	r := transcript.NewReader(path)
	want(t, refresh(t, r), "claude-opus-5[1m]", 0, false)
	appendLines(t, path, assistantLine("claude-opus-5", 4, 1000, 188677, false))
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)
	appendLines(t, path, assistantLine("claude-opus-5", 10, 500, 1000, false), assistantNoUsage("claude-opus-5"))
	want(t, refresh(t, r), "claude-opus-5[1m]", 1510, true)
}

// TestSidechainIgnored: a sidechain assistant record (a subagent's turn)
// contributes neither its model nor its usage.
func TestSidechainIgnored(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path,
		attachmentLine("claude-opus-5[1m]"),
		assistantLine("claude-opus-5", 4, 1000, 188677, false),
		assistantLine("claude-haiku-5", 1, 2, 3, true),
		`{"type":"attachment","isSidechain":true,"attachment":{"type":"model","identity":{"modelId":"claude-haiku-5"}}}`,
	)
	want(t, refresh(t, transcript.NewReader(path)), "claude-opus-5[1m]", 189681, true)
}

// TestIncrementalWithPartialTrailingLine: a trailing line without its
// newline is left for the next call — the facts before it stand and the
// bytes are consumed once the line completes — and nothing is re-read.
func TestIncrementalWithPartialTrailingLine(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	whole := assistantLine("claude-opus-5", 4, 1000, 188677, false)
	write(t, path, attachmentLine("claude-opus-5[1m]"))
	appendRaw(t, path, whole[:len(whole)/2])
	r := transcript.NewReader(path)
	want(t, refresh(t, r), "claude-opus-5[1m]", 0, false)
	// Nothing new: a no-op call.
	want(t, refresh(t, r), "claude-opus-5[1m]", 0, false)
	appendRaw(t, path, whole[len(whole)/2:]+"\n")
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)
	// A switch appended afterwards, in pieces across three calls.
	sw := attachmentLine("claude-fable-5-1")
	appendRaw(t, path, sw[:10])
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)
	appendRaw(t, path, sw[10:])
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)
	appendRaw(t, path, "\n")
	want(t, refresh(t, r), "claude-fable-5-1", 189681, true)
}

// TestTruncationResets: a file shorter than the offset starts the reader
// over — the old attachment is forgotten and the new content alone is
// the facts.
func TestTruncationResets(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path, attachmentLine("claude-opus-5[1m]"), assistantLine("claude-opus-5", 4, 1000, 188677, false))
	r := transcript.NewReader(path)
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)
	write(t, path, assistantLine("claude-sonnet-5", 4, 0, 2044, false))
	want(t, refresh(t, r), "claude-sonnet-5", 2048, true)
	// Truncated to nothing: everything is forgotten.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want(t, refresh(t, r), "", 0, false)
}

// TestSkipsNonJSONAndOverLongLines: a line that is not JSON, one whose
// members have the wrong types and one longer than MaxLineBytes are
// skipped, and the records after each are still applied. The over-long
// line arrives in two calls — its first part without a newline, then the
// rest — so the skip is shown to span calls, and its "model" would be
// visible if any part of it were parsed.
func TestSkipsNonJSONAndOverLongLines(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path,
		attachmentLine("claude-opus-5[1m]"),
		"not json at all",
		`{"type":"assistant","isSidechain":"yes","message":"a string"}`,
		`[]`,
		assistantLine("claude-opus-5", 4, 1000, 188677, false),
	)
	r := transcript.NewReader(path)
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)

	huge := `{"type":"assistant","isSidechain":false,"message":{"model":"never-seen","usage":{"input_tokens":1,"cache_creation_input_tokens":1,"cache_read_input_tokens":1}},"pad":"`
	pad := strings.Repeat("x", transcript.MaxLineBytes/2)
	appendRaw(t, path, huge+pad)
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)
	appendRaw(t, path, pad+`"}`+"\n")
	appendLines(t, path, assistantLine("claude-fable-5-1", 4, 0, 2044, false))
	want(t, refresh(t, r), "claude-fable-5-1", 2048, true)

	// A whole over-long line in one call, followed by a record.
	appendRaw(t, path, huge+pad+pad+`"}`+"\n")
	appendLines(t, path, assistantLine("claude-sonnet-5", 1, 2, 3, false))
	want(t, refresh(t, r), "claude-sonnet-5", 6, true)
}

// TestLineAtTheCapIsApplied is the positive control for the cap: a line
// of exactly MaxLineBytes (newline excluded) is parsed.
func TestLineAtTheCapIsApplied(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	head := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5","usage":{"input_tokens":1,"cache_creation_input_tokens":1,"cache_read_input_tokens":1}},"pad":"`
	tail := `"}`
	line := head + strings.Repeat("x", transcript.MaxLineBytes-len(head)-len(tail)) + tail
	if len(line) != transcript.MaxLineBytes {
		t.Fatalf("fixture arithmetic: %d", len(line))
	}
	appendRaw(t, path, line+"\n")
	want(t, refresh(t, transcript.NewReader(path)), "claude-opus-5", 3, true)
}

// TestRefusesWhatIsNotARegularFile: a FIFO (which would block a plain
// open until a writer appeared), a directory and a missing file are each
// refused with a fixed error that never names the path, promptly, and
// the facts already known stand. The 30 s bound is a hang catcher.
func TestRefusesWhatIsNotARegularFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		err  error
	}{
		{"fifo", fifo, transcript.ErrNotRegular},
		{"directory", dir, transcript.ErrNotRegular},
		{"missing", filepath.Join(dir, "missing.jsonl"), transcript.ErrMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			var facts transcript.Facts
			go func() {
				f, err := transcript.NewReader(tc.path).Refresh()
				facts = f
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, tc.err) {
					t.Fatalf("Refresh = %v, want %v", err, tc.err)
				}
				if strings.Contains(err.Error(), dir) {
					t.Fatalf("the error names the path: %q", err)
				}
				if facts != (transcript.Facts{}) {
					t.Fatalf("facts %+v from a refused file", facts)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("Refresh blocked")
			}
		})
	}
}

// TestFactsSurviveAVanishedFile: once read, the facts are returned with
// every later error (the watcher keeps heartbeating what it knew), and a
// file that reappears is read from the start.
func TestFactsSurviveAVanishedFile(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path, attachmentLine("claude-opus-5[1m]"), assistantLine("claude-opus-5", 4, 1000, 188677, false))
	r := transcript.NewReader(path)
	want(t, refresh(t, r), "claude-opus-5[1m]", 189681, true)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	facts, err := r.Refresh()
	if !errors.Is(err, transcript.ErrMissing) {
		t.Fatalf("Refresh after removal = %v, want ErrMissing", err)
	}
	want(t, facts, "claude-opus-5[1m]", 189681, true)
	write(t, path, assistantLine("claude-sonnet-5", 4, 0, 2044, false))
	want(t, refresh(t, r), "claude-sonnet-5", 2048, true)
	if got := r.Path(); got != path {
		t.Fatalf("Path = %q", got)
	}
}

// TestUsageIsNeverNegative: a usage with a negative count is corruption
// and is skipped — the previous sum stands, or nothing is known when it
// was the first — and a sum that would overflow saturates at math.MaxInt
// rather than wrapping, so the reader never yields a count the wire
// forbids (4.4.4) and the watcher never sends a heartbeat the adapter
// would refuse whole.
func TestUsageIsNeverNegative(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path, assistantLine("claude-opus-5", -5, 0, 0, false))
	r := transcript.NewReader(path)
	want(t, refresh(t, r), "claude-opus-5", 0, false)
	appendLines(t, path, assistantLine("claude-opus-5", 4, 1000, 188677, false), assistantLine("claude-opus-5", 0, -1, 0, false), assistantLine("claude-opus-5", 0, 0, -1, false))
	want(t, refresh(t, r), "claude-opus-5", 189681, true)
	appendLines(t, path, assistantLine("claude-opus-5", math.MaxInt, math.MaxInt, 5, false))
	want(t, refresh(t, r), "claude-opus-5", math.MaxInt, true)
	appendLines(t, path, assistantLine("claude-opus-5", 1, 2, 3, false))
	want(t, refresh(t, r), "claude-opus-5", 6, true)
}

// TestLenientStringsAreNotRefused: a record whose strings hold a lone
// surrogate escape ("\ud800", which JSON.stringify writes for an unpaired
// surrogate) or invalid UTF-8 is still a record — its model and usage are
// applied — and so is one with a duplicated member, whose last value wins
// as it does for the JSON.parse that reads the file back.
func TestLenientStringsAreNotRefused(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path,
		`{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-5","content":[{"type":"text","text":"\ud800"}],"usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}}`,
	)
	r := transcript.NewReader(path)
	want(t, refresh(t, r), "claude-opus-5", 6, true)
	appendRaw(t, path, "{\"type\":\"assistant\",\"isSidechain\":false,\"message\":{\"model\":\"claude-opus-5\",\"content\":[{\"type\":\"text\",\"text\":\"\xff\"}],\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0}}}\n")
	want(t, refresh(t, r), "claude-opus-5", 10, true)
	appendLines(t, path, `{"type":"assistant","isSidechain":false,"message":{"model":"claude-sonnet-5","model":"claude-fable-5-1","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`)
	want(t, refresh(t, r), "claude-fable-5-1", 20, true)
}

// TestSyntheticRecordsAreIgnored: the `<synthetic>` assistant record
// Claude Code writes for a turn the API refused (a 529, a usage limit,
// "not logged in" — measured on 2.1.267 with every usage count 0) names
// neither the model nor the context: after one, the model is still the
// attachment's full id (the bare-id-disagrees rule must not fire on it)
// and the context is still the last real turn's sum — or, before any real
// turn, still unknown rather than 0.
func TestSyntheticRecordsAreIgnored(t *testing.T) {
	t.Parallel()
	path := newPath(t)
	write(t, path,
		attachmentLine("claude-opus-5[1m]"),
		assistantLine("<synthetic>", 0, 0, 0, false),
	)
	r := transcript.NewReader(path)
	facts, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if facts.Model != "claude-opus-5[1m]" || facts.HasContext {
		t.Fatalf("after an attachment and a synthetic record: %+v, want the attachment's model and no context", facts)
	}
	appendLines(t, path,
		assistantLine("claude-opus-5", 2, 100, 50, false),
		assistantLine("<synthetic>", 0, 0, 0, false),
	)
	facts, err = r.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if facts.Model != "claude-opus-5[1m]" || !facts.HasContext || facts.ContextUsedTokens != 152 {
		t.Fatalf("after a real turn and a synthetic record: %+v, want claude-opus-5[1m] and 152", facts)
	}
}
