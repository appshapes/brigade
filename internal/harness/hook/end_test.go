package hook

import (
	"os"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/pidfile"
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
			exit, out, errOut := f.run(SubSessionEnd, f.endDoc(tc.reason))
			if exit != 0 || out != "" {
				t.Fatalf("exit %d out %q err %q", exit, out, errOut)
			}
			closes := seam.callsFor("session close")
			if !tc.teardown {
				if len(closes) != 0 || !alive(watcher) || !f.mapExists() {
					t.Fatalf("reason %q tore the session down: closes %d alive %v map %v", tc.reason, len(closes), alive(watcher), f.mapExists())
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
