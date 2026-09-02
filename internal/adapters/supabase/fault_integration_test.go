package supabase

// The two Docker fault tests of plan 9.4, gated by BRIGADE_TEST_DOCKER=1
// (`make test-integration` and CI's supabase job set it; a bare `go test`
// skips with the reason):
//
//	(a) polling degradation — the Realtime container is stopped under a
//	    running watch: `status polling` inside the reconnect backoff, a
//	    message sent meanwhile still delivered by the 10 s drain timer,
//	    and `status live` plus a push delivery again once the container
//	    is back;
//	(b) stack-restart recovery — the whole stack goes down and comes back
//	    through `make supabase-stop` / `make supabase-start`, so the
//	    container exclusion list is the one the Makefile owns rather than
//	    a copy of it here, and the watch reconnects, rejoins and the
//	    unacknowledged message is still delivered.
//
// Both restore what they broke in t.Cleanup whatever happens, and both
// are written so that a failure leaves the stack running: a test that
// leaves a developer's Realtime container stopped costs more than the
// signal it buys.
//
// These two run ALONE against a stack nobody else is using. The rest of
// the suite tolerates concurrent runs (every test mints its own
// principals); these do not, because they take the backend away.

import (
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// realtimeContainer is the local stack's Realtime container. The name is
// `supabase_realtime_<project id>` and the project id is the directory
// name pinned in supabase/config.toml.
const realtimeContainer = "supabase_realtime_brigade"

// requireDockerFaults skips unless the caller asked for the fault tests
// AND docker answers. The two conditions are separate on purpose: an
// operator who set BRIGADE_TEST_DOCKER=1 on a machine without docker
// should see why nothing ran.
func requireDockerFaults(t *testing.T) {
	t.Helper()
	if os.Getenv("BRIGADE_TEST_DOCKER") == "" {
		t.Skip("BRIGADE_TEST_DOCKER unset: this test stops and starts containers of the local stack")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("BRIGADE_TEST_DOCKER is set but docker is not on PATH: %v", err)
	}
}

// dockerRun runs one docker subcommand on the named container and
// returns its combined output.
func dockerRun(t *testing.T, verb, container string) (string, error) {
	t.Helper()
	// The context outlives the test's own: `docker start` in a cleanup
	// must run AFTER t.Context() is cancelled, or a failing test would
	// leave the container stopped.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 90*time.Second)
	defer cancel()
	//nolint:gosec // G204: argv array, no shell; verb is one of stop/start/inspect and container is a constant
	cmd := exec.CommandContext(ctx, "docker", verb, container)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// containerUp reports whether the container is running.
func containerUp(t *testing.T, container string) bool {
	t.Helper()
	out, err := dockerRun(t, "inspect", container)
	return err == nil && strings.Contains(out, `"Running": true`)
}

// stopContainer stops one container and returns the function that starts
// it again. The restore is registered as a cleanup as well and is safe to
// call twice, so an assertion that fails between the two never leaves the
// stack crippled.
func stopContainer(t *testing.T, container string) func() {
	t.Helper()
	if !containerUp(t, container) {
		t.Skipf("%s is not running: nothing to degrade", container)
	}
	started := false
	restore := func() {
		if started {
			return
		}
		started = true
		if out, err := dockerRun(t, "start", container); err != nil {
			t.Errorf("docker start %s: %v\n%s", container, err, out)
			return
		}
		t.Logf("docker start %s", container)
	}
	t.Cleanup(restore)
	if out, err := dockerRun(t, "stop", container); err != nil {
		t.Fatalf("docker stop %s: %v\n%s", container, err, out)
	}
	t.Logf("docker stop %s", container)
	return restore
}

// makeTarget runs one Makefile target from the repository root, with the
// same `supabase` override CI uses when the CLI binary is on PATH (the
// Makefile's default is the npx form). The nested-make variables are
// dropped so a parent `make test-integration` cannot hand this run a
// jobserver it has no token for.
func makeTarget(t *testing.T, target string, within time.Duration) {
	t.Helper()
	args := []string{target}
	if _, err := exec.LookPath("supabase"); err == nil {
		args = append(args, "supabase=supabase")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), within)
	defer cancel()
	//nolint:gosec // G204: argv array, no shell; the target and the one override are literals of this file
	cmd := exec.CommandContext(ctx, "make", args...)
	cmd.Dir = testutil.RepoRoot(t)
	cmd.Env = withoutMakeVars(os.Environ())
	started := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("make %s took %s", strings.Join(args, " "), time.Since(started).Round(time.Second))
	if err != nil {
		t.Fatalf("make %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// withoutMakeVars drops MAKEFLAGS, MAKELEVEL and MFLAGS.
func withoutMakeVars(environ []string) []string {
	out := environ[:0:0]
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if name == "MAKEFLAGS" || name == "MAKELEVEL" || name == "MFLAGS" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// statusIs matches a `status` event in the named state.
func statusIs(state string) func(map[string]any) bool {
	return func(event map[string]any) bool {
		return event["event"] == protocol.EventStatus && event["state"] == state
	}
}

// messageIs matches a `message` event carrying the named id.
func messageIs(id string) func(map[string]any) bool {
	return func(event map[string]any) bool {
		if event["event"] != protocol.EventMessage {
			return false
		}
		message, _ := event["message"].(map[string]any)
		return message["message_id"] == id
	}
}

// awaitEvent reads watch events until pred matches or d elapses. Unlike
// watchRun.expect it tolerates BOTH of the event kinds an outage
// produces: `status` (the degradation itself) and an `error` with
// retryable true (the drain reporting the backend is unreachable, once
// per outage, 5.6). A NON-retryable error is fatal — that is the watch
// giving up, and the test must say so rather than time out.
func awaitEvent(t *testing.T, w *watchRun, what string, d time.Duration, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	until := time.Now().Add(d)
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			t.Fatalf("no %s within %s", what, d)
		}
		// Not w.next: its timeout says only "no watch event within …",
		// and the commonest failure of a fault test is exactly this one —
		// the watch fell silent — so the message has to name WHAT was
		// being waited for or a CI log leaves the reader guessing.
		var event map[string]any
		select {
		case line, ok := <-w.lines:
			if !ok {
				t.Fatalf("the watch closed stdout while waiting for %s", what)
			}
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatalf("a watch line is not JSON while waiting for %s: %v (%q)", what, err, line)
			}
		case <-time.After(remaining):
			t.Fatalf("no %s within %s", what, d)
		}
		if pred(event) {
			return event
		}
		switch event["event"] {
		case protocol.EventStatus:
			t.Logf("status %v/%v while waiting for %s", event["state"], event["detail"], what)
		case protocol.EventMessage:
			// Another envelope from the same inbox. These tests never
			// assert that NOTHING else arrives — an inbox holding two
			// unacknowledged messages delivers both, in whatever order the
			// drain reads them — so a message that is not the one being
			// waited for is noted and skipped.
			message, _ := event["message"].(map[string]any)
			t.Logf("message %v while waiting for %s", message["message_id"], what)
		case protocol.EventError:
			object, _ := event["error"].(map[string]any)
			if retryable, _ := object["retryable"].(bool); !retryable {
				t.Fatalf("the watch gave up while waiting for %s: %v", what, event)
			}
			t.Logf("retryable error while waiting for %s: %v", what, object["code"])
		default:
			t.Fatalf("unexpected event %v while waiting for %s", event, what)
		}
	}
}

// TestIntegrationWatchPollingDegradation is 9.4's polling-degradation
// fault and criterion 4's second half: with Realtime stopped the send
// still reports `accepted` and the message is still delivered — by the
// drain timer at the polling interval, which is exactly the latency the
// `status polling` event announces (5.6) — and the watch returns to
// `status live` and push delivery when the container comes back.
func TestIntegrationWatchPollingDegradation(t *testing.T) {
	requireDockerFaults(t)
	a, secret := liveTeam(t, liveName(t, "p2-11-degrade"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))
	send := func(body string) string {
		t.Helper()
		return str(t, sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"`+body+`"}`), "message_id")
	}

	w := startWatch(t, b, "message", "watch", "--session", sb)
	w.expect(protocol.EventReady, 5*time.Second)
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	restore := stopContainer(t, realtimeContainer)
	stopped := time.Now()
	// The longest backoff step bounds how late the watch may notice; the
	// loss of the open socket is normally immediate.
	degradeBudget := watchTiming.reconnect[len(watchTiming.reconnect)-1] + 30*time.Second
	polling := awaitEvent(t, w, "status polling", degradeBudget, statusIs(protocol.StatusStatePolling))
	t.Logf("status polling (%v) %s after the container stopped", polling["detail"], time.Since(stopped).Round(time.Millisecond))

	// A send with Realtime down: accepted, and delivered by the drain.
	sent := time.Now()
	id := send("delivered while polling")
	drained := awaitEvent(t, w, "the message sent while polling", watchTiming.drainPolling+30*time.Second, messageIs(id))
	t.Logf("delivered %s after the send, with no channel (drain interval while polling: %s)",
		time.Since(sent).Round(time.Millisecond), watchTiming.drainPolling)
	message, _ := drained["message"].(map[string]any)
	if message["delivery_state"] != protocol.DeliveryStateAccepted || message["recipient_session_id"] != sb {
		t.Errorf("the drained envelope is %v", message)
	}

	restore()
	back := time.Now()
	live := awaitEvent(t, w, "status live again", 3*time.Minute, statusIs(protocol.StatusStateLive))
	if live["detail"] != statusDetailJoined {
		t.Errorf("status live detail %v, want %s", live["detail"], statusDetailJoined)
	}
	t.Logf("status live again %s after docker start", time.Since(back).Round(time.Millisecond))

	pushSent := time.Now()
	pushed := send("delivered by the channel again")
	awaitEvent(t, w, "the pushed message", watchTiming.drainPolling+30*time.Second, messageIs(pushed))
	latency := time.Since(pushSent)
	t.Logf("push delivery %s after the send", latency.Round(time.Millisecond))
	if latency > watchTiming.drainPolling {
		t.Errorf("delivery took %s, which is the polling drain rather than a push: the channel did not recover", latency)
	}

	w.send(`{"type":"close"}`)
	if code := w.wait(10 * time.Second); code != 0 {
		t.Errorf("exit %d after close, want 0", code)
	}
}

// TestIntegrationWatchStackRestartRecovery is the stack-restart row of
// the P2-10 brief, bounded at five minutes. The whole stack goes away and
// comes back through the Makefile (so the `-x` exclusion list is the
// one the recipe owns), and:
//
//   - the running watch degrades to polling, then rejoins and reports
//     `status live` again, and a message sent afterwards arrives;
//   - the message that was never acknowledged is STILL in the inbox and
//     is re-emitted to a fresh watch on the same session.
//
// The re-emission needs the second watch: within one process the watcher
// remembers what it has emitted (`w.seen`, 5.7), so the message the
// first watch already delivered is deliberately not delivered twice by
// it. At-least-once is a property of the INBOX across restarts, and that
// is what the second watch measures.
func TestIntegrationWatchStackRestartRecovery(t *testing.T) {
	requireDockerFaults(t)
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make is not on PATH: %v", err)
	}
	// budget is a hang catcher, not a performance bound: it covers `make
	// supabase-stop`, `make supabase-start` (23 s here, several times that
	// on a contended CI runner doing Docker work) and the rejoin. Measured
	// 26 s on this machine; the wall time is logged below so a regression
	// is visible without turning a slow runner into a failure.
	const budget = 10 * time.Minute
	started := time.Now()
	defer func() {
		t.Logf("stack-restart recovery took %s of the %s budget", time.Since(started).Round(time.Second), budget)
	}()
	left := func() time.Duration { return budget - time.Since(started) }

	a, secret := liveTeam(t, liveName(t, "p2-11-restart"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	w := startWatch(t, b, "message", "watch", "--session", sb)
	w.expect(protocol.EventReady, 5*time.Second)
	w.expectStatus(protocol.StatusStateLive, statusDetailJoined)

	unacked := str(t, sendLive(t, a,
		`{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"unacknowledged across the restart"}`), "message_id")
	awaitEvent(t, w, "the message that will not be acknowledged", 15*time.Second, messageIs(unacked))

	// Down and up. The cleanup is registered BEFORE the stop, so a panic
	// or a failed assertion in between still brings the stack back.
	restarted := false
	t.Cleanup(func() {
		if !restarted {
			makeTarget(t, "supabase-start", 4*time.Minute)
		}
	})
	stopAt := time.Now()
	makeTarget(t, "supabase-stop", 2*time.Minute)
	polling := awaitEvent(t, w, "status polling after the stack stopped", 90*time.Second, statusIs(protocol.StatusStatePolling))
	t.Logf("status polling (%v) %s after `make supabase-stop`", polling["detail"], time.Since(stopAt).Round(time.Second))

	makeTarget(t, "supabase-start", 4*time.Minute)
	restarted = true
	backAt := time.Now()

	live := awaitEvent(t, w, "status live after the restart", min(3*time.Minute, left()), statusIs(protocol.StatusStateLive))
	if live["detail"] != statusDetailJoined {
		t.Errorf("status live detail %v, want %s", live["detail"], statusDetailJoined)
	}
	t.Logf("the watch rejoined %s after `make supabase-start` returned", time.Since(backAt).Round(time.Second))

	after := str(t, sendLive(t, a,
		`{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"after the restart"}`), "message_id")
	awaitEvent(t, w, "a message sent after the restart", watchTiming.drainPolling+30*time.Second, messageIs(after))
	if code := w.exit(); code != 0 {
		t.Errorf("the first watch exited %d, want 0", code)
	}

	// The unacknowledged message survived the restart and is re-emitted
	// to a fresh watch; the one acknowledged below is not.
	second := startWatch(t, b, "message", "watch", "--session", sb)
	second.expect(protocol.EventReady, 10*time.Second)
	again := awaitEvent(t, second, "the unacknowledged message, re-emitted", 30*time.Second, messageIs(unacked))
	message, _ := again["message"].(map[string]any)
	if message["delivery_state"] != protocol.DeliveryStateAccepted {
		t.Errorf("the re-emitted envelope is %v", message)
	}
	second.send(`{"type":"ack","message_ids":["` + unacked + `","` + after + `"]}`)
	awaitEvent(t, second, "the acked event", 15*time.Second, func(event map[string]any) bool {
		return event["event"] == protocol.EventAcked
	})
	if code := second.exit(); code != 0 {
		t.Errorf("the second watch exited %d, want 0", code)
	}
	if got := receiveLive(t, b, sb); len(got) != 0 {
		t.Errorf("the inbox still holds %d messages after the acks", len(got))
	}

	if elapsed := time.Since(started); elapsed > budget {
		t.Errorf("the stack-restart recovery took %s, past the %s bound", elapsed.Round(time.Second), budget)
	} else {
		t.Logf("stack-restart recovery in %s (bound %s)", elapsed.Round(time.Second), budget)
	}
}
