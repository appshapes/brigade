// Package adapterclient is the one place the harness spawns an adapter
// child (plan 3.4, 4.1, 6.6; brief section 2.2). Every request/response
// command goes through [Client.Call] and [adapterkit.Spawn]; the one
// long-running `message watch` child goes through [Client.StartWatch] and
// the single exec.CommandContext in spawn.go. No other harness package
// spawns an adapter.
//
// The child environment is built FROM SCRATCH with [adapterkit.ChildEnv]:
// inherited BRIGADE_* and CLAUDE_CODE_MESSAGING_* never reach the child,
// only the allow-listed variables (PATH, HOME, TMPDIR, the proxy
// variables, SSL_CERT_*, XDG_*, CLAUDE_CONFIG_DIR) plus the four computed
// BRIGADE_PROFILE/CONFIG_DIR/STATE_DIR/LOG_LEVEL do. The child's stderr is
// captured to the adapter log, never surfaced, so a returned error carries
// only the adapter's own safe envelope message or [adapterkit.Spawn]'s
// fixed classification — never raw adapter stderr (U-24).
//
// A failing envelope the adapter PRODUCED is returned as a *protocol.Error
// alongside the envelope; an error the adapter did NOT produce (a timeout,
// a signal death, a missing executable, a runaway or unparseable stdout)
// is returned by Spawn with a nil envelope, so a caller can tell "the
// adapter said" from "the adapter broke" (4.6).
package adapterclient

import (
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
)

// The 4.1 timeout budgets, exposed so callers wrap ctx with them; Call
// itself honours whatever deadline ctx already carries.
const (
	// DefaultTimeout is the 20 s budget for an ordinary request/response
	// command.
	DefaultTimeout = 20 * time.Second
	// RegisterTimeout is the 8 s budget for `session register` at
	// SessionStart.
	RegisterTimeout = 8 * time.Second
	// CloseTimeout is the 1 s budget for `session close` at SessionEnd.
	CloseTimeout = 1 * time.Second
	// WatchRequestTimeout is the 3 s budget for a request the watcher
	// makes.
	WatchRequestTimeout = 3 * time.Second
	// DescribeTimeout is the 3 s cap on the cached describe probe.
	DescribeTimeout = 3 * time.Second
)

// bundledAdapterArgv is what an empty (bundled) [config.Adapter] resolves
// to at spawn time: this executable, `adapter supabase`. It is resolved
// through os.Executable() and never stored, so a development setup running
// different cached binaries is not pinned to one (D36).
func bundledAdapterArgv() ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "cannot resolve this executable for the bundled adapter",
			Details: map[string]string{"reason": "executable_unresolved"},
		}
	}
	return []string{self, "adapter", "supabase"}, nil
}

// A Client spawns one adapter's children for one profile. The zero value
// is not usable; set Adapter, Profile, ConfigDir and StateDir at least.
type Client struct {
	// Adapter is the resolved argv prefix (config.ResolveAdapter's result).
	Adapter config.Adapter
	// Profile, ConfigDir and StateDir are the computed BRIGADE_* values the
	// child's environment carries.
	Profile   string
	ConfigDir string
	StateDir  string
	// LogLevel is the child's BRIGADE_LOG_LEVEL; "" lets the adapter
	// default it.
	LogLevel string
	// Environ is the parent environment ChildEnv filters. Inherited
	// BRIGADE_* and CLAUDE_CODE_MESSAGING_* are dropped from it.
	Environ []string
	// Logger receives this package's scalar diagnostics; nil discards them.
	Logger *slog.Logger
	// Spawn is the request/response spawn seam: nil means
	// [adapterkit.Spawn], and production never sets it. A caller's test
	// injects a recorder here to assert exactly what every child would
	// have received — argv, environment, stdin — or that no child was
	// spawned at all (U-25), without starting a process. StartWatch is not
	// routed through it: the watch child is a real process by nature and
	// the fake adapter's dump file is its recorder.
	Spawn SpawnFunc
}

// A SpawnFunc has [adapterkit.Spawn]'s shape.
type SpawnFunc func(context.Context, adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error)

// spawn returns the request/response spawn function in use.
func (c *Client) spawn() SpawnFunc {
	if c.Spawn != nil {
		return c.Spawn
	}
	return adapterkit.Spawn
}

// logger returns a non-nil logger.
func (c *Client) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// baseArgv is the adapter's argv prefix: config.Adapter.Argv, or the
// bundled adapter's resolved argv.
func (c *Client) baseArgv() ([]string, error) {
	if !c.Adapter.Bundled && len(c.Adapter.Argv) > 0 {
		return append([]string{}, c.Adapter.Argv...), nil
	}
	return bundledAdapterArgv()
}

// argv builds the full child argv:
//
//	<adapter prefix> --profile <p> <group> [verb] [flags...]
func (c *Client) argv(group, verb string, flags []string) ([]string, error) {
	base, err := c.baseArgv()
	if err != nil {
		return nil, err
	}
	argv := make([]string, 0, len(base)+4+len(flags))
	argv = append(argv, base...)
	argv = append(argv, "--profile", c.Profile, group)
	if verb != "" {
		argv = append(argv, verb)
	}
	argv = append(argv, flags...)
	return argv, nil
}

// childEnv builds the from-scratch child environment.
func (c *Client) childEnv() []string {
	return adapterkit.ChildEnv(c.Environ,
		"BRIGADE_PROFILE="+c.Profile,
		"BRIGADE_CONFIG_DIR="+c.ConfigDir,
		"BRIGADE_STATE_DIR="+c.StateDir,
		"BRIGADE_LOG_LEVEL="+c.LogLevel,
	)
}

// adapterLog opens the append-mode 0600 adapter log for the child's
// stderr, or returns nil when it cannot be opened (a read-only HOME under
// the sandbox, 6.12): the child's stderr is discarded rather than sent to
// the process stderr of a session-bound command.
func (c *Client) adapterLog() io.WriteCloser {
	dir := filepath.Join(c.StateDir, "logs")
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		return nil
	}
	path := filepath.Join(dir, "adapter-"+c.Profile+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600, path from BRIGADE_STATE_DIR by design
	if err != nil {
		return nil
	}
	return f
}

// Call runs one request/response adapter command and returns its envelope.
//
//   - group/verb name the command (verb is "" for `describe`); flags are
//     the argv flags (e.g. --session, --include-offline); stdin is the JSON
//     input document, or nil for a command that takes none.
//   - On a successful spawn that produced an OK envelope: (envelope, nil).
//   - On a failing envelope the adapter PRODUCED: (envelope, *protocol.Error)
//     — the code, message, retry_after_ms and details from the wire, with
//     an unrecognised code normalised to `internal`; retryability is the
//     caller's to recompute from the code (4.3).
//   - On an error the adapter did NOT produce (a timeout, a signal death, a
//     missing executable, a runaway or unparseable stdout): (nil, err) as
//     [adapterkit.Spawn] returns it.
//
// The deadline is ctx's: wrap it with one of the package timeout constants.
func (c *Client) Call(ctx context.Context, group, verb string, flags []string, stdin any) (*protocol.Envelope, error) {
	argv, err := c.argv(group, verb, flags)
	if err != nil {
		return nil, err
	}
	body, err := marshalStdin(stdin)
	if err != nil {
		return nil, err
	}
	logWriter := c.adapterLog()
	var stderr io.Writer
	if logWriter != nil {
		stderr = logWriter
		defer func() { _ = logWriter.Close() }()
	}

	res, spawnErr := c.spawn()(ctx, adapterkit.SpawnSpec{
		Argv:   argv,
		Env:    c.childEnv(),
		Stdin:  body,
		Stderr: stderr,
		Logger: c.logger(),
	})
	if spawnErr != nil {
		return nil, spawnErr
	}
	env := res.Envelope
	if !env.OK {
		return env, errorFromEnvelope(env.Error)
	}
	return env, nil
}

// marshalStdin renders the input document, or nil for a command that takes
// none.
func marshalStdin(stdin any) ([]byte, error) {
	if stdin == nil {
		return nil, nil
	}
	body, err := json.Marshal(stdin)
	if err != nil {
		return nil, &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "cannot encode the adapter input document",
			Details: map[string]string{"reason": "encode_stdin"},
		}
	}
	return body, nil
}

// knownCodes is the twelve-code 4.6 taxonomy. A code outside it is
// normalised to `internal`, so an adapter that invents one cannot turn a
// failure into an unmapped exit status.
var knownCodes = map[protocol.Code]bool{
	protocol.CodeInternal: true, protocol.CodeUsage: true, protocol.CodeInvalidInput: true,
	protocol.CodeUnauthenticated: true, protocol.CodeUnauthorized: true, protocol.CodeNotFound: true,
	protocol.CodeConflict: true, protocol.CodeRateLimited: true, protocol.CodeUnavailable: true,
	protocol.CodeProtocolMismatch: true, protocol.CodeConfig: true, protocol.CodeLoopDetected: true,
}

// errorFromEnvelope turns a failing wire error into a *protocol.Error. The
// adapter's own retryable flag is deliberately dropped: retryability is
// recomputed from the code by protocol.Error.Object() (4.3, "consumers
// SHOULD derive retryability from code"). An unrecognised code is
// `internal`.
func errorFromEnvelope(obj *protocol.ErrorObject) *protocol.Error {
	if obj == nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"}
	}
	code := obj.Code
	if !knownCodes[code] {
		code = protocol.CodeInternal
	}
	return &protocol.Error{
		Code:         code,
		Message:      obj.Message,
		RetryAfterMS: obj.RetryAfterMS,
		Details:      obj.Details,
	}
}

// --- describe cache and protocol check -------------------------------------

// describeCache holds one validated DescribeResult per adapter argv within
// the process (brief 2.2): a command runs `describe` at most once, and the
// capability and protocol-version checks read the cached result.
var describeCache = newDescribeStore()

// A describeStore is the process-wide describe cache, keyed by (adapter
// argv, profile). It is safe for concurrent use.
type describeStore struct {
	mu sync.Mutex
	m  map[string]*protocol.DescribeResult
}

func newDescribeStore() *describeStore {
	return &describeStore{m: map[string]*protocol.DescribeResult{}}
}

func (s *describeStore) get(key string) (*protocol.DescribeResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.m[key]
	return d, ok
}

func (s *describeStore) put(key string, d *protocol.DescribeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = d
}

// Describe returns the adapter's describe result, cached per adapter argv,
// and performs the protocol check: a describe whose protocol_version is not
// this harness's is protocol_mismatch (exit 10), carrying the adapter's own
// name and version in details for `whoami`. The cached result serves the
// capability checks P3-3/P3-5 make.
//
// The deadline is ctx's, like [Client.Call]: a caller wraps it with
// [DescribeTimeout] (the 3 s describe cap of 4.1). Deferring the cap to the
// caller keeps the one describe per command bounded in production without
// making a loaded test's spawn race a 3 s stopwatch.
func (c *Client) Describe(ctx context.Context) (*protocol.DescribeResult, error) {
	key, err := c.describeKey()
	if err != nil {
		return nil, err
	}
	if d, ok := describeCache.get(key); ok {
		return d, protocolCheck(d)
	}
	env, err := c.Call(ctx, "describe", "", nil, nil)
	if err != nil {
		return nil, err
	}
	var d protocol.DescribeResult
	if derr := protocol.Decode([]byte(env.Result), &d); derr != nil {
		return nil, describeDecodeError()
	}
	describeCache.put(key, &d)
	return &d, protocolCheck(&d)
}

// describeKey identifies the (adapter, profile) whose describe is cached.
func (c *Client) describeKey() (string, error) {
	base, err := c.baseArgv()
	if err != nil {
		return "", err
	}
	return strings.Join(append(base, c.Profile), "\x00"), nil
}

// protocolCheck maps a describe with the wrong protocol major to
// protocol_mismatch, naming the adapter for whoami.
func protocolCheck(d *protocol.DescribeResult) error {
	if d.ProtocolVersion == protocol.ProtocolVersion {
		return nil
	}
	return &protocol.Error{
		Code:    protocol.CodeProtocolMismatch,
		Message: "the adapter speaks a different protocol version than this harness",
		Details: map[string]string{
			"adapter_name":             d.Adapter.Name,
			"adapter_version":          d.Adapter.Version,
			"adapter_protocol_version": d.ProtocolVersion,
			"harness_protocol_version": protocol.ProtocolVersion,
		},
	}
}

// describeDecodeError is the failure when a describe envelope's result is
// not a valid DescribeResult. It is `internal`: the adapter broke its own
// output.
func describeDecodeError() error {
	return &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: "the adapter's describe result is not a valid document",
		Details: map[string]string{"reason": "describe_invalid"},
	}
}
