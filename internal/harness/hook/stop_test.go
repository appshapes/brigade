//go:build !windows

package hook

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// stopDoc is a Stop document (card 162): Claude Code's own spelling of
// the flag, and the permission mode the turn ran in.
func (f *fixture) stopDoc(mode string, active bool) string {
	return f.doc(map[string]any{
		"session_id": f.nativeID, "cwd": f.cwd, "hook_event_name": "Stop", "permission_mode": mode,
		"stop_hook_active": active, "last_assistant_message": "never read", "transcript_path": "/never/read.jsonl",
	})
}

// blockReason is the reason of the block answer on stdout, "" for no
// answer; anything else on stdout fails the test.
func blockReason(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	var doc struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Decision != "block" || doc.Reason == "" || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout %q is not one block answer: %v", out, err)
	}
	return doc.Reason
}

// feedbackContext is the additionalContext of the feedback answer on
// stdout (card 186), "" for no answer; anything else on stdout — the block
// answer included — fails the test.
func feedbackContext(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	var doc map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc) != 1 || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout %q is not one feedback answer: %v", out, err)
	}
	specific := doc["hookSpecificOutput"]
	if len(specific) != 2 || specific["hookEventName"] != "Stop" || specific["additionalContext"] == "" {
		t.Fatalf("stdout %q is not one feedback answer", out)
	}
	return specific["additionalContext"]
}

// stateSession is a registered session in a project with a state folder,
// a doing mode that may print (quiet), and the entry written by one
// prompt at fixedTime in the given permission mode.
func stateSession(t *testing.T, f *fixture, mode string) state.Store {
	t.Helper()
	f.spawner.watcherPID = testutil.NewSleeper(t)
	f.writeTeamFile(stateMember)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	seam.describe = describeWithDescription()
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("session-start: exit %d: %s", exit, errOut)
	}
	// The prompt carries the permission mode the map keeps.
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc(mode)); exit != 0 {
		t.Fatalf("prompt: exit %d: %s", exit, errOut)
	}
	return f.stateStore(t)
}

// stopStamp reads the fixture's stop stamp, "" for none.
func (f *fixture) stopStamp(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(stopStampPath(f.stateDir, f.pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// TestStopAsksForANoteAfterACommit (card 162, plan §5.3 gate 1): a turn
// that made a commit with no note after it is asked once for a note, in
// the feedback answer (the fixture's registry says 2.1.259) with the
// constant's sentence; the Stop that follows
// the ask (stop_hook_active) asks nothing; a Stop within ten minutes asks
// nothing (never twice in a row, once per ten minutes); the same commit
// is never asked twice; a commit with a note after it is not asked. The
// commits carry git's clock, so the hook's clock runs from the real time
// here, and the doing line is kept fresh so its own gate stays shut.
func TestStopAsksForANoteAfterACommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := os.RemoveAll(f.cwd + "/.git"); err != nil {
		t.Fatal(err)
	}
	f.gitIn("init", "-q", "-b", "master")
	f.gitIn("config", "user.email", "me@example.com")
	f.gitIn("config", "user.name", "Me")
	f.gitIn("commit", "-q", "--allow-empty", "-m", "0: before")
	path := "PATH=" + gitPath(t)
	s := stateSession(t, f, "bypassPermissions")
	base := time.Now().UTC().Truncate(time.Second)
	refreshDoing := func(at time.Time) {
		t.Helper()
		e, err := s.Load("brigade-sess-1")
		if err != nil {
			t.Fatal(err)
		}
		e.AddDoing("writing the Stop hook", at)
		if err := s.Save(e); err != nil {
			t.Fatal(err)
		}
	}
	refreshDoing(base)
	f.now = base
	if exit, _, _ := f.run(SubPrompt, f.promptDoc("bypassPermissions"), path); exit != 0 {
		t.Fatal(exit)
	}
	// A quiet turn: nothing to ask, the stamp says so.
	f.now = base.Add(30 * time.Second)
	exit, out, errOut := f.run(SubStop, f.stopDoc("bypassPermissions", false), path)
	if exit != 0 || out != "" {
		t.Fatalf("exit %d out %q err %q", exit, out, errOut)
	}
	if got := f.stopStamp(t); !strings.HasSuffix(got, " "+f.nativeID+" quiet") {
		t.Fatalf("stamp after a quiet stop: %q", got)
	}
	f.gitIn("commit", "-q", "--allow-empty", "-m", "162: Add the Stop hook")
	f.now = base.Add(40 * time.Second)
	_, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", false), path)
	if got := feedbackContext(t, out); got != stopAskOpen+stopAskNote+stopAskClose {
		t.Fatalf("reason = %q", got)
	}
	if got := f.stopStamp(t); got != f.now.Format(time.RFC3339Nano)+" "+f.nativeID+" asked" {
		t.Fatalf("stamp after the ask: %q", got)
	}
	e, err := s.Load("brigade-sess-1")
	if err != nil || len(e.Commits) != 1 || e.Commits[0].Card != 162 {
		t.Fatalf("the Stop did not refresh the commits: %+v %v", e, err)
	}
	// The continuation's Stop: the active flag, nothing asked, the stamp kept.
	f.now = base.Add(50 * time.Second)
	if _, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", true), path); out != "" {
		t.Fatalf("the active Stop asked: %q", out)
	}
	if got := f.stopStamp(t); !strings.HasSuffix(got, " asked") {
		t.Fatalf("the active Stop touched the stamp: %q", got)
	}
	// The next real Stop, a minute on: never twice in a row.
	f.now = base.Add(2 * time.Minute)
	if _, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", false), path); out != "" {
		t.Fatalf("asked twice in a row: %q", out)
	}
	if got := f.stopStamp(t); got != base.Add(40*time.Second).Format(time.RFC3339Nano)+" "+f.nativeID+" quiet" {
		t.Fatalf("stamp after the quiet stop keeps the ask time: %q", got)
	}
	// Twelve minutes on, still no note: the same commit is not asked twice.
	refreshDoing(base.Add(11 * time.Minute))
	f.now = base.Add(12 * time.Minute)
	if _, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", false), path); out != "" {
		t.Fatalf("the same commit was asked again: %q", out)
	}
	// A new commit, a note after it: nothing to ask.
	f.gitIn("commit", "-q", "--allow-empty", "-m", "162: Record the note")
	e, _ = s.Load("brigade-sess-1")
	e.AddNote("Done: the Stop hook", base.Add(20*time.Minute))
	e.AddDoing("recording", base.Add(24*time.Minute))
	if err := s.Save(e); err != nil {
		t.Fatal(err)
	}
	f.now = base.Add(25 * time.Minute)
	if _, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", false), path); out != "" {
		t.Fatalf("asked with a note after the commit: %q", out)
	}
	if e, _ = s.Load("brigade-sess-1"); len(e.Commits) != 2 {
		t.Fatalf("the quiet Stop did not refresh the commits: %+v", e.Commits)
	}
}

// TestStopAsksAfterALongTurn (gates 2 and 3): after a turn longer than a
// minute with no doing line published, the doing sentence; with anyone
// subscribed to the digest, the decision sentence too; a turn under a minute
// asks neither; a fresh doing line silences the doing sentence.
func TestStopAsksAfterALongTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	s := stateSession(t, f, "bypassPermissions")
	f.now = fixedTime.Add(30 * time.Second)
	if _, out, _ := f.run(SubStop, f.stopDoc("bypassPermissions", false)); out != "" {
		t.Fatalf("a short turn asked: %q", out)
	}
	f.now = fixedTime.Add(12 * time.Minute)
	_, out, _ := f.run(SubStop, f.stopDoc("bypassPermissions", false))
	if got := feedbackContext(t, out); got != stopAskOpen+stopAskDoing+stopAskClose {
		t.Fatalf("reason = %q", got)
	}
	// Someone subscribed to the digest, and the ask interval passed.
	if err := os.WriteFile(state.ReportsPath(s.Folder), []byte(`{"version":2,"subscribers":[{"label":"rjae@example.com","recipients":["@rjae"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.now = fixedTime.Add(23 * time.Minute)
	if _, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", false)); out != "" {
		t.Fatalf("asked twice in a row: %q", out)
	}
	f.now = fixedTime.Add(24 * time.Minute)
	_, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", false))
	if got := feedbackContext(t, out); got != stopAskOpen+stopAskDoing+" "+stopAskDecision+stopAskClose {
		t.Fatalf("reason = %q", got)
	}
	// A fresh doing line: the decision alone.
	e, _ := s.Load("brigade-sess-1")
	e.AddDoing("writing the Stop hook", fixedTime.Add(30*time.Minute))
	if err := s.Save(e); err != nil {
		t.Fatal(err)
	}
	f.now = fixedTime.Add(35 * time.Minute)
	f.run(SubStop, f.stopDoc("bypassPermissions", false)) // the quiet one between
	f.now = fixedTime.Add(36 * time.Minute)
	_, out, _ = f.run(SubStop, f.stopDoc("bypassPermissions", false))
	if got := feedbackContext(t, out); got != stopAskOpen+stopAskDecision+stopAskClose {
		t.Fatalf("reason = %q", got)
	}
}

// TestStopGates: the gates that hold whatever the facts say — the
// `status` option off, a quiet session, an ineligible permission mode, a
// doing mode that never prints, a non-interactive session, another
// conversation's document, no state folder — each ask nothing and leave
// no stamp, with a long turn and no doing line that would otherwise ask.
func TestStopGates(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		env     []string
		mode    string
		arrange func(t *testing.T, f *fixture)
		doc     func(f *fixture) string
		noState bool
	}{
		"status off":          {env: []string{config.OptionStatus + "=off"}, mode: "bypassPermissions"},
		"status neither word": {env: []string{config.OptionStatus + "=maybe"}, mode: "bypassPermissions"},
		"a quiet session": {mode: "bypassPermissions", arrange: func(t *testing.T, f *fixture) {
			t.Helper()
			m := f.mustMap()
			m.SetInboundRequest("hold")
			writeMapOf(t, f, m)
		}},
		"plan mode":                {mode: "plan"},
		"default mode under quiet": {mode: "default"},
		"doing mode unasked":       {mode: "bypassPermissions", arrange: func(t *testing.T, f *fixture) { t.Helper(); f.setDoingMode(t, doing.ModeUnasked) }},
		"doing mode off":           {mode: "bypassPermissions", arrange: func(t *testing.T, f *fixture) { t.Helper(); f.setDoingMode(t, doing.ModeOff) }},
		"non-interactive": {mode: "bypassPermissions", arrange: func(t *testing.T, f *fixture) {
			t.Helper()
			m := f.mustMap()
			m.NonInteractive = true
			writeMapOf(t, f, m)
		}},
		"another conversation": {mode: "bypassPermissions", doc: func(f *fixture) string {
			return f.doc(map[string]any{"session_id": "other-conversation", "cwd": f.cwd, "hook_event_name": "Stop", "permission_mode": "bypassPermissions", "stop_hook_active": false})
		}},
		"no state folder": {mode: "bypassPermissions", noState: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.noState {
				f.spawner.watcherPID = testutil.NewSleeper(t)
				seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
				seam.describe = describeWithDescription()
				if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
					t.Fatalf("exit %d: %s", exit, errOut)
				}
				if exit, _, _ := f.run(SubPrompt, f.promptDoc(tc.mode)); exit != 0 {
					t.Fatal(exit)
				}
			} else {
				stateSession(t, f, tc.mode)
			}
			if tc.arrange != nil {
				tc.arrange(t, f)
			}
			doc := f.stopDoc(tc.mode, false)
			if tc.doc != nil {
				doc = tc.doc(f)
			}
			f.now = fixedTime.Add(12 * time.Minute)
			exit, out, errOut := f.run(SubStop, doc, tc.env...)
			if exit != 0 || out != "" {
				t.Fatalf("exit %d out %q err %q", exit, out, errOut)
			}
			if got := f.stopStamp(t); got != "" {
				t.Fatalf("a gated Stop wrote a stamp: %q", got)
			}
		})
	}
	// The positive control: the same session, no gate, asks.
	t.Run("control", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		stateSession(t, f, "bypassPermissions")
		f.now = fixedTime.Add(12 * time.Minute)
		_, out, _ := f.run(SubStop, f.stopDoc("bypassPermissions", false))
		if feedbackContext(t, out) == "" {
			t.Fatal("the control asked nothing")
		}
	})
}

// writeMapOf rewrites the fixture's map.
func writeMapOf(t *testing.T, f *fixture, m *sessionmap.ByPID) {
	t.Helper()
	if err := f.store().WriteByPID(m); err != nil {
		t.Fatal(err)
	}
}

// TestStopAllowedModePrintsInDefault: under an allow rule that covers the
// verb (doing mode allowed) the ask comes in default mode too, as the
// doing reminder does.
func TestStopAllowedModePrintsInDefault(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	stateSession(t, f, "default")
	f.setDoingMode(t, doing.ModeAllowed)
	f.now = fixedTime.Add(12 * time.Minute)
	_, out, _ := f.run(SubStop, f.stopDoc("default", false))
	if got := feedbackContext(t, out); got != stopAskOpen+stopAskDoing+stopAskClose {
		t.Fatalf("reason = %q", got)
	}
}

// TestPromptCarriesTheAskOnAPointedHost (the parity rule): a Muse Code
// session, which has no Stop hook, gets the same ask as a context line at
// its next prompt, judged on the turn that ended there, under the same
// stamp; a Claude Code prompt never prints it.
func TestPromptCarriesTheAskOnAPointedHost(t *testing.T) {
	t.Parallel()
	f := newMuseFixture(t)
	f.writeTeamFile(stateMember)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	seam.describe = describeWithDescription()
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("bypassPermissions")); exit != 0 || strings.Contains(out, stopAskOpen) {
		t.Fatalf("the first prompt asked: exit %d out %q", exit, out)
	}
	f.now = fixedTime.Add(12 * time.Minute)
	exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if !strings.Contains(out, stopAskOpen+stopAskDoing+stopAskClose) {
		t.Fatalf("the long turn's prompt did not carry the ask:\n%s", out)
	}
	if got := f.stopStamp(t); !strings.HasSuffix(got, " asked") {
		t.Fatalf("stamp %q", got)
	}
	f.now = fixedTime.Add(13 * time.Minute)
	if _, out, _ = f.run(SubPrompt, f.promptDoc("bypassPermissions")); strings.Contains(out, stopAskOpen) {
		t.Fatalf("asked twice in a row: %q", out)
	}
	// A Claude Code session's prompt never carries it: its Stop hook does.
	g := newFixture(t)
	stateSession(t, g, "bypassPermissions")
	g.now = fixedTime.Add(12 * time.Minute)
	if _, out, _ := g.run(SubPrompt, g.promptDoc("bypassPermissions")); strings.Contains(out, stopAskOpen) {
		t.Fatalf("a Claude Code prompt carried the ask: %q", out)
	}
}

// TestGrokStopCarriesTheAsk: Grok's Stop hook, which arms the monitor
// first, asks for the state once nothing is left to arm — the same
// sentence, the same stamp — and the arming ask takes precedence on the
// first completed turn.
func TestGrokStopCarriesTheAsk(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	f.writeTeamFile(stateMember)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	seam.describe = describeWithDescription()
	// The Grok fixture's options file switches sync off; the state folder
	// rides on sync, so this session's file says on.
	body, _ := json.Marshal(map[string]string{"adapter_command": f.adapterOption(), "sync": "on"})
	if err := os.WriteFile(filepath.Join(f.configDir, config.GrokOptionsFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if exit, _, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", map[string]any{"permissionMode": "bypassPermissions"})); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if exit, _, errOut := grokRun(f, SubPrompt, grokDoc(f, "user_prompt_submit", "", map[string]any{"permissionMode": "bypassPermissions"})); exit != 0 {
		t.Fatalf("prompt: exit %d: %s", exit, errOut)
	}
	if _, err := f.stateStore(t).Load("brigade-sess-1"); err != nil {
		t.Fatalf("no entry after the Grok prompt: %v", err)
	}
	// Grok keeps the block answer for both asks (card 186), whatever a
	// Claude Code session registry at its pid says: the fixture's says
	// 2.1.259, a Claude Code that takes the feedback answer.
	// The first completed turn arms the monitor; the state ask yields.
	f.now = fixedTime.Add(12 * time.Minute)
	_, out, _ := grokRun(f, SubStop, grokDoc(f, "stop", "", map[string]any{"permissionMode": "bypassPermissions"}))
	if reason := blockReason(t, out); !strings.Contains(reason, "Arm this session's Brigade inbox") || strings.Contains(reason, stopAskOpen) {
		t.Fatalf("the first Stop's reason: %q", reason)
	}
	if got := f.stopStamp(t); got != "" {
		t.Fatalf("the arming Stop wrote a state stamp: %q", got)
	}
	// Armed: the next Stop, a long turn on, is the state ask.
	f.now = fixedTime.Add(13 * time.Minute)
	_, out, _ = grokRun(f, SubStop, grokDoc(f, "stop", "", map[string]any{"permissionMode": "bypassPermissions"}))
	if got := blockReason(t, out); got != stopAskOpen+stopAskDoing+stopAskClose {
		t.Fatalf("the second Stop's reason: %q", got)
	}
	// And stop_hook_active in Grok's spelling asks nothing.
	f.now = fixedTime.Add(14 * time.Minute)
	if _, out, _ = grokRun(f, SubStop, grokDoc(f, "stop", "", map[string]any{"permissionMode": "bypassPermissions", "stopHookActive": true})); out != "" {
		t.Fatalf("the active Stop asked: %q", out)
	}
}

// TestStopAnswerFollowsTheVersion (card 186): the same ask, as the
// feedback answer when the session registry's version is 2.1.163 or later,
// and as the block answer when the version is older, unparsable, not a
// string, absent from the entry, or the entry itself is missing.
func TestStopAnswerFollowsTheVersion(t *testing.T) {
	t.Parallel()
	want := stopAskOpen + stopAskDoing + stopAskClose
	for name, tc := range map[string]struct {
		entry    func(f *fixture) string // "" for no registry entry at all
		feedback bool
	}{
		"2.1.163, the first": {entry: versionEntry(`"2.1.163"`), feedback: true},
		"2.1.316, measured":  {entry: versionEntry(`"2.1.316"`), feedback: true},
		"2.2.0":              {entry: versionEntry(`"2.2.0"`), feedback: true},
		"3.0.0":              {entry: versionEntry(`"3.0.0"`), feedback: true},
		"2.1.162, the last":  {entry: versionEntry(`"2.1.162"`)},
		"2.0.999":            {entry: versionEntry(`"2.0.999"`)},
		"1.9.400":            {entry: versionEntry(`"1.9.400"`)},
		"a suffix":           {entry: versionEntry(`"2.1.316-beta"`)},
		"unknown":            {entry: versionEntry(`"unknown"`)},
		"empty":              {entry: versionEntry(`""`)},
		"not a string":       {entry: versionEntry(`2.1`)},
		"no version member":  {entry: versionEntry("")},
		"no registry entry":  {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			entries := map[int]string{}
			if tc.entry != nil {
				entries[f.pid] = tc.entry(f)
			}
			f.registry = fakeregistry.New(t, entries)
			f.deps.Registry = f.registry
			stateSession(t, f, "bypassPermissions")
			f.now = fixedTime.Add(12 * time.Minute)
			exit, out, errOut := f.run(SubStop, f.stopDoc("bypassPermissions", false))
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			got := ""
			if tc.feedback {
				got = feedbackContext(t, out)
			} else {
				got = blockReason(t, out)
			}
			if got != want {
				t.Fatalf("the ask = %q, want %q", got, want)
			}
		})
	}
}

// TestStopAnswerReadsTheMapBeforeTheRegistry (card 186): the version
// the answer follows is the session map's harness_version when the map
// has one — enough on its own, and it wins over the registry — and the
// registry read at the Stop only when the map has none ("unknown").
func TestStopAnswerReadsTheMapBeforeTheRegistry(t *testing.T) {
	t.Parallel()
	want := stopAskOpen + stopAskDoing + stopAskClose
	for name, tc := range map[string]struct {
		mapVersion string
		entry      func(f *fixture) string // "" for no registry entry at all
		feedback   bool
	}{
		"the map is enough":  {mapVersion: "2.1.316", feedback: true},
		"the map wins":       {mapVersion: "2.1.162", entry: versionEntry(`"2.1.316"`)},
		"unknown falls back": {mapVersion: unknownVersion, entry: versionEntry(`"2.1.316"`), feedback: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			stateSession(t, f, "bypassPermissions")
			m := f.mustMap()
			m.HarnessVersion = tc.mapVersion
			writeMapOf(t, f, m)
			entries := map[int]string{}
			if tc.entry != nil {
				entries[f.pid] = tc.entry(f)
			}
			f.registry = fakeregistry.New(t, entries)
			f.deps.Registry = f.registry
			f.now = fixedTime.Add(12 * time.Minute)
			exit, out, errOut := f.run(SubStop, f.stopDoc("bypassPermissions", false))
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			got := ""
			if tc.feedback {
				got = feedbackContext(t, out)
			} else {
				got = blockReason(t, out)
			}
			if got != want {
				t.Fatalf("the ask = %q, want %q", got, want)
			}
		})
	}
}

// versionEntry is the fixture's registry entry with its version member
// replaced by raw (a JSON value), or dropped when raw is "".
func versionEntry(raw string) func(f *fixture) string {
	return func(f *fixture) string {
		entry := fakeregistry.Observed(f.pid, "payments-api", "busy", f.socket)
		member := `"version":"2.1.259",`
		if !strings.Contains(entry, member) {
			panic("versionEntry: the observed entry has no version member")
		}
		if raw == "" {
			return strings.Replace(entry, member, "", 1)
		}
		return strings.Replace(entry, member, `"version":`+raw+`,`, 1)
	}
}

// TestVersionAtLeast: three runs of decimal digits, compared part by part;
// anything else is not a version and reads as older.
func TestVersionAtLeast(t *testing.T) {
	t.Parallel()
	for v, want := range map[string]bool{
		"2.1.163": true, "2.1.164": true, "2.1.316": true, "2.2.0": true, "3.0.0": true, "10.0.0": true,
		"2.1.1630": true, "02.01.163": true,
		"2.1.162": false, "2.0.999": false, "1.99.999": false, "0.0.0": false,
		"": false, "unknown": false, "2.1": false, "2.1.163.1": false, "2.1.163-beta": false,
		"v2.1.163": false, "+2.1.163": false, "2.+1.163": false, "2.1.-163": false,
		"2..163": false, "2.1.": false, ".1.163": false, "2.1.99999999999999999999": false,
	} {
		if got := versionAtLeast(v, stopFeedbackSince); got != want {
			t.Errorf("versionAtLeast(%q) = %v, want %v", v, got, want)
		}
	} // Whitespace around a version is not trimmed: it is not a version.
	for _, v := range []string{" 2.1.163", "2.1.163 ", "2.1.163\n", "2. 1.163"} {
		if versionAtLeast(v, stopFeedbackSince) {
			t.Errorf("versionAtLeast(%q) = true, want false", v)
		}
	}
}
