package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTokensCountEachMessageOnce is work item 347: Claude Code writes one
// assistant record for each content block and repeats the message's usage
// on each, so the totals count a message once by its id; a subagent's call
// in the same file counts, a refused call (<synthetic>) and a message with
// no id do not, and an append is counted at the next refresh.
func TestTokensCountEachMessageOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	rec := func(id, model string, side bool, in, out, cw, cr int) string {
		s := `{"type":"assistant","isSidechain":` + map[bool]string{true: "true", false: "false"}[side] +
			`,"message":{"model":"` + model + `"`
		if id != "" {
			s += `,"id":"` + id + `"`
		}
		return s + `,"usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) +
			`,"cache_creation_input_tokens":` + itoa(cw) + `,"cache_read_input_tokens":` + itoa(cr) + `}}}` + "\n"
	}
	body := rec("m1", "claude-opus-5-5", false, 10, 5, 100, 1000) +
		rec("m1", "claude-opus-5-5", false, 10, 5, 100, 1000) +
		rec("m1", "claude-opus-5-5", false, 10, 5, 100, 1000) +
		rec("m2", "claude-opus-5-5", true, 1, 2, 3, 4) +
		rec("m3", "<synthetic>", false, 0, 0, 0, 0) +
		rec("", "claude-opus-5-5", false, 7, 7, 7, 7)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReader(path)
	f, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	want := Tokens{Input: 11, Output: 7, CacheWrite: 103, CacheRead: 1004}
	if f.Calls != 2 || f.Total != want || f.Last != (Tokens{Input: 1, Output: 2, CacheWrite: 3, CacheRead: 4}) {
		t.Fatalf("calls %d total %+v last %+v", f.Calls, f.Total, f.Last)
	}
	// The subagent's record leaves the context as the main session's.
	if f.ContextUsedTokens != 7+7+7 {
		t.Fatalf("context %d", f.ContextUsedTokens)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(rec("m1", "claude-opus-5-5", false, 10, 5, 100, 1000) + rec("m4", "claude-opus-5-5", false, 1, 1, 1, 1)); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if f, _ = r.Refresh(); f.Calls != 3 || f.Total.Input != 12 {
		t.Fatalf("after an append: calls %d total %+v", f.Calls, f.Total)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
