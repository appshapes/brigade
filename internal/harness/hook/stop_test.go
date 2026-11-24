//go:build !windows

package hook

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
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
// test sets git's clock for each commit (gitAt), so a commit sits where
// the hook's clock says the turn is, and the doing line is kept fresh so
// its own gate stays shut.
func TestStopAsksForANoteAfterACommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	base := fixedTime.Add(time.Hour)
	f.initCheckout(fixedTime.Add(-time.Hour))
	path := "PATH=" + gitPath(t)
	s := stateSession(t, f, "bypassPermissions")
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
	f.gitAt(base.Add(35*time.Second), "commit", "-q", "--allow-empty", "-m", "162: Add the Stop hook")
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
	f.gitAt(base.Add(19*time.Minute), "commit", "-q", "--allow-empty", "-m", "162: Record the note")
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

// gitAt runs git in the fixture's checkout with git's clock set to at: a
// commit's time and the reflog entry of whatever moves HEAD both carry
// it, so a test places a commit before, inside or after a turn of the
// hook's own clock (work item 319).
func (f *fixture) gitAt(at time.Time, args ...string) {
	f.t.Helper()
	date := strconv.FormatInt(at.Unix(), 10) + " +0000"
	f.git([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, args...)
}

// initCheckout makes the fixture's checkout a repository of the author
// me@example.com with one commit, made at `at`.
func (f *fixture) initCheckout(at time.Time) {
	f.t.Helper()
	if err := os.RemoveAll(f.cwd + "/.git"); err != nil {
		f.t.Fatal(err)
	}
	f.gitIn("init", "-q", "-b", "master")
	f.gitIn("config", "user.email", "me@example.com")
	f.gitIn("config", "user.name", "Me")
	f.gitAt(at, "commit", "-q", "--allow-empty", "-m", "0: before")
}

// noteAskSession is a session in a project with a state folder, in a
// checkout whose one commit was made an hour before the session started:
// what the note gate's tests of work item 319 start from. It answers the
// PATH that carries git, which every hook of the test runs with.
func noteAskSession(t *testing.T, f *fixture) string {
	t.Helper()
	f.initCheckout(fixedTime.Add(-time.Hour))
	stateSession(t, f, "bypassPermissions")
	return "PATH=" + gitPath(t)
}

// turn is one turn of the session: the prompt at `at`, what happens in the
// checkout meanwhile, and the Stop twenty seconds on — short enough that
// the doing and the decision gates stay shut. It answers the Stop's ask,
// "" for none.
func (f *fixture) turn(t *testing.T, path string, at time.Time, during func()) string {
	t.Helper()
	f.now = at
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"), path); exit != 0 {
		t.Fatalf("prompt: exit %d: %s", exit, errOut)
	}
	if during != nil {
		during()
	}
	f.now = at.Add(20 * time.Second)
	exit, out, errOut := f.run(SubStop, f.stopDoc("bypassPermissions", false), path)
	if exit != 0 {
		t.Fatalf("stop: exit %d: %s", exit, errOut)
	}
	return feedbackContext(t, out)
}

// TestStopNoteAskIsForACommitOfTheTurn (work item 319): the note is asked
// for a commit made in the checkout during the turn that is ending, and
// for no other. The sightings on the work item, each one a line that said
// "this turn made a commit" of a turn that made none: a commit of an
// earlier turn whose own ask the cadence had silenced, asked two hours
// later; a commit another session of the member made in the same checkout
// while this one was between turns; and, read from the code, the member's
// own commit that a pull brought in.
func TestStopNoteAskIsForACommitOfTheTurn(t *testing.T) {
	t.Parallel()
	const ask = stopAskOpen + stopAskNote + stopAskClose
	t.Run("a commit an earlier turn made", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		path := noteAskSession(t, f)
		t1 := fixedTime.Add(time.Minute)
		// The control: the turn commits, and its Stop asks.
		if got := f.turn(t, path, t1, func() {
			f.gitAt(t1.Add(10*time.Second), "commit", "-q", "--allow-empty", "-m", "319: the first")
		}); got != ask {
			t.Fatalf("the turn that committed was not asked: %q", got)
		}
		// The next turn commits too, two minutes on: never twice in a row.
		t2 := t1.Add(2 * time.Minute)
		if got := f.turn(t, path, t2, func() {
			f.gitAt(t2.Add(10*time.Second), "commit", "-q", "--allow-empty", "-m", "319: the second")
		}); got != "" {
			t.Fatalf("asked twice in a row: %q", got)
		}
		// A turn that commits nothing, past the ask interval: the second
		// commit is an earlier turn's, and this turn made none.
		if got := f.turn(t, path, t1.Add(15*time.Minute), nil); got != "" {
			t.Fatalf("a turn that made no commit was asked for an earlier turn's: %q", got)
		}
		if e := f.mustEntry(t); len(e.Commits) != 2 || !e.Commits[0].Made || !e.Commits[1].Made {
			t.Fatalf("the entry does not carry both commits as made here: %+v", e.Commits)
		}
	})
	t.Run("a commit another session made between this one's turns", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		path := noteAskSession(t, f)
		t1 := fixedTime.Add(time.Minute)
		if got := f.turn(t, path, t1, nil); got != "" {
			t.Fatalf("a turn in an untouched checkout was asked: %q", got)
		}
		// Another session of the member, in the same checkout, commits
		// while this one waits for its user: the same author, the same
		// HEAD, the same reflog.
		f.gitAt(t1.Add(5*time.Minute), "commit", "-q", "--allow-empty", "-m", "320: another session's")
		if got := f.turn(t, path, t1.Add(20*time.Minute), nil); got != "" {
			t.Fatalf("a turn that made no commit was asked for another session's: %q", got)
		}
		// The entry's facts are what they were: the digest still lists it.
		if e := f.mustEntry(t); len(e.Commits) != 1 || e.Commits[0].Card != 320 {
			t.Fatalf("the entry's commits: %+v", e.Commits)
		}
	})
	t.Run("a commit made in the checkout during the turn", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		path := noteAskSession(t, f)
		// Git's files do not say which session of a checkout made a
		// commit, so the sentence says what was found and leaves the rest
		// to the model, which knows whether it committed.
		t1 := fixedTime.Add(time.Minute)
		got := f.turn(t, path, t1, func() {
			f.gitAt(t1.Add(10*time.Second), "commit", "-q", "--allow-empty", "-m", "320: whoever made it")
		})
		if got != ask {
			t.Fatalf("a commit made in the checkout during the turn was not asked: %q", got)
		}
		// What the hook printed, against the words themselves.
		if !strings.HasPrefix(got, "Brigade state (one ask, then finish): a commit was made in this checkout during this turn and no note followed; if this session or an agent it started made it, record one line with `brigade note") ||
			!strings.Contains(got, "; if another session made it, nothing is needed.") || strings.Contains(got, "this turn made a commit") {
			t.Fatalf("the ask does not say what was found: %q", got)
		}
	})
	t.Run("a commit made in the second of the prompt", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		path := noteAskSession(t, f)
		// Git's times are whole seconds: a commit in the prompt's own
		// second was made before the turn could have made one.
		t1 := fixedTime.Add(time.Minute)
		if got := f.turn(t, path, t1.Add(900*time.Millisecond), func() {
			f.gitAt(t1, "commit", "-q", "--allow-empty", "-m", "320: another session's, a moment before the prompt")
		}); got != "" {
			t.Fatalf("a commit made in the prompt's own second was asked about: %q", got)
		}
	})
	t.Run("the member's own commit that a pull brought in", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		path := noteAskSession(t, f)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		f.gitIn("clone", "-q", f.cwd, elsewhere)
		t1 := fixedTime.Add(time.Minute)
		if got := f.turn(t, path, t1, func() {
			f.gitAt(t1.Add(5*time.Second), "-C", elsewhere, "-c", "user.email=me@example.com", "-c", "user.name=Me",
				"commit", "-q", "--allow-empty", "-m", "321: made in another checkout")
			f.gitAt(t1.Add(10*time.Second), "pull", "-q", "--no-edit", "--no-rebase", elsewhere, "master")
		}); got != "" {
			t.Fatalf("a turn that only pulled was asked for a note: %q", got)
		}
		if e := f.mustEntry(t); len(e.Commits) != 1 || e.Commits[0].Card != 321 || e.Commits[0].Made {
			t.Fatalf("the entry's commits: %+v", e.Commits)
		}
	})
}

// TestPromptNoteAskIsForACommitSinceThePreviousPrompt (work item 319): on
// a host with no Stop hook the prompt judges the turn that just ended, so
// the commit it asks about is one made between the previous prompt and
// this one, and a commit made before that prompt is not asked about.
// Nothing there marks where the turn ended and the wait began, so the
// sentence says "since this session's previous prompt" and no more.
func TestPromptNoteAskIsForACommitSinceThePreviousPrompt(t *testing.T) {
	t.Parallel()
	f := newMuseFixture(t)
	f.initCheckout(fixedTime.Add(-time.Hour))
	f.writeTeamFile(stateMember)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	seam.describe = describeWithDescription()
	path := "PATH=" + gitPath(t)
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), path); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	prompt := func(at time.Time) string {
		t.Helper()
		f.now = at
		exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"), path)
		if exit != 0 {
			t.Fatalf("prompt: exit %d: %s", exit, errOut)
		}
		return out
	}
	// Another session commits before this one's first prompt; the first
	// prompt ends no turn, and the turn it starts makes no commit.
	f.gitAt(fixedTime.Add(10*time.Second), "commit", "-q", "--allow-empty", "-m", "320: another session's")
	t1 := fixedTime.Add(time.Minute)
	if out := prompt(t1); strings.Contains(out, stopAskOpen) {
		t.Fatalf("the first prompt asked: %q", out)
	}
	t2 := t1.Add(20 * time.Second)
	if out := prompt(t2); strings.Contains(out, stopAskOpen) {
		t.Fatalf("a turn that made no commit was asked for another session's: %q", out)
	}
	// A commit after the prompt at t2; the next prompt asks, in its own
	// words.
	f.gitAt(t2.Add(10*time.Second), "commit", "-q", "--allow-empty", "-m", "319: since the previous prompt")
	out := prompt(t2.Add(20 * time.Second))
	if !strings.Contains(out, namedIn(f, stopAskOpen+promptAskNote+stopAskClose)) {
		t.Fatalf("the prompt after a commit did not carry the ask:\n%s", out)
	}
	if !strings.Contains(out, "a commit was made in this checkout since this session's previous prompt and no note followed; if this session") || strings.Contains(out, "during this turn") {
		t.Fatalf("the prompt's ask does not say what was found:\n%s", out)
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

// TestStopAsksNothingWithoutASecondOfItsBudget: the ask is stamped and
// written only while stopMinBudget of the hook's budget is left, so that a
// hook its host stops before the answer is out has stamped nothing and the
// next Stop asks (the P5-18 shape, as the doing reminder's). It is the gate
// that left a test of what the ask says with an empty answer on a busy
// machine (work item 336, the fixture's unhurried); here the budget is made
// short, and the control is the same Stop with its budget back.
func TestStopAsksNothingWithoutASecondOfItsBudget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	stateSession(t, f, "bypassPermissions")
	f.now = fixedTime.Add(12 * time.Minute)
	f.deps.StopBudget = 500 * time.Millisecond
	if exit, out, errOut := f.run(SubStop, f.stopDoc("bypassPermissions", false)); exit != 0 || out != "" {
		t.Fatalf("a Stop with half a second of budget: exit %d, out %q\n%s", exit, out, errOut)
	}
	if got := f.stopStamp(t); got != "" {
		t.Fatalf("a stamp was written with no budget to answer in: %q", got)
	}
	f.unhurried()
	_, out, errOut := f.run(SubStop, f.stopDoc("bypassPermissions", false))
	if got := feedbackContext(t, out); got != stopAskOpen+stopAskDoing+stopAskClose {
		t.Fatalf("the same Stop with its budget back: %q\n%s", got, errOut)
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

// namedIn is a line of constants as a pointed host's hook prints it (work
// item 249): each command it asks for is named by the package's bootstrap,
// the fixture's pluginBin, where the constant says the bare `brigade`.
func namedIn(f *fixture, line string) string {
	return strings.ReplaceAll(line, "`brigade ", "`"+f.pluginBin+" ")
}

// saidIn is a line of the hook as the fixture's host says it: with the
// command's path on a host whose shell has no brigade on PATH, and as it
// is on Claude Code (work item 359).
func saidIn(f *fixture, line string) string {
	if f.host.Pointed() {
		return namedIn(f, line)
	}
	return line
}

// startLine is the start line as Claude Code says it, with the bare word:
// what startLineIn gives a host that has brigade on PATH.
func startLine(name, id, team, policy string) string {
	return startLineWho(name, id, team, policy) + startLineUse
}

// startLineOn is the start line of a session of the fixture's team that
// accepts messages, as the fixture's host says it (work item 432): its two
// commands by the package's path on a host whose shell has no brigade on
// PATH, and startLine itself on Claude Code. The names before the commands
// are never rewritten, whatever they hold.
func startLineOn(f *fixture, name, id string) string {
	if f.host.Pointed() {
		return startLineWho(name, id, teamName, "accept") + namedIn(f, startLineUse)
	}
	return startLine(name, id, teamName, "accept")
}

// TestRemindersNameTheCommandOnAPointedHost (work item 249): the line that
// asks for a doing line is a constant that names the bare `brigade`, which
// is on the PATH of a Claude Code session's shell and of no other host's.
// A Muse Code session ran it as written and got exit status 127. On a host
// whose command is the package's bootstrap, the line names that path; on
// Claude Code it stays the constant, byte for byte (TestDoingNudge*).
func TestRemindersNameTheCommandOnAPointedHost(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(*testing.T) *fixture{"Muse Code": newMuseFixture, "Codex CLI": newCodexFixture} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := build(t)
			f.writeTeamFile(stateMember)
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			seam.describe = describeWithDescription()
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			exit, out, errOut := f.run(SubPrompt, f.promptDoc("bypassPermissions"))
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			want := "Brigade doing: this session's line is blank"
			line := ""
			for _, l := range lines(out) {
				if strings.HasPrefix(l, want) {
					line = l
				}
			}
			if line != namedIn(f, doingLineBlank) {
				t.Fatalf("the doing line:\n%q\nwant, with the command's path:\n%q\n(stdout %q)", line, namedIn(f, doingLineBlank), out)
			}
			if !strings.Contains(line, "`"+f.pluginBin+" doing <<'EOF'`") || strings.Contains(out, "`brigade ") {
				t.Fatalf("the prompt still names a bare brigade, or not the bootstrap: %q", out)
			}
		})
	}
}

// TestCommandInLeavesClaudeCodeAlone (work item 249, finding F1): where
// the bare word is the command, a line is returned as it is.
func TestCommandInLeavesClaudeCodeAlone(t *testing.T) {
	t.Parallel()
	r := &run{}
	// The lines of a team file that is refused name a command too (work
	// item 359).
	for _, line := range []string{doingLineBlank, doingLineFull, doingLineShort, stopAskOpen + stopAskNote + " " + stopAskDoing + " " + stopAskDecision + stopAskClose, driftLine, membershipRefusedLine} {
		if got := r.commandIn(facts{}, line); got != line {
			t.Fatalf("a host with brigade on PATH got another line:\n%q\nwant\n%q", got, line)
		}
		pointed := facts{host: config.HostMuse, pluginBin: "/p/bin/brigade"}
		got := r.commandIn(pointed, line)
		if strings.Contains(got, "`brigade ") || !strings.Contains(got, "`/p/bin/brigade ") || strings.ReplaceAll(got, "/p/bin/brigade", "brigade") != line {
			t.Fatalf("a pointed host's line: %q", got)
		}
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
	// The ask names the command by the path this host's model can run
	// (work item 249): no bare `brigade` is on a Muse Code session's PATH.
	if !strings.Contains(out, namedIn(f, stopAskOpen+stopAskDoing+stopAskClose)) || strings.Contains(out, "`brigade ") {
		t.Fatalf("the long turn's prompt did not carry the ask with the command's path:\n%s", out)
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
	_, out, errOut := grokRun(f, SubStop, grokDoc(f, "stop", "", map[string]any{"permissionMode": "bypassPermissions"}))
	if reason := blockReason(t, out); !strings.Contains(reason, "Arm this session's Brigade inbox") || strings.Contains(reason, stopAskOpen) {
		t.Fatalf("the first Stop's reason: %q\n%s", reason, errOut)
	}
	if got := f.stopStamp(t); got != "" {
		t.Fatalf("the arming Stop wrote a state stamp: %q", got)
	}
	// Armed: the next Stop, a long turn on, is the state ask. It is not made
	// with under a second of the hook's budget left, here as on Claude Code
	// (TestStopAsksNothingWithoutASecondOfItsBudget), and nothing is stamped.
	f.now = fixedTime.Add(13 * time.Minute)
	f.deps.StopBudget = 500 * time.Millisecond
	if _, out, errOut = grokRun(f, SubStop, grokDoc(f, "stop", "", map[string]any{"permissionMode": "bypassPermissions"})); out != "" || f.stopStamp(t) != "" {
		t.Fatalf("a Stop with half a second of budget: out %q, stamp %q\n%s", out, f.stopStamp(t), errOut)
	}
	f.unhurried()
	_, out, errOut = grokRun(f, SubStop, grokDoc(f, "stop", "", map[string]any{"permissionMode": "bypassPermissions"}))
	if got := blockReason(t, out); got != namedIn(f, stopAskOpen+stopAskDoing+stopAskClose) || strings.Contains(got, "`brigade ") {
		t.Fatalf("the second Stop's reason: %q\n%s", got, errOut)
	}
	// And stop_hook_active in Grok's spelling asks nothing.
	f.now = fixedTime.Add(14 * time.Minute)
	if _, out, _ = grokRun(f, SubStop, grokDoc(f, "stop", "", map[string]any{"permissionMode": "bypassPermissions", "stopHookActive": true})); out != "" {
		t.Fatalf("the active Stop asked: %q", out)
	}
}

// TestStatusInTheOptionsFileStopsTheAsk (work item 439): `status` is a key
// of the options file, so a member of a host that reads its options there
// can turn the end-of-turn ask off. Grok Build's Stop hook and the next
// prompt of Muse Code and of Codex CLI, on the long turn with no doing line
// that the tests of each host show is asked about, ask nothing when the
// file says off; the same session with the key left out is the control.
// Codex CLI's case runs under "Full Access", the one choice of Codex's
// under which the ask is made (work item 440).
func TestStatusInTheOptionsFileStopsTheAsk(t *testing.T) {
	t.Parallel()
	options := func(t *testing.T, f *fixture, file, status string, with map[string]string) {
		t.Helper()
		if status != "" {
			with["status"] = status
		}
		body, err := json.Marshal(with)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(f.configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.configDir, file), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		status  string
		wantAsk bool
	}{
		{"the key left out", "", true},
		{"off", "off", false},
	} {
		t.Run("Grok Build's Stop hook, "+tc.name, func(t *testing.T) {
			t.Parallel()
			f := grokFixture(t)
			f.unhurried()
			f.writeTeamFile(stateMember)
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			seam.describe = describeWithDescription()
			// As in TestGrokStopCarriesTheAsk: the state folder rides on sync.
			options(t, f, config.GrokOptionsFile, tc.status, map[string]string{"adapter_command": f.adapterOption(), "sync": "on"})
			mode := map[string]any{"permissionMode": "bypassPermissions"}
			if exit, _, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", mode)); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if exit, _, errOut := grokRun(f, SubPrompt, grokDoc(f, "user_prompt_submit", "", mode)); exit != 0 {
				t.Fatalf("prompt: exit %d: %s", exit, errOut)
			}
			// The first completed turn arms the monitor, whatever `status` says.
			f.now = fixedTime.Add(12 * time.Minute)
			if _, out, errOut := grokRun(f, SubStop, grokDoc(f, "stop", "", mode)); !strings.Contains(blockReason(t, out), "Arm this session's Brigade inbox") {
				t.Fatalf("the first Stop did not arm the monitor: %q\n%s", out, errOut)
			}
			f.now = fixedTime.Add(13 * time.Minute)
			_, out, errOut := grokRun(f, SubStop, grokDoc(f, "stop", "", mode))
			if tc.wantAsk {
				if got := blockReason(t, out); got != namedIn(f, stopAskOpen+stopAskDoing+stopAskClose) {
					t.Fatalf("the control was not asked: %q\n%s", got, errOut)
				}
				return
			}
			if out != "" || f.stopStamp(t) != "" {
				t.Fatalf("status off in the options file: out %q, stamp %q\n%s", out, f.stopStamp(t), errOut)
			}
		})
		for _, host := range []struct {
			name          string
			fixture       func(*testing.T) *fixture
			file          string
			start, prompt func(*fixture) string
		}{
			{"Muse Code", newMuseFixture, config.MuseOptionsFile,
				func(f *fixture) string { return f.startDoc("startup") },
				func(f *fixture) string { return f.promptDoc("bypassPermissions") }},
			{"Codex CLI", newCodexFixture, config.CodexOptionsFile,
				func(f *fixture) string { return f.codexDocIn(codexModeFullAccess, "SessionStart", "startup") },
				func(f *fixture) string { return f.codexDocIn(codexModeFullAccess, "UserPromptSubmit", "") }},
		} {
			t.Run(host.name+"'s next prompt, "+tc.name, func(t *testing.T) {
				t.Parallel()
				f := host.fixture(t)
				f.unhurried()
				// The fixture's default reader answers only Claude Code's
				// settings path; this hook reads the options file for real.
				f.deps.ReadFile = os.ReadFile
				options(t, f, host.file, tc.status, map[string]string{})
				f.writeTeamFile(stateMember)
				seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
				seam.describe = describeWithDescription()
				if exit, _, errOut := f.run(SubSessionStart, host.start(f)); exit != 0 {
					t.Fatalf("exit %d: %s", exit, errOut)
				}
				if exit, out, _ := f.run(SubPrompt, host.prompt(f)); exit != 0 || strings.Contains(out, stopAskOpen) {
					t.Fatalf("the first prompt asked: exit %d out %q", exit, out)
				}
				f.now = fixedTime.Add(12 * time.Minute)
				exit, out, errOut := f.run(SubPrompt, host.prompt(f))
				if exit != 0 {
					t.Fatalf("exit %d: %s", exit, errOut)
				}
				if asked := strings.Contains(out, namedIn(f, stopAskOpen+stopAskDoing+stopAskClose)); asked != tc.wantAsk || (!tc.wantAsk && strings.Contains(out, stopAskOpen)) {
					t.Fatalf("asked %v, want %v:\n%s", asked, tc.wantAsk, out)
				}
				// The stamp is the session's, by the key its map has: the
				// hook's parent on Muse Code, the thread's key on Codex CLI.
				stamp, _ := os.ReadFile(stopStampPath(f.stateDir, f.mustMap().ClaudePID))
				if stamped := strings.HasSuffix(strings.TrimSpace(string(stamp)), " asked"); stamped != tc.wantAsk || (!tc.wantAsk && len(stamp) != 0) {
					t.Fatalf("stamp %q, want asked %v", stamp, tc.wantAsk)
				}
			})
		}
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
