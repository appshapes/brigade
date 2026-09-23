// instance.go is the lifecycle of the dedicated Syncthing instance (plan
// 4.4): the home directory, the per-session refs, the pidfile, the GUI
// port, and the start and stop of the daemon. It is the ONE place Brigade
// starts a long-running daemon (the .golangci.yml exec carve-out names this
// package), and it carries the darwin || linux constraint (D33) because
// Setsid and procutil are unix-only.

//go:build darwin || linux

package syncthing

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/procutil"
)

// maxLogBytes caps syncthing.log: a start truncates a log past it (plan
// 4.4), which is all the rotation a session-scoped daemon needs.
const maxLogBytes = 4 << 20

// An instance is the Syncthing home under one state directory.
type instance struct {
	home string
	a    *adapter
}

func newInstance(stateDir string, a *adapter) *instance {
	return &instance{home: filepath.Join(stateDir, "sync", "syncthing"), a: a}
}

func (in *instance) path(name string) string { return filepath.Join(in.home, name) }
func (in *instance) refsDir() string         { return in.path("refs") }

// prepare creates the home and refs/ (0700) and takes the lock every start
// and stop decision is made under, so two sessions attaching at once start
// one daemon and a detach racing an attach never stops a daemon the attach
// has just counted itself into.
func (in *instance) prepare() (*adapterkit.FileLock, error) {
	if err := adapterkit.MkdirPrivate(in.refsDir()); err != nil {
		return nil, err
	}
	return adapterkit.LockFile(in.path("lock"), lockWait)
}

// attach counts the session in and makes sure the daemon runs, then
// answers its device id (plan 4.3: the engine is running afterwards, with
// this session counted).
func (in *instance) attach(sessionID string) (*attachResult, error) {
	lock, err := in.prepare()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	ref := filepath.Join(in.refsDir(), sessionID)
	if err := os.WriteFile(ref, nil, 0o600); err != nil {
		return nil, err
	}
	pid, running := in.daemon()
	if !running {
		if pid, err = in.start(); err != nil {
			// The session is not attached after all: the ref must not
			// keep a daemon that never started "in use".
			_ = os.Remove(ref)
			return nil, err
		}
	}
	id, err := in.waitReady(pid)
	if err != nil {
		if !running {
			in.kill(pid)
		}
		_ = os.Remove(ref)
		return nil, err
	}
	in.a.log.Info("attached", slog.String("session_id", sessionID), slog.Bool("started", !running), slog.Int("daemon_pid", pid))
	return &attachResult{Peer: id}, nil
}

// detach counts the session out and, when it was the last, stops the
// daemon: sync runs only while a session is active (plan section 1).
func (in *instance) detach(sessionID string) (*detachResult, error) {
	lock, err := in.prepare()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	if err := os.Remove(filepath.Join(in.refsDir(), sessionID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	refs, err := os.ReadDir(in.refsDir())
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 {
		return &detachResult{Stopped: false}, nil
	}
	if pid, running := in.daemon(); running {
		in.stop(pid)
	}
	_ = os.Remove(in.path("daemon.pid"))
	in.a.log.Info("detached the last session; the daemon is stopped", slog.String("session_id", sessionID))
	return &detachResult{Stopped: true}, nil
}

// daemon reads daemon.pid ("<pid> <start token>") and reports whether it
// names a live process — the same incarnation that was started, so a pid
// the kernel has since reused reads as stopped. A missing, unreadable or
// stale pidfile is "not running"; start replaces it.
func (in *instance) daemon() (int, bool) {
	raw, err := os.ReadFile(in.path("daemon.pid"))
	if err != nil {
		return 0, false
	}
	pidText, token, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, alive(pid, token)
}

// alive reports whether pid is a live process of this user (a zombie is
// dead, E0-5) whose start token is token when one is given.
func alive(pid int, token string) bool {
	info, err := procutil.Lookup(pid)
	if err != nil || !info.Exists || info.Zombie || info.Foreign {
		return false
	}
	return token == "" || info.StartToken == token
}

// start launches `syncthing serve` detached (plan 3.2, 4.4): no generate
// step — v2 has no --no-default-folder, and serve on a fresh home creates
// the certificate and a config.xml with an empty folder list — its own
// session, stdin from the null device, both output streams appended to
// syncthing.log, the allow-listed environment, and the GUI (the REST API)
// on 127.0.0.1 only.
func (in *instance) start() (int, error) {
	prefix, err := in.a.d.syncthing()
	if err != nil || len(prefix) == 0 {
		return 0, errUnavailable("syncthing_not_found", "syncthing is not on PATH; install it to sync folders")
	}
	port, err := in.port()
	if err != nil {
		return 0, err
	}
	logPath := in.path("syncthing.log")
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > maxLogBytes {
		_ = os.Truncate(logPath, 0)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600 under the Brigade state directory by design
	if err != nil {
		return 0, err
	}
	defer func() { _ = logf.Close() }() // the child holds its own descriptor
	argv := append(append([]string{}, prefix[1:]...),
		"serve", "--home", in.home,
		"--no-browser", "--no-restart", "--no-upgrade", "--no-port-probing",
		"--gui-address", "127.0.0.1:"+strconv.Itoa(port))
	// A context that is never cancelled: the daemon outlives this process
	// by design, and nothing of this invocation may kill it. This is the
	// one daemon Brigade starts (plan 4.4) and the package's .golangci.yml
	// carve-out: an argv array, the allow-listed environment, no shell.
	cmd := exec.CommandContext(context.Background(), prefix[0], argv...)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Dir = in.home
	cmd.Env = adapterkit.ChildEnv(in.a.environ)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return 0, errUnavailable("syncthing_not_found", "syncthing is not on PATH; install it to sync folders")
		}
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	info, _ := procutil.Lookup(pid)
	_ = os.Remove(in.path("daemon.pid")) // stale: daemon() found it dead, under the lock
	if err := adapterkit.WritePidfile(in.path("daemon.pid"), []byte(strconv.Itoa(pid)+" "+info.StartToken+"\n")); err != nil {
		in.kill(pid)
		return 0, err
	}
	in.a.log.Info("syncthing started", slog.Int("daemon_pid", pid), slog.Int("gui_port", port))
	return pid, nil
}

// port is the instance's GUI port, picked once as a free loopback port
// and kept in <home>/port so the API stays where the book-keeping says it
// is. A kept port another program has taken since is replaced by a new
// pick, because a daemon that cannot bind its API never becomes ready.
func (in *instance) port() (int, error) {
	if raw, err := os.ReadFile(in.path("port")); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && p > 0 && p < 65536 && portFree(p) {
			return p, nil
		}
	}
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if err := adapterkit.WriteAtomic(in.path("port"), []byte(strconv.Itoa(p)+"\n")); err != nil {
		return 0, err
	}
	return p, nil
}

func portFree(p int) bool {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// currentPort reads the kept port for the REST calls of a running daemon.
func (in *instance) currentPort() (int, error) {
	raw, err := os.ReadFile(in.path("port"))
	if err != nil {
		return 0, errUnavailable("not_running", "the syncthing instance has no API port recorded")
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || p <= 0 || p >= 65536 {
		return 0, errUnavailable("not_running", "the syncthing instance's API port record is unreadable")
	}
	return p, nil
}

// waitReady waits up to startWait for config.xml to carry the API key and
// for GET /rest/system/status to answer, and returns the device id
// (myID). A daemon that exits meanwhile fails at once — its reason is in
// syncthing.log, which is Syncthing's own text and not echoed here.
func (in *instance) waitReady(pid int) (string, error) {
	deadline := time.Now().Add(in.a.d.startWait)
	for {
		if !alive(pid, "") {
			return "", errUnavailable("syncthing_exited", "syncthing exited during start; see syncthing.log in its home under the Brigade state directory")
		}
		if api, err := in.api(); err == nil {
			if st, err := api.systemStatus(); err == nil && st.MyID != "" {
				return st.MyID, nil
			}
		}
		if time.Now().After(deadline) {
			return "", errUnavailable("start_timeout", "syncthing did not answer on its API within "+in.a.d.startWait.String())
		}
		time.Sleep(pollEvery)
	}
}

// stop asks the daemon to shut down over the API (measured: it is gone
// within 2 s, plan 3.2), and signals it only when that fails or it
// lingers: SIGTERM, then SIGKILL after stopWait.
func (in *instance) stop(pid int) {
	if api, err := in.api(); err == nil {
		if err := api.shutdown(); err == nil && in.waitGone(pid) {
			return
		} else if err != nil {
			in.a.log.Warn("syncthing shutdown over the API failed; signalling", adapterlog.Err(err))
		}
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		in.a.log.Warn("SIGTERM to syncthing", adapterlog.Err(err), slog.Int("daemon_pid", pid))
	}
	if !in.waitGone(pid) {
		in.kill(pid)
	}
}

// kill SIGKILLs pid when it is still this user's live process.
func (in *instance) kill(pid int) {
	if alive(pid, "") {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// waitGone polls for pid to die for up to stopWait.
func (in *instance) waitGone(pid int) bool {
	deadline := time.Now().Add(in.a.d.stopWait)
	for alive(pid, "") {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollEvery)
	}
	return true
}
