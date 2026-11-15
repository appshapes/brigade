//go:build !windows

package hook

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/state"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
)

// What a hook that moved a session and was stopped left undone is the next
// prompt's to do (work item 327, ADR 0007, Decision 3): the lines, and the
// roads to the session that the hook had still to file.

// TestAMoveWhoseHookWasStoppedLeavesTheShellsRoadToTheSession: on Muse Code
// and Codex CLI a command in the model's shell finds the session through
// the by-host pointer, which names the Brigade session beside the host pid,
// and a pointer that names another session than the map is refused
// (config.Session). The hook that moves a session writes the map first and
// the pointer near its end. Held where it would be stopped in between, the
// map names the new session and the pointer the old one: every `brigade`
// command of the session is refused. The next prompt, which says the move,
// files the pointer for the session as it is now.
func TestAMoveWhoseHookWasStoppedLeavesTheShellsRoadToTheSession(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostMuse, config.HostCodex} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			f := movingFixture(t, host)
			f.noAdapterOption = true
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
			shell := []string{"HOME=" + f.dirs.Home, host.NativeIDVar() + "=" + f.nativeID}
			if m, err := config.SessionIn(shell, f.store()); err != nil || m.BrigadeSessionID != "brigade-sess-1" {
				t.Fatalf("the shell's road to the session before the move: %v", err)
			}
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			f.prompted()
			held := stoppedAtTheWatcher{watchers: f.spawner, reached: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
			stopped := *f
			stopped.deps.Spawner = held
			heldHook(t, &stopped, held.reached, held.release)
			if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" {
				t.Fatalf("the hook stands at the watcher with the map %q, %q", m.TeamKey, m.BrigadeSessionID)
			}
			// The next prompt says the move, with the host's own line after the
			// context line, and a command of the session finds it.
			own := armLine(f.pluginBin)
			if host == config.HostCodex {
				own = codexLine(f.pluginBin)
			}
			want := []string{followedLine(tf, &before, true, f.pluginBin), startLine(f.mustMap().SessionName, "brigade-sess-2", teamName, "accept"), own}
			if got := f.prompted(); !slices.Equal(got, want) {
				t.Fatalf("the prompt after the hook that was stopped:\n%q\nwant\n%q", got, want)
			}
			m, err := config.SessionIn(shell, f.store())
			if err != nil || m.BrigadeSessionID != "brigade-sess-2" || m.TeamKey != newKey {
				t.Fatalf("a command in the session's shell after the prompt that said the move: %v", err)
			}
			if bn, err := f.store().ReadByNative(f.nativeID); err != nil || bn.BrigadeSessionID != "brigade-sess-2" || bn.TeamKey != newKey {
				t.Fatalf("the resume hint of the conversation after that prompt: %+v, %v", bn, err)
			}
		})
	}
}

// TestAMoveWhoseHookWasStoppedBeforeItsWatcherIsServedByTheNextPrompt: the
// hook that moves a session starts the watcher right after it writes the
// map, and can be stopped in between. The watcher the session had is then
// still alive, serving the session the move left, until it sees the map and
// has closed that session on the backend the team left. It is no watcher of
// the session as it is now: the next prompt starts that one beside it, and
// neither stops the other one itself nor waits for anything. A prompt whose
// live watcher serves the map's session starts none.
func TestAMoveWhoseHookWasStoppedBeforeItsWatcherIsServedByTheNextPrompt(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	f.moveTeamFile("team-1")
	f.prompted()
	held := stoppedAtTheWatcher{watchers: f.spawner, reached: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	stopped := *f
	stopped.deps.Spawner = held
	heldHook(t, &stopped, held.reached, held.release)
	spawns := f.spawner.count()
	if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry.BrigadeSessionID != "brigade-sess-1" || f.mustMap().BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the hook stands at the watcher beside the watcher %+v, %v", entry, err)
	}
	// What that hook removes at its end is still there: the stamp of a
	// registration that failed earlier, the doing reminder's of the session
	// the move left, and the word the follow left for the checkout.
	mark := config.MoveFollowedMark(f.stateDir, f.configDir, f.canon())
	retry := retryStampPath(f.stateDir, f.key())
	if err := os.WriteFile(retry, []byte(f.now.Add(-time.Hour).Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.plantDoingStamp(t, f.now, f.nativeID)
	if !exists(mark) {
		t.Fatalf("the follow left no mark for the checkout")
	}
	f.prompted()
	if exists(retry) || exists(f.doingStamp()) || exists(mark) {
		t.Fatalf("after the prompt that said the move: the retry stamp is there: %v; the doing stamp: %v; the follow's mark: %v", exists(retry), exists(f.doingStamp()), exists(mark))
	}
	if f.spawner.count() != spawns+1 {
		t.Fatalf("the prompt after the hook that was stopped started %d watcher(s), want the session's own", f.spawner.count()-spawns)
	}
	if seen := f.spawner.lastMap(t); seen.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the watcher that prompt started found the map of %q", seen.BrigadeSessionID)
	}
	if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the watcher after that prompt: %+v, %v", entry, err)
	}
	// The hook signalled nobody: retiring the other watcher is the new one's.
	if !alive(f.spawner.watcherPID) {
		t.Fatalf("the prompt stopped the watcher it found itself")
	}
	f.prompted()
	if f.spawner.count() != spawns+1 {
		t.Fatalf("a prompt whose watcher serves the session started another")
	}
}

// TestAMoveWhoseHookWasStoppedEndsTheEntryOfTheSessionItLeft: in a project
// that shares a state folder the hook that moves a session ends the entry
// of the session it left, as moved, after it starts the watcher. Stopped
// before, it leaves an entry that never ends, and the digest reports what a
// session did when it ends. The next prompt ends it, under the id the
// notice kept, and writes the entry of the session as it is now.
func TestAMoveWhoseHookWasStoppedEndsTheEntryOfTheSessionItLeft(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	old := f.checkoutPin()
	write := func(doc map[string]any) {
		t.Helper()
		doc["version"], doc["team_ref"], doc["team_name"] = 1, "team-1", teamName
		doc["sync"] = map[string]any{"folders": []string{".brigade"}, "state": ".brigade/state"}
		if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), mustJSON(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"adapter": "supabase", "url": old.URL, "publishable_key": old.PublishableKey})
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	folder := f.mustMap().StateFolder
	if folder == "" {
		t.Fatal("the fixture's session shares no state folder")
	}
	store := state.Store{StateDir: f.stateDir, Folder: folder}
	write(map[string]any{"adapter": "rest", "url": movedURL, "certificate": movedPin})
	f.prompted()
	held := stoppedAtTheWatcher{watchers: f.spawner, reached: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	stopped := *f
	stopped.deps.Spawner = held
	heldHook(t, &stopped, held.reached, held.release)
	if e, err := store.Load("brigade-sess-1"); err != nil || !e.EndedAt.IsZero() || f.mustMap().BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the hook stands at the watcher with the entry %+v, %v", e, err)
	}
	f.prompted()
	gone, err := store.Load("brigade-sess-1")
	if err != nil || gone.EndedAt.IsZero() || gone.EndReason != "moved" {
		t.Fatalf("the entry of the session the move left, after the prompt that said the move: %+v, %v", gone, err)
	}
	if here, err := store.Load("brigade-sess-2"); err != nil || !here.EndedAt.IsZero() {
		t.Fatalf("the entry of the session as it is now: %+v, %v", here, err)
	}
	// An entry that has its end keeps it: a later notice of the same move
	// (the hook that said it was stopped too) does not end it again.
	f.now = f.now.Add(1)
	notice := `{"version":1,"was":"brigade-sess-1","from":"` + f.teamKey + `"}`
	if err := os.WriteFile(config.MovedNotice(f.stateDir, f.key()), []byte(notice+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.prompted()
	if again, err := store.Load("brigade-sess-1"); err != nil || !again.EndedAt.Equal(gone.EndedAt) {
		t.Fatalf("the entry was ended a second time: %+v, %v", again, err)
	}
}

// TestAMoveSaidFromItsNoticeNamesTheSessionsOwnCheckout: the prompt that
// says a move whose hook was stopped can come from anywhere, as the prompt
// that made the move could. From a repository of another team it says that
// the checkout the session was started in followed, never that "this
// checkout" did.
func TestAMoveSaidFromItsNoticeNamesTheSessionsOwnCheckout(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	before := f.checkoutPin()
	f.memberOf("team-2")
	other, _ := f.addCheckout("other-team", serverFile("team-2"), serverPin("team-2", ""))
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	f.prompted()
	held := stoppedAtTheWatcher{watchers: f.spawner, reached: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	stopped := *f
	stopped.deps.Spawner = held
	heldHook(t, &stopped, held.reached, held.release)
	if f.mustMap().BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the hook stands at the watcher with the map of %q", f.mustMap().BrigadeSessionID)
	}
	home := f.cwd
	f.cwd = other
	got := f.prompted()
	f.cwd = home
	want := []string{followedLineOf(startedCheckout, tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if !slices.Equal(got, want) {
		t.Fatalf("the prompt after the hook that was stopped, from a checkout of another team:\n%q\nwant\n%q", got, want)
	}
}
