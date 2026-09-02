package conformance

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// Harness is the `harness` member every registration the suite sends
// carries; `harness_version` is buildinfo.String().
const Harness = "brigade-conformance"

// A Principal is one adapter installation: its own HOME, configuration
// and state directories under the run directory. The fixture fills in the
// identity members after provisioning; a scratch principal has them empty.
type Principal struct {
	Name, Home, ConfigDir, StateDir string
	PrincipalRef, TeamRef, TeamName string

	dir         string // <run>/principals/<x>
	session     string // the fixture session id; "" for other principals
	origProfile []byte // profile.json before the first default Rebind
	rebound     bool
}

// caseAbort and caseSkip are the panics T.Fatalf and T.Skip raise; the
// runner recovers them.
type (
	caseAbort struct{}
	caseSkip  struct{}
)

// launcherAbort is raised when the fixture cannot be built: the run stops
// with ExitLauncher rather than failing every later case one by one.
type launcherAbort struct{ err error }

// T is the per-case context: the fixture, the spawning seam, the
// assertion helpers and the bookkeeping. One T lives for one case.
type T struct {
	c   Case
	run *runner
	ctx context.Context

	mu       sync.Mutex
	failures []string
	notes    []string
	skipWhy  string
	watches  []*WatchProc
	scratch  map[string]bool
	done     bool
}

func newT(ctx context.Context, run *runner, c Case) *T {
	return &T{c: c, run: run, ctx: ctx, scratch: map[string]bool{}}
}

// ---- fixture ----

// A returns fixture principal A (team T1), building the fixture on first use.
func (t *T) A() *Principal { return t.run.fixture.ensure(t).a }

// B returns fixture principal B (team T1).
func (t *T) B() *Principal { return t.run.fixture.ensure(t).b }

// C returns fixture principal C (team T2).
func (t *T) C() *Principal { return t.run.fixture.ensure(t).c }

// Session returns the fixture session id of p (fixture-a, -b or -c);
// a principal without one aborts the case.
func (t *T) Session(p *Principal) string {
	if p.session == "" {
		t.Fatalf("principal %s has no fixture session", p.Name)
	}
	return p.session
}

// Scratch creates a fresh principal — the three directories, no setup, no
// team — for the cases that must not touch the fixture (C-01..C-08). The
// directory is <run>/principals/<case id>-<name>; name must be unique
// within the case.
func (t *T) Scratch(name string) *Principal {
	t.mu.Lock()
	dup := t.scratch[name]
	t.scratch[name] = true
	t.mu.Unlock()
	if dup {
		t.Fatalf("Scratch(%q) called twice in one case", name)
	}
	p, err := t.run.newPrincipal(name, strings.ToLower(t.c.ID)+"-"+name)
	if err != nil {
		t.Fatalf("scratch principal %s: %v", name, err)
	}
	return p
}

// JoinPrincipal joins a fresh principal into T1 with the remembered join
// secret, labelled <name>@example.com. It needs team.join and a known
// secret; without them the case is skipped. No session is registered.
func (t *T) JoinPrincipal(name string) *Principal {
	f := t.run.fixture.ensure(t)
	if !t.HasCap("team.join") || f.secret == "" {
		t.Skip("needs team.join to provision an extra principal")
	}
	p, err := t.run.newPrincipal(name, strings.ToLower(t.c.ID)+"-"+name)
	if err != nil {
		t.Fatalf("principal %s: %v", name, err)
	}
	req := protocol.TeamJoinRequest{JoinSecret: f.secret, HumanLabel: name + "@example.com"}
	var res protocol.TeamJoinResult
	t.OK(t.Exec(p, t.mustJSON(&req), "team", "join"), &res)
	p.PrincipalRef, p.TeamRef, p.TeamName = res.PrincipalRef, res.TeamRef, res.TeamName
	f.mu.Lock()
	f.extras = append(f.extras, p)
	f.mu.Unlock()
	return p
}

// T1Principals returns the principal_refs the suite has put into team T1:
// A, B and every principal JoinPrincipal has joined so far, in that order.
// A case that asserts "only T1 sessions are listed" checks against this
// set rather than {A, B}, so it holds whatever cases ran before it (C-28
// and C-40 join extra principals into T1; a reordered run put them before
// C-12 and failed it).
func (t *T) T1Principals() []string {
	f := t.run.fixture.ensure(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	refs := []string{f.a.PrincipalRef, f.b.PrincipalRef}
	for _, p := range f.extras {
		refs = append(refs, p.PrincipalRef)
	}
	return refs
}

// JoinSecret returns T1's join secret, or "" when the fixture was
// provisioned by --setup (cases that need it Skip).
func (t *T) JoinSecret() string { return t.run.fixture.ensure(t).secret }

// Describe returns the start-of-run describe result.
func (t *T) Describe() *protocol.DescribeResult { return t.run.describe }

// DescribeTouched reports what the start-of-run describe left behind on
// its fresh scratch principal — entries under its config or state
// directory, or the shared directory — as one line each; empty when it
// touched nothing (4.2). That spawn precedes every case, so C-01 can
// assert it in any case order.
func (t *T) DescribeTouched() []string { return slices.Clone(t.run.describeTouched) }

// HasCap reports whether describe advertises the capability.
func (t *T) HasCap(name string) bool {
	for _, c := range t.run.describe.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// RunDir returns the run directory.
func (t *T) RunDir() string { return t.run.launcher.runDir }

// SharedDir returns <run>/shared, the directory --shared-env names, or ""
// without that flag. The suite never creates it (C-01 asserts describe
// does not either).
func (t *T) SharedDir() string { return t.run.launcher.sharedDir() }

// Slow reports whether the run was started with --slow, for the arms a
// non-slow case runs only then (C-19b's lease-expiry arm).
func (t *T) Slow() bool { return t.run.opts.Slow }

// Setup runs the --setup command once against p with p's environment (the
// fixture's operator seam, for C-07's "without those caps" leg). It
// returns false when no --setup was given; a non-zero exit or a spawn
// failure aborts the case.
func (t *T) Setup(p *Principal) bool {
	cmd := t.run.opts.Setup
	if cmd == "" {
		return false
	}
	r, err := t.run.launcher.operator(t.ctx, t.c.ID, p, cmd, nil)
	if err != nil {
		t.Fatalf("--setup for %s: %v", p.Name, err)
	}
	if r.Exit != 0 {
		t.Fatalf("--setup for %s exited %d", p.Name, r.Exit)
	}
	return true
}

// ---- spawning ----

// Exec runs one adapter command for p with the document on stdin. A nil
// stdin is the null device (a command that takes no input); a non-nil
// stdin, even empty, is fed through a pipe and closed. The global checks
// of the launcher (exit range, stdout discipline, secrets) are recorded
// against the case; nothing else is asserted.
func (t *T) Exec(p *Principal, stdin []byte, args ...string) *Result {
	return t.exec(p, nil, stdin, false, args...)
}

// ExecEnv is Exec with extra K=V pairs appended to the process environment
// (the launcher's BRIGADE_TEST_OFFLINE=1 for C-01 and C-07).
func (t *T) ExecEnv(p *Principal, extra []string, stdin []byte, args ...string) *Result {
	return t.exec(p, extra, stdin, false, args...)
}

// ExecStdinOpen is Exec with stdin the read end of a pipe that is never
// written and never closed until the process exits (B-1: a command that
// takes no input must not read stdin; one that does hangs until the
// timeout and fails).
func (t *T) ExecStdinOpen(p *Principal, args ...string) *Result {
	return t.exec(p, nil, nil, true, args...)
}

// ExecStdinOpenEnv is ExecStdinOpen with extra K=V pairs appended to the
// process environment, so C-01's B-1 describe runs under the same
// BRIGADE_TEST_OFFLINE=1 as its first one.
func (t *T) ExecStdinOpenEnv(p *Principal, extra []string, args ...string) *Result {
	return t.exec(p, extra, nil, true, args...)
}

func (t *T) exec(p *Principal, extra []string, stdin []byte, open bool, args ...string) *Result {
	if p == nil {
		t.Fatalf("%s: nil principal", commandName(args))
	}
	r, violations := t.run.launcher.spawn(t.ctx, t.c.ID, p, extra, stdin, open, args...)
	for _, v := range violations {
		t.Errorf("%s", v)
	}
	return r
}

// ---- assertions on a Result ----

// OK asserts ok:true and exit 0, then decodes and validates the result
// into `into` (nil: the result is not decoded — use OKRaw for composite
// results). Any failure aborts the case.
func (t *T) OK(r *Result, into protocol.Validator) {
	name := r.name()
	if r.Envelope == nil {
		t.Fatalf("%s: stdout is not one valid envelope (exit %d)", name, r.Exit)
	}
	if !r.Envelope.OK {
		t.Fatalf("%s: want ok:true, got ok:false (%s), exit %d", name, describeError(r.Envelope.Error), r.Exit)
	}
	if r.Exit != protocol.ExitOK {
		t.Fatalf("%s: ok:true but exit %d, want 0", name, r.Exit)
	}
	if into != nil {
		if err := protocol.Decode(r.Envelope.Result, into); err != nil {
			t.Fatalf("%s: result does not decode and validate as %T: %v", name, into, err)
		}
	}
}

// OKRaw asserts ok:true and exit 0 and returns the result object parsed
// loosely, for the composite results (session register, session list,
// message receive, team members) and for key-presence checks.
func (t *T) OKRaw(r *Result) map[string]any {
	t.OK(r, nil)
	result, ok := r.Raw["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: result is not a JSON object", r.name())
	}
	return result
}

// Fail asserts ok:false, exit == code.Exit(), error.code == code, that the
// `retryable` member is present (B-2) and that it equals code.Retryable().
// It returns the error object. Any failure aborts the case.
func (t *T) Fail(r *Result, code protocol.Code) *protocol.ErrorObject {
	name := r.name()
	if r.Envelope == nil {
		t.Fatalf("%s: stdout is not one valid envelope (exit %d); want %s", name, r.Exit, code)
	}
	if r.Envelope.OK {
		t.Fatalf("%s: want ok:false (%s), got ok:true, exit %d", name, code, r.Exit)
	}
	e := r.Envelope.Error
	if e.Code != code {
		t.Fatalf("%s: want error.code %s, got %s", name, code, describeError(e))
	}
	if r.Exit != code.Exit() {
		t.Fatalf("%s: want exit %d for %s, got %d", name, code.Exit(), code, r.Exit)
	}
	errObj, _ := r.Raw["error"].(map[string]any)
	rv, present := errObj["retryable"]
	if !present {
		t.Fatalf("%s: error.retryable is absent (B-2)", name)
	}
	rb, isBool := rv.(bool)
	if !isBool {
		t.Fatalf("%s: error.retryable is not a boolean (4.3)", name)
	}
	if rb != code.Retryable() {
		t.Fatalf("%s: error.retryable is %v, want %v for %s (4.6)", name, rb, code.Retryable(), code)
	}
	return e
}

// describeError renders an error object for a reason: code, message and
// details.reason. Messages are value-free by 4.3, so they are safe here.
func describeError(e *protocol.ErrorObject) string {
	if e == nil {
		return "no error object"
	}
	s := string(e.Code) + ": " + e.Message
	if reason := e.Details["reason"]; reason != "" {
		s += " [reason " + reason + "]"
	}
	if field := e.Details["field"]; field != "" {
		s += " [field " + field + "]"
	}
	return s
}

// SameBytes records a failure unless a.Stdout and b.Stdout are identical
// byte for byte (the no-oracle rule of 4.5.6, 4.5.7, 4.4.10). Always pair
// it with DifferentBytes in the same case.
func (t *T) SameBytes(what string, a, b *Result) {
	if string(a.Stdout) != string(b.Stdout) {
		t.Errorf("%s: stdout of %s and %s differ; want byte-identical", what, a.name(), b.name())
	}
}

// DifferentBytes is the positive control for SameBytes: it records a
// failure when the two stdouts are identical.
func (t *T) DifferentBytes(what string, a, b *Result) {
	if string(a.Stdout) == string(b.Stdout) {
		t.Errorf("%s: stdout of %s and %s are identical; want different (positive control)", what, a.name(), b.name())
	}
}

// ---- protocol helpers: one Exec plus OK each ----

// mustJSON marshals a request; a marshalling failure is a suite bug and
// aborts the case.
func (t *T) mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

// Register registers a session for p named name: harness
// brigade-conformance, harness_version buildinfo.String(), activity busy,
// inbound accept, then edit (nil allowed). The raw result is returned for
// the composite members (resumed, lease_seconds, server_time) together
// with the session id; the record itself is validated as a SessionRecord.
func (t *T) Register(p *Principal, name string, edit func(*protocol.SessionRegistration)) (record map[string]any, sessionID string) {
	reg := protocol.SessionRegistration{
		Harness:        Harness,
		HarnessVersion: buildinfo.String(),
		SessionName:    name,
		Activity:       protocol.ActivityBusy,
		Inbound:        protocol.InboundAccept,
	}
	if edit != nil {
		edit(&reg)
	}
	r := t.Exec(p, t.mustJSON(&reg), "session", "register")
	raw := t.OKRaw(r)
	var rec protocol.SessionRecord
	if err := protocol.Decode(r.Envelope.Result, &rec); err != nil {
		t.Fatalf("%s: result does not validate as a SessionRecord: %v", r.name(), err)
	}
	return raw, rec.SessionID
}

// Send sends body from sender to recipient (both session ids) for p, after
// edit (nil allowed), and asserts an accepted SendResponse.
func (t *T) Send(p *Principal, sender, recipient, body string, edit func(*protocol.SendRequest)) *protocol.SendResponse {
	req := protocol.SendRequest{SenderSessionID: sender, RecipientSessionID: recipient, Body: body}
	if edit != nil {
		edit(&req)
	}
	var res protocol.SendResponse
	t.OK(t.Exec(p, t.mustJSON(&req), "message", "send"), &res)
	return &res
}

// SendRaw runs `message send` with an arbitrary stdin document and asserts
// nothing: for the forbidden members of C-23 and malformed documents.
func (t *T) SendRaw(p *Principal, stdin []byte) *Result {
	return t.Exec(p, stdin, "message", "send")
}

// Receive runs `message receive --session session [--limit limit]`
// (limit <= 0: no flag) and returns the validated envelopes. An absent
// `messages` array is a failure (JSON convention 3) and reads as empty.
func (t *T) Receive(p *Principal, session string, limit int) []protocol.MessageEnvelope {
	args := []string{"message", "receive", "--session", session}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	r := t.Exec(p, nil, args...)
	raw := t.OKRaw(r)
	items, present := raw["messages"].([]any)
	if _, has := raw["messages"]; !has {
		t.Errorf("%s: result has no `messages` member; a required array is always present (JSON convention 3)", r.name())
		return nil
	}
	if !present {
		t.Fatalf("%s: `messages` is not an array", r.name())
	}
	out := make([]protocol.MessageEnvelope, 0, len(items))
	for i, item := range items {
		var m protocol.MessageEnvelope
		if err := decodeMember(item, &m); err != nil {
			t.Fatalf("%s: messages[%d] does not validate: %v", r.name(), i, err)
		}
		out = append(out, m)
	}
	return out
}

// Ack runs `message ack --session session` with ids and asserts an AckResult.
func (t *T) Ack(p *Principal, session string, ids ...string) *protocol.AckResult {
	req := protocol.AckRequest{MessageIDs: ids}
	var res protocol.AckResult
	t.OK(t.Exec(p, t.mustJSON(&req), "message", "ack", "--session", session), &res)
	return &res
}

// List runs `session list [--session self] [--include-offline]` and
// returns the validated records plus the raw result for the composite
// members (team_ref, team_name, server_time, truncated).
func (t *T) List(p *Principal, self string, includeOffline bool) (sessions []protocol.SessionRecord, raw map[string]any) {
	args := []string{"session", "list"}
	if self != "" {
		args = append(args, "--session", self)
	}
	if includeOffline {
		args = append(args, "--include-offline")
	}
	r := t.Exec(p, nil, args...)
	raw = t.OKRaw(r)
	if _, has := raw["sessions"]; !has {
		t.Errorf("%s: result has no `sessions` member; a required array is always present (JSON convention 3)", r.name())
		return nil, raw
	}
	items, ok := raw["sessions"].([]any)
	if !ok {
		t.Fatalf("%s: `sessions` is not an array", r.name())
	}
	for i, item := range items {
		var rec protocol.SessionRecord
		if err := decodeMember(item, &rec); err != nil {
			t.Fatalf("%s: sessions[%d] does not validate: %v", r.name(), i, err)
		}
		sessions = append(sessions, rec)
	}
	return sessions, raw
}

// Heartbeat runs `session heartbeat --session session` with req (nil: an
// empty document, a pure renewal) and asserts a HeartbeatResult.
func (t *T) Heartbeat(p *Principal, session string, req *protocol.HeartbeatRequest) *protocol.HeartbeatResult {
	if req == nil {
		req = &protocol.HeartbeatRequest{}
	}
	var res protocol.HeartbeatResult
	t.OK(t.Exec(p, t.mustJSON(req), "session", "heartbeat", "--session", session), &res)
	return &res
}

// Close runs `session close --session session`, asserts ok:true and exit
// 0, and returns the Result for the {session_id, state} check.
func (t *T) Close(p *Principal, session string) *Result {
	r := t.Exec(p, nil, "session", "close", "--session", session)
	t.OK(r, nil)
	return r
}

// decodeMember re-encodes one loosely parsed member and decodes it into a
// typed, validated shape.
func decodeMember(item any, into protocol.Validator) error {
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return protocol.Decode(b, into)
}

// ---- rebind ----

// Rebind binds p's profile to teamRef: through the --rebind command with
// {"team_ref": …} on stdin when given, else by rewriting `team_ref` in
// <config>/profiles/default/profile.json, keeping the original bytes for
// Restore. Cases that call it ALWAYS `defer t.Restore(p)`.
func (t *T) Rebind(p *Principal, teamRef string) {
	if cmd := t.run.opts.Rebind; cmd != "" {
		t.runOperatorCommand(p, cmd, []byte(`{"team_ref":`+strconv.Quote(teamRef)+`}`), "--rebind")
		p.rebound = true
		return
	}
	path := t.profilePath(p)
	if p.origProfile == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("rebind %s: %v", p.Name, err)
		}
		p.origProfile = data
	}
	var profile map[string]any
	if err := json.Unmarshal(p.origProfile, &profile); err != nil {
		t.Fatalf("rebind %s: profile.json does not parse: %v", p.Name, err)
	}
	profile["team_ref"] = teamRef
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("rebind %s: %v", p.Name, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("rebind %s: %v", p.Name, err)
	}
	p.rebound = true
}

// Restore puts p's original binding back after Rebind: the original
// profile.json bytes, or the --rebind command with the original team_ref.
// It is a no-op for a principal that was never rebound.
func (t *T) Restore(p *Principal) {
	if !p.rebound {
		return
	}
	p.rebound = false
	if cmd := t.run.opts.Rebind; cmd != "" {
		t.runOperatorCommand(p, cmd, []byte(`{"team_ref":`+strconv.Quote(p.TeamRef)+`}`), "--rebind")
		return
	}
	if err := os.WriteFile(t.profilePath(p), p.origProfile, 0o600); err != nil {
		t.Fatalf("restore %s: %v", p.Name, err)
	}
}

func (t *T) profilePath(p *Principal) string {
	return filepath.Join(p.ConfigDir, "profiles", "default", "profile.json")
}

// runOperatorCommand runs a --setup/--rebind command (argv split on
// whitespace, no shell) in p's environment; a non-zero exit aborts the
// case. The protocol checks do not apply: it is the operator's command,
// not the adapter's.
func (t *T) runOperatorCommand(p *Principal, command string, stdin []byte, what string) {
	r, err := t.run.launcher.operator(t.ctx, t.c.ID, p, command, stdin)
	if err != nil {
		t.Fatalf("%s command for %s: %v", what, p.Name, err)
	}
	if r.Exit != 0 {
		t.Fatalf("%s command for %s exited %d", what, p.Name, r.Exit)
	}
}

// ---- watch ----

// Watch spawns `message watch --session session` for p and returns the
// running process. The runner kills every watch still alive when the case
// ends.
func (t *T) Watch(p *Principal, session string) *WatchProc {
	return t.WatchArgs(p, "--session", session)
}

// WatchArgs spawns `message watch` with args for p: the seam for a case
// whose subject is the arguments themselves. C-37 uses it for its positive
// control, a watch with no --session at all, whose `usage` error event must
// NOT be byte-identical to the not_found one (4.1: an unknown or missing
// flag on a core command is usage; 4.4.9: a watch says so in an `error`
// event, because its stdout is NDJSON and nothing else).
func (t *T) WatchArgs(p *Principal, args ...string) *WatchProc {
	w, err := newWatch(t, p, args)
	if err != nil {
		t.Fatalf("message watch %s: %v", strings.Join(args, " "), err)
	}
	t.mu.Lock()
	t.watches = append(t.watches, w)
	t.mu.Unlock()
	return w
}

// PushDeadline is how long a case waits for a `message` event after a
// send: 5 s with message.watch.push, and 5 s for a polling adapter too
// (see the package comment: BAP/1 publishes no poll interval to read).
func (t *T) PushDeadline() time.Duration { return 5 * time.Second }

// ---- bookkeeping ----

// Errorf records a failure and continues.
func (t *T) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	t.mu.Lock()
	if !t.done {
		t.failures = append(t.failures, msg)
	}
	t.mu.Unlock()
	t.run.launcher.logf(t.c.ID, "FAIL: "+msg)
}

// Fatalf records a failure and aborts the case (deferred functions run).
func (t *T) Fatalf(format string, args ...any) {
	t.Errorf(format, args...)
	panic(caseAbort{})
}

// Skip marks the case SKIP with reason and aborts it. A case that already
// recorded a failure stays FAIL.
func (t *T) Skip(reason string) {
	t.mu.Lock()
	t.skipWhy = reason
	t.mu.Unlock()
	t.run.launcher.logf(t.c.ID, "SKIP: "+reason)
	panic(caseSkip{})
}

// Note records an observation that is reported but not asserted (C-32's
// order, C-29b's window arm).
func (t *T) Note(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	t.mu.Lock()
	if !t.done {
		t.notes = append(t.notes, msg)
	}
	t.mu.Unlock()
	t.run.launcher.logf(t.c.ID, "note: "+msg)
}

// Logf writes to the -v log only.
func (t *T) Logf(format string, args ...any) {
	t.run.launcher.logf(t.c.ID, fmt.Sprintf(format, args...))
}

// Sleep pauses for d, or until the run is cancelled.
func (t *T) Sleep(d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.ctx.Done():
	}
}

// RunID returns the run's identifier (unique per run, used in every name
// the cases create).
func (t *T) RunID() string { return t.run.runID }

// Now returns the suite's wall clock.
func (t *T) Now() time.Time { return time.Now() }

// outcome computes the case's report entry after the body returned.
func (t *T) outcome(duration time.Duration) CaseResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done = true
	res := CaseResult{ID: t.c.ID, Rule: t.c.Rule, DurationMS: duration.Milliseconds(), Notes: t.notes}
	switch {
	case len(t.failures) > 0:
		res.Status = StatusFail
		res.Reason = strings.Join(t.failures, "; ")
	case t.skipWhy != "":
		res.Status = StatusSkip
		res.Reason = t.skipWhy
	default:
		res.Status = StatusPass
	}
	return res
}
