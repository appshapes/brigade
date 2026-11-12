// Package stub is a stand-in Brigade backend for tests: an http.Handler
// that implements the backend REST contract (docs/backend-rest-v1.md) by
// running the filesystem adapter (internal/adapters/fs) in-process for
// every request.
//
// It is a TEST FIXTURE. It is INSECURE and it is NEVER SHIPPED: the store
// behind it is the fs adapter's, one directory tree with no access control
// beyond file modes, its credentials live in memory and die with the
// process, and it serves plain HTTP. It exists so the bundled `rest`
// adapter's tests and its conformance run have a server to talk to, with
// no Docker and no local stack. cmd/brigade-rest-stub is the development
// binary around it.
//
// How it maps the contract onto the fs adapter:
//
//   - A principal is one private configuration directory, which the fs
//     adapter sees as one profile; every principal shares one store root.
//     A credential is 32 random bytes; the stub keeps the credential's
//     SHA-256 and the directory it names, nothing else.
//   - A request is one fs command run with fs.Run: the request body (or
//     the member of it that is the command's input) on stdin, the session
//     as `--session`, the principal's directory in the environment. The
//     command's envelope is the response's.
//   - What the fs adapter answers about its own files is translated to
//     what a server would say: a profile bound to no team is the uniform
//     `unauthorized` of 4.5.7, and `usage` and `config`, which a server
//     never answers, are `internal`.
//   - `team_ref` in a request is checked against the team the principal's
//     profile is bound to; a mismatch is the same uniform `unauthorized`.
//   - `after`, `cursor` and `wait_seconds` are the stub's own (receive.go):
//     it remembers, per session, the order in which it first saw each
//     pending message id, and polls `message receive` while a request
//     waits.
package stub

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/adapters/fs"
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// serverName is the `server.name` of /v1/info.
const serverName = "brigade-rest-stub"

// contractVersion is the contract this stub implements.
const contractVersion = "1"

// maxBodyBytes is the contract's cap on a request body.
const maxBodyBytes = 1 << 20

// createTokenHeader carries the creation token of the contract's section 3.
const createTokenHeader = "Brigade-Create-Token" //nolint:gosec // G101: a header name, not a credential

// defaultPoll is how often a waiting receive looks at the inbox again. The
// contract gives a waiting request one second to answer after an
// acceptance; a tenth of that leaves room for a loaded test machine.
const defaultPoll = 100 * time.Millisecond

// Options configure a Server. The zero value is a server anyone may create
// a team on.
type Options struct {
	// CreateToken, when not empty, is the creation token /v1/team/create
	// requires in the Brigade-Create-Token header.
	CreateToken string
	// Poll is how often a waiting receive polls the inbox; zero means
	// defaultPoll.
	Poll time.Duration
}

// A Server is one stand-in backend over one directory.
type Server struct {
	dir         string
	store       string
	poll        time.Duration
	createToken string
	nonce       string

	mu         sync.Mutex
	principals map[[sha256.Size]byte]*principal
	created    int
	inboxes    map[string]*inbox
}

// A principal is the server's record of one member's installation: the
// environment that points the fs adapter at its private directory. mu
// makes the fs commands run for one principal take turns, because each
// reads and rewrites that directory's two files.
type principal struct {
	mu  sync.Mutex
	dir string
	env []string
}

// New returns a server that keeps everything under dir, which must exist
// and should be empty and private to the caller: the store under
// dir/store and one directory per principal under dir/principals.
func New(dir string, o Options) (*Server, error) {
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	poll := o.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	return &Server{
		dir:         dir,
		store:       filepath.Join(dir, "store"),
		poll:        poll,
		createToken: o.CreateToken,
		nonce:       hex.EncodeToString(seed[:]),
		principals:  map[[sha256.Size]byte]*principal{},
		inboxes:     map[string]*inbox{},
	}, nil
}

// A call is one request being answered.
type call struct {
	r    *http.Request
	body []byte
	doc  map[string]jsontext.Value
	key  [sha256.Size]byte
	who  *principal // nil when the request carries no known credential
}

// An endpoint is one path of the contract's section 2.
type endpoint struct {
	// open endpoints run without a known credential: /v1/info, and the two
	// that create a principal.
	open bool
	run  func(*Server, *call) (any, *protocol.Error)
}

// endpoints is the contract's section 2.
var endpoints = map[string]endpoint{
	"/v1/info":              {open: true, run: (*Server).info},
	"/v1/team/create":       {open: true, run: (*Server).teamCreate},
	"/v1/team/join":         {open: true, run: (*Server).teamJoin},
	"/v1/team/leave":        {run: (*Server).teamLeave},
	"/v1/team/members":      {run: (*Server).teamMembers},
	"/v1/session/register":  {run: (*Server).sessionRegister},
	"/v1/session/heartbeat": {run: (*Server).sessionHeartbeat},
	"/v1/session/list":      {run: (*Server).sessionList},
	"/v1/session/close":     {run: (*Server).sessionClose},
	"/v1/message/send":      {run: (*Server).messageSend},
	"/v1/message/receive":   {run: (*Server).messageReceive},
	"/v1/message/ack":       {run: (*Server).messageAck},
	"/v1/credential/revoke": {run: (*Server).credentialRevoke},
}

// ServeHTTP answers one request with one result envelope and the status
// the contract's section 1 pairs with it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ep, known := endpoints[r.URL.Path]
	if !known {
		writeError(w, &protocol.Error{Code: protocol.CodeNotFound, Message: "no such endpoint"})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, invalid("method", "invalid_value", "every request of this contract is a POST"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, invalid("body", "too_long", "the request body could not be read or exceeds 1 MiB"))
		return
	}
	c := &call{r: r, body: body}
	if protocol.Unmarshal(body, &c.doc) != nil || c.doc == nil {
		writeError(w, invalid("body", "malformed_json", "the request body is not one JSON object"))
		return
	}
	if presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && presented != "" {
		c.key = sha256.Sum256([]byte(presented))
		c.who = s.lookup(c.key)
	}
	if !ep.open && c.who == nil {
		writeError(w, unknownCredential())
		return
	}
	result, perr := ep.run(s, c)
	if perr != nil {
		writeError(w, perr)
		return
	}
	writeResult(w, result)
}

// ---- the endpoints ----

func (s *Server) info(*call) (any, *protocol.Error) {
	return map[string]any{
		"contract_version": contractVersion,
		"server":           map[string]string{"name": serverName, "version": buildinfo.String()},
	}, nil
}

// teamCreate creates a team as the caller: the principal a known
// credential names, else a new principal whose credential rides the
// result. A create that fails for a new principal leaves nothing behind.
func (s *Server) teamCreate(c *call) (any, *protocol.Error) {
	if s.createToken != "" {
		presented := c.r.Header.Get(createTokenHeader)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(s.createToken)) != 1 {
			return nil, &protocol.Error{Code: protocol.CodeUnauthorized, Message: "this server does not let this caller create a team"}
		}
	}
	req := &protocol.TeamCreateRequest{}
	if perr := decode(c.body, req); perr != nil {
		return nil, perr
	}
	return s.bind(c, &protocol.TeamCreateRequest{TeamName: req.TeamName, HumanLabel: req.HumanLabel}, "team", "create")
}

// teamJoin joins a team as the caller, as teamCreate creates one. A
// rejected secret is the fs adapter's one fixed `unauthorized`, and a
// principal that would have been new is discarded.
func (s *Server) teamJoin(c *call) (any, *protocol.Error) {
	req := &protocol.TeamJoinRequest{}
	if perr := decode(c.body, req); perr != nil {
		return nil, perr
	}
	return s.bind(c, &protocol.TeamJoinRequest{JoinSecret: req.JoinSecret, HumanLabel: req.HumanLabel}, "team", "join")
}

// bind runs a create or a join for the call's principal, or for a new one
// that exists only if the command succeeds, and adds the new principal's
// credential to the result.
func (s *Server) bind(c *call, input any, args ...string) (any, *protocol.Error) {
	who, fresh := c.who, false
	if who == nil {
		who, fresh = s.newPrincipal(), true
	}
	stdin, err := json.Marshal(input)
	if err != nil {
		return nil, internal()
	}
	result, perr := s.fs(who, stdin, args...)
	if perr != nil {
		if fresh {
			_ = os.RemoveAll(who.dir)
		}
		return nil, perr
	}
	if !fresh {
		return result, nil
	}
	members := map[string]jsontext.Value{}
	if protocol.Unmarshal(result, &members) != nil {
		return nil, internal()
	}
	issued, err := s.issue(who)
	if err != nil {
		return nil, internal()
	}
	members["credential"] = jsontext.Value(strconv.Quote(issued))
	return members, nil
}

// teamLeave revokes the caller's membership of the team the request
// names. Leaving a team the caller is not an active member of is the
// success it always is (4.4.10: idempotent), and changes nothing.
func (s *Server) teamLeave(c *call) (any, *protocol.Error) {
	teamRef, perr := text(c.doc, "team_ref", true)
	if perr != nil {
		return nil, perr
	}
	status, perr := s.status(c.who)
	if perr != nil {
		return nil, perr
	}
	if status.TeamRef != teamRef {
		return &protocol.TeamLeaveResult{TeamRef: teamRef, PrincipalRef: status.PrincipalRef, Left: true}, nil
	}
	return s.fs(c.who, nil, "team", "leave")
}

func (s *Server) teamMembers(c *call) (any, *protocol.Error) {
	if perr := s.inTeam(c); perr != nil {
		return nil, perr
	}
	return s.fs(c.who, nil, "team", "members")
}

// sessionRegister registers a session in the team the request names. The
// body is the registration with `team_ref` beside it; the fs adapter reads
// the registration and ignores the member it does not know.
func (s *Server) sessionRegister(c *call) (any, *protocol.Error) {
	if perr := s.inTeam(c); perr != nil {
		return nil, perr
	}
	if perr := leaseInRange(c.body); perr != nil {
		return nil, perr
	}
	return s.fs(c.who, c.body, "session", "register")
}

func (s *Server) sessionHeartbeat(c *call) (any, *protocol.Error) {
	id, perr := text(c.doc, "session_id", true)
	if perr != nil {
		return nil, perr
	}
	heartbeat := c.doc["heartbeat"]
	if len(heartbeat) == 0 || heartbeat.Kind() != '{' {
		return nil, invalid("heartbeat", "required", "heartbeat must be an object")
	}
	if perr := leaseInRange(heartbeat); perr != nil {
		return nil, perr
	}
	return s.fs(c.who, heartbeat, "session", "heartbeat", "--session="+id)
}

func (s *Server) sessionList(c *call) (any, *protocol.Error) {
	if perr := s.inTeam(c); perr != nil {
		return nil, perr
	}
	id, perr := text(c.doc, "session_id", false)
	if perr != nil {
		return nil, perr
	}
	args := []string{"session", "list"}
	if id != "" {
		args = append(args, "--session="+id)
	}
	var includeOffline bool
	if raw, present := c.doc["include_offline"]; present {
		if json.Unmarshal(raw, &includeOffline) != nil {
			return nil, invalid("include_offline", "invalid_value", "include_offline must be a boolean")
		}
	}
	if includeOffline {
		args = append(args, "--include-offline")
	}
	return s.fs(c.who, nil, args...)
}

func (s *Server) sessionClose(c *call) (any, *protocol.Error) {
	id, perr := text(c.doc, "session_id", true)
	if perr != nil {
		return nil, perr
	}
	return s.fs(c.who, nil, "session", "close", "--session="+id)
}

// messageSend hands the body to the fs adapter as it arrived, so the
// forbidden members of 4.4.6 are refused there and every other unknown
// member is ignored.
func (s *Server) messageSend(c *call) (any, *protocol.Error) {
	return s.fs(c.who, c.body, "message", "send")
}

func (s *Server) messageAck(c *call) (any, *protocol.Error) {
	id, perr := text(c.doc, "session_id", true)
	if perr != nil {
		return nil, perr
	}
	stdin, err := json.Marshal(map[string]jsontext.Value{"message_ids": orNull(c.doc["message_ids"])})
	if err != nil {
		return nil, internal()
	}
	return s.fs(c.who, stdin, "message", "ack", "--session="+id)
}

// credentialRevoke makes the presented credential unknown. The
// principal's directory, and so its membership, stays as it is.
func (s *Server) credentialRevoke(c *call) (any, *protocol.Error) {
	s.mu.Lock()
	delete(s.principals, c.key)
	s.mu.Unlock()
	return map[string]bool{"revoked": true}, nil
}

// ---- principals and credentials ----

// lookup returns the principal a credential's hash names, or nil.
func (s *Server) lookup(key [sha256.Size]byte) *principal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.principals[key]
}

// newPrincipal reserves a private directory for a principal that does not
// exist yet. The fs adapter creates it on its first write.
func (s *Server) newPrincipal() *principal {
	s.mu.Lock()
	s.created++
	n := s.created
	s.mu.Unlock()
	dir := filepath.Join(s.dir, "principals", strconv.Itoa(n))
	return &principal{dir: dir, env: []string{
		"BRIGADE_CONFIG_DIR=" + filepath.Join(dir, "config"),
		"BRIGADE_STATE_DIR=" + filepath.Join(dir, "state"),
		"BRIGADE_FS_ROOT=" + s.store,
		"BRIGADE_PROFILE=default",
		"BRIGADE_LOG_LEVEL=error",
	}}
}

// issue mints a credential for who and remembers its hash.
func (s *Server) issue(who *principal) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	issued := base64.RawURLEncoding.EncodeToString(raw[:])
	s.mu.Lock()
	s.principals[sha256.Sum256([]byte(issued))] = who
	s.mu.Unlock()
	return issued, nil
}

// profileStatus is what the fs adapter's `profile status` says about a
// principal's directory.
type profileStatus struct {
	TeamRef      string `json:"team_ref"`
	PrincipalRef string `json:"principal_ref"`
}

// status reads the team a principal is bound to and its principal_ref.
func (s *Server) status(who *principal) (*profileStatus, *protocol.Error) {
	result, perr := s.fs(who, nil, "profile", "status")
	if perr != nil {
		return nil, perr
	}
	out := &profileStatus{}
	if protocol.Unmarshal(result, out) != nil {
		return nil, internal()
	}
	return out, nil
}

// inTeam checks the request's `team_ref` against the team the caller is
// bound to. A request that names another team, or a caller bound to none,
// is the uniform `unauthorized` of 4.5.7, the same bytes for a team that
// exists and one that does not (C-26, C-43).
func (s *Server) inTeam(c *call) *protocol.Error {
	teamRef, perr := text(c.doc, "team_ref", true)
	if perr != nil {
		return perr
	}
	status, perr := s.status(c.who)
	if perr != nil {
		return perr
	}
	if status.TeamRef == "" || status.TeamRef != teamRef {
		return notMember()
	}
	return nil
}

// ---- the fs adapter ----

// fs runs one fs adapter command for a principal and returns its result,
// or its failure as a server would word it.
func (s *Server) fs(who *principal, stdin []byte, args ...string) (jsontext.Value, *protocol.Error) {
	who.mu.Lock()
	defer who.mu.Unlock()
	var stdout bytes.Buffer
	fs.Run(args, bytes.NewReader(stdin), &stdout, io.Discard, who.env)
	var env protocol.Envelope
	if protocol.Unmarshal(stdout.Bytes(), &env) != nil || env.Validate() != nil {
		return nil, internal()
	}
	if env.OK {
		return env.Result, nil
	}
	return nil, translate(env.Error)
}

// translate turns the fs adapter's failure into the server's. A server
// never answers `usage`, `config` or `protocol_mismatch` (the contract's
// section 1): the fs adapter's "profile is not bound to a team" is, seen
// from a server, a principal that is not an active member of a team, and
// the rest of those three, like a directory that lost its credential,
// would be the stub's own fault.
func translate(e *protocol.ErrorObject) *protocol.Error {
	if e.Code == protocol.CodeConfig && e.Details["reason"] == "no_team_bound" {
		return notMember()
	}
	if _, answered := statuses[e.Code]; !answered || e.Code == protocol.CodeUnauthenticated {
		return internal()
	}
	return &protocol.Error{Code: e.Code, Message: e.Message, RetryAfterMS: e.RetryAfterMS, Details: e.Details}
}

// ---- errors ----

// notMember is the uniform unauthorized of 4.5.7, the fs adapter's own
// words for it.
func notMember() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
}

func unknownCredential() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeUnauthenticated, Message: "the credential is not known to this server"}
}

func internal() *protocol.Error {
	return &protocol.Error{Code: protocol.CodeInternal, Message: "the stand-in server failed"}
}

func invalid(field, reason, message string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: message,
		Details: map[string]string{"field": field, "reason": reason},
	}
}

// ---- requests ----

// decode reads a request body as one of the protocol's shapes.
func decode(body []byte, v protocol.Validator) *protocol.Error {
	return asError(protocol.Decode(body, v))
}

// asError is err as the protocol error it is, nil for nil.
func asError(err error) *protocol.Error {
	if err == nil {
		return nil
	}
	var perr *protocol.Error
	if errors.As(err, &perr) && perr != nil {
		return perr
	}
	return internal()
}

// text reads a string member of the request. A required one must be
// present and not empty; an optional one may be absent.
func text(doc map[string]jsontext.Value, name string, required bool) (string, *protocol.Error) {
	raw, present := doc[name]
	if !present {
		if required {
			return "", invalid(name, "required", "required member "+name+" is missing or empty")
		}
		return "", nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", invalid(name, "invalid_value", name+" must be a string")
	}
	if value == "" && required {
		return "", invalid(name, "required", "required member "+name+" is missing or empty")
	}
	return value, nil
}

// number reads an optional integer member within low..high; absent is def.
func number(doc map[string]jsontext.Value, name string, def, low, high int) (int, *protocol.Error) {
	raw, present := doc[name]
	if !present {
		return def, nil
	}
	var value int
	if json.Unmarshal(raw, &value) != nil || value < low || value > high {
		return 0, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: name + " must be between " + strconv.Itoa(low) + " and " + strconv.Itoa(high),
			Details: map[string]string{"field": name, "reason": "out_of_range", "min": strconv.Itoa(low), "max": strconv.Itoa(high)},
		}
	}
	return value, nil
}

// orNull is raw, or JSON null for an absent member, so a document built
// around it stays valid JSON and the fs adapter names the missing member.
func orNull(raw jsontext.Value) jsontext.Value {
	if len(raw) == 0 {
		return jsontext.Value("null")
	}
	return raw
}

// leaseInRange holds a registration's or a heartbeat's lease_seconds to
// the range every version 1 server implements (the contract's section 5),
// BAP/1's 30..600. The fs adapter's own range starts at 1.
func leaseInRange(document []byte) *protocol.Error {
	var doc struct {
		LeaseSeconds *int `json:"lease_seconds"`
	}
	// A document that does not parse has no lease to check, and the fs
	// adapter refuses it for what it is.
	_ = protocol.Unmarshal(document, &doc)
	return asError(protocol.DefaultLease().CheckSeconds("lease_seconds", doc.LeaseSeconds))
}

// ---- responses ----

// statuses is the contract's status table.
var statuses = map[protocol.Code]int{
	protocol.CodeInvalidInput:    http.StatusBadRequest,
	protocol.CodeUnauthenticated: http.StatusUnauthorized,
	protocol.CodeUnauthorized:    http.StatusForbidden,
	protocol.CodeNotFound:        http.StatusNotFound,
	protocol.CodeConflict:        http.StatusConflict,
	protocol.CodeLoopDetected:    http.StatusUnprocessableEntity,
	protocol.CodeRateLimited:     http.StatusTooManyRequests,
	protocol.CodeInternal:        http.StatusInternalServerError,
	protocol.CodeUnavailable:     http.StatusServiceUnavailable,
}

func writeResult(w http.ResponseWriter, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		writeError(w, internal())
		return
	}
	write(w, http.StatusOK, &protocol.Envelope{OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: raw})
}

func writeError(w http.ResponseWriter, perr *protocol.Error) {
	status, known := statuses[perr.Code]
	if !known {
		perr, status = internal(), http.StatusInternalServerError
	}
	write(w, status, &protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: perr.Object()})
}

func write(w http.ResponseWriter, status int, env *protocol.Envelope) {
	body, err := json.Marshal(env)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
