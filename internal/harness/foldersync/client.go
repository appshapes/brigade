package foldersync

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The details.reason values this package's own failures carry. A failure
// adapterkit.Spawn classifies keeps Spawn's reason (adapter_not_found,
// timeout, …); a failing envelope the adapter wrote keeps the adapter's.
const (
	// ReasonNotOnPath: no executable brigade-sync-<name> on PATH.
	ReasonNotOnPath = "sync_adapter_not_on_path"
	// ReasonInvalidName: the adapter name breaks the team file's
	// adapter-name rule (a map that was not the hook's).
	ReasonInvalidName = "sync_adapter_invalid_name"
	// ReasonResultInvalid: the adapter's result is not the verb's shape.
	ReasonResultInvalid = "sync_result_invalid"
	// ReasonProtocolMismatch: `describe` names another sync protocol.
	ReasonProtocolMismatch = "sync_protocol_mismatch"
)

// adapterName is the team file's rule for an adapter NAME
// (teamfile.SyncConfig; folder-sync plan §4.1), repeated here because a
// name reaches this package from the by-pid map as well as the file.
var adapterName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// A SpawnFunc has adapterkit.Spawn's shape; a test injects a recorder.
type SpawnFunc func(context.Context, adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error)

// A Client runs one sync adapter's verbs for one Brigade state directory.
// Set Adapter, StateDir and Environ at least.
type Client struct {
	// Adapter is the NAME the team file gave (the by-pid map's
	// sync_adapter): BundledAdapter, or the suffix of an executable
	// brigade-sync-<name> on PATH. Never a path.
	Adapter string
	// PluginBin is the plugin's `brigade` executable (the by-pid map's
	// plugin_bin), the bundled adapter's argv[0]; "" falls back to this
	// executable, as the bundled backend adapter does (adapterclient).
	PluginBin string
	// StateDir is Brigade's state directory: the child's
	// BRIGADE_STATE_DIR and every request's state_dir, and where the
	// child's stderr is logged (logs/sync-<name>.log).
	StateDir string
	// Environ is the parent environment adapterkit.ChildEnv filters, and
	// the PATH an external adapter is searched on.
	Environ []string
	// Logger receives scalar diagnostics; nil discards them.
	Logger *slog.Logger
	// Command, when set, replaces the resolved argv prefix. Production
	// never sets it; a test points it at `/bin/sh <fixture script>` (the
	// repository's rule for a script fixture: read, never exec'd).
	Command []string
	// Spawn is the spawn seam; nil means adapterkit.Spawn.
	Spawn SpawnFunc
}

// Argv resolves the adapter's argv prefix (§4.3): the bundled adapter is
// [<plugin binary>, "sync-adapter", "syncthing"]; any other name is the
// absolute path of brigade-sync-<name> found on Environ's PATH. The verb
// is appended per call. Nothing is ever interpreted by a shell.
func (c *Client) Argv() ([]string, error) {
	if len(c.Command) > 0 {
		return append([]string{}, c.Command...), nil
	}
	if !adapterName.MatchString(c.Adapter) {
		return nil, &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "the sync adapter name is not a valid adapter name",
			Details: map[string]string{"reason": ReasonInvalidName},
		}
	}
	if c.Adapter == BundledAdapter {
		bin := c.PluginBin
		if bin == "" {
			self, err := os.Executable()
			if err != nil {
				return nil, &protocol.Error{
					Code:    protocol.CodeInternal,
					Message: "cannot resolve this executable for the bundled sync adapter",
					Details: map[string]string{"reason": "executable_unresolved"},
				}
			}
			bin = self
		}
		return []string{bin, "sync-adapter", BundledAdapter}, nil
	}
	if p, ok := c.lookPath(ExternalPrefix + c.Adapter); ok {
		return []string{p}, nil
	}
	return nil, &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: ExternalPrefix + c.Adapter + " is not on PATH",
		Details: map[string]string{"reason": ReasonNotOnPath},
	}
}

// lookPath searches Environ's PATH — the environment this Client was
// handed, not the process's — for an executable name, and returns its
// absolute path. A relative PATH entry is skipped: a child is never
// spawned from a path relative to whatever the cwd happens to be.
func (c *Client) lookPath(name string) (string, bool) {
	for _, dir := range filepath.SplitList(adapterkit.Getenv(c.Environ, "PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if p, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return p, true
		}
	}
	return "", false
}

// IsNotFound reports whether err says the adapter's executable is absent:
// an external one not on PATH, or a bundled one whose binary is gone.
func IsNotFound(err error) bool {
	perr, ok := asError(err)
	if !ok {
		return false
	}
	switch perr.Details["reason"] {
	case ReasonNotOnPath, "adapter_not_found":
		return true
	}
	return false
}

// Describe runs `describe` and checks the sync protocol version.
func (c *Client) Describe(ctx context.Context) (*DescribeResult, error) {
	var out DescribeResult
	if err := c.call(ctx, VerbDescribe, struct{}{}, &out); err != nil {
		return nil, err
	}
	if out.ProtocolVersion != ProtocolVersion {
		return nil, &protocol.Error{
			Code:    protocol.CodeProtocolMismatch,
			Message: "the sync adapter speaks another sync protocol than " + ProtocolVersion,
			Details: map[string]string{"reason": ReasonProtocolMismatch},
		}
	}
	return &out, nil
}

// Attach runs `attach` for sessionID. The descriptor must be non-empty
// and short enough that <adapter>:<descriptor> fits sync_peer's wire cap.
func (c *Client) Attach(ctx context.Context, sessionID string) (*AttachResult, error) {
	var out AttachResult
	if err := c.call(ctx, VerbAttach, SessionRequest{StateDir: c.StateDir, SessionID: sessionID}, &out); err != nil {
		return nil, err
	}
	wire := WirePeer(c.Adapter, out.Peer)
	if out.Peer == "" || protocol.TruncateRunes(wire, protocol.MaxSyncPeerChars) != wire {
		return nil, resultInvalid(VerbAttach)
	}
	return &out, nil
}

// Apply runs `apply` with the whole desired set.
func (c *Client) Apply(ctx context.Context, sessionID string, folders []Folder, peers []Peer) (*ApplyResult, error) {
	if folders == nil {
		folders = []Folder{}
	}
	if peers == nil {
		peers = []Peer{}
	}
	var out ApplyResult
	req := ApplyRequest{StateDir: c.StateDir, SessionID: sessionID, Folders: folders, Peers: peers}
	if err := c.call(ctx, VerbApply, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status runs `status`.
func (c *Client) Status(ctx context.Context) (*StatusResult, error) {
	var out StatusResult
	if err := c.call(ctx, VerbStatus, StatusRequest{StateDir: c.StateDir}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Detach runs `detach` for sessionID.
func (c *Client) Detach(ctx context.Context, sessionID string) (*DetachResult, error) {
	var out DetachResult
	if err := c.call(ctx, VerbDetach, SessionRequest{StateDir: c.StateDir, SessionID: sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// call runs one verb under CallTimeout (or ctx's earlier deadline): the
// request as the one stdin document, the child's stderr to the sync log,
// the result decoded into out. A failing envelope the adapter wrote comes
// back as its *protocol.Error; a failure the adapter did not produce as
// adapterkit.Spawn's classification. Neither carries a result body, so a
// caller logging the error logs no adapter data.
func (c *Client) call(ctx context.Context, verb string, req, out any) error {
	argv, err := c.Argv()
	if err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "cannot encode the sync adapter request", Details: map[string]string{"reason": "encode_stdin"}}
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	logFile := c.adapterLog()
	var stderr io.Writer
	if logFile != nil {
		stderr = logFile
		defer func() { _ = logFile.Close() }()
	}
	spawn := c.Spawn
	if spawn == nil {
		spawn = adapterkit.Spawn
	}
	res, err := spawn(ctx, adapterkit.SpawnSpec{
		Argv:   append(argv, verb),
		Env:    adapterkit.ChildEnv(c.Environ, "BRIGADE_STATE_DIR="+c.StateDir),
		Stdin:  body,
		Stderr: stderr,
		Logger: c.logger(),
	})
	if err != nil {
		return err
	}
	if !res.Envelope.OK {
		return errorFromEnvelope(res.Envelope.Error)
	}
	if err := json.Unmarshal(res.Envelope.Result, out); err != nil {
		return resultInvalid(verb)
	}
	return nil
}

// adapterLog opens the append-mode 0600 log for the child's stderr, or
// nil when it cannot be opened: the stderr is then discarded, never sent
// to a session-bound command's own stderr.
func (c *Client) adapterLog() io.WriteCloser {
	if !filepath.IsAbs(c.StateDir) || !adapterName.MatchString(c.Adapter) {
		return nil
	}
	dir := filepath.Join(c.StateDir, "logs")
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "sync-"+c.Adapter+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600, path from BRIGADE_STATE_DIR by design
	if err != nil {
		return nil
	}
	return f
}

func (c *Client) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// errorFromEnvelope turns a failing wire error into a *protocol.Error,
// as adapterclient does for a backend adapter: an unknown code is
// `internal`, and retryability is the code's, not the wire flag's.
func errorFromEnvelope(obj *protocol.ErrorObject) *protocol.Error {
	if obj == nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "internal error"}
	}
	code := obj.Code
	if code.Exit() == protocol.CodeInternal.Exit() && code != protocol.CodeInternal {
		code = protocol.CodeInternal
	}
	return &protocol.Error{Code: code, Message: obj.Message, RetryAfterMS: obj.RetryAfterMS, Details: obj.Details}
}

// resultInvalid is the failure for a result that is not the verb's shape.
func resultInvalid(verb string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: "the sync adapter's " + verb + " result is not valid",
		Details: map[string]string{"reason": ReasonResultInvalid},
	}
}

// asError finds the *protocol.Error in err's chain.
func asError(err error) (*protocol.Error, bool) {
	var perr *protocol.Error
	return perr, errors.As(err, &perr) && perr != nil
}
