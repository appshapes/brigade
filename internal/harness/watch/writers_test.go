package watch_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The exit waits for every goroutine that can write under the state
// directory (P14-6, writers.go). The witness is Deps.Writers: the fixture
// reads the live count AT the instant Run returns — see
// running.assertNoLiveWriters, which every stop site in this package runs
// — and it must be zero.
//
// This test forces the window the intermittent failure found by itself: it
// holds the re-open's `session register` inside the spawn seam, asks the
// watcher to stop, and lets the register go only once the exit path has
// reached its last step before the join (the "watch child ended" line).
// With the join, Run cannot have returned yet and the count at its return
// is zero. Without it — the join removed and the seam kept — Run has
// already returned by then, with the re-open writer still inside the
// spawn that creates ${stateDir}/logs/adapter-<profile>.log and the child
// which writes the store: exactly the writer that outlived exit 0 on CI
// run 34778172498.
func TestTheExitJoinsAReopenWriter(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	now := time.Now().UTC().Format(time.RFC3339)
	closed := &protocol.WatchError{Event: protocol.EventError, Error: *closedConflict()}
	fx.useFake(fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"session register": {{Result: resumedRegister(now)}},
		},
		Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t), {Raw: rawLine(t, closed)}}},
	})
	fx.writeMapWith(func(m *sessionmap.ByPID) { m.BrigadeSessionID = reopenSessionID })

	entered := make(chan struct{})
	enter := sync.OnceFunc(func() { close(entered) })
	release := make(chan struct{})
	letGo := sync.OnceFunc(func() { close(release) })
	defer letGo() // a failed assertion never leaves the watcher joined to a blocked writer

	deps := fx.deps()
	deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if strings.Contains(strings.Join(spec.Argv, " "), " session register") {
			enter()
			<-release
		}
		return adapterkit.Spawn(ctx, spec)
	}
	r := fx.start(deps, fx.args()...)
	fx.waitLog("the session was closed under the watcher; re-opening", nil)
	select {
	case <-entered:
	case <-time.After(waitShort): // a hang catcher, not a bound
		t.Fatal("the re-open never reached its spawn")
	}

	// The writer is live and cannot finish until this test says so.
	r.requestStop()
	fx.waitLog("watch child ended", nil)
	letGo()

	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := r.writersAtExit(); n != 0 {
		t.Fatalf("state-directory writers live when Run returned = %d, want 0", n)
	}
	// The join is what makes the re-open's whole effect — the adapter log,
	// the child, the registration — precede the exit, so its own line is
	// already in the log with no waiting at all.
	if !fx.logHas("session re-opened", map[string]any{"session_id": reopenSessionID}) {
		t.Error("the re-open had not finished when Run returned")
	}
}
