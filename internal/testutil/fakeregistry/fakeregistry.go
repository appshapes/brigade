// Package fakeregistry is the test double for Claude Code's session
// registry (plan 9.5, A.3): an fstest.MapFS holding <pid>.json entries in
// the observed shape, served through the fs.FS the registry reader takes,
// wrapped in a recorder that FAILS THE TEST when any name ending in .key is
// opened and records every name that was. The reader's contract is "open
// exactly one name, <pid>.json; never list the directory; never touch the
// peer auth key" (6.5), and this fixture is what makes a violation a red
// test rather than a code-review hope.
//
// Every entry is created beside a fake <pid>.<sha>.key file, and one
// stray key exists for a pid with no entry, so there is always something
// to trip over.
package fakeregistry

import (
	"encoding/json/v2"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// KeyContent is the body of every fake peer auth key. It never appears
// in a passing test's observations.
const KeyContent = "FAKE-PEER-AUTH-KEY-MATERIAL-NEVER-READ"

// fakeSHA stands in for the hash in the real key name <pid>.<sha>.key.
const fakeSHA = "0123456789abcdef0123456789abcdef01234567"

// StrayKeyPID is a pid that has a key file but no entry in every recorder.
const StrayKeyPID = 424242

// Example is the A.3 registry entry as observed on 2026-08-30 (pid 632),
// verbatim, for tests that want the real shape rather than a built one.
const Example = `{"pid":632,"sessionId":"beee3690-0000-4000-8000-000000000632","cwd":"/Users/rjae/Development/appshapes/brigade","startedAt":1788089611588,"procStart":"Sun Aug 30 11:33:27 2026","version":"2.1.251","peerProtocol":1,"peerFeatures":["notify_idle","reply_across_default_dirs","artifact_yield"],"kind":"interactive","entrypoint":"cli","pidDomain":"darwin","messagingSocketPath":"/tmp/cc-socks/632.sock","name":"15-create-team-session-messaging","nameSource":"user","nameSince":1788091073682,"status":"busy","updatedAt":1788091073682,"statusUpdatedAt":1788091063200,"bridgeSessionId":"session_0123456789abcdef"}`

// ExamplePID is the pid of Example.
const ExamplePID = 632

// KeyName is the fake peer auth key file name for pid.
func KeyName(pid int) string {
	return strconv.Itoa(pid) + "." + fakeSHA + ".key"
}

// EntryName is the registry entry file name for pid.
func EntryName(pid int) string {
	return strconv.Itoa(pid) + ".json"
}

// observed mirrors the A.3 member order for Observed.
type observed struct {
	PID                 int      `json:"pid"`
	SessionID           string   `json:"sessionId"`
	Cwd                 string   `json:"cwd"`
	StartedAt           int64    `json:"startedAt"`
	ProcStart           string   `json:"procStart"`
	Version             string   `json:"version"`
	PeerProtocol        int      `json:"peerProtocol"`
	PeerFeatures        []string `json:"peerFeatures"`
	Kind                string   `json:"kind"`
	Entrypoint          string   `json:"entrypoint"`
	PIDDomain           string   `json:"pidDomain"`
	MessagingSocketPath string   `json:"messagingSocketPath"`
	Name                string   `json:"name"`
	NameSource          string   `json:"nameSource"`
	NameSince           int64    `json:"nameSince"`
	Status              string   `json:"status"`
	UpdatedAt           int64    `json:"updatedAt"`
	StatusUpdatedAt     int64    `json:"statusUpdatedAt"`
	BridgeSessionID     string   `json:"bridgeSessionId"`
}

// Observed builds an A.3-shaped entry for pid with the given display
// name, status and socket path, every other member filled with plausible
// fixed values. The result is one line of JSON.
func Observed(pid int, name, status, socket string) string {
	o := observed{
		PID:                 pid,
		SessionID:           "beee3690-0000-4000-8000-" + strings.Repeat("0", 12-len(strconv.Itoa(pid))) + strconv.Itoa(pid),
		Cwd:                 "/work/project",
		StartedAt:           1788089611588,
		ProcStart:           "Sun Aug 30 11:33:27 2026",
		Version:             "2.1.259",
		PeerProtocol:        1,
		PeerFeatures:        []string{"notify_idle", "reply_across_default_dirs", "artifact_yield"},
		Kind:                "interactive",
		Entrypoint:          "cli",
		PIDDomain:           "darwin",
		MessagingSocketPath: socket,
		Name:                name,
		NameSource:          "user",
		NameSince:           1788091073682,
		Status:              status,
		UpdatedAt:           1788091073682,
		StatusUpdatedAt:     1788091063200,
		BridgeSessionID:     "session_0123456789abcdef",
	}
	data, err := json.Marshal(&o)
	if err != nil {
		panic("fakeregistry.Observed: " + err.Error())
	}
	return string(data)
}

// A Recorder is the fs.FS the registry reader is handed in tests. It
// serves the entries it was built with and records every Open; opening a
// *.key name, or any name that is not <pid>.json, fails the test that
// built it.
type Recorder struct {
	tb   testing.TB
	fsys fstest.MapFS

	mu     sync.Mutex
	opened []string
}

var entryName = regexp.MustCompile(`^[0-9]+\.json$`)

// New builds a Recorder over entries (pid → the JSON text of that pid's
// <pid>.json), each with a fake key file beside it, plus the stray key
// of StrayKeyPID. tb is captured so a forbidden Open can fail the test
// that constructed the fixture.
func New(tb testing.TB, entries map[int]string) *Recorder {
	tb.Helper()
	m := fstest.MapFS{
		KeyName(StrayKeyPID): &fstest.MapFile{Data: []byte(KeyContent), Mode: 0o600},
	}
	for pid, content := range entries {
		m[EntryName(pid)] = &fstest.MapFile{Data: []byte(content), Mode: 0o600}
		m[KeyName(pid)] = &fstest.MapFile{Data: []byte(KeyContent), Mode: 0o600}
	}
	return &Recorder{tb: tb, fsys: m}
}

// Open implements fs.FS. A missing <pid>.json is an ordinary
// fs.ErrNotExist (a legitimate miss); a *.key name or a name outside the
// <pid>.json contract fails the test and returns fs.ErrPermission.
func (r *Recorder) Open(name string) (fs.File, error) {
	r.tb.Helper()
	r.mu.Lock()
	r.opened = append(r.opened, name)
	r.mu.Unlock()
	switch {
	case strings.HasSuffix(name, ".key"):
		r.tb.Errorf("fakeregistry: Open(%q): a peer auth key was opened; the registry reader must never open *.key (plan 6.5)", name)
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	case !entryName.MatchString(name):
		r.tb.Errorf("fakeregistry: Open(%q): not a <pid>.json name; the reader opens exactly one name and never lists the directory (plan 6.5)", name)
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return r.fsys.Open(name)
}

// Opened returns every name passed to Open, in order.
func (r *Recorder) Opened() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.opened)
}

// A Spy is a testing.TB whose Errorf records instead of failing, so a
// test can prove that the Recorder bites — run a deliberately broken
// reader against a Recorder built on a Spy and assert Failed() — without
// failing itself. Every other method is the real TB's.
type Spy struct {
	testing.TB

	mu       sync.Mutex
	messages []string
}

// NewSpy wraps tb.
func NewSpy(tb testing.TB) *Spy {
	tb.Helper()
	return &Spy{TB: tb}
}

// Errorf records the message and marks the spy failed; the real test is
// untouched.
func (s *Spy) Errorf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, strings.TrimSpace(sprintf(format, args...)))
}

// Failed reports whether Errorf was called.
func (s *Spy) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages) > 0
}

// Messages returns what Errorf recorded.
func (s *Spy) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages)
}
