//go:build !windows

package hook

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// A session's start in a process whose session is running where its team
// left it (work item 327, ADR 0007): `/clear` fires the start again, and
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
		if n := len(seam.callsFor("session register")); n != registers || f.spawner.count() != spawns || len(seam.callsFor("session heartbeat")) != 0 {
			t.Fatalf("the start registered %d time(s), started %d watcher(s) and heartbeated %d time(s)", n-registers, f.spawner.count()-spawns, len(seam.callsFor("session heartbeat")))
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
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
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
	if len(got) != 4 || !strings.Contains(got[0], "followed team") || got[1] != moveWaitsLine(1) || !strings.Contains(got[2], "(brigade-sess-1)") || got[3] != armLine(f.pluginBin) {
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
