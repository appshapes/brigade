package hook

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/cli"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

func streamsOf(t *testing.T) cli.Streams {
	t.Helper()
	return cli.Streams{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
}

// registered runs a SessionStart through the seam and returns the seam, so
// the prompt tests start from a registered session with a live watcher.
func registered(t *testing.T, f *fixture, extra map[string][]fakeadapter.Response, env ...string) *adapterSeam {
	t.Helper()
	responses := map[string][]fakeadapter.Response{
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"message ack":      {okResp(ackDoc())},
	}
	for k, v := range extra {
		responses[k] = v
	}
	seam := f.useSeam(responses)
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), env...); exit != 0 {
		t.Fatalf("session-start: exit %d: %s", exit, errOut)
	}
	return seam
}

// TestPromptUpdatesPermissionMode: a changed permission_mode is written
// with a new updated_at; an unchanged one rewrites nothing.
func TestPromptUpdatesPermissionMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, nil)
	f.now = fixedTime.Add(time.Minute)
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("plan")); exit != 0 || out != "" {
		t.Fatalf("exit %d out %q", exit, out)
	}
	m := f.mustMap()
	if m.PermissionMode != "plan" || !m.UpdatedAt.Equal(f.now) {
		t.Fatalf("map %+v", *m)
	}
	f.now = fixedTime.Add(2 * time.Minute)
	if exit, _, _ := f.run(SubPrompt, f.promptDoc("plan")); exit != 0 {
		t.Fatal(exit)
	}
	if m := f.mustMap(); !m.UpdatedAt.Equal(fixedTime.Add(time.Minute)) {
		t.Fatalf("an unchanged mode rewrote the map: %v", m.UpdatedAt)
	}
	// A document without permission_mode leaves the value alone.
	if exit, _, _ := f.run(SubPrompt, f.doc(map[string]any{"session_id": f.nativeID, "cwd": "/w", "hook_event_name": "UserPromptSubmit"})); exit != 0 {
		t.Fatal(exit)
	}
	if m := f.mustMap(); m.PermissionMode != "plan" {
		t.Fatalf("permission_mode %q", m.PermissionMode)
	}
}

// TestPromptEnsuresWatcher: a missing, dead or forged pidfile respawns the
// watcher from the map's values; a live one is left alone; without a
// socket nothing is spawned.
func TestPromptEnsuresWatcher(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		arrange   func(t *testing.T, f *fixture, watcher int)
		noSocket  bool
		wantSpawn bool
	}{
		{"alive watcher is kept", func(*testing.T, *fixture, int) {}, false, false},
		{"missing pidfile respawns", func(t *testing.T, f *fixture, _ int) {
			t.Helper()
			if err := os.Remove(f.pidfilePath()); err != nil {
				t.Fatal(err)
			}
		}, false, true},
		{"dead watcher respawns", func(t *testing.T, _ *fixture, watcher int) {
			t.Helper()
			if err := syscall.Kill(watcher, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(watcher) })
		}, false, true},
		{"forged start token respawns", func(t *testing.T, f *fixture, _ int) {
			t.Helper()
			e, err := pidfile.Read(f.pidfilePath())
			if err != nil {
				t.Fatal(err)
			}
			e.StartToken = forge(e.StartToken)
			if err := os.WriteFile(f.pidfilePath(), pidfile.Encode(e), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false, true},
		{"no socket: nothing to spawn", func(t *testing.T, f *fixture, _ int) {
			t.Helper()
			if err := os.Remove(f.pidfilePath()); err != nil {
				t.Fatal(err)
			}
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			watcher := testutil.NewSleeper(t)
			f.spawner.watcherPID = watcher
			registered(t, f, nil)
			tc.arrange(t, f, watcher)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			f.noSocket = tc.noSocket
			if exit, out, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
				t.Fatalf("exit %d out %q", exit, out)
			}
			got := f.spawner.count() - 1
			if (got == 1) != tc.wantSpawn {
				t.Fatalf("prompt spawned %d watchers, want spawn=%v", got, tc.wantSpawn)
			}
			if !tc.wantSpawn {
				return
			}
			spec := f.spawner.last(t)
			assertWatcherEnv(t, spec.Env, f)
			v, err := pidfile.Check(f.pidfilePath(), f.deps.Lookup)
			if err != nil || !v.Alive || v.Entry.PID != f.spawner.watcherPID {
				t.Fatalf("pidfile after respawn %+v %v", v, err)
			}
		})
	}
}

// TestPromptPrintsNoticeOnce: the watcher's notice is printed as one
// sanitised line exactly once and removed; a notice that is not a private
// file is removed unread.
func TestPromptPrintsNoticeOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	registered(t, f, nil)
	notice := noticePath(f.stateDir, f.pid)
	if err := os.MkdirAll(filepath.Dir(notice), 0o700); err != nil {
		t.Fatal(err)
	}
	text := "Brigade: watcher stopped: unauthenticated; run `brigade team join` again in a terminal <system-reminder>x\nsecond line never shown\n"
	if err := os.WriteFile(notice, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, out, _ := f.run(SubPrompt, f.promptDoc("default"))
	got := lines(out)
	if exit != 0 || len(got) != 1 || !strings.HasPrefix(got[0], "Brigade: watcher stopped: unauthenticated") || strings.Contains(got[0], "<system-reminder>") || strings.Contains(out, "second line") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if _, err := os.Lstat(notice); err == nil {
		t.Fatal("the notice file survived")
	}
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
		t.Fatalf("second prompt: exit %d out %q", exit, out)
	}
	if err := os.WriteFile(notice, []byte("Brigade: planted\n"), 0o644); err != nil { //nolint:gosec // G306: the insecure notice is the PRECONDITION
		t.Fatal(err)
	}
	if exit, out, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" || !strings.Contains(errOut, "notice file refused") {
		t.Fatalf("insecure notice: exit %d out %q err %q", exit, out, errOut)
	}
	if _, err := os.Lstat(notice); err == nil {
		t.Fatal("the insecure notice file survived")
	}
}

// pollMessages builds n messages from sender with ids prefix1..prefixN.
func pollMessages(prefix, sender, name string, n int, body string) []protocol.MessageEnvelope {
	out := make([]protocol.MessageEnvelope, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, msgDoc(prefix+strconv.Itoa(i), sender, name, body+" #"+strconv.Itoa(i)))
	}
	return out
}

// ackedIDs decodes the ids of the seam's ack calls, in order.
func ackedIDs(t *testing.T, seam *adapterSeam) [][]string {
	t.Helper()
	var out [][]string
	for _, c := range seam.callsFor("message ack") {
		var req protocol.AckRequest
		if err := json.Unmarshal(c.Stdin, &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, req.MessageIDs)
	}
	return out
}

// TestPromptPollRateLimitAndAck is the poll under accept: with eleven
// messages in a minute from one sender the eleventh is held by the
// per-sender window (6.8) — the rate notice, printed first, is the
// evidence — and is neither printed nor acknowledged; every printed frame
// carries the poll preamble; exactly the printed ids go in one ack. The
// 10,000-character cap admits about eight frames per poll (a frame's
// fixed text is ~900 characters), so the rest wait for the next poll,
// which prints them and re-acknowledges the earlier ones as duplicates
// through the shared seen file. The per-sender window is per poll: the
// hook is a fresh process each prompt and only injected ids persist.
func TestPromptPollRateLimitAndAck(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	msgs := pollMessages("a", senderA, "payments-api", 11, "from A")
	msgs = append(msgs, msgDoc("b1", senderB, "ci-runner", "from B"))
	seam := registered(t, f, map[string][]fakeadapter.Response{"message receive": {okResp(receiveDoc(msgs...))}}, config.OptionPollOnPrompt+"=true")
	exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true")
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if n := len([]rune(out)); n > OutputCap {
		t.Fatalf("printed %d characters, over the cap", n)
	}
	got := lines(out)
	if len(got) == 0 || got[0] != inbound.RateNotice(1, "payments-api") {
		t.Fatalf("the rate notice for A's eleventh message must come first: %q", out)
	}
	printed := strings.Count(out, frame.OpenTag)
	if printed < 5 || printed > 10 || strings.Count(out, frame.PollPreamble) != printed {
		t.Fatalf("%d frames printed with %d preambles", printed, strings.Count(out, frame.PollPreamble))
	}
	if strings.Contains(out, `message-id="a11"`) {
		t.Fatalf("the eleventh from A was printed: %s", out)
	}
	acks := ackedIDs(t, seam)
	if len(acks) != 1 || len(acks[0]) != printed {
		t.Fatalf("acks %v, want one call with the %d printed ids", acks, printed)
	}
	for _, id := range acks[0] {
		if id == "a11" || !strings.Contains(out, `message-id="`+id+`"`) {
			t.Fatalf("%s acknowledged without being printed", id)
		}
	}
	// The poll's seen file is the WATCHER's: state/seen/<Brigade session id>.json
	// (P5-14), named here by the literal join rather than inbound.SeenPath so
	// an encoding that ignored the id could not satisfy this assertion.
	seen, err := inbound.FileSeenStore{Path: filepath.Join(f.stateDir, "state", "seen", f.mustMap().BrigadeSessionID+".json")}.Load()
	if err != nil || len(seen) != printed {
		t.Fatalf("seen file %v %v, want the %d printed ids", seen, err, printed)
	}
	// The next poll a minute later: the printed ones are duplicates
	// (acknowledged again, not printed), the rest are printed — a11
	// included, its window having passed — and acknowledged.
	f.now = fixedTime.Add(2 * time.Minute)
	seam.responses["message receive"] = []fakeadapter.Response{okResp(receiveDoc(msgs...))}
	exit, out, _ = f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true")
	if exit != 0 {
		t.Fatal(exit)
	}
	if strings.Count(out, frame.OpenTag) != 12-printed || !strings.Contains(out, `message-id="b1"`) || !strings.Contains(out, `message-id="a11"`) {
		t.Fatalf("second poll printed %d frames, want the remaining %d: %s", strings.Count(out, frame.OpenTag), 12-printed, out)
	}
	acks = ackedIDs(t, seam)
	if len(acks) != 2 || len(acks[1]) != 12 {
		t.Fatalf("acks after the second poll %v, want 12 ids in the second call", acks)
	}
	if got := seam.verbs(); strings.Count(strings.Join(got, ","), "message receive") != 2 {
		t.Fatalf("calls %q", got)
	}
}

// TestPromptPollOutputCapAcksOnlyPrinted: frames are printed until the
// 10,000-character cap would be exceeded; only the printed ones are
// acknowledged; the next poll prints the next one (and re-acks the first
// as a duplicate through the seen file).
func TestPromptPollOutputCapAcksOnlyPrinted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedTeam(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	big := strings.Repeat("x", 6000)
	msgs := pollMessages("m", senderA, "payments-api", 3, big)
	seam := registered(t, f, map[string][]fakeadapter.Response{"message receive": {okResp(receiveDoc(msgs...))}}, config.OptionPollOnPrompt+"=true")
	exit, out, _ := f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true")
	if exit != 0 {
		t.Fatal(exit)
	}
	if n := len([]rune(out)); n > OutputCap {
		t.Fatalf("printed %d characters, over the cap", n)
	}
	if strings.Count(out, frame.OpenTag) != 1 || !strings.Contains(out, `message-id="m1"`) {
		t.Fatalf("want exactly m1 printed: %d frames", strings.Count(out, frame.OpenTag))
	}
	acks := ackedIDs(t, seam)
	if len(acks) != 1 || strings.Join(acks[0], ",") != "m1" {
		t.Fatalf("acks %v, want [m1]", acks)
	}
	// The next poll: m1 is a duplicate (acked again, not printed), m2 is
	// printed and acked, m3 still waits.
	seam.responses["message receive"] = []fakeadapter.Response{okResp(receiveDoc(msgs...))}
	exit, out, _ = f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true")
	if exit != 0 {
		t.Fatal(exit)
	}
	if strings.Count(out, frame.OpenTag) != 1 || !strings.Contains(out, `message-id="m2"`) {
		t.Fatalf("second poll: want exactly m2 printed: %s", out)
	}
	acks = ackedIDs(t, seam)
	if len(acks) != 2 || strings.Join(acks[1], ",") != "m1,m2" {
		t.Fatalf("acks %v, want the second call [m1 m2]", acks)
	}
}

// TestPromptPollRefuseFetchesNothing: under refuse the poll makes no
// adapter call and prints nothing; accept (the control) fetches.
func TestPromptPollRefuseFetchesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		extra     []string
		wantFetch bool
	}{
		{"refuse", []string{config.OptionTeamInbound + "=refuse", config.OptionPollOnPrompt + "=true"}, false},
		{"accept (control)", []string{config.OptionPollOnPrompt + "=true"}, true},
		{"poll off by default", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.spawner.watcherPID = testutil.NewSleeper(t)
			seam := registered(t, f, map[string][]fakeadapter.Response{"message receive": {okResp(receiveDoc(msgDoc("r1", senderA, "payments-api", "hello")))}}, tc.extra...)
			before := len(seam.calls)
			exit, out, _ := f.run(SubPrompt, f.promptDoc("default"), tc.extra...)
			if exit != 0 {
				t.Fatal(exit)
			}
			fetched := len(seam.callsFor("message receive")) == 1
			if fetched != tc.wantFetch {
				t.Fatalf("fetched %v, want %v (calls after start: %q)", fetched, tc.wantFetch, seam.verbs()[before:])
			}
			if tc.wantFetch != strings.Contains(out, frame.OpenTag) {
				t.Fatalf("out %q", out)
			}
		})
	}
}

// TestPromptRetriesRegistration: after a retryable SessionStart failure the
// next prompt registers (the "retrying at your next prompt" of the
// context line), at most once a minute while the backend stays down.
func TestPromptRetriesRegistration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {
		errResp(protocol.CodeUnavailable, ""), errResp(protocol.CodeUnavailable, ""), okResp(registerDoc("brigade-sess-1", "payments-api", false)),
	}})
	exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 || out != notConnected(protocol.CodeUnavailable)+"\n" || f.mapExists() {
		t.Fatalf("exit %d out %q map %v", exit, out, f.mapExists())
	}
	// First prompt: a retry that fails again (the second unavailable).
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || !strings.HasPrefix(out, "Brigade: not connected (unavailable)") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if n := len(seam.callsFor("session register")); n != 2 {
		t.Fatalf("%d register calls, want 2", n)
	}
	// Thirty seconds later: too soon, no attempt.
	f.now = fixedTime.Add(30 * time.Second)
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if n := len(seam.callsFor("session register")); n != 2 {
		t.Fatalf("%d register calls, want still 2", n)
	}
	// A minute later: the retry succeeds, the session is registered and
	// the start line is printed.
	f.now = fixedTime.Add(61 * time.Second)
	exit, out, _ = f.run(SubPrompt, f.promptDoc("default"))
	if exit != 0 || !strings.Contains(out, `this session is "payments-api" (brigade-sess-1)`) {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if !f.mapExists() || f.spawner.count() != 1 {
		t.Fatalf("map %v spawns %d after the successful retry", f.mapExists(), f.spawner.count())
	}
}

// TestPromptBrokenMapDoesNothing: a by-pid map that fails the privacy
// check is not acted on — no spawn, no poll, nothing printed — and the
// same map restored to 0600 works again (the control).
func TestPromptBrokenMapDoesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	seam := registered(t, f, map[string][]fakeadapter.Response{"message receive": {okResp(receiveDoc())}}, config.OptionPollOnPrompt+"=true")
	if err := os.Remove(f.pidfilePath()); err != nil {
		t.Fatal(err)
	}
	path, _ := f.store().ByPIDPath(f.pid)
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: the insecure map is the PRECONDITION
		t.Fatal(err)
	}
	calls, spawns := len(seam.calls), f.spawner.count()
	exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true")
	if exit != 0 || out != "" || len(seam.calls) != calls || f.spawner.count() != spawns || !strings.Contains(errOut, "cannot be trusted") {
		t.Fatalf("exit %d out %q calls %d spawns %d err %q", exit, out, len(seam.calls)-calls, f.spawner.count()-spawns, errOut)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if exit, _, _ := f.run(SubPrompt, f.promptDoc("default"), config.OptionPollOnPrompt+"=true"); exit != 0 || f.spawner.count() != spawns+1 || len(seam.callsFor("message receive")) != 1 {
		t.Fatalf("control: exit %d spawns %d receives %d", exit, f.spawner.count()-spawns, len(seam.callsFor("message receive")))
	}
}

// TestPromptRetryStampFollowsTheAttempt (P5-18): the retry stamp is written
// only AFTER a registration attempt returns, never before it. Claude Code
// kills a hook that outlives its timeout with SIGTERM, and nothing runs
// after that — so "no stamp at the instant of the register call" is
// exactly "a killed attempt leaves no stamp", and the next prompt tries
// again. A RETURNED failure still stamps: thirty seconds later there is no
// attempt, and the stamp the next attempt finds is the OLD one.
func TestPromptRetryStampFollowsTheAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.spawner.watcherPID = testutil.NewSleeper(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {
		errResp(protocol.CodeUnavailable, ""), errResp(protocol.CodeUnavailable, ""), okResp(registerDoc("brigade-sess-1", "payments-api", false)),
	}})
	stamp := retryStampPath(f.stateDir, f.pid)
	// The seam reads the stamp at the instant of every register call.
	var mu sync.Mutex
	var seen []string
	inner := f.deps.Spawn
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) == "session register" {
			data, err := os.ReadFile(stamp)
			if err != nil {
				data = []byte("<absent>")
			}
			mu.Lock()
			seen = append(seen, strings.TrimSpace(string(data)))
			mu.Unlock()
		}
		return inner(ctx, spec)
	}
	observed := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, seen...)
	}
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || f.mapExists() {
		t.Fatalf("session-start: exit %d map %v", exit, f.mapExists())
	}
	if _, err := os.Lstat(stamp); err == nil {
		t.Fatal("SessionStart wrote a retry stamp")
	}
	// Prompt 1: an attempt that fails and RETURNS. No stamp exists while the
	// adapter is being asked; one exists afterwards, with this attempt's time.
	exit, out, _ := f.run(SubPrompt, f.promptDoc("default"))
	if exit != 0 || !strings.HasPrefix(out, "Brigade: not connected (unavailable)") {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if got := observed(); len(got) != 2 || got[1] != "<absent>" {
		t.Fatalf("stamp at the register calls %q, want the prompt's (second) call to find none", got)
	}
	data, err := adapterkit.ReadStrict(stamp)
	if err != nil {
		t.Fatalf("no stamp after the returned failure: %v", err)
	}
	first := strings.TrimSpace(string(data))
	if first != fixedTime.Format(time.RFC3339Nano) {
		t.Fatalf("stamp %q, want %s", first, fixedTime.Format(time.RFC3339Nano))
	}
	// Thirty seconds later: the returned failure rate-limits; no attempt.
	f.now = fixedTime.Add(30 * time.Second)
	if exit, out, _ := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || out != "" {
		t.Fatalf("exit %d out %q", exit, out)
	}
	if n := len(seam.callsFor("session register")); n != 2 {
		t.Fatalf("%d register calls, want still 2", n)
	}
	// A minute later: a fresh attempt, which finds the OLD stamp (not one
	// written for itself), succeeds, and stamps its own time afterwards.
	f.now = fixedTime.Add(61 * time.Second)
	exit, out, _ = f.run(SubPrompt, f.promptDoc("default"))
	if exit != 0 || !strings.Contains(out, `this session is "payments-api" (brigade-sess-1)`) || !f.mapExists() {
		t.Fatalf("exit %d out %q map %v", exit, out, f.mapExists())
	}
	if got := observed(); len(got) != 3 || got[2] != first {
		t.Fatalf("stamp at the third register call %q, want the earlier attempt's %q", got, first)
	}
	data, err = adapterkit.ReadStrict(stamp)
	if err != nil || strings.TrimSpace(string(data)) != f.now.Format(time.RFC3339Nano) {
		t.Fatalf("stamp after the successful attempt %q %v, want %s", data, err, f.now.Format(time.RFC3339Nano))
	}
}
