package watch_test

import (
	"context"
	"encoding/json/v2"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// A watcher whose session is closed under it re-opens it (reopen.go). The
// closure a replaced predecessor leaves behind is played by the fake
// adapter: a heartbeat answered `conflict` with reason session_closed (the
// RPC path), or the same as an `error` event on the watch stream (the
// stdin-command path). The register the watcher then sends must carry its
// own id as the resume hint, and a resumed answer with that id puts the
// heartbeats back on track; a session that cannot be re-opened stops the
// watcher with reason session_gone.

const reopenSessionID = "s1"

// closedConflict is the adapters' answer to a heartbeat on a closed
// session (4.5.8, C-15).
func closedConflict() *protocol.ErrorObject {
	return &protocol.ErrorObject{
		Code: protocol.CodeConflict, Message: "that session is closed", Retryable: true,
		Details: map[string]string{"reason": "session_closed"},
	}
}

// resumedRegister is register_session's answer to a resume of the owned,
// closed session: the record with the same id and resumed true (C-19).
func resumedRegister(now string) []byte {
	return []byte(`{"session_id":"` + reopenSessionID + `","session_name":"payments-api","principal_ref":"p1",` +
		`"human_label":"alice@example.com","state":"active","activity":"busy","inbound":"accept",` +
		`"last_seen_at":"` + now + `","lease_until":"` + now + `","harness":"claude-code","harness_version":"2.1.267",` +
		`"created_at":"` + now + `","is_self":false,"resumed":true,"lease_seconds":90,"server_time":"` + now + `"}`)
}

// registerRecorder wraps the spawn seam to keep every `session register`
// document the watcher hands a one-shot child, decoded: the fake's dump
// records byte counts, not documents, and the resume hint is the point.
type registerRecorder struct {
	mu   sync.Mutex
	docs []protocol.SessionRegistration
}

func (rr *registerRecorder) spawn(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
	if strings.Contains(strings.Join(spec.Argv, " "), " session register") {
		var reg protocol.SessionRegistration
		if err := json.Unmarshal(spec.Stdin, &reg); err == nil {
			rr.mu.Lock()
			rr.docs = append(rr.docs, reg)
			rr.mu.Unlock()
		}
	}
	return adapterkit.Spawn(ctx, spec)
}

func (rr *registerRecorder) registrations() []protocol.SessionRegistration {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]protocol.SessionRegistration{}, rr.docs...)
}

// TestReopensASessionClosedUnderItOnTheRPCPath: an adapter without
// stdin commands answers the first heartbeat conflict:session_closed; the
// watcher registers with its own id as the resume hint, is answered
// resumed, logs "session re-opened", and the following heartbeats succeed.
func TestReopensASessionClosedUnderItOnTheRPCPath(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	now := time.Now().UTC().Format(time.RFC3339)
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	fx.useFake(fakeadapter.Script{
		Describe: fakeadapter.DescribeJSON("1", "message.receive", "session.inbound", "session.resume", "session.model", "session.context_used_tokens"),
		Responses: map[string][]fakeadapter.Response{
			"session heartbeat": {
				{Error: closedConflict()},
				{Result: []byte(`{"session_id":"` + reopenSessionID + `","state":"idle","lease_until":"` + now + `","server_time":"` + now + `"}`)},
			},
			"session register": {{Result: resumedRegister(now)}},
			"session close":    {{Result: []byte(`{"session_id":"` + reopenSessionID + `","state":"offline"}`)}},
		},
		Watch:    &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}},
		DumpFile: dump,
	})
	fx.writeMapWith(func(m *sessionmap.ByPID) { m.BrigadeSessionID = reopenSessionID })
	rr := &registerRecorder{}
	deps := fx.deps()
	deps.HeartbeatInterval = 150 * time.Millisecond
	deps.Spawn = rr.spawn
	r := fx.start(deps, fx.args()...)
	fx.waitLog("watch child started", map[string]any{"stdin_commands": false})
	fx.waitLog("the session was closed under the watcher; re-opening", map[string]any{"code": "conflict"})
	fx.waitLog("session re-opened", map[string]any{"session_id": reopenSessionID})
	testutil.Eventually(t, waitShort, pollEvery, func() bool {
		return fx.logCount("heartbeat_ok", nil) >= 1 || dumpCount(t, dump, "session", "heartbeat") >= 3
	})
	regs := rr.registrations()
	if len(regs) != 1 || regs[0].Resume == nil || regs[0].Resume.SessionID != reopenSessionID {
		t.Fatalf("registrations %+v, want exactly one with resume.session_id %s", regs, reopenSessionID)
	}
	if regs[0].Harness != "claude-code" || regs[0].SessionName == "" || regs[0].Activity == "" || regs[0].Inbound == "" {
		t.Fatalf("the re-open registration is incomplete: %+v", regs[0])
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestReopensASessionClosedUnderItOnTheStdinPath: the closure arrives as
// an `error` event on the watch stream (the adapter refused a stdin
// heartbeat); the re-open runs off the event loop and the child stays.
func TestReopensASessionClosedUnderItOnTheStdinPath(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	now := time.Now().UTC().Format(time.RFC3339)
	closed := &protocol.WatchError{Event: protocol.EventError, Error: *closedConflict()}
	fx.useFake(fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"session register": {{Result: resumedRegister(now)}},
		},
		Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{
			readyLine(t),
			{Raw: rawLine(t, closed), DelayMS: 200},
		}},
	})
	fx.writeMapWith(func(m *sessionmap.ByPID) { m.BrigadeSessionID = reopenSessionID })
	rr := &registerRecorder{}
	deps := fx.deps()
	deps.Spawn = rr.spawn
	r := fx.start(deps, fx.args()...)
	fx.waitLog("watch child started", map[string]any{"stdin_commands": true})
	fx.waitLog("the session was closed under the watcher; re-opening", map[string]any{"code": "conflict"})
	fx.waitLog("session re-opened", map[string]any{"session_id": reopenSessionID})
	if regs := rr.registrations(); len(regs) != 1 || regs[0].Resume == nil || regs[0].Resume.SessionID != reopenSessionID {
		t.Fatalf("registrations %+v, want exactly one with resume.session_id %s", regs, reopenSessionID)
	}
	if r.exited() {
		t.Fatal("the watcher exited on a re-openable closure")
	}
	if code := r.stopAndWait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestStopsWhenTheSessionCannotBeReopened: a resume answered not_found is
// a session gone for good; the watcher stops with reason session_gone
// (the next prompt hook respawns one that tries again).
func TestStopsWhenTheSessionCannotBeReopened(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(fakeadapter.Script{
		Describe: fakeadapter.DescribeJSON("1", "message.receive", "session.inbound", "session.resume"),
		Responses: map[string][]fakeadapter.Response{
			"session heartbeat": {{Error: closedConflict()}},
			"session register":  {{Error: &protocol.ErrorObject{Code: protocol.CodeNotFound, Message: "no such session", Retryable: false}}},
			"session close":     {{Result: []byte(`{"session_id":"` + reopenSessionID + `","state":"offline"}`)}},
		},
		Watch: &fakeadapter.WatchScript{Lines: []fakeadapter.WatchLine{readyLine(t)}},
	})
	fx.writeMapWith(func(m *sessionmap.ByPID) { m.BrigadeSessionID = reopenSessionID })
	deps := fx.deps()
	deps.HeartbeatInterval = 150 * time.Millisecond
	r := fx.start(deps, fx.args()...)
	fx.waitLog("the session cannot be re-opened; stopping", map[string]any{"code": "not_found"})
	fx.waitLog("closing the session", map[string]any{"reason": "session_gone"})
	testutil.Eventually(t, waitShort, pollEvery, r.exited)
}
