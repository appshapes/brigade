//go:build !windows

package hook

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/testutil"
)

// agyFixture is a session of Antigravity CLI (work item 532): its options
// in antigravity-options.json, as a Grok Build session's are in its file.
func agyFixture(t *testing.T) *fixture {
	t.Helper()
	f := buildFixture(t, config.HostAntigravity)
	f.deps.ReadFile = os.ReadFile
	f.spawner.watcherPID = testutil.NewSleeper(t)
	body, _ := json.Marshal(map[string]string{"adapter_command": f.adapterOption(), "sync": "off"})
	if err := os.MkdirAll(f.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.configDir, config.AntigravityOptionsFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// agyDoc is a hook document as agy 1.5.18 writes it (work item 531): camelCase,
// the conversation's id, the model, and per event invocationNum (PreInvocation)
// or fullyIdle and continuationActive (Stop).
func agyDoc(f *fixture, event string, extra map[string]any) string {
	d := map[string]any{
		"conversationId": f.nativeID, "cwd": f.cwd, "workspacePaths": []string{f.cwd}, "hookEventName": event,
		"modelName": "gemini-4-hydrogen-high", "transcriptPath": "/never/read/transcript.jsonl", "lastUserInput": "never read",
	}
	for k, v := range extra {
		d[k] = v
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// agyRun runs a hook as agy's hooks.json does: the package's bootstrap with
// --host antigravity and --plugin-root, the conversation's id as the one
// ANTIGRAVITY_* variable of the environment.
func agyRun(f *fixture, sub, doc string) (int, string, string) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	env := []string{"HOME=" + f.dirs.Home, "PATH=/usr/bin:/bin", config.AntigravitySessionIDVar + "=" + f.nativeID}
	f.writeScript()
	code := Run([]string{sub, "--host", "antigravity", "--plugin-root", f.dirs.PluginRoot},
		cli.Streams{In: strings.NewReader(doc), Out: &out, Err: &errOut}, env, f.deps)
	return code, out.String(), errOut.String()
}

// agyAnswer decodes the one JSON document a hook printed, and fails on
// anything else: agy takes nothing but JSON from a hook.
func agyAnswer(t *testing.T, out string) map[string]any {
	t.Helper()
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("stdout is not one line: %q", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not JSON: %q", out)
	}
	return m
}

// injected is the text of the user messages an answer adds, one after the
// other, "" for none. It fails on a system message: nothing a hook adds to
// an agy conversation is one (work item 532, the owner's review).
func injected(t *testing.T, m map[string]any) string {
	t.Helper()
	steps, _ := m["injectSteps"].([]any)
	var texts []string
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		u, ok := step["userMessage"].(string)
		if len(step) != 1 || !ok {
			t.Fatalf("a step that is not one user message: %v", raw)
		}
		texts = append(texts, u)
	}
	return strings.Join(texts, "\n")
}

// TestAntigravitySessionStart: the conversation is the session's key, the
// agy process is kept for liveness, the model comes from the document,
// no transcript path is kept (agy's is not Claude Code's), the watcher is
// spawned with a sink, and the answer is one JSON document whose user
// message is the context line and the line on how a message arrives.
func TestAntigravitySessionStart(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	exit, out, errOut := agyRun(f, SubSessionStart, agyDoc(f, "SessionStart", nil))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	text := injected(t, agyAnswer(t, out))
	if strings.Contains(text, " follows (") {
		t.Fatalf("a start with no message waiting hands one over: %q", text)
	}
	if !strings.Contains(text, "Brigade: this session is") || !strings.Contains(text, "before its next model call") ||
		!strings.Contains(text, f.pluginBin+" send") {
		t.Fatalf("the start's message %q", text)
	}
	m := f.mustMap()
	key := conversationKey(f.nativeID)
	if key == codexKey(f.nativeID) || key < 1<<30+1<<29 || key >= 1<<31 {
		t.Fatalf("conversationKey %d is not in a range of its own above every pid", key)
	}
	switch {
	case m.Harness != string(config.HostAntigravity), m.Model != "gemini-4-hydrogen-high", m.ClaudePID != key,
		m.HostProcessPID != f.pid, m.LivenessPID() != f.pid, m.ClaudeSessionID != f.nativeID, m.TranscriptPath != "",
		m.PluginBin != f.pluginBin:
		t.Fatalf("map %+v", *m)
	}
	ptr, err := f.store().ReadByHost(string(config.HostAntigravity), f.nativeID)
	if err != nil || ptr.HostPID != key {
		t.Fatalf("by-host pointer %+v %v", ptr, err)
	}
	spec := f.spawner.last(t)
	// As Muse Code's: the watcher learns the host from the map, and writes
	// the sink the hook reads.
	if len(spec.Args) != 3 || spec.Args[0] != "watch" || spec.Args[1] != "--sink" || !filepath.IsAbs(spec.Args[2]) {
		t.Fatalf("watcher args %q, want watch --sink <file>", spec.Args)
	}
}

// TestAntigravityPreInvocationHandsOverWaitingMessages: a message the
// watcher put in the sink reaches the model at the next model call, once;
// a later call with nothing new answers "{}"; a call after the first of a
// turn does not run the prompt hook again.
func TestAntigravityPreInvocationHandsOverWaitingMessages(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	if exit, out, errOut := agyRun(f, SubSessionStart, agyDoc(f, "SessionStart", nil)); exit != 0 {
		t.Fatalf("start exit %d: %s %s", exit, out, errOut)
	}
	m := f.mustMap()
	sink := inbound.NewestSink(f.stateDir, m.BrigadeSessionID)
	if sink == "" {
		sink = inbound.NewSinkPath(f.stateDir, m.BrigadeSessionID, time.Now())
	}
	rec, _ := json.Marshal(inbound.SinkRecord{TS: time.Now().UTC(), Frame: "<brigade-message>hello</brigade-message>", MessageID: "msg-1"})
	if err := os.MkdirAll(filepath.Dir(sink), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sink, append(rec, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, out, errOut := agyRun(f, SubPrompt, agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 2, "initialNumSteps": 9}))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	// A teammate's message is a user message, never the system's words.
	if text := injected(t, agyAnswer(t, out)); !strings.Contains(text, "team message msg-1 follows") || !strings.Contains(text, "hello") {
		t.Fatalf("the call's message %q", text)
	}
	exit, out, _ = agyRun(f, SubPrompt, agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 3}))
	if exit != 0 || strings.TrimSpace(out) != "{}" {
		t.Fatalf("a call with nothing new: exit %d, %q", exit, out)
	}
}

// TestAntigravityStopGoesOnWithAMessageThatArrived: a message that arrived
// while the turn ran makes the turn go on with a line that says so; the
// message itself follows at the next model call, as a user message, since
// a Stop answer's reason is a system message. With nothing to say, the
// answer is "{}".
func TestAntigravityStopGoesOnWithAMessageThatArrived(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	if exit, _, errOut := agyRun(f, SubSessionStart, agyDoc(f, "SessionStart", nil)); exit != 0 {
		t.Fatalf("start: %s", errOut)
	}
	stop := agyDoc(f, "Stop", map[string]any{"fullyIdle": true, "terminationReason": "NO_TOOL_CALL", "executionNum": 1})
	exit, out, _ := agyRun(f, SubStop, stop)
	if exit != 0 || strings.TrimSpace(out) != "{}" {
		t.Fatalf("an empty stop: exit %d, %q", exit, out)
	}
	m := f.mustMap()
	sink := inbound.NewestSink(f.stateDir, m.BrigadeSessionID)
	if sink == "" {
		sink = inbound.NewSinkPath(f.stateDir, m.BrigadeSessionID, time.Now())
	}
	rec, _ := json.Marshal(inbound.SinkRecord{TS: time.Now().UTC(), Frame: "late news", MessageID: "msg-2"})
	if err := os.MkdirAll(filepath.Dir(sink), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sink, append(rec, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, out, _ = agyRun(f, SubStop, stop)
	answer := agyAnswer(t, out)
	reason, _ := answer["reason"].(string)
	if exit != 0 || answer["decision"] != "continue" || !strings.Contains(reason, messagesWaitLine) || strings.Contains(reason, "late news") {
		t.Fatalf("stop with a message: exit %d, %v", exit, answer)
	}
	exit, out, _ = agyRun(f, SubPrompt, agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 4}))
	if user := injected(t, agyAnswer(t, out)); exit != 0 || !strings.Contains(user, "late news") {
		t.Fatalf("the call after the stop: exit %d, %q", exit, out)
	}
}

// TestAntigravityAnswersJSONOnEveryFailure: bad stdin, and a document that
// names another conversation than the environment, are the empty answer:
// a hook never prints text agy cannot read.
func TestAntigravityAnswersJSONOnEveryFailure(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	for _, doc := range []string{"", "not json", `{"conversationId":"another-conversation-id"}`} {
		for _, sub := range []string{SubSessionStart, SubPrompt, SubStop} {
			exit, out, _ := agyRun(f, sub, doc)
			if exit != 0 {
				t.Fatalf("%s %q: exit %d", sub, doc, exit)
			}
			if m := agyAnswer(t, out); len(m) != 0 && sub != SubSessionStart {
				t.Fatalf("%s %q: %v", sub, doc, m)
			}
		}
	}
}

// seedSink puts one record in the session's sink, as the watcher would.
func seedSink(t *testing.T, f *fixture, id, frame string) {
	t.Helper()
	m := f.mustMap()
	sink := inbound.NewestSink(f.stateDir, m.BrigadeSessionID)
	if sink == "" {
		sink = inbound.NewSinkPath(f.stateDir, m.BrigadeSessionID, time.Now())
	}
	rec, _ := json.Marshal(inbound.SinkRecord{TS: time.Now().UTC(), Frame: frame, MessageID: id})
	if err := os.MkdirAll(filepath.Dir(sink), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(sink, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(rec, '\n')); err != nil {
		t.Fatal(err)
	}
}

// TestAntigravityTakesNoMessageForAnotherConversation: a document that
// names another conversation than the hook's environment takes none of
// this session's messages; they wait for a call that is this session's
// (review of work item 532).
func TestAntigravityTakesNoMessageForAnotherConversation(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	if exit, _, errOut := agyRun(f, SubSessionStart, agyDoc(f, "SessionStart", nil)); exit != 0 {
		t.Fatalf("start: %s", errOut)
	}
	seedSink(t, f, "msg-9", "kept for its own session")
	for _, doc := range []string{
		`{"conversationId":"0199c0de-0000-7000-8000-00000000beef","hookEventName":"PreInvocation","invocationNum":0}`,
		`{"conversationId":"0199c0de-0000-7000-8000-00000000beef","hookEventName":"PreInvocation","invocationNum":3}`,
	} {
		_, out, _ := agyRun(f, SubPrompt, doc)
		if user := injected(t, agyAnswer(t, out)); strings.Contains(user, "kept for its own session") {
			t.Fatalf("another conversation's call took %q", user)
		}
	}
	_, out, _ := agyRun(f, SubPrompt, agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 1}))
	if user := injected(t, agyAnswer(t, out)); !strings.Contains(user, "kept for its own session") {
		t.Fatalf("the session's own call: %q", out)
	}
}

// failingWriter is a stdout whose reader is gone.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// TestAntigravityAnswerNotWrittenLeavesTheMessages: an answer that could
// not be written moves no cursor, so its messages come at the next call.
func TestAntigravityAnswerNotWrittenLeavesTheMessages(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	if exit, _, errOut := agyRun(f, SubSessionStart, agyDoc(f, "SessionStart", nil)); exit != 0 {
		t.Fatalf("start: %s", errOut)
	}
	seedSink(t, f, "msg-7", "not lost")
	var errOut bytes.Buffer
	env := []string{"HOME=" + f.dirs.Home, "PATH=/usr/bin:/bin", config.AntigravitySessionIDVar + "=" + f.nativeID}
	code := Run([]string{SubPrompt, "--host", "antigravity", "--plugin-root", f.dirs.PluginRoot},
		cli.Streams{In: strings.NewReader(agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 2})), Out: failingWriter{}, Err: &errOut}, env, f.deps)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	_, out, _ := agyRun(f, SubPrompt, agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 3}))
	if user := injected(t, agyAnswer(t, out)); !strings.Contains(user, "not lost") {
		t.Fatalf("after a failed write: %q", out)
	}
}

// TestAntigravityStopDoesNotKeepTheSessionTurning: a turn that a Stop
// answer already continued is not told again that a message waits, and a
// line that is no record is passed over at the next call, so it is not new
// at every Stop (code review of work item 532).
func TestAntigravityStopDoesNotKeepTheSessionTurning(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	if exit, _, errOut := agyRun(f, SubSessionStart, agyDoc(f, "SessionStart", nil)); exit != 0 {
		t.Fatalf("start: %s", errOut)
	}
	seedSink(t, f, "msg-1", "waits")
	_, out, _ := agyRun(f, SubStop, agyDoc(f, "Stop", map[string]any{"fullyIdle": true, "continuationActive": true}))
	if strings.TrimSpace(out) != "{}" {
		t.Fatalf("a continued turn's stop: %q", out)
	}
	_, _, _ = agyRun(f, SubPrompt, agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 1}))
	m := f.mustMap()
	sink := inbound.NewestSink(f.stateDir, m.BrigadeSessionID)
	file, err := os.OpenFile(sink, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("\n")
	_ = file.Close()
	_, out, _ = agyRun(f, SubPrompt, agyDoc(f, "PreInvocation", map[string]any{"invocationNum": 2}))
	if strings.TrimSpace(out) != "{}" {
		t.Fatalf("a call with only an empty line: %q", out)
	}
	_, out, _ = agyRun(f, SubStop, agyDoc(f, "Stop", map[string]any{"fullyIdle": true}))
	if strings.TrimSpace(out) != "{}" {
		t.Fatalf("a stop after the empty line was passed over: %q", out)
	}
}

// TestAntigravityRefusesACompetingMarker: an agy started from another
// host's shell inherits that host's marker; its hook takes no session,
// rewrites no other host's map, and says so at the start (Grok review of
// work item 532).
func TestAntigravityRefusesACompetingMarker(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	var out, errOut bytes.Buffer
	env := []string{"HOME=" + f.dirs.Home, "PATH=/usr/bin:/bin", config.AntigravitySessionIDVar + "=" + f.nativeID, "CODEX_THREAD_ID=0199c0de-0000-7000-8000-000000000042"}
	f.writeScript()
	code := Run([]string{SubSessionStart, "--host", "antigravity", "--plugin-root", f.dirs.PluginRoot},
		cli.Streams{In: strings.NewReader(agyDoc(f, "SessionStart", nil)), Out: &out, Err: &errOut}, env, f.deps)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if text := injected(t, agyAnswer(t, out.String())); text != competingRefusal {
		t.Fatalf("start under a competing marker: %q", out.String())
	}
	if f.spawner.count() != 0 {
		t.Fatal("a watcher was spawned under a competing marker")
	}
	if _, err := f.store().ReadByPID(codexKey("0199c0de-0000-7000-8000-000000000042")); err == nil {
		t.Fatal("the Codex thread's map was written")
	}
}

// TestACodexHookUnderAnInheritedAgyMarkerIsCodexs: a Codex CLI started
// from an agy agent's command inherits ANTIGRAVITY_CONVERSATION_ID; its
// hook names --host codex, and it registers as Codex, as on master (Rjae's
// ruling of 2026-11-27, work item 532).
func TestACodexHookUnderAnInheritedAgyMarkerIsCodexs(t *testing.T) {
	t.Parallel()
	f := newCodexFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	exit, out, errOut := f.run(SubSessionStart, f.codexDoc("SessionStart", "startup"),
		"CODEX_HOME="+f.dirs.Home+"/.codex", config.AntigravitySessionIDVar+"=0199c0de-0000-7000-8000-0000000a9a9a")
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if strings.Contains(out, "injectSteps") {
		t.Fatalf("a Codex hook answered in agy's dialect: %q", out)
	}
	if m := f.mustMap(); m.Harness != string(config.HostCodex) {
		t.Fatalf("registered as %q, want codex", m.Harness)
	}
}

// TestAntigravityStopGoesOnOnlyAfterATurnThatEndedByItself: after an
// error, max_steps_exceeded or a cancel, a Stop does not make the turn go
// on, though a message waits; after NO_TOOL_CALL it does (the owner's
// review of work item 532).
func TestAntigravityStopGoesOnOnlyAfterATurnThatEndedByItself(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	if exit, _, errOut := agyRun(f, SubSessionStart, agyDoc(f, "SessionStart", nil)); exit != 0 {
		t.Fatalf("start: %s", errOut)
	}
	seedSink(t, f, "msg-3", "waits")
	for _, extra := range []map[string]any{
		{"terminationReason": "error", "error": "the model failed"},
		{"terminationReason": "NO_TOOL_CALL", "error": "a late error"},
		{"terminationReason": "max_steps_exceeded"},
		{"terminationReason": "user_cancelled"},
	} {
		if _, out, _ := agyRun(f, SubStop, agyDoc(f, "Stop", extra)); strings.TrimSpace(out) != "{}" {
			t.Fatalf("stop after %v: %q", extra, out)
		}
	}
	_, out, _ := agyRun(f, SubStop, agyDoc(f, "Stop", map[string]any{"terminationReason": "NO_TOOL_CALL"}))
	if answer := agyAnswer(t, out); answer["decision"] != "continue" || answer["reason"] != messagesWaitLine {
		t.Fatalf("stop after a turn that ended by itself: %v", answer)
	}
}

// TestAntigravityStartWithWorkspacePathsAlone: a document with
// workspacePaths and no cwd registers the session from the first
// workspace (the owner's review of work item 532).
func TestAntigravityStartWithWorkspacePathsAlone(t *testing.T) {
	t.Parallel()
	f := agyFixture(t)
	doc := map[string]any{}
	if err := json.Unmarshal([]byte(agyDoc(f, "SessionStart", nil)), &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "cwd")
	b, _ := json.Marshal(doc)
	if exit, out, errOut := agyRun(f, SubSessionStart, string(b)); exit != 0 || !strings.Contains(injected(t, agyAnswer(t, out)), "Brigade: this session is") {
		t.Fatalf("start with workspacePaths alone: exit %d, %q\n%s", exit, out, errOut)
	}
	if m := f.mustMap(); m.Harness != string(config.HostAntigravity) {
		t.Fatalf("map %+v", *m)
	}
}
