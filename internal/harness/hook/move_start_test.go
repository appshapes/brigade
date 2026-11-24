//go:build !windows

package hook

import (
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// A session's start in a process whose session is running where its team
// left it (work item 327, ADR 0008): `/clear` fires the start again, and
// what keeps a prompt from moving the session keeps a start from it too.
// The session stays where it is, is told what a prompt would tell it, and
// is never told that it is `not connected`.

// otherConversation is the native id of the conversation a /clear begins.
const otherConversation = "8d1f0a52-6c3b-4f7e-9a41-2b5c7d9e0f13"

// keptWhereItWas asserts that the fixture's session is still the one it
// registered on the team's first backend, served by the watcher it had,
// and that its map names the conversation the last hook document named.
func (f *fixture) keptWhereItWas(when string) {
	f.t.Helper()
	m := f.mustMap()
	if m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || m.ClaudeSessionID != f.nativeID || m.Checkout != f.canon() {
		f.t.Fatalf("%s: the map names %q, %q, the conversation %q and the checkout %q", when, m.TeamKey, m.BrigadeSessionID, m.ClaudeSessionID, m.Checkout)
	}
	if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry.BrigadeSessionID != "brigade-sess-1" || !alive(f.spawner.watcherPID) {
		f.t.Fatalf("%s: the session's watcher: %+v, %v, alive %v", when, entry, err, alive(f.spawner.watcherPID))
	}
	if bn, err := f.store().ReadByNative(f.nativeID); err != nil || bn.BrigadeSessionID != "brigade-sess-1" || bn.TeamKey != f.teamKey {
		f.t.Fatalf("%s: the conversation's resume hint: %+v, %v", when, bn, err)
	}
}

// heartbeatsOn are the heartbeats the seam saw under the team key of one
// backend.
func heartbeatsOn(seam *adapterSeam, key string) []adapterCall {
	var out []adapterCall
	for _, c := range seam.callsFor("session heartbeat") {
		if profileOf(c) == key {
			out = append(out, c)
		}
	}
	return out
}

// TestAStartThatKeepsASessionTellsItsBackendOfTheConversationThatBegins: a
// start that keeps a running session where it is, because messages wait
// for it there or because the start comes from another checkout, is still
// a start: `/clear` began another conversation. The map takes what a
// conversation changes (its id, the name the session has now, the
// permission mode, the transcript) and nothing of the backend. And the
// backend the session is on is told, as a start that continues tells it:
// one heartbeat, under the team key the map names and for the id it
// issued, with the name and, when the conversation changed, an empty doing
// line, because a doing line lives inside one conversation. The backend
// the team went to is sent nothing.
func TestAStartThatKeepsASessionTellsItsBackendOfTheConversationThatBegins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// foreign: the start comes from another checkout of the team, one
		// that is on the server; otherwise from the session's own, which
		// followed, with a message waiting.
		foreign bool
		// same: the start names the conversation the map names.
		same bool
	}{
		{"messages wait for it, and another conversation begins", false, false},
		{"the start comes from another checkout, and another conversation begins", true, false},
		{"messages wait for it, and the conversation is the one it was", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := memberSeam(t, f, movedDoc(movedURL, movedPin), nil)
			// Both backends keep a doing line.
			var described protocol.DescribeResult
			if err := json.Unmarshal(seam.describe, &described); err != nil {
				t.Fatal(err)
			}
			described.Capabilities = append(described.Capabilities, doingCapability)
			seam.describe = mustJSON(&described)
			startMember(t, f)
			home, newKey := f.cwd, teamstore.Key("rest", movedURL, "team-1")
			from := home
			if tc.foreign {
				f.memberOf("team-1")
				from, _ = f.addCheckout("followed", serverFile("team-1"), serverPin("team-1", f.checkoutPin().URL))
			} else {
				f.moveTeamFile("team-1")
				if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
					t.Fatalf("lines = %q", got)
				}
				if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: heldEntries(1, "payments-api"), UpdatedAt: fixedTime}); err != nil {
					t.Fatal(err)
				}
			}
			// The session was renamed since it started.
			f.deps.Registry = fakeregistry.New(t, map[int]string{f.pid: fakeregistry.Observed(f.pid, "renamed", "busy", f.socket)})
			if !tc.same {
				f.nativeID = otherConversation
			}
			beats := len(seam.callsFor("session heartbeat"))
			exit, out, errOut := f.run(SubSessionStart, f.doc(map[string]any{
				"session_id": f.nativeID, "cwd": from, "hook_event_name": "SessionStart", "source": "clear",
				"permission_mode": "plan", "transcript_path": "/another/transcript.jsonl",
			}))
			if exit != 0 || strings.Contains(out, "not connected") {
				t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
			}
			m := f.mustMap()
			if m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || m.Checkout != f.canon() {
				t.Fatalf("the start did not keep the session: %+v", m)
			}
			if m.ClaudeSessionID != f.nativeID || m.SessionName != "renamed" || m.PermissionMode != "plan" || m.TranscriptPath != "/another/transcript.jsonl" {
				t.Fatalf("the map after the start names the conversation %q, the name %q, the mode %q and the transcript %q", m.ClaudeSessionID, m.SessionName, m.PermissionMode, m.TranscriptPath)
			}
			if !slices.Contains(lines(out), startLine("renamed", "brigade-sess-1", teamName, m.Inbound)) {
				t.Fatalf("the start's lines: %q", lines(out))
			}
			sent := seam.callsFor("session heartbeat")[beats:]
			if len(sent) != 1 || profileOf(sent[0]) != f.teamKey || !slices.Contains(sent[0].Argv, "brigade-sess-1") || len(heartbeatsOn(seam, newKey)) != 0 {
				t.Fatalf("the start sent %d heartbeat(s), %d of them to the backend the team went to: want one, to the backend the session is on, for its id", len(sent), len(heartbeatsOn(seam, newKey)))
			}
			var hb protocol.HeartbeatRequest
			if err := json.Unmarshal(sent[0].Stdin, &hb); err != nil {
				t.Fatalf("the heartbeat's document: %v", err)
			}
			if hb.SessionName == nil || *hb.SessionName != "renamed" || hb.Inbound == nil || *hb.Inbound != m.Inbound {
				t.Fatalf("the heartbeat's name and policy: %s", sent[0].Stdin)
			}
			// A doing line ends with its conversation, and only then.
			if blanked := hb.SessionDescription != nil && *hb.SessionDescription == ""; blanked == tc.same {
				t.Fatalf("the heartbeat blanked the doing line: %v; the conversation is the same: %v\n%s", blanked, tc.same, sent[0].Stdin)
			}
		})
	}
}

// TestAStartKeepsARunningSessionWhoseMessagesWait: messages wait for the
// session on the backend its team left, and `/clear` fires its start. A
// start that registered it anew would leave them behind: the map would name
// another session, and no `brigade inbox` lists the messages of a session
// no map names. So the start keeps the session, as a prompt does: no
// registration, the watcher it had, the map naming the same session for
// the conversation the /clear began. It says how the checkout got to the
// new backend and why the session has not moved, then the context line of
// the session where it is. The prompts after it repeat neither line, and
// the one after the messages are gone moves the session.
func TestAStartKeepsARunningSessionWhoseMessagesWait(t *testing.T) {
	t.Parallel()
	held := func(t *testing.T, f *fixture, entries []inbound.PendingEntry) {
		t.Helper()
		if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: entries, UpdatedAt: fixedTime}); err != nil {
			t.Fatal(err)
		}
	}
	// after is the rest of both cases: the start kept the session, and the
	// prompts that follow.
	after := func(t *testing.T, f *fixture, seam *adapterSeam, entries []inbound.PendingEntry, registers, spawns int) {
		t.Helper()
		// The backend the team went to has never heard of the session's id:
		// nothing is registered there and nothing heartbeated.
		if n := len(seam.callsFor("session register")); n != registers || f.spawner.count() != spawns || len(heartbeatsOn(seam, teamstore.Key("rest", movedURL, "team-1"))) != 0 {
			t.Fatalf("the start registered %d time(s), started %d watcher(s) and heartbeated the new backend %d time(s)", n-registers, f.spawner.count()-spawns, len(heartbeatsOn(seam, teamstore.Key("rest", movedURL, "team-1"))))
		}
		f.keptWhereItWas("after the start")
		if pending, err := pendingStoreOf(f).Load(); err != nil || len(pending.Entries) != len(entries) || f.mustMap().BrigadeSessionID != heldSession {
			t.Fatalf("the messages that wait: %d entries, %v, under a map that names %q", len(pending.Entries), err, f.mustMap().BrigadeSessionID)
		}
		// The conversation has heard both lines: its prompts say the held
		// notice and nothing of the move.
		for range 2 {
			if got := f.prompted(); !slices.Equal(got, []string{inbound.HeldNotice(entries)}) {
				t.Fatalf("a prompt after the start: %q", got)
			}
		}
		f.keptWhereItWas("after the prompts")
		// Delivered: the next prompt moves the session, and says the line of
		// the follow no second time.
		held(t, f, nil)
		if got := f.prompted(); !slices.Equal(got, []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) {
			t.Fatalf("the prompt with nothing left waiting: %q", got)
		}
		if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" || m.ClaudeSessionID != f.nativeID {
			t.Fatalf("the map after the move: %+v", m)
		}
	}

	t.Run("the checkout had followed", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
			"session heartbeat": {errResp(protocol.CodeUnavailable, "network")},
		})
		before := f.checkoutPin()
		f.moveTeamFile("team-1")
		tf := f.movedFile()
		if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
			t.Fatalf("lines = %q", got)
		}
		entries := heldEntries(2, "payments-api", "ci-runner")
		held(t, f, entries)
		if got := f.prompted(); !slices.Equal(got, []string{followedLine(tf, &before, true, "brigade"), moveWaitsLine(2), inbound.HeldNotice(entries)}) {
			t.Fatalf("the prompt that found the messages: %q", got)
		}
		registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
		f.nativeID = otherConversation
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		if exit != 0 || strings.Contains(out, "not connected") {
			t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
		}
		// The mark went with the line the prompt said, so the start says the
		// line as one that found the machine joined.
		want := []string{followedLine(tf, &before, false, "brigade"), moveWaitsLine(2), startLine("payments-api", "brigade-sess-1", teamName, "accept")}
		if got := lines(out); !slices.Equal(got, want) {
			t.Fatalf("the start's lines:\n%q\nwant\n%q", got, want)
		}
		after(t, f, seam, entries, registers, spawns)
	})

	t.Run("the start follows in its own run", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
			"session heartbeat": {errResp(protocol.CodeUnavailable, "network")},
		})
		before := f.checkoutPin()
		entries := heldEntries(1, "payments-api")
		held(t, f, entries)
		f.moveTeamFile("team-1")
		tf := f.movedFile()
		registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
		f.nativeID = otherConversation
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		if exit != 0 || strings.Contains(out, "not connected") {
			t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
		}
		want := []string{followedLine(tf, &before, true, "brigade"), moveWaitsLine(1), startLine("payments-api", "brigade-sess-1", teamName, "accept")}
		if got := lines(out); !slices.Equal(got, want) {
			t.Fatalf("the start's lines:\n%q\nwant\n%q", got, want)
		}
		if pin := f.checkoutPin(); !pin.Matches(tf) || len(seam.callsFor("team join")) != 1 {
			t.Fatalf("the start did not follow: pin %+v, %d joins", pin, len(seam.callsFor("team join")))
		}
		after(t, f, seam, entries, registers, spawns)
	})

	// The control, and the rule's edge: a map that is not this process's
	// beyond doubt — no live watcher, another conversation — may be one a
	// dead process left under the same pid, whose messages are not this
	// session's to wait for. That start registers, as it always did.
	t.Run("a map that may be another process's keeps nobody", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
		f.moveTeamFile("team-1")
		if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
			t.Fatalf("lines = %q", got)
		}
		held(t, f, heldEntries(2, "payments-api", "ci-runner"))
		if err := os.Remove(f.pidfilePath()); err != nil { // its watcher is gone
			t.Fatal(err)
		}
		f.nativeID = otherConversation
		if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" {
			t.Fatalf("the map: %+v", m)
		}
	})
}

// TestAStartThatCannotReachTheNewBackendKeepsTheSession: the checkout
// followed, `/clear` fires the session's start, and the backend the team
// went to cannot be reached. The session is where it was, with its watcher
// and its map, so the start does not say `not connected`: it says what a
// prompt says, that the session could not be registered there and tries
// again, and then the context line of the session where it is. The attempt
// is stamped: the prompts of the next minute do not try, the one after it
// does, and moves the session.
func TestAStartThatCannotReachTheNewBackendKeepsTheSession(t *testing.T) {
	t.Parallel()
	unreachable := func() map[string][]fakeadapter.Response {
		return map[string][]fakeadapter.Response{
			"session register": {
				okResp(registerDoc("brigade-sess-1", "payments-api", false)),
				errResp(protocol.CodeUnavailable, "network"),
				okResp(registerDoc("brigade-sess-2", "payments-api", false)),
			},
		}
	}

	t.Run("a session that is running here", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		seam := attachedMember(t, f, movedDoc(movedURL, movedPin), unreachable())
		before := f.checkoutPin()
		f.moveTeamFile("team-1")
		tf := f.movedFile()
		if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
			t.Fatalf("lines = %q", got)
		}
		registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
		f.nativeID = otherConversation
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		if exit != 0 || strings.Contains(out, "not connected") {
			t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
		}
		want := []string{followedLine(tf, &before, true, "brigade"), moveUnreachedLine, startLine("payments-api", "brigade-sess-1", teamName, "accept")}
		if got := lines(out); !slices.Equal(got, want) {
			t.Fatalf("the start's lines:\n%q\nwant\n%q", got, want)
		}
		if n := len(seam.callsFor("session register")); n != registers+1 || f.spawner.count() != spawns {
			t.Fatalf("%d registrations attempted and %d watchers started, want one attempt and no watcher", n-registers, f.spawner.count()-spawns)
		}
		f.keptWhereItWas("after the start")
		if !exists(retryStampPath(f.stateDir, f.pid)) {
			t.Fatalf("the attempt that failed left no stamp")
		}
		// Within the minute no prompt tries, or says either line again.
		for range 2 {
			if got := f.prompted(); len(got) != 0 || len(seam.callsFor("session register")) != registers+1 {
				t.Fatalf("a prompt within the minute: lines %q, %d registrations", got, len(seam.callsFor("session register"))-registers)
			}
		}
		f.keptWhereItWas("within the minute")
		f.now = f.now.Add(registerRetryInterval)
		if got := f.prompted(); !slices.Equal(got, []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) {
			t.Fatalf("the prompt a minute later: %q", got)
		}
		if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" {
			t.Fatalf("the map: %+v", m)
		}
		if exists(retryStampPath(f.stateDir, f.pid)) || exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
			t.Fatalf("the prompt that moved the session left a stamp of the move")
		}
	})

	// The control: a start over a map that may be a dead process's is a
	// session that is attached to nothing, and it is told so.
	t.Run("a start that is not of a running session", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		attachedMember(t, f, movedDoc(movedURL, movedPin), unreachable())
		f.moveTeamFile("team-1")
		if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
			t.Fatalf("lines = %q", got)
		}
		if err := os.Remove(f.pidfilePath()); err != nil { // its watcher is gone
			t.Fatal(err)
		}
		f.nativeID = otherConversation
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
		if exit != 0 {
			t.Fatalf("exit %d: %s", exit, errOut)
		}
		if got := lines(out); len(got) != 2 || !strings.HasPrefix(got[1], "Brigade: not connected (unavailable)") || strings.Contains(out, moveUnreachedLine) {
			t.Fatalf("the start's lines: %q", got)
		}
	})
}

// TestAStartThatCannotWriteTheMapOfASessionItKeepsSaysItsLine: a start
// that keeps a running session writes its map again for the conversation
// that begins. Where that cannot be done the session is where it was, under
// the map it had, and the start says so: the lines of a session that
// stays, then its context line, and never that it is `not connected`. That
// holds when messages wait for it, and when its registration on the new
// backend was made and the new map could not be written, which is left to
// its lease there.
func TestAStartThatCannotWriteTheMapOfASessionItKeepsSaysItsLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		held bool
		why  func() string
	}{
		{"messages wait for it", true, func() string { return moveWaitsLine(1) }},
		{"the registration was made, and the map could not be written", false, func() string { return moveUnreachedLine }},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if tc.held {
				if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: heldEntries(1, "payments-api"), UpdatedAt: fixedTime}); err != nil {
					t.Fatal(err)
				}
			}
			first := f.nativeID
			mapPath, err := f.store().ByPIDPath(f.key())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(mapPath), 0o500); err != nil { //nolint:gosec // G302: a directory, made unwritable on purpose
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(mapPath), 0o700) }) //nolint:gosec // G302: the directory's own mode again
			registers := len(seam.callsFor("session register"))
			f.nativeID = otherConversation
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
			if exit != 0 || strings.Contains(out, "not connected") {
				t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
			}
			want := []string{followedLine(tf, &before, true, "brigade"), tc.why(), startLine("payments-api", "brigade-sess-1", teamName, "accept")}
			if got := lines(out); !slices.Equal(got, want) {
				t.Fatalf("the start's lines:\n%q\nwant\n%q", got, want)
			}
			// The map is the one the session had; a registration was made only
			// where nothing kept the session.
			if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || m.ClaudeSessionID != first {
				t.Fatalf("the map: %q, %q, the conversation %q", m.TeamKey, m.BrigadeSessionID, m.ClaudeSessionID)
			}
			if made := len(seam.callsFor("session register")) - registers; (made == 0) != tc.held {
				t.Fatalf("%d registrations were made; messages waited: %v", made, tc.held)
			}
		})
	}
}

// TestAStartWhoseNewAdapterCannotDescribeKeepsTheSession: a registration
// begins with the adapter's describe, and that can fail before the backend
// is asked anything: a profile the adapter cannot read, an adapter that
// does not start. It is the same start of the same running session, so it
// ends the same way as one whose backend could not be reached: the session
// is kept where it is and told that it could not be registered, never that
// it is `not connected`, and the attempt is stamped.
func TestAStartWhoseNewAdapterCannotDescribeKeepsTheSession(t *testing.T) {
	t.Parallel()
	// The server is one no other test names. The adapter client keeps one
	// describe per adapter and profile for the life of the process, and a
	// profile is a team key: under the address the other tests move to, a
	// describe one of them made would answer this start, and its adapter
	// would never be asked.
	const server = "https://cannot-describe.example.test"
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(server, movedPin), nil)
	before, newKey := f.checkoutPin(), teamstore.Key("rest", server, "team-1")
	moved := map[string]any{"version": 1, "adapter": "rest", "url": server, "certificate": movedPin, "team_ref": "team-1", "team_name": teamName}
	if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), mustJSON(moved), 0o600); err != nil {
		t.Fatal(err)
	}
	tf := f.movedFile()
	// The new backend's adapter cannot describe from the first: a follow
	// asks it what it offers once the machine has joined (work item 341),
	// and a describe that was answered would be kept for the start.
	var described atomic.Int32
	inner := f.deps.Spawn
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) == "describe" && profileOf(adapterCall{Argv: spec.Argv}) == newKey {
			described.Add(1)
			return nil, &protocol.Error{Code: protocol.CodeConfig, Message: "the profile cannot be read"}
		}
		return inner(ctx, spec)
	}
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	// The follow joined on the old backend's record, and its question to the
	// new backend's adapter, which could not describe, was left at that: the
	// checkout followed.
	if on, pin := newBackendCalls(seam, newKey), f.checkoutPin(); !slices.Equal(on, []string{"team join"}) || !pin.Matches(tf) {
		t.Fatalf("the follow beside an adapter that cannot describe: calls %v, pin %+v", on, pin)
	}
	followDescribed := described.Load()
	registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
	f.nativeID = otherConversation
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
	if exit != 0 || strings.Contains(out, "not connected") {
		t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
	}
	if described.Load() == followDescribed {
		t.Fatalf("the start did not ask the new backend's adapter to describe: the test holds nothing")
	}
	want := []string{followedLine(tf, &before, true, "brigade"), moveUnreachedLine, startLine("payments-api", "brigade-sess-1", teamName, "accept")}
	if got := lines(out); !slices.Equal(got, want) {
		t.Fatalf("the start's lines:\n%q\nwant\n%q", got, want)
	}
	if n := len(seam.callsFor("session register")); n != registers || f.spawner.count() != spawns {
		t.Fatalf("%d registrations and %d watchers started, want none", n-registers, f.spawner.count()-spawns)
	}
	f.keptWhereItWas("after the start")
	if !exists(retryStampPath(f.stateDir, f.pid)) {
		t.Fatalf("the attempt that failed left no stamp")
	}
}

// TestTheRetryOfASessionAttachedToNothingSaysThatBrigadeIsAsking: a
// session that started before the move was recorded is attached to
// nothing, and its registration retry starts the follow as a child, which
// may join the new backend before the session's next prompt. The retry
// that starts it says so, once in a conversation and echoing nothing from
// the file, before the drift line it has always said. A retry that starts
// no child, because the member's option keeps the session from following,
// says the drift line alone. When the child has followed, the next prompt
// registers the session in the checkout the retry resolved.
func TestTheRetryOfASessionAttachedToNothingSaysThatBrigadeIsAsking(t *testing.T) {
	t.Parallel()
	notMoved := okResp(mustJSON(map[string]any{"team_ref": "team-1", "moved": false}))
	unattached := func(t *testing.T, env ...string) (*fixture, *adapterSeam) {
		t.Helper()
		f := newMemberFixture(t)
		f.labelOldBinding()
		f.moveTeamFile("team-1")
		seam := f.useMoveSeam(nil, map[string][]fakeadapter.Response{
			"team moved":       {notMoved},
			"team join":        {joinOK()},
			"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		})
		if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"), env...); exit != 0 || !slices.Equal(lines(out), []string{driftLine}) || f.mapExists() {
			t.Fatalf("the start: exit %d, lines %q", exit, lines(out))
		}
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		return f, seam
	}

	t.Run("a retry that starts the follow", func(t *testing.T) {
		t.Parallel()
		f, seam := unattached(t)
		before, tf := f.checkoutPin(), f.movedFile()
		got := f.prompted()
		if !slices.Equal(got, []string{movedRetryLine, driftLine}) || len(f.spawner.followed()) != 1 {
			t.Fatalf("the retry: lines %q, %d follows", got, len(f.spawner.followed()))
		}
		if strings.Contains(got[0], "brigade.example") || strings.Contains(got[0], "sha256:") || strings.Contains(got[0], teamName) {
			t.Fatalf("the line echoed the file: %q", got[0])
		}
		// The retry is not due: nothing.
		if got := f.prompted(); len(got) != 0 {
			t.Fatalf("a prompt within the minute: %q", got)
		}
		// A minute later it is, and starts a child again: the conversation
		// has heard that Brigade is asking, and hears the drift line alone.
		f.now = f.now.Add(registerRetryInterval)
		if got := f.prompted(); !slices.Equal(got, []string{driftLine}) || len(f.spawner.followed()) != 2 {
			t.Fatalf("the retry a minute later: lines %q, %d follows", got, len(f.spawner.followed()))
		}
		// Another conversation of the process has heard nothing.
		first := f.nativeID
		f.nativeID = otherConversation
		f.now = f.now.Add(registerRetryInterval)
		if got := f.prompted(); !slices.Equal(got, []string{movedRetryLine, driftLine}) {
			t.Fatalf("another conversation's retry: %q", got)
		}
		f.nativeID = first
		// The administrator records the move: the next child follows, after
		// the retry that started it has returned, and the prompt after it
		// registers the session.
		f.useMoveSeam(movedDoc(movedURL, movedPin), seam.responses)
		f.spawner.onFollow = nil
		f.now = f.now.Add(registerRetryInterval)
		f.prompted()
		follows := f.spawner.followed()
		f.runFollow(follows[len(follows)-1])
		if pin := f.checkoutPin(); !pin.Matches(tf) {
			t.Fatalf("the child did not follow: %+v", pin)
		}
		got = f.prompted()
		if len(got) < 2 || got[0] != followedLine(tf, &before, true, "brigade") || !strings.Contains(got[1], "(brigade-sess-1)") {
			t.Fatalf("the prompt after the follow: %q", got)
		}
		if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.Checkout != f.canon() {
			t.Fatalf("the map the retry wrote: key %q, checkout %q", m.TeamKey, m.Checkout)
		}
		if exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
			t.Fatalf("the session attached and the stamp of what it was told is still there")
		}
	})

	// The member does not wait for the record and joins the server by hand:
	// the retry after the join attaches the session, and what its
	// conversation was told while it was attached to nothing goes with it.
	t.Run("the member joins before the record", func(t *testing.T) {
		t.Parallel()
		f, _ := unattached(t)
		if got := f.prompted(); !slices.Equal(got, []string{movedRetryLine, driftLine}) {
			t.Fatalf("the retry: %q", got)
		}
		if !exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
			t.Fatalf("the line was said and not recorded")
		}
		f.memberOf("team-1")
		pins := map[string]any{"version": 1, "projects": map[string]any{f.canon(): serverPin("team-1", "")}}
		if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), mustJSON(pins)); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(registerRetryInterval)
		got := f.prompted()
		if len(got) == 0 || !strings.Contains(got[0], "(brigade-sess-1)") || strings.Contains(strings.Join(got, "\n"), "followed") {
			t.Fatalf("the retry after the member's own join: %q", got)
		}
		if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.Checkout != f.canon() {
			t.Fatalf("the map: key %q, checkout %q", m.TeamKey, m.Checkout)
		}
		if exists(config.TeamMovedStamp(f.stateDir, f.pid)) {
			t.Fatalf("the session attached and the stamp of what it was told is still there")
		}
	})

	t.Run("a retry that starts none", func(t *testing.T) {
		t.Parallel()
		off := config.OptionFollowTeamMove + "=off"
		f, seam := unattached(t, off)
		asks := len(seam.callsFor("team moved"))
		for range 2 {
			if got := f.prompted(off); slices.Contains(got, movedRetryLine) || len(f.spawner.followed()) != 0 {
				t.Fatalf("a retry with the follow off: lines %q, %d follows", got, len(f.spawner.followed()))
			}
			f.now = f.now.Add(registerRetryInterval)
		}
		if len(seam.callsFor("team moved")) != asks {
			t.Fatalf("a retry with the follow off asked the old backend")
		}
	})
}

// TestAStartThatResolvesNoTeamKeepsARunningSession: `/clear` fires a
// session's start in the same process, from wherever the model is. A start
// that resolves no team there (the file of the session's own checkout names
// a backend with no record behind it; another checkout whose file does; a
// checkout that is not joined; a directory with no team file) used to
// return before it read the map, with the line of a session that is not
// connected or not joined, or none. The session was attached all the same,
// and its map went on naming the conversation that ended. Now the start
// keeps it: no registration, the watcher it had, the map and the resume
// hint for the conversation that began, one heartbeat to the backend it is
// on, and the context line of the session where it is as its only line. On
// Muse Code the shell of the conversation that began finds the session, and
// the arming line follows.
func TestAStartThatResolvesNoTeamKeepsARunningSession(t *testing.T) {
	t.Parallel()
	notMoved := okResp(mustJSON(map[string]any{"team_ref": "team-1", "moved": false}))
	for _, host := range []config.Host{config.HostClaude, config.HostMuse} {
		for _, where := range []string{
			"its own checkout, with no record behind the file",
			"another checkout, with no record behind the file",
			"a checkout that is not joined",
			"a directory with no team file",
		} {
			t.Run(string(host)+"/"+where, func(t *testing.T) {
				t.Parallel()
				f := movingFixture(t, host)
				f.noAdapterOption = true
				seam := attachedMember(t, f, nil, map[string][]fakeadapter.Response{"team moved": {notMoved}})
				first, own, oldPin := f.nativeID, f.canon(), f.checkoutPin()
				from := f.cwd
				switch where {
				case "its own checkout, with no record behind the file":
					f.moveTeamFile("team-1")
				case "another checkout, with no record behind the file":
					from, _ = f.addCheckout("changed", serverFile("team-1"), pinDoc(oldPin))
				case "a checkout that is not joined":
					from, _ = f.addCheckout("unjoined", fileOf(oldPin), nil)
				default:
					from = filepath.Join(t.TempDir(), "no-checkout")
					if err := os.MkdirAll(from, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				registers, spawns, beats := len(seam.callsFor("session register")), f.spawner.count(), len(seam.callsFor("session heartbeat"))
				f.nativeID, f.cwd = otherConversation, from
				exit, out, errOut := f.run(SubSessionStart, f.sessionDoc("clear"))
				if exit != 0 || strings.Contains(out, "not connected") || strings.Contains(out, "not joined") {
					t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
				}
				m := f.mustMap()
				if m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || m.Checkout != own || m.ClaudeSessionID != otherConversation {
					t.Fatalf("the map after the start names %q, %q, the checkout %q and the conversation %q", m.TeamKey, m.BrigadeSessionID, m.Checkout, m.ClaudeSessionID)
				}
				want := []string{startLineOn(f, m.SessionName, "brigade-sess-1")}
				if host == config.HostMuse {
					want = append(want, armLine(f.pluginBin))
				}
				// A team file that does not match what the member joined is theirs
				// to hear of, in the words of a session that is connected.
				if strings.Contains(where, "no record") {
					// With the command named as this host's model can run it
					// (work item 359).
					line := driftLine
					if host == config.HostMuse {
						line = namedIn(f, driftLine)
					}
					want = append(want, keptFileLine(line))
				}
				if got := lines(out); !slices.Equal(got, want) || m.SessionName == "" {
					t.Fatalf("the start's lines:\n%q\nwant\n%q", got, want)
				}
				if n := len(seam.callsFor("session register")); n != registers || f.spawner.count() != spawns {
					t.Fatalf("the start registered %d time(s) and started %d watcher(s)", n-registers, f.spawner.count()-spawns)
				}
				if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry.BrigadeSessionID != "brigade-sess-1" || !alive(f.spawner.watcherPID) {
					t.Fatalf("the session's watcher: %+v, %v", entry, err)
				}
				// The backend the session is on is told of the conversation that
				// began, once.
				if sent := seam.callsFor("session heartbeat")[beats:]; len(sent) != 1 || profileOf(sent[0]) != f.teamKey || !slices.Contains(sent[0].Argv, "brigade-sess-1") {
					t.Fatalf("the start sent %d heartbeat(s): want one, to the backend the session is on, for its id", len(sent))
				}
				if bn, err := f.store().ReadByNative(otherConversation); err != nil || bn.BrigadeSessionID != "brigade-sess-1" || bn.TeamKey != f.teamKey {
					t.Fatalf("the resume hint of the conversation that began: %+v, %v", bn, err)
				}
				if host != config.HostMuse {
					return
				}
				// The shell of the conversation that began finds the session, and
				// the road of the one that ended is gone.
				if p, err := f.store().ReadByHost(string(config.HostMuse), otherConversation); err != nil || p.BrigadeSessionID != "brigade-sess-1" || p.HostPID != f.key() {
					t.Fatalf("the pointer of the conversation that began: %+v, %v", p, err)
				}
				if _, err := f.store().ReadByHost(string(config.HostMuse), first); err == nil {
					t.Fatalf("the pointer of the conversation that ended is still there")
				}
			})
		}
	}

	// What the resolution said of a session that is not connected is not
	// said of one that is; that the team file there was refused is, whatever
	// the refusal: a file that holds what looks like a join secret is the
	// member's to hear of from any session.
	t.Run("a team file that holds what looks like a secret", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		attachedMember(t, f, nil, nil)
		leaked := fileOf(f.checkoutPin())
		leaked["note"] = moveTicket
		dir, _ := f.addCheckout("leaked", leaked, nil)
		f.nativeID, f.cwd = otherConversation, dir
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		got := lines(out)
		if exit != 0 || len(got) != 2 || got[0] != startLine("payments-api", "brigade-sess-1", teamName, "accept") ||
			!strings.Contains(got[1], "looks like a join secret") || strings.Contains(out, "not connected") || strings.Contains(out, moveTicket) {
			t.Fatalf("exit %d, lines %q: %s", exit, got, errOut)
		}
	})
	for _, refusal := range []string{
		driftLine,
		"Brigade: not connected: .brigade.json contains what looks like a join secret — remove it and rotate now (brigade team rotate-secret).",
		"Brigade: not connected (config: team_file_unreadable): fix .brigade.json.",
	} {
		if line := keptFileLine(refusal); strings.Contains(line, "not connected") || !strings.HasPrefix(line, "Brigade: this session stays where it is.") || !strings.Contains(line, ".brigade.json") {
			t.Errorf("the kept session's line of %q: %q", refusal, line)
		}
	}

	// The controls. A map with no live watcher may be one a dead process
	// left: the start is of a session that is attached to nothing, and says
	// so, the map as it was. So is a map beside the pidfile of a watcher
	// that died: the file shows no running session, its process does. A
	// watcher that is alive and serves another session than the map names
	// shows no running session either. And a member who left the team has a
	// binding that names none: a session of theirs is not kept by a start
	// that resolves nothing.
	for _, tc := range []struct {
		name string
		gone func(t *testing.T, f *fixture)
	}{
		{"no watcher is alive", func(t *testing.T, f *fixture) {
			t.Helper()
			if err := os.Remove(f.pidfilePath()); err != nil {
				t.Fatal(err)
			}
		}},
		{"the watcher died and left its pidfile", func(t *testing.T, f *fixture) {
			t.Helper()
			watcher := f.spawner.watcherPID
			if err := syscall.Kill(watcher, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(watcher) })
			if !exists(f.pidfilePath()) {
				t.Fatal("the watcher that died left no pidfile")
			}
		}},
		{"the watcher that is alive serves another session", func(t *testing.T, f *fixture) {
			t.Helper()
			m := f.mustMap()
			m.BrigadeSessionID = "a-session-the-watcher-does-not-serve"
			if err := f.store().WriteByPID(m); err != nil {
				t.Fatal(err)
			}
		}},
		{"the member left the team", func(t *testing.T, f *fixture) {
			t.Helper()
			binding, err := teamstore.LoadBinding(f.configDir, f.teamKey)
			if err != nil {
				t.Fatal(err)
			}
			binding.TeamRef, binding.TeamName, binding.PrincipalRef = "", "", "p-left"
			if err := adapterkit.SaveProfile(f.configDir, f.teamKey, binding); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			seam := attachedMember(t, f, nil, nil)
			first := f.nativeID
			unjoined, _ := f.addCheckout("unjoined", fileOf(f.checkoutPin()), nil)
			tc.gone(t, f)
			beats := len(seam.callsFor("session heartbeat"))
			f.nativeID, f.cwd = otherConversation, unjoined
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
			if got := lines(out); exit != 0 || len(got) != 1 || !strings.HasPrefix(got[0], "Brigade: not joined:") {
				t.Fatalf("exit %d, lines %q: %s", exit, got, errOut)
			}
			if m := f.mustMap(); m.ClaudeSessionID != first || len(seam.callsFor("session heartbeat")) != beats {
				t.Fatalf("the start rewrote the map for the conversation %q, or sent a heartbeat", m.ClaudeSessionID)
			}
		})
	}
}

// TestAStartWhoseSettingsAreRefusedKeepsTheSessionAndSaysWhatHolds: a
// `/clear` of a running session reads the member's settings again, and they
// can be ones the start refuses: a frame level that is no level, a
// frame_file that is not there, an adapter_command that is no command. A
// session that starts anew with such a setting connects to nothing and
// says why. The running one is kept, as where no team resolves, and nothing
// of what was refused reaches it: the map holds the frame and the adapter
// it held, no watcher is started or stopped, and the watcher that runs
// reads that map. So the start says one line more, in the words of a
// session that is connected: what could not be used and why, what holds
// instead, and what to do, which is never `brigade team join`. While an
// option cannot be parsed a prompt reads none of them, as the line says:
// it polls for nothing, where a prompt beside a frame_file that failed its
// check, whose options were read, polls as before.
func TestAStartWhoseSettingsAreRefusedKeepsTheSessionAndSaysWhatHolds(t *testing.T) {
	t.Parallel()
	const guarded = config.OptionFrame + "=guarded"
	missing := config.OptionFrameFile + "=" + filepath.Join(t.TempDir(), "no-such-clause.txt")
	for _, host := range []config.Host{config.HostClaude, config.HostMuse} {
		for _, tc := range []struct {
			name    string
			setting string
			want    string
			// unread: the options could not be parsed, so a prompt reads none.
			unread bool
		}{
			{"a frame level that is no level", config.OptionFrame + "=strickt",
				"Brigade: this session stays connected as it was. The `frame` option cannot be used (invalid_frame_level), so no change to your options is in force here: the session's watcher goes on with the frame and the options the session had, and its prompts read no option until this one is fixed. Fix the option, then `/clear` or start a new session.",
				true},
			{"a frame_file that is not there", missing,
				"Brigade: this session stays connected as it was. The `frame_file` option cannot be used (frame_file_unreadable), so the frame your options name now is not in force here: team messages still reach this session under the frame it had. Fix the `frame` or `frame_file` option, then `/clear` or start a new session.",
				false},
			{"an adapter_command that is no command", config.OptionAdapterCommand + "=relative/adapter",
				"Brigade: this session stays connected as it was. The `adapter_command` option cannot be used (adapter_relative), so the adapter it names is not in force here: the session goes on through the adapter it had. Fix the option, then `/clear` or start a new session.",
				false},
		} {
			t.Run(string(host)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				f := movingFixture(t, host)
				f.noAdapterOption = true
				seam := attachedMember(t, f, nil, map[string][]fakeadapter.Response{"message receive": {okResp(receiveDoc())}}, guarded)
				before := f.mustMap()
				watcher, err := pidfile.Read(f.pidfilePath())
				if err != nil || before.FrameLevel != "guarded" {
					t.Fatalf("the session before the start: frame %q, watcher %v", before.FrameLevel, err)
				}
				registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
				f.nativeID = otherConversation
				exit, out, errOut := f.run(SubSessionStart, f.sessionDoc("clear"), tc.setting)
				want := []string{startLineOn(f, before.SessionName, "brigade-sess-1")}
				if host == config.HostMuse {
					want = append(want, armLine(f.pluginBin))
				}
				want = append(want, tc.want)
				if exit != 0 || !slices.Equal(lines(out), want) {
					t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
				}
				if strings.Contains(out, "not connected") || strings.Contains(out, "team join") {
					t.Fatalf("the start said of a session it kept: %q", out)
				}
				// What holds: the frame, the adapter and the watcher the session had.
				m := f.mustMap()
				if m.FrameLevel != "guarded" || m.FrameText != "" || !slices.Equal(m.AdapterCommand, before.AdapterCommand) || m.Inbound != before.Inbound ||
					m.TeamKey != before.TeamKey || m.BrigadeSessionID != "brigade-sess-1" || m.ClaudeSessionID != otherConversation {
					t.Fatalf("the map after the start: frame %q %q, adapter %q, inbound %q, key %q, session %q, conversation %q",
						m.FrameLevel, m.FrameText, m.AdapterCommand, m.Inbound, m.TeamKey, m.BrigadeSessionID, m.ClaudeSessionID)
				}
				if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry != watcher || !alive(watcher.PID) ||
					f.spawner.count() != spawns || len(seam.callsFor("session register")) != registers {
					t.Fatalf("the start touched the watcher or registered: %+v, %v; %d spawns, %d registrations", entry, err,
						f.spawner.count()-spawns, len(seam.callsFor("session register"))-registers)
				}
				// A prompt beside the same setting.
				polls := len(seam.callsFor("message receive"))
				if exit, _, errOut := f.run(SubPrompt, f.promptDoc("default"), tc.setting, config.OptionPollOnPrompt+"=true"); exit != 0 {
					t.Fatalf("prompt: exit %d: %s", exit, errOut)
				}
				if got := len(seam.callsFor("message receive")) - polls; (got == 0) != tc.unread {
					t.Fatalf("a prompt beside that setting polled %d time(s); the options were unread: %v", got, tc.unread)
				}
				if m := f.mustMap(); m.FrameLevel != "guarded" || m.BrigadeSessionID != "brigade-sess-1" {
					t.Fatalf("the map after that prompt: frame %q, session %q", m.FrameLevel, m.BrigadeSessionID)
				}

				// The control: a session that starts anew beside the same setting
				// connects to nothing and says the refusal as it always has.
				fresh := movingFixture(t, host)
				fresh.noAdapterOption = true
				memberSeam(t, fresh, nil, nil)
				exit, out, errOut = fresh.run(SubSessionStart, fresh.sessionDoc("startup"), tc.setting)
				if exit != 0 || !strings.HasPrefix(out, "Brigade: not connected (config") || strings.Contains(out, "stays connected") || fresh.mapExists() || fresh.spawner.count() != 0 {
					t.Fatalf("a start with no running session: exit %d, %q, a map: %v\n%s", exit, out, fresh.mapExists(), errOut)
				}
			})
		}
	}

	// The adapter can also be one the team file of the start's directory
	// names and this machine has not registered: the line names it as that
	// file's, and the terminal command that shows why.
	t.Run("an adapter the team file there names and adapters.json does not hold", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		seam := attachedMember(t, f, nil, nil)
		before := f.mustMap()
		const url, ref = "https://elsewhere.example.test", "team-9"
		if err := adapterkit.SaveProfile(f.configDir, teamstore.Key("carrier", url, ref), &adapterkit.Profile{
			Version: adapterkit.ProfileVersion, Adapter: "carrier", URL: url, PublishableKey: "pk_carrier", TeamRef: ref, TeamName: "others", PrincipalRef: "p-9",
		}); err != nil {
			t.Fatal(err)
		}
		dir, _ := f.addCheckout("other-adapter",
			map[string]any{"version": 1, "adapter": "carrier", "url": url, "publishable_key": "pk_carrier", "team_ref": ref, "team_name": "others"},
			map[string]any{"adapter": "carrier", "url": url, "publishable_key": "pk_carrier", "team_ref": ref, "consented_at": "2026-11-14T12:00:00Z"})
		registers := len(seam.callsFor("session register"))
		f.nativeID, f.cwd = otherConversation, dir
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		want := []string{
			startLine("payments-api", "brigade-sess-1", teamName, "accept"),
			"Brigade: this session stays connected as it was. The adapter \"carrier\" that the team file in the directory this conversation began in names cannot be used (adapter_unregistered), so this start changed nothing: the session goes on through the adapter it had. Run `brigade team status` in a terminal to see why and fix it, then `/clear` or start a new session.",
		}
		if exit != 0 || !slices.Equal(lines(out), want) {
			t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
		}
		if m := f.mustMap(); m.TeamKey != before.TeamKey || !slices.Equal(m.AdapterCommand, before.AdapterCommand) || len(seam.callsFor("session register")) != registers {
			t.Fatalf("the map after the start names %q and the adapter %q", m.TeamKey, m.AdapterCommand)
		}
	})
}

// TestACodexStartThatResolvesNoTeamKeepsARunningSession: a Codex CLI
// session is keyed by its thread, so the start that finds its map is one
// of the same thread: a thread that is resumed while its watcher is still
// alive. From a directory with no team file it is kept as a Claude Code or
// Muse Code session is, and says its context line with Codex's own line
// after it.
func TestACodexStartThatResolvesNoTeamKeepsARunningSession(t *testing.T) {
	t.Parallel()
	f := movingFixture(t, config.HostCodex)
	f.noAdapterOption = true
	seam := attachedMember(t, f, nil, nil)
	own := f.canon()
	bare := filepath.Join(t.TempDir(), "no-checkout")
	if err := os.MkdirAll(bare, 0o700); err != nil {
		t.Fatal(err)
	}
	registers := len(seam.callsFor("session register"))
	f.cwd = bare
	exit, out, errOut := f.run(SubSessionStart, f.sessionDoc("resume"))
	want := []string{startLineOn(f, f.mustMap().SessionName, "brigade-sess-1"), codexLine(f.pluginBin)}
	if exit != 0 || !slices.Equal(lines(out), want) {
		t.Fatalf("exit %d, lines\n%q\nwant\n%q\n%s", exit, lines(out), want, errOut)
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || m.Checkout != own || len(seam.callsFor("session register")) != registers {
		t.Fatalf("the map after the start names %q, %q and the checkout %q", m.TeamKey, m.BrigadeSessionID, m.Checkout)
	}
	if p, err := f.store().ReadByHost(string(config.HostCodex), f.nativeID); err != nil || p.BrigadeSessionID != "brigade-sess-1" || p.HostPID != f.key() {
		t.Fatalf("the thread's pointer after the start: %+v, %v", p, err)
	}
}

// TestAStartThatResolvesNoTeamRefreshesTheEntryOfTheSessionItKeeps: in a
// project that shares a state folder a session's start writes the session's
// entry, on the register and the continue path alike. A start that keeps a
// session where no team was resolved does the same for the session where it
// is, from the map it kept: the entry is the session's, and stamped by this
// start.
func TestAStartThatResolvesNoTeamRefreshesTheEntryOfTheSessionItKeeps(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	doc := fileOf(f.checkoutPin())
	doc["sync"] = map[string]any{"folders": []string{".brigade"}, "state": ".brigade/state"}
	if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), mustJSON(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	attachedMember(t, f, nil, nil)
	folder := f.mustMap().StateFolder
	if folder == "" {
		t.Fatal("the fixture's session shares no state folder")
	}
	store := state.Store{StateDir: f.stateDir, Folder: folder}
	started := f.now
	if e, err := store.Load("brigade-sess-1"); err != nil || !e.UpdatedAt.Equal(started) {
		t.Fatalf("the entry after the session's start: %+v, %v", e, err)
	}
	bare := filepath.Join(t.TempDir(), "no-checkout")
	if err := os.MkdirAll(bare, 0o700); err != nil {
		t.Fatal(err)
	}
	f.now = started.Add(time.Hour)
	f.nativeID, f.cwd = otherConversation, bare
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
	if exit != 0 || !slices.Equal(lines(out), []string{startLine("payments-api", "brigade-sess-1", teamName, "accept")}) {
		t.Fatalf("exit %d, lines %q: %s", exit, lines(out), errOut)
	}
	if e, err := store.Load("brigade-sess-1"); err != nil || !e.UpdatedAt.Equal(f.now) || !e.EndedAt.IsZero() {
		t.Fatalf("the entry after the start that kept the session: %+v, %v", e, err)
	}
}

// TestAStartThatCannotRefreshAKeptMapLeavesTheShellsRoad: on Muse Code a
// start that keeps a session files the pointer of the conversation that
// begins and retires the one before, in that order and only once the map
// is written. Where the map cannot be written the session is where it was,
// under the map it had, which names the conversation before: the pointer
// of that conversation is the one road the shell still has to the session,
// and it stays. The start says the session's line as it was.
func TestAStartThatCannotRefreshAKeptMapLeavesTheShellsRoad(t *testing.T) {
	t.Parallel()
	f := movingFixture(t, config.HostMuse)
	f.noAdapterOption = true
	attachedMember(t, f, nil, nil)
	first, name := f.nativeID, f.mustMap().SessionName
	bare := filepath.Join(t.TempDir(), "no-checkout")
	if err := os.MkdirAll(bare, 0o700); err != nil {
		t.Fatal(err)
	}
	mapPath, err := f.store().ByPIDPath(f.key())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(mapPath), 0o500); err != nil { //nolint:gosec // G302: a directory, made unwritable on purpose
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(mapPath), 0o700) }) //nolint:gosec // G302: the directory's own mode again
	f.nativeID, f.cwd = otherConversation, bare
	exit, out, errOut := f.run(SubSessionStart, f.sessionDoc("clear"))
	want := []string{startLineOn(f, name, "brigade-sess-1"), armLine(f.pluginBin)}
	if got := lines(out); exit != 0 || !slices.Equal(got, want) {
		t.Fatalf("exit %d, lines %q\nwant %q\n%s", exit, got, want, errOut)
	}
	if m := f.mustMap(); m.ClaudeSessionID != first {
		t.Fatalf("the map names the conversation %q", m.ClaudeSessionID)
	}
	if p, err := f.store().ReadByHost(string(config.HostMuse), first); err != nil || p.BrigadeSessionID != "brigade-sess-1" {
		t.Fatalf("the pointer of the conversation the map still names: %+v, %v", p, err)
	}
}

// TestAStartKeepsTheShellsRoadToAMuseSessionThatStays: on Muse Code a
// command in the model's shell finds its session through a pointer filed
// under the native session id, and a start whose document names another id
// retires the pointer of the one before. A start that keeps the session
// where it is (messages wait for it) therefore has to file the pointer of
// the conversation that begins, to the same session: without it the shell
// of a session that is attached would find none. The arming line follows
// the context line, as at every start there.
func TestAStartKeepsTheShellsRoadToAMuseSessionThatStays(t *testing.T) {
	t.Parallel()
	f := movingFixture(t, config.HostMuse)
	f.noAdapterOption = true
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	f.moveTeamFile("team-1")
	if got := f.prompted(); !slices.Equal(got, []string{saidIn(f, movedLine)}) {
		t.Fatalf("lines = %q", got)
	}
	entries := heldEntries(1, "payments-api")
	if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: entries, UpdatedAt: fixedTime}); err != nil {
		t.Fatal(err)
	}
	first, registers := f.nativeID, len(seam.callsFor("session register"))
	f.nativeID = otherConversation
	exit, out, errOut := f.run(SubSessionStart, f.sessionDoc("startup"))
	if exit != 0 || strings.Contains(out, "not connected") {
		t.Fatalf("exit %d, stdout %q: %s", exit, out, errOut)
	}
	got := lines(out)
	if len(got) != 4 || !strings.Contains(got[0], "followed team") || got[1] != saidIn(f, moveWaitsLine(1)) || !strings.Contains(got[2], "(brigade-sess-1)") || got[3] != armLine(f.pluginBin) {
		t.Fatalf("the start's lines: %q", got)
	}
	m := f.mustMap()
	if m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || m.ClaudeSessionID != otherConversation || len(seam.callsFor("session register")) != registers {
		t.Fatalf("the map after the start: %+v", m)
	}
	if p, err := f.store().ReadByHost(string(config.HostMuse), otherConversation); err != nil || p.BrigadeSessionID != "brigade-sess-1" || p.HostPID != f.key() {
		t.Fatalf("the pointer of the conversation that began: %+v, %v", p, err)
	}
	if _, err := f.store().ReadByHost(string(config.HostMuse), first); err == nil {
		t.Fatalf("the pointer of the conversation that ended is still there")
	}
}
