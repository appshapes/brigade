// Package fakesync is a fake sync adapter for tests of the harness side
// of the sync-adapter protocol (folder-sync plan §4.3, docs/sync-adapters.md):
// a POSIX shell script the test writes into its own temp directory and
// launches as `/bin/sh <script> <verb>` — an argv array, never `sh -c`, and
// the fresh file is READ by the shell rather than exec'd, which is what
// macOS's first-exec assessment and Linux's ETXTBSY need (the
// repository's rule for a script fixture).
//
// The script appends one line per call to its record file — the verb, the
// BRIGADE_STATE_DIR it was given and its stdin request, tab-separated —
// and answers each verb with a canned 4.3 envelope from [Answers]. Nothing
// here ships.
package fakesync

import (
	"bytes"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Answers are the canned results, each the raw JSON of the verb's
// `result` member. A zero member takes the default below; ErrorOn names
// verbs answered with a failing envelope (code `unavailable`, exit 9, the
// message ErrorMessage) instead.
type Answers struct {
	Describe string
	Attach   string
	Apply    string
	Status   string
	Detach   string
	// ErrorOn lists the verbs that fail.
	ErrorOn      []string
	ErrorMessage string
}

// The defaults: a sync/1 adapter named syncthing whose engine's device id
// is SelfPeer, one idle folder and one connected peer.
const (
	SelfPeer        = "SELF-DEVICE-1"
	DefaultDescribe = `{"name":"syncthing","version":"test","protocol_version":"sync/1"}`
	DefaultAttach   = `{"peer":"` + SelfPeer + `"}`
	DefaultApply    = `{"folders":[{"id":"f1","state":"idle"}],"peers":[{"peer":"P1","connected":true}]}`
	DefaultStatus   = `{"running":true,"peer":"` + SelfPeer + `","folders":[],"peers":[]}`
	DefaultDetach   = `{"stopped":true}`
)

// A Record is one call the script received.
type Record struct {
	Verb     string
	StateDir string
	Request  jsontext.Value
}

// A Fake is one written script.
type Fake struct {
	// Argv is the argv prefix a Client runs: /bin/sh and the script.
	Argv []string
	// RecordPath is the file every call is appended to.
	RecordPath string
}

// Write writes the script and its (empty) record file into dir.
func Write(tb testing.TB, dir string, a Answers) *Fake {
	tb.Helper()
	script := filepath.Join(dir, "fake-sync-adapter.sh")
	record := filepath.Join(dir, "fake-sync-record.tsv")
	if err := os.WriteFile(record, nil, 0o600); err != nil {
		tb.Fatal(err)
	}
	failing := map[string]bool{}
	for _, v := range a.ErrorOn {
		failing[v] = true
	}
	msg := a.ErrorMessage
	if msg == "" {
		msg = "the fake sync adapter was told to fail"
	}
	answer := func(verb, result, def string) string {
		if failing[verb] {
			return "  printf '%s\\n' " + quote(`{"ok":false,"protocol_version":"1","error":{"code":"unavailable","message":"`+msg+`","retryable":true}}`) + "\n  exit 9\n"
		}
		if result == "" {
			result = def
		}
		return "  printf '%s\\n' " + quote(`{"ok":true,"protocol_version":"1","result":`+result+`}`) + "\n"
	}
	var b strings.Builder
	b.WriteString("verb=$1\nreq=$(cat)\n")
	b.WriteString("printf '%s\\t%s\\t%s\\n' \"$verb\" \"$BRIGADE_STATE_DIR\" \"$req\" >> " + quote(record) + "\n")
	b.WriteString("case \"$verb\" in\n")
	b.WriteString("describe)\n" + answer("describe", a.Describe, DefaultDescribe) + "  ;;\n")
	b.WriteString("attach)\n" + answer("attach", a.Attach, DefaultAttach) + "  ;;\n")
	b.WriteString("apply)\n" + answer("apply", a.Apply, DefaultApply) + "  ;;\n")
	b.WriteString("status)\n" + answer("status", a.Status, DefaultStatus) + "  ;;\n")
	b.WriteString("detach)\n" + answer("detach", a.Detach, DefaultDetach) + "  ;;\n")
	b.WriteString("*)\n  printf '%s\\n' " + quote(`{"ok":false,"protocol_version":"1","error":{"code":"usage","message":"unknown verb","retryable":false}}`) + "\n  exit 2\n  ;;\nesac\n")
	if err := os.WriteFile(script, []byte(b.String()), 0o600); err != nil {
		tb.Fatal(err)
	}
	return &Fake{Argv: []string{"/bin/sh", script}, RecordPath: record}
}

// quote single-quotes s for the script. The canned JSON and the temp
// paths carry no single quote; one that did would be a test bug.
func quote(s string) string {
	if strings.Contains(s, "'") {
		panic("fakesync: a single quote in a canned value")
	}
	return "'" + s + "'"
}

// Records reads every call recorded so far, in order. A line still being
// appended (no newline yet) is left for the next read.
func (f *Fake) Records(tb testing.TB) []Record {
	tb.Helper()
	data, err := os.ReadFile(f.RecordPath)
	if err != nil {
		tb.Fatal(err)
	}
	complete := data[:bytes.LastIndexByte(data, '\n')+1]
	var out []Record
	for _, line := range strings.Split(strings.TrimSuffix(string(complete), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			tb.Fatalf("fakesync: malformed record line %q", line)
		}
		out = append(out, Record{Verb: parts[0], StateDir: parts[1], Request: jsontext.Value(parts[2])})
	}
	return out
}

// Verbs is the recorded verbs, in order.
func (f *Fake) Verbs(tb testing.TB) []string {
	tb.Helper()
	var out []string
	for _, r := range f.Records(tb) {
		out = append(out, r.Verb)
	}
	return out
}
