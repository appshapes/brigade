//go:build !windows

package hook

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// requestInbound does what `brigade inbound <word>` does to the map:
// records the request and derives the effective policy.
func (f *fixture) requestInbound(t *testing.T, request string) {
	t.Helper()
	m := f.mustMap()
	m.SetInboundRequest(request)
	if err := f.store().WriteByPID(m); err != nil {
		t.Fatal(err)
	}
}

// TestSessionStartRecordsTheDecision: the map a SessionStart writes
// carries the decision beside the effective policy, equal, with no
// request.
func TestSessionStartRecordsTheDecision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, nil)
	m := f.mustMap()
	if m.Inbound != "accept" || m.InboundDecided != "accept" || m.InboundRequest != "" {
		t.Fatalf("map after start: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
}

// TestPromptKeepsTheRuntimeRequest is the re-decision under a request
// (card 126): the decision is the ceiling and the request tightens below
// it. With a hold requested in an accept session, a prompt in the same
// mode leaves hold in force and prints nothing; a bypass prompt without
// the user accept decides refuse, which wins, with the parity warning;
// the user accept lifts the decision back to accept and the effective
// policy returns to hold, not accept, announced as hold; and the request
// cleared the way the command clears it is accept again with nothing to
// say, since the command already wrote the effective value.
func TestPromptKeepsTheRuntimeRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, nil, writeOff)
	f.requestInbound(t, protocol.InboundHold)

	f.deps.ReadFile = noSettings
	exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"), writeOff)
	if exit != 0 || out != "" {
		t.Fatalf("default prompt: exit %d out %q err %s", exit, out, errOut)
	}
	if m := f.mustMap(); m.Inbound != "hold" || m.InboundDecided != "accept" || m.InboundRequest != "hold" {
		t.Fatalf("map after a default prompt: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	if n := f.spawner.count(); n != 1 {
		t.Fatalf("spawns %d, want the one watcher (nothing changed)", n)
	}

	f.spawner.watcherPID = testutil.NewSleeper(t)
	exit, out, errOut = f.run(SubPrompt, f.promptDoc("bypassPermissions"), writeOff)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	parity := policy.Scan{UserFile: f.userSettingsFile()}.ParityWarning() + "\n"
	if out != parity {
		t.Fatalf("bypass prompt printed:\n%q\nwant:\n%q", out, parity)
	}
	if m := f.mustMap(); m.Inbound != "refuse" || m.InboundDecided != "refuse" || m.InboundRequest != "hold" {
		t.Fatalf("map after the bypass prompt: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	if v := envValue(f.spawner.last(t).Env, "BRIGADE_TEAM_INBOUND"); v != "refuse" {
		t.Fatalf("replacement watcher BRIGADE_TEAM_INBOUND=%q", v)
	}

	f.deps.ReadFile = userAcceptSettings(f.dirs.ClaudeConfig)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	exit, out, errOut = f.run(SubPrompt, f.promptDoc("bypassPermissions"), writeOff)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if want := policyNowLine(policy.Hold) + "\n"; out != want {
		t.Fatalf("after the accept was added:\n%q\nwant:\n%q", out, want)
	}
	if m := f.mustMap(); m.Inbound != "hold" || m.InboundDecided != "accept" || m.InboundRequest != "hold" {
		t.Fatalf("map after the accept: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	if v := envValue(f.spawner.last(t).Env, "BRIGADE_TEAM_INBOUND"); v != "hold" {
		t.Fatalf("replacement watcher BRIGADE_TEAM_INBOUND=%q, want hold", v)
	}

	// `brigade inbound accept` writes accept itself; the next prompt
	// agrees and says nothing.
	f.requestInbound(t, "")
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("bypassPermissions"), writeOff); exit != 0 || out != "" {
		t.Fatalf("after the lift: exit %d out %q", exit, out)
	}
	if m := f.mustMap(); m.Inbound != "accept" || m.InboundDecided != "accept" || m.InboundRequest != "" {
		t.Fatalf("map after the lift: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
}

// TestPromptFillsTheDecisionOfAnOlderMap: a map from 0.35.0 (no decided
// member) gains it at the first prompt that decides, written once, with
// no change of policy and nothing printed.
func TestPromptFillsTheDecisionOfAnOlderMap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, nil, writeOff)
	m := f.mustMap()
	m.InboundDecided = ""
	if err := f.store().WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	f.deps.ReadFile = noSettings
	f.now = f.now.Add(time.Minute)
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("default"), writeOff); exit != 0 || out != "" {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if got := f.mustMap(); got.InboundDecided != "accept" || got.Inbound != "accept" || !got.UpdatedAt.Equal(f.now) {
		t.Fatalf("map: %q/%q updated %v, want the decision filled and the map rewritten", got.Inbound, got.InboundDecided, got.UpdatedAt)
	}
}

// TestClearAndCompactKeepTheRuntimeRequest: the session continues across
// /clear (the continue path rebuilds the map from this SessionStart's
// decision and carries the request) and /compact (the refresh touches
// neither member), and the continue heartbeat carries the effective
// policy, hold, not the bare decision.
func TestClearAndCompactKeepTheRuntimeRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{
		"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"session heartbeat": {okResp(heartbeatDoc())},
	})
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatal(exit)
	}
	f.requestInbound(t, protocol.InboundHold)

	f.nativeID = "d2c72366-0000-4000-8000-000000000002"
	f.now = fixedTime.Add(time.Minute)
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("clear"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if !strings.Contains(out, "inbound: hold;") {
		t.Fatalf("the context line after /clear does not say hold: %q", out)
	}
	m := f.mustMap()
	if m.Inbound != "hold" || m.InboundDecided != "accept" || m.InboundRequest != "hold" || m.ClaudeSessionID != f.nativeID {
		t.Fatalf("map after clear: %q/%q/%q native %q", m.Inbound, m.InboundDecided, m.InboundRequest, m.ClaudeSessionID)
	}
	var hb protocol.HeartbeatRequest
	if err := json.Unmarshal(seam.callsFor("session heartbeat")[0].Stdin, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.Inbound == nil || *hb.Inbound != "hold" {
		t.Fatalf("the continue heartbeat carries inbound %v, want hold", hb.Inbound)
	}

	f.now = f.now.Add(time.Minute)
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("compact")); exit != 0 {
		t.Fatalf("compact: exit %d: %s", exit, errOut)
	}
	m = f.mustMap()
	if m.Inbound != "hold" || m.InboundDecided != "accept" || m.InboundRequest != "hold" || !m.UpdatedAt.Equal(f.now) {
		t.Fatalf("map after compact: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	if f.spawner.count() != 1 {
		t.Fatalf("the watcher was respawned (%d)", f.spawner.count())
	}
}
