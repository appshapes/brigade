//go:build !windows

package hook

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// A session that moves is always told (work item 327, ADR 0008, Decision
// 3). Its id changes when its map is written, and Claude Code discards
// what a prompt hook printed when it stops the hook at its timeout: a hook
// that is stopped between the registration and its end has moved the
// session and said nothing. So the prompt that moves a session leaves a
// notice before it registers it and removes it as its last act, and a
// prompt that finds one over a map that names another session says the
// lines itself. The built binary is stopped for real in
// internal/harness/e2e; here the hook that moves the session is held where
// it would be stopped, and the next prompt runs beside it.

// stoppedAtTheWatcher is a Spawner that holds a watcher's spawn until
// released: the first thing a hook does after it wrote the map of a
// session it moved. reached is closed when a hook stands there.
type stoppedAtTheWatcher struct {
	watchers Spawner
	reached  chan struct{}
	release  chan struct{}
	once     *sync.Once
}

func (s stoppedAtTheWatcher) Spawn(ctx context.Context, spec SpawnSpec) (int, error) {
	if len(spec.Args) > 0 && spec.Args[0] == "watch" {
		s.once.Do(func() { close(s.reached) })
		<-s.release
	}
	return s.watchers.Spawn(ctx, spec)
}

// heldHook runs one prompt of stopped, a copy of the fixture whose seams
// hold the hook somewhere, beside the test, and returns once the hook
// stands at reached. A hook that ends without getting there fails the test
// at once: nothing waits for a place the hook never comes to. The hook is
// let go, and waited for, when the test ends.
func heldHook(t *testing.T, stopped *fixture, reached <-chan struct{}, release chan struct{}, env ...string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopped.run(SubPrompt, stopped.sessionPromptDoc(), env...)
	}()
	t.Cleanup(func() {
		close(release)
		<-done
	})
	select {
	case <-reached:
	case <-done:
		t.Fatalf("the hook ended before it came to where it was to be stopped")
	}
}

// noticeOf reports whether the fixture's session has a move's notice.
func (f *fixture) noticeOf() bool {
	return exists(config.MovedNotice(f.stateDir, f.key()))
}

// TestAMoveWhoseHookWasStoppedIsSaidByTheNextPrompt: the hook that moves
// the session is held right after it wrote the session's new map, where
// Claude Code's timeout would stop it: the session has a new id, the old
// watcher still runs, and nothing was printed. The next prompt finds the
// notice and says the lines of the move: how the checkout arrived, which
// this conversation had not heard, and the context line with the id the
// session has now. The notice goes when that prompt ends, and the prompt
// after it has nothing to say. On Muse Code the arming line follows the
// context line, as after any move there.
func TestAMoveWhoseHookWasStoppedIsSaidByTheNextPrompt(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostClaude, config.HostMuse} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			f := movingFixture(t, host)
			f.noAdapterOption = true
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			if got := f.prompted(); !slices.Equal(got, []string{saidIn(f, movedLine)}) {
				t.Fatalf("lines = %q", got)
			}
			if f.noticeOf() {
				t.Fatalf("a prompt that moved nobody left a notice")
			}
			// The hook that moves the session, held where it would be stopped.
			held := stoppedAtTheWatcher{watchers: f.spawner, reached: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
			stopped := *f
			stopped.deps.Spawner = held
			heldHook(t, &stopped, held.reached, held.release)
			if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || !f.noticeOf() {
				t.Fatalf("the hook stands at the watcher with the map %q, %q and a notice: %v", m.TeamKey, m.BrigadeSessionID, f.noticeOf())
			}
			registers := len(seam.callsFor("session register"))
			// The next prompt says what that hook did not.
			got := f.prompted()
			command := "brigade"
			if host == config.HostMuse {
				command = f.pluginBin
			}
			want := []string{followedLine(tf, &before, true, command), startLineOn(f, f.mustMap().SessionName, "brigade-sess-2")}
			if host == config.HostMuse {
				want = append(want, armLine(f.pluginBin))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("the prompt after the hook that was stopped:\n%q\nwant\n%q", got, want)
			}
			if f.noticeOf() || len(seam.callsFor("session register")) != registers {
				t.Fatalf("after that prompt: a notice is left: %v; %d more registrations", f.noticeOf(), len(seam.callsFor("session register"))-registers)
			}
			if exists(config.TeamMovedStamp(f.stateDir, f.key())) || exists(doingStampPath(f.stateDir, f.key())) {
				t.Fatalf("what the conversation was told of the move, or its doing stamp, outlived the move")
			}
			if got := f.prompted(); slices.Contains(got, want[0]) || slices.Contains(got, want[1]) {
				t.Fatalf("a later prompt said the move again: %q", got)
			}
		})
	}
}

// TestAMoveWhoseHookWasStoppedAfterItsLinesIsSaidByTheNextPrompt: a hook
// can be stopped after it printed its lines as well, and what it printed is
// discarded all the same: the notice has to outlive the lines, and goes
// only as the hook's last act. The hook that moves the session is held in
// the poll that follows its lines; the notice is still there, and the next
// prompt says the move. It says how the checkout arrived too: the hook that
// was stopped had said it, to nobody.
func TestAMoveWhoseHookWasStoppedAfterItsLinesIsSaidByTheNextPrompt(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"message receive": {okResp(receiveDoc())},
	})
	before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	// The hook polls the backend the session moved to, and is held there.
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stopped := *f
	inner := f.deps.Spawn
	stopped.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) == "message receive" && profileOf(adapterCall{Argv: spec.Argv}) == newKey {
			once.Do(func() { close(reached) })
			<-release
		}
		return inner(ctx, spec)
	}
	heldHook(t, &stopped, reached, release, config.OptionPollOnPrompt+"=true")
	if m := f.mustMap(); m.BrigadeSessionID != "brigade-sess-2" || !f.noticeOf() {
		t.Fatalf("the hook stands in its poll with the map %q and a notice: %v", m.BrigadeSessionID, f.noticeOf())
	}
	got := f.prompted()
	want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if !slices.Equal(got, want) || f.noticeOf() || len(seam.callsFor("session register")) != 2 {
		t.Fatalf("the prompt after the hook that was stopped:\n%q\nwant\n%q; a notice is left: %v", got, want, f.noticeOf())
	}
}

// TestAMoveSaidFromItsNoticeOwesNoLineTheConversationHeard: the line of how
// the checkout arrived is said once in a conversation. A session that was
// held back by a message heard it at the prompt that found the message; the
// message is released, the hook that then moves the session is stopped, and
// the next prompt says the context line with the session's new id and not
// the arrival line a second time.
func TestAMoveSaidFromItsNoticeOwesNoLineTheConversationHeard(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	f.prompted()
	entries := heldEntries(1, "payments-api")
	if err := pendingStoreOf(f).Save(inbound.PendingFile{Entries: entries, UpdatedAt: fixedTime}); err != nil {
		t.Fatal(err)
	}
	arrival := followedLine(tf, &before, true, "brigade")
	if got := f.prompted(); !slices.Contains(got, arrival) || !slices.Contains(got, moveWaitsLine(1)) {
		t.Fatalf("the prompt that found the message: %q", got)
	}
	if err := pendingStoreOf(f).Save(inbound.PendingFile{UpdatedAt: fixedTime}); err != nil {
		t.Fatal(err)
	}
	held := stoppedAtTheWatcher{watchers: f.spawner, reached: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	stopped := *f
	stopped.deps.Spawner = held
	heldHook(t, &stopped, held.reached, held.release)
	if m := f.mustMap(); m.TeamKey != newKey || !f.noticeOf() {
		t.Fatalf("the hook stands at the watcher with the map %q and a notice: %v", m.TeamKey, f.noticeOf())
	}
	if got := f.prompted(); !slices.Equal(got, []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) {
		t.Fatalf("the prompt after the hook that was stopped: %q", got)
	}
}

// sessionPromptDoc is a prompt document of the fixture's host.
func (f *fixture) sessionPromptDoc() string {
	if f.codex() {
		return f.codexDoc("UserPromptSubmit", "")
	}
	return f.promptDoc("default")
}

// TestAMovesNoticeGoesWithTheHookThatLeftIt: a prompt that moves a session
// and ends leaves no notice; neither does one whose registration failed,
// the session being where it was. A notice that is there over a map that
// still names the session it was left for is of a registration that was
// not recorded (the hook was stopped before it wrote the map): the next
// prompt removes it, says nothing of it, and makes the move itself. So does
// a notice of a move from the backend the map still names, whatever session
// it was left for: a session that is on the backend a move was from has not
// made that move. And a notice no prompt can read is removed.
func TestAMovesNoticeGoesWithTheHookThatLeftIt(t *testing.T) {
	t.Parallel()

	t.Run("a move that was said", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
		f.moveTeamFile("team-1")
		f.prompted()
		if got := f.prompted(); len(got) != 2 || f.noticeOf() || f.mustMap().BrigadeSessionID != "brigade-sess-2" {
			t.Fatalf("the prompt that moved the session: %q; a notice is left: %v", got, f.noticeOf())
		}
	})

	t.Run("a registration that failed", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
			"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false)), errResp(protocol.CodeUnavailable, "network")},
		})
		f.moveTeamFile("team-1")
		f.prompted()
		if got := f.prompted(); !slices.Contains(got, moveUnreachedLine) || f.noticeOf() || f.mustMap().BrigadeSessionID != "brigade-sess-1" {
			t.Fatalf("the prompt whose registration failed: %q; a notice is left: %v", got, f.noticeOf())
		}
	})

	for _, tc := range []struct {
		name   string
		notice func(f *fixture) string
	}{
		{"a notice of a registration that was not recorded", func(f *fixture) string {
			return `{"version":1,"was":"brigade-sess-1","from":"` + f.teamKey + `","arrival":true}`
		}},
		{"a notice of a move from the backend the session is on, left for another session", func(f *fixture) string {
			return `{"version":1,"was":"a-session-before","from":"` + f.teamKey + `","arrival":true}`
		}},
		{"a notice that cannot be read", func(*fixture) string { return `{"version":7,"was":` }},
		{"a notice that names no backend a store could hold", func(*fixture) string {
			return `{"version":1,"was":"another-session","from":"../../elsewhere"}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			before := f.checkoutPin()
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			f.prompted()
			path := config.MovedNotice(f.stateDir, f.key())
			if err := adapterkit.WriteAtomic(path, []byte(tc.notice(f)+"\n")); err != nil {
				t.Fatal(err)
			}
			// The session is where it was, so the prompt says nothing of the
			// notice and moves the session as it would have.
			got := f.prompted()
			want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
			if !slices.Equal(got, want) || f.noticeOf() || len(seam.callsFor("session register")) != 2 {
				t.Fatalf("the prompt over that notice:\n%q\nwant\n%q; a notice is left: %v", got, want, f.noticeOf())
			}
		})
	}
}

// TestAMoveThatCouldNotBeToldOfIsNotMade: the notice is what tells a
// session of a move whose hook was stopped. Where it cannot be written the
// session is not registered on the new backend at all: it stays where it
// is, with its map and its watcher, and no backend is asked.
func TestAMoveThatCouldNotBeToldOfIsNotMade(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	f.moveTeamFile("team-1")
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	// A directory where the notice goes: no file can be renamed over it.
	if err := os.MkdirAll(filepath.Join(config.MovedNotice(f.stateDir, f.key()), "held"), 0o700); err != nil {
		t.Fatal(err)
	}
	registers, spawns := len(seam.callsFor("session register")), f.spawner.count()
	for range 2 {
		if got := f.prompted(); strings.Contains(strings.Join(got, "\n"), "this session is") {
			t.Fatalf("a prompt whose notice could not be written said %q", got)
		}
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || len(seam.callsFor("session register")) != registers || f.spawner.count() != spawns {
		t.Fatalf("the session was moved with no notice: %+v", m)
	}
}

// TestAStartSettlesTheNoticeOfAMove: a hook that moved the session was
// stopped, and before the next prompt a `/clear` fires the session's start.
// The start says the context line of the session as it is now, which is
// what the notice was for: it removes the notice, and the prompt after it
// says nothing of the move.
func TestAStartSettlesTheNoticeOfAMove(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	oldKey := f.teamKey
	f.moveTeamFile("team-1")
	f.prompted()
	if got := f.prompted(); len(got) != 2 {
		t.Fatalf("the prompt that moved the session: %q", got)
	}
	// What a hook that was stopped after the move leaves: its notice.
	notice := `{"version":1,"was":"brigade-sess-1","from":"` + oldKey + `","arrival":true}`
	if err := adapterkit.WriteAtomic(config.MovedNotice(f.stateDir, f.key()), []byte(notice+"\n")); err != nil {
		t.Fatal(err)
	}
	f.nativeID = otherConversation
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
	if exit != 0 || !slices.Contains(lines(out), startLine("payments-api", "brigade-sess-2", teamName, "accept")) || f.noticeOf() {
		t.Fatalf("exit %d, lines %q; a notice is left: %v\n%s", exit, lines(out), f.noticeOf(), errOut)
	}
	if got := f.prompted(); strings.Contains(strings.Join(got, "\n"), "this session is") || strings.Contains(strings.Join(got, "\n"), "followed team") {
		t.Fatalf("the prompt after the start said the move: %q", got)
	}
}

// TestAStartThatResolvesNoTeamAndASessionsEndSettleTheNoticeToo: the two
// other hooks that come after a hook that was stopped. A `/clear` in a
// directory with no team file keeps the session and says its context line,
// with the id it has now: the notice has nothing left to tell. And a
// session that ends takes its notice with it, as it takes its stamps: the
// next process under the same id finds none.
func TestAStartThatResolvesNoTeamAndASessionsEndSettleTheNoticeToo(t *testing.T) {
	t.Parallel()
	moved := func(t *testing.T) *fixture {
		t.Helper()
		f := newMemberFixture(t)
		f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
		attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
		oldKey := f.teamKey
		f.moveTeamFile("team-1")
		f.prompted()
		if got := f.prompted(); len(got) != 2 {
			t.Fatalf("the prompt that moved the session: %q", got)
		}
		notice := `{"version":1,"was":"brigade-sess-1","from":"` + oldKey + `","arrival":true}`
		if err := adapterkit.WriteAtomic(config.MovedNotice(f.stateDir, f.key()), []byte(notice+"\n")); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("a start in a directory with no team file", func(t *testing.T) {
		t.Parallel()
		f := moved(t)
		bare := filepath.Join(t.TempDir(), "no-checkout")
		if err := os.MkdirAll(bare, 0o700); err != nil {
			t.Fatal(err)
		}
		f.nativeID, f.cwd = otherConversation, bare
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
		if exit != 0 || !slices.Equal(lines(out), []string{startLine("payments-api", "brigade-sess-2", teamName, "accept")}) || f.noticeOf() {
			t.Fatalf("exit %d, lines %q; a notice is left: %v\n%s", exit, lines(out), f.noticeOf(), errOut)
		}
	})

	t.Run("the session's end", func(t *testing.T) {
		t.Parallel()
		f := moved(t)
		if exit, _, errOut := f.run(SubSessionEnd, f.endDoc("exit")); exit != 0 || f.noticeOf() {
			t.Fatalf("exit %d; a notice is left: %v\n%s", exit, f.noticeOf(), errOut)
		}
	})
}
