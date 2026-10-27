//go:build !windows

package hook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

type grokSpawnFunc func(context.Context, SpawnSpec) (int, error)

func (f grokSpawnFunc) Spawn(ctx context.Context, spec SpawnSpec) (int, error) { return f(ctx, spec) }

func TestGrokPublishesExactBindingBeforeWatcherSpawn(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	f.deps.Spawner = grokSpawnFunc(func(ctx context.Context, spec SpawnSpec) (int, error) {
		m := f.mustMap()
		ptr, err := f.store().ReadByHost("grok", f.nativeID)
		if err != nil || ptr.HostPID != f.pid || ptr.BrigadeSessionID != m.BrigadeSessionID {
			t.Fatalf("watcher admitted before matching pointer: %+v %v", ptr, err)
		}
		if len(spec.Args) != 3 || filepath.Dir(spec.Args[2]) != inbound.GrokSinkDir(f.stateDir, m.BrigadeSessionID, m.Grok.Incarnation) {
			t.Fatal("watcher sink and published receiver differ")
		}
		return f.spawner.Spawn(ctx, spec)
	})
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	if f.spawner.count() != 1 {
		t.Fatal("watcher was not admitted")
	}
}

// Native first-prompt hooks can overlap SessionStart. Hold the first backend
// registration response while the other hook enters, reproducing two successful
// IDs and an older finish overwriting a newer receiver without synchronization.
func TestGrokOverlappingInitialHooksKeepOneReceiver(t *testing.T) {
	t.Parallel()
	for _, first := range []string{SubSessionStart, SubPrompt} {
		t.Run(first, func(t *testing.T) {
			t.Parallel()
			f := grokFixture(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{
				"session register":  {okResp(registerDoc("first", "grok", false)), okResp(registerDoc("second", "grok", false))},
				"session heartbeat": {okResp(heartbeatDoc())},
			})
			entered, release := make(chan struct{}), make(chan struct{})
			var registrations atomic.Int32
			deps := f.deps
			deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
				result, err := seam.spawn(ctx, spec)
				if verbOf(spec.Argv) == "session register" && registrations.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return result, err
			}
			runHook := func(sub string) {
				event, source := "user_prompt_submit", ""
				if sub == SubSessionStart {
					event, source = "session_start", "new"
				}
				var out, errOut bytes.Buffer
				env := []string{"HOME=" + f.dirs.Home, "PATH=/usr/bin:/bin", "GROK_SESSION_ID=" + f.nativeID, "GROK_WORKSPACE_ROOT=" + f.cwd, "GROK_PLUGIN_ROOT=" + f.dirs.PluginRoot}
				Run([]string{sub, "--host", "grok"}, cli.Streams{In: strings.NewReader(grokDoc(f, event, source, nil)), Out: &out, Err: &errOut}, env, deps)
			}
			firstDone, secondDone := make(chan struct{}), make(chan struct{})
			go func() { runHook(first); close(firstDone) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first registration never entered")
			}
			second := SubPrompt
			if first == SubPrompt {
				second = SubSessionStart
			}
			go func() { runHook(second); close(secondDone) }()
			// A contending hook must not register while the first owns the receiver.
			time.Sleep(100 * time.Millisecond)
			close(release)
			for _, done := range []chan struct{}{firstDone, secondDone} {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("hook did not settle")
				}
			}
			if got := registrations.Load(); got != 1 {
				t.Fatalf("overlapping hooks registered %d backend IDs", got)
			}
			current := f.mustMap()
			for _, atSpawn := range f.spawner.maps {
				if !sessionmap.SameReceiver(current, atSpawn) {
					t.Fatal("published receiver differs from a spawned watcher's binding")
				}
			}
			ptr, err := f.store().ReadByHost("grok", f.nativeID)
			if err != nil || ptr.BrigadeSessionID != current.BrigadeSessionID {
				t.Fatalf("pointer drift: %+v %v", ptr, err)
			}
		})
	}
}

func TestGrokRepeatedNewStartPreservesMonitorIncarnation(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	before := f.mustMap()
	grokRun(f, "stop", grokDoc(f, "stop", "", nil))
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	after := f.mustMap()
	if !sessionmap.SameReceiver(before, after) || after.Grok.MonitorState != "arming-attempted" {
		t.Fatal("same native receiver was replaced by a repeated new hook")
	}
}

func TestGrokMissingWatcherRecoversWithoutRegisteringAgain(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("only-session", "grok", false))}, "session heartbeat": {okResp(heartbeatDoc())}})
	f.spawner.watcherPID = 0
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	before := f.mustMap()
	grokRun(f, "stop", grokDoc(f, "stop", "", nil))
	f.spawner.watcherPID = testutil.NewSleeper(t)
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	after := f.mustMap()
	if len(seam.callsFor("session register")) != 1 || !sessionmap.SameReceiver(before, after) || after.Grok.MonitorState != "arming-attempted" {
		t.Fatal("missing watcher created a new backend or monitor receiver")
	}
	if !sessionmap.SameReceiver(after, f.spawner.lastMap(t)) {
		t.Fatal("replacement watcher was admitted with different receiver state")
	}
}

func TestGrokContinuationDoesNotBorrowAnotherConfiguredProfile(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("old-profile-session", "grok", false)), okResp(registerDoc("configured-profile-session", "grok", false))}})
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	before := f.mustMap()
	before.TeamKey = "different-profile"
	if err := f.store().WriteByPID(before); err != nil {
		t.Fatal(err)
	}
	f.spawner.watcherPID = testutil.NewSleeper(t)
	grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
	after := f.mustMap()
	if len(seam.callsFor("session register")) != 2 || after.BrigadeSessionID != "configured-profile-session" || after.TeamKey == before.TeamKey || after.Grok.Incarnation == before.Grok.Incarnation {
		t.Fatal("another profile's receiver was continued")
	}
}

func TestGrokHookLockOwnershipAndUnsafeFiles(t *testing.T) {
	t.Parallel()
	f := grokFixture(t)
	newHook := func(pid int, id string) *run {
		deps := f.deps.withDefaults()
		deps.Getppid = func() int { return pid }
		deps.Lookup = func(int) (procutil.Info, error) {
			return procutil.Info{Exists: true, Command: "grok", StartToken: "synthetic-start"}, nil
		}
		r := newRun(cli.Streams{}, []string{"HOME=" + f.dirs.Home, "GROK_SESSION_ID=" + id}, deps, hookArgs{sub: SubPrompt, host: config.HostGrok})
		r.grokDeadline = time.Now().Add(time.Second)
		return r
	}
	first := newHook(f.pid, f.nativeID)
	release, err := first.lockGrokHook()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	otherID := "02000000-0000-7000-8000-000000000002"
	for _, r := range []*run{newHook(f.pid, otherID), newHook(f.pid+10000000, f.nativeID)} {
		if unlock, err := r.lockGrokHook(); err == nil {
			unlock()
			t.Fatal("overlapping PID or native identity was not excluded")
		}
	}
	independent := newHook(f.pid+10000000, otherID)
	unlock, err := independent.lockGrokHook()
	if err != nil {
		t.Fatal("independent sibling blocked", err)
	}
	unlock()
	release()
	unlock, err = first.lockGrokHook()
	if err != nil {
		t.Fatal("closed descriptors did not release locks", err)
	}
	unlock()
	path := filepath.Join(f.stateDir, "state", "grok-pid-"+strconv.Itoa(f.pid)+".lock")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "unrelated-owned-data")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if unlock, err := first.lockGrokHook(); err == nil {
		unlock()
		t.Fatal("symlink lock accepted")
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "unchanged" {
		t.Fatal("unrelated fixture changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil { //nolint:gosec // deliberately public owned lock is a refusal precondition
		t.Fatal(err)
	}
	if unlock, err := first.lockGrokHook(); err == nil {
		unlock()
		t.Fatal("public lock accepted")
	}
}
