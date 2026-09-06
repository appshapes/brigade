// spawn.go holds the ONE watcher spawn of the harness (plan 6.6, 7.3): the
// detached `brigade watch` child the SessionStart and prompt hooks start.
// It is the second exec.CommandContext the forbidigo rule allows (the
// other is adapterclient/spawn.go, the `message watch` adapter child), and
// it carries the darwin || linux constraint (D33) because Setsid is a unix
// process attribute.

//go:build darwin || linux

package hook

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

// A SpawnSpec is everything the watcher child is started with. The
// executable is this binary (os.Executable) and is not a member: a map or
// an option must never be able to name the program the hook detaches.
type SpawnSpec struct {
	// Args is the argv after the program name: "watch", plus "--sink
	// <file>" when Deps.Sink is set.
	Args []string
	// Env is the child's whole environment, built from scratch by
	// watcherEnviron. The messaging token is in here and nowhere else.
	Env []string
	// Dir is the child's working directory (the user's HOME).
	Dir string
	// LogPath is the 0600 append-mode file both of the child's output
	// streams go to: ${stateDir}/logs/watcher-<claude_pid>.log.
	LogPath string
}

// RealSpawner starts the detached watcher for real (6.6): stdin from the
// null device, stdout and stderr to the log file, its own session and
// process group (Setsid), no controlling terminal, the given environment
// and nothing else; started, released, never waited for.
type RealSpawner struct{}

// Spawn implements Spawner.
func (RealSpawner) Spawn(ctx context.Context, spec SpawnSpec) (int, error) {
	if err := adapterkit.MkdirPrivate(filepath.Dir(spec.LogPath)); err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600 under BRIGADE_STATE_DIR by design
	if err != nil {
		return 0, err
	}
	defer func() { _ = logf.Close() }() // the child holds its own descriptor
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	// The child must outlive this process, so it is bound to a context
	// that is never cancelled (the ctx's cancellation would kill it).
	//nolint:forbidigo,gosec // hook/spawn.go IS the one watcher-spawn seam the 7.3 rule allows (6.6); argv is fixed, no shell
	cmd := exec.CommandContext(context.WithoutCancel(ctx), self, spec.Args...)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// watcherLogPath is ${stateDir}/logs/watcher-<claude_pid>.log (3.2).
func watcherLogPath(stateDir string, claudePID int) string {
	return filepath.Join(stateDir, "logs", "watcher-"+strconv.Itoa(claudePID)+".log")
}

// watcherEnviron builds the watcher's environment from scratch (6.6, 3.2):
// the adapter-child allow-list of environ (PATH, HOME, TMPDIR, LANG, LC_*,
// XDG_*, CLAUDE_CONFIG_DIR, the proxy variables, SSL_CERT_*; every
// inherited BRIGADE_* and CLAUDE_CODE_MESSAGING_* dropped), then the six
// hook-built BRIGADE_* of config.WatcherEnv.Vars, BRIGADE_LOG_LEVEL, and the
// socket and token as the hook saw them. The token travels here and only
// here (T13.4).
func watcherEnviron(environ []string, w config.WatcherEnv, socket, token, logLevel string) ([]string, error) {
	vars, err := w.Vars()
	if err != nil {
		return nil, err
	}
	out := adapterkit.ChildEnv(environ, vars...)
	out = append(out, "BRIGADE_LOG_LEVEL="+logLevel)
	if socket != "" {
		out = append(out, envMessagingSocket+"="+socket)
	}
	if token != "" {
		out = append(out, envMessagingToken+"="+token)
	}
	return out, nil
}

// spawnWatcher starts the watcher for the session m describes and waits
// up to pidfileWait for its pidfile (the watcher writes it, 6.6). A
// watcher that does not appear is logged, not an error: the next prompt
// hook respawns it.
func (r *run) spawnWatcher(ctx context.Context, f facts, m *sessionmap.ByPID, pidfileWait time.Duration) {
	if f.socket == "" && r.deps.Sink == "" {
		r.log.Info("no inbox socket in this session; the watcher is not started (poll_on_prompt is the fallback)")
		return
	}
	adapter, err := config.AdapterFromArgv(m.AdapterCommand)
	if err != nil {
		r.log.Warn("watcher not started: adapter command", log.Err(err))
		return
	}
	env, err := watcherEnviron(r.environ, config.WatcherEnv{
		ClaudePID:   f.pid,
		Profile:     m.TeamKey,
		ConfigDir:   m.ConfigDir,
		StateDir:    f.stateDir,
		Adapter:     adapter,
		TeamInbound: config.Inbound(m.Inbound),
	}, f.socket, f.token, r.logLevel)
	if err != nil {
		r.log.Warn("watcher not started: environment", log.Err(err))
		return
	}
	args := []string{"watch"}
	if r.deps.Sink != "" {
		args = append(args, "--sink", r.deps.Sink)
	}
	dir := f.home
	if dir == "" || !filepath.IsAbs(dir) {
		dir = f.stateDir
	}
	pid, err := r.deps.Spawner.Spawn(ctx, SpawnSpec{
		Args:    args,
		Env:     env,
		Dir:     dir,
		LogPath: watcherLogPath(f.stateDir, f.pid),
	})
	if err != nil {
		r.log.Warn("watcher not started", log.Err(err))
		return
	}
	r.log.Info("watcher started", slog.Int("watcher_pid", pid))
	if !r.awaitPidfile(f, pidfileWait) {
		r.log.Warn("watcher pidfile did not appear; the next prompt respawns", slog.Int("watcher_pid", pid))
	}
}

// awaitPidfile polls for this pid's watcher pidfile until it exists or
// wait elapses (the system clock: this is a real wait, not a timestamp).
func (r *run) awaitPidfile(f facts, wait time.Duration) bool {
	path := pidfile.Path(f.stateDir, f.pid)
	deadline := time.Now().Add(wait)
	for {
		v, err := pidfile.Check(path, r.deps.Lookup)
		if err == nil && v.Found {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pidfilePollInterval)
	}
}

// pidfilePollInterval is the poll interval of awaitPidfile and stopWatcher.
const pidfilePollInterval = 25 * time.Millisecond

// stopWatcher SIGTERMs the watcher e describes and waits up to
// watcherStopWait for its pidfile to be retired or its process to die,
// so the replacement's O_EXCL create does not collide with a live holder
// (6.3, D9).
func (r *run) stopWatcher(e pidfile.Entry, f facts) {
	if err := syscall.Kill(e.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		r.log.Debug("SIGTERM to the watcher", log.Err(err), slog.Int("watcher_pid", e.PID))
	}
	path := pidfile.Path(f.stateDir, f.pid)
	deadline := time.Now().Add(watcherStopWait)
	for {
		v, err := pidfile.Check(path, r.deps.Lookup)
		if err != nil || !v.Found || !v.Alive || v.Entry.PID != e.PID {
			return
		}
		if time.Now().After(deadline) {
			r.log.Warn("watcher did not stop in time", slog.Int("watcher_pid", e.PID))
			return
		}
		time.Sleep(pidfilePollInterval)
	}
}
