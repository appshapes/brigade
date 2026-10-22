package commands

import (
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// readMap reads the fixture's map back through the strict reader.
func (f *fixture) readMap(t *testing.T) *sessionmap.ByPID {
	t.Helper()
	m, err := (sessionmap.Store{StateDir: f.stateDir}).ReadByPID(fixturePID)
	if err != nil {
		t.Fatalf("read map: %v", err)
	}
	return m
}

// withInbound rewrites the fixture's map with the given decided policy
// (and the effective one derived from it).
func (f *fixture) withInbound(t *testing.T, decided string) {
	t.Helper()
	m := f.byPID()
	m.SetInboundDecision(decided)
	f.writeMap(t, m)
}

// runInbound runs `brigade inbound <args>` in the session and returns
// the stdout and the error.
func (f *fixture) runInbound(t *testing.T, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	f.out.Reset()
	f.rec.on("session heartbeat", okAnswer(heartbeatResultJSON()))
	inv := f.inv(f.sessionEnv(), "", args...)
	inv.JSON = jsonOut
	err := Inbound(inv)
	return f.out.String(), err
}

// inboundDoc decodes the --json result.
func inboundDoc(t *testing.T, out string) inboundResult {
	t.Helper()
	var env struct {
		OK     bool          `json:"ok"`
		Result inboundResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || !env.OK {
		t.Fatalf("envelope %q: %v", out, err)
	}
	return env.Result
}

// TestInboundRefusesBeforeAnySpawn: a bad word, two words, and a terminal
// are answered before any child is spawned (U-05).
func TestInboundRefusesBeforeAnySpawn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		args     []string
		terminal bool
		code     protocol.Code
		msg      string
	}{
		{"an unknown word", []string{"quiet"}, false, protocol.CodeUsage, InboundUsage},
		{"wrongly cased", []string{"Hold"}, false, protocol.CodeUsage, InboundUsage},
		{"two words", []string{"hold", "now"}, false, protocol.CodeUsage, InboundUsage},
		{"a terminal", []string{"hold"}, true, protocol.CodeConfig, "needs a Brigade session"},
		{"a terminal status", nil, true, protocol.CodeConfig, "needs a Brigade session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			env := f.sessionEnv()
			if tc.terminal {
				env = f.terminalEnv()
			}
			err := Inbound(f.inv(env, "", tc.args...))
			perr := wantCodeErr(t, err, tc.code, "")
			if !strings.Contains(perr.Message, tc.msg) {
				t.Errorf("message %q, want it to carry %q", perr.Message, tc.msg)
			}
			if n := f.rec.count(); n != 0 {
				t.Errorf("%d spawns, want none", n)
			}
		})
	}
}

// TestInboundStatusSpawnsNothing: the no-argument form reads the map and
// the pending file and prints the three lines without a child.
func TestInboundStatusSpawnsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	out, err := f.runInbound(t, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "inbound: accept (the session's own policy; no request in this session)\nheld: 0\n" + InboundNoteAccept + "\n"
	if out != want {
		t.Errorf("status printed:\n%q\nwant:\n%q", out, want)
	}
	if n := f.rec.count(); n != 0 {
		t.Errorf("%d spawns, want none", n)
	}
	out, err = f.runInbound(t, true)
	if err != nil {
		t.Fatal(err)
	}
	r := inboundDoc(t, out)
	if r.SelfSessionID != selfSessionID || r.Inbound != "accept" || r.Decided != "accept" || r.Requested != "" || r.Changed || r.Held != 0 || r.Published || r.Note != InboundNote {
		t.Errorf("status result %+v", r)
	}
}

// TestInboundHoldThenAccept is the card's main path: `hold` writes the
// request and the effective policy into the map and sends exactly one
// heartbeat carrying `inbound` alone; a repeated `hold` writes and sends
// nothing; `accept` clears the request, the map and the roster return to
// the decision.
func TestInboundHoldThenAccept(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	out, err := f.runInbound(t, false, "hold")
	if err != nil {
		t.Fatal(err)
	}
	want := "inbound: hold (requested in this session; the session's own policy is accept)\nheld: 0\n" + InboundNoteHold + "\n"
	if out != want {
		t.Errorf("hold printed:\n%q\nwant:\n%q", out, want)
	}
	m := f.readMap(t)
	if m.Inbound != "hold" || m.InboundDecided != "accept" || m.InboundRequest != "hold" || !m.UpdatedAt.Equal(fixtureNow) {
		t.Fatalf("map after hold: %q/%q/%q updated %v", m.Inbound, m.InboundDecided, m.InboundRequest, m.UpdatedAt)
	}
	if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session heartbeat"}) {
		t.Fatalf("spawned %v, want describe then one heartbeat", got)
	}
	spec := f.rec.spec(t, 1)
	if i := slices.Index(spec.Argv, "--session"); i < 0 || i+1 >= len(spec.Argv) || spec.Argv[i+1] != selfSessionID {
		t.Fatalf("argv %v does not carry --session %s", spec.Argv, selfSessionID)
	}
	if string(spec.Stdin) != `{"inbound":"hold"}` {
		t.Fatalf("heartbeat stdin %q, want the one member", spec.Stdin)
	}
	// The same request again: a status call.
	out, err = f.runInbound(t, true, "hold")
	if err != nil {
		t.Fatal(err)
	}
	if r := inboundDoc(t, out); r.Changed || r.Published || r.Inbound != "hold" || r.Requested != "hold" {
		t.Errorf("repeated hold result %+v", r)
	}
	// The describe is cached, so nothing at all was spawned.
	if got := f.rec.verbs(); !slices.Equal(got, []string{"describe", "session heartbeat"}) {
		t.Fatalf("spawned %v, want no second heartbeat", got)
	}
	// Lifted.
	out, err = f.runInbound(t, true, "accept")
	if err != nil {
		t.Fatal(err)
	}
	r := inboundDoc(t, out)
	if !r.Changed || !r.Published || r.Inbound != "accept" || r.Decided != "accept" || r.Requested != "" {
		t.Errorf("accept result %+v", r)
	}
	m = f.readMap(t)
	if m.Inbound != "accept" || m.InboundDecided != "accept" || m.InboundRequest != "" {
		t.Fatalf("map after accept: %q/%q/%q", m.Inbound, m.InboundDecided, m.InboundRequest)
	}
	if spec := f.rec.spec(t, 2); string(spec.Stdin) != `{"inbound":"accept"}` {
		t.Fatalf("heartbeat stdin %q", spec.Stdin)
	}
}

// TestInboundRefuseRequest: `refuse` is the stronger form, with its own
// note; `accept` lifts it the same way.
func TestInboundRefuseRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	out, err := f.runInbound(t, false, "refuse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "inbound: refuse (requested in this session; the session's own policy is accept)\n") || !strings.HasSuffix(out, InboundNoteRefuse+"\n") {
		t.Errorf("refuse printed:\n%q", out)
	}
	if m := f.readMap(t); m.Inbound != "refuse" || m.InboundRequest != "refuse" {
		t.Fatalf("map after refuse: %+v", *m)
	}
	// hold under a refuse request is a loosening of the request, which is
	// allowed: the request is the session's own.
	out, err = f.runInbound(t, true, "hold")
	if err != nil {
		t.Fatal(err)
	}
	if r := inboundDoc(t, out); !r.Changed || r.Inbound != "hold" || r.Requested != "hold" {
		t.Errorf("hold after refuse %+v", r)
	}
}

// TestInboundCannotRiseAboveTheDecision: with a decided refuse (the
// native scan, the parity rule or the option), `hold` is recorded but
// changes nothing and `accept` lifts nothing; both say why and send no
// heartbeat. With a decided hold, a request of refuse tightens and
// `accept` returns to hold, not accept.
func TestInboundCannotRiseAboveTheDecision(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withInbound(t, "refuse")
	out, err := f.runInbound(t, false, "hold")
	if err != nil {
		t.Fatal(err)
	}
	want := "inbound: refuse (the session's own policy; the request for hold in this session adds nothing)\nheld: 0\n" + InboundNoteCeiling + "\n"
	if out != want {
		t.Errorf("hold under a decided refuse printed:\n%q\nwant:\n%q", out, want)
	}
	if m := f.readMap(t); m.Inbound != "refuse" || m.InboundDecided != "refuse" || m.InboundRequest != "hold" {
		t.Fatalf("map: %+v", *m)
	}
	out, err = f.runInbound(t, true, "accept")
	if err != nil {
		t.Fatal(err)
	}
	if r := inboundDoc(t, out); r.Changed || r.Published || r.Inbound != "refuse" || r.Decided != "refuse" || r.Requested != "" {
		t.Errorf("accept under a decided refuse %+v", r)
	}
	if got := f.rec.verbs(); !slices.Equal(got, []string{"describe"}) {
		t.Fatalf("spawned %v, want no heartbeat", got)
	}

	g := newFixture(t)
	g.withInbound(t, "hold")
	out, err = g.runInbound(t, true, "refuse")
	if err != nil {
		t.Fatal(err)
	}
	if r := inboundDoc(t, out); !r.Changed || r.Inbound != "refuse" || r.Decided != "hold" {
		t.Errorf("refuse under a decided hold %+v", r)
	}
	out, err = g.runInbound(t, false, "accept")
	if err != nil {
		t.Fatal(err)
	}
	want = "inbound: hold (the session's own policy; no request in this session)\nheld: 0\n" + InboundNoteHold + "\n"
	if out != want {
		t.Errorf("accept under a decided hold printed:\n%q\nwant:\n%q", out, want)
	}
}

// TestInboundCountsHeldMessages: the held line counts the unreleased
// entries of the pending file and names the first three distinct
// senders.
func TestInboundCountsHeldMessages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withInbound(t, "hold")
	entries := []inbound.PendingEntry{
		{MessageID: "m1", SenderSessionID: "s1", SenderName: "carol", SenderPrincipal: "p1", ReceivedAt: fixtureNow},
		{MessageID: "m2", SenderSessionID: "s2", SenderName: "dave", SenderPrincipal: "p2", ReceivedAt: fixtureNow},
		{MessageID: "m3", SenderSessionID: "s1", SenderName: "carol", SenderPrincipal: "p1", ReceivedAt: fixtureNow},
		{MessageID: "m4", SenderSessionID: "s3", SenderName: "erin", SenderPrincipal: "p3", ReceivedAt: fixtureNow},
		{MessageID: "m5", SenderSessionID: "s4", SenderName: "frank", SenderPrincipal: "p4", ReceivedAt: fixtureNow},
		{MessageID: "m6", SenderSessionID: "s5", SenderName: "gone", SenderPrincipal: "p5", ReceivedAt: fixtureNow, ReleasedAt: fixtureNow.Add(time.Second)},
	}
	if err := f.pendingStore().Save(inbound.PendingFile{Version: inbound.PendingFileVersion, SessionID: selfSessionID, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	out, err := f.runInbound(t, false)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 || lines[1] != "held: 5 (from carol, dave, erin and 1 more)" {
		t.Errorf("held line %q (all: %q)", lines, out)
	}
	out, err = f.runInbound(t, true)
	if err != nil {
		t.Fatal(err)
	}
	if r := inboundDoc(t, out); r.Held != 5 {
		t.Errorf("held = %d, want 5", r.Held)
	}
}

// TestInboundHeartbeatFailureStillWritesTheMap: the map is the
// enforcement and is written first; a heartbeat the adapter refuses adds
// the roster line, leaves published false, and is not an error.
func TestInboundHeartbeatFailureStillWritesTheMap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.rec.on("session heartbeat", failAnswer(protocol.CodeUnavailable, "down"))
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "", "hold")
	if err := Inbound(inv); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if !strings.HasSuffix(out, InboundNoteHold+"\n"+InboundRosterLine+"\n") {
		t.Errorf("printed:\n%q", out)
	}
	if m := f.readMap(t); m.Inbound != "hold" || m.InboundRequest != "hold" {
		t.Fatalf("map: %+v", *m)
	}
	f.out.Reset()
	inv = f.inv(f.sessionEnv(), "", "accept")
	inv.JSON = true
	if err := Inbound(inv); err != nil {
		t.Fatal(err)
	}
	if r := inboundDoc(t, f.out.String()); !r.Changed || r.Published || r.Inbound != "accept" {
		t.Errorf("accept with a failing heartbeat %+v", r)
	}
	if len(f.sleeps) != 0 {
		t.Errorf("slept %v: the heartbeat is not retried", f.sleeps)
	}
}

// TestInboundFillsAnOlderMap: a map written before the decided member
// existed (0.35.0) gains it from the effective value at the first
// request, and a planted request that fails validation is never read:
// the strict reader refuses the file.
func TestInboundFillsAnOlderMap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.byPID()
	m.Inbound = "hold" // a configured hold, decided member absent
	f.writeMap(t, m)
	if _, err := f.runInbound(t, false, "refuse"); err != nil {
		t.Fatal(err)
	}
	if got := f.readMap(t); got.Inbound != "refuse" || got.InboundDecided != "hold" || got.InboundRequest != "refuse" {
		t.Fatalf("map: %q/%q/%q", got.Inbound, got.InboundDecided, got.InboundRequest)
	}
	if _, err := f.runInbound(t, false, "accept"); err != nil {
		t.Fatal(err)
	}
	if got := f.readMap(t); got.Inbound != "hold" || got.InboundDecided != "hold" || got.InboundRequest != "" {
		t.Fatalf("map after accept: %q/%q/%q", got.Inbound, got.InboundDecided, got.InboundRequest)
	}
	// A written map is validated, so the file can never carry a loosened
	// policy; the reader applies the same rule to whatever it finds.
	bad := f.byPID()
	bad.InboundDecided, bad.InboundRequest, bad.Inbound = "accept", "hold", "accept"
	var perr *protocol.Error
	if err := (sessionmap.Store{StateDir: f.stateDir}).WriteByPID(bad); !errors.As(err, &perr) || perr.Details["field"] != "inbound" {
		t.Fatalf("a loosened map was written: %v", err)
	}
}
