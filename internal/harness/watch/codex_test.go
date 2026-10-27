//go:build !windows

package watch_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// queueRecorder stands in for `codex` (card 107): a POSIX script that
// appends what it was run with — every argument, SOH-separated, then the
// environment names a test asserts on — to a record file, and exits with
// the given status. Launched as `/bin/sh <script>`, never exec'd, as the
// repository's fixture rule asks.
func queueRecorder(t *testing.T, exit string) (argv []string, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "queue.record")
	script := filepath.Join(dir, "codex.sh")
	// The environment lines first, then STX, then the arguments (a frame
	// carries newlines, so the argument text is last and SOH-separated),
	// then the record separator.
	body := "#!/bin/sh\nrec=$1; code=$2; shift 2\n{\n  env | grep -E '^(CLAUDE_CODE_MESSAGING_|CODEX_HOME=|BRIGADE_|CODEX_THREAD_ID=)' | sort\n" +
		"  printf '\\002'\n  for a in \"$@\"; do printf '%s' \"$a\"; printf '\\001'; done\n  printf '\\036'\n} >> \"$rec\"\nexit \"$code\"\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"/bin/sh", script, record, exit}, record
}

// A queueCall is one recorded run of the stand-in: its arguments after the
// program name and the environment lines it saw.
type queueCall struct {
	args []string
	env  []string
}

// queueCalls parses the record file; a missing file is no calls.
func queueCalls(t *testing.T, record string) []queueCall {
	t.Helper()
	data, err := os.ReadFile(record)
	if err != nil {
		return nil
	}
	var out []queueCall
	for _, rec := range strings.Split(string(data), "\x1e") {
		if rec == "" {
			continue
		}
		envText, argText, _ := strings.Cut(rec, "\x02")
		c := queueCall{args: strings.Split(strings.TrimSuffix(argText, "\x01"), "\x01")}
		for _, l := range strings.Split(envText, "\n") {
			if l != "" {
				c.env = append(c.env, l)
			}
		}
		out = append(out, c)
	}
	return out
}

// TestCodexQueueDeliversTheBareFrame is the Codex counterpart of the sink
// tests (card 107): a message the adapter delivers goes out as one `codex
// queue --thread <the map's thread> --message <frame>` — the Codex variant
// of the frame, no variant-C wrapper — into a from-scratch environment
// that carries the user's CODEX_HOME and nothing of the watcher's own
// BRIGADE_* or any socket secret, and only then is the message acknowledged.
func TestCodexQueueDeliversTheBareFrame(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{host: "codex"})
	fx.extraEnv = []string{"CODEX_HOME=" + fx.dirs.Home + "/.codex"}
	m := fakeMessage("m1", "sender-a", "hello codex, from a teammate")
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t), messageLine(t, m, 0)}}})
	fx.writeMap()
	deps := fx.deps()
	var record string
	deps.QueueCommand, record = queueRecorder(t, "0")
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("injection reported", map[string]any{"message_id": "m1", "outcome": "injected", "ack": true}) == 1
	})
	fx.waitLog("ack sent", nil)
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !fx.logHas("watcher starting", map[string]any{"mode": "queue"}) {
		t.Error("the watcher did not start in queue mode")
	}
	calls := queueCalls(t, record)
	if len(calls) != 1 {
		t.Fatalf("codex was run %d times, want 1: %+v", len(calls), calls)
	}
	c := calls[0]
	if len(c.args) != 5 || c.args[0] != "queue" || c.args[1] != "--thread" || c.args[2] != "native-1" || c.args[3] != "--message" {
		t.Fatalf("codex argv %q", c.args)
	}
	frame := c.args[4]
	switch {
	case !strings.HasPrefix(frame, "<brigade-message "):
		t.Errorf("the message is not a bare frame: %q", frame[:min(len(frame), 80)])
	case strings.Contains(frame, "<cross-session-message"):
		t.Error("the frame carries Claude Code's wrapper")
	case !strings.Contains(frame, "another person's coding session"), !strings.Contains(frame, "codex queue cannot reach Brigade sessions"):
		t.Errorf("the frame is not the Codex variant: %q", frame)
	case !strings.Contains(frame, "hello codex, from a teammate"):
		t.Error("the frame lacks the body")
	}
	env := strings.Join(c.env, "\n")
	if !strings.Contains(env, "CODEX_HOME="+fx.dirs.Home+"/.codex") {
		t.Errorf("the child did not get CODEX_HOME: %q", env)
	}
	for _, forbidden := range []string{"BRIGADE_", "CLAUDE_CODE_MESSAGING_", "CODEX_THREAD_ID"} {
		if strings.Contains(env, forbidden) {
			t.Errorf("the child's environment carries %s: %q", forbidden, env)
		}
	}
}

// TestCodexQueueFollowsTheThreadOfTheMap pins D3's watcher half (card
// 107): Codex's /new rewrites the map with a new thread id at the new
// chat's first prompt, and the next frame is queued into that thread, not
// the one the watcher started with.
func TestCodexQueueFollowsTheThreadOfTheMap(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{host: "codex"})
	m1, m2 := fakeMessage("m1", "sender-a", "first"), fakeMessage("m2", "sender-a", "second")
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{
		readyLine(t), messageLine(t, m1, 0), messageLine(t, m2, 1500),
	}}})
	fx.writeMap()
	deps := fx.deps()
	var record string
	deps.QueueCommand, record = queueRecorder(t, "0")
	r := fx.start(deps, fx.args()...)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("injection reported", map[string]any{"message_id": "m1", "outcome": "injected"}) == 1
	})
	fx.writeMapWith(func(m *sessionmap.ByPID) { m.ClaudeSessionID = "native-2" })
	fx.waitLog("native session id updated from the by-pid map", nil)
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("injection reported", map[string]any{"message_id": "m2", "outcome": "injected"}) == 1
	})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := queueCalls(t, record)
	if len(calls) != 2 || calls[0].args[2] != "native-1" || calls[1].args[2] != "native-2" {
		var threads []string
		for _, c := range calls {
			threads = append(threads, c.args[2])
		}
		t.Fatalf("threads queued into %v, want [native-1 native-2]", threads)
	}
}

// TestCodexQueueNothingWhenTheProcessIsGone pins the §3.8 rule (card 107):
// exit 0 of `codex queue` means stored, not delivered, and a row queued
// into a thread whose TUI has quit lands on the next resume — so the
// watcher checks the TUI pid right before each queue and queues nothing
// into a dead one, whatever the liveness tick (here far away) has seen.
func TestCodexQueueNothingWhenTheProcessIsGone(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{host: "codex"})
	m := fakeMessage("m1", "sender-a", "lands on nobody")
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t), messageLine(t, m, 700)}}})
	fx.writeMap()
	deps := fx.deps()
	deps.PollInterval = 10 * time.Second
	var record string
	deps.QueueCommand, record = queueRecorder(t, "0")
	r := fx.start(deps, fx.args()...)
	fx.waitLog("watcher starting", map[string]any{"host_pid": fx.hostPID})
	if err := syscall.Kill(fx.hostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	fx.waitLog("injection failed; backing off", map[string]any{"message_id": "m1"})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if calls := queueCalls(t, record); len(calls) != 0 {
		t.Fatalf("codex was run for a dead TUI: %+v", calls)
	}
	if fx.logHas("ack sent", nil) {
		t.Error("a message that was never queued was acknowledged")
	}
	if !fx.logHas("injection failed; backing off", map[string]any{"message_id": "m1", "err": "watch: the Codex process is gone; nothing is queued into a thread with no client"}) {
		t.Errorf("log: %v", fx.logLines())
	}
}

// TestCodexQueueFailureIsRetriedNotAcked: a `codex queue` that exits
// non-zero leaves the message unacknowledged for the server's redelivery,
// and the drain backs off as it does on a failed socket post.
func TestCodexQueueFailureIsRetriedNotAcked(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{host: "codex"})
	m := fakeMessage("m1", "sender-a", "refused by codex")
	fx.useFake(fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t), messageLine(t, m, 0)}}})
	fx.writeMap()
	deps := fx.deps()
	var record string
	deps.QueueCommand, record = queueRecorder(t, "1")
	r := fx.start(deps, fx.args()...)
	fx.waitLog("injection failed; backing off", map[string]any{"message_id": "m1"})
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if calls := queueCalls(t, record); len(calls) == 0 {
		t.Fatal("codex was never run")
	}
	if fx.logHas("ack sent", nil) || fx.logHas("injection reported", map[string]any{"message_id": "m1", "ack": true}) {
		t.Error("a message codex refused was acknowledged")
	}
}

// TestHostFlagRefusals pins the argv rules of `--host` (card 107): only
// codex; never beside --sink or a socket; and a map that is not a Codex
// map is a configuration the watcher refuses.
func TestHostFlagRefusals(t *testing.T) {
	t.Parallel()
	t.Run("another host is usage", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{host: "codex"})
		fx.useFake(fakeadapter.Script{})
		fx.writeMap()
		for _, args := range [][]string{{"--host", "muse"}, {"--host", "claude-code"}, {"--host"}, {"--host", "codex", "--sink", "/abs/sink"}} {
			c, _, e := runOnce(t, fx.environ(), args, fx.deps())
			if c != 2 || !strings.HasPrefix(e, "brigade watch failed (usage): ") {
				t.Errorf("%q: exit %d stderr %q", args, c, e)
			}
		}
	})
	t.Run("a socket beside --host is usage", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{})
		fx.useFake(fakeadapter.Script{})
		fx.writeMap()
		c, _, e := runOnce(t, fx.environ(), []string{"--host", "codex"}, fx.deps())
		if c != 2 || !strings.Contains(e, "--host is refused with --sink or while CLAUDE_CODE_MESSAGING_SOCKET is set") {
			t.Errorf("exit %d stderr %q", c, e)
		}
	})
	t.Run("a map of another harness is config", func(t *testing.T) {
		t.Parallel()
		fx := newFixture(t, fixtureOptions{host: "codex"})
		fx.useFake(fakeadapter.Script{})
		fx.writeMapWith(func(m *sessionmap.ByPID) { m.Harness = "" })
		c, _, _ := runOnce(t, fx.environ(), fx.args(), fx.deps())
		if c != 11 {
			t.Errorf("exit %d, want 11 (config)", c)
		}
		if !fx.logHas("watcher cannot start", map[string]any{"code": "config"}) {
			t.Errorf("log: %v", fx.logLines())
		}
	})
}
