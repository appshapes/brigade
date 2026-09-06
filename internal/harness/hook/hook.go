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
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/registry"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

// The three subcommands (6.3, hooks.json).
const (
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
	// OutputCap is the 10,000-character hook-output cap of 6.3: frames
	// are printed until it would be exceeded, and only printed frames are
	// acknowledged.
	OutputCap = 10000
	// maxStdinBytes bounds the hook's stdin document (a real one is well
	// under 4 KiB).
	maxStdinBytes = 1 << 20
	// harnessName is the `harness` member of every registration.
	harnessName = "claude-code"
	// unknownVersion is harness_version when the registry has none.
	unknownVersion = "unknown"
	// pollLimit is the `--limit` of the prompt-hook poll (6.3).
	pollLimit = 20
)

// The hook stdin members this package reads (6.3, 6.5; the 2.1.251
// evidence under docs/research/claude-plugin-mcp-evidence). permission_mode
// and session_title are optional everywhere; transcript_path and prompt
// are deliberately absent from the type — they are never read, stored or
// sent.
type input struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	PermissionMode string `json:"permission_mode"`
	SessionTitle   string `json:"session_title"`
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
	// ReadFile reads the three settings files of the native scan (6.10)
	// and, at SessionStart, the frame_file option's file (P5-12); nil
	// means os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// Spawn is the adapter request/response seam (adapterclient.Client.
	// Spawn); nil means adapterkit.Spawn, a real child.
	Spawn adapterclient.SpawnFunc
	// Lookup reads process facts for the pidfile guard; nil means
	// procutil.Lookup.
	Lookup pidfile.LookupFunc
	// LookPath searches a PATH value for an executable name (the
	// shadowing check of 6.2); nil means the package's own search.
	LookPath func(pathVar, name string) (string, bool)
	// Sink, when set, is passed to the watcher as `--sink <file>` (6.6).
	// Only a test harness sets it; production never does.
	Sink string
	// PidfileWait bounds the wait for a spawned watcher's pidfile; zero
	// means DefaultPidfileWait.
	PidfileWait time.Duration
}

// RealDeps returns the production dependencies.
func RealDeps() Deps {
	return Deps{
		Now:         time.Now,
		Spawner:     RealSpawner{},
		ReadFile:    os.ReadFile,
		Lookup:      procutil.Lookup,
		LookPath:    lookPath,
		PidfileWait: DefaultPidfileWait,
	}
}

// withDefaults fills every nil member with its production value.
func (d Deps) withDefaults() Deps {
	prod := RealDeps()
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
	if d.PidfileWait <= 0 {
		d.PidfileWait = prod.PidfileWait
	}
	return d
}

// Run executes one `brigade hook <subcommand>` invocation and returns the
// process exit status. args excludes the words `brigade hook`; the only
// flag is --log-level. environ is the process environment in os.Environ
// form. Every subcommand returns 0 on every failure (6.3); the one
// non-zero answer is `usage` (2) for a missing or unknown subcommand or
// flag, which no hooks.json ever produces.
func Run(args []string, streams cli.Streams, environ []string, deps Deps) int {
	sub, level, perr := parseArgs(args)
	if perr != "" {
		cmd, _ := cli.Lookup("hook")
		return cli.Usage(cmd, args, streams, perr)
	}
	r := newRun(streams, environ, deps.withDefaults(), level)
	switch sub {
	case SubSessionStart:
		return r.sessionStart()
	case SubPrompt:
		return r.prompt()
	default:
		return r.sessionEnd()
	}
}

// parseArgs picks the subcommand and the optional --log-level. The
// message is fixed text: argv is never echoed (4.5.14).
func parseArgs(args []string) (sub, level, usage string) {
	level = "info"
	if len(args) == 0 {
		return "", "", "hook needs one of session-start, prompt, session-end"
	}
	sub = args[0]
	if sub != SubSessionStart && sub != SubPrompt && sub != SubSessionEnd {
		return "", "", "unknown hook; the hooks are session-start, prompt and session-end"
	}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		name, value, has := strings.Cut(rest[i], "=")
		if name != "--log-level" {
			return "", "", "unknown hook flag; the only flag is --log-level"
		}
		if !has {
			if i+1 >= len(rest) {
				return "", "", "--log-level needs a value"
			}
			i++
			value = rest[i]
		}
		if _, err := log.ParseLevel(value); err != nil {
			return "", "", "--log-level must be one of error, warn, info, debug"
		}
		level = value
	}
	return sub, level, ""
}

// A run is one hook invocation.
type run struct {
	deps     Deps
	streams  cli.Streams
	environ  []string
	logLevel string
	redactor *log.Redactor
	log      *slog.Logger
	// written counts the code points written to stdout so far, against
	// OutputCap.
	written int
}

func newRun(streams cli.Streams, environ []string, deps Deps, level string) *run {
	lvl, _ := log.ParseLevel(level)
	redactor := log.NewRedactor()
	// The token is registered before anything is logged, so no line can
	// carry it even by accident (U-25).
	redactor.Add(adapterkit.Getenv(environ, envMessagingToken))
	return &run{
		deps:     deps,
		streams:  streams,
		environ:  environ,
		logLevel: level,
		redactor: redactor,
		log:      log.New(streams.Err, lvl, redactor),
	}
}

// The Claude Code variables the hook reads (6.5), all through
// adapterkit.Getenv over the environ it was handed.
const (
	envMessagingSocket = "CLAUDE_CODE_MESSAGING_SOCKET"
	envMessagingToken  = "CLAUDE_CODE_MESSAGING_TOKEN" //nolint:gosec // G101: a variable NAME, not a credential
	envPluginRoot      = "CLAUDE_PLUGIN_ROOT"
	envEntrypoint      = "CLAUDE_CODE_ENTRYPOINT"
	entrypointSDK      = "sdk-cli"
)

// facts are the session facts every subcommand resolves from the
// environment (6.5).
type facts struct {
	pid             int
	stateDir        string
	claudeConfigDir string // "" when it cannot be resolved (registry and user-settings scan skipped)
	socket          string
	token           string
	pluginBin       string
	entrypoint      string
	home            string
}

// facts resolves the session facts. The error is `config`: no CLAUDE_PID
// (the hook was run outside a session) or no state directory (HOME unset).
func (r *run) facts() (facts, error) {
	pid, err := config.ClaudePID(r.environ)
	if err != nil {
		return facts{}, err
	}
	stateDir, err := config.BrigadeStateDir(r.environ)
	if err != nil {
		return facts{}, err
	}
	f := facts{
		pid:        pid,
		stateDir:   stateDir,
		socket:     adapterkit.Getenv(r.environ, envMessagingSocket),
		token:      adapterkit.Getenv(r.environ, envMessagingToken),
		entrypoint: adapterkit.Getenv(r.environ, envEntrypoint),
		home:       adapterkit.Getenv(r.environ, "HOME"),
	}
	if !filepath.IsAbs(f.socket) {
		// The map requires an absolute socket path or none; a relative
		// value is not a socket the watcher may post to.
		f.socket = ""
	}
	if dir, cerr := config.ClaudeConfigDir(r.environ); cerr == nil {
		f.claudeConfigDir = dir
	} else {
		r.log.Debug("claude config dir unresolved; registry and user settings skipped", log.Err(cerr))
	}
	if root := adapterkit.Getenv(r.environ, envPluginRoot); root != "" && filepath.IsAbs(root) {
		f.pluginBin = pluginBin(root)
	}
	return f, nil
}

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
	return in, nil
}

// say writes one context line to stdout and counts it against OutputCap.
func (r *run) say(line string) {
	if r.streams.Out == nil {
		return
	}
	_, _ = io.WriteString(r.streams.Out, line+"\n")
	r.written += utf8.RuneCountInString(line) + 1
}

// fits reports whether line (plus its newline) still fits under OutputCap.
func (r *run) fits(line string) bool {
	return r.written+utf8.RuneCountInString(line)+1 <= OutputCap
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
// the human in a terminal.
func notConnected(code protocol.Code) string {
	if code.Retryable() {
		return "Brigade: not connected (" + string(code) + "); retrying at your next prompt"
	}
	return "Brigade: not connected (" + string(code) + "); run `brigade team join` in a terminal"
}

// notConnectedFor is notConnected for an adapter failure: a `config` the
// adapter produced carries its fixed details.reason (profile_missing, a
// malformed profile, …) so the line says WHY the resolved adapter cannot
// read the profile (the P3-4 row, D36) and never just the code; the
// remedy stays the terminal. The reason is a fixed token, never a value.
func notConnectedFor(err error) string {
	code, reason := codeOf(err)
	if code == protocol.CodeConfig && reason != "" {
		return "Brigade: not connected (config: " + attr(reason) + "); run `brigade team join` in a terminal"
	}
	return notConnected(code)
}

// adapterLine is the D36 context line for an adapter that could not be
// resolved, naming the source (option, sidecar, profile) and never the
// value.
func adapterLine(profile string, err error) string {
	source := ""
	var perr *protocol.Error
	if errors.As(err, &perr) {
		source = perr.Details["source"]
	}
	if source == "" {
		source = "its configuration"
	}
	return "Brigade: not connected (config): the adapter for profile \"" + attr(profile) +
		"\" could not be resolved from " + attr(source) + "; run `brigade profile status` in a terminal"
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
// docs/setup.md (finding F1, ruled 2026-09-04).
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
// basename(cwd)), the activity (registry status → idle), the harness
// version (registry version → unknown) and the entrypoint (environment →
// registry; `kind` is ignored).
func (r *run) identity(f facts, in input) identity {
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
		id.name = harnessName
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

// stateFile is ${stateDir}/state/<pid><suffix>.
func stateFile(stateDir string, pid int, suffix string) string {
	return filepath.Join(stateDir, "state", strconv.Itoa(pid)+suffix)
}

// noticePath is the watcher's one-line notice (3.2).
func noticePath(stateDir string, pid int) string { return stateFile(stateDir, pid, ".notice") }

// retryStampPath records the last registration retry of the prompt hook.
func retryStampPath(stateDir string, pid int) string {
	return stateFile(stateDir, pid, ".register-retry")
}
