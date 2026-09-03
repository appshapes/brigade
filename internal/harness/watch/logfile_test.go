package watch_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestLogRotatesAtInjectedThreshold: with LogRotateBytes small, a burst
// of debug lines rotates the log once — <log>.1 exists, the live file is
// under the threshold, both 0600, and the earlier generation is replaced
// rather than accumulated.
func TestLogRotatesAtInjectedThreshold(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	lines := []fakeadapter.WatchLine{readyLine(t)}
	for i := range 200 {
		lines = append(lines, messageLine(t, fakeMessage("m"+strconv.Itoa(i), "s"+strconv.Itoa(i), "rotate"), 0))
	}
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	fx.writeMap()
	deps := fx.deps()
	deps.LogRotateBytes = 4000
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		_, err := os.Stat(fx.logPath() + ".1")
		return err == nil
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, p := range []string{fx.logPath(), fx.logPath() + ".1"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, info.Mode().Perm())
		}
		if info.Size() > deps.LogRotateBytes+1024 {
			t.Errorf("%s is %d bytes, over the threshold by more than one line", p, info.Size())
		}
	}
	if _, err := os.Stat(fx.logPath() + ".2"); err == nil {
		t.Errorf("a second generation exists; only one is kept")
	}
	// The live log is still readable NDJSON after the rotation.
	if len(fx.logLines()) == 0 {
		t.Errorf("the live log is empty after rotation")
	}
}

// TestLogIsPrivateNDJSONWithoutRotation: the default threshold never
// rotates a short run; the file is 0600 in a 0700 directory and every
// line is JSON with a level and a message.
func TestLogIsPrivateNDJSONWithoutRotation(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(readyScript(t))
	fx.writeMap()
	r := fx.start(fx.deps(), fx.args()...)
	fx.waitLog("watch ready", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	info, err := os.Stat(fx.logPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log: %v %v", info, err)
	}
	dir, err := os.Stat(fx.dirs.BrigadeState + "/logs")
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("logs dir: %v %v", dir, err)
	}
	if _, err := os.Stat(fx.logPath() + ".1"); err == nil {
		t.Errorf("a short run rotated the log")
	}
	for _, l := range fx.logLines() {
		if l["level"] == nil || l["msg"] == nil || l["time"] == nil {
			t.Errorf("log line lacks level/msg/time: %v", l)
		}
	}
}
