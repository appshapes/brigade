//go:build !windows

package hook

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestSessionEndReasons is the 6.3 table: clear and resume leave
// everything running; every other reason (and an unknown one) signals the
// watcher, removes the pidfile and the by-pid map, keeps by-native and
// runs `session close`.
func TestSessionEndReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason   string
		teardown bool
	}{
		{"clear", false},
		{"resume", false},
		{"other", true},
		{"logout", true},
		{"prompt_input_exit", true},
		{"", true},
		{"some_future_reason", true},
	} {
		t.Run("reason="+tc.reason, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			watcher := testutil.NewSleeper(t)
			f.spawner.watcherPID = watcher
			seam := registered(t, f, map[string][]fakeadapter.Response{"session close": {okResp(closeDoc())}})
			f.writeStartFacts(t)
			exit, out, errOut := f.run(SubSessionEnd, f.endDoc(tc.reason))
			if exit != 0 || out != "" {
				t.Fatalf("exit %d out %q err %q", exit, out, errOut)
			}
			closes := seam.callsFor("session close")
			if !tc.teardown {
				if len(closes) != 0 || !alive(watcher) || !f.mapExists() || !f.startFactsExist() {
					t.Fatalf("reason %q tore the session down: closes %d alive %v map %v start facts %v", tc.reason, len(closes), alive(watcher), f.mapExists(), f.startFactsExist())
				}
				if _, err := os.Lstat(f.pidfilePath()); err != nil {
					t.Fatalf("pidfile gone: %v", err)
				}
				return
			}
			testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(watcher) })
			if _, err := os.Lstat(f.pidfilePath()); err == nil {
				t.Fatal("the pidfile survived")
			}
			if f.mapExists() {
				t.Fatal("the by-pid map survived")
			}
			if f.startFactsExist() {
				t.Fatal("the start facts survived")
			}
			if bn, err := f.store().ReadByNative(f.nativeID); err != nil || bn.BrigadeSessionID != "brigade-sess-1" {
				t.Fatalf("by-native must be kept: %+v %v", bn, err)
			}
			if len(closes) != 1 || strings.Join(closes[0].Argv[len(closes[0].Argv)-4:], " ") != "session close --session brigade-sess-1" {
				t.Fatalf("close calls %d %v", len(closes), closes)
			}
		})
	}
}

// TestSessionEndWithoutState: with no pidfile and no map the hook has
// nothing to do and says so on stderr only.
func TestSessionEndWithoutState(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seam := f.useSeam(nil)
	if exit, out, _ := f.run(SubSessionEnd, f.endDoc("other")); exit != 0 || out != "" || len(seam.calls) != 0 {
		t.Fatalf("exit %d out %q calls %d", exit, out, len(seam.calls))
	}
}

// TestSessionEndBadStdinDoesNothing: with no readable reason the hook does
// not tear the session down (a `clear` mistaken for an end would kill a
// live session's watcher); the watcher's own liveness poll is the
// authoritative close.
func TestSessionEndBadStdinDoesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	watcher := testutil.NewSleeper(t)
	f.spawner.watcherPID = watcher
	seam := registered(t, f, nil)
	before := len(seam.calls)
	if exit, out, errOut := f.run(SubSessionEnd, "{oops"); exit != 0 || out != "" || !strings.Contains(errOut, "bad stdin") {
		t.Fatalf("exit %d out %q err %q", exit, out, errOut)
	}
	if !alive(watcher) || !f.mapExists() || len(seam.calls) != before {
		t.Fatalf("bad stdin touched the session: alive %v map %v calls %d", alive(watcher), f.mapExists(), len(seam.calls)-before)
	}
}

// TestSessionEndCloseFailureStillExitsZero: a close the adapter refuses
// or cannot complete changes nothing about the exit status; lease expiry
// is authoritative.
func TestSessionEndCloseFailureStillExitsZero(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, map[string][]fakeadapter.Response{"session close": {errResp("unavailable", "timeout")}})
	if exit, out, errOut := f.run(SubSessionEnd, f.endDoc("other")); exit != 0 || out != "" || !strings.Contains(errOut, "close did not complete") {
		t.Fatalf("exit %d out %q err %q", exit, out, errOut)
	}
	if f.mapExists() {
		t.Fatal("the map survived a failed close")
	}
}

// TestSessionEndGivesItsCloseWhatItsBudgetLeaves: the close is made under
// the end's budget (Deps.EndBudget, 1.4 s in production, which
// TestRealDepsAreTheDocumentedBudgets pins). An end whose budget is spent
// asks for the close under a deadline that has passed, says that the close
// did not complete, and still takes the session down on this machine. The
// control is TestSessionEndReasons, whose ends close under the fixture's
// budget. Every fixture has a long one (unhurried): were the end bounded
// by anything but the dependency, by the constant it once read, a busy
// machine would decide what the tests of an end see (work item 336).
func TestSessionEndGivesItsCloseWhatItsBudgetLeaves(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, map[string][]fakeadapter.Response{"session close": {okResp(closeDoc())}})
	// The backend answers a close at once, and not one whose caller has
	// stopped waiting: the seam itself reads no deadline.
	var asked, spent atomic.Int64
	inner := f.deps.Spawn
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) != "session close" {
			return inner(ctx, spec)
		}
		asked.Add(1)
		if ctx.Err() == nil {
			return inner(ctx, spec)
		}
		spent.Add(1)
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "the caller stopped waiting"}
	}
	f.deps.EndBudget = time.Nanosecond
	exit, out, errOut := f.run(SubSessionEnd, f.endDoc("other"))
	if exit != 0 || out != "" {
		t.Fatalf("exit %d out %q err %q", exit, out, errOut)
	}
	if asked.Load() != 1 || spent.Load() != 1 || !strings.Contains(errOut, "close did not complete") {
		t.Fatalf("an end with a nanosecond's budget asked for %d closes, %d of them under a deadline that had passed, want one and one, and to say the close did not complete:\n%s",
			asked.Load(), spent.Load(), errOut)
	}
	if f.mapExists() {
		t.Fatal("the map survived an end whose budget was spent")
	}
}

// TestSessionEndRunsUnderABudgetMadeLonger is the other half of
// TestSessionEndGivesItsCloseWhatItsBudgetLeaves, which makes the budget
// shorter. Every fixture makes it longer, a minute (unhurried), and that has
// to be the end's budget too: an end that took the lesser of its dependency
// and production's 1.4 s passed that test and every other, with every
// fixture back on the budget a busy machine uses up (work item 336, a second
// reviewer's mutation).
//
// Here one step of the end takes longer than the whole of production's
// budget: its look at the watcher's process (Deps.Lookup), which comes after
// the state entry and before the close, takes a second and a half. The
// close is then asked under a deadline that had not passed when the look
// returned. Two instants are compared, and not a deadline with the
// machine's clock: under a budget of 1.4 s the close's deadline is the
// end's, which the look outlasted however fast the machine; under the
// fixture's minute it is the close's own second
// (adapterclient.CloseTimeout), counted from after the look.
func TestSessionEndRunsUnderABudgetMadeLonger(t *testing.T) {
	t.Parallel()
	// Written out, and more than production's 1.4 s.
	const slow = 1500 * time.Millisecond
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, map[string][]fakeadapter.Response{"session close": {okResp(closeDoc())}})
	if f.deps.EndBudget != time.Minute {
		t.Fatalf("the fixture's end has a budget of %v, want its minute: the test holds nothing of a longer budget otherwise", f.deps.EndBudget)
	}
	// The end's look at the watcher's process takes its time, and notes
	// when it returned.
	var looks, asked atomic.Int32
	var looked, closeBy atomic.Pointer[time.Time]
	lookup, watcher := f.deps.Lookup, f.spawner.watcherPID
	f.deps.Lookup = func(pid int) (procutil.Info, error) {
		info, err := lookup(pid)
		if pid == watcher {
			time.Sleep(slow)
			now := time.Now()
			looked.Store(&now)
			looks.Add(1)
		}
		return info, err
	}
	// The backend notes the deadline its close was asked under.
	inner := f.deps.Spawn
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) == "session close" {
			asked.Add(1)
			if by, ok := ctx.Deadline(); ok {
				closeBy.Store(&by)
			}
		}
		return inner(ctx, spec)
	}
	exit, out, errOut := f.run(SubSessionEnd, f.endDoc("other"))
	if exit != 0 || out != "" {
		t.Fatalf("exit %d out %q err %q", exit, out, errOut)
	}
	if looks.Load() != 1 || asked.Load() != 1 || closeBy.Load() == nil {
		t.Fatalf("the end looked at its watcher's process %d times and asked for %d closes, under a deadline: %v; want one look, one close and a deadline: the test holds nothing otherwise\n%s",
			looks.Load(), asked.Load(), closeBy.Load() != nil, errOut)
	}
	if left := closeBy.Load().Sub(*looked.Load()); left <= 0 {
		t.Fatalf("an end with a budget of %v asked for its close under a deadline %v before its look at the watcher's process had returned, a look of %v: the end did not run under the budget it was given\n%s",
			f.deps.EndBudget, -left, slow, errOut)
	}
	if f.mapExists() {
		t.Fatal("the map survived the end")
	}
}

// TestSessionEndDeadPidfileIsRemoved: a pidfile whose watcher is already
// gone is removed by content and the close still runs.
func TestSessionEndDeadPidfileIsRemoved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	seam := registered(t, f, map[string][]fakeadapter.Response{"session close": {okResp(closeDoc())}})
	e, err := pidfile.Read(f.pidfilePath())
	if err != nil {
		t.Fatal(err)
	}
	e.StartToken = forge(e.StartToken)
	if err := os.WriteFile(f.pidfilePath(), pidfile.Encode(e), 0o600); err != nil {
		t.Fatal(err)
	}
	if exit, _, _ := f.run(SubSessionEnd, f.endDoc("other")); exit != 0 {
		t.Fatal(exit)
	}
	if _, err := os.Lstat(f.pidfilePath()); err == nil {
		t.Fatal("the dead pidfile survived")
	}
	if len(seam.callsFor("session close")) != 1 {
		t.Fatal("no close")
	}
}
