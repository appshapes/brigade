package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/testutil"
)

// TestPointedStartPointer pins card 116 on both pointed hosts: a session
// that is NOT joined (a team file with no pin) gets the not-joined line,
// its start facts and a start pointer from the native id to their key, so
// that `team join` in the model's shell — which carries the native id and
// not the key — resolves config.StateKey to the facts; a session that
// attaches keeps the start pointer beside the by-host pointer; SessionEnd
// removes both with the facts.
func TestPointedStartPointer(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostCodex, config.HostMuse} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			f := buildFixture(t, host)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			shellEnv := []string{"HOME=" + f.dirs.Home, host.NativeIDVar() + "=" + f.nativeID}
			startDoc := f.museStartDoc("default")
			if host == config.HostCodex {
				startDoc = f.codexDoc("SessionStart", "startup")
			}

			// Not joined: no pin for the checkout.
			if err := os.Remove(filepath.Join(f.configDir, "projects.json")); err != nil {
				t.Fatal(err)
			}
			exit, out, errOut := f.run(SubSessionStart, startDoc)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if !strings.Contains(out, "Brigade: not joined") || f.mapExists() {
				t.Fatalf("not-joined start: out %q, map %v", out, f.mapExists())
			}
			// The remedy names the bootstrap's path: nothing puts a package's
			// bin/ on these shells' PATH, and this is the only Brigade line
			// the model sees before the join.
			if !strings.Contains(out, "run `"+f.pluginBin+" team join` here or in a terminal") {
				t.Fatalf("the not-joined line does not name the bootstrap: %q", out)
			}
			key, err := config.StateKey(shellEnv, f.store())
			if err != nil || key != f.key() {
				t.Fatalf("StateKey from the shell before the join: %d %v, want %d", key, err, f.key())
			}
			if facts, ferr := f.store().ReadStart(key); ferr != nil || facts.ConfigDir != f.configDir {
				t.Fatalf("start facts through the pointer: %+v %v", facts, ferr)
			}
			if f.spawner.count() != 0 {
				t.Fatal("a watcher was spawned for a session that is not joined")
			}

			// Joined (the pin is back): the session attaches; the start pointer
			// stays beside the by-host pointer and both name the key.
			f.seedTeam(t)
			if host == config.HostCodex {
				startDoc = f.codexDoc("SessionStart", "startup")
			} else {
				startDoc = f.museStartDoc("default")
			}
			if exit, _, errOut := f.run(SubSessionStart, startDoc); exit != 0 {
				t.Fatalf("joined exit %d: %s", exit, errOut)
			}
			if !f.mapExists() {
				t.Fatal("the joined session has no map")
			}
			if key, err := config.StateKey(shellEnv, f.store()); err != nil || key != f.key() {
				t.Fatalf("StateKey after attaching: %d %v", key, err)
			}
			if _, err := f.store().ReadStartPointer(string(host), f.nativeID); err != nil {
				t.Fatalf("the start pointer went with the attach: %v", err)
			}

			// SessionEnd takes the facts, both pointers and the map.
			endDoc := f.endDoc("other")
			if host == config.HostCodex {
				endDoc = f.codexDoc("SessionEnd", "")
			}
			if exit, _, errOut := f.run(SubSessionEnd, endDoc); exit != 0 {
				t.Fatalf("end exit %d: %s", exit, errOut)
			}
			if _, err := f.store().ReadStartPointer(string(host), f.nativeID); err == nil {
				t.Fatal("the start pointer survived SessionEnd")
			}
			if _, err := config.StateKey(shellEnv, f.store()); err == nil {
				t.Fatal("StateKey still resolves after SessionEnd")
			}
			if f.startFactsExist() || f.mapExists() {
				t.Fatal("the facts or the map survived SessionEnd")
			}
		})
	}
}

// Keep the sessionmap import honest for the pointer type the test reads.
var _ = sessionmap.StartPointer{}
