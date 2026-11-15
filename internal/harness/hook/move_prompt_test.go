//go:build !windows

package hook

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// A running session follows its team (work item 327, ADR 0007): the
// fixture's session is attached on the team's first backend, by name, and
// the team file is then rewritten to name a Brigade server for the same
// team. A prompt starts the follow as a child; the tests run that child in
// process, with the arguments and the environment the hook gave it.

// joinOK is the new backend admitting this machine.
func joinOK() fakeadapter.Response {
	return okResp(mustJSON(map[string]any{"team_ref": "team-1", "team_name": teamName, "principal_ref": "p-new", "rejoined": false}))
}

// attachedMember starts the fixture's session on the team's first backend,
// with a live watcher, and returns the seam that answers both backends:
// the first registration is brigade-sess-1 and the next brigade-sess-2,
// unless extra says otherwise.
func attachedMember(t *testing.T, f *fixture, moved map[string]any, extra map[string][]fakeadapter.Response, env ...string) *adapterSeam {
	t.Helper()
	seam := memberSeam(t, f, moved, extra)
	startMember(t, f, env...)
	return seam
}

// memberSeam is the first half of attachedMember: the seam, before any
// adapter is asked anything.
func memberSeam(t *testing.T, f *fixture, moved map[string]any, extra map[string][]fakeadapter.Response) *adapterSeam {
	t.Helper()
	f.labelOldBinding()
	f.spawner.watcherPID = testutil.NewSleeper(t)
	responses := map[string][]fakeadapter.Response{
		"team join": {joinOK()},
		"session register": {
			okResp(registerDoc("brigade-sess-1", "payments-api", false)),
			okResp(registerDoc("brigade-sess-2", "payments-api", false)),
		},
		"session heartbeat": {okResp(heartbeatDoc())},
		"session close":     {okResp(closeDoc())},
	}
	for k, v := range extra {
		responses[k] = v
	}
	return f.useMoveSeam(moved, responses)
}

// startMember is the second half: the session's start.
func startMember(t *testing.T, f *fixture, env ...string) {
	t.Helper()
	if exit, _, errOut := f.run(SubSessionStart, f.sessionDoc("startup"), env...); exit != 0 {
		t.Fatalf("session-start: exit %d: %s", exit, errOut)
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || m.TeamRef != "team-1" {
		t.Fatalf("the session did not attach on the team's first backend: %+v", m)
	}
}

// sessionDoc is a SessionStart document of the fixture's host.
func (f *fixture) sessionDoc(source string) string {
	switch {
	case f.codex():
		return f.codexDoc("SessionStart", source)
	case f.muse():
		return f.museStartDoc("default")
	}
	return f.startDoc(source)
}

// prompted runs one prompt of the fixture's session and returns the lines
// it printed. Nothing a prompt prints or logs may carry the move's ticket,
// and no line the member's label.
func (f *fixture) prompted(env ...string) []string {
	f.t.Helper()
	doc := f.promptDoc("default")
	if f.codex() {
		doc = f.codexDoc("UserPromptSubmit", "")
	}
	exit, out, errOut := f.run(SubPrompt, doc, env...)
	if exit != 0 {
		f.t.Fatalf("prompt: exit %d: %s", exit, errOut)
	}
	if strings.Contains(out+errOut, moveTicket) || strings.Contains(out, movedLabel) {
		f.t.Fatalf("the ticket or the member's label reached a stream of the prompt hook")
	}
	return lines(out)
}

// movedFile is the fixture's team file as it is now, parsed.
func (f *fixture) movedFile() *teamfile.File {
	f.t.Helper()
	tf, err := teamfile.Parse(filepath.Join(f.cwd, teamfile.FileName))
	if err != nil {
		f.t.Fatal(err)
	}
	return tf
}

// canon is the canonical path of the fixture's checkout.
func (f *fixture) canon() string {
	f.t.Helper()
	canon, err := teamfile.Canonicalize(f.cwd)
	if err != nil {
		f.t.Fatal(err)
	}
	return canon
}

// newBackendCalls are the adapter calls made under the team key of the
// backend the team file names, describe included.
func newBackendCalls(seam *adapterSeam, newKey string) []string {
	seam.mu.Lock()
	defer seam.mu.Unlock()
	var out []string
	for _, c := range seam.calls {
		if profileOf(c) == newKey {
			out = append(out, verbOf(c.Argv))
		}
	}
	return out
}

// registration decodes the document of a `session register` call.
func registration(t *testing.T, c adapterCall) protocol.SessionRegistration {
	t.Helper()
	var reg protocol.SessionRegistration
	if err := json.Unmarshal(c.Stdin, &reg); err != nil {
		t.Fatalf("a registration's document: %v", err)
	}
	return reg
}

// exists reports whether path names a file.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// TestPromptFollowsARunningSessionInTwoSteps is the whole of a running
// session's follow. The first prompt after the team file changed starts
// the follow as a child, with nothing in its environment or arguments that
// lets it send or read a message, says one line that echoes nothing from
// the file, and leaves the session where it is. The child asks the backend
// the checkout joined, joins the new one with the record's ticket and
// writes the pin. The next prompt says the follow's line, registers the
// session anew on the new backend under the name it has — with no resume
// hint and no heartbeat of the id it had — writes the map, starts the
// watcher for it and has the doing line asked for again. A third prompt
// has nothing left to say or do.
func TestPromptFollowsARunningSessionInTwoSteps(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	oldKey, before := f.teamKey, f.checkoutPin()
	newKey := teamstore.Key("rest", movedURL, "team-1")

	if got := f.prompted(); len(got) != 0 || len(f.spawner.followed()) != 0 {
		t.Fatalf("a prompt with the team file unchanged: lines %q, %d follows", got, len(f.spawner.followed()))
	}
	f.moveTeamFile("team-1")
	tf := f.movedFile()

	// Step 1: the follow, off the hook's budget.
	calls := len(asked(seam))
	got := f.prompted()
	if !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("the first prompt after the file changed: %q, want the one line", got)
	}
	if strings.Contains(got[0], "brigade.example") || strings.Contains(got[0], "sha256:") {
		t.Fatalf("the line echoed the file: %q", got[0])
	}
	follows := f.spawner.followed()
	if len(follows) != 1 {
		t.Fatalf("%d follow children were started, want 1", len(follows))
	}
	spec := follows[0]
	wantArgs := []string{"hook", "follow", "--checkout", f.canon(), "--config-dir", f.configDir, "--state-dir", f.stateDir,
		"--key", strconv.Itoa(f.pid), "--log-level", "info"}
	if !slices.Equal(spec.Args, wantArgs) || spec.LogPath != filepath.Join(f.stateDir, "logs", "team-move.log") {
		t.Fatalf("the child's arguments %q and log %q", spec.Args, spec.LogPath)
	}
	for _, entry := range spec.Env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "CLAUDE_CODE_") || strings.HasPrefix(name, "CLAUDE_PLUGIN_") || strings.HasPrefix(name, "BRIGADE_") ||
			name == "CLAUDE_PID" || name == "CLAUDECODE" || strings.Contains(entry, msgTok) {
			t.Errorf("the child's environment carries %s", name)
		}
	}
	if envValue(spec.Env, "HOME") != f.dirs.Home {
		t.Errorf("the child's environment has no HOME")
	}
	// The hook has returned and the child has not run yet: the hook itself
	// asked no backend anything and wrote nothing to the store.
	if verbs := asked(seam)[calls:]; len(verbs) != 0 || f.checkoutPin() != before {
		t.Fatalf("the prompt hook's own run: adapter calls %v, pin %+v", verbs, f.checkoutPin())
	}
	f.runFollow(spec)
	if verbs := asked(seam)[calls:]; !slices.Equal(verbs, []string{"team moved", "team join"}) {
		t.Fatalf("the child's adapter calls: %v", verbs)
	}
	if moved, joined := seam.callsFor("team moved")[0], seam.callsFor("team join")[0]; profileOf(moved) != oldKey || profileOf(joined) != newKey {
		t.Fatalf("team moved ran under %q and team join under %q, want the old team key and the new", profileOf(moved), profileOf(joined))
	}
	if pin := f.checkoutPin(); !pin.Matches(tf) || pin.MovedFrom != before.URL {
		t.Fatalf("the pin after the child: %+v", pin)
	}
	if m := f.mustMap(); m.TeamKey != oldKey || m.BrigadeSessionID != "brigade-sess-1" {
		t.Fatalf("the session moved before its second prompt: %+v", m)
	}
	f.noTicketLeft()

	// Step 2: the re-attachment. The registry no longer names the session
	// and a prompt's document carries no title, so the name it registers
	// under can only be the one its map holds.
	f.deps.Registry = fakeregistry.New(t, map[int]string{})
	doingStamp := doingStampPath(f.stateDir, f.pid)
	if err := adapterkit.WriteAtomic(doingStamp, []byte(fixedTime.Format(time.RFC3339Nano)+" "+f.nativeID+"\n")); err != nil {
		t.Fatal(err)
	}
	registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
	got = f.prompted()
	want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if !slices.Equal(got, want) {
		t.Fatalf("the second prompt:\n%q\nwant\n%q", got, want)
	}
	m := f.mustMap()
	if m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || m.SessionName != "payments-api" ||
		!sessionmap.IsBundledRest(m.AdapterCommand) || m.ClaudeSessionID != f.nativeID {
		t.Fatalf("the map after the second prompt: %+v", m)
	}
	regs := seam.callsFor("session register")
	if len(regs) != registers+1 || profileOf(regs[len(regs)-1]) != newKey {
		t.Fatalf("%d registrations, the last under %q: want one more, under the new team key", len(regs), profileOf(regs[len(regs)-1]))
	}
	if reg := registration(t, regs[len(regs)-1]); reg.Resume != nil || reg.SessionName != "payments-api" || reg.Inbound != protocol.InboundAccept {
		t.Fatalf("the registration on the new backend: resume %v, name %q, inbound %q", reg.Resume, reg.SessionName, reg.Inbound)
	}
	if on := newBackendCalls(seam, newKey); slices.Contains(on, "session heartbeat") {
		t.Fatalf("the hook heartbeated on the new backend: %v", on)
	}
	if f.spawner.count() != spawns+1 || f.spawner.lastMap(t).BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("%d watchers started, want one more, for the new session", f.spawner.count()-spawns)
	}
	if w, err := config.FromWatcherEnv(f.spawner.last(t).Env); err != nil || w.Profile != newKey {
		t.Fatalf("the watcher's team key: %q, %v", w.Profile, err)
	}
	if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the pidfile after the second prompt: %+v, %v", entry, err)
	}
	if bn, err := f.store().ReadByNative(f.nativeID); err != nil || bn.BrigadeSessionID != "brigade-sess-2" || bn.TeamKey != newKey {
		t.Fatalf("the by-native map after the second prompt: %+v, %v", bn, err)
	}
	for what, path := range map[string]string{
		"the doing reminder's stamp": doingStamp,
		"the team-moved stamp":       config.TeamMovedStamp(f.stateDir, f.pid),
		"the follow's mark":          config.MoveFollowedMark(f.stateDir, f.configDir, f.canon()),
	} {
		if exists(path) {
			t.Errorf("the second prompt left %s", what)
		}
	}

	// A third prompt: nothing of the move is said, asked or started again.
	calls = len(seam.verbs())
	if got := f.prompted(); len(got) != 0 || len(f.spawner.followed()) != 1 || len(seam.verbs()) != calls {
		t.Fatalf("a third prompt: lines %q, %d follows, adapter calls %v", got, len(f.spawner.followed()), seam.verbs()[calls:])
	}
}

// TestPromptNeverFollowsWithoutTheRecord is the anchor, for a session that
// is running: a team file that names another backend is believed only when
// the backend the member joined holds the administrator's record of exactly
// that move. A file with no such record behind it — a hostile one that
// arrived by `git pull` among them — earns one question to the old backend
// and nothing to the address it names: not a join, not a registration, not
// even the local describe. The session stays attached where it was, says
// one line that echoes nothing from the file, and the store is as it was.
// The question is not asked again at the next prompt, and is asked once
// more a minute later. Where the member's options keep a session from
// following, nothing is asked at all.
func TestPromptNeverFollowsWithoutTheRecord(t *testing.T) {
	t.Parallel()
	notMoved := okResp(mustJSON(map[string]any{"team_ref": "team-1", "moved": false}))
	for _, tc := range []struct {
		name      string
		moved     map[string]any
		responses map[string][]fakeadapter.Response
		env       []string
		// named is false for the one case that runs with the adapter_command
		// option, the session naming its adapter itself.
		named bool
		// asks is the adapter commands one follow runs, in order.
		asks []string
		// said is what the first prompt after the file changed prints.
		said []string
		// follows is how many children that prompt starts.
		follows int
		// spoil changes the fixture before the session starts, nil for
		// nothing: an adapter's describe is asked once in a process.
		spoil func(f *fixture, seam *adapterSeam)
	}{
		{"no move is recorded", nil, map[string][]fakeadapter.Response{"team moved": {notMoved}},
			nil, true, []string{"team moved"}, []string{movedLine}, 1, nil},
		{"the record names another url", movedDoc("https://elsewhere.example.test", movedPin), nil,
			nil, true, []string{"team moved"}, []string{movedLine}, 1, nil},
		{"the record names another certificate", movedDoc(movedURL, strings.Replace(movedPin, "3", "4", 1)), nil,
			nil, true, []string{"team moved"}, []string{movedLine}, 1, nil},
		{"the record names no certificate", movedDoc(movedURL, ""), nil,
			nil, true, []string{"team moved"}, []string{movedLine}, 1, nil},
		{"the old backend refuses the member", nil, map[string][]fakeadapter.Response{"team moved": {errResp(protocol.CodeUnauthorized, "")}},
			nil, true, []string{"team moved"}, []string{movedLine}, 1, nil},
		{"the old backend is unreachable", nil, map[string][]fakeadapter.Response{"team moved": {errResp(protocol.CodeUnavailable, "network")}},
			nil, true, []string{"team moved"}, []string{movedLine}, 1, nil},
		{"the old backend's adapter keeps no record of moves", movedDoc(movedURL, movedPin), nil,
			nil, true, nil, []string{movedLine}, 1, func(f *fixture, seam *adapterSeam) {
				var described protocol.DescribeResult
				if err := json.Unmarshal(seam.describe, &described); err != nil {
					f.t.Fatal(err)
				}
				described.Capabilities = slices.DeleteFunc(described.Capabilities, func(c string) bool { return c == "team.move" })
				seam.describe = mustJSON(&described)
			}},
		{"this machine was a member at the new backend and left it", movedDoc(movedURL, movedPin), nil,
			nil, true, nil, []string{movedLine}, 1, func(f *fixture, _ *adapterSeam) {
				if err := adapterkit.SaveProfile(f.configDir, teamstore.Key("rest", movedURL, "team-1"), &adapterkit.Profile{
					Version: adapterkit.ProfileVersion, Adapter: "rest", URL: movedURL, Certificate: movedPin, PrincipalRef: "p-new",
				}); err != nil {
					f.t.Fatal(err)
				}
			}},
		{"the option is off", movedDoc(movedURL, movedPin), nil,
			[]string{config.OptionFollowTeamMove + "=off"}, true, nil, []string{movedStaysLine}, 0, nil},
		{"the option is neither word", movedDoc(movedURL, movedPin), nil,
			[]string{config.OptionFollowTeamMove + "=sometimes"}, true, nil, []string{config.WarnFollowTeamMoveInvalid, movedStaysLine}, 0, nil},
		{"the session names its adapter itself", movedDoc(movedURL, movedPin), nil,
			nil, false, nil, []string{movedStaysLine}, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMovingFixture(t)
			f.noAdapterOption = tc.named
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := memberSeam(t, f, tc.moved, tc.responses)
			if tc.spoil != nil {
				tc.spoil(f, seam)
			}
			startMember(t, f, tc.env...)
			newKey := teamstore.Key("rest", movedURL, "team-1")
			before, pinBefore, mapBefore := storeFingerprint(t, f.configDir), f.checkoutPin(), *f.mustMap()
			f.moveTeamFile("team-1")
			calls := len(asked(seam))

			got := f.prompted(tc.env...)
			if !slices.Equal(got, tc.said) {
				t.Fatalf("lines = %q, want %q", got, tc.said)
			}
			for _, marker := range []string{"brigade.example", "elsewhere.example", "sha256:"} {
				if strings.Contains(strings.Join(got, "\n"), marker) {
					t.Fatalf("a line echoed %q", marker)
				}
			}
			if n := len(f.spawner.followed()); n != tc.follows {
				t.Fatalf("%d follow children started, want %d", n, tc.follows)
			}
			if verbs := asked(seam)[calls:]; !slices.Equal(verbs, tc.asks) {
				t.Fatalf("adapter calls: %v, want %v", verbs, tc.asks)
			}
			if on := newBackendCalls(seam, newKey); len(on) != 0 {
				t.Fatalf("the backend the file names was sent %v", on)
			}
			unmoved := func(when string) {
				t.Helper()
				if m := f.mustMap(); m.TeamKey != mapBefore.TeamKey || m.BrigadeSessionID != mapBefore.BrigadeSessionID {
					t.Fatalf("%s: the session's map names %q and %q", when, m.TeamKey, m.BrigadeSessionID)
				}
				if pin := f.checkoutPin(); pin != pinBefore {
					t.Fatalf("%s: the pin changed: %+v", when, pin)
				}
				if exists(config.MoveFollowedMark(f.stateDir, f.configDir, f.canon())) {
					t.Fatalf("%s: a follow that followed nothing left its mark", when)
				}
				f.noTicketLeft()
				// The store holds what it held, but for the empty lock file a
				// follow that got as far as asking leaves beside the pins.
				_ = os.Remove(filepath.Join(f.configDir, "team-move.lock"))
				if after := storeFingerprint(t, f.configDir); after != before {
					t.Fatalf("%s: the store changed", when)
				}
			}
			unmoved("after the first prompt")

			// The next prompt, within the minute: nothing is said, started or
			// asked again.
			if got := f.prompted(tc.env...); len(got) != 0 || len(f.spawner.followed()) != tc.follows || len(asked(seam)) != calls+len(tc.asks) {
				t.Fatalf("a second prompt within the minute: lines %q, %d follows, calls %v", got, len(f.spawner.followed()), asked(seam)[calls:])
			}
			// A minute later the question is asked once more, and the line,
			// which was said, is not.
			f.now = f.now.Add(followInterval)
			if got := f.prompted(tc.env...); len(got) != 0 || len(f.spawner.followed()) != 2*tc.follows {
				t.Fatalf("a prompt a minute later: lines %q, %d follows, want %d", got, len(f.spawner.followed()), 2*tc.follows)
			}
			if verbs := asked(seam)[calls:]; !slices.Equal(verbs, append(slices.Clone(tc.asks), tc.asks...)) {
				t.Fatalf("adapter calls after a minute: %v, want %v twice", verbs, tc.asks)
			}
			if on := newBackendCalls(seam, newKey); len(on) != 0 {
				t.Fatalf("the backend the file names was sent %v", on)
			}
			unmoved("a minute later")
		})
	}
}

// TestPromptDoesNotMoveASessionTheNewBackendRefused: the record names the
// backend the file names, so the join is sent there with the ticket, and
// the backend refuses it. Nothing but that join reaches it, and the
// session stays where it was.
func TestPromptDoesNotMoveASessionTheNewBackendRefused(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{"team join": {errResp(protocol.CodeUnauthorized, "")}})
	newKey, pinBefore := teamstore.Key("rest", movedURL, "team-1"), f.checkoutPin()
	f.moveTeamFile("team-1")
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	if on := newBackendCalls(seam, newKey); !slices.Equal(on, []string{"team join"}) {
		t.Fatalf("the new backend was sent %v, want the one join", on)
	}
	if got := f.prompted(); len(got) != 0 {
		t.Fatalf("a second prompt: %q", got)
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || f.checkoutPin() != pinBefore {
		t.Fatalf("the session or its pin moved: %+v", m)
	}
	f.noTicketLeft()
}

// TestPromptRetryFollowsThroughAChild: a session that started before the
// move was recorded is attached to nothing, and each later prompt runs the
// registration again in the few seconds a prompt hook has. That retry does
// not follow in its own run: it starts the child, says that the backend the
// member joined is being asked and then the drift line as before, and asks
// no backend anything. When the child has followed, the very next prompt
// registers the session, and says the follow's line before any other.
func TestPromptRetryFollowsThroughAChild(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.labelOldBinding()
	f.moveTeamFile("team-1")
	before, tf := f.checkoutPin(), f.movedFile()
	seam := f.useMoveSeam(nil, map[string][]fakeadapter.Response{
		"team moved":       {okResp(mustJSON(map[string]any{"team_ref": "team-1", "moved": false}))},
		"team join":        {joinOK()},
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
	})
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !slices.Equal(lines(out), []string{driftLine}) || f.mapExists() {
		t.Fatalf("a start before the record: exit %d, lines %q", exit, lines(out))
	}
	// The administrator records the move.
	seam = f.useMoveSeam(movedDoc(movedURL, movedPin), seam.responses)
	if got := f.prompted(); !slices.Equal(got, []string{movedRetryLine, driftLine}) || f.mapExists() {
		t.Fatalf("the prompt's retry: lines %q, map %v", got, f.mapExists())
	}
	if got := asked(seam); len(got) != 0 {
		t.Fatalf("the prompt's retry asked a backend in its own run: %v", got)
	}
	follows := f.spawner.followed()
	if len(follows) != 1 || f.checkoutPin() != before {
		t.Fatalf("%d follow children started, pin %+v: want one child and the pin as it was", len(follows), f.checkoutPin())
	}
	// A prompt before the child has finished: the retry is not due, and no
	// second child is started.
	if got := f.prompted(); len(got) != 0 || len(f.spawner.followed()) != 1 {
		t.Fatalf("a prompt before the child finished: lines %q, %d follows", got, len(f.spawner.followed()))
	}
	// The child finishes: it followed, and the session's retry is due at once.
	f.runFollow(follows[0])
	if verbs := asked(seam); !slices.Equal(verbs, []string{"team moved", "team join"}) {
		t.Fatalf("the child's adapter calls: %v", verbs)
	}
	if exists(retryStampPath(f.stateDir, f.pid)) {
		t.Fatalf("the child left the session's retry stamp")
	}
	got := f.prompted()
	if len(got) < 2 || got[0] != followedLine(tf, &before, true, "brigade") || !strings.Contains(got[1], "(brigade-sess-1)") {
		t.Fatalf("the prompt after the follow: %q, want the follow's line and then the context line", got)
	}
	if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") {
		t.Fatalf("the session attached under %q", m.TeamKey)
	}
	if exists(config.MoveFollowedMark(f.stateDir, f.configDir, f.canon())) {
		t.Fatalf("the follow's line was said and its mark is still there")
	}
}

// TestSessionStartSaysTheLineOfAFollowItDidNotMake: the session ends
// between the two steps. Its checkout followed, with nobody told, so the
// next start there — an ordinary one, the pin matching the file — says the
// follow's line before any other, asks the old backend nothing, and offers
// the new backend no session of the old one as a resume hint. The start
// after that says nothing of the move.
func TestSessionStartSaysTheLineOfAFollowItDidNotMake(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	before := f.checkoutPin()
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	if exit, _, errOut := f.run(SubSessionEnd, f.endDoc("exit")); exit != 0 || f.mapExists() {
		t.Fatalf("session end: exit %d, map %v: %s", exit, f.mapExists(), errOut)
	}
	if exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
		t.Fatalf("the session's end left the team-moved stamp")
	}
	// The same conversation is resumed in a new process: its by-native map
	// names the session it had on the backend the team left.
	asks := len(seam.callsFor("team moved"))
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("resume"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	got := lines(out)
	if len(got) < 2 || got[0] != followedLine(tf, &before, true, "brigade") || !strings.Contains(got[1], "(brigade-sess-2)") {
		t.Fatalf("the start after the follow: %q, want the follow's line and then the context line", got)
	}
	if len(seam.callsFor("team moved")) != asks {
		t.Fatalf("an ordinary start asked the old backend about the move")
	}
	regs := seam.callsFor("session register")
	if reg := registration(t, regs[len(regs)-1]); reg.Resume != nil {
		t.Fatalf("the start offered the new backend %q, a session of the old one, as a resume hint", reg.Resume.SessionID)
	}
	if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the map after the start: %+v", m)
	}
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || strings.Contains(out, "followed") {
		t.Fatalf("a later start: exit %d, lines %q", exit, lines(out))
	}
	// A mark that speaks of a follow to another backend is not this pin's,
	// and its line is not said.
	stale := mustJSON(followedMark{Version: followedMarkVersion, TeamKey: teamstore.Key("rest", "https://elsewhere.example.test", "team-1"), Joined: true, FollowedAt: f.now})
	if err := adapterkit.WriteAtomic(config.MoveFollowedMark(f.stateDir, f.configDir, f.canon()), stale); err != nil {
		t.Fatal(err)
	}
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || strings.Contains(out, "followed") {
		t.Fatalf("a start beside a mark of another follow: exit %d, lines %q", exit, lines(out))
	}
}

// TestClearAfterAMoveRegistersAnew: /clear fires the session start again in
// the same process, so a running session follows there in one run. The
// session its map names is on the backend the team left: the hook must
// neither heartbeat that id on the new backend — before work item 327 it
// did, and any answer but not_found left a map naming the new backend and
// the old session — nor offer it as a resume hint. It registers anew, the
// map names the new backend and the session that backend issued, a watcher
// is started for it, and a request for quiet made in the session is kept.
func TestClearAfterAMoveRegistersAnew(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	// A heartbeat, were one sent, would be answered with a failure that is
	// not not_found: the answer that used to leave the map broken.
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"session heartbeat": {errResp(protocol.CodeUnavailable, "network")},
	})
	before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
	quiet := f.mustMap()
	quiet.SetInboundRequest(protocol.InboundHold)
	if err := f.store().WriteByPID(quiet); err != nil {
		t.Fatal(err)
	}
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	f.nativeID = "8d1f0a52-6c3b-4f7e-9a41-2b5c7d9e0f13" // /clear begins another conversation
	spawns := f.spawner.count()
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	got := lines(out)
	want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "hold")}
	if len(got) < 2 || !slices.Equal(got[:2], want) {
		t.Fatalf("lines = %q, want %q first", got, want)
	}
	if hb := seam.callsFor("session heartbeat"); len(hb) != 0 {
		t.Fatalf("the hook heartbeated %d time(s), under %q", len(hb), profileOf(hb[0]))
	}
	regs := seam.callsFor("session register")
	reg := registration(t, regs[len(regs)-1])
	if profileOf(regs[len(regs)-1]) != newKey || reg.Resume != nil || reg.Inbound != protocol.InboundHold {
		t.Fatalf("the registration: under %q, resume %v, inbound %q", profileOf(regs[len(regs)-1]), reg.Resume, reg.Inbound)
	}
	m := f.mustMap()
	if m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || m.ClaudeSessionID != f.nativeID ||
		m.Inbound != protocol.InboundHold || m.InboundRequest != protocol.InboundHold || m.InboundDecided != protocol.InboundAccept {
		t.Fatalf("the map after /clear: %+v", m)
	}
	if f.spawner.count() != spawns+1 || f.spawner.lastMap(t).BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("%d watchers started, want one, for the new session", f.spawner.count()-spawns)
	}
	if verbs := asked(seam); !slices.Equal(verbs[len(verbs)-3:], []string{"team moved", "team join", "session register"}) {
		t.Fatalf("adapter calls: %v", verbs)
	}
}

// TestAMapOfAnotherProcessGivesNoRequest: the request for quiet is carried
// to the new backend only from a map that is this process's beyond doubt.
// A map with no live watcher that names another conversation may be one a
// dead process left under the same pid, and a session that starts over it
// starts without a request.
func TestAMapOfAnotherProcessGivesNoRequest(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	stale := f.mustMap()
	stale.SetInboundRequest(protocol.InboundHold)
	if err := f.store().WriteByPID(stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.pidfilePath()); err != nil { // its watcher is gone
		t.Fatal(err)
	}
	f.moveTeamFile("team-1")
	f.nativeID = "8d1f0a52-6c3b-4f7e-9a41-2b5c7d9e0f13"
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	regs := seam.callsFor("session register")
	if reg := registration(t, regs[len(regs)-1]); reg.Inbound != protocol.InboundAccept || reg.Resume != nil {
		t.Fatalf("the registration: inbound %q, resume %v", reg.Inbound, reg.Resume)
	}
	if m := f.mustMap(); m.BrigadeSessionID != "brigade-sess-2" || m.InboundRequest != "" || m.Inbound != protocol.InboundAccept {
		t.Fatalf("the map: %+v", m)
	}
}

// TestAQuietSessionStaysQuietWhenItMoves: a session that asked not to be
// messaged (`brigade inbound hold`) is registered on the new backend as
// holding, and its map keeps the request, so `brigade inbound accept` still
// returns it to what the hook decided.
func TestAQuietSessionStaysQuietWhenItMoves(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	before := f.checkoutPin()
	quiet := f.mustMap()
	quiet.SetInboundRequest(protocol.InboundHold)
	if err := f.store().WriteByPID(quiet); err != nil {
		t.Fatal(err)
	}
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	got := f.prompted()
	want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "hold")}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	regs := seam.callsFor("session register")
	if reg := registration(t, regs[len(regs)-1]); reg.Inbound != protocol.InboundHold {
		t.Fatalf("the session registered on the new backend as %q", reg.Inbound)
	}
	m := f.mustMap()
	if m.BrigadeSessionID != "brigade-sess-2" || m.Inbound != protocol.InboundHold || m.InboundRequest != protocol.InboundHold || m.InboundDecided != protocol.InboundAccept {
		t.Fatalf("the map: %+v", m)
	}
	if w, err := config.FromWatcherEnv(f.spawner.last(t).Env); err != nil || string(w.TeamInbound) != protocol.InboundHold {
		t.Fatalf("the new watcher runs under %q, %v", w.TeamInbound, err)
	}
}

// TestHeldMessagesKeepASessionWhereItIs: messages that wait for the session
// on the backend it is leaving stay there, so the session stays with them.
// The member is still told at once that the checkout followed, and told
// once why the session has not; a message that was released and is not yet
// delivered keeps it too; and with nothing left waiting the next prompt
// moves it, without saying the follow's line a second time.
func TestHeldMessagesKeepASessionWhereItIs(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	before := f.checkoutPin()
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	entries := heldEntries(2, "payments-api", "ci-runner")
	save := func(entries []inbound.PendingEntry) {
		t.Helper()
		if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: entries, UpdatedAt: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	save(entries)
	registers := len(seam.callsFor("session register"))
	stays := func(when string) {
		t.Helper()
		if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || len(seam.callsFor("session register")) != registers {
			t.Fatalf("%s: the session moved with messages held for it: %+v", when, m)
		}
	}
	got := f.prompted()
	want := []string{followedLine(tf, &before, true, "brigade"), moveWaitsLine(2), inbound.HeldNotice(entries)}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q\nwant %q", got, want)
	}
	stays("the prompt after the follow")
	if exists(config.MoveFollowedMark(f.stateDir, f.configDir, f.canon())) {
		t.Fatalf("the follow's line was said and its mark is still there")
	}
	if got := f.prompted(); !slices.Equal(got, []string{inbound.HeldNotice(entries)}) {
		t.Fatalf("a later prompt said more than the held notice: %q", got)
	}
	stays("a later prompt")
	// Both released, neither delivered yet: nothing is held for review, and
	// the session still waits for them.
	entries[0].ReleasedAt, entries[1].ReleasedAt = fixedTime.Add(time.Minute), fixedTime.Add(time.Minute)
	save(entries)
	if got := f.prompted(); len(got) != 0 {
		t.Fatalf("a prompt with the messages released and not delivered: %q", got)
	}
	stays("with the messages released and not delivered")
	// Delivered.
	save(nil)
	got = f.prompted()
	if !slices.Equal(got, []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) {
		t.Fatalf("the prompt with nothing left waiting: %q", got)
	}
	if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the map: %+v", m)
	}
}

// TestAnUnreachableNewBackendLeavesTheSessionWhereItIs: the checkout
// followed and the new backend cannot be reached when the session's prompt
// tries to register there. The map is untouched and no watcher is started,
// so the session goes on where it was; it is told that the checkout
// followed and, once, that it could not be registered, never that it is
// `not connected`; the attempt is not repeated at the next prompt and is
// made again a minute later.
func TestAnUnreachableNewBackendLeavesTheSessionWhereItIs(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"session register": {
			okResp(registerDoc("brigade-sess-1", "payments-api", false)),
			errResp(protocol.CodeUnavailable, "network"),
			okResp(registerDoc("brigade-sess-2", "payments-api", false)),
		},
	})
	before := f.checkoutPin()
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
	got := f.prompted()
	if !slices.Equal(got, []string{followedLine(tf, &before, true, "brigade"), moveUnreachedLine}) {
		t.Fatalf("lines = %q", got)
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || f.spawner.count() != spawns {
		t.Fatalf("the map %+v, %d watchers started: want the session where it was", m, f.spawner.count()-spawns)
	}
	// Its watcher was not stopped to make room for one that never came.
	if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry.BrigadeSessionID != "brigade-sess-1" || !alive(f.spawner.watcherPID) {
		t.Fatalf("the old session's watcher after the failed registration: %+v, %v, alive %v", entry, err, alive(f.spawner.watcherPID))
	}
	if n := len(seam.callsFor("session register")); n != registers+1 {
		t.Fatalf("%d registrations attempted, want 1", n-registers)
	}
	// The next prompt, within the minute: no attempt, no line.
	if got := f.prompted(); len(got) != 0 || len(seam.callsFor("session register")) != registers+1 {
		t.Fatalf("a prompt within the minute: lines %q, %d registrations", got, len(seam.callsFor("session register"))-registers)
	}
	// A minute later the attempt is made again, and the session moves.
	f.now = f.now.Add(registerRetryInterval)
	got = f.prompted()
	if !slices.Equal(got, []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) {
		t.Fatalf("the prompt a minute later: %q", got)
	}
	if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the map: %+v", m)
	}
	if exists(retryStampPath(f.stateDir, f.pid)) || exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
		t.Fatalf("the prompt that moved the session left a stamp of the move")
	}
}

// TestAFollowThatWasStoppedIsCompletedByTheNext: the child is stopped
// after the new backend admitted this machine and before it wrote the pin.
// What it leaves is laid out here by hand: the membership at the new
// backend, its ticket file, the stamp of its attempt and, when it got that
// far, its mark. The follow a later prompt starts asks the old backend
// again, joins nothing, sweeps the ticket and writes the pin, and the
// prompt after that moves the session. Its line says that the machine
// joined with the ticket when the stopped follow left word of that, and
// that it had joined there already when it did not.
func TestAFollowThatWasStoppedIsCompletedByTheNext(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		marked bool
	}{
		{"stopped before it left its mark", false},
		{"stopped between its mark and the pin", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			if err := adapterkit.SaveProfile(f.configDir, newKey, &adapterkit.Profile{
				Version: adapterkit.ProfileVersion, Adapter: "rest", URL: movedURL, Certificate: movedPin,
				TeamRef: "team-1", TeamName: teamName, PrincipalRef: "p-new",
			}); err != nil {
				t.Fatal(err)
			}
			if err := adapterkit.WriteTicketFile(filepath.Join(f.configDir, ".move-ticket-0011223344556677"), moveTicket); err != nil {
				t.Fatal(err)
			}
			attempt := config.MoveAttemptStamp(f.stateDir, f.configDir, f.canon())
			if err := adapterkit.MkdirPrivate(filepath.Dir(attempt)); err != nil {
				t.Fatal(err)
			}
			if err := adapterkit.WriteAtomic(attempt, []byte(f.now.Format(time.RFC3339Nano)+"\n")); err != nil {
				t.Fatal(err)
			}
			if tc.marked {
				mark := mustJSON(followedMark{Version: followedMarkVersion, TeamKey: newKey, Joined: true, FollowedAt: f.now})
				if err := adapterkit.WriteAtomic(config.MoveFollowedMark(f.stateDir, f.configDir, f.canon()), mark); err != nil {
					t.Fatal(err)
				}
			}
			// Within the minute of the attempt that was stopped, no other is made.
			calls := len(asked(seam))
			if got := f.prompted(); !slices.Equal(got, []string{movedLine}) || len(f.spawner.followed()) != 0 {
				t.Fatalf("a prompt within the minute: lines %q, %d follows", got, len(f.spawner.followed()))
			}
			f.now = f.now.Add(followInterval)
			if got := f.prompted(); len(got) != 0 || len(f.spawner.followed()) != 1 {
				t.Fatalf("the prompt a minute later: lines %q, %d follows", got, len(f.spawner.followed()))
			}
			if verbs := asked(seam)[calls:]; !slices.Equal(verbs, []string{"team moved"}) {
				t.Fatalf("the follow that completed it ran %v, want the one question and no join", verbs)
			}
			if pin := f.checkoutPin(); !pin.Matches(tf) || pin.MovedFrom != before.URL {
				t.Fatalf("the pin: %+v", pin)
			}
			f.noTicketLeft()
			got := f.prompted()
			want := []string{followedLine(tf, &before, tc.marked, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
			if !slices.Equal(got, want) {
				t.Fatalf("lines = %q\nwant %q", got, want)
			}
		})
	}
}

// TestARegistrationThatWasNotRecordedIsLeftToItsLease: the hook is stopped
// after the new backend registered the session and before the map was
// written. The stop is made here by taking the map's directory away at the
// moment of the registration. The map names the old backend still, so the
// next prompt registers again and the map then names that registration:
// the first is an orphan nothing heartbeats, resumes or closes, and it
// asked for a lease, with which it ends.
func TestARegistrationThatWasNotRecordedIsLeftToItsLease(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"session register": {
			okResp(registerDoc("brigade-sess-1", "payments-api", false)),
			okResp(registerDoc("brigade-sess-orphan", "payments-api", false)),
			okResp(registerDoc("brigade-sess-2", "payments-api", false)),
		},
	})
	newKey := teamstore.Key("rest", movedURL, "team-1")
	f.moveTeamFile("team-1")
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	mapPath, err := f.store().ByPIDPath(f.key())
	if err != nil {
		t.Fatal(err)
	}
	inner, stopped := f.deps.Spawn, false
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) == "session register" && !stopped {
			stopped = true
			if err := os.Chmod(filepath.Dir(mapPath), 0o500); err != nil { //nolint:gosec // G302: a directory, made unwritable on purpose
				t.Error(err)
			}
		}
		return inner(ctx, spec)
	}
	spawns := f.spawner.count()
	f.prompted()
	if err := os.Chmod(filepath.Dir(mapPath), 0o700); err != nil { //nolint:gosec // G302: the directory's own mode again
		t.Fatal(err)
	}
	if !stopped {
		t.Fatalf("the prompt did not register on the new backend")
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || f.spawner.count() != spawns {
		t.Fatalf("the map after the registration that was not recorded: %+v, %d watchers started", m, f.spawner.count()-spawns)
	}
	regs := seam.callsFor("session register")
	if reg := registration(t, regs[len(regs)-1]); profileOf(regs[len(regs)-1]) != newKey || reg.LeaseSeconds == nil || *reg.LeaseSeconds <= 0 {
		t.Fatalf("the orphan's registration asked for no lease")
	}
	// A hook that was stopped wrote no retry stamp: the next prompt tries.
	_ = os.Remove(retryStampPath(f.stateDir, f.pid))
	calls := len(seam.verbs())
	f.prompted()
	if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the map after the next prompt: %+v", m)
	}
	regs = seam.callsFor("session register")
	if reg := registration(t, regs[len(regs)-1]); reg.Resume != nil {
		t.Fatalf("the next registration resumed %q", reg.Resume.SessionID)
	}
	for _, verb := range seam.verbs()[calls:] {
		if verb == "session heartbeat" || verb == "session close" {
			t.Fatalf("the hook ran %s; the orphan is left to its lease", verb)
		}
	}
}

// TestASessionMovesWithItsMembersOwnJoin: a member who turned the follow
// off, or whose team has no record of its move, joins the new backend in
// the checkout with the secret file. The pin is then their own consent, and
// the running session moves at its next prompt: nothing is followed, so no
// child is started, the old backend is asked nothing, and the line says
// what happened.
func TestASessionMovesWithItsMembersOwnJoin(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	env := []string{config.OptionFollowTeamMove + "=off"}
	seam := attachedMember(t, f, nil, nil, env...)
	newKey := teamstore.Key("rest", movedURL, "team-1")
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(env...); !slices.Equal(got, []string{movedStaysLine}) {
		t.Fatalf("lines = %q", got)
	}
	// What `brigade team join --secret-file <path>` writes: the membership,
	// and a pin that moved from nowhere.
	if err := adapterkit.SaveProfile(f.configDir, newKey, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "rest", URL: movedURL, Certificate: movedPin,
		TeamRef: "team-1", TeamName: teamName, PrincipalRef: "p-new",
	}); err != nil {
		t.Fatal(err)
	}
	pins := map[string]any{"version": 1, "projects": map[string]any{f.canon(): map[string]any{
		"adapter": "rest", "url": movedURL, "certificate": movedPin, "team_ref": "team-1", "consented_at": "2026-11-14T12:00:00Z",
	}}}
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), mustJSON(pins)); err != nil {
		t.Fatal(err)
	}
	calls := len(asked(seam))
	got := f.prompted(env...)
	want := []string{rejoinedLine(tf), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q\nwant %q", got, want)
	}
	if !strings.Contains(got[0], "brigade.example.test") || !strings.Contains(got[0], movedPin) {
		t.Fatalf("the line does not name where the session went: %q", got[0])
	}
	if verbs := asked(seam)[calls:]; !slices.Equal(verbs, []string{"session register"}) || len(f.spawner.followed()) != 0 {
		t.Fatalf("adapter calls %v, %d follows: want the one registration and no follow", verbs, len(f.spawner.followed()))
	}
	if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the map: %+v", m)
	}
}

// TestASessionStaysWhenItsCheckoutIsOfAnotherTeam: a team file that names
// ANOTHER team is not a move, whatever backend it names: a prompt says
// nothing and starts nothing. And a member who joins that other team in
// the checkout has consented to it for the sessions they start next: the
// one that is running belongs to the team it registered with, and stays.
func TestASessionStaysWhenItsCheckoutIsOfAnotherTeam(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	f.moveTeamFile("team-2")
	stays := func(when string) {
		t.Helper()
		calls := len(seam.verbs())
		if got := f.prompted(); len(got) != 0 || len(f.spawner.followed()) != 0 || len(seam.verbs()) != calls {
			t.Fatalf("%s: lines %q, %d follows, adapter calls %v", when, got, len(f.spawner.followed()), seam.verbs()[calls:])
		}
		if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" {
			t.Fatalf("%s: the session moved to another team: %+v", when, m)
		}
		if exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
			t.Fatalf("%s: the team-moved stamp was written", when)
		}
	}
	stays("a file that names another team")
	if err := adapterkit.SaveProfile(f.configDir, teamstore.Key("rest", movedURL, "team-2"), &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "rest", URL: movedURL, Certificate: movedPin,
		TeamRef: "team-2", TeamName: teamName, PrincipalRef: "p-other",
	}); err != nil {
		t.Fatal(err)
	}
	pins := map[string]any{"version": 1, "projects": map[string]any{f.canon(): map[string]any{
		"adapter": "rest", "url": movedURL, "certificate": movedPin, "team_ref": "team-2", "consented_at": "2026-11-14T12:00:00Z",
	}}}
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), mustJSON(pins)); err != nil {
		t.Fatal(err)
	}
	stays("a checkout joined to another team")
}

// TestAMuseOrCodexSessionFollowsInTwoSteps: a Muse Code or Codex CLI hook
// finds its session through its parent process, which the child does not
// have, so the child is given what it needs as arguments: the checkout, the
// store, the state directory and the session's key. Nothing in its
// environment names the session. The second prompt moves the session, the
// pointer its shell finds it by included.
func TestAMuseOrCodexSessionFollowsInTwoSteps(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostMuse, config.HostCodex} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			f := movingFixture(t, host)
			f.noAdapterOption = true
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
				t.Fatalf("lines = %q", got)
			}
			follows := f.spawner.followed()
			if len(follows) != 1 {
				t.Fatalf("%d follow children started", len(follows))
			}
			wantArgs := []string{"hook", "follow", "--checkout", f.canon(), "--config-dir", f.configDir, "--state-dir", f.stateDir,
				"--key", strconv.Itoa(f.key()), "--log-level", "info"}
			if !slices.Equal(follows[0].Args, wantArgs) {
				t.Fatalf("the child's arguments: %q\nwant %q", follows[0].Args, wantArgs)
			}
			for _, entry := range follows[0].Env {
				if name, _, _ := strings.Cut(entry, "="); strings.HasPrefix(name, "MUSE_") || strings.HasPrefix(name, "CODEX_") || strings.HasPrefix(name, "BRIGADE_") {
					t.Errorf("the child's environment carries %s", name)
				}
			}
			if pin := f.checkoutPin(); !pin.Matches(tf) {
				t.Fatalf("the child did not follow: %+v", pin)
			}
			got := f.prompted()
			if len(got) < 2 || got[0] != followedLine(tf, &before, true, f.pluginBin) || !strings.Contains(got[1], "(brigade-sess-2)") {
				t.Fatalf("the second prompt: %q", got)
			}
			m := f.mustMap()
			if m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || m.Harness != string(host) {
				t.Fatalf("the map: %+v", m)
			}
			if p, err := f.store().ReadByHost(string(host), f.nativeID); err != nil || p.BrigadeSessionID != "brigade-sess-2" || p.HostPID != f.key() {
				t.Fatalf("the session's pointer: %+v, %v", p, err)
			}
		})
	}
}

// TestAGrokSessionStaysWhereItIs: Grok Build discards what a prompt hook
// prints, so a session there could be told neither that it followed nor
// the id it has afterwards. Its prompt starts no follow and moves nothing,
// even when the checkout is joined to the backend the file names.
func TestAGrokSessionStaysWhereItIs(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	if code, _, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil)); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	before := f.mustMap()
	// The checkout is joined to the server, for the team the session's map
	// names, as a member's own join would leave it.
	f.moveTeamFile(before.TeamRef)
	newKey := teamstore.Key("rest", movedURL, before.TeamRef)
	if err := adapterkit.SaveProfile(f.configDir, newKey, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "rest", URL: movedURL, Certificate: movedPin,
		TeamRef: before.TeamRef, TeamName: teamName, PrincipalRef: "p-new",
	}); err != nil {
		t.Fatal(err)
	}
	pins := map[string]any{"version": 1, "projects": map[string]any{f.canon(): map[string]any{
		"adapter": "rest", "url": movedURL, "certificate": movedPin, "team_ref": before.TeamRef, "consented_at": "2026-11-14T12:00:00Z",
	}}}
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), mustJSON(pins)); err != nil {
		t.Fatal(err)
	}
	registers := strings.Count(strings.Join(f.dumpVerbs(), ","), "session register")
	if code, _, errOut := grokRun(f, SubPrompt, grokDoc(f, "user_prompt_submit", "", nil)); code != 0 {
		t.Fatalf("prompt exit %d: %s", code, errOut)
	}
	if m := f.mustMap(); m.TeamKey != before.TeamKey || m.BrigadeSessionID != before.BrigadeSessionID {
		t.Fatalf("the Grok session moved: %+v", m)
	}
	if n := strings.Count(strings.Join(f.dumpVerbs(), ","), "session register"); n != registers || len(f.spawner.followed()) != 0 {
		t.Fatalf("a Grok prompt registered %d time(s) and started %d follows", n-registers, len(f.spawner.followed()))
	}
}

// TestHookFollowIsNotRunInsideASession: the child is the prompt hook's
// own, started with no session in its environment. Run where there is one
// — from a session's shell, where the option that turns the follow off
// cannot be read — it refuses with `usage` and asks no backend anything;
// and it refuses arguments that are not the three absolute paths it needs.
func TestHookFollowIsNotRunInsideASession(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.labelOldBinding()
	f.moveTeamFile("team-1")
	seam := f.useMoveSeam(movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{"team join": {joinOK()}})
	pinBefore := f.checkoutPin()
	args := []string{SubFollow, "--checkout", f.canon(), "--config-dir", f.configDir, "--state-dir", f.stateDir, "--key", strconv.Itoa(f.pid)}
	run := func(args, env []string) (int, string) {
		var out, errOut bytes.Buffer
		code := Run(args, cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, env, f.deps)
		if out.Len() != 0 {
			t.Errorf("hook follow wrote to stdout: %q", out.String())
		}
		return code, errOut.String()
	}
	outside := []string{"PATH=" + f.emptyPath(), "HOME=" + f.dirs.Home}
	for _, marker := range []string{"CLAUDE_PID=" + strconv.Itoa(f.pid), "MUSE_SESSION_ID=" + f.nativeID, "CODEX_THREAD_ID=" + f.nativeID, "GROK_SESSION_ID=" + f.nativeID} {
		if code, errOut := run(args, append(slices.Clone(outside), marker)); code != protocol.CodeUsage.Exit() || !strings.Contains(errOut, "not run inside a session") {
			t.Fatalf("inside a session (%s): exit %d: %s", marker, code, errOut)
		}
	}
	for _, bad := range [][]string{
		{SubFollow},
		{SubFollow, "--checkout", f.canon(), "--config-dir", f.configDir},
		{SubFollow, "--checkout", "checkout", "--config-dir", f.configDir, "--state-dir", f.stateDir},
		{SubFollow, "--checkout", f.canon(), "--config-dir", "config", "--state-dir", f.stateDir},
		append(slices.Clone(args), "--host", "codex"),
		append(slices.Clone(args[:len(args)-1]), "0"),
	} {
		if code, _ := run(bad, outside); code != protocol.CodeUsage.Exit() {
			t.Fatalf("hook %q: exit %d, want usage", bad, code)
		}
	}
	// A checkout that is not named by its canonical path is followed
	// nowhere: the stamps and the pin are keyed by that path.
	if code, _ := run([]string{SubFollow, "--checkout", f.canon() + "/../checkout", "--config-dir", f.configDir, "--state-dir", f.stateDir}, outside); code != 0 {
		t.Fatalf("hook follow for a checkout not named canonically: exit %d", code)
	}
	if got := seam.verbs(); len(got) != 0 || f.checkoutPin() != pinBefore {
		t.Fatalf("a refused follow ran %v and left the pin %+v", got, f.checkoutPin())
	}
	// The control: the same arguments outside a session follow.
	code, errOut := run(args, outside)
	if pin := f.checkoutPin(); code != 0 || !pin.Matches(f.movedFile()) {
		t.Fatalf("hook follow outside a session: exit %d, pin %+v\n%s", code, pin, errOut)
	}
}
