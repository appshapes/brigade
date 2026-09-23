package syncthing

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// Name is the adapter's name: the `sync.adapter` value of .brigade.json
// that selects it, and the prefix the harness puts on its peer descriptor
// on the wire (`syncthing:<device id>`, plan 4.3).
const Name = "syncthing"

// ProtocolVersion is the sync-adapter protocol this adapter speaks (plan
// 4.3). It is not the backend protocol's "1": the two protocols version
// independently.
const ProtocolVersion = "sync/1"

// progName prefixes the one human line a failure writes to stderr.
const progName = "brigade-sync-syncthing"

// The verbs of plan 4.3.
const (
	verbDescribe = "describe"
	verbAttach   = "attach"
	verbApply    = "apply"
	verbStatus   = "status"
	verbDetach   = "detach"
)

// The waits of plan 4.4. startWait bounds how long attach waits for a
// fresh daemon's config.xml and REST API (the harness gives the whole call
// 20 s); stopWait is how long detach waits for the daemon to exit after
// asking it to, and again after SIGTERM, before SIGKILL; restTimeout is
// every REST call's own bound; lockWait bounds a second attach or detach
// queued behind a start (a start holds the lock for at most startWait).
const (
	startWait   = 15 * time.Second
	stopWait    = 5 * time.Second
	restTimeout = 5 * time.Second
	lockWait    = 16 * time.Second
	pollEvery   = 100 * time.Millisecond
)

// deps are the seams a test replaces. Production values come from
// realDeps; there is deliberately no environment variable for any of them
// in shipped code.
type deps struct {
	// syncthing resolves the argv prefix that runs Syncthing:
	// [exec.LookPath("syncthing")] in production. A test hands
	// ["/bin/sh", <fixture script>] instead, because the repository's
	// fixture rule (CLAUDE.md) launches a script fixture through /bin/sh,
	// never exec'ing it directly.
	syncthing func() ([]string, error)
	// baseURL maps the instance's GUI port to its REST base URL:
	// http://127.0.0.1:<port> in production (loopback only); a test points
	// it at an httptest server.
	baseURL func(port int) string
	// startWait and stopWait default to the constants above; tests shorten
	// them.
	startWait time.Duration
	stopWait  time.Duration
}

func realDeps() deps {
	return deps{
		syncthing: func() ([]string, error) {
			path, err := exec.LookPath("syncthing")
			if err != nil {
				return nil, err
			}
			return []string{path}, nil
		},
		baseURL:   func(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) },
		startWait: startWait,
		stopWait:  stopWait,
	}
}

// Run executes one verb and returns its 4.6 exit status. It is the seam
// the `brigade sync-adapter syncthing` entry dispatches to: args are the
// words after the adapter name (the verb), and the streams and the
// environment are parameters so tests never touch the process's own.
// stdin must be the real os.Stdin from an entry point, for
// adapterkit.ReadInput's terminal refusal.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	return run(args, stdin, stdout, stderr, environ, realDeps())
}

// An adapter is one invocation.
type adapter struct {
	d       deps
	stdout  io.Writer
	stderr  io.Writer
	environ []string
	log     *slog.Logger
	http    *http.Client
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string, d deps) int {
	a := &adapter{
		d: d, stdout: stdout, stderr: stderr, environ: environ,
		log: slog.New(slog.DiscardHandler),
		// Proxy nil: the API is loopback-only, and an inherited
		// HTTP_PROXY (ChildEnv keeps the proxy variables for the backend
		// adapters) must never route it anywhere else.
		http: &http.Client{Timeout: restTimeout, Transport: &http.Transport{Proxy: nil}},
	}
	levelName := adapterkit.Getenv(environ, "BRIGADE_LOG_LEVEL")
	if levelName == "" {
		levelName = "info"
	}
	level, err := adapterlog.ParseLevel(levelName)
	if err != nil {
		return a.fail(&protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "BRIGADE_LOG_LEVEL must be one of: error, warn, info, debug",
			Details: map[string]string{"variable": "BRIGADE_LOG_LEVEL", "reason": "invalid_value"},
		})
	}
	a.log = adapterlog.New(stderr, level, nil).With(slog.String("comp", "sync-syncthing"))

	verb, err := parseVerb(args)
	if err != nil {
		return a.fail(err)
	}
	data, err := adapterkit.ReadInput(stdin)
	if err != nil {
		return a.fail(err)
	}
	var req request
	if err := protocol.Unmarshal(data, &req); err != nil {
		return a.fail(err)
	}
	result, err := a.execute(verb, &req)
	if err != nil {
		return a.fail(err)
	}
	return adapterkit.WriteResult(stdout, result)
}

// parseVerb takes exactly one verb. `--json` is tolerated anywhere: the
// `brigade` dispatcher honours it for every raw command, and this
// adapter's stdout is always the envelope anyway.
func parseVerb(args []string) (string, error) {
	var words []string
	for _, a := range args {
		if a != "--json" {
			words = append(words, a)
		}
	}
	if len(words) != 1 {
		return "", errUsage("expected exactly one verb: describe, attach, apply, status or detach")
	}
	switch words[0] {
	case verbDescribe, verbAttach, verbApply, verbStatus, verbDetach:
		return words[0], nil
	}
	return "", errUsage("unknown verb; expected describe, attach, apply, status or detach")
}

func (a *adapter) execute(verb string, req *request) (any, error) {
	if verb == verbDescribe {
		return describeResult{Name: Name, Version: buildinfo.String(), ProtocolVersion: ProtocolVersion}, nil
	}
	stateDir, err := a.stateDir(req.StateDir)
	if err != nil {
		return nil, err
	}
	inst := newInstance(stateDir, a)
	switch verb {
	case verbAttach:
		if err := checkSession(req); err != nil {
			return nil, err
		}
		return inst.attach(req.SessionID, req.PID)
	case verbApply:
		return inst.apply(req.Folders, req.Peers)
	case verbStatus:
		return inst.status()
	default: // verbDetach; parseVerb admits nothing else
		if err := checkSession(req); err != nil {
			return nil, err
		}
		return inst.detach(req.SessionID)
	}
}

// checkSession checks what attach and detach read: the session id, and
// the optional pid, which is absent (0) or a positive process id.
func checkSession(req *request) error {
	if err := checkSessionID(req.SessionID); err != nil {
		return err
	}
	if req.PID < 0 {
		return errInput("pid", "pid must be a positive process id when it is given")
	}
	return nil
}

// stateDir is the request's state_dir, else the environment's
// BRIGADE_STATE_DIR chain (the harness sets both). It must be absolute: the
// instance's home is under it, and a relative one would move with the
// working directory.
func (a *adapter) stateDir(fromRequest string) (string, error) {
	if fromRequest == "" {
		return adapterkit.StateDir(a.environ)
	}
	if !filepath.IsAbs(fromRequest) {
		return "", errInput("state_dir", "state_dir must be an absolute path")
	}
	return filepath.Clean(fromRequest), nil
}

// checkSessionID admits any id that is one path element, because it names
// the session's refs/<session_id> file and nothing more.
func checkSessionID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\x00") {
		return errInput("session_id", "session_id is required and must be usable as a file name")
	}
	return nil
}

// fail writes the failing envelope and one human line on stderr.
func (a *adapter) fail(err error) int {
	code := adapterkit.WriteError(a.stdout, err)
	message := "internal error"
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		message = string(perr.Code) + ": " + perr.Message
	} else {
		a.log.Debug("unclassified failure", adapterlog.Err(err))
	}
	_, _ = io.WriteString(a.stderr, progName+": "+message+"\n")
	return code
}

// The request of every verb (plan 4.3). One shape serves all five: the
// loose parse ignores what a verb does not read. PID is attach's and
// detach's optional "pid": the process that holds the session's
// reference (the harness sends its watcher's), kept in refs/<session_id>
// so a reference whose process died without detaching is pruned.
type request struct {
	StateDir  string       `json:"state_dir"`
	SessionID string       `json:"session_id"`
	PID       int          `json:"pid"`
	Folders   []folderSpec `json:"folders"`
	Peers     []peerSpec   `json:"peers"`
}

// A folderSpec is one folder apply shares: the id the harness derived
// ("brigade-<team>-<hash>"), the absolute path in this checkout, and a
// label for Syncthing's own UI.
type folderSpec struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Label string `json:"label"`
}

// A peerSpec is one teammate's descriptor (a Syncthing device id, the
// harness having stripped the "syncthing:" prefix) and a label.
type peerSpec struct {
	Peer  string `json:"peer"`
	Label string `json:"label"`
}

type describeResult struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocol_version"`
}

type attachResult struct {
	Peer string `json:"peer"`
}

type applyResult struct {
	Folders []folderState `json:"folders"`
	Peers   []peerState   `json:"peers"`
}

type statusResult struct {
	Running bool          `json:"running"`
	Peer    string        `json:"peer"`
	Folders []folderState `json:"folders"`
	Peers   []peerState   `json:"peers"`
}

type detachResult struct {
	Stopped bool `json:"stopped"`
}

// A folderState is one folder of apply's result ({id, state}) or of
// status's ({id, path, state}). State is Syncthing's own folder state
// ("idle", "scanning", "syncing", …), or one of this adapter's: "unknown"
// (Syncthing did not answer for it), "rejected" (Syncthing refused the
// folder object), "conflict_path" (the folder id is held at another path
// — the second clone of a repo on one machine — or another folder id
// holds the path, plan 3.2).
type folderState struct {
	ID    string `json:"id"`
	Path  string `json:"path,omitzero"`
	State string `json:"state"`
}

type peerState struct {
	Peer      string `json:"peer"`
	Connected bool   `json:"connected"`
}

func errUsage(message string) error {
	return &protocol.Error{Code: protocol.CodeUsage, Message: message}
}

func errInput(field, message string) error {
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: message,
		Details: map[string]string{"field": field, "reason": "invalid_value"},
	}
}

// errUnavailable is the 4.6 `unavailable` refusal with a reason the
// harness can turn into its one session line.
func errUnavailable(reason, message string) error {
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: message,
		Details: map[string]string{"reason": reason},
	}
}
