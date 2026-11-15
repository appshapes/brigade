//go:build !windows

package hook

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The prompt hook's budget, with backends that never answer (work item 327,
// ADR 0008; the review of work item 273 took the follow out of the prompt
// hook because it could outlast it). The tests above this file answer every
// adapter call at once, so none of them could tell a hook that asks a
// backend from one that does not wait for it. Here a call that is named
// hangs, as a server that accepts a connection and never answers does: until
// the context it was made under ends, or the test lets it go.

// hangingBackend makes every adapter call that hangs names block until its
// context ends or the returned release is called. It reports how many calls
// are, or were, blocked. A call that was let go, or whose context ended,
// fails as a backend that timed out.
func hangingBackend(f *fixture, hangs func(spec adapterkit.SpawnSpec) bool) (blocked *atomic.Int32, release func()) {
	f.t.Helper()
	inner, gate, once := f.deps.Spawn, make(chan struct{}), sync.Once{}
	blocked = &atomic.Int32{}
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if !hangs(spec) {
			return inner(ctx, spec)
		}
		// The call is on the record before it blocks, as a child that was
		// started is.
		if f.seam != nil {
			f.seam.mu.Lock()
			f.seam.calls = append(f.seam.calls, adapterCall{Argv: slices.Clone(spec.Argv), Env: slices.Clone(spec.Env), Stdin: slices.Clone(spec.Stdin)})
			f.seam.mu.Unlock()
		}
		blocked.Add(1)
		select {
		case <-ctx.Done():
		case <-gate:
		}
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the backend did not answer"}
	}
	return blocked, func() { once.Do(func() { close(gate) }) }
}

// detachedFollows has the fixture's spawner run each follow child beside
// the hook that started it, as the real one does, and not before it
// returns. The children are waited for when the test ends: call it before
// hangingBackend's release is registered, so that the release comes first.
func detachedFollows(f *fixture) {
	f.t.Helper()
	var children sync.WaitGroup
	f.spawner.onFollow = func(spec SpawnSpec) {
		children.Go(func() { f.runFollow(spec) })
	}
	f.t.Cleanup(children.Wait)
}

// timed runs one prompt and returns its lines and how long the hook took.
func (f *fixture) timed() ([]string, time.Duration) {
	f.t.Helper()
	began := time.Now()
	got := f.prompted()
	return got, time.Since(began)
}

// TestAPromptDoesNotWaitOnAFollowWhoseBackendHangs: the backend the checkout
// joined accepts the follow's question and never answers it. The prompt
// that starts the follow returns at once, in a small part of its budget,
// with the child still waiting; so does the prompt after it; and neither
// asked a backend anything in its own run. The same holds for a session
// that is attached to nothing, whose registration retry starts the child:
// the path the review of work item 273 closed. Were the follow made in the
// hook, each of these prompts would last its whole budget.
func TestAPromptDoesNotWaitOnAFollowWhoseBackendHangs(t *testing.T) {
	t.Parallel()
	const budget = 4 * time.Second
	movedOnly := func(spec adapterkit.SpawnSpec) bool { return verbOf(spec.Argv) == "team moved" }
	// within is the part of the budget a prompt that waits on nothing stays
	// far under, on a machine that is busy, and that a prompt which waited
	// for the backend would exceed.
	const within = budget / 2

	t.Run("a session that is attached", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.deps.PromptBudget = budget
		seam := attachedMember(t, f, nil, nil)
		detachedFollows(f)
		blocked, release := hangingBackend(f, movedOnly)
		t.Cleanup(release)
		pinBefore := f.checkoutPin()
		f.moveTeamFile("team-1")

		got, took := f.timed()
		if !slices.Equal(got, []string{movedLine}) || took >= within {
			t.Fatalf("the prompt that started the follow: lines %q in %v, want the one line in under %v", got, took, within)
		}
		if n := len(f.spawner.followed()); n != 1 {
			t.Fatalf("%d follow children started, want 1", n)
		}
		// The child is where a real one would be: waiting on the old backend.
		waitFor(t, func() bool { return blocked.Load() == 1 })
		// The control: the question is on the record, so a hook that had asked
		// it would have been seen waiting too.
		if asks := seam.callsFor("team moved"); len(asks) != 1 || profileOf(asks[0]) != f.teamKey {
			t.Fatalf("the child's question: %d calls", len(asks))
		}
		// A prompt while the child still waits: nothing said, started or asked,
		// and no wait.
		got, took = f.timed()
		if len(got) != 0 || took >= within || len(f.spawner.followed()) != 1 || blocked.Load() != 1 {
			t.Fatalf("a prompt while the child waits: lines %q in %v, %d follows, %d calls blocked", got, took, len(f.spawner.followed()), blocked.Load())
		}
		if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || f.checkoutPin() != pinBefore {
			t.Fatalf("the session or its pin moved: %+v", m)
		}
		release()
	})

	t.Run("a session attached to nothing", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.deps.PromptBudget = budget
		f.labelOldBinding()
		f.moveTeamFile("team-1")
		seam := f.useMoveSeam(nil, map[string][]fakeadapter.Response{
			"team moved": {okResp(mustJSON(map[string]any{"team_ref": "team-1", "moved": false}))},
		})
		// The start, with a backend that answers: no record, so the drift line.
		if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !slices.Equal(lines(out), []string{driftLine}) || f.mapExists() {
			t.Fatalf("the start: exit %d, lines %q", exit, lines(out))
		}
		asksAtStart := len(seam.callsFor("team moved"))
		detachedFollows(f)
		blocked, release := hangingBackend(f, movedOnly)
		t.Cleanup(release)

		got, took := f.timed()
		if !slices.Equal(got, []string{movedRetryLine, driftLine}) || took >= within {
			t.Fatalf("the retry that started the follow: lines %q in %v, want its two lines in under %v", got, took, within)
		}
		if n := len(f.spawner.followed()); n != 1 {
			t.Fatalf("%d follow children started, want 1", n)
		}
		waitFor(t, func() bool { return blocked.Load() == 1 })
		if n := len(seam.callsFor("team moved")); n != asksAtStart+1 {
			t.Fatalf("%d questions since the start, want the child's one", n-asksAtStart)
		}
		got, took = f.timed()
		if len(got) != 0 || took >= within || len(f.spawner.followed()) != 1 || blocked.Load() != 1 || f.mapExists() {
			t.Fatalf("a prompt while the child waits: lines %q in %v, %d follows, %d calls blocked", got, took, len(f.spawner.followed()), blocked.Load())
		}
		release()
	})
}

// TestAPromptDoesNotWaitOnTheFollowsLock: another process of this machine
// holds the lock a follow takes, as a follow that is waiting on its backend
// does. Neither step of a running session's follow is the hook's to wait
// for it: the prompt that starts the child returns at once, and, with the
// pin written, the prompt that moves the session registers it while the
// lock is still held.
func TestAPromptDoesNotWaitOnTheFollowsLock(t *testing.T) {
	t.Parallel()
	const budget = 4 * time.Second
	f := newMemberFixture(t)
	f.deps.PromptBudget = budget
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	f.moveTeamFile("team-1")
	lockPath := filepath.Join(f.configDir, "team-move.lock")
	lock, err := adapterkit.LockFile(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })

	got, took := f.timed()
	if !slices.Equal(got, []string{movedLine}) || took >= budget/2 || len(f.spawner.followed()) != 1 {
		t.Fatalf("the prompt that started the follow under a held lock: lines %q in %v, %d follows", got, took, len(f.spawner.followed()))
	}
	// The child follows once the lock is free; then the lock is held again.
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	f.runFollow(f.spawner.followed()[0])
	if lock, err = adapterkit.LockFile(lockPath, time.Second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	got, took = f.timed()
	if len(got) != 2 || took >= budget/2 {
		t.Fatalf("the prompt that moved the session under a held lock: lines %q in %v", got, took)
	}
	if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" {
		t.Fatalf("the map: %+v", m)
	}
}

// TestAPromptWhoseNewBackendHangsGivesUpAtTheRegistrationsDeadline: the
// checkout followed, and the backend the team went to accepts the session's
// registration and never answers. The registration is the one call of the
// follow the hook makes itself, so this prompt does wait: until the
// registration's deadline, which is a second short of the hook's budget
// (moveRegisterSlack), and no longer. It leaves the map, the watcher and
// the old session as they were, says that the session could not be
// registered and never that it is `not connected`, and stamps the attempt,
// so the prompts of the next minute do not wait again.
//
// How long the prompt took to end after that is not held: it is the hook's
// stamps, each a file synced to disk, and beside other agents' work on
// this machine it was 0.25 s in one run and 2.8 s in another (2026-11-14).
// A hook that is stopped in that time has moved nobody.
func TestAPromptWhoseNewBackendHangsGivesUpAtTheRegistrationsDeadline(t *testing.T) {
	t.Parallel()
	// The registration's deadline is written out, a second short of the
	// budget: a test that took it from the constant would pass whatever the
	// constant said.
	const budget, deadline = 2500 * time.Millisecond, 1500 * time.Millisecond
	f := newMemberFixture(t)
	f.deps.PromptBudget = budget
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	registers := func(spec adapterkit.SpawnSpec) bool {
		return verbOf(spec.Argv) == "session register" && profileOf(adapterCall{Argv: spec.Argv}) == newKey
	}
	blocked, release := hangingBackend(f, registers)
	t.Cleanup(release)
	spawns := f.spawner.count()

	got, took := f.timed()
	if blocked.Load() != 1 {
		t.Fatalf("%d registrations were sent to the new backend, want 1", blocked.Load())
	}
	// It waited for the registration, which never answers, until that one's
	// deadline. That the deadline is a second short of the budget, and no
	// more, is TestAMoveIsMadeOnlyWhenItsRegistrationLeavesTimeToSayIt's.
	if took < deadline {
		t.Fatalf("the prompt took %v, less than the registration's deadline of %v", took, deadline)
	}
	if !slices.Equal(got, []string{followedLine(tf, &before, true, "brigade"), moveUnreachedLine}) {
		t.Fatalf("lines = %q", got)
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || f.spawner.count() != spawns {
		t.Fatalf("the map %+v, %d watchers started: want the session where it was", m, f.spawner.count()-spawns)
	}
	if entry, err := pidfile.Read(f.pidfilePath()); err != nil || entry.BrigadeSessionID != "brigade-sess-1" || !alive(f.spawner.watcherPID) {
		t.Fatalf("the old session's watcher: %+v, %v, alive %v", entry, err, alive(f.spawner.watcherPID))
	}
	if !exists(retryStampPath(f.stateDir, f.pid)) {
		t.Fatalf("the attempt that timed out left no stamp: every prompt would wait")
	}
	// The next prompt, within the minute: no registration, so nothing to
	// wait for.
	if got := f.prompted(); len(got) != 0 || blocked.Load() != 1 {
		t.Fatalf("a prompt within the minute: lines %q, %d registrations", got, blocked.Load())
	}
	if on := newBackendCalls(seam, newKey); slices.Contains(on, "session heartbeat") || slices.Contains(on, "session close") {
		t.Fatalf("the new backend was sent %v", on)
	}
}

// TestAMoveIsMadeOnlyWhenItsRegistrationLeavesTimeToSayIt: Claude Code
// discards everything a prompt hook printed when the hook reaches its
// timeout, and the registration of a move writes the session's new map. A
// registration answered in the last moments of the budget would move the
// session with nothing said: a new id the model was never told. So the
// registration has a deadline a second short of the budget. A backend that
// answers after it, though still inside the budget, is given up on as one
// that did not answer: the map is as it was, the session is told it could
// not be registered. The control is the same backend answering in good
// time, which moves the session and says so.
//
// Nothing here is held against the clock of the machine. The second is
// read where the backends are asked: the deadline the registration was made
// under, and the deadline of the poll that follows it in the same hook,
// which runs under the hook's whole budget. Their difference is the
// registration's second exactly, however long the hook took to get there.
// And the backend that answers late is given up on before its answer
// because its caller's deadline comes first, not because a prompt was
// timed: the hook's other work, its stamps, each a file synced to disk,
// took 0.25 s in one run and 2.8 s in another beside other agents' work on
// this machine (measured 2026-11-14). Where the second is not enough for
// that, the move's notice is what tells the session (move_notice_test.go).
func TestAMoveIsMadeOnlyWhenItsRegistrationLeavesTimeToSayIt(t *testing.T) {
	t.Parallel()
	// The second is written out: a test that took it from the constant would
	// pass whatever the constant said.
	const budget, second = 3 * time.Second, time.Second
	for _, tc := range []struct {
		name string
		// answers is how long the new backend takes to register the session.
		answers time.Duration
		moves   bool
	}{
		{"answered in good time", 200 * time.Millisecond, true},
		{"answered after the registration's deadline and inside the budget", 2500 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.deps.PromptBudget = budget
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
				"message receive": {okResp(receiveDoc())},
			})
			before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
				t.Fatalf("lines = %q", got)
			}
			// A backend that takes its time, as a real one does: its answer
			// reaches a caller that is still waiting, and nobody else. It notes
			// the deadline its call was made under, and whether its caller
			// stopped waiting; the poll of the same hook notes its own.
			var registerBy, pollBy atomic.Int64
			var gaveUp atomic.Bool
			inner := f.deps.Spawn
			f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
				onNew := profileOf(adapterCall{Argv: spec.Argv}) == newKey
				if d, ok := ctx.Deadline(); ok && onNew && verbOf(spec.Argv) == "message receive" {
					pollBy.Store(d.UnixNano())
				}
				if verbOf(spec.Argv) != "session register" || !onNew {
					return inner(ctx, spec)
				}
				if d, ok := ctx.Deadline(); ok {
					registerBy.Store(d.UnixNano())
				}
				select {
				case <-time.After(tc.answers):
					return inner(ctx, spec)
				case <-ctx.Done():
					gaveUp.Store(true)
					return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the caller stopped waiting"}
				}
			}
			began := time.Now()
			exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true")
			took := time.Since(began)
			got, m := lines(out), f.mustMap()
			if exit != 0 || registerBy.Load() == 0 {
				t.Fatalf("exit %d; the registration was made under a deadline: %v\n%s", exit, registerBy.Load() != 0, errOut)
			}
			if !tc.moves {
				// The caller stopped waiting before the backend answered, and
				// not before its deadline.
				if !gaveUp.Load() || took < budget-second {
					t.Fatalf("a registration answered after its deadline was waited for: %v; the prompt took %v, the deadline being %v", !gaveUp.Load(), took, budget-second)
				}
				if m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" {
					t.Fatalf("a registration answered after its deadline moved the session: %+v", m)
				}
				if !slices.Equal(got, []string{followedLine(tf, &before, true, "brigade"), moveUnreachedLine}) {
					t.Fatalf("lines = %q", got)
				}
				return
			}
			if gaveUp.Load() {
				t.Fatalf("a registration answered in good time was given up")
			}
			if m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" {
				t.Fatalf("a registration answered in good time did not move the session: %+v", m)
			}
			want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
			if !slices.Equal(got, want) {
				t.Fatalf("lines = %q\nwant %q", got, want)
			}
			// The poll that followed ran under the hook's whole budget: the
			// registration's deadline was a second before the hook's.
			if pollBy.Load() == 0 {
				t.Fatalf("the hook did not poll the backend the session moved to: the test holds nothing of the second")
			}
			if left := time.Duration(pollBy.Load() - registerBy.Load()); left != second {
				t.Fatalf("the registration's deadline was %v before the hook's, want %v", left, second)
			}
		})
	}
}

// TestAMoveAnsweredAtTheLastMomentDoesNotWaitForAPidfile is the most that
// can follow a registration, which is what its deadline's second is for.
// The backend answers a quarter of a second before the registration's
// deadline. The session's old watcher is dead and its pidfile gone, and the
// watcher the hook starts never writes one: a hook that waited for a
// pidfile here would wait as long as it may, and log that none appeared.
// The prompt that moves a session does not wait at all: nothing in it reads
// the pidfile, a watcher that did not come up is started by the next
// prompt, and the half second was time a move has to be said in. The
// session moves and both lines are said.
//
// How long the prompt then takes to end is logged and not held: it is the
// map, the resume hint, the notice and the stamps, each a file synced to
// disk, and with this package's other tests running it varies by a second.
// A move whose hook is stopped there is said by the next prompt
// (TestAMoveWhoseHookWasStoppedIsSaidByTheNextPrompt).
func TestAMoveAnsweredAtTheLastMomentDoesNotWaitForAPidfile(t *testing.T) {
	t.Parallel()
	const budget, lead, startWait = 3 * time.Second, 250 * time.Millisecond, 6 * time.Second
	f := newMemberFixture(t)
	f.deps.PromptBudget = budget
	// A start's wait, made long: a wait would show.
	f.deps.PidfileWait = startWait
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
		t.Fatalf("lines = %q", got)
	}
	// The old watcher is gone with its pidfile, and the next never comes up.
	if err := os.Remove(f.pidfilePath()); err != nil {
		t.Fatal(err)
	}
	f.spawner.watcherPID = 0
	spawns := f.spawner.count()
	// The backend answers lead before the deadline its call was made under,
	// and notes when that was, as time since the prompt began.
	var began time.Time
	var answered atomic.Int64
	inner := f.deps.Spawn
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) != "session register" || profileOf(adapterCall{Argv: spec.Argv}) != newKey {
			return inner(ctx, spec)
		}
		due, ok := ctx.Deadline()
		if !ok {
			t.Errorf("the registration was made under no deadline")
			return inner(ctx, spec)
		}
		select {
		case <-time.After(time.Until(due) - lead):
			answered.Store(int64(time.Since(began)))
			return inner(ctx, spec)
		case <-ctx.Done():
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the caller stopped waiting"}
		}
	}
	began = time.Now()
	exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"))
	took := time.Since(began)
	at := time.Duration(answered.Load())
	if exit != 0 || at == 0 {
		t.Fatalf("exit %d; the backend's answer was waited for: %v\n%s", exit, at != 0, errOut)
	}
	if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || f.spawner.count() != spawns+1 {
		t.Fatalf("a registration answered before its deadline did not move the session, or started %d watchers: %+v", f.spawner.count()-spawns, m)
	}
	want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if got := lines(out); !slices.Equal(got, want) {
		t.Fatalf("lines = %q\nwant %q", got, want)
	}
	// It did not look for the watcher's pidfile: a hook that waits for one
	// that never appears says so in its log, and takes at least that long.
	if after := took - at; strings.Contains(errOut, "watcher pidfile did not appear") || after >= startWait {
		t.Fatalf("the prompt waited for the watcher's pidfile: it ended %v after the backend's answer\n%s", after, errOut)
	}
	t.Logf("the backend answered %v after the prompt began, and the prompt ended %v after that", at.Round(time.Millisecond), (took - at).Round(time.Millisecond))
}

// waitFor waits for cond, which another goroutine makes true.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(pollInterval)
	}
}

// TestAFollowWhoseBackendHangsEndsWithItsBudget: the child itself, with the
// backend the checkout joined accepting its question and never answering.
// teammove.Follow puts no deadline on its calls, so the child's budget is
// the only thing that ends it: without one it would wait for ever, holding
// the lock every other follow of this machine waits for. It ends when its
// budget does, with exit 0, the pin as it was, no mark, no ticket file and
// the lock free.
func TestAFollowWhoseBackendHangsEndsWithItsBudget(t *testing.T) {
	t.Parallel()
	const budget = 500 * time.Millisecond
	f := newMemberFixture(t)
	f.deps.FollowBudget = budget
	f.labelOldBinding()
	f.moveTeamFile("team-1")
	seam := f.useMoveSeam(nil, nil)
	blocked, release := hangingBackend(f, func(spec adapterkit.SpawnSpec) bool { return verbOf(spec.Argv) == "team moved" })
	t.Cleanup(release)
	pinBefore := f.checkoutPin()
	spec := SpawnSpec{
		Args: []string{"hook", SubFollow, "--checkout", f.canon(), "--config-dir", f.configDir, "--state-dir", f.stateDir, "--log-level", "info"},
		Env:  []string{"PATH=" + f.emptyPath(), "HOME=" + f.dirs.Home},
	}
	began, done := time.Now(), make(chan struct{})
	go func() {
		defer close(done)
		f.runFollow(spec)
	}()
	select {
	case <-done:
	case <-time.After(budget + 10*time.Second):
		release()
		<-done
		t.Fatalf("the child was still waiting on its backend %v after its budget of %v", time.Since(began).Round(time.Millisecond), budget)
	}
	if took := time.Since(began); took < budget || blocked.Load() != 1 {
		t.Fatalf("the child ended after %v with %d questions asked, want its budget of %v and one question", took, blocked.Load(), budget)
	}
	if asks := seam.callsFor("team moved"); len(asks) != 1 || profileOf(asks[0]) != f.teamKey {
		t.Fatalf("the child's question: %d calls", len(asks))
	}
	if pin := f.checkoutPin(); pin != pinBefore || exists(config.MoveFollowedMark(f.stateDir, f.configDir, f.canon())) {
		t.Fatalf("a follow that was not answered left the pin %+v or a mark", pin)
	}
	f.noTicketLeft()
	lock, err := adapterkit.LockFile(filepath.Join(f.configDir, "team-move.lock"), 0)
	if err != nil {
		t.Fatalf("the child that ended still holds the follow's lock: %v", err)
	}
	_ = lock.Unlock()
}
