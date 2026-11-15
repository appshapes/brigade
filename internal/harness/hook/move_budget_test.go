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
	"github.com/appshapes/brigade/internal/testutil"
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
// are, or were, blocked, and how many of those have ended: its caller
// stopped waiting, or the call was let go. Either way the call fails as a
// backend that timed out.
func hangingBackend(f *fixture, hangs func(spec adapterkit.SpawnSpec) bool) (blocked, ended *atomic.Int32, release func()) {
	f.t.Helper()
	inner, gate, once := f.deps.Spawn, make(chan struct{}), sync.Once{}
	blocked, ended = &atomic.Int32{}, &atomic.Int32{}
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
		ended.Add(1)
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the backend did not answer"}
	}
	return blocked, ended, func() { once.Do(func() { close(gate) }) }
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

// promptLong is the budget of a prompt that is held to a part of it: long
// enough that a third of it is several times what a prompt that waits on
// nothing took on the slowest machine measured.
const promptLong = 5 * time.Minute

// timed runs one prompt and returns its lines and how long the hook took.
func (f *fixture) timed() ([]string, time.Duration) {
	f.t.Helper()
	began := time.Now()
	got := f.prompted()
	return got, time.Since(began)
}

// A hookEnd is how one run of a hook ended.
type hookEnd struct {
	exit        int
	out, errOut string
}

// promptBeside runs one prompt of the fixture's session beside the test, on
// a goroutine of hooks, and returns the channel its end comes on. A prompt
// that may wait is run so: the test can then say at its own hang catcher
// that the prompt is not back, where a prompt run on the test's goroutine
// says it only once the hook has given up. The caller waits for hooks in a
// cleanup, registered before the cleanup that lets go of what the hook may
// wait on (cleanups run last in, first out): the hook is let go, ends, and
// only then do its fixture's directories go, as detachedFollows has it for
// a follow child.
func (f *fixture) promptBeside(hooks *sync.WaitGroup) <-chan hookEnd {
	done := make(chan hookEnd, 1)
	hooks.Go(func() {
		exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"))
		done <- hookEnd{exit, out, errOut}
	})
	return done
}

// TestAPromptDoesNotWaitOnAFollowWhoseBackendHangs: the backend the checkout
// joined accepts the follow's question and never answers it. The prompt
// that starts the follow is back with the child still waiting; so is the
// prompt after it; and neither asked a backend anything in its own run. The
// same holds for a session that is attached to nothing, whose registration
// retry starts the child: the path the review of work item 273 closed. Were
// the follow made in the hook, each of these prompts would last its whole
// budget: the question it had asked would end with that budget, and only
// then would the prompt be back.
//
// Two things are held (work item 336). The order: a prompt is back while
// the backend's question is unanswered, nothing having ended it (no caller
// gave up on it and the test has not let it go), and the one question on
// the record is the child's. And the time, against a budget made long: the
// prompt has five minutes and is back in a third of them. A hook that
// waits, on the backend, on the child or on a file the child writes, waits
// as long as its budget lets it, all five; a prompt that waits on nothing
// took 23 s at the most on one core that three runs of this package's tests
// shared with other suites (2026-11-15), which is why the budget is not the
// minute it first was: a third of that was 20 s. The order alone was not
// enough: a hook that polled for the child's pin until its budget ended
// passed it. Each prompt used to be held to 2 s of a budget of 4 s, which is
// a bound a busy machine passes with a hook that waits on nothing.
func TestAPromptDoesNotWaitOnAFollowWhoseBackendHangs(t *testing.T) {
	t.Parallel()
	const budget, within = promptLong, promptLong / 3
	movedOnly := func(spec adapterkit.SpawnSpec) bool { return verbOf(spec.Argv) == "team moved" }

	t.Run("a session that is attached", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		// The child's own budget is out of the way: its question ends when
		// the test lets it go.
		f.deps.PromptBudget, f.deps.FollowBudget = budget, 10*budget
		seam := attachedMember(t, f, nil, nil)
		detachedFollows(f)
		blocked, ended, release := hangingBackend(f, movedOnly)
		t.Cleanup(release)
		pinBefore := f.checkoutPin()
		f.moveTeamFile("team-1")

		got, took := f.timed()
		if !slices.Equal(got, []string{movedLine}) || ended.Load() != 0 || took >= within {
			t.Fatalf("the prompt that started the follow: lines %q in %v, and %d questions to the backend had ended when it was back; want the one line in under %v of its %v and the question unanswered", got, took, ended.Load(), within, budget)
		}
		if n := len(f.spawner.followed()); n != 1 {
			t.Fatalf("%d follow children started, want 1", n)
		}
		// The child is where a real one would be: waiting on the old backend.
		testutil.Await(t, pollTimeout, pollInterval, "the follow child's question to reach the backend that hangs", func() bool { return blocked.Load() == 1 }, nil)
		// The control: the question is on the record, so a hook that had asked
		// it would have been seen waiting too.
		if asks := seam.callsFor("team moved"); len(asks) != 1 || profileOf(asks[0]) != f.teamKey {
			t.Fatalf("the child's question: %d calls", len(asks))
		}
		// A prompt while the child still waits: nothing said, started or asked,
		// and it is back with the child's question still unanswered.
		got, took = f.timed()
		if len(got) != 0 || len(f.spawner.followed()) != 1 || blocked.Load() != 1 || ended.Load() != 0 || took >= within {
			t.Fatalf("a prompt while the child waits: lines %q in %v (want under %v of its %v), %d follows, %d calls blocked, %d of them ended", got, took, within, budget, len(f.spawner.followed()), blocked.Load(), ended.Load())
		}
		if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || f.checkoutPin() != pinBefore {
			t.Fatalf("the session or its pin moved: %+v", m)
		}
		release()
	})

	t.Run("a session attached to nothing", func(t *testing.T) {
		t.Parallel()
		f := newMemberFixture(t)
		f.deps.PromptBudget, f.deps.FollowBudget = budget, 10*budget
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
		blocked, ended, release := hangingBackend(f, movedOnly)
		t.Cleanup(release)

		got, took := f.timed()
		if !slices.Equal(got, []string{movedRetryLine, driftLine}) || ended.Load() != 0 || took >= within {
			t.Fatalf("the retry that started the follow: lines %q in %v, and %d questions to the backend had ended when it was back; want its two lines in under %v of its %v and the question unanswered", got, took, ended.Load(), within, budget)
		}
		if n := len(f.spawner.followed()); n != 1 {
			t.Fatalf("%d follow children started, want 1", n)
		}
		testutil.Await(t, pollTimeout, pollInterval, "the follow child's question to reach the backend that hangs", func() bool { return blocked.Load() == 1 }, nil)
		if n := len(seam.callsFor("team moved")); n != asksAtStart+1 {
			t.Fatalf("%d questions since the start, want the child's one", n-asksAtStart)
		}
		got, took = f.timed()
		if len(got) != 0 || len(f.spawner.followed()) != 1 || blocked.Load() != 1 || ended.Load() != 0 || took >= within || f.mapExists() {
			t.Fatalf("a prompt while the child waits: lines %q in %v (want under %v of its %v), %d follows, %d calls blocked, %d of them ended", got, took, within, budget, len(f.spawner.followed()), blocked.Load(), ended.Load())
		}
		release()
	})
}

// TestAPromptDoesNotWaitOnTheFollowsLock: a follow holds a lock across its
// question to the backend the checkout joined, for as long as that backend
// takes to answer. Neither step of a running session's follow is the hook's
// to wait for that lock: not the prompt that starts the child, and not,
// with the pin written, the prompt that moves the session and registers it.
//
// There are two cases, and each holds a different thing.
//
// With the lock free, any ask for it is seen, whatever it would have waited:
// whoever asks for the lock opens its file, and makes the file where there
// is none (adapterkit.LockFile), and after each of the two prompts there is
// none. The child's own follow is the control: it takes the lock, and its
// file is there. This is the case that fails a hook that asks for the lock
// at all.
//
// With the lock held, as a follow of this machine that waits on its backend
// holds it, the test takes the lock, each of the two prompts is back while
// the test still has it, and only then does the test let it go (for the
// child's own follow, between the two, which needs it). A lock has no list
// of who waits for it, so with the lock held a wait shows only on a clock,
// and the clock of this case is a hang catcher: each prompt has a budget of
// five minutes and is back in a third of it. That catches a wait with no
// bound of its own, which lasts as long as the hook's budget lets it. It
// does not catch a wait under the bound every wait for this lock has in the
// tree today, ten seconds (teammove.lockTimeout): a hook that asked for the
// held lock and gave up after those ten seconds is back in good time, and
// passes this case while it fails the other. No clock that would tell ten
// seconds of waiting from none can be put here: a prompt that waits on
// nothing took 23 s on a starved core
// (TestAPromptDoesNotWaitOnAFollowWhoseBackendHangs). Each prompt runs
// beside the test, so one that is not back fails the test at that third,
// and not when the hook at last gives up; the lock is let go when the test
// ends, the hook ends with it, and the test waits for the hook.
//
// The test used to hold the lock and time each prompt against 2 s, half its
// budget, which is how long a follow made in the hook would have waited for
// it. A prompt that waited on nothing passed 2 s beside other work on this
// machine: its stamps are files synced to disk, 0.25 s in one run and 2.8 s
// in another (2026-11-14). Work item 336 first took the held lock out with
// the 2 s, and left the file alone to say that nobody asked; a review put
// the lock back, under a budget no busy machine reaches; and a second
// review found what the held case does not see, which is said above.
func TestAPromptDoesNotWaitOnTheFollowsLock(t *testing.T) {
	t.Parallel()
	const budget, within = promptLong, promptLong / 3
	for _, tc := range []struct {
		name string
		held bool
	}{
		{"a follow of this machine holds the lock", true},
		{"nobody holds the lock", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			// The registration of the move has the budget, less a second,
			// to be answered in.
			f.deps.PromptBudget = budget
			attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			f.moveTeamFile("team-1")
			lockPath := filepath.Join(f.configDir, "team-move.lock")
			if exists(lockPath) {
				t.Fatal("the lock's file is there before any follow: the test would hold nothing")
			}
			// The prompts run beside the test and are waited for when it
			// ends. Registered before any hold of the lock, this runs after
			// the lock has been let go.
			var hooks sync.WaitGroup
			t.Cleanup(hooks.Wait)
			// hold takes the follow's lock, where the case has it held, and
			// returns what lets it go: called once the prompt is back, it
			// fails the test unless the lock was the test's until then.
			hold := func() func() {
				if !tc.held {
					return func() {}
				}
				lock, err := adapterkit.LockFile(lockPath, pollTimeout)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = lock.Unlock() })
				return func() {
					t.Helper()
					if other, err := adapterkit.LockFile(lockPath, 0); err == nil {
						_ = other.Unlock()
						t.Fatal("the follow's lock could be had while the test held it: the test holds nothing")
					}
					if err := lock.Unlock(); err != nil {
						t.Fatal(err)
					}
				}
			}
			// unasked fails the test when the lock's file is there and the
			// test did not make it.
			unasked := func(who string) {
				t.Helper()
				if !tc.held && exists(lockPath) {
					t.Fatalf("%s asked for the follow's lock", who)
				}
			}

			// back runs one prompt, which has a third of its budget to be
			// back in, and returns the lines it said.
			back := func(who string) []string {
				t.Helper()
				select {
				case got := <-f.promptBeside(&hooks):
					if got.exit != 0 || strings.Contains(got.out+got.errOut, moveTicket) || strings.Contains(got.out, movedLabel) {
						t.Fatalf("%s: exit %d, or the move's ticket or the member's label on one of its streams\n%s", who, got.exit, got.errOut)
					}
					return lines(got.out)
				case <-time.After(within):
					t.Fatalf("waited %v, a third of its budget of %v, for %s to be back, which it was not (the case: %s)", within, budget, who, tc.name)
					return nil
				}
			}

			letGo := hold()
			if got := back("the prompt that started the follow"); !slices.Equal(got, []string{movedLine}) || len(f.spawner.followed()) != 1 {
				t.Fatalf("the prompt that started the follow: lines %q, %d follows", got, len(f.spawner.followed()))
			}
			letGo()
			unasked("the prompt that started the follow")
			// The child follows, under the lock: the control.
			f.runFollow(f.spawner.followed()[0])
			if !exists(lockPath) {
				t.Fatal("the child's follow left no lock file: the test holds nothing of who asks for the lock")
			}
			if !tc.held {
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
			}
			letGo = hold()
			if got := back("the prompt that moved the session"); len(got) != 2 {
				t.Fatalf("the prompt that moved the session: lines %q, want two", got)
			}
			if m := f.mustMap(); m.TeamKey != teamstore.Key("rest", movedURL, "team-1") || m.BrigadeSessionID != "brigade-sess-2" {
				t.Fatalf("the map: %+v", m)
			}
			letGo()
			unasked("the prompt that moved the session")
		})
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
	blocked, _, release := hangingBackend(f, registers)
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
// Nothing here is held against the clock of the machine (work item 336
// took out what still was: the control's answer came 200 ms into a budget
// of 3 s, which a hook has to reach its registration 1.8 s into). The
// control now has a budget no busy machine reaches and an answer at once.
// The second is read in the other case, where the backends are asked: the
// deadline the registration was made under, and the deadline of the poll
// that follows it in the same hook, which runs under the hook's whole
// budget. Their difference is the registration's second exactly, however
// long the hook took to get to either, and whether or not its budget had
// run out by then: a deadline is read off a call that is made, late or
// not. The budget of that case is 3 s because a longer one hides both: a
// registration has 8 s of its own and a poll 4 s, and the nearer deadline
// is the one a call shows. And the backend that answers late is given up
// on before its answer because its caller's deadline comes first, not
// because a prompt was timed.
func TestAMoveIsMadeOnlyWhenItsRegistrationLeavesTimeToSayIt(t *testing.T) {
	t.Parallel()
	// The second is written out: a test that took it from the constant would
	// pass whatever the constant said.
	const second = time.Second
	for _, tc := range []struct {
		name   string
		budget time.Duration
		// late is a backend that answers half a second after the deadline
		// its call was made under; the other answers at once.
		late bool
	}{
		{"answered in good time", time.Minute, false},
		{"answered after the registration's deadline and inside the budget", 3 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			f.deps.PromptBudget = tc.budget
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
				"message receive": {okResp(receiveDoc()), okResp(receiveDoc())},
			})
			before, newKey := f.checkoutPin(), teamstore.Key("rest", movedURL, "team-1")
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			if got := f.prompted(); !slices.Equal(got, []string{movedLine}) {
				t.Fatalf("lines = %q", got)
			}
			// The backend the team went to notes the deadline its registration
			// was made under, and whether its caller stopped waiting; the poll
			// of the same hook, of whichever backend the session is on by
			// then, notes its own. An answer reaches a caller that is still
			// waiting, and nobody else.
			var registerBy, pollBy atomic.Int64
			var gaveUp atomic.Bool
			inner := f.deps.Spawn
			f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
				if d, ok := ctx.Deadline(); ok && verbOf(spec.Argv) == "message receive" {
					pollBy.Store(d.UnixNano())
				}
				if verbOf(spec.Argv) != "session register" || profileOf(adapterCall{Argv: spec.Argv}) != newKey {
					return inner(ctx, spec)
				}
				due, ok := ctx.Deadline()
				if !ok {
					t.Errorf("the registration was made under no deadline")
					return inner(ctx, spec)
				}
				registerBy.Store(due.UnixNano())
				answer := time.After(0)
				if tc.late {
					answer = time.After(time.Until(due) + second/2)
				}
				select {
				case <-ctx.Done():
				case <-answer:
					if ctx.Err() == nil {
						return inner(ctx, spec)
					}
				}
				gaveUp.Store(true)
				return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the caller stopped waiting"}
			}
			began := time.Now()
			exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true")
			took := time.Since(began)
			got, m := lines(out), f.mustMap()
			if exit != 0 || registerBy.Load() == 0 {
				t.Fatalf("exit %d; the registration was made under a deadline: %v\n%s", exit, registerBy.Load() != 0, errOut)
			}
			if !tc.late {
				if gaveUp.Load() {
					t.Fatalf("a registration answered at once, under a budget of %v, was given up after %v", tc.budget, took)
				}
				if m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" {
					t.Fatalf("a registration answered in good time did not move the session: %+v", m)
				}
				want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
				if !slices.Equal(got, want) {
					t.Fatalf("lines = %q\nwant %q", got, want)
				}
				return
			}
			// The caller stopped waiting before the backend answered, and
			// not before its deadline.
			if !gaveUp.Load() || took < tc.budget-second {
				t.Fatalf("a registration answered after its deadline was waited for: %v; the prompt took %v, the deadline being %v", !gaveUp.Load(), took, tc.budget-second)
			}
			if m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" {
				t.Fatalf("a registration answered after its deadline moved the session: %+v", m)
			}
			if !slices.Equal(got, []string{followedLine(tf, &before, true, "brigade"), moveUnreachedLine}) {
				t.Fatalf("lines = %q", got)
			}
			// The poll that followed ran under the hook's whole budget: the
			// registration's deadline was a second before the hook's.
			if pollBy.Load() == 0 {
				t.Fatalf("the hook did not poll after the registration: the test holds nothing of the second\n%s", errOut)
			}
			if left := time.Duration(pollBy.Load() - registerBy.Load()); left != second {
				t.Fatalf("the registration's deadline was %v before the hook's, want %v", left, second)
			}
		})
	}
}

// TestAMoveAnsweredLateDoesNotWaitForAPidfile: what follows a registration
// is what its deadline's second is for, and the most that could follow is a
// wait. The backend the team went to takes its time over the registration:
// it holds its answer until the test lets it go, which is once the hook is
// known to be waiting on it, the session still where it was. Then it
// answers, inside its deadline. The session's old watcher is dead and its
// pidfile gone, and the watcher the hook starts never writes one: a hook
// that waited for a pidfile here would wait as long as it may. The prompt
// that moves a session does not wait at all: nothing in it reads the
// pidfile, a watcher that did not come up is started by the next prompt,
// and the half second of a prompt's wait was time a move has to be said in.
// The session moves, the watcher is started and both lines are said.
//
// That it did not wait is counted, not timed and not read off the wording
// of a log line: a hook tells Deps.PidfileAwaited each time it begins to
// wait for a pidfile, and the prompt that moved the session told it
// nothing. The controls are the session's own start before it and the
// prompt after it, which finds no watcher, starts one and waits for its
// pidfile: one each. (A start whose watcher never comes is
// TestPidfileWaitTimesOut, counted too.)
//
// The backend used to answer a quarter of a second before the
// registration's deadline, under a budget of 3 s, and the test was named
// for that last moment (work item 336). That held the hook to reaching its
// registration within 1.75 s and what followed the answer to fitting in a
// quarter of a second: two guesses about the machine, which a busy one
// did not keep. What is left out with them is the last moment itself: what
// a hook does with an answer that leaves it little of its budget can only
// be shown by an answer that leaves little, and how much an answer leaves
// is the machine's clock, not the test's. The move looks at no clock after
// its registration (reattach). The one look a prompt takes at what is left
// of its budget, before its doing reminder, has a test with a budget of
// its own (doing_test.go, "under a second of budget"), and the deadline's
// second is held, as a value, by
// TestAMoveIsMadeOnlyWhenItsRegistrationLeavesTimeToSayIt.
func TestAMoveAnsweredLateDoesNotWaitForAPidfile(t *testing.T) {
	t.Parallel()
	// The prompt's budget, the minute every fixture has. The test's two
	// waits on the prompt are as long, each a hang catcher.
	const budget = time.Minute
	f := newMemberFixture(t)
	f.deps.PromptBudget = budget
	// Short, for the control's sake: its watcher's pidfile never comes.
	f.deps.PidfileWait = 50 * time.Millisecond
	var awaited atomic.Int32
	f.deps.PidfileAwaited = func() { awaited.Add(1) }
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
	// The session's start waited for its watcher's pidfile, and no prompt
	// since: what is counted is what comes after this.
	waits := awaited.Load()
	if waits == 0 {
		t.Fatal("the session's start told nobody that it waited for its watcher's pidfile: the count would hold nothing")
	}
	// The new backend holds its answer to the registration until it is let
	// go, and gives up with its caller.
	var asked, gaveUp atomic.Int32
	answer := make(chan struct{})
	inner := f.deps.Spawn
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) != "session register" || profileOf(adapterCall{Argv: spec.Argv}) != newKey {
			return inner(ctx, spec)
		}
		asked.Add(1)
		select {
		case <-answer:
			return inner(ctx, spec)
		case <-ctx.Done():
			gaveUp.Add(1)
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the caller stopped waiting"}
		}
	}
	type ended struct {
		exit        int
		out, errOut string
	}
	done := make(chan ended, 1)
	go func() {
		exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"))
		done <- ended{exit, out, errOut}
	}()
	let := sync.OnceFunc(func() { close(answer) })
	t.Cleanup(let)
	testutil.Await(t, budget, pollInterval, "the hook's registration to reach the backend the team went to", func() bool {
		if asked.Load() == 1 {
			return true
		}
		// A prompt that is back by now asked for no registration, and none
		// will come: said at once, where the wait would say it in a minute.
		select {
		case early := <-done:
			t.Fatalf("the prompt ended, exit %d, with no registration asked of the backend the team went to\n%s", early.exit, early.errOut)
		default:
		}
		return false
	}, nil)
	// The hook waits on the answer: the session is where it was, with no
	// watcher started for it.
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" || f.spawner.count() != spawns {
		t.Fatalf("with its registration unanswered the session is at %+v, %d watchers started: want it where it was", m, f.spawner.count()-spawns)
	}
	let()
	var got ended
	select {
	case got = <-done:
	case <-time.After(budget):
		t.Fatalf("waited %v, the whole of the prompt's budget, for the prompt to end after the backend's answer, which it did not", budget)
	}
	if got.exit != 0 || asked.Load() != 1 || gaveUp.Load() != 0 {
		t.Fatalf("exit %d, %d registrations asked, %d given up: want the one, answered\n%s", got.exit, asked.Load(), gaveUp.Load(), got.errOut)
	}
	if m := f.mustMap(); m.TeamKey != newKey || m.BrigadeSessionID != "brigade-sess-2" || f.spawner.count() != spawns+1 {
		t.Fatalf("a registration answered late, inside its deadline, did not move the session, or started %d watchers: %+v\n%s", f.spawner.count()-spawns, m, got.errOut)
	}
	want := []string{followedLine(tf, &before, true, "brigade"), startLine("payments-api", "brigade-sess-2", teamName, "accept")}
	if said := lines(got.out); !slices.Equal(said, want) {
		t.Fatalf("lines = %q\nwant %q", said, want)
	}
	if n := awaited.Load() - waits; n != 0 {
		t.Fatalf("the prompt that moved the session waited for a watcher's pidfile %d time(s)\n%s", n, got.errOut)
	}
	// The control: the watcher never came up, so the next prompt starts one,
	// and that prompt does wait for its pidfile.
	if exit, _, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || f.spawner.count() != spawns+2 || awaited.Load()-waits != 1 {
		t.Fatalf("the prompt after the move: exit %d, %d watchers started, %d waits for a pidfile; want a watcher started and waited for once: the count holds nothing otherwise\n%s",
			exit, f.spawner.count()-spawns-1, awaited.Load()-waits, errOut)
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
	blocked, _, release := hangingBackend(f, func(spec adapterkit.SpawnSpec) bool { return verbOf(spec.Argv) == "team moved" })
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
