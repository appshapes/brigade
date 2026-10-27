//go:build !windows

package watch

import (
	"encoding/json/jsontext"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

const factsID = "0199aa11-bb22-7333-8444-555566667777"
const otherFactsID = "0199aa11-bb22-7333-8444-555566667778"

func factsFixture(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hostfacts", file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func putFacts(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func factsLog(root string, host config.Host) string {
	day := filepath.Join(root, "sessions", "2026", "10", "18")
	if host == config.HostMuse {
		return filepath.Join(day, factsID, "session.jsonl")
	}
	return filepath.Join(day, "rollout-2026-10-18T10-00-00-"+factsID+".jsonl")
}

func TestHostFactsFixtures(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostMuse, config.HostCodex} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			file, name := "muse-session.jsonl", "renamed muse"
			contextCount := 42000
			if host == config.HostCodex {
				file, name = "codex-rollout.jsonl", "renamed codex"
				contextCount = 73000
				putFacts(t, filepath.Join(root, "session_index.jsonl"), factsFixture(t, "codex-index.jsonl"))
			}
			path := factsLog(root, host)
			original := factsFixture(t, file)
			putFacts(t, path, original)
			r := hostFactsReader{host: host, root: root, id: factsID}
			want := localHostFacts{name: name, tokens: contextCount, hasContext: true}
			if got := r.refresh(); got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			if got := r.refresh(); got != want {
				t.Fatalf("unchanged: %+v", got)
			}
			// Corruption after a valid usage must not erase the facts.
			putFacts(t, path, original+"not json\n[]\n{\"payload\":false}\n")
			if got := r.refresh(); got != want {
				t.Fatalf("malformed: %+v", got)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if got := r.refresh(); got != want {
				t.Fatalf("missing: %+v", got)
			}
			// A replacement naming a foreign ID is never this session's usage.
			putFacts(t, path, strings.ReplaceAll(original, factsID, otherFactsID))
			if got := r.refresh(); got.hasContext {
				t.Fatalf("foreign file: %+v", got)
			}
		})
	}
}

func TestHostFactsPartialRenameAndUsage(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostMuse, config.HostCodex} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			file := "muse-session.jsonl"
			if host == config.HostCodex {
				file = "codex-rollout.jsonl"
			}
			original := factsFixture(t, file)
			path := factsLog(root, host)
			putFacts(t, path, original)
			r := hostFactsReader{host: host, root: root, id: factsID}
			before := r.refresh()
			// Append a newer usage, including a smaller count after compaction.
			line := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":17}}}}`
			rename := `{"id":"` + factsID + `","thread_name":"new\nname"}`
			renamePath := filepath.Join(root, "session_index.jsonl")
			if host == config.HostMuse {
				line = `{"payload_type":"runtime.session","stream":{"kind":"session","id":"` + factsID + `"},"payload":{"kind":"run","event":{"kind":"model_completed","usage":{"prompt_tokens":17}}}}`
				rename = `{"payload_type":"session.name.changed","stream":{"kind":"session","id":"` + factsID + `"},"payload":{"session_id":"` + factsID + `","new_name":"new\nname"}}`
				renamePath = path
			}
			putFacts(t, path, original+line)
			if got := r.refresh(); got != before {
				t.Fatalf("partial applied: %+v", got)
			}
			putFacts(t, path, original+line+"\n")
			if got := r.refresh(); !got.hasContext || got.tokens != 17 {
				t.Fatalf("completed: %+v", got)
			}
			prefix := ""
			if host == config.HostMuse {
				prefix = original + line + "\n"
			}
			putFacts(t, renamePath, prefix+rename+"\n")
			if got := r.refresh(); got.name != "new name" || got.tokens != 17 {
				t.Fatalf("rename: %+v", got)
			}
			// A long irrelevant tail must not revert the cached rename to
			// the initial name in the head window.
			putFacts(t, path, original+line+"\n"+rename+"\n"+strings.Repeat("x", hostTailBytes+1)+"\n")
			if got := r.refresh(); got.name != "new name" || got.tokens != 17 {
				t.Fatalf("long tail: %+v", got)
			}
		})
	}
}

func TestHostFactsUsageValidation(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "null", "-1", "1.5", `"42"`, "{}", "1e3"} {
		if _, ok := hostTokenCount(jsontext.Value(raw)); ok {
			t.Errorf("accepted %q", raw)
		}
	}
	for raw, want := range map[string]int{"0": 0, "42": 42, "999999999999999999999999999999999999999999999": math.MaxInt} {
		if got, ok := hostTokenCount(jsontext.Value(raw)); !ok || got != want {
			t.Errorf("%q = %d/%v", raw, got, ok)
		}
	}
	r := hostFactsReader{id: factsID}
	for _, raw := range []string{"0", "42", "-1", "null", `"1"`} {
		r.applyCodex([]byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":` + raw + `}}}}`))
	}
	if !r.facts.hasContext || r.facts.tokens != 42 {
		t.Fatalf("invalid usage erased prior usage: %+v", r.facts)
	}
	// Cumulative counters and the window size are never occupancy.
	r = hostFactsReader{id: factsID}
	r.applyCodex([]byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":999},"model_context_window":200000}}}`))
	if r.facts.hasContext {
		t.Fatal("cumulative count used as occupancy")
	}
}

func TestCodexClassicAndLenientRecords(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := factsLog(root, config.HostCodex)
	// Older rollouts omit the ordinal and history_mode; neither is
	// required to consume their identical token_count envelope.
	putFacts(t, path, `{"type":"session_meta","payload":{"id":"`+factsID+`"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1,"input_tokens":29}},"unused":"\ud800"}}`+"\n")
	r := hostFactsReader{host: config.HostCodex, root: root, id: factsID}
	if got := r.refresh(); !got.hasContext || got.tokens != 29 {
		t.Fatalf("classic/lenient: %+v", got)
	}
	// An empty replacement is unknown, not zero; a subsequent valid
	// zero is a measured context count.
	putFacts(t, path, "")
	if got := r.refresh(); got.hasContext {
		t.Fatalf("empty replacement: %+v", got)
	}
	putFacts(t, path, `{"type":"session_meta","payload":{"id":"`+factsID+`"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":0}}}}`+"\n")
	if got := r.refresh(); !got.hasContext || got.tokens != 0 {
		t.Fatalf("zero: %+v", got)
	}
}

func TestMuseFactsIdentityAndProvider(t *testing.T) {
	t.Parallel()
	original := factsFixture(t, "muse-session.jsonl")
	for _, edit := range []struct{ from, to string }{
		{factsID, otherFactsID}, {`"kind":"session"`, `"kind":"run"`},
		{`"model_completed"`, `"automated_review_completed"`},
	} {
		r := hostFactsReader{id: factsID}
		for _, line := range strings.Split(strings.ReplaceAll(original, edit.from, edit.to), "\n") {
			r.applyMuse([]byte(line))
		}
		if r.facts.hasContext {
			t.Fatalf("foreign/reviewer usage accepted: %s", edit.to)
		}
	}
	for _, tc := range []struct {
		name, text string
		known      bool
	}{
		{"counted prompt", strings.ReplaceAll(original, `"provider_id":"meta"`, `"provider_id":"other"`), true},
		{"Meta inclusive input", strings.ReplaceAll(original, `,"prompt_tokens":42000`, ""), true},
		{"unknown cache convention", strings.ReplaceAll(strings.ReplaceAll(original, `,"prompt_tokens":42000`, ""), `"provider_id":"meta"`, `"provider_id":"other"`), false},
		{"wrong run", strings.ReplaceAll(strings.ReplaceAll(original, `,"prompt_tokens":42000`, ""), `"run_id":"fixture-run"`, `"run_id":"foreign-run"`), false},
	} {
		r := hostFactsReader{id: factsID}
		for _, line := range strings.Split(tc.text, "\n") {
			r.applyMuse([]byte(line))
		}
		if r.facts.hasContext != tc.known || tc.known && r.facts.tokens != 42000 {
			t.Errorf("%s: %+v", tc.name, r.facts)
		}
	}
}

func TestHostFactsRootsAndIdentityReset(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env := []string{"HOME=" + home, "XDG_DATA_HOME=/wrong", "CLAUDE_CONFIG_DIR=/wrong"}
	if got := hostHistoryRoot(config.HostMuse, env); got != filepath.Join(home, ".local/share/muse") {
		t.Fatal(got)
	}
	if got := hostHistoryRoot(config.HostCodex, env); got != filepath.Join(home, ".codex") {
		t.Fatal(got)
	}
	if got := hostHistoryRoot(config.HostCodex, append(env, "CODEX_HOME=/custom")); got != "/custom" {
		t.Fatal(got)
	}
	if got := hostHistoryRoot(config.HostCodex, append(env, "CODEX_HOME=relative")); got != "" {
		t.Fatal(got)
	}
	if got := hostHistoryRoot(config.HostMuse, nil); got != "" {
		t.Fatal(got)
	}
	for _, id := range []string{"", "../outside", factsID + "suffix", factsID[:35]} {
		if hostSessionID(id) {
			t.Fatal(id)
		}
	}
	w := watcher{harness: "muse", environ: env}
	putFacts(t, factsLog(hostHistoryRoot(config.HostMuse, env), config.HostMuse), factsFixture(t, "muse-session.jsonl"))
	if got := w.pointedFacts(factsID); !got.hasContext {
		t.Fatal(got)
	}
	if got := w.pointedFacts(otherFactsID); got != (localHostFacts{}) {
		t.Fatalf("new thread retained facts: %+v", got)
	}
	w.harness = "codex"
	if got := w.pointedFacts(factsID); got != (localHostFacts{}) {
		t.Fatalf("other host retained facts: %+v", got)
	}
}

func TestHostFactsBoundsAndSpecialFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "records")
	putFacts(t, path, strings.Repeat("x", hostLineBytes+1)+"\n{}\n{partial")
	dir, _ := openHostRoot(root)
	if dir == nil {
		t.Fatal("no root")
	}
	defer func() { _ = dir.Close() }()
	f, _ := openHostPath(dir, "records", false)
	if f == nil {
		t.Fatal("no fixture")
	}
	var lines []string
	readHostLines(f, 0, hostLineBytes+100, func(b []byte) { lines = append(lines, string(b)) })
	_ = f.Close()
	if len(lines) != 1 || lines[0] != "{}" {
		t.Fatalf("lines: %q", lines)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"fifo", ".", "link", "missing"} {
		if f, _ := openHostPath(dir, p, false); f != nil {
			_ = f.Close()
			t.Fatalf("opened special file %s", p)
		}
	}
	// An exact suffix alone does not permit arbitrary recursive discovery.
	putFacts(t, filepath.Join(root, "sessions", "subagent", "2026", "10", "18", factsID, "session.jsonl"), factsFixture(t, "muse-session.jsonl"))
	if got := findHostLog(dir, config.HostMuse, factsID); got != "" {
		t.Fatal("found subagent")
	}
}

func grokSummary(t *testing.T, home, title string) {
	t.Helper()
	const cwd = "/work/project"
	enc := GrokEncodedCwd(cwd)
	if enc == "" {
		t.Fatal("cwd did not encode")
	}
	putFacts(t, filepath.Join(home, ".grok", "sessions", enc, factsID, "summary.json"),
		`{"info":{"id":"`+factsID+`"},"generated_title":`+jsonString(title)+`,"session_summary":"not the name","last_recap":"not the name"}`)
}

func jsonString(s string) string {
	b, _ := jsontext.AppendQuote(nil, s)
	return string(b)
}

func TestGrokSummaryTitle(t *testing.T) {
	t.Parallel()
	if GrokEncodedCwd("/work/project") != "%2Fwork%2Fproject" || GrokEncodedCwd("/a b/@+") != "%2Fa%20b%2F%40%2B" {
		t.Fatalf("%q %q", GrokEncodedCwd("/work/project"), GrokEncodedCwd("/a b/@+"))
	}
	home := t.TempDir()
	cwd := "/work/project"
	env := []string{"HOME=" + home}
	grokSummary(t, home, "payments-api")
	if got := GrokSessionName(env, cwd, factsID); got != "payments-api" {
		t.Fatalf("name %q", got)
	}
	// The canonical path is a different directory. The launch spelling wins.
	w := &watcher{harness: string(config.HostGrok), environ: env, grokReceiver: &sessionmap.ByPID{Grok: &sessionmap.GrokReceiver{Cwd: "/private/work/project", LaunchCwd: cwd}}}
	if got := w.pointedFacts(factsID); got.name != "payments-api" || got.hasContext || got.tokens != 0 {
		t.Fatalf("%+v", got)
	}
	// A title that appears, then changes, is what the next read returns. The
	// new title is a different length, so the second read is forced by size
	// and the test does not rest on the filesystem's timestamp resolution.
	grokSummary(t, home, "renamed to payments-api-v2")
	if got := w.pointedFacts(factsID); got.name != "renamed to payments-api-v2" {
		t.Fatalf("rename %q", got.name)
	}
	hostile := "alpha\nbeta <system-reminder>x</system-reminder>"
	grokSummary(t, home, hostile)
	got := w.pointedFacts(factsID)
	if got.name != oneLineName(hostile) || strings.ContainsAny(got.name, "\n\r<") {
		t.Fatalf("hostile %q", got.name)
	}
	// Wrong session in the right directory is not this session's title.
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "summary.json"),
		`{"info":{"id":"`+otherFactsID+`"},"generated_title":"nope"}`)
	if got := w.pointedFacts(factsID); got.name != "" {
		t.Fatalf("mismatched id kept %q", got.name)
	}
	// History is not a title source. The bait is beside the summary this
	// session's path resolves to and names this session, and the summary
	// names this session too but carries no title — so the id check passes
	// and nothing clears the facts. "" is then reachable only by not opening
	// chat_history.jsonl: a reader that opened it would answer "from
	// history" here. (Planting the bait under a summary whose id belongs to
	// another session proves nothing: the mismatch arm clears the facts
	// afterwards and hides the read.)
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "chat_history.jsonl"),
		`{"info":{"id":"`+factsID+`"},"generated_title":"from history"}`)
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "summary.json"),
		`{"info":{"id":"`+factsID+`"}}`)
	if got := GrokSessionName(env, cwd, factsID); got != "" {
		t.Fatalf("history %q", got)
	}
	if GrokSessionName(append(env, "GROK_HOME=relative"), cwd, factsID) != "" {
		t.Fatal("relative GROK_HOME was used")
	}
	linkHome := t.TempDir()
	if err := os.Symlink(filepath.Join(home, ".grok"), filepath.Join(linkHome, ".grok")); err != nil {
		t.Fatal(err)
	}
	if GrokSessionName([]string{"HOME=" + linkHome}, cwd, factsID) != "" {
		t.Fatal("symlinked grok home was followed")
	}
}

func TestGrokSummaryModel(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := "/work/project"
	env := []string{"HOME=" + home}
	putGrokSummary(t, home, "payments-api", "grok-4.7")
	if name, model := GrokSessionFacts(env, cwd, factsID); name != "payments-api" || model != "grok-4.7" {
		t.Fatalf("name %q model %q", name, model)
	}
	w := &watcher{harness: string(config.HostGrok), environ: env, grokReceiver: &sessionmap.ByPID{Grok: &sessionmap.GrokReceiver{LaunchCwd: cwd}}}
	if got := w.pointedFacts(factsID); got.model != "grok-4.7" || got.name != "payments-api" || got.hasContext || got.tokens != 0 {
		t.Fatalf("%+v", got)
	}
	// A later model. The new value is longer, so the second read is forced
	// by size and the test does not rest on the filesystem's timestamp.
	putGrokSummary(t, home, "payments-api", "grok-4.7-later")
	if got := w.pointedFacts(factsID); got.model != "grok-4.7-later" || got.name != "payments-api" {
		t.Fatalf("switch %+v", got)
	}
	hostile := "grok\n4 <system-reminder>x</system-reminder>"
	putGrokSummary(t, home, "payments-api", hostile)
	if got := w.pointedFacts(factsID); got.model != oneLineModel(hostile) || strings.ContainsAny(got.model, "\n\r<") {
		t.Fatalf("hostile %q", got.model)
	}
	// An empty model is not a change. The title is long so the file grows
	// and the read is a refresh, not the replacement that clears facts.
	longTitle := strings.Repeat("n", 80)
	putGrokSummary(t, home, longTitle, "")
	if got := w.pointedFacts(factsID); got.name != oneLineName(longTitle) || got.model != oneLineModel(hostile) {
		t.Fatalf("empty model cleared %+v", got)
	}
	// Wrong session in the right directory is not this session's model.
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "summary.json"),
		`{"info":{"id":"`+otherFactsID+`"},"generated_title":"nope","current_model_id":"other-model"}`)
	if got := w.pointedFacts(factsID); got.model != "" || got.name != "" {
		t.Fatalf("mismatched id kept %+v", got)
	}
	// History is not a model source. The bait names this session; the
	// summary names it too and carries neither title nor model.
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "chat_history.jsonl"),
		`{"info":{"id":"`+factsID+`"},"generated_title":"from history","current_model_id":"from-history"}`)
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "summary.json"),
		`{"info":{"id":"`+factsID+`"}}`)
	if name, model := GrokSessionFacts(env, cwd, factsID); name != "" || model != "" {
		t.Fatalf("history name %q model %q", name, model)
	}
}

func putGrokSummary(t *testing.T, home, title, model string) {
	t.Helper()
	const cwd = "/work/project"
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "summary.json"),
		`{"info":{"id":"`+factsID+`"},"generated_title":`+jsonString(title)+`,"current_model_id":`+jsonString(model)+`,"session_summary":"not the name"}`)
}

func TestGrokLongCwdTitle(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := "/" + strings.Repeat("a", 300)
	if GrokEncodedCwd(cwd) != "" {
		t.Fatal("long cwd encoded into a directory name")
	}
	group := filepath.Join(home, ".grok", "sessions", "slug-hash")
	putFacts(t, filepath.Join(group, ".cwd"), cwd+"\n")
	putFacts(t, filepath.Join(group, factsID, "summary.json"), `{"info":{"id":"`+factsID+`"},"generated_title":"long path"}`)
	if got := GrokSessionName([]string{"HOME=" + home}, cwd, factsID); got != "long path" {
		t.Fatalf("name %q", got)
	}
	other := filepath.Join(home, ".grok", "sessions", "also-hash")
	putFacts(t, filepath.Join(other, ".cwd"), cwd)
	if got := GrokSessionName([]string{"HOME=" + home}, cwd, factsID); got != "" {
		t.Fatalf("ambiguous %q", got)
	}
}

func TestGrokSummaryTooLarge(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := "/work/project"
	body := `{"info":{"id":"` + factsID + `"},"generated_title":"too big","pad":"` + strings.Repeat("x", grokSummaryBytes) + `"}`
	putFacts(t, filepath.Join(home, ".grok", "sessions", GrokEncodedCwd(cwd), factsID, "summary.json"), body)
	if got := GrokSessionName([]string{"HOME=" + home}, cwd, factsID); got != "" {
		t.Fatalf("oversized %q", got)
	}
}
