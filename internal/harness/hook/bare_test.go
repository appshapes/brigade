package hook

import (
	"io"
	"strings"
	"testing"
	"time"
)

// promptDocument is the document Claude Code hands a prompt hook on stdin.
const promptDocument = `{"session_id":"s-1","transcript_path":"/never/read.jsonl","cwd":"/work","hook_event_name":"UserPromptSubmit","prompt":"hello"}`

// neverEnds is a stdin nobody writes to and nobody closes.
type neverEnds struct{}

func (neverEnds) Read([]byte) (int, error) { select {} }

// staysOpen is a stdin whose host wrote one document and left the pipe
// open: after the document a read blocks for good.
type staysOpen struct{ doc io.Reader }

func (s staysOpen) Read(p []byte) (int, error) {
	if n, err := s.doc.Read(p); n > 0 || err == nil {
		return n, nil
	}
	select {}
}

// TestABareCallCarriesAHooksMarks is work item 402 (GitHub issue 59): a
// `brigade` with no arguments is a hook whose host dropped its words when
// it carries either mark of one, the plugin's root in the environment or
// a hook's document on stdin. The document is told from its start: one
// that `hook prompt` would refuse, and exit 0 on, is a hook's all the same.
func TestABareCallCarriesAHooksMarks(t *testing.T) {
	t.Parallel()
	root := []string{"HOME=/home/x", "CLAUDE_PLUGIN_ROOT=/plugins/brigade"}
	long := strings.Repeat("a", 2*maxStdinBytes)
	for name, tc := range map[string]struct {
		in      io.Reader
		environ []string
	}{
		"the plugin's root in the environment":   {strings.NewReader(""), root},
		"the root and a document":                {strings.NewReader(promptDocument), root},
		"the root with no stdin at all":          {nil, root},
		"the root and a stdin that never closes": {neverEnds{}, root},
		"a hook's document on stdin alone":       {strings.NewReader(promptDocument), []string{"HOME=/home/x"}},
		"a session start's document":             {strings.NewReader(`{"hook_event_name":"SessionStart","source":"startup"}`), nil},
		"a document with a line end after it":    {strings.NewReader(promptDocument + "\n"), nil},
		"a document with space before it":        {strings.NewReader("\n  " + promptDocument), nil},
		"a document whose pipe stays open":       {staysOpen{strings.NewReader(promptDocument)}, nil},
		"a document in a session's shell tool":   {strings.NewReader(promptDocument), []string{"CLAUDE_PID=4242", "CLAUDECODE=1"}},
		"a prompt longer than a hook reads":      {strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"` + long + `"}`), nil},
		"a long prompt before the event":         {strings.NewReader(`{"prompt":"` + long + `","hook_event_name":"UserPromptSubmit"}`), nil},
		"a prompt that is no UTF-8":              {strings.NewReader("{\"prompt\":\"\xff\",\"hook_event_name\":\"UserPromptSubmit\"}"), nil},
		"a lone surrogate in the prompt":         {strings.NewReader(`{"prompt":"\ud800","hook_event_name":"UserPromptSubmit"}`), nil},
		"a name twice before the event":          {strings.NewReader(`{"cwd":"/a","cwd":"/b","hook_event_name":"Stop"}`), nil},
		"an object and a list before the event":  {strings.NewReader(`{"a":{"hook_event_name":""},"b":[1,{"c":2}],"hook_event_name":"Stop"}`), nil},
		"a document cut after its event":         {strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"he`), nil},
	} {
		// The wait is never spent: each of these is answered from what
		// is there. It is long so that a busy runner cannot spend it.
		if !bareCall(tc.in, tc.environ, time.Minute) {
			t.Errorf("%s: taken for no hook's call", name)
		}
	}
}

// TestABareCallWithNoMarkIsNoHooks: with neither mark the call is a
// person's or a model's slip, at a terminal or in a session's shell tool
// (whose environment carries CLAUDE_PID and no plugin root), and it keeps
// its usage error. Another host's plugin root is no mark: those packages'
// hooks are one string or a wrapper script, which no host cuts in two.
func TestABareCallWithNoMarkIsNoHooks(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		in      io.Reader
		environ []string
	}{
		"no stdin and no mark":               {nil, nil},
		"an empty stdin":                     {strings.NewReader(""), nil},
		"a session's shell tool":             {strings.NewReader(""), []string{"CLAUDE_PID=4242", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=s-1"}},
		"an empty plugin root":               {strings.NewReader(""), []string{"CLAUDE_PLUGIN_ROOT="}},
		"Muse Code's plugin root":            {strings.NewReader(""), []string{"MUSE_PLUGIN_ROOT=/plugins/brigade"}},
		"Grok Build's plugin root":           {strings.NewReader(""), []string{"GROK_PLUGIN_ROOT=/plugins/brigade"}},
		"text that is no document":           {strings.NewReader("hello\n"), nil},
		"a JSON object that is no hook's":    {strings.NewReader(`{"prompt":"hello"}`), nil},
		"a hook's event that names nothing":  {strings.NewReader(`{"hook_event_name":""}`), nil},
		"an event that is no text":           {strings.NewReader(`{"hook_event_name":7}`), nil},
		"an event inside another object":     {strings.NewReader(`{"tool_input":{"hook_event_name":"Stop"}}`), nil},
		"a JSON array":                       {strings.NewReader(`["hook_event_name"]`), nil},
		"a text that names the event's name": {strings.NewReader(`"hook_event_name"`), nil},
		"a document cut before its event":    {strings.NewReader(`{"session_id":"s-1","cwd":"/wo`), nil},
		"a stdin that nobody writes to":      {neverEnds{}, nil},
	} {
		// Only the last one spends the wait.
		if bareCall(tc.in, tc.environ, 50*time.Millisecond) {
			t.Errorf("%s: taken for a hook's call", name)
		}
	}
}

// TestTheBareCallsWaitStaysUnderAHooksTimeout: the prompt and stop hooks
// are given five seconds (plugin/hooks/hooks.json), and a bare call that
// waits for its document must have answered well before a host ends it.
func TestTheBareCallsWaitStaysUnderAHooksTimeout(t *testing.T) {
	t.Parallel()
	if bareCallWait <= 0 || bareCallWait >= 5*time.Second {
		t.Fatalf("bareCallWait = %s, want above 0 and under the hooks' 5 s", bareCallWait)
	}
}
