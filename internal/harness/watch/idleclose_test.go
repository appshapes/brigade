package watch_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/registry"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

// The idle close tests (card 61, idleclose.go). A test clock — the real
// time plus an offset the test moves — stands in for the hours, so the
// liveness tick reads them without waiting; the transcript and the
// registry entry are real files, as in production.

// A testClock is the watcher's Clock dependency under the test's control.
type testClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

// hoursPtr is the shape IdleCloseHours takes.
func hoursPtr(n int) *int { return &n }

// settle waits a few liveness ticks, so the watcher has read the clock,
// the transcript and the registry since the test last changed one.
func settle(d watch.Deps) { time.Sleep(8 * d.PollInterval) }

const idleClosing = "no activity for the team's idle close; closing the session"

// writeTranscriptLine appends one record to the session's transcript,
// which is what a prompt does; the watcher reads only its mtime here.
func writeTranscriptLine(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeRegistryEntry writes Claude Code's registry entry for the fixture's
// pid with the given status, where the watcher's registry read looks.
func writeRegistryEntry(t *testing.T, fx *fixture, status string) {
	t.Helper()
	dir := filepath.Join(fx.dirs.ClaudeConfig, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := ""
	if fx.sock != nil {
		socket = fx.sock.Path()
	}
	entry := fakeregistry.Observed(fx.claudePID, fx.name, status, socket)
	if err := os.WriteFile(filepath.Join(dir, registry.FileName(fx.claudePID)), []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestIdleCloseEndsAVSCodeSession: a session the VS Code extension
// started, idle for the team's hour, is closed by its own watcher: the
// clean exit path — the store shows closed_at, the pidfile is gone, the
// map stays for the prompt hook's respawn — with reason idle_expired. A
// minute short of the threshold it is still running.
func TestIdleCloseEndsAVSCodeSession(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.Entrypoint = "claude-vscode"
		m.IdleCloseHours = hoursPtr(1)
	})
	clock := &testClock{}
	d := fx.deps()
	d.Clock = clock.now
	r := fx.start(d)
	fx.waitLog("watch ready", nil)
	if !fx.logHas("idle close armed", nil) {
		t.Fatalf("the idle close was not armed: %v", fx.logLines())
	}
	clock.advance(59 * time.Minute)
	settle(d)
	if r.exited() || fx.logHas(idleClosing, nil) {
		t.Fatalf("closed a minute short of the threshold: %v", fx.logLines())
	}
	clock.advance(2 * time.Minute)
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !fx.logHas(idleClosing, nil) || !fx.logHas("closing the session", map[string]any{"reason": "idle_expired"}) {
		t.Fatalf("no idle close in the log: %v", fx.logLines())
	}
	if s := fx.session(); s.ClosedAt == nil {
		t.Errorf("the session was not closed in the store: %+v", s)
	}
	if _, err := os.Stat(fx.pidfilePath()); !os.IsNotExist(err) {
		t.Errorf("pidfile still present: %v", err)
	}
	if _, err := fx.store().ReadByPID(fx.claudePID); err != nil {
		t.Errorf("the watcher removed the by-pid map, which the next prompt's respawn needs: %v", err)
	}
}

// TestIdleCloseNeverTouchesOtherHostsOrAnOffTeam: the CLI, a -p session,
// a session with no entrypoint, a host nobody measured, and a VS Code
// session on a team that set 0 all run on through a day of idleness.
func TestIdleCloseNeverTouchesOtherHostsOrAnOffTeam(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(*sessionmap.ByPID){
		"the CLI":                             func(m *sessionmap.ByPID) { m.Entrypoint = "cli" },
		"a -p session":                        func(m *sessionmap.ByPID) { m.Entrypoint = "sdk-cli" },
		"no entrypoint":                       func(*sessionmap.ByPID) {},
		"a host nobody measured":              func(m *sessionmap.ByPID) { m.Entrypoint = "jetbrains" },
		"VS Code with the close switched off": func(m *sessionmap.ByPID) { m.Entrypoint = "claude-vscode"; m.IdleCloseHours = hoursPtr(0) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newFixture(t, fixtureOptions{})
			fx.useFS()
			fx.writeMapWith(edit)
			clock := &testClock{}
			d := fx.deps()
			d.Clock = clock.now
			r := fx.start(d)
			fx.waitLog("watch ready", nil)
			if fx.logHas("idle close armed", nil) {
				t.Fatalf("the idle close was armed: %v", fx.logLines())
			}
			clock.advance(24 * time.Hour)
			settle(d)
			if r.exited() || fx.logHas(idleClosing, nil) {
				t.Fatalf("the watcher closed the session: %v", fx.logLines())
			}
			if code := r.stopAndWait(); code != 0 {
				t.Fatalf("exit %d, want 0", code)
			}
			if fx.logHas("closing the session", map[string]any{"reason": "idle_expired"}) {
				t.Fatalf("the exit was an idle close: %v", fx.logLines())
			}
		})
	}
}

// TestIdleCloseResetsOnActivity: a transcript write (a prompt) and the
// registry saying busy (the model working, on a prompt or a teammate's
// message) each restart the hour; the file that already exists at the
// start is a baseline, not activity. After the last activity the hour
// runs out as before.
func TestIdleCloseResetsOnActivity(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{})
	fx.useFS()
	transcript := filepath.Join(fx.dirs.Root, "transcript.jsonl")
	writeTranscriptLine(t, transcript)
	fx.writeMapWith(func(m *sessionmap.ByPID) {
		m.Entrypoint = "claude-vscode"
		m.IdleCloseHours = hoursPtr(1)
		m.TranscriptPath = transcript
	})
	clock := &testClock{}
	d := fx.deps()
	d.Clock = clock.now
	r := fx.start(d)
	fx.waitLog("watch ready", nil)
	settle(d) // the first look at the transcript is the baseline

	clock.advance(50 * time.Minute)
	settle(d)
	writeTranscriptLine(t, transcript) // the person prompted
	settle(d)
	clock.advance(50 * time.Minute) // 100 min since the start, 50 since the prompt
	settle(d)
	if r.exited() || fx.logHas(idleClosing, nil) {
		t.Fatalf("closed although the transcript moved 50 minutes ago: %v", fx.logLines())
	}

	writeRegistryEntry(t, fx, "busy")
	fx.waitLog("activity changed", map[string]any{"activity": "busy"})
	clock.advance(3 * time.Hour) // busy on every tick: the clock never runs
	settle(d)
	if r.exited() || fx.logHas(idleClosing, nil) {
		t.Fatalf("closed while the registry said busy: %v", fx.logLines())
	}

	writeRegistryEntry(t, fx, "idle")
	fx.waitLog("activity changed", map[string]any{"activity": "idle"})
	clock.advance(59 * time.Minute)
	settle(d)
	if r.exited() || fx.logHas(idleClosing, nil) {
		t.Fatalf("closed 59 minutes after the last busy tick: %v", fx.logLines())
	}
	clock.advance(2 * time.Minute)
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !fx.logHas("closing the session", map[string]any{"reason": "idle_expired"}) {
		t.Fatalf("no idle close in the log: %v", fx.logLines())
	}
	if s := fx.session(); s.ClosedAt == nil {
		t.Errorf("the session was not closed in the store: %+v", s)
	}
}
