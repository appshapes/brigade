// engine.go is the lifecycle of the dedicated Syncthing instance (plan
// folder-sync 4.4): the home directory, the per-session refs, the pidfile,
// the GUI port, and the start and stop of the daemon. It is the ONE place
// Brigade starts a long-running daemon (the .golangci.yml exec carve-out
// names this file), and it carries the darwin || linux constraint (D33)
// because Setsid and procutil are unix-only.

//go:build darwin || linux

package syncthing

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
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

const (
	// StartWait is how long an attach waits for a freshly started daemon
	// to answer on its API.
	StartWait = 15 * time.Second
	// StopWait is how long a detach waits for the daemon to be gone after
	// asking it to shut down, before signalling it.
	StopWait = 5 * time.Second
	// RESTTimeout bounds one REST call.
	RESTTimeout = 5 * time.Second
	lockWait    = 16 * time.Second
	pollEvery   = 100 * time.Millisecond
)

// MaxLogBytes caps syncthing.log: a start truncates a log past it (plan
// 4.4), which is all the rotation a session-scoped daemon needs.
const MaxLogBytes = 4 << 20

// Deps is how an Engine finds the syncthing binary and its API; tests
// substitute a fake script and an httptest server.
type Deps struct {
	// Syncthing answers the argv prefix of the syncthing binary (the
	// path from exec.LookPath, or `/bin/sh <script>` in a test).
	Syncthing func() ([]string, error)
	// BaseURL is the REST API's base for the GUI port Brigade kept.
	BaseURL func(port int) string
	// StartWait and StopWait are the two waits above.
	StartWait time.Duration
	StopWait  time.Duration
}

// RealDeps is the production wiring: syncthing on PATH, the API on
// 127.0.0.1, the package's waits.
func RealDeps() Deps {
	return Deps{
		Syncthing: func() ([]string, error) {
			path, err := exec.LookPath("syncthing")
			if err != nil {
				return nil, err
			}
			return []string{path}, nil
		},
		BaseURL:   func(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) },
		StartWait: StartWait,
		StopWait:  StopWait,
	}
}

// An Engine is the Syncthing home under one state directory:
// <state dir>/sync/syncthing (0700), holding Syncthing's config.xml,
// certificate and index beside Brigade's book-keeping — daemon.pid, port,
// listen-port, lock, syncthing.log and one refs/<session_id> file per
// attached session.
type Engine struct {
	home    string
	d       Deps
	log     *slog.Logger
	environ []string
	http    *http.Client
}

// Home is the instance's home under stateDir.
func Home(stateDir string) string {
	return filepath.Join(stateDir, "sync", "syncthing")
}

// New is the engine under stateDir. log may be nil (nothing is logged);
// environ is the parent environment adapterkit.ChildEnv filters for the
// daemon.
func New(stateDir string, d Deps, log *slog.Logger, environ []string) *Engine {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Engine{
		home: Home(stateDir), d: d, log: log, environ: environ,
		http: &http.Client{Timeout: RESTTimeout, Transport: &http.Transport{Proxy: nil}},
	}
}

func (e *Engine) path(name string) string { return filepath.Join(e.home, name) }
func (e *Engine) refsDir() string         { return e.path("refs") }

// prepare creates the home and refs/ (0700) and takes the lock every start
// and stop decision is made under, so two sessions attaching at once start
// one daemon and a detach racing an attach never stops a daemon the attach
// has just counted itself into.
func (e *Engine) prepare() (*adapterkit.FileLock, error) {
	if err := adapterkit.MkdirPrivate(e.refsDir()); err != nil {
		return nil, err
	}
	return adapterkit.LockFile(e.path("lock"), lockWait)
}

// Attach counts the session in and makes sure the daemon runs, then
// answers its device id (plan 4.3: the engine is running afterwards, with
// this session counted) and whether this call started it. holder is the
// process that holds the reference (0: none given); the ref records it,
// and every attach and detach prunes the refs whose holder is dead.
func (e *Engine) Attach(sessionID string, holder int) (deviceID string, started bool, err error) {
	lock, err := e.prepare()
	if err != nil {
		return "", false, err
	}
	defer func() { _ = lock.Unlock() }()
	ref := filepath.Join(e.refsDir(), sessionID)
	if err := os.WriteFile(ref, refContent(holder), 0o600); err != nil {
		return "", false, err
	}
	e.pruneDeadRefs(sessionID)
	pid, running := e.Daemon()
	listen, err := e.listenPort(!running)
	if err != nil {
		_ = os.Remove(ref)
		return "", false, err
	}
	if !running {
		if pid, err = e.start(); err != nil {
			// The session is not attached after all: the ref must not
			// keep a daemon that never started "in use".
			_ = os.Remove(ref)
			return "", false, err
		}
	}
	id, err := e.waitReady(pid)
	if err != nil {
		if !running {
			e.kill(pid)
		}
		_ = os.Remove(ref)
		return "", false, err
	}
	e.setListen(listen)
	// A repeat attach — the watcher runs one before every apply — is
	// routine: debug, so the sync log does not grow a line every round.
	level := slog.LevelDebug
	if !running {
		level = slog.LevelInfo
	}
	e.log.Log(context.Background(), level, "attached",
		slog.String("session_id", sessionID), slog.Bool("started", !running), slog.Int("daemon_pid", pid))
	return id, !running, nil
}

// refContent is a ref file's body: "<pid> <start token>" for a holder
// that is alive now (the token tells a reused pid from it later), "<pid>"
// for one whose token cannot be read, and empty for no holder — a ref
// only detach removes, as before the pid member existed.
func refContent(holder int) []byte {
	if holder <= 0 {
		return nil
	}
	line := strconv.Itoa(holder)
	if info, err := procutil.Lookup(holder); err == nil && info.StartToken != "" {
		line += " " + info.StartToken
	}
	return []byte(line + "\n")
}

// pruneDeadRefs removes every ref but keep whose holder process is dead:
// gone, a zombie, another user's, or another incarnation of the pid. A
// ref with no pid, or one that cannot be read or looked up, stays — only
// its own detach removes it. Called under the lock.
func (e *Engine) pruneDeadRefs(keep string) {
	entries, err := os.ReadDir(e.refsDir())
	if err != nil {
		return
	}
	for _, en := range entries {
		if en.Name() == keep || en.IsDir() {
			continue
		}
		path := filepath.Join(e.refsDir(), en.Name())
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a ref file under the instance's own refs/
		if err != nil {
			continue
		}
		pidText, token, _ := strings.Cut(strings.TrimSpace(string(raw)), " ")
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			continue
		}
		info, err := procutil.Lookup(pid)
		if err != nil {
			continue
		}
		if info.Exists && !info.Zombie && !info.Foreign && (token == "" || info.StartToken == token) {
			continue
		}
		if err := os.Remove(path); err == nil {
			e.log.Info("pruned the reference of a session whose process is gone",
				slog.String("session_id", en.Name()), slog.Int("holder_pid", pid))
		}
	}
}

// listenPort is the instance's own sync listen port (TCP and QUIC), kept
// in <home>/listen-port beside the GUI port: picked once, free on every
// interface for both protocols, so the instance never shares Syncthing's
// default 22000 with a Syncthing the person runs themselves (on macOS
// both would bind TCP 22000 through SO_REUSEPORT and an incoming
// connection could reach the wrong one). fresh is true when the daemon is
// about to start: a kept port another program holds is then replaced.
// While the daemon runs it holds the port itself, so the kept one is
// taken as it is.
func (e *Engine) listenPort(fresh bool) (int, error) {
	if raw, err := os.ReadFile(e.path("listen-port")); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && p > 0 && p < 65536 && (!fresh || listenFree(p)) {
			return p, nil
		}
	}
	for range 16 {
		p, err := pickListenPort()
		if err != nil {
			return 0, err
		}
		if p == 0 {
			continue
		}
		if err := adapterkit.WriteAtomic(e.path("listen-port"), []byte(strconv.Itoa(p)+"\n")); err != nil {
			return 0, err
		}
		return p, nil
	}
	return 0, ErrUnavailable("no_listen_port", "no port was free for both TCP and UDP to give the syncthing instance its own listen port")
}

// ListenPort is the kept sync listen port, or 0 when none was picked yet
// (a test drives two engines on one machine and needs each one's port to
// address the other).
func (e *Engine) ListenPort() int {
	raw, err := os.ReadFile(e.path("listen-port"))
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || p <= 0 || p >= 65536 {
		return 0
	}
	return p
}

// pickListenPort asks the kernel for a free TCP port on every interface
// and answers it when UDP is free on it too (QUIC), else 0.
func pickListenPort() (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if !listenFree(p) {
		return 0, nil // UDP (or, by now, TCP) is taken on it: the caller picks again
	}
	return p, nil
}

// listenFree reports whether p is free on every interface for TCP and UDP.
func listenFree(p int) bool {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "0.0.0.0:"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	defer func() { _ = l.Close() }()
	u, err := lc.ListenPacket(context.Background(), "udp", "0.0.0.0:"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	_ = u.Close()
	return true
}

// setListen points the running instance's listen addresses at its own
// port (only when they differ). A failure is logged and not fatal: the
// instance still syncs on whatever it listens on, and the next attach
// tries again.
func (e *Engine) setListen(port int) {
	c, err := e.API()
	if err == nil {
		var changed bool
		if changed, err = c.EnsureListen(port); err == nil {
			if changed {
				e.log.Info("syncthing listens on its own port", slog.Int("listen_port", port))
			}
			return
		}
	}
	e.log.Warn("syncthing's listen addresses could not be set; the next attach tries again", adapterlog.Err(err))
}

// Detach counts the session out and, when it was the last, stops the
// daemon: sync runs only while a session is active (plan section 1). It
// reports whether this call stopped it.
func (e *Engine) Detach(sessionID string) (stopped bool, err error) {
	lock, err := e.prepare()
	if err != nil {
		return false, err
	}
	defer func() { _ = lock.Unlock() }()
	if err := os.Remove(filepath.Join(e.refsDir(), sessionID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	e.pruneDeadRefs("")
	refs, err := os.ReadDir(e.refsDir())
	if err != nil {
		return false, err
	}
	if len(refs) > 0 {
		return false, nil
	}
	if pid, running := e.Daemon(); running {
		e.stop(pid)
	}
	_ = os.Remove(e.path("daemon.pid"))
	e.log.Info("detached the last session; the daemon is stopped", slog.String("session_id", sessionID))
	return true, nil
}

// Daemon reads daemon.pid ("<pid> <start token>") and reports whether it
// names a live process — the same incarnation that was started, so a pid
// the kernel has since reused reads as stopped. A missing, unreadable or
// stale pidfile is "not running"; start replaces it.
func (e *Engine) Daemon() (int, bool) {
	raw, err := os.ReadFile(e.path("daemon.pid"))
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
func (e *Engine) start() (int, error) {
	prefix, err := e.d.Syncthing()
	if err != nil || len(prefix) == 0 {
		return 0, ErrUnavailable(ReasonNotFound, "syncthing is not on PATH; install it to sync folders")
	}
	port, err := e.port()
	if err != nil {
		return 0, err
	}
	logPath := e.path("syncthing.log")
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > MaxLogBytes {
		_ = os.Truncate(logPath, 0)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600 under the Brigade state directory by design
	if err != nil {
		return 0, err
	}
	defer func() { _ = logf.Close() }() // the child holds its own descriptor
	argv := append(append([]string{}, prefix[1:]...),
		"serve", "--home", e.home,
		"--no-browser", "--no-restart", "--no-upgrade", "--no-port-probing",
		"--gui-address", "127.0.0.1:"+strconv.Itoa(port))
	// A context that is never cancelled: the daemon outlives this process
	// by design, and nothing of this invocation may kill it. This is the
	// one daemon Brigade starts (plan 4.4) and the file's .golangci.yml
	// carve-out: an argv array, the allow-listed environment, no shell.
	cmd := exec.CommandContext(context.Background(), prefix[0], argv...)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Dir = e.home
	cmd.Env = adapterkit.ChildEnv(e.environ)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return 0, ErrUnavailable(ReasonNotFound, "syncthing is not on PATH; install it to sync folders")
		}
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	info, _ := procutil.Lookup(pid)
	_ = os.Remove(e.path("daemon.pid")) // stale: Daemon() found it dead, under the lock
	if err := adapterkit.WritePidfile(e.path("daemon.pid"), []byte(strconv.Itoa(pid)+" "+info.StartToken+"\n")); err != nil {
		e.kill(pid)
		return 0, err
	}
	e.log.Info("syncthing started", slog.Int("daemon_pid", pid), slog.Int("gui_port", port))
	return pid, nil
}

// port is the instance's GUI port, picked once as a free loopback port
// and kept in <home>/port so the API stays where the book-keeping says it
// is. A kept port another program has taken since is replaced by a new
// pick, because a daemon that cannot bind its API never becomes ready.
func (e *Engine) port() (int, error) {
	if raw, err := os.ReadFile(e.path("port")); err == nil {
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
	if err := adapterkit.WriteAtomic(e.path("port"), []byte(strconv.Itoa(p)+"\n")); err != nil {
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
func (e *Engine) currentPort() (int, error) {
	raw, err := os.ReadFile(e.path("port"))
	if err != nil {
		return 0, ErrUnavailable(ReasonNotRunning, "the syncthing instance has no API port recorded")
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || p <= 0 || p >= 65536 {
		return 0, ErrUnavailable(ReasonNotRunning, "the syncthing instance's API port record is unreadable")
	}
	return p, nil
}

// API reads the key from config.xml (fresh on every invocation: Syncthing
// owns the file) and the port Brigade kept, and builds the client. It
// does not check that the daemon runs: Running does.
func (e *Engine) API() (*API, error) {
	key, err := ReadAPIKey(e.path("config.xml"))
	if err != nil {
		return nil, err
	}
	port, err := e.currentPort()
	if err != nil {
		return nil, err
	}
	return &API{base: e.d.BaseURL(port), key: key, client: e.http}, nil
}

// Running is the daemon's API when daemon.pid names a live process, else
// the `not_running` refusal: a caller that must not start the daemon —
// that is Attach's, and only Attach counts a session in — goes through it.
func (e *Engine) Running() (*API, error) {
	if _, ok := e.Daemon(); !ok {
		return nil, ErrUnavailable(ReasonNotRunning, "the syncthing instance is not running; attach starts it")
	}
	return e.API()
}

// waitReady waits up to StartWait for config.xml to carry the API key and
// for GET /rest/system/status to answer, and returns the device id
// (myID). A daemon that exits meanwhile fails at once — its reason is in
// syncthing.log, which is Syncthing's own text and not echoed here.
func (e *Engine) waitReady(pid int) (string, error) {
	deadline := time.Now().Add(e.d.StartWait)
	for {
		if !alive(pid, "") {
			return "", ErrUnavailable("syncthing_exited", "syncthing exited during start; see syncthing.log in its home under the Brigade state directory")
		}
		if api, err := e.API(); err == nil {
			if st, err := api.SystemStatus(); err == nil && st.MyID != "" {
				return st.MyID, nil
			}
		}
		if time.Now().After(deadline) {
			return "", ErrUnavailable("start_timeout", "syncthing did not answer on its API within "+e.d.StartWait.String())
		}
		time.Sleep(pollEvery)
	}
}

// stop asks the daemon to shut down over the API (measured: it is gone
// within 2 s, plan 3.2), and signals it only when that fails or it
// lingers: SIGTERM, then SIGKILL after StopWait. The signals go to the
// daemon's process GROUP: `syncthing serve` is a monitor process running
// the real Syncthing as its child, start's Setsid makes the monitor the
// group's leader (the group id is its pid), and it is the child that
// holds the sockets — signalling the monitor alone could leave the child
// listening.
func (e *Engine) stop(pid int) {
	if api, err := e.API(); err == nil {
		if err := api.Shutdown(); err == nil && e.waitGone(pid) {
			return
		} else if err != nil {
			e.log.Warn("syncthing shutdown over the API failed; signalling", adapterlog.Err(err))
		}
	}
	if err := signalDaemon(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		e.log.Warn("SIGTERM to syncthing", adapterlog.Err(err), slog.Int("daemon_pid", pid))
	}
	if !e.waitGroupGone(pid) {
		e.kill(pid)
	}
}

// signalDaemon sends sig to the daemon's process group and to the daemon
// itself (a daemon that is not a group leader — one a test planted — has
// no group of its id). It answers nil when either reached a process.
func signalDaemon(pid int, sig syscall.Signal) error {
	gerr := syscall.Kill(-pid, sig)
	var perr error = syscall.ESRCH
	if alive(pid, "") {
		perr = syscall.Kill(pid, sig)
	}
	if gerr == nil || perr == nil {
		return nil
	}
	return perr
}

// groupAlive reports whether a process group of id pid still has a member
// this user can signal: the daemon's children outlive it for a moment.
func groupAlive(pid int) bool {
	return syscall.Kill(-pid, 0) == nil
}

// kill SIGKILLs the daemon's process group, and pid itself when it is
// still this user's live process.
func (e *Engine) kill(pid int) {
	_ = signalDaemon(pid, syscall.SIGKILL)
}

// waitGroupGone polls for pid and its process group to be gone for up to
// StopWait.
func (e *Engine) waitGroupGone(pid int) bool {
	deadline := time.Now().Add(e.d.StopWait)
	for alive(pid, "") || groupAlive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollEvery)
	}
	return true
}

// waitGone polls for pid to die for up to StopWait.
func (e *Engine) waitGone(pid int) bool {
	deadline := time.Now().Add(e.d.StopWait)
	for alive(pid, "") {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollEvery)
	}
	return true
}

// Alive reports whether pid is a live process of this user (a zombie is
// dead) whose start token is token when one is given. Exported for the
// adapters' tests, which plant and reap daemons.
func Alive(pid int, token string) bool { return alive(pid, token) }
