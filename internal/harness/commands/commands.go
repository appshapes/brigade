// Package commands implements the human and model command surface of the
// `brigade` binary (plan 6.4): `sessions`, `send`, `whoami`, `team members`,
// and the terminal pass-through of `team create|join|leave` and `profile
// init|status|reset|revoke-credentials`. The cli package's command table
// calls in here; nothing here knows the flag parser or the exit-code
// reporter, which is what keeps the two packages from importing each
// other.
//
// Every session-bound command resolves its session the same way (6.4,
// 6.5): inside a Claude Code session — CLAUDE_PID set — everything comes
// from the by-pid map the SessionStart hook wrote (profile, config dir,
// adapter argv, Brigade session id, team), read through the strict reader,
// and every inherited BRIGADE_* variable is ignored (U-27; the map is the
// trust boundary of E0-7); in a plain terminal the profile comes from
// --profile or BRIGADE_PROFILE, the directories from the shell's BRIGADE_*
// or the XDG defaults, and the adapter from the D36 chain (sidecar → profile
// member → bundled). Every adapter call goes through adapterclient.Client:
// the from-scratch child environment, the 4.1 budgets, the cached
// `describe` with its protocol check.
//
// Output discipline (6.4, 7.3): human output is stable, one item per line,
// written to the Out stream the caller hands in — never os.Stdout — and
// every remote string is sanitised (protocol.Sanitize*) before it is
// printed; human_label is always shown as unverified (B-3). With --json,
// stdout carries exactly one 4.3 envelope whose result is the adapter's
// result plus the harness members self_session_id and note. Failures are
// returned as *protocol.Error values (the cli renders the one stderr line
// or the error envelope, with the code's exit status) and never carry raw
// adapter stderr (U-24).
package commands

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// An Invocation is everything one command run may read: its arguments,
// the process environment in os.Environ form (never os.Getenv), the three
// streams, the two global flags and the injectable side effects.
type Invocation struct {
	// Args are the positional arguments after the command word for the
	// typed commands (sessions, send, whoami), or the whole argument
	// vector after the command word, verbatim, for the raw ones (team,
	// profile), which parse their own grammar because the adapter flags
	// they forward are not the harness's to know.
	Args []string
	// Environ is the process environment. Inside a session only CLAUDE_*
	// and the XDG/HOME variables are honoured; every BRIGADE_* is
	// ignored (config.Trusted).
	Environ []string
	// In, Out and Err are the process streams. Out carries human output
	// or exactly one JSON envelope; Err carries the redacting logger's
	// diagnostics.
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// JSON is the global --json flag: the 4.3 envelope on Out instead of
	// human output.
	JSON bool
	// LogLevel is the global --log-level flag, "" for the default (warn
	// for the harness logger; the adapter child applies its own default).
	LogLevel string
	// Deps are the injectable side effects; the zero value is production.
	Deps Deps
}

// Deps are the side effects a test injects. Every member is optional; a
// nil member means the real thing.
type Deps struct {
	// Now is the clock the idempotency key of D11 is derived from.
	Now func() time.Time
	// Spawn is the request/response spawn seam (adapterclient.Client.Spawn):
	// a recorder here proves zero spawns for a refused body (U-05) and
	// what every child would have received.
	Spawn adapterclient.SpawnFunc
	// Sleep is the pause before the one retry on `unavailable`.
	Sleep func(time.Duration)
	// IsTerminal reports whether a stream is a terminal; `send` refuses
	// to read a body from one (a human would wait forever).
	IsTerminal func(io.Reader) bool
}

// RetryPause is the pause before the single retry of `message send` on
// `unavailable` (6.4): long enough for a transient backend blip, short
// enough that the model's 20 s budget is not doubled by waiting.
const RetryPause = time.Second

// The fixed texts of 6.4 that tests and the skill assert word for word.
const (
	// RefusalInSession is the whole message of the `usage` refusal of
	// `team create`, `team join` and (P5-2) `team rotate-secret` inside a
	// Claude Code session: each of the three handles the join secret.
	RefusalInSession = "run this in your own terminal: the join secret must never pass through the chat"
	// RefusalAdminInSession is the whole message of the same refusal, in
	// the same shape (usage, exit 2, details.reason in_session), for
	// `team revoke-member` and `team transfer` (P5-2): destructive
	// administrative acts that a session reading untrusted teammate text
	// must not be talked into (4.5 rule 15) get their own line.
	RefusalAdminInSession = "run this in your own terminal: team administration is not driven from a session"
	// AcceptedNote closes every successful `send` line and is the note of
	// its --json result: 4.5.1 never says "delivered".
	AcceptedNote = "Accepted means durably stored by the adapter, not read."
	// UnverifiedSuffix follows every human_label (B-3).
	UnverifiedSuffix = " (unverified)"
)

// An ExitStatus is the error a pass-through command returns when the
// adapter ran and exited with a status of its own: the adapter already
// wrote its envelope to the caller's stdout, so the harness must forward
// the status and print nothing. The cli maps it to the process exit.
type ExitStatus int

// Error implements the error interface.
func (e ExitStatus) Error() string {
	return "the adapter exited with status " + itoa(int(e))
}

// now returns the injected clock or the real one.
func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// sleep pauses through the injected sleeper or time.Sleep.
func (d Deps) sleep(delay time.Duration) {
	if d.Sleep != nil {
		d.Sleep(delay)
		return
	}
	time.Sleep(delay)
}

// isTerminal reports whether r is a terminal, through the seam or
// x/term on a real *os.File.
func (d Deps) isTerminal(r io.Reader) bool {
	if d.IsTerminal != nil {
		return d.IsTerminal(r)
	}
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// logger builds the redacting stderr logger for one invocation. An
// unknown level is the cli's usage error before this runs; "" is warn, so
// a healthy command writes nothing to stderr.
func (inv Invocation) logger() *slog.Logger {
	level := slog.LevelWarn
	if inv.LogLevel != "" {
		if parsed, err := adapterlog.ParseLevel(inv.LogLevel); err == nil {
			level = parsed
		}
	}
	w := inv.Err
	if w == nil {
		w = io.Discard
	}
	return adapterlog.New(w, level, nil).With(slog.String("comp", "commands"))
}

// A target is the resolved adapter and, inside a session, the session the
// command acts for.
type target struct {
	// inSession is true when CLAUDE_PID is set and the by-pid map was read.
	inSession bool
	// session is the by-pid map; nil in a terminal.
	session *sessionmap.ByPID
	// profile, configDir and stateDir are the resolved values the child
	// environment carries.
	profile   string
	configDir string
	stateDir  string
	// adapter is the argv prefix that is spawned (6.5, D36): the map's
	// resolved command inside a session, the D36 chain's result outside.
	adapter config.Adapter
	// defaultAdapter is the profile's default per the D36 chain, for
	// `profile status`; defaultErr is set instead when the chain failed
	// for a pass-through inside a session (the session still runs on the
	// map's command).
	defaultAdapter config.Adapter
	defaultErr     error
	// client spawns this adapter's children.
	client *adapterclient.Client
	// describe is the cached, protocol-checked describe result.
	describe *protocol.DescribeResult
}

// selfSessionID is the Brigade session id inside a session, "" outside.
func (t *target) selfSessionID() string {
	if t.session == nil {
		return ""
	}
	return t.session.BrigadeSessionID
}

// resolveSession resolves the target of a SESSION-BOUND command (6.4):
// inside a session from the by-pid map alone, outside from --profile /
// BRIGADE_PROFILE and the D36 chain. It then runs the cached `describe`
// (the protocol check → protocol_mismatch). profileFlag is the --profile
// value; inside a session it is a usage error, because the session's
// profile is the map's and nothing else (E0-7).
func (inv Invocation) resolveSession(profileFlag string) (*target, error) {
	if config.InSession(inv.Environ) {
		if profileFlag != "" {
			return nil, usage("--profile is not accepted inside a Claude Code session; the session's profile comes from its session map")
		}
		return inv.sessionTarget()
	}
	return inv.terminalTarget(profileFlag, false)
}

// sessionTarget builds the target from the by-pid map.
func (inv Invocation) sessionTarget() (*target, error) {
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return nil, err
	}
	m, err := config.Session(inv.Environ, stateDir)
	if err != nil {
		return nil, err
	}
	adapter, err := config.AdapterFromArgv(m.AdapterCommand)
	if err != nil {
		return nil, err
	}
	t := &target{
		inSession: true,
		session:   m,
		profile:   m.Profile,
		configDir: m.ConfigDir,
		stateDir:  stateDir,
		adapter:   adapter,
	}
	t.client = inv.client(t)
	return t.probe()
}

// terminalTarget builds the target of a command run from a terminal —
// or of a pass-through, which may run anywhere. The profile is
// profileFlag, else BRIGADE_PROFILE outside a session ("default" inside);
// the config and state directories are the shell's BRIGADE_* outside a
// session and the XDG defaults inside; the adapter is the D36 chain with
// no override. Inside a session a readable by-pid map supplies the
// session's profile and config dir when --profile is absent (a human
// running `profile status` from the Bash tool asks about THIS session's
// profile), and is recorded on the target so `profile status` can name
// the override in force; an unreadable map is not an error here, because
// the terminal commands need no session. passThrough marks that use.
func (inv Invocation) terminalTarget(profileFlag string, passThrough bool) (*target, error) {
	configDir, err := config.BrigadeConfigDir(inv.Environ)
	if err != nil {
		return nil, err
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return nil, err
	}
	profile := profileFlag
	if profile == "" {
		profile = config.ProfileName(inv.Environ)
	}
	t := &target{profile: profile, configDir: configDir, stateDir: stateDir}
	if passThrough && config.InSession(inv.Environ) {
		if m, merr := config.Session(inv.Environ, stateDir); merr == nil {
			t.session = m
			t.configDir = m.ConfigDir
			if profileFlag == "" {
				t.profile = m.Profile
			}
		}
	}
	if err := adapterkit.CheckProfileName(t.profile); err != nil {
		return nil, err
	}
	def, derr := config.ResolveAdapter(config.Options{}, t.configDir, t.profile)
	if t.session != nil && t.session.Profile == t.profile {
		// The session's own profile: the command runs on the adapter the
		// session actually uses (the map's resolved command, override
		// included); the D36 default is kept for `profile status` and its
		// failure does not stop a command the session can run.
		t.adapter, err = config.AdapterFromArgv(t.session.AdapterCommand)
		if err != nil {
			return nil, err
		}
		t.defaultAdapter, t.defaultErr = def, derr
	} else {
		if derr != nil {
			return nil, derr
		}
		t.adapter, t.defaultAdapter = def, def
	}
	t.client = inv.client(t)
	if passThrough {
		return t, nil
	}
	return t.probe()
}

// client builds the adapterclient for a target.
func (inv Invocation) client(t *target) *adapterclient.Client {
	return &adapterclient.Client{
		Adapter:   t.adapter,
		Profile:   t.profile,
		ConfigDir: t.configDir,
		StateDir:  t.stateDir,
		LogLevel:  inv.LogLevel,
		Environ:   inv.Environ,
		Logger:    inv.logger(),
		Spawn:     inv.Deps.Spawn,
	}
}

// probe runs the cached `describe` with its 3 s budget: the protocol check
// of 4.5.13 is what turns a wrong-version adapter into protocol_mismatch
// before any other spawn.
func (t *target) probe() (*target, error) {
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.DescribeTimeout)
	defer cancel()
	d, err := t.client.Describe(ctx)
	if err != nil {
		return nil, err
	}
	t.describe = d
	return t, nil
}

// call runs f under the 20 s request/response budget of 4.1.
func call[T any](f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), adapterclient.DefaultTimeout)
	defer cancel()
	return f(ctx)
}

// usage builds a `usage` failure with fixed text. Callers never
// interpolate an argument: argv can carry a secret (4.5.14).
func usage(msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUsage, Message: msg}
}

// notInSession is the `config` failure of a command that needs a Brigade
// session when CLAUDE_PID is unset.
func notInSession(command string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeConfig,
		Message: "brigade " + command + " needs a Brigade session: run it from the Bash tool inside a Claude Code session with the plugin enabled",
		Details: map[string]string{"reason": config.ReasonNotInSession},
	}
}

// writeJSON writes exactly one 4.3 success envelope carrying result.
func writeJSON(w io.Writer, result any) error {
	if exit := adapterkit.WriteResult(w, result); exit != 0 {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the result envelope could not be written"}
	}
	return nil
}

// writeLines writes human output: each line newline-terminated.
func writeLines(w io.Writer, lines ...string) error {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// isCode reports whether err is a *protocol.Error of the given code.
func isCode(err error, code protocol.Code) bool {
	var perr *protocol.Error
	return errors.As(err, &perr) && perr.Code == code
}

// isReason reports whether err is a *protocol.Error whose details.reason is
// reason (adapterkit.Spawn's own classifications carry one: timeout,
// adapter_not_found, stdout_overflow, …).
func isReason(err error, reason string) bool {
	var perr *protocol.Error
	return errors.As(err, &perr) && perr.Details["reason"] == reason
}
