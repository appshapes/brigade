//go:build !windows

package hook

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// renameStamp is the fixture session's pending-name file (card 174).
func (f *fixture) renameStamp() string { return config.RenameStamp(f.stateDir, f.pid) }

// plantRename writes the pending name as `brigade rename` would.
func (f *fixture) plantRename(t *testing.T, id, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.renameStamp()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.renameStamp(), []byte(id+" "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// promptOutput is the shape Claude Code reads from a prompt hook.
type promptOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		SessionTitle      string `json:"sessionTitle"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// TestPromptAppliesAPendingName: with a pending name for this
// conversation, the prompt's whole stdout is one JSON document carrying
// the name as sessionTitle and the line it would have printed (the BLANK
// doing reminder of a fresh registration) as additionalContext; the file
// is taken, and the doing stamp is written as on a plain prompt.
func TestPromptAppliesAPendingName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	doingRegistered(t, f)
	f.plantRename(t, f.nativeID, "174-let-a-session-rename-itself")
	exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasPrefix(out, "{") {
		t.Fatalf("stdout is not one JSON line:\n%q", out)
	}
	var got promptOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	h := got.HookSpecificOutput
	if h.HookEventName != "UserPromptSubmit" || h.SessionTitle != "174-let-a-session-rename-itself" {
		t.Fatalf("hookSpecificOutput %+v", h)
	}
	if h.AdditionalContext != doingLineBlank {
		t.Fatalf("additionalContext:\n%q\nwant the BLANK doing line:\n%q", h.AdditionalContext, doingLineBlank)
	}
	if _, err := os.Stat(f.renameStamp()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the pending name was not taken (%v)", err)
	}
	if got := f.readDoingStamp(t); got != f.stampOf(fixedTime) {
		t.Fatalf("doing stamp %q, want %q", got, f.stampOf(fixedTime))
	}
	// Card 164: the map carries the applied name at once — before Claude
	// Code has written its registry — so `brigade whoami` in the turn
	// that follows names the session as the roster will; the next prompt,
	// the registry now naming it as Claude Code wrote it, rewrites
	// nothing.
	if m := f.mustMap(); m.SessionName != "174-let-a-session-rename-itself" {
		t.Fatalf("map after the apply names %q", m.SessionName)
	}
	f.registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, "174-let-a-session-rename-itself", "busy", f.socket)})
	f.deps.Registry = f.registry
	f.now = fixedTime.Add(time.Minute)
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.SessionName != "174-let-a-session-rename-itself" || !m.UpdatedAt.Equal(fixedTime) {
		t.Fatalf("the confirming prompt rewrote the map: name %q, updated_at %v", m.SessionName, m.UpdatedAt)
	}
}

// TestPromptFollowsARenameIntoTheMap (card 164): the map's session_name
// follows Claude Code's session registry — where /rename lands, and what
// the watcher reads and heartbeats to the roster — at the next prompt,
// folded as the heartbeat folds it and with a new updated_at, so `brigade
// whoami`, which reads the map and never the registry, names the session
// as `brigade sessions` does. A prompt that finds the registry unchanged
// rewrites nothing, and a registry name that folds to nothing leaves the
// map's name standing.
func TestPromptFollowsARenameIntoTheMap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, nil)
	// The first prompt settles the permission mode, which the SessionStart
	// document did not carry, so the writes below are the name's alone.
	if exit, out, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
		t.Fatalf("exit %d out %q: %s", exit, out, errOut)
	}
	if m := f.mustMap(); m.SessionName != "payments-api" || !m.UpdatedAt.Equal(fixedTime) {
		t.Fatalf("map before the rename: name %q, updated_at %v", m.SessionName, m.UpdatedAt)
	}
	f.registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, "164-whoami  after\na\trename", "busy", f.socket)})
	f.deps.Registry = f.registry
	renamedAt := fixedTime.Add(time.Minute)
	f.now = renamedAt
	if exit, out, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
		t.Fatalf("exit %d out %q: %s", exit, out, errOut)
	}
	if m := f.mustMap(); m.SessionName != "164-whoami after a rename" || !m.UpdatedAt.Equal(renamedAt) {
		t.Fatalf("map after the rename: name %q, updated_at %v", m.SessionName, m.UpdatedAt)
	}
	f.now = fixedTime.Add(2 * time.Minute)
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.SessionName != "164-whoami after a rename" || !m.UpdatedAt.Equal(renamedAt) {
		t.Fatalf("an unchanged registry rewrote the map: name %q, updated_at %v", m.SessionName, m.UpdatedAt)
	}
	f.registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, " \t\n ", "busy", f.socket)})
	f.deps.Registry = f.registry
	f.now = fixedTime.Add(3 * time.Minute)
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.SessionName != "164-whoami after a rename" || !m.UpdatedAt.Equal(renamedAt) {
		t.Fatalf("a blank registry name changed the map: name %q, updated_at %v", m.SessionName, m.UpdatedAt)
	}
	// With the registry saying nothing, the document's session_title is
	// the host's word (titleOf); with the registry naming the session, the
	// registry wins over the document; and the directory's name is never a
	// prompt's fallback (the cwd is /work/project throughout).
	titled := func(title string) string {
		return f.doc(map[string]any{
			"session_id": f.nativeID, "cwd": "/work/project", "hook_event_name": "UserPromptSubmit",
			"permission_mode": "default", "prompt": "never read", "prompt_id": "p1", "session_title": title,
		})
	}
	documentAt := fixedTime.Add(4 * time.Minute)
	f.now = documentAt
	if exit, _, errOut := f.run(SubPrompt, titled("164-from-the-document")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.SessionName != "164-from-the-document" || !m.UpdatedAt.Equal(documentAt) {
		t.Fatalf("the document's title was not adopted: name %q, updated_at %v", m.SessionName, m.UpdatedAt)
	}
	f.registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, "164-from-the-registry", "busy", f.socket)})
	f.deps.Registry = f.registry
	f.now = fixedTime.Add(5 * time.Minute)
	if exit, _, errOut := f.run(SubPrompt, titled("164-from-the-document")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.SessionName != "164-from-the-registry" {
		t.Fatalf("the registry did not win over the document: name %q", m.SessionName)
	}
}

// TestPromptDropsAPendingNameItMustNotApply: a pending name that names
// another conversation, is already the session's title, is empty, or
// sits in a file the strict reader refuses is taken (removed) and not
// applied: the prompt prints its lines plain, as it does without one.
func TestPromptDropsAPendingNameItMustNotApply(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, f *fixture)
		title string // the session_title the prompt document carries
	}{
		{"another conversation's", func(t *testing.T, f *fixture) {
			t.Helper()
			f.plantRename(t, otherNativeID, "174-the-card")
		}, ""},
		{"already the title", func(t *testing.T, f *fixture) {
			t.Helper()
			f.plantRename(t, f.nativeID, "174-the-card")
		}, "174-the-card"},
		{"empty", func(t *testing.T, f *fixture) {
			t.Helper()
			f.plantRename(t, f.nativeID, "")
		}, ""},
		{"insecure mode", func(t *testing.T, f *fixture) {
			t.Helper()
			f.plantRename(t, f.nativeID, "174-the-card")
			if err := os.Chmod(f.renameStamp(), 0o644); err != nil { //nolint:gosec // G302: the insecure file is the PRECONDITION
				t.Fatal(err)
			}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			doingRegistered(t, f)
			tc.plant(t, f)
			doc := map[string]any{
				"session_id": f.nativeID, "cwd": "/work/project", "hook_event_name": "UserPromptSubmit",
				"permission_mode": "bypassPermissions", "prompt": "never read", "prompt_id": "p1", "transcript_path": "/never/read.jsonl",
			}
			if tc.title != "" {
				doc["session_title"] = tc.title
			}
			exit, out, errOut := f.run(SubPrompt, f.doc(doc))
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if out != doingLineBlank+"\n" {
				t.Fatalf("stdout:\n%q\nwant the plain BLANK line and nothing else", out)
			}
			if _, err := os.Stat(f.renameStamp()); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the pending name was not taken (%v)", err)
			}
		})
	}
}

// TestPromptWithoutAPendingNamePrintsPlain is the common path's control:
// no file, no JSON, no stat beyond the one that finds nothing.
func TestPromptWithoutAPendingNamePrintsPlain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	doingRegistered(t, f)
	exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if out != doingLineBlank+"\n" {
		t.Fatalf("stdout:\n%q", out)
	}
}

// TestMarshalPromptOutputOmitsEmptyContext: a prompt with nothing else to
// say carries the title alone.
func TestMarshalPromptOutputOmitsEmptyContext(t *testing.T) {
	t.Parallel()
	b, err := marshalPromptOutput("174-the-card", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","sessionTitle":"174-the-card"}}` {
		t.Fatalf("json = %s", b)
	}
}
