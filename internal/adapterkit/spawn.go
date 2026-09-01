// The spawn helper reaps children with syscall.WaitStatus and signals
// them with SIGTERM, both of which exist on the two supported platforms
// only (D33): the build constraint makes go vet on a third OS fail loudly
// instead of compiling a binary that cannot supervise its adapter.

//go:build darwin || linux

package adapterkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// MaxAdapterStdout is the cap on what a child may write to its stdout
// before the harness cancels it (plan 7.3): one 4.3 envelope is far
// smaller, so anything past 4 MiB is a runaway or an attack, not a result.
const MaxAdapterStdout = 4 << 20

// DefaultWaitDelay is how long Spawn waits after SIGTERM (or after a
// clean exit that left the stdout pipe open) before SIGKILL and pipe
// closure. 5 s matches the watcher's supervision of its adapter child
// (plan 6.6).
const DefaultWaitDelay = 5 * time.Second

// The details.reason values Spawn's 4.6 mapping emits. "timeout" and
// "adapter_not_found" are named by the plan (4.6); the stdout reasons are
// diagnostic refinements of `internal`.
const (
	spawnReasonTimeout     = "timeout"
	spawnReasonCanceled    = "canceled"
	spawnReasonNotFound    = "adapter_not_found"
	spawnReasonOverflow    = "stdout_overflow"
	spawnReasonNotJSON     = "stdout_not_json"
	spawnReasonBadEnvelope = "stdout_invalid_envelope"
	spawnReasonSpawnError  = "spawn_error"
	spawnReasonWaitDelay   = "wait_delay_expired"
	spawnReasonEmptyArgv   = "empty_argv"
	spawnDetailReasonKey   = "reason"
	spawnDetailSignalKey   = "signal"
)

// A SpawnSpec describes one adapter child process. Argv is an argv array
// and nothing else: Argv[0] is executed directly, no shell is ever
// involved, and no field of this struct is interpreted by one (plan 7.3).
type SpawnSpec struct {
	// Argv is the child's argv; Argv[0] is the executable. It must be
	// non-empty.
	Argv []string
	// Env is the child's entire environment, in os.Environ form, built
	// from scratch by the caller — normally with [ChildEnv]. The child
	// NEVER inherits this process's environment: a nil Env runs the child
	// with an empty one, not the parent's (the dynamic half of the 3.2
	// environment-isolation rule; forbidigo's exec.Command ban is the
	// static half).
	Env []string
	// Stdin is the input document, fed from a bytes.Reader. A nil Stdin
	// runs the child with no stdin at all (the null device): a command
	// that takes no input must not be handed a pipe it could block on.
	Stdin []byte
	// Stderr receives the child's stderr — normally the adapter log file.
	// A nil Stderr discards it. Stdout is never the caller's to redirect:
	// it is the protocol channel and Spawn owns it.
	Stderr io.Writer
	// WaitDelay bounds how long the child may outlive its context cancel
	// (SIGTERM) or its own exit with the stdout pipe still open, before
	// SIGKILL and forced pipe closure. Zero or negative means
	// [DefaultWaitDelay].
	WaitDelay time.Duration
	// Logger receives the diagnostics 7.3 requires (the tolerated
	// exec.ErrWaitDelay case is logged), scalar attributes only. A nil
	// Logger discards them.
	Logger *slog.Logger
}

// A SpawnResult is a child that produced a parseable protocol envelope.
type SpawnResult struct {
	// Envelope is the child's stdout, parsed and validated against 4.3.
	// It may be a failing envelope: an adapter that exits with its own
	// 4.6 status and a well-formed error envelope is speaking the
	// protocol, not failing to — the caller reads Envelope.Error and
	// ExitCode and decides. Spawn deliberately does not cross-check the
	// exit status against the envelope's code.
	Envelope *protocol.Envelope
	// ExitCode is the child's own exit status.
	ExitCode int
	// WaitDelayExpired records the tolerated exec.ErrWaitDelay case: the
	// child exited 0 with a valid result but its stdout pipe stayed open
	// past WaitDelay (an adapter must not hand stdout to a grandchild,
	// but a valid result that still arrived stands, plan 7.3).
	WaitDelayExpired bool
}

// Spawn runs one adapter child to completion and maps every way it can
// fail onto the 4.6 taxonomy. The returned error, when non-nil, is always
// a *protocol.Error:
//
//   - a missing executable (errors.Is os.ErrNotExist, or exec.ErrNotFound
//     for a PATH search) → `unavailable`, details.reason "adapter_not_found";
//   - the caller's ctx expiring → `unavailable`, details.reason "timeout"
//     (detected with ctx.Err(), never from cmd.Wait: Wait reports the
//     SIGTERM death the cancel itself caused, plan 4.6 [verified, A.7]);
//   - a child killed by a signal with the context still live →
//     `unavailable`, details.signal naming the signal;
//   - stdout past [MaxAdapterStdout] → the context is cancelled (SIGTERM,
//     then SIGKILL after WaitDelay) and the mapping is `internal`,
//     details.reason "stdout_overflow";
//   - stdout that is not one valid 4.3 envelope → `internal`.
//
// The deadline is the caller's: pass a context bounded with
// context.WithTimeout (the harness applies its 3.5 budgets there).
func Spawn(ctx context.Context, spec SpawnSpec) (*SpawnResult, error) {
	if len(spec.Argv) == 0 {
		return nil, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "adapter spawn called with an empty argv",
			Details: map[string]string{spawnDetailReasonKey: spawnReasonEmptyArgv},
		}
	}
	logger := spec.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	// The child context exists so the capped stdout writer can stop the
	// child; the caller's ctx keeps the authority on timeouts, which is
	// why classification below asks ctx.Err(), never cctx.Err().
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	//nolint:forbidigo // adapterkit.Spawn IS the spawn seam the 7.3 forbidigo rule points callers at.
	cmd := exec.CommandContext(cctx, spec.Argv[0], spec.Argv[1:]...)
	// Never nil: a nil cmd.Env would inherit this process's environment.
	cmd.Env = append(make([]string, 0, len(spec.Env)), spec.Env...)
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	out := &cappedStdout{limit: MaxAdapterStdout, cancel: cancel}
	cmd.Stdout = out
	if spec.Stderr != nil {
		cmd.Stderr = spec.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	cmd.Cancel = func() error {
		// SIGTERM first so the adapter can flush; WaitDelay escalates to
		// SIGKILL and closes the pipes (plan 7.3).
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = spec.WaitDelay
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = DefaultWaitDelay
	}

	runErr := cmd.Run()

	// Order is load-bearing. Overflow first: the cancel it fired makes
	// every later observation (a SIGTERM death, a context error) an
	// effect, not a cause. Then a clean exit, whose parsed result stands
	// even if the caller's deadline expired in the same instant. Then the
	// two start-level and deadline causes, and only then the signal
	// death, which under a timeout is merely how the cancel looks from
	// wait(2).
	if out.truncated {
		return nil, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "adapter stdout exceeded the 4 MiB cap; the child was cancelled",
			Details: map[string]string{spawnDetailReasonKey: spawnReasonOverflow},
		}
	}

	if runErr == nil {
		env, perr := parseAdapterStdout(out.buf.Bytes())
		if perr != nil {
			return nil, perr
		}
		return &SpawnResult{Envelope: env, ExitCode: cmd.ProcessState.ExitCode()}, nil
	}

	if errors.Is(runErr, exec.ErrWaitDelay) {
		// The child itself exited 0 but something (a grandchild holding
		// the pipe) kept stdout open until the forced closure. A valid
		// result that arrived anyway stands: logged, treated as success.
		env, perr := parseAdapterStdout(out.buf.Bytes())
		if perr != nil {
			return nil, perr
		}
		logger.Warn("adapter exited 0 but its stdout stayed open past the wait delay; result accepted",
			slog.String(spawnDetailReasonKey, spawnReasonWaitDelay))
		return &SpawnResult{Envelope: env, ExitCode: 0, WaitDelayExpired: true}, nil
	}

	if errors.Is(runErr, os.ErrNotExist) || errors.Is(runErr, exec.ErrNotFound) {
		return nil, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "adapter executable not found",
			Details: map[string]string{spawnDetailReasonKey: spawnReasonNotFound},
		}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		reason := spawnReasonCanceled
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			reason = spawnReasonTimeout
		}
		return nil, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "adapter did not finish within its deadline",
			Details: map[string]string{spawnDetailReasonKey: reason},
		}
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return nil, &protocol.Error{
				Code:    protocol.CodeUnavailable,
				Message: "adapter was killed by a signal",
				Details: map[string]string{spawnDetailSignalKey: signalName(ws.Signal())},
			}
		}
		// The child exited non-zero on its own: its envelope carries the
		// error and the caller keeps both halves.
		env, perr := parseAdapterStdout(out.buf.Bytes())
		if perr != nil {
			return nil, perr
		}
		return &SpawnResult{Envelope: env, ExitCode: exitErr.ExitCode()}, nil
	}

	// Pipe plumbing failures and other unclassified wait errors. The raw
	// error goes to the (redacting) logger, never into the envelope.
	logger.Error("adapter spawn failed", slog.String("error", runErr.Error()))
	return nil, &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: "adapter spawn failed",
		Details: map[string]string{spawnDetailReasonKey: spawnReasonSpawnError},
	}
}

// parseAdapterStdout parses a child's whole stdout as exactly one 4.3
// envelope. Failures map to `internal` (plan 4.6: non-JSON stdout is the
// harness's problem to report, not invalid_input, which describes OUR
// stdin), and the raw bytes are never echoed — a broken adapter's stdout
// can carry anything.
func parseAdapterStdout(stdout []byte) (*protocol.Envelope, *protocol.Error) {
	var env protocol.Envelope
	if err := protocol.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		return nil, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "adapter stdout is not a single protocol JSON envelope",
			Details: map[string]string{spawnDetailReasonKey: spawnReasonNotJSON},
		}
	}
	if err := env.Validate(); err != nil {
		return nil, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "adapter stdout is not a valid protocol envelope",
			Details: map[string]string{spawnDetailReasonKey: spawnReasonBadEnvelope},
		}
	}
	return &env, nil
}

// cappedStdout buffers a child's stdout up to limit and cancels the
// child's context on the first byte past it. The overflow is swallowed
// rather than surfaced as a write error so that classification stays with
// Spawn (a write error would come back as cmd.Wait's error and mask the
// cause), and the child is genuinely stopped — SIGTERM now, SIGKILL after
// WaitDelay — rather than left running against a full pipe.
//
// Write is called only from cmd's single stdout-copying goroutine, and
// cmd.Wait completes before Spawn reads buf/truncated, which establishes
// the happens-before edge.
type cappedStdout struct {
	limit     int
	cancel    context.CancelFunc
	buf       bytes.Buffer
	truncated bool
}

func (w *cappedStdout) Write(p []byte) (int, error) {
	if !w.truncated {
		if room := w.limit - w.buf.Len(); len(p) > room {
			w.buf.Write(p[:room])
			w.truncated = true
			w.cancel()
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// childEnvExact is the exact-name half of the 3.2 allow-list for adapter
// children; childEnvPrefixes is the family half (XDG_*, LC_*). Everything
// else — GODEBUG, GOFLAGS, NODE_OPTIONS, inherited BRIGADE_*,
// CLAUDE_CODE_MESSAGING_* and the rest of the caller's environment — is
// absent by construction: there is no deny-list to keep current.
var childEnvExact = map[string]bool{
	"PATH":              true,
	"HOME":              true,
	"TMPDIR":            true,
	"LANG":              true,
	"CLAUDE_CONFIG_DIR": true,
	"HTTP_PROXY":        true,
	"HTTPS_PROXY":       true,
	"NO_PROXY":          true,
	"http_proxy":        true,
	"https_proxy":       true,
	"no_proxy":          true,
	"SSL_CERT_FILE":     true,
	"SSL_CERT_DIR":      true,
}

var childEnvPrefixes = []string{"XDG_", "LC_"}

// ChildEnv builds an adapter child's environment from scratch (3.2): it
// keeps only the allow-listed variables of environ — PATH, HOME, TMPDIR,
// LANG, LC_*, XDG_*, CLAUDE_CONFIG_DIR, the proxy variables in both
// cases, and SSL_CERT_FILE/SSL_CERT_DIR — and appends computed last, so
// the harness-computed BRIGADE_PROFILE, BRIGADE_CONFIG_DIR,
// BRIGADE_STATE_DIR and BRIGADE_LOG_LEVEL always win (last occurrence
// wins, as [Getenv] reads and os/exec dedupes). An entry with an empty
// value is dropped, matching Getenv's empty-means-unset rule.
//
// environ is the parent's os.Environ() at an entry point, or a literal in
// tests; inherited BRIGADE_* never survives the filter, which is why a
// hostile repository's `env` block cannot steer a child spawned here.
func ChildEnv(environ []string, computed ...string) []string {
	out := make([]string, 0, len(environ)+len(computed))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		if childEnvExact[name] || hasChildEnvPrefix(name) {
			out = append(out, entry)
		}
	}
	return append(out, computed...)
}

func hasChildEnvPrefix(name string) bool {
	for _, prefix := range childEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// signalNames maps the signals an adapter child plausibly dies from to
// their conventional names for details.signal. The map is used instead of
// Signal.String(), whose "terminated"/"killed" prose is ambiguous across
// platforms, and instead of a switch, which the exhaustive linter would
// demand cover every signal the platform defines.
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL:  "SIGILL",
	syscall.SIGTRAP: "SIGTRAP",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGBUS:  "SIGBUS",
	syscall.SIGFPE:  "SIGFPE",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGUSR1: "SIGUSR1",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGTERM: "SIGTERM",
}

func signalName(sig syscall.Signal) string {
	if name, ok := signalNames[sig]; ok {
		return name
	}
	return "signal " + strconv.Itoa(int(sig))
}
