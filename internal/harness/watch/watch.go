// Package watch is the detached inbound watcher of plan 6.6 and 6.8 — the
// process `brigade hook session-start` spawns with Setsid and the
// hook-built environment, and which then supervises the adapter's
// `message watch` child, runs every `message` event through the shared
// receive-side pipeline (internal/harness/inbound), injects each frame
// into the Claude Code session's inbox socket (internal/harness/socketpost)
// or, in the test harness's sink mode, appends it to a file, acknowledges
// only what was injected, heartbeats the Brigade session, and exits when
// the Claude Code process, the by-pid map or a SIGTERM says the session is
// over. Under the `hold` policy (P5-9) nothing is injected or acknowledged:
// the pipeline records each message in the session's pending file, and the
// watcher applies the release file `brigade inbox release` writes in a
// terminal — on its 2 s liveness tick and once when the watch child is
// ready — injecting exactly the released ids through the accept path.
//
// Configuration comes ONLY from the environment the hook built
// (config.FromWatcherEnv: BRIGADE_CLAUDE_PID, BRIGADE_PROFILE,
// BRIGADE_CONFIG_DIR, BRIGADE_STATE_DIR, BRIGADE_ADAPTER_COMMAND as the
// resolved JSON array, BRIGADE_TEAM_INBOUND) plus CLAUDE_CODE_MESSAGING_SOCKET,
// CLAUDE_CODE_MESSAGING_TOKEN and CLAUDE_CONFIG_DIR, and from the by-pid map
// the hook wrote, which is the trust boundary (E0-7): the Brigade session
// id, the team, the name, the inbound policy and the profile are read from
// it, and a map that names another team key than the environment is `config`.
// The messaging token is held in memory only, goes on the socket auth line
// and nowhere else, and is registered with the log redactor.
//
// The watcher writes nothing to its stdout: its stdout and stderr are the
// log file the hook opened, and its own diagnostics go through adapterkit/log
// into ${stateDir}/logs/watcher-<claude_pid>.log, rotated once at LogRotateBytes.
//
// Every side effect that a lifecycle test needs to control is in [Deps]:
// process facts, the signal a superseded watcher receives, the clock, the
// registry, the socket poster, the request/response spawn seam, the
// intervals and schedules, the log rotation threshold, and a stop channel
// that ends the watcher exactly as SIGTERM would (so an in-process test never
// signals its own test binary). [RealDeps] is what internal/app passes.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/backoff"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/registry"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/socketpost"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

// The two Claude Code variables the watcher reads beside config's own
// (6.6). They are inherited from the hook's from-scratch environment, never
// from a session, and the token never leaves this process except on the
// socket auth line.
const (
	SocketVar = "CLAUDE_CODE_MESSAGING_SOCKET"
	TokenVar  = "CLAUDE_CODE_MESSAGING_TOKEN" //nolint:gosec // G101: the NAME of the variable, not a credential
	// LogLevelVar is the hook's BRIGADE_LOG_LEVEL for the watcher's own log.
	LogLevelVar = "BRIGADE_LOG_LEVEL"
	// SinkFlag is the one argv flag: `brigade watch --sink <absolute file>`.
	SinkFlag = "sink"
	// StdinCommandsCapability is the describe capability that selects the
	// stdin command path over the one-shot calls (4.7).
	StdinCommandsCapability = "message.watch.stdin_commands"
)

// The production values of the [Deps] knobs (6.6, 6.8).
const (
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultPollInterval      = 2 * time.Second
	DefaultReadyTimeout      = 10 * time.Second
	// DefaultFatalExitGrace is how long a child may outlive an `error`
	// event it flagged retryable: false before it is stopped (C-37: 10 s).
	DefaultFatalExitGrace = 10 * time.Second
	// DefaultReplaceWait is how long the guard waits for a superseded
	// watcher to remove its pidfile after the SIGTERM.
	DefaultReplaceWait = 2 * time.Second
	// DefaultCloseWaitClean and DefaultCloseWaitDeath bound the session
	// close on the exit path: 1 s after a clean stop, 3 s when the Claude
	// process died (6.6, 3.8).
	DefaultCloseWaitClean = 1 * time.Second
	DefaultCloseWaitDeath = 3 * time.Second
	// DefaultGiveUpFailures inside DefaultGiveUpWindow ends the watcher with
	// exit 3; the next prompt hook respawns it (6.6).
	DefaultGiveUpFailures = 10
	DefaultGiveUpWindow   = 5 * time.Minute
	// DefaultHealthyAfter is how long a watch child must have been ready
	// before its end counts as a fresh failure sequence rather than one
	// more consecutive failure.
	DefaultHealthyAfter = time.Minute
	// DefaultLogRotateBytes is the log rotation threshold (6.6: 5 MB).
	DefaultLogRotateBytes = 5 * 1000 * 1000
	// DefaultLeaseSeconds is the lease the heartbeat asks for when the
	// adapter's advertised range allows it (6.6: 90 s = three missed beats).
	DefaultLeaseSeconds = protocol.LeaseDefaultSeconds
	// ExitGaveUp is the exit status after DefaultGiveUpFailures.
	ExitGaveUp = 3
)

// injectorStopWait bounds the wait for a post in flight on the exit path
// (a post is bounded by its own dial and write deadlines; this is only a
// hang catcher).
const injectorStopWait = 15 * time.Second

// PostFunc has socketpost.Post's shape.
type PostFunc func(ctx context.Context, target socketpost.Target, content string, opts socketpost.Options) error

// Deps are the injectable side effects of the watcher. The zero value of
// every member means its production default; RealDeps spells them out.
type Deps struct {
	// Lookup answers process facts (procutil.Lookup).
	Lookup pidfile.LookupFunc
	// Signal delivers sig to pid (syscall.Kill): the guard's SIGTERM to a
	// live watcher holding different values. A test injects a recorder so
	// it never signals its own process.
	Signal func(pid int, sig syscall.Signal) error
	// Clock is the time source (time.Now) for the pipeline, the give-up
	// window and the sink records.
	Clock func() time.Time
	// Registry opens Claude Code's session registry (registry.Dir).
	Registry func(claudeConfigDir string) fs.FS
	// Post writes one frame to the inbox socket (socketpost.Post) with
	// PostOptions.
	Post        PostFunc
	PostOptions socketpost.Options
	// Spawn is the request/response spawn seam of adapterclient.Client for
	// the one-shot ack/heartbeat/close calls; nil is the real spawn.
	Spawn adapterclient.SpawnFunc
	// Signals are the process signals that end the watcher (SIGTERM,
	// SIGINT). nil registers none, which an in-process test wants.
	Signals []os.Signal
	// Stop ends the watcher exactly as a signal would; nil in production.
	Stop <-chan struct{}

	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	ReadyTimeout      time.Duration
	FatalExitGrace    time.Duration
	ReplaceWait       time.Duration
	CloseWaitClean    time.Duration
	CloseWaitDeath    time.Duration
	// RestartSchedule builds the watch-child restart schedule
	// (backoff.WatchRestart); InjectSchedule the socket failure schedule
	// (backoff.AdapterError).
	RestartSchedule func() *backoff.Schedule
	InjectSchedule  func() *backoff.Schedule
	GiveUpFailures  int
	GiveUpWindow    time.Duration
	HealthyAfter    time.Duration
	LogRotateBytes  int64
}

// RealDeps are the production dependencies.
func RealDeps() Deps {
	return Deps{
		Lookup:            procutil.Lookup,
		Signal:            syscall.Kill,
		Clock:             time.Now,
		Registry:          registry.Dir,
		Post:              socketpost.Post,
		Signals:           []os.Signal{syscall.SIGTERM, syscall.SIGINT},
		HeartbeatInterval: DefaultHeartbeatInterval,
		PollInterval:      DefaultPollInterval,
		ReadyTimeout:      DefaultReadyTimeout,
		FatalExitGrace:    DefaultFatalExitGrace,
		ReplaceWait:       DefaultReplaceWait,
		CloseWaitClean:    DefaultCloseWaitClean,
		CloseWaitDeath:    DefaultCloseWaitDeath,
		RestartSchedule:   func() *backoff.Schedule { return backoff.WatchRestart(nil) },
		InjectSchedule:    func() *backoff.Schedule { return backoff.AdapterError(nil) },
		GiveUpFailures:    DefaultGiveUpFailures,
		GiveUpWindow:      DefaultGiveUpWindow,
		HealthyAfter:      DefaultHealthyAfter,
		LogRotateBytes:    DefaultLogRotateBytes,
	}
}

// withDefaults fills every zero member with its production value, so a
// test may override one knob and inherit the rest.
func (d Deps) withDefaults() Deps {
	prod := RealDeps()
	if d.Lookup == nil {
		d.Lookup = prod.Lookup
	}
	if d.Signal == nil {
		d.Signal = prod.Signal
	}
	if d.Clock == nil {
		d.Clock = prod.Clock
	}
	if d.Registry == nil {
		d.Registry = prod.Registry
	}
	if d.Post == nil {
		d.Post = prod.Post
	}
	if d.HeartbeatInterval <= 0 {
		d.HeartbeatInterval = prod.HeartbeatInterval
	}
	if d.PollInterval <= 0 {
		d.PollInterval = prod.PollInterval
	}
	if d.ReadyTimeout <= 0 {
		d.ReadyTimeout = prod.ReadyTimeout
	}
	if d.FatalExitGrace <= 0 {
		d.FatalExitGrace = prod.FatalExitGrace
	}
	if d.ReplaceWait <= 0 {
		d.ReplaceWait = prod.ReplaceWait
	}
	if d.CloseWaitClean <= 0 {
		d.CloseWaitClean = prod.CloseWaitClean
	}
	if d.CloseWaitDeath <= 0 {
		d.CloseWaitDeath = prod.CloseWaitDeath
	}
	if d.RestartSchedule == nil {
		d.RestartSchedule = prod.RestartSchedule
	}
	if d.InjectSchedule == nil {
		d.InjectSchedule = prod.InjectSchedule
	}
	if d.GiveUpFailures <= 0 {
		d.GiveUpFailures = prod.GiveUpFailures
	}
	if d.GiveUpWindow <= 0 {
		d.GiveUpWindow = prod.GiveUpWindow
	}
	if d.HealthyAfter <= 0 {
		d.HealthyAfter = prod.HealthyAfter
	}
	if d.LogRotateBytes <= 0 {
		d.LogRotateBytes = prod.LogRotateBytes
	}
	return d
}

// args are the parsed argv of `brigade watch`.
type args struct {
	sink     string
	logLevel string
}

// parseArgs accepts exactly `[--sink <path>] [--log-level <level>]`, in
// either `--flag value` or `--flag=value` form. Anything else is `usage`.
func parseArgs(argv []string) (args, error) {
	var a args
	for i := 0; i < len(argv); i++ {
		name, value, hasValue := strings.Cut(strings.TrimLeft(argv[i], "-"), "=")
		if !strings.HasPrefix(argv[i], "-") || name == "" {
			return a, errors.New("unexpected argument; usage: brigade watch [--sink <absolute file>]")
		}
		if !hasValue {
			if i+1 >= len(argv) {
				return a, errors.New("flag --" + name + " needs a value")
			}
			i++
			value = argv[i]
		}
		switch name {
		case SinkFlag:
			if !filepath.IsAbs(value) {
				return a, errors.New("--sink must be an absolute path")
			}
			a.sink = value
		case "log-level":
			if _, err := adlog.ParseLevel(value); err != nil {
				return a, errors.New("--log-level must be one of error, warn, info, debug")
			}
			a.logLevel = value
		default:
			return a, errors.New("unknown flag; usage: brigade watch [--sink <absolute file>]")
		}
	}
	return a, nil
}

// runConfig is everything Run resolved before the supervisor starts.
type runConfig struct {
	env             config.WatcherEnv
	sink            string
	socketPath      string
	token           string
	claudeConfigDir string
	level           slog.Level
}

// loadConfig resolves the configuration from argv and the hook-built
// environment (6.6). A `--sink` with the socket variable set is `usage`:
// a real session must never be diverted to a file. Without a socket and
// without a sink there is nothing to inject into: `config`.
func loadConfig(a args, environ []string) (runConfig, error) {
	var rc runConfig
	env, err := config.FromWatcherEnv(environ)
	if err != nil {
		return rc, err
	}
	rc.env = env
	rc.socketPath = adapterkit.Getenv(environ, SocketVar)
	rc.token = adapterkit.Getenv(environ, TokenVar)
	rc.sink = a.sink
	if rc.sink != "" && rc.socketPath != "" {
		return rc, &protocol.Error{
			Code:    protocol.CodeUsage,
			Message: "--sink is refused while " + SocketVar + " is set: a live session is never diverted to a file",
			Details: map[string]string{"reason": "sink_with_socket"},
		}
	}
	if rc.sink == "" && rc.socketPath == "" {
		return rc, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "nothing to inject into: neither " + SocketVar + " nor --sink is set",
			Details: map[string]string{"reason": "no_socket"},
		}
	}
	if rc.socketPath != "" && !filepath.IsAbs(rc.socketPath) {
		return rc, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: SocketVar + " must be an absolute path",
			Details: map[string]string{"reason": "socket_relative"},
		}
	}
	dir, err := config.ClaudeConfigDir(environ)
	if err != nil {
		return rc, err
	}
	rc.claudeConfigDir = dir
	levelText := a.logLevel
	if levelText == "" {
		levelText = adapterkit.Getenv(environ, LogLevelVar)
	}
	rc.level = slog.LevelInfo
	if levelText != "" {
		lvl, perr := adlog.ParseLevel(levelText)
		if perr != nil {
			return rc, &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: LogLevelVar + " must be one of error, warn, info, debug",
				Details: map[string]string{"reason": "invalid_log_level"},
			}
		}
		rc.level = lvl
	}
	return rc, nil
}

// Run is `brigade watch [--sink <file>]`: the detached watcher's whole
// life, from the hook-built environment to the removed pidfile. It returns
// the process exit status: 0 after a clean end (the session is over, or
// another watcher already serves it), 2 for a usage refusal, 11 for a
// configuration it cannot honour, the 4.6 status of a non-retryable watch
// failure (4, 5, 10, 11, …) or ExitGaveUp after too many restarts. Nothing
// is ever written to streams.Out; early refusals go to streams.Err in the
// one-line `brigade watch failed (<code>): <message>` form, everything
// after that to the log file.
func Run(argv []string, streams cli.Streams, environ []string, deps Deps) int {
	d := deps.withDefaults()
	a, err := parseArgs(argv)
	if err != nil {
		return reportEarly(streams.Err, &protocol.Error{Code: protocol.CodeUsage, Message: err.Error()})
	}
	rc, err := loadConfig(a, environ)
	if err != nil {
		return reportEarly(streams.Err, err)
	}

	logFile := newRotatingFile(logPath(rc.env.StateDir, rc.env.ClaudePID), d.LogRotateBytes)
	defer func() { _ = logFile.Close() }()
	var sink io.Writer = logFile
	if oerr := logFile.open(); oerr != nil {
		// A read-only state dir: the hook's stderr (the same log file when
		// it could be opened, else wherever the hook pointed it) is the
		// fallback rather than silence.
		sink = streams.Err
	}
	redactor := adlog.NewRedactor(rc.token)
	lg := adlog.New(sink, rc.level, redactor)

	w, code, err := newWatcher(rc, environ, d, lg)
	if err != nil {
		lg.Error("watcher cannot start", slog.String("code", string(codeOf(err))), adlog.Err(err))
		return code
	}
	return w.run()
}

// reportEarly writes the one-line failure of a refusal that happened before
// the log existed and returns the code's exit status.
func reportEarly(errw io.Writer, err error) int {
	code := codeOf(err)
	msg := "internal error"
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		msg = perr.Message
	}
	_, _ = fmt.Fprintf(errw, "%s watch failed (%s): %s\n", cli.Program, code, msg)
	return code.Exit()
}

// codeOf is the 4.6 code of err: a *protocol.Error's own, else internal.
func codeOf(err error) protocol.Code {
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		return perr.Code
	}
	return protocol.CodeInternal
}

// logPath is ${stateDir}/logs/watcher-<claude_pid>.log (3.2).
func logPath(stateDir string, claudePID int) string {
	return filepath.Join(stateDir, "logs", fmt.Sprintf("watcher-%d.log", claudePID))
}

// noticePath is ${stateDir}/state/<claude_pid>.notice (3.2).
func noticePath(stateDir string, claudePID int) string {
	return filepath.Join(stateDir, "state", fmt.Sprintf("%d.notice", claudePID))
}

// A watcher is one running `brigade watch`.
type watcher struct {
	deps    Deps
	rc      runConfig
	environ []string
	log     *slog.Logger
	client  *adapterclient.Client
	store   sessionmap.Store

	sessionID   string
	teamRef     string
	claudeStart string // the Claude pid's start token at startup (pid reuse)
	pipeline    *inbound.Pipeline
	pidPath     string
	releasePath string // the hold policy's release file (inbound.ReleasePath)
	entry       pidfile.Entry
	// lastReleaseIssue is the last reason the release file was left in
	// place, so a file that is refused on every tick is logged once.
	lastReleaseIssue string

	state    *shared
	injector *injector

	// ctx ends with a signal, the Stop channel or a liveness verdict;
	// cancel is what every exit path calls first.
	ctx    context.Context
	cancel context.CancelFunc
	// stopReason names why ctx was cancelled ("signal", "stop",
	// "claude_gone", "map_gone", …); "" while running.
	stopReason string
}

// newWatcher reads the by-pid map (the trust boundary) and builds the
// client, the pipeline and the shared state. A failure is a `config`
// exit 11 unless the error says otherwise.
func newWatcher(rc runConfig, environ []string, d Deps, lg *slog.Logger) (*watcher, int, error) {
	store := sessionmap.Store{StateDir: rc.env.StateDir}
	m, err := store.ReadByPID(rc.env.ClaudePID)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, protocol.CodeConfig.Exit(), &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "no by-pid map for this session; the hook did not run",
			Details: map[string]string{"reason": config.ReasonNotRegistered},
		}
	case err != nil:
		return nil, codeOf(err).Exit(), err
	}
	if m.TeamKey != rc.env.Profile {
		return nil, protocol.CodeConfig.Exit(), &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the by-pid map names a different profile than the watcher's environment",
			Details: map[string]string{"reason": "map_team_key_mismatch"},
		}
	}
	pol := policy.Policy(m.Inbound)
	if !pol.Valid() {
		pol = policy.Refuse
	}
	// The frame instruction comes from the MAP, as the policy does (P5-12):
	// the hook resolved it once at SessionStart; an invalid one is a
	// `config` exit from inbound.New, never a silent fallback.
	pipe, err := inbound.New(inbound.Config{
		Policy:      pol,
		Instruction: m.Instruction(),
		SessionID:   m.BrigadeSessionID,
		TeamName:    m.TeamName,
		Wrap:        true,
		Clock:       inbound.ClockFunc(d.Clock),
		Seen:        inbound.FileSeenStore{Path: inbound.SeenPath(rc.env.StateDir, m.BrigadeSessionID)},
		Pending: inbound.FilePendingStore{
			Path: inbound.PendingPath(rc.env.StateDir, m.BrigadeSessionID), SessionID: m.BrigadeSessionID,
		},
		Logger: lg,
	})
	if err != nil {
		return nil, protocol.CodeConfig.Exit(), err
	}
	levelText := "info"
	if rc.level == slog.LevelDebug {
		levelText = "debug"
	}
	client := &adapterclient.Client{
		Adapter:   rc.env.Adapter,
		Profile:   rc.env.Profile,
		ConfigDir: rc.env.ConfigDir,
		StateDir:  rc.env.StateDir,
		LogLevel:  levelText,
		Environ:   environ,
		Logger:    lg,
		Spawn:     d.Spawn,
	}
	w := &watcher{
		deps:        d,
		rc:          rc,
		environ:     environ,
		log:         lg,
		client:      client,
		store:       store,
		sessionID:   m.BrigadeSessionID,
		teamRef:     m.TeamRef,
		pipeline:    pipe,
		pidPath:     pidfile.Path(rc.env.StateDir, rc.env.ClaudePID),
		releasePath: inbound.ReleasePath(rc.env.StateDir, m.BrigadeSessionID),
		state: newShared(socketpost.Target{Path: rc.socketPath, Token: rc.token},
			m.SessionName, m.Inbound),
	}
	return w, 0, nil
}

// run is the watcher's life after configuration: the single-instance
// guard, the supervisor, the pidfile removal.
func (w *watcher) run() int {
	base, cancel := context.WithCancel(context.Background())
	w.ctx, w.cancel = base, cancel
	if len(w.deps.Signals) > 0 {
		sctx, stop := signal.NotifyContext(base, w.deps.Signals...)
		defer stop()
		w.ctx = sctx
	}
	if w.deps.Stop != nil {
		go func() {
			select {
			case <-w.deps.Stop:
				w.stop("stop")
			case <-w.ctx.Done():
			}
		}()
	}
	defer cancel()

	mode := "socket"
	if w.rc.sink != "" {
		mode = "sink"
	}
	w.log.Info("watcher starting",
		slog.Int("claude_pid", w.rc.env.ClaudePID),
		slog.String("session_id", w.sessionID),
		slog.String("profile", w.rc.env.Profile),
		slog.String("inbound", w.state.snapshot().inbound),
		slog.String("mode", mode),
		slog.Int("pid", os.Getpid()),
	)

	if info, err := w.deps.Lookup(w.rc.env.ClaudePID); err == nil {
		w.claudeStart = info.StartToken
	}

	proceed, code := w.acquire()
	if !proceed {
		return code
	}
	defer w.release()

	w.injector = newInjector(w)
	ictx, icancel := context.WithCancel(base)
	defer icancel()
	go w.injector.loop(ictx)

	code = w.supervise()
	icancel()
	select {
	case <-w.injector.done:
	case <-time.After(injectorStopWait):
		w.log.Warn("injector did not stop in time")
	}
	attrs := append([]slog.Attr{slog.Int("exit", code), slog.String("reason", w.reasonOfStop())}, w.stateForLog()...)
	w.log.LogAttrs(base, slog.LevelInfo, "watcher exiting", attrs...)
	return code
}

// stop records why the watcher is ending and cancels its context; the
// first reason wins.
func (w *watcher) stop(reason string) {
	w.state.mu.Lock()
	if w.stopReason == "" {
		w.stopReason = reason
	}
	w.state.mu.Unlock()
	w.cancel()
}

// stopping reports whether the watcher's context has ended.
func (w *watcher) stopping() bool {
	select {
	case <-w.ctx.Done():
		return true
	default:
		return false
	}
}

// reasonOfStop is the recorded stop reason, "signal" when the context
// ended without one (a signal cancels the context directly).
func (w *watcher) reasonOfStop() string {
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	if w.stopReason == "" && w.ctx.Err() != nil {
		w.stopReason = "signal"
	}
	return w.stopReason
}

// applyRelease is the hold policy's release path (3.6, P5-9), run on the
// liveness tick and once when the watch child is ready: read the release
// file `brigade inbox release` wrote, stamp and persist the released ids
// in the pending file, and only THEN delete the release file by content
// (inbound.ConsumeRelease) — a batch written meanwhile leaves different
// bytes and is applied on the next tick; a crash between the two steps
// re-applies the same stamps, which is idempotent. A file naming another
// session, or one the strict reader refuses, is left in place and logged
// once. Whatever is stamped and deliverable is then queued and the
// injector woken, so a released message whose envelope arrived after the
// stamp (a restart's redelivery) goes out within one tick.
func (w *watcher) applyRelease() {
	rel, raw, err := inbound.ReadRelease(w.releasePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		w.lastReleaseIssue = ""
	case err != nil:
		w.releaseIssue("release file refused; left in place", err)
	case rel.SessionID != w.sessionID:
		w.releaseIssue("release file names another session; left in place", inbound.ErrReleaseForeignSession)
	default:
		w.lastReleaseIssue = ""
		res := w.pipeline.Release(rel.MessageIDs)
		w.log.Info("release file applied",
			slog.Int("ids", len(rel.MessageIDs)), slog.Int("stamped", len(res.Stamped)),
			slog.Int("queued", len(res.Queued)), slog.Int("waiting", res.Waiting), slog.Int("unknown", len(res.Unknown)))
		if removed, cerr := inbound.ConsumeRelease(w.releasePath, raw); cerr != nil {
			w.log.Warn("release file not removed", adlog.Err(cerr))
		} else if !removed {
			w.log.Info("release file was rewritten while it was applied; the next tick applies the rest")
		}
		if len(res.Queued) > 0 {
			w.injector.kickNow()
		}
	}
	if res := w.pipeline.Release(nil); len(res.Queued) > 0 {
		w.log.Info("released messages queued", slog.Int("queued", len(res.Queued)), slog.Int("waiting", res.Waiting))
		w.injector.kickNow()
	}
}

// releaseIssue logs why the release file was left in place, once per
// distinct reason rather than on every tick.
func (w *watcher) releaseIssue(msg string, err error) {
	key := msg + ": " + err.Error()
	if w.lastReleaseIssue == key {
		return
	}
	w.lastReleaseIssue = key
	w.log.Warn(msg, adlog.Err(err))
}

// writeNotice writes ONE line to ${stateDir}/state/<pid>.notice, overwriting
// (3.2: the next prompt hook prints it once). Best effort, logged.
func (w *watcher) writeNotice(line string) {
	path := noticePath(w.rc.env.StateDir, w.rc.env.ClaudePID)
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		w.log.Warn("notice not written", adlog.Err(err))
		return
	}
	if err := adapterkit.WriteAtomic(path, []byte(line+"\n")); err != nil {
		w.log.Warn("notice not written", adlog.Err(err))
		return
	}
	w.log.Info("notice written", slog.String("notice", line))
}
