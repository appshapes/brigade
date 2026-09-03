package watch_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// runOnce calls Run with the given environ and args and returns the exit
// status, stdout and stderr. For refusals that happen before the guard.
func runOnce(t *testing.T, environ, args []string, deps watch.Deps) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := watch.Run(args, cli.Streams{In: strings.NewReader(""), Out: &out, Err: &errw}, environ, deps)
	return code, out.String(), errw.String()
}

// TestUsageRefusals: every bad argv is exit 2 with the one-line stderr
// form and nothing on stdout, before any file is touched.
func TestUsageRefusals(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(fakeadapter.Script{})
	fx.writeMap()
	for name, args := range map[string][]string{
		"unknown flag":      {"--bogus"},
		"positional":        {"something"},
		"relative sink":     {"--sink", "relative/file"},
		"sink without path": {"--sink"},
		"bad log level":     {"--log-level", "loud"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			code, out, errw := runOnce(t, fx.environ(), args, fx.deps())
			if code != 2 {
				t.Fatalf("exit %d, want 2 (stderr %q)", code, errw)
			}
			if !strings.HasPrefix(errw, "brigade watch failed (usage): ") || strings.Count(errw, "\n") != 1 {
				t.Errorf("stderr = %q, want one usage line", errw)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if _, err := os.Stat(fx.pidfilePath()); err == nil {
				t.Errorf("a usage refusal wrote the pidfile")
			}
		})
	}
	// Positive control: the same fixture with a good argv proceeds past
	// argument parsing (the watcher runs and stops cleanly).
	r := fx.start(fx.deps(), fx.args()...)
	fx.waitLog("pidfile created", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("control: exit %d, want 0", code)
	}
}

// TestSinkRefusedWithSocketVariable is U-27's watch half: `--sink` with
// CLAUDE_CODE_MESSAGING_SOCKET set is `usage`, so a real session can never
// be diverted to a file; the control is the same `--sink` without the
// variable, which runs.
func TestSinkRefusedWithSocketVariable(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}}})
	fx.writeMap()
	sink := filepath.Join(t.TempDir(), "sink.ndjson")
	// A watcher that accepted the flag would run until stopped: the refusal
	// is run with its own hang catcher so that mutant fails here, not at the
	// package timeout.
	type answer struct {
		code      int
		out, errw string
	}
	refusal := make(chan answer, 1)
	go func() {
		c, o, e := runOnce(t, fx.environ(), []string{"--sink", sink}, fx.deps())
		refusal <- answer{c, o, e}
	}()
	var code int
	var out, errw string
	select {
	case a := <-refusal:
		code, out, errw = a.code, a.out, a.errw
	case <-time.After(waitShort):
		t.Fatalf("--sink with %s set was not refused within %s: the watcher is running", watch.SocketVar, waitShort)
	}
	if code != 2 {
		t.Fatalf("exit %d, want 2 (stderr %q)", code, errw)
	}
	if !strings.Contains(errw, "brigade watch failed (usage): --sink is refused while CLAUDE_CODE_MESSAGING_SOCKET is set") {
		t.Errorf("stderr = %q", errw)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	if _, err := os.Stat(sink); err == nil {
		t.Errorf("the sink file was created")
	}
	// Control: strip the socket variable and the same argv runs in sink mode.
	var environ []string
	for _, kv := range fx.environ() {
		if !strings.HasPrefix(kv, watch.SocketVar+"=") && !strings.HasPrefix(kv, watch.TokenVar+"=") {
			environ = append(environ, kv)
		}
	}
	stop := make(chan struct{})
	deps := fx.deps()
	deps.Stop = stop
	exit := make(chan int, 1)
	go func() {
		c, _, _ := runOnce(t, environ, []string{"--sink", sink}, deps)
		exit <- c
	}()
	fx.waitLog("watch ready", nil)
	close(stop)
	select {
	case c := <-exit:
		if c != 0 {
			t.Fatalf("control exit %d, want 0", c)
		}
	case <-time.After(waitLong):
		t.Fatal("control watcher did not exit")
	}
	if !fx.logHas("watcher starting", map[string]any{"mode": "sink"}) {
		t.Errorf("the control did not run in sink mode: %v", fx.logLines())
	}
}

// TestNothingToInjectIntoIsConfig: no socket variable and no --sink is
// `config` (exit 11); a relative socket path is `config` too.
func TestNothingToInjectIntoIsConfig(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(fakeadapter.Script{})
	fx.writeMap()
	code, _, errw := runOnce(t, fx.environ(), nil, fx.deps())
	if code != 11 || !strings.Contains(errw, "brigade watch failed (config): nothing to inject into") {
		t.Fatalf("exit %d stderr %q, want 11 and the config line", code, errw)
	}
	code, _, errw = runOnce(t, fx.environ(watch.SocketVar+"=relative.sock"), nil, fx.deps())
	if code != 11 || !strings.Contains(errw, "must be an absolute path") {
		t.Fatalf("relative socket: exit %d stderr %q", code, errw)
	}
}

// TestIncompleteWatcherEnvIsConfig: a watcher started without the hook's
// variables (BRIGADE_CLAUDE_PID missing) is `config`, and a hostile
// CLAUDE_PLUGIN_OPTION_* or a registered NAME in BRIGADE_ADAPTER_COMMAND is
// never honoured (FromWatcherEnv refuses a name; options are not read).
func TestIncompleteWatcherEnvIsConfig(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(fakeadapter.Script{})
	fx.writeMap()
	var environ []string
	for _, kv := range fx.environ() {
		if !strings.HasPrefix(kv, "BRIGADE_CLAUDE_PID=") {
			environ = append(environ, kv)
		}
	}
	code, _, errw := runOnce(t, environ, fx.args(), fx.deps())
	if code != 11 || !strings.HasPrefix(errw, "brigade watch failed (config): ") {
		t.Fatalf("exit %d stderr %q, want 11", code, errw)
	}
	// A registered name where the resolved array belongs is refused.
	var named []string
	for _, kv := range fx.environ() {
		if strings.HasPrefix(kv, "BRIGADE_ADAPTER_COMMAND=") {
			kv = "BRIGADE_ADAPTER_COMMAND=fs"
		}
		named = append(named, kv)
	}
	code, _, errw = runOnce(t, named, fx.args(), fx.deps())
	if code != 11 || !strings.HasPrefix(errw, "brigade watch failed (config): ") {
		t.Fatalf("named adapter: exit %d stderr %q, want 11", code, errw)
	}
}

// TestMapIsTheTrustBoundary: a missing by-pid map is `config`
// (not_registered); a map naming another profile than BRIGADE_PROFILE is
// `config`; both after the log opened, so the log carries the reason.
func TestMapIsTheTrustBoundary(t *testing.T) {
	t.Parallel()
	t.Run("missing map", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useFake(fakeadapter.Script{})
		code, out, _ := runOnce(t, fx.environ(), fx.args(), fx.deps())
		if code != 11 || out != "" {
			t.Fatalf("exit %d stdout %q, want 11 and nothing", code, out)
		}
		if !fx.logHas("watcher cannot start", map[string]any{"code": "config"}) {
			t.Errorf("log lacks the config line: %v", fx.logLines())
		}
		if _, err := os.Stat(fx.pidfilePath()); err == nil {
			t.Errorf("a refused start wrote the pidfile")
		}
	})
	t.Run("profile mismatch", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useFake(fakeadapter.Script{})
		fx.writeMapWith(func(m *sessionmap.ByPID) { m.Profile = "other" })
		code, _, _ := runOnce(t, fx.environ(), fx.args(), fx.deps())
		if code != 11 {
			t.Fatalf("exit %d, want 11", code)
		}
		if !fx.logHas("watcher cannot start", map[string]any{"code": "config"}) {
			t.Errorf("log lacks the config line: %v", fx.logLines())
		}
	})
	t.Run("world-readable map", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{sink: true})
		fx.useFake(fakeadapter.Script{})
		fx.writeMap()
		path, _ := fx.store().ByPIDPath(fx.claudePID)
		if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode IS the precondition
			t.Fatal(err)
		}
		code, _, _ := runOnce(t, fx.environ(), fx.args(), fx.deps())
		if code != 11 {
			t.Fatalf("exit %d, want 11 for a map that is not private", code)
		}
	})
}

// TestRealDepsAreTheDocumentedDefaults pins the production knobs of 6.6.
func TestRealDepsAreTheDocumentedDefaults(t *testing.T) {
	t.Parallel()
	d := watch.RealDeps()
	if d.HeartbeatInterval != 30*time.Second || d.PollInterval != 2*time.Second || d.ReadyTimeout != 10*time.Second {
		t.Errorf("intervals: %+v", d)
	}
	if d.CloseWaitClean != time.Second || d.CloseWaitDeath != 3*time.Second || d.ReplaceWait != 2*time.Second {
		t.Errorf("budgets: %+v", d)
	}
	if d.GiveUpFailures != 10 || d.GiveUpWindow != 5*time.Minute || d.LogRotateBytes != 5_000_000 {
		t.Errorf("give-up/rotation: %+v", d)
	}
	if len(d.Signals) != 2 || d.Signals[0] != syscall.SIGTERM || d.Signals[1] != syscall.SIGINT {
		t.Errorf("signals: %v", d.Signals)
	}
	s := d.RestartSchedule()
	if s.Min() != backoff.WatchRestartMin || s.Max() != backoff.WatchRestartMax {
		t.Errorf("restart schedule %v..%v", s.Min(), s.Max())
	}
	s = d.InjectSchedule()
	if s.Min() != backoff.AdapterErrorMin || s.Max() != backoff.AdapterErrorMax {
		t.Errorf("inject schedule %v..%v", s.Min(), s.Max())
	}
	if d.Lookup == nil || d.Signal == nil || d.Clock == nil || d.Registry == nil || d.Post == nil || d.Stop != nil || d.Spawn != nil {
		t.Errorf("seams: %+v", d)
	}
}

// TestZeroDepsFallBackToProduction: a zero Deps (only Stop set) runs with
// the production values — the watcher starts, becomes ready and stops.
func TestZeroDepsFallBackToProduction(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}}})
	fx.writeMap()
	r := fx.start(watch.Deps{}, fx.args()...)
	fx.waitLog("watch ready", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		_, err := os.Stat(fx.pidfilePath())
		return os.IsNotExist(err)
	})
}
