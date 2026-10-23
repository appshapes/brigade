// Package hook implements the three Claude Code lifecycle hooks of plan 6.3
// — `brigade hook session-start`, `brigade hook prompt` and `brigade hook
// session-end` — the processes Claude Code runs from plugin/hooks/hooks.json
// with a JSON document on stdin and the session's environment (6.5).
//
// The hooks are the only code that reads the CLAUDE_PLUGIN_OPTION_* values,
// resolves the profile and adapter (D36), registers the Brigade session and
// writes the by-pid and by-native maps every other command reads (3.2), and
// they are the only legitimate producer of the detached watcher's
// environment (6.6): the messaging token reaches the watcher ONLY through
// the environment the hook builds, is never written to a file (its SHA-256 in
// the pidfile is the one derived form), never put on argv, never logged and
// never handed to an adapter child.
//
// Every subcommand exits 0 on every failure, with one stderr diagnostic
// through the redacting logger and, where useful, a context line on stdout:
// a non-zero exit from the prompt hook would block and erase the user's
// prompt, and SessionStart/SessionEnd cannot block at all (6.3). Nothing a
// hook prints carries a remote string unsanitised: team and session names go
// through protocol.SanitizeAttribute (quotes, angle brackets and newlines
// dropped, 64 code points), because hook stdout is attached to the user's own
// turn with no harness preamble.
//
// Inside a session every inherited BRIGADE_* variable is ignored (U-27): the
// configuration comes from the options, the by-pid map and the CLAUDE_* facts
// alone, all read through internal/harness/config and adapterkit.Getenv.
//
// Every side effect is injectable through Deps — the clock, the watcher
// Spawner, the registry fs.FS, the settings reader, the adapter spawn seam,
// the process-facts lookup and the PATH search — so the tests run hermetically
// against the fake adapter, a fake registry, a temp XDG triple and a reaped
// sleeper as CLAUDE_PID.
package hook

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/notice"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/polling"
	"github.com/appshapes/brigade/internal/harness/registry"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/watch"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

// Hook subcommands (6.3, hooks.json), including Grok Stop.
const (
	SubStop         = "stop"
	SubSessionStart = "session-start"
	SubPrompt       = "prompt"
	SubSessionEnd   = "session-end"
)

// The hook's budgets. The per-call adapter budgets are adapterclient's 4.1
// constants; these bound the whole hook run under the timeouts hooks.json
// declares (60 s, 5 s, 5 s — the SessionEnd budget is really 1.5 s shared
// across every SessionEnd hook, E0-5 (h)).
const (
	// startBudget is a hang catcher under the 60 s SessionStart timeout.
	startBudget = 55 * time.Second
	// promptBudget keeps the prompt hook under its 5 s timeout with room
	// for the process to exit.
	promptBudget = 4500 * time.Millisecond
	// endBudget keeps the session-end hook under the 1.5 s shared budget.
	endBudget = 1400 * time.Millisecond
	// receiveTimeout is the `message receive --limit 20` cap of 6.3.
	receiveTimeout = 4 * time.Second
	// watcherStopWait is how long the hook waits, after SIGTERM, for a
	// watcher's pidfile to be retired before it respawns (6.3, D9).
	watcherStopWait = 2 * time.Second
	// DefaultPidfileWait is how long the hook waits for a freshly spawned
	// watcher to write its pidfile (6.6) before it logs and moves on; the
	// next prompt respawns a watcher that never appeared.
	DefaultPidfileWait = 2 * time.Second
	// promptPidfileWait caps that wait inside the prompt hook's budget.
	promptPidfileWait = 500 * time.Millisecond
	// registerRetryInterval bounds how often a prompt hook retries a
	// registration the SessionStart hook could not complete.
	registerRetryInterval = time.Minute
	// doingNudgeInterval is the floor between two doing-line reminders in
	// one conversation (card 25, plan 5.4): a fixed ten minutes of prompted
	// time, no backoff — "unanswered" is the right outcome when the work
	// has not changed, and a doubling cooldown would skip the pivot the
	// line exists to catch.
	doingNudgeInterval = 10 * time.Minute
	// doingNudgeMinBudget is what must remain of promptBudget before a
	// reminder is stamped and printed: a hook killed between the stamp
	// write and its exit would burn ten silent minutes (the P5-18 shape).
	doingNudgeMinBudget = time.Second
	// doingDescribeTimeout caps the one local describe the prompt hook
	// spends resolving a doing mode a pre-feature map lacks (plan 5.2),
	// well under adapterclient.DescribeTimeout so the reminder and the
	// poll keep their share of promptBudget.
	doingDescribeTimeout = time.Second
	// OutputCap is the 10,000-character hook-output cap of 6.3: frames
	// are printed until it would be exceeded, and only printed frames are
	// acknowledged.
	OutputCap = 10000
	// maxStdinBytes bounds the hook's stdin document (a real one is well
	// under 4 KiB).
	maxStdinBytes = 1 << 20
	// The `harness` member of a registration is the session's host
	// (config.HostClaude or config.HostMuse, card 88): facts.host.
	// museByteCap is the hook's stdout cap on Muse Code, in BYTES (card
	// 88): Muse kills a hook's whole process tree over 16,384 bytes of
	// stdout and parses nothing, so the cap stays well under it. Claude
	// Code's cap is OutputCap in code points; both apply on Muse.
	museByteCap = 15 * 1024
	// unknownVersion is harness_version when the registry has none.
	unknownVersion = "unknown"
	// pollLimit is the `--limit` of the prompt-hook poll (6.3).
	pollLimit = 20
)

// The hook stdin members this package reads (6.3, 6.5; the 2.1.251
// evidence under docs/research/claude-plugin-mcp-evidence). permission_mode
// and session_title are optional everywhere. transcript_path — the
// documented common hook member naming the session's own NDJSON
// transcript — IS read: the hook keeps it, when absolute, in the 0600
// by-pid map (transcriptPath), where the detached watcher finds it and
// reads the file locally for the two facts a heartbeat carries, the model
// and the context occupancy (internal/harness/transcript). The path itself
// is never sent, never logged and never printed, and the hook never opens
// the file (T10); `prompt` stays absent from the type — never read,
// stored or sent.
type input struct {
	WorkspaceRoot   string     `json:"workspaceRoot"`
	SubagentType    string     `json:"subagentType"`
	StopReason      string     `json:"stopReason"`
	StopHookActive  bool       `json:"stopHookActive"`
	BackgroundTasks []grokTask `json:"backgroundTasks"`
	grokIncarnation string
	// launchCwd is the hook document's cwd before symlink resolution. Grok
	// names its session directory from that spelling. It is not a JSON
	// field: a document cannot set it.
	launchCwd string

	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	PermissionMode string `json:"permission_mode"`
	SessionTitle   string `json:"session_title"`
	TranscriptPath string `json:"transcript_path"`
	// Model is the model the host names in every hook document (Muse Code
	// does, card 88; Claude Code's documents carry none): the map keeps it
	// for the heartbeat's `model` member.
	Model string `json:"model"`
}

// transcriptPath is the document's transcript_path when it is absolute,
// else "" — the rule the socket path follows in facts: a relative value is
// not a file the watcher may open, and the map refuses one (Validate).
func transcriptPath(in input) string {
	if filepath.IsAbs(in.TranscriptPath) {
		return in.TranscriptPath
	}
	return ""
}

// The SessionStart `source` and SessionEnd `reason` values the hook keys on.
const (
	sourceCompact = "compact"
	reasonClear   = "clear"
	reasonResume  = "resume"
)

// A Spawner starts the detached watcher (6.6). RealSpawner is the one
// exec.CommandContext of this package (spawn.go); tests inject a recorder.
type Spawner interface {
	// Spawn starts `brigade watch` per spec and returns its pid. It never
	// waits for the child.
	Spawn(ctx context.Context, spec SpawnSpec) (int, error)
}

// Deps are the injectable side effects of a hook run. RealDeps returns the
// production set; a zero field means the production default, so a test
// overrides only what it needs.
type Deps struct {
	// Now is the clock for every timestamp the hook records (map times,
	// the prune stamp, the retry stamp) and for the pipeline's rate
	// window; nil means time.Now. Real waits (for a pidfile) use the
	// system clock regardless.
	Now func() time.Time
	// Spawner starts the watcher; nil means RealSpawner.
	Spawner Spawner
	// Registry is the fs.FS the session registry is read from; nil means
	// registry.Dir(config.ClaudeConfigDir(environ)) per run.
	Registry fs.FS
	// ReadFile reads the three settings files of the native scan (6.10),
	// the candidate settings files of the doing-rules scan (card 25, plan
	// 5.2) and, at SessionStart, the frame_file option's file (P5-12);
	// nil means os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// WriteFile is the ONE write under the Claude Code configuration
	// directory (card 50, policy.EnsureUserAccept); nil means
	// adapterkit.WriteAtomicMode.
	WriteFile func(path string, data []byte, mode os.FileMode) error
	// Spawn is the adapter request/response seam (adapterclient.Client.
	// Spawn); nil means adapterkit.Spawn, a real child.
	Spawn adapterclient.SpawnFunc
	// Lookup reads process facts for the pidfile guard; nil means
	// procutil.Lookup.
	Lookup pidfile.LookupFunc
	// LookPath searches a PATH value for an executable name (the
	// shadowing check of 6.2); nil means the package's own search.
	LookPath func(pathVar, name string) (string, bool)
	// SoundPlayer resolves the fixed argv of the sound player the
	// `message_sound` option would run on this machine, or nil and why
	// not (card 35, notify.Sound), and Notifier the same for the desktop
	// notifier of `message_notification` (card 36, notify.Banner); nil
	// means the package's own probe. The hook never runs either:
	// SessionStart only says when one cannot be run.
	SoundPlayer func(pathVar string) ([]string, string)
	Notifier    func(pathVar string) ([]string, string)
	// BrigadeVersion is this binary's own version as a registration
	// reports it (brigade_version, C-46); nil means buildinfo.Claimed,
	// which is nil itself for a binary with no version to claim.
	BrigadeVersion func() *string
	// SyncPeer is the folder-sync peer descriptor a registration and the
	// start heartbeat report when the hook knows one (sync_peer, C-47);
	// nil means it knows none, and the member is then absent — never
	// cleared. At SessionStart it usually knows none: the watcher's sync
	// adapter attaches after the registration and the watcher's first
	// heartbeat after that carries the peer (plan folder-sync.md 4.2).
	SyncPeer func() *string
	// Sink, when set, is passed to the watcher as `--sink <file>` (6.6).
	// Only a test harness sets it; production never does.
	Sink string
	// PidfileWait bounds the wait for a spawned watcher's pidfile; zero
	// means DefaultPidfileWait.
	PidfileWait time.Duration
	// PromptBudget bounds the whole prompt-hook run; zero means
	// promptBudget. Only a test lowers it, to reach the doing reminder's
	// budget guard (plan 5.4) without a real 3.5 s stall.
	PromptBudget time.Duration
	// Getppid answers this process's parent pid, which on Muse Code is the
	// Muse process the hook serves (card 88; hooks are the host's direct
	// children there); nil means os.Getppid. A test injects a sleeper's
	// pid, as it injects CLAUDE_PID for Claude.
	Getppid func() int
}

// RealDeps returns the production dependencies.
func RealDeps() Deps {
	return Deps{
		Now:          time.Now,
		Spawner:      RealSpawner{},
		ReadFile:     os.ReadFile,
		Lookup:       procutil.Lookup,
		LookPath:     lookPath,
		SoundPlayer:  soundPlayer,
		Notifier:     notifier,
		PidfileWait:  DefaultPidfileWait,
		PromptBudget: promptBudget,
		Getppid:      os.Getppid,
	}
}

// withDefaults fills every nil member with its production value.
func (d Deps) withDefaults() Deps {
	prod := RealDeps()
	if d.BrigadeVersion == nil {
		d.BrigadeVersion = buildinfo.Claimed
	}
	if d.SyncPeer == nil {
		d.SyncPeer = noSyncPeer
	}
	if d.Now == nil {
		d.Now = prod.Now
	}
	if d.Spawner == nil {
		d.Spawner = prod.Spawner
	}
	if d.ReadFile == nil {
		d.ReadFile = prod.ReadFile
	}
	if d.Lookup == nil {
		d.Lookup = prod.Lookup
	}
	if d.LookPath == nil {
		d.LookPath = prod.LookPath
	}
	if d.SoundPlayer == nil {
		d.SoundPlayer = prod.SoundPlayer
	}
	if d.Notifier == nil {
		d.Notifier = prod.Notifier
	}
	if d.PidfileWait <= 0 {
		d.PidfileWait = prod.PidfileWait
	}
	if d.PromptBudget <= 0 {
		d.PromptBudget = prod.PromptBudget
	}
	if d.Getppid == nil {
		d.Getppid = prod.Getppid
	}
	return d
}

// noSyncPeer is Deps.SyncPeer's production value: the hook knows no
// sync peer, so neither the registration nor the start heartbeat names
// one and a stored peer stands (plan folder-sync.md 4.2).
func noSyncPeer() *string { return nil }

// Run executes one `brigade hook <subcommand>` invocation and returns the
// process exit status. args excludes the words `brigade hook`; the flags
// are --log-level, and — for a host whose hooks carry no marker of their
// own (Codex CLI, card 107) — --host <name> and --plugin-root <dir>.
// environ is the process environment in os.Environ form. Every subcommand
// returns 0 on every failure (6.3); the one non-zero answer is `usage` (2)
// for a missing or unknown subcommand or flag, which no hooks.json ever
// produces.
func Run(args []string, streams cli.Streams, environ []string, deps Deps) int {
	a, perr := parseArgs(args)
	if perr != "" {
		cmd, _ := cli.Lookup("hook")
		return cli.Usage(cmd, args, streams, perr)
	}
	r := newRun(streams, environ, deps.withDefaults(), a)
	if a.host == config.HostGrok || config.HostOf(environ) == config.HostGrok {
		return r.runGrok()
	}
	if a.sub == SubStop {
		return 0
	}
	switch a.sub {
	case SubSessionStart:
		return r.sessionStart()
	case SubPrompt:
		return r.prompt()
	default:
		return r.sessionEnd()
	}
}

// hookArgs are the parsed argv of `brigade hook`.
type hookArgs struct {
	sub   string
	level string
	// host is the host named by --host, "" when the environment decides
	// (Claude Code by CLAUDE_PID, Muse Code by MUSE_SESSION_ID). Codex CLI
	// is the one host that needs the flag (card 107): its hooks run with no
	// CODEX_* variable, so nothing in the environment says what they are.
	host config.Host
	// pluginRoot is the package root named by --plugin-root, for a host
	// that exports no plugin-root variable to its hooks (Codex CLI). "" when
	// absent or relative: the map then carries no plugin_bin.
	pluginRoot string
}

// parseArgs picks the subcommand and the optional flags. The message is
// fixed text: argv is never echoed (4.5.14).
func parseArgs(args []string) (hookArgs, string) {
	a := hookArgs{level: "info"}
	if len(args) == 0 {
		return a, "hook needs one of session-start, prompt, session-end"
	}
	a.sub = args[0]
	if a.sub != SubSessionStart && a.sub != SubPrompt && a.sub != SubSessionEnd && a.sub != SubStop {
		return a, "unknown hook; the hooks are session-start, prompt and session-end"
	}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		name, value, has := strings.Cut(rest[i], "=")
		if name != "--log-level" && name != "--host" && name != "--plugin-root" {
			return a, "unknown hook flag; the flags are --log-level, --host and --plugin-root"
		}
		if !has {
			if i+1 >= len(rest) {
				return a, name + " needs a value"
			}
			i++
			value = rest[i]
		}
		switch name {
		case "--log-level":
			if _, err := log.ParseLevel(value); err != nil {
				return a, "--log-level must be one of error, warn, info, debug"
			}
			a.level = value
		case "--host":
			if config.Host(value) != config.HostCodex && config.Host(value) != config.HostGrok {
				return a, "--host names a host whose hooks carry no marker of their own; the recognized explicit hosts are codex and grok"
			}
			a.host = config.Host(value)
		case "--plugin-root":
			if filepath.IsAbs(value) {
				a.pluginRoot = filepath.Clean(value)
			}
		}
	}
	return a, ""
}

// A run is one hook invocation.
type run struct {
	grokDeadline time.Time
	deps         Deps
	streams      cli.Streams
	environ      []string
	logLevel     string
	redactor     *log.Redactor
	log          *slog.Logger
	// written counts the code points written to stdout so far, against
	// OutputCap; writtenBytes the bytes, against byteCap (0: no byte cap).
	written      int
	writtenBytes int
	byteCap      int
	// optionsEnv is the environment ParseOptions reads: environ itself on
	// Claude Code; on a host with an options file (Muse Code, Codex CLI)
	// environ plus the entries the file supplies (config.OptionsEnviron),
	// resolved once per run by options(). optionsWarning is the file's one
	// warning, printed once at SessionStart.
	optionsEnv     []string
	optionsWarning string
	// args are the parsed argv: the forced host and the plugin root of a
	// host whose hooks carry neither (Codex CLI, card 107).
	args hookArgs
}

func newRun(streams cli.Streams, environ []string, deps Deps, a hookArgs) *run {
	lvl, _ := log.ParseLevel(a.level)
	redactor := log.NewRedactor()
	// The token is registered before anything is logged, so no line can
	// carry it even by accident (U-25).
	redactor.Add(adapterkit.Getenv(environ, envMessagingToken))
	r := &run{
		deps:     deps,
		streams:  streams,
		environ:  environ,
		logLevel: a.level,
		redactor: redactor,
		log:      log.New(streams.Err, lvl, redactor),
		args:     a,
	}
	if config.HostOf(environ) == config.HostMuse {
		r.byteCap = museByteCap
	}
	return r
}

// adoptHost gives a run whose host came from --host (Codex CLI, card 107)
// the environment the config package keys on: the document's session_id
// is the thread id the model's shell will carry as CODEX_THREAD_ID, so the
// hook appends that very entry to its own environment once the document
// is read, and from then on every config resolver — HostOf, Trusted, the
// state and config directories, the options file — treats the run as the
// Codex session it is. A document whose id is not a safe path component
// is refused: nothing of this session could be keyed by it. Nothing
// happens for a host the environment already names, and CLAUDE_PID wins
// in HostOf regardless, so a Codex flag can never relabel a Claude session.
func (r *run) adoptHost(in input) error {
	if r.args.host != config.HostCodex {
		return nil
	}
	if err := sessionmap.CheckNativeID(in.SessionID); err != nil {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the Codex hook document carries no usable thread id",
			Details: map[string]string{"reason": config.ReasonInvalidNativeID, "variable": config.CodexThreadIDVar},
		}
	}
	r.environ = append(slices.Clone(r.environ), config.CodexThreadIDVar+"="+in.SessionID)
	return nil
}

// options resolves the plugin options for this host (cards 88, 107): on
// Claude Code from CLAUDE_PLUGIN_OPTION_* in environ; on Muse Code and
// Codex CLI from the host's options file as well, read once per run.
func (r *run) options() (config.Options, error) {
	if r.optionsEnv == nil {
		r.optionsEnv = r.environ
		if path, err := config.OptionsPath(r.environ); err == nil && path != "" {
			r.optionsEnv, r.optionsWarning = config.OptionsEnviron(r.environ, path, r.deps.ReadFile)
		}
	}
	return config.ParseOptions(r.optionsEnv)
}

// The Claude Code variables the hook reads (6.5), all through
// adapterkit.Getenv over the environ it was handed.
const (
	envMessagingSocket = "CLAUDE_CODE_MESSAGING_SOCKET"
	envMessagingToken  = "CLAUDE_CODE_MESSAGING_TOKEN" //nolint:gosec // G101: a variable NAME, not a credential
	envPluginRoot      = "CLAUDE_PLUGIN_ROOT"
	envEntrypoint      = "CLAUDE_CODE_ENTRYPOINT"
	entrypointSDK      = "sdk-cli"
	// envProjectDir is the project root Claude Code reads its project
	// settings from; the doing scan reads the same files (card 25).
	envProjectDir = "CLAUDE_PROJECT_DIR"
	// envMusePluginRoot is the Muse Code counterpart of CLAUDE_PLUGIN_ROOT
	// in a plugin hook's environment (card 88).
	envMusePluginRoot = "MUSE_PLUGIN_ROOT"
)

// facts are the session facts every subcommand resolves from the
// environment (6.5; cards 88 and 107 for the host).
type facts struct {
	// host is the session's harness (config.HostClaude, config.HostMuse or
	// config.HostCodex).
	host config.Host
	// pid is what the session's state is keyed by: CLAUDE_PID on Claude
	// Code, the hook's parent on Muse Code (the Muse process), and on Codex
	// CLI a number derived from the thread id (codexKey), because Codex runs
	// its hooks from a daemon every Codex session shares.
	pid int
	// hostProcessPID is the process whose life is the session's when it is
	// not the key (card 107): the Codex hook's parent — the TUI in embedded
	// mode, the shared app-server daemon in daemon mode. 0 elsewhere.
	hostProcessPID  int
	stateDir        string
	claudeConfigDir string // "" when it cannot be resolved, and always on a pointed host (registry and user-settings scan skipped)
	socket          string
	token           string
	pluginBin       string
	// pluginRoot is the package root the hook knows, "" when it does not:
	// ${CLAUDE_PLUGIN_ROOT}, ${MUSE_PLUGIN_ROOT}, or Codex's --plugin-root.
	pluginRoot string
	entrypoint string
	home       string
}

// facts resolves the session facts. The error is `config`: no host marker
// at all (the hook was run outside a session), a pointed host's hook with
// no usable parent, or no state directory (HOME unset).
func (r *run) facts() (facts, error) {
	host := config.HostOf(r.environ)
	var pid, hostProcess int
	switch host {
	case config.HostMuse, config.HostCodex, config.HostGrok:
		nativeID, err := config.NativeID(host, r.environ)
		if err != nil {
			return facts{}, err
		}
		pid = r.deps.Getppid()
		if pid <= 1 {
			return facts{}, &protocol.Error{
				Code:    protocol.CodeConfig,
				Message: "the " + host.Name() + " hook has no usable parent process to key the session by",
				Details: map[string]string{"reason": config.ReasonInvalidClaudePID},
			}
		}
		if host == config.HostCodex {
			// Measured on 0.163.0 with the hooks trusted: the parent is the
			// shared app-server daemon, not the TUI, so the key is the
			// thread's and the parent is kept for liveness only.
			hostProcess, pid = pid, codexKey(nativeID)
		}
	case config.HostClaude, "":
		// A terminal ("") gets ClaudePID's own not_in_session error.
		p, err := config.ClaudePID(r.environ)
		if err != nil {
			return facts{}, err
		}
		host, pid = config.HostClaude, p
	}
	stateDir, err := config.BrigadeStateDir(r.environ)
	if err != nil {
		return facts{}, err
	}
	f := facts{
		host:           host,
		pid:            pid,
		hostProcessPID: hostProcess,
		stateDir:       stateDir,
		socket:         adapterkit.Getenv(r.environ, envMessagingSocket),
		token:          adapterkit.Getenv(r.environ, envMessagingToken),
		entrypoint:     adapterkit.Getenv(r.environ, envEntrypoint),
		home:           adapterkit.Getenv(r.environ, "HOME"),
	}
	if !filepath.IsAbs(f.socket) {
		// The map requires an absolute socket path or none; a relative
		// value is not a socket the watcher may post to.
		f.socket = ""
	}
	root := adapterkit.Getenv(r.environ, envPluginRoot)
	switch host {
	case config.HostMuse, config.HostCodex, config.HostGrok:
		// Neither host has an inbox socket, a session registry or Claude
		// settings files: the socket and the token are never read, and
		// the Claude config dir stays unresolved so that nothing under
		// ~/.claude is scanned or written for such a session. Muse names
		// its plugin root in the hook's environment; Codex names nothing,
		// so the package's hooks.json passes --plugin-root (card 107).
		f.socket, f.token, f.entrypoint = "", "", ""
		root = adapterkit.Getenv(r.environ, envMusePluginRoot)
		if host == config.HostCodex {
			root = r.args.pluginRoot
		}
		if host == config.HostGrok {
			root = r.args.pluginRoot
			if root == "" {
				root = adapterkit.Getenv(r.environ, "GROK_PLUGIN_ROOT")
			}
		}
	case config.HostClaude, "":
		if dir, cerr := config.ClaudeConfigDir(r.environ); cerr == nil {
			f.claudeConfigDir = dir
		} else {
			r.log.Debug("claude config dir unresolved; registry and user settings skipped", log.Err(cerr))
		}
	}
	if root != "" && filepath.IsAbs(root) {
		f.pluginRoot = root
		f.pluginBin = pluginBin(root)
	}
	if host == config.HostGrok {
		if _, err := r.grokProcess(); err != nil {
			return facts{}, err
		}
	}
	return f, nil
}

// codexKey is the by-pid key of a Codex CLI session (card 107): a number
// derived from the thread id, in a range no real pid reaches (above 2^30;
// Linux's pid_max is 2^22), so one thread is one map, two threads on one
// daemon never share a map, and the same thread resumed finds its map
// again. FNV-1a over the id; 29 bits of it.
func codexKey(thread string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(thread))
	return 1<<30 | int(h.Sum32()&(1<<29-1))
}

// muse reports whether the session runs in Muse Code (card 88).
func (f facts) muse() bool { return f.host == config.HostMuse }

// codex reports whether the session runs in Codex CLI (card 107).
func (f facts) codex() bool { return f.host == config.HostCodex }

// pointed reports whether the session runs in a host found through the
// by-host pointer (Muse Code, Codex CLI): keyed by the hook's parent, no
// Claude settings, no registry, no inbox socket, no Claude account.
func (f facts) pointed() bool { return f.host.Pointed() }

// pluginBin is ${CLAUDE_PLUGIN_ROOT}/bin/brigade resolved through symlinks
// best effort (3.2: the map's plugin_bin is the bootstrap's realpath).
func pluginBin(root string) string {
	p := filepath.Join(root, "bin", "brigade")
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// readInput reads and decodes the one stdin document. Its errors carry
// fixed text: the document is attacker-influenced and never echoed.
func (r *run) readInput() (input, error) {
	if r.streams.In == nil {
		return input{}, errors.New("hook stdin is missing")
	}
	data, err := io.ReadAll(io.LimitReader(r.streams.In, maxStdinBytes+1))
	if err != nil {
		return input{}, errors.New("hook stdin could not be read")
	}
	if len(data) > maxStdinBytes {
		return input{}, errors.New("hook stdin exceeds the size cap")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return input{}, errors.New("hook stdin is empty")
	}
	var in input
	if err := json.Unmarshal(data, &in); err != nil {
		return input{}, errors.New("hook stdin is not a valid JSON document")
	}
	if r.isGrok() {
		if err := r.decodeGrok(data, &in); err != nil {
			return input{}, err
		}
	}
	if err := r.adoptHost(in); err != nil {
		return input{}, err
	}
	return in, nil
}

// say writes one context line to stdout and counts it against OutputCap
// (code points) and, where one applies, byteCap (bytes).
func (r *run) say(line string) {
	if r.streams.Out == nil {
		return
	}
	_, _ = io.WriteString(r.streams.Out, line+"\n")
	r.written += utf8.RuneCountInString(line) + 1
	r.writtenBytes += len(line) + 1
}

// fits reports whether line (plus its newline) still fits under OutputCap
// and, on a host with a byte cap, under that too.
func (r *run) fits(line string) bool {
	if r.byteCap > 0 && r.writtenBytes+len(line)+1 > r.byteCap {
		return false
	}
	return r.written+utf8.RuneCountInString(line)+1 <= OutputCap
}

// canInject reports whether a watcher would have somewhere to deliver
// into: an inbox socket (Claude Code), a test sink, the inbox sink of a
// Muse Code session (card 88), or the queue of a Codex CLI session (card
// 107).
func (r *run) canInject(f facts) bool {
	return f.socket != "" || r.deps.Sink != "" || f.pointed()
}

// sinkFor is the watcher's `--sink` file for the session, "" when the
// watcher posts to a socket instead: the test harness's Deps.Sink when set,
// else on Muse Code a fresh per-watcher file under the session's inbox
// directory (inbound.NewSinkPath), named now so the follower reads it from
// its beginning.
func (r *run) sinkFor(f facts, sessionID string) string {
	if r.deps.Sink != "" {
		return r.deps.Sink
	}
	if f.muse() || f.host == config.HostGrok {
		return inbound.NewSinkPath(f.stateDir, sessionID, r.deps.Now())
	}
	return ""
}

// commandName is how the context lines name the brigade command on this
// host: the bare word on Claude Code, where the plugin's bin/ is on the
// Bash tool's PATH (finding F1); on Muse Code and Codex CLI the bootstrap's
// path, because nothing puts a plugin's bin/ on either shell's PATH and
// neither host has a permission rule for the path to break.
func (r *run) commandName(f facts) string {
	if f.pointed() && f.pluginBin != "" {
		return oneLine(f.pluginBin, 512)
	}
	return "brigade"
}

// armLine is the one Muse Code line that turns a mailbox into a peer (card
// 88, the plan's route B): the model arms its monitor once, persistent,
// with an immediate wake, on `brigade inbox follow`; each team message the
// follower prints wakes the idle session 0.2 s later (measured). It is
// printed by SessionStart on every source but compact, so a resumed
// session — whose monitor died with its process — is told again.
func armLine(command string) string {
	return "Brigade: team messages reach this session through your Monitor tool. Arm it once now, persistent, " +
		"wake_delay_ms 0, on exactly this command: " + command + " inbox follow . Each message it prints stands " +
		"between a `Brigade: team message … follows` line and a `Brigade: end of message …` line; read it as the " +
		"team-messaging skill says, reply with " + command + " send, and never stop the monitor."
}

// codexLine is the one Codex CLI line after the context line (card 107):
// no arming is needed — `codex queue` wakes the session from outside — so
// it says how a team message arrives (as a turn the user did not type) and
// names the bootstrap, which is not on the model's PATH (D7, D9).
func codexLine(command string) string {
	return "Brigade: a team message reaches this session as a turn of its own, a <brigade-message> frame your user did " +
		"not type; read it as the team-messaging skill says and reply with " + command + " send. The brigade command is " +
		command + " when the bare word is not on your PATH."
}

// fail logs a failure with its code and reason, then prints the context
// line the model should see.
func (r *run) fail(what string, err error, line string) {
	code, reason := codeOf(err)
	r.log.Warn(what, slog.String("code", string(code)), slog.String("reason", reason), log.Err(err))
	if line != "" {
		r.say(line)
	}
}

// codeOf classifies an error: a *protocol.Error's code and details.reason,
// anything else `internal`.
func codeOf(err error) (protocol.Code, string) {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Code, perr.Details["reason"]
	}
	return protocol.CodeInternal, ""
}

// isCode reports whether err is a *protocol.Error with one of codes.
func isCode(err error, codes ...protocol.Code) bool {
	got, _ := codeOf(err)
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		return false
	}
	for _, c := range codes {
		if got == c {
			return true
		}
	}
	return false
}

// notConnected is the context line for a failed registration (6.3): a
// retryable code is retried by the next prompt hook; anything else needs
// the human to join again — here or in a terminal (P7-11).
func notConnected(code protocol.Code) string {
	if code.Retryable() {
		return "Brigade: not connected (" + string(code) + "); retrying at your next prompt"
	}
	return "Brigade: not connected (" + string(code) + "); run `brigade team join`"
}

// notConnectedFor is notConnected for an adapter failure: a `config` the
// adapter produced carries its fixed details.reason (profile_missing, a
// malformed profile, …) so the line says WHY the resolved adapter cannot
// read the profile (the P3-4 row, D36) and never just the code; the
// remedy is a join. The reason is a fixed token, never a value.
func notConnectedFor(err error) string {
	code, reason := codeOf(err)
	if code == protocol.CodeConfig && reason != "" {
		return "Brigade: not connected (config: " + attr(reason) + "); run `brigade team join`"
	}
	return notConnected(code)
}

// adapterLine is the context line for an adapter name that could not be
// resolved (P7-6): the name comes from the project's team file, the
// resolution is strictly user-side (adapters.json), and the remedy is
// the terminal. The name is a validated dialect name, safe to echo.
func adapterLine(name string, err error) string {
	source := ""
	var perr *protocol.Error
	if errors.As(err, &perr) {
		source = perr.Details["source"]
	}
	if source == "" {
		source = "its configuration"
	}
	return "Brigade: not connected (config): the team's adapter \"" + attr(name) +
		"\" could not be resolved from " + attr(source) + "; run `brigade team status` in a terminal"
}

// notJoinedLine is the one line a valid team file earns before a human
// has consented in this checkout (brief §4): the sanitized team name is
// the ONLY repo-sourced value that ever reaches the model, and only
// here (the other repo-sourced text, the reduced NAMES of ignored
// members, is printed only once a session attaches: teamFileNotes). The
// remedy is runnable as printed: command is `brigade` on Claude Code,
// where the plugin's bin/ is on PATH (P7-11), and the bootstrap's path on
// Muse Code and Codex CLI (card 116: nothing puts a package's bin/ on
// those shells' PATH, and the line naming the path is otherwise printed
// only once a session attaches, so a model told to join had no command to
// run). It names no path of the secret file: the human supplies that, and
// the model never invents one.
func notJoinedLine(teamName, command string) string {
	return "Brigade: not joined: this project uses team \"" + attr(teamName) + "\" — run `" + command + " team join` here or in a terminal (a first join on this machine needs `--secret-file <path>`, the join secret saved to a file outside the repository)."
}

// driftLine is the swap refusal (brief §5): the committed file no longer
// matches what the human consented to, the session attaches to NEITHER
// team, and not one byte of the drifted file is echoed. A cross-team
// change needs the secret file (owner ruling 3): the join backstop, not
// the refusal, is what a re-pointed file runs into.
const driftLine = "Brigade: not connected: .brigade.json does not match the team you joined here — run `brigade team join` to review the change (a cross-team change needs --secret-file)."

// teamFileLine renders a team-file refusal: one fixed line per token of
// the parser's closed list, with the two secret findings taking their
// own urgent forms (the remedies differ: rotate the JOIN secret for a
// committed brg1 shape, rotate the SUPABASE key in the dashboard for an
// sb_secret_ publishable_key).
func teamFileLine(err error) string {
	_, reason := codeOf(err)
	switch reason {
	case teamfile.ReasonSecretShaped:
		return "Brigade: not connected: .brigade.json contains what looks like a join secret — remove it and rotate now (brigade team rotate-secret)."
	case teamfile.ReasonSecretKey:
		return "Brigade: not connected: .brigade.json's publishable_key looks like a Supabase secret key — remove it and rotate that key in the Supabase dashboard."
	case "":
		reason = "unreadable"
	}
	return "Brigade: not connected (config: team_file_" + attr(reason) + "): fix .brigade.json."
}

// maxIgnoredNames caps how many ignored member names the line lists; the
// rest are counted, so a file of a thousand members is still one line.
const maxIgnoredNames = 8

// ignoredMembersLine is the one line for members this version does not
// define (card 32, folder-sync plan §4.1): the file opened, so they are
// ignored rather than refused, and the line says which — names only,
// already reduced to a plain character set and capped by the parser.
func ignoredMembersLine(names []string) string {
	shown, more := names, ""
	if len(names) > maxIgnoredNames {
		shown, more = names[:maxIgnoredNames], ", and "+strconv.Itoa(len(names)-maxIgnoredNames)+" more"
	}
	list := make([]string, len(shown))
	for i, n := range shown {
		list[i] = attr(n)
	}
	return "Brigade: .brigade.json carries members this version does not define (" + strings.Join(list, ", ") + more + "); ignored."
}

// syncUnusableLine is the one line for a `sync` member the parser could
// not use: never a refusal — the session connects, without file sync —
// and the reason is a token from teamfile.SyncReasons, never a value.
func syncUnusableLine(reason string) string {
	return "Brigade: .brigade.json's sync member is not usable (" + attr(reason) + "); file sync is off for this session."
}

// gatewayUnusableLine is the one line for a `gateway` member the parser
// could not use (card 48): never a refusal, and the reason is a token from
// teamfile.GatewayReasons, never a value.
func gatewayUnusableLine(reason string) string {
	return "Brigade: .brigade.json's gateway member is not usable (" + attr(reason) + "); no mail gateway is known to this session."
}

// pollingSetLine is the one line for a usable `polling` member that sets
// a value other than the default (card 53): what the team chose, so a
// session that shows online longer, or a teammate introduced later, has
// its reason in view. "" at the defaults. The values are integers the
// parser bounded, never text from the file.
func pollingSetLine(p *teamfile.PollingConfig) string {
	var set []string
	if p.HeartbeatSeconds != 0 && p.HeartbeatSeconds != polling.DefaultHeartbeatSeconds {
		set = append(set, "a heartbeat every "+strconv.Itoa(p.HeartbeatSeconds)+" s")
	}
	if p.RosterSeconds != 0 && p.RosterSeconds != polling.DefaultRosterSeconds {
		set = append(set, "a roster read every "+strconv.Itoa(p.RosterSeconds)+" s")
	}
	if h := p.IdleCloseHours; h != nil && *h != polling.DefaultIdleCloseHours {
		// Card 61: the hours a VS Code session may stay quiet before it
		// closes itself, or none at all.
		if *h == 0 {
			set = append(set, "no idle close for VS Code sessions")
		} else {
			set = append(set, "an idle close for VS Code sessions after "+strconv.Itoa(*h)+" h")
		}
	}
	if len(set) == 0 {
		return ""
	}
	return "Brigade: .brigade.json's polling member sets " + strings.Join(set, " and ") + "."
}

// pollingUnusableLine is the one line for a `polling` member the parser
// could not use (card 53): never a refusal, and the reason is a token from
// teamfile.PollingReasons, never a value.
func pollingUnusableLine(reason string) string {
	return "Brigade: .brigade.json's polling member is not usable (" + attr(reason) + "); this session uses the default intervals."
}

// teamFileNotes are the lines an attached session's team file earns
// beside the context line: the ignored members, then an unusable sync,
// an unusable gateway, and a polling member that is unusable or sets
// other than the defaults.
func teamFileNotes(tf *teamfile.File) []string {
	if tf == nil {
		return nil
	}
	var notes []string
	if len(tf.Ignored) > 0 {
		notes = append(notes, ignoredMembersLine(tf.Ignored))
	}
	if tf.SyncUnusable != "" {
		notes = append(notes, syncUnusableLine(tf.SyncUnusable))
	}
	if tf.GatewayUnusable != "" {
		notes = append(notes, gatewayUnusableLine(tf.GatewayUnusable))
	}
	if tf.PollingUnusable != "" {
		notes = append(notes, pollingUnusableLine(tf.PollingUnusable))
	}
	if tf.Polling != nil {
		if line := pollingSetLine(tf.Polling); line != "" {
			notes = append(notes, line)
		}
	}
	return notes
}

// optionsLine is the context line for an option ParseOptions refused. A
// frame or frame_file value the parser itself refuses — a level outside
// the three words, a relative path — is the frame line, whose remedy is
// the user's settings (the P5-12 verifier's 3.1); every other option keeps
// the terminal remedy. The reason tokens are the parser's own.
func optionsLine(err error) string {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		switch perr.Details["option"] {
		case "frame", "frame_file":
			return frameLine(err)
		}
	}
	return notConnected(protocol.CodeConfig)
}

// frameLine is the context line for a frame or frame_file option the hook
// could not use (P5-12): the fixed details.reason, never the value and
// never the path; the remedy is the user's settings. The SessionStart
// context line itself never names the level — that line is model-facing
// by construction, and the level is the human's configuration (whoami).
func frameLine(err error) string {
	_, reason := codeOf(err)
	if reason == "" {
		reason = "frame_file_unreadable"
	}
	return "Brigade: not connected (config: " + attr(reason) + "); fix the `frame` or `frame_file` option in your settings"
}

// startLine is the one SessionStart context line (6.3, brief 2.2: the
// teammate count is omitted — the registration result carries none and a
// second spawn is not worth it — so the line points at `brigade sessions`).
// It names ONLY the bare `brigade`, the form the Bash tool finds on PATH
// inside a session and the only form the permission rules can see: an
// earlier line ended "terminal commands: <plugin path>" and the model took
// it literally — it replied through that absolute path in 4 of 29 idle
// wakes (P4-3) and in an interactive bypass session (P4-5), a form
// `Bash(brigade:*)` denies, the ask rule `Bash(brigade send*)` does not
// gate (that send executed with no dialog) and 9.6's judge classes
// `evasive`. The path is a human surface now — `brigade whoami` and
// docs/team-member/install/terminal.md (finding F1, ruled 2026-09-04).
func startLine(name, id, team, policy string) string {
	var b strings.Builder
	b.WriteString("Brigade: this session is \"")
	b.WriteString(attr(name))
	b.WriteString("\" (")
	b.WriteString(ident(id))
	b.WriteString(") in team \"")
	b.WriteString(attr(team))
	b.WriteString("\"; inbound: ")
	b.WriteString(policy)
	b.WriteString("; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`.")
	return b.String()
}

// shadowLine warns that another `brigade` on the hook's PATH shadows the
// plugin's (E0-8 (e), 6.2).
func shadowLine(path string) string {
	return "Brigade: another `brigade` at " + oneLine(path, 512) + " shadows the plugin's; remove it or the wrong version runs"
}

// The three doing-line reminders (card 25, plan 5.4), printed by the prompt
// hook's doingNudge on the stamp's say-so: BLANK when the stamp is missing
// or names another conversation (a new, resumed, cleared or forked
// conversation, or a failed carry — Brigade itself just blanked the value),
// FULL when the stamp is zeroed (a /compact, or a prompt seen in an
// ineligible permission mode: the value may still stand), SHORT when the
// stamp is doingNudgeInterval old (the full text is still in context). They
// are CONSTANTS — no session text, no teammate text, nothing to sanitise —
// declarative as the hooks page advises, naming only the bare `brigade`
// (the F1 rule of startLine), and every one says a refusal is final for the
// conversation, because an ask or deny Brigade cannot see fails open (plan
// 5.2). Byte for byte what is printed. Measured on a real interactive
// session (docs/experiments/E10-doing-triggers.md part 2, 2026-09-20):
// BLANK was acted on 22 s after a neutral task prompt and SHORT 4 s after a
// pivot; a prompt that scoped the model's "whole task" was not. Changing
// them costs three constants.
const (
	doingLineBlank = "Brigade doing: this session's line is blank (a new, resumed or cleared conversation starts without one); teammates route by it. It is set with `brigade doing <<'EOF'`, one short sentence, `EOF`, once the work is clear, and again when the work changes (new task, or new phase, e.g. implementing to testing). No secrets, local paths or customer names. Subagents leave it alone. If refused, it is not run another way; the work carries on."
	doingLineFull  = "Brigade doing: teammates route by one line per session saying what it is working on. This session's is set with `brigade doing <<'EOF'`, one short sentence, `EOF`, when none is set or the work has changed (new task, or new phase, e.g. implementing to testing); if it still fits, nothing is needed. No secrets, local paths or customer names. Subagents leave it alone. If refused, it is not run another way; the work carries on."
	doingLineShort = "Brigade doing: this session's line is due again only if the work has changed since it was set (new task or new phase): `brigade doing <<'EOF'`, one short sentence, `EOF`. If it still fits, or it was refused earlier in this conversation, nothing is needed."
)

// attr sanitises a remote string for the context line: the attribute
// rules of 6.7 step 4 (quotes, angle brackets and newlines dropped) and the
// 64-code-point cap.
func attr(s string) string { return protocol.SanitizeAttribute(s) }

// ident sanitises an opaque id for the context line with the attribute
// character rules but no cap: a truncated session id would route
// `brigade send` to a session that does not exist (6.7).
func ident(s string) string {
	s = protocol.Sanitize(s)
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '<', '>', '\n', '\r':
			return -1
		}
		return r
	}, s)
}

// oneLine folds s onto one sanitised line of at most limit code points,
// for text that is Brigade's own or local (a path, a notice) but must
// still be one line of context.
func oneLine(s string, limit int) string {
	s = protocol.Sanitize(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i]
		}
		n++
	}
	return s
}

// registeredName folds a display name onto one line and caps it at the
// wire limit before it is registered (C-16).
func registeredName(s string) string {
	return oneLine(protocol.SanitizeName(s), protocol.MaxSessionNameCodepoints)
}

// identity is the 6.5 resolution.
type identity struct {
	name           string
	activity       string
	harnessVersion string
	entrypoint     string
	nonInteractive bool
}

// identity resolves the display name (registry → session_title →
// basename(cwd); Grok, its summary title, otherwise "grok"), the activity
// (registry status → idle), the harness version (registry version →
// unknown) and the entrypoint (environment → registry; `kind` is ignored).
func (r *run) identity(f facts, in input) identity {
	if f.host == config.HostGrok {
		// The title is local metadata, not the hook document. SessionStart
		// blanks session_title. With no title yet the roster keeps "grok"
		// until a later heartbeat sees one.
		name := "grok"
		cwd := in.launchCwd
		if cwd == "" {
			cwd = in.Cwd
		}
		if title := watch.GrokSessionName(r.environ, cwd, in.SessionID); title != "" {
			name = title
		}
		return identity{name: name, activity: protocol.ActivityIdle, harnessVersion: unknownVersion}
	}
	entry := r.registryEntry(f)
	id := identity{
		activity:       entry.Activity(),
		harnessVersion: entry.Version,
		entrypoint:     f.entrypoint,
	}
	if id.harnessVersion == "" {
		id.harnessVersion = unknownVersion
	}
	if id.entrypoint == "" {
		id.entrypoint = entry.Entrypoint
	}
	id.nonInteractive = id.entrypoint == entrypointSDK
	for _, candidate := range []string{entry.Name, in.SessionTitle, cwdName(in.Cwd)} {
		if name := registeredName(candidate); name != "" {
			id.name = name
			break
		}
	}
	if id.name == "" {
		id.name = string(f.host)
	}
	return id
}

// cwdName is basename(cwd), or "" when cwd names no directory.
func cwdName(cwd string) string {
	if cwd == "" {
		return ""
	}
	base := filepath.Base(cwd)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// registryEntry reads the session registry entry best effort (6.5): the
// injected fs.FS, else the directory under the Claude config dir; a
// missing config dir or entry is the zero Entry.
func (r *run) registryEntry(f facts) registry.Entry {
	if f.pointed() {
		// Muse Code and Codex CLI keep no session registry under a Claude
		// config directory (cards 88, 107): the name comes from the
		// document, the activity is idle and the version unknown, whatever
		// a test injected for the Claude path.
		return registry.Entry{}
	}
	fsys := r.deps.Registry
	if fsys == nil {
		if f.claudeConfigDir == "" {
			return registry.Entry{}
		}
		fsys = registry.Dir(f.claudeConfigDir)
	}
	entry, err := registry.Read(fsys, f.pid)
	if err != nil {
		r.log.Debug("registry entry unavailable", log.Err(err))
	}
	return entry
}

// client builds the adapter client for a resolved adapter and profile.
func (r *run) client(adapter config.Adapter, profile, configDir, stateDir string) *adapterclient.Client {
	return &adapterclient.Client{
		Adapter:   adapter,
		Profile:   profile,
		ConfigDir: configDir,
		StateDir:  stateDir,
		LogLevel:  r.logLevel,
		Environ:   r.environ,
		Logger:    r.log,
		Spawn:     r.deps.Spawn,
	}
}

// noticePath is the watcher's notice file (3.2; the notice package).
func noticePath(stateDir string, pid int) string { return notice.Path(stateDir, pid) }

// retryStampPath records the last registration retry of the prompt hook.
func retryStampPath(stateDir string, pid int) string {
	return config.RegisterRetryStamp(stateDir, pid)
}

// doingStampPath is the prompt hook's doing-line reminder stamp (card 25,
// plan 5.4), the path config shares with the watcher.
func doingStampPath(stateDir string, pid int) string {
	return config.DoingNudgeStamp(stateDir, pid)
}
