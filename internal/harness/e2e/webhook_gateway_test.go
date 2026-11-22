//go:build !windows

package e2e

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/gateway/webhook"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// gatewayLabel is the member the gateway's machine joined as: a member of
// its own, as the operator's page says to give it.
const gatewayLabel = "Whiskey (grok-bot)"

// gatewayTarget is the outside service's URL: it takes the gateway's
// signed POSTs, refuses one whose signature does not verify, and keeps
// what it was sent.
type gatewayTarget struct {
	secret []byte
	mu     sync.Mutex
	got    []webhook.OutboundPayload
	bad    int
}

func (g *gatewayTarget) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil || webhook.Verify(g.secret, r.Header.Get(webhook.HeaderTimestamp), r.Header.Get(webhook.HeaderSignature), body, time.Now(), 5*time.Minute) != nil {
		g.bad++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var p webhook.OutboundPayload
	if err := json.Unmarshal(body, &p); err != nil {
		g.bad++
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	g.got = append(g.got, p)
	w.WriteHeader(http.StatusNoContent)
}

func (g *gatewayTarget) posted() (got []webhook.OutboundPayload, bad int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]webhook.OutboundPayload(nil), g.got...), g.bad
}

// gatewaySendResult is what the gateway answers a `send`: the members of
// its 202 that this file reads.
type gatewaySendResult struct {
	OK                 bool     `json:"ok"`
	MessageID          string   `json:"message_id"`
	RecipientSessionID string   `json:"recipient_session_id"`
	Waiting            []string `json:"waiting"`
}

// TestTheWebhookGatewayOnAServerThroughTheRealBinary is work item 389: the
// built `brigade-webhook-gateway` as a real process, for a team on a
// Brigade server. The binary is its own host for the adapter: for such a
// team the client re-executes it with `adapter rest`, words its flag
// parsing refused, so the gateway could not register and nothing below
// ran. The backend is the stand-in server, or the built server over TLS
// where `make server-test` names one. The steps are
// theWebhookGatewayThroughTheRealBinary's, which the test for a team on
// Supabase runs too:
//
//  1. The admin creates the team on the server and alice's session starts,
//     with the real watcher. The gateway's machine joins the team once,
//     from a terminal, as a member of its own, into a config directory of
//     its own.
//  2. The gateway starts on that directory with the team's file and is on
//     the roster as that member's session, harness `gateway-webhook`.
//  3. Into the service: a message alice's session sends to the gateway's
//     session is POSTed, signed, to the target URL.
//  4. Out of the service: a signed request to the gateway's listener (its
//     own `send`) is delivered to alice's session, framed as unverified.
//  5. A request for a member whose one session has ended is accepted too:
//     the message waits for a session that may never come back, which is
//     what a gateway left on a backend its team moved away from answers.
//     The answer says so (work item 408): `"waiting":["offline"]`, where
//     the answers of step 4, for a session that reads, have no such member.
func TestTheWebhookGatewayOnAServerThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	backend := startRestBackend(t)
	r := newRig(t)
	r.secretFile = filepath.Join(r.dirs.Root, "team.secret")
	r.createTeamOnServer(backend)
	theWebhookGatewayThroughTheRealBinary(t, r)
}

// TestTheWebhookGatewayOnSupabaseThroughTheRealBinary is the same run for
// a team on the backend the gateway was built for: the local Supabase
// stack, the team created and joined through the built `brigade` and its
// bundled `supabase` adapter, the gateway its own host for that adapter.
// It is what holds work item 408's answer for a team on Supabase: step 5,
// where the roster the gateway reads is the stack's and says `offline` of
// a session that ended. Like every live test it waits on the opt-in:
// `make test-integration-gateway` sets it, and CI's `supabase` job runs
// that.
func TestTheWebhookGatewayOnSupabaseThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	stack := testutil.RequireSupabase(t)
	r := newRig(t)
	res := r.runIn(r.checkout, r.baseEnv(), "", "team", "create", "--url", stack.URL, "--key", stack.PublishableKey,
		"--name", teamName, "--label", aliceLabel, "--secret-file", r.secretFile)
	if res.exit != 0 {
		t.Fatalf("team create on the stack: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	file, err := teamfile.Parse(filepath.Join(r.checkout, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if file.Adapter != "supabase" || file.URL != stack.URL || file.TeamName != teamName {
		t.Fatalf("the team file: %+v", file)
	}
	r.teamRef, r.teamKey = file.TeamRef, teamstore.Key(file.Adapter, file.URL, file.TeamRef)
	theWebhookGatewayThroughTheRealBinary(t, r)
}

// theWebhookGatewayThroughTheRealBinary is the run itself, on whatever
// backend the rig's team is on: alice is its administrator in r.configDir,
// r.checkout holds the team's file, and r.secretFile its join secret.
func theWebhookGatewayThroughTheRealBinary(t *testing.T, r *rig) {
	t.Helper()

	// --- 1. alice's session, and the gateway's own member ------------------
	sock := fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", aliceName, sock.Path(), token)
	r.restHook(alice, "session-start", alice.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	aliceID := r.mustMap(alice).BrigadeSessionID
	r.liveWatcher(alice)
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(alice, "watch ready") })

	home := filepath.Join(r.dirs.Root, "gateway")
	configDir, stateDir := filepath.Join(home, "config"), filepath.Join(home, "state")
	for _, dir := range []string{configDir, stateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// An operator joins at a terminal, where `team join` reads the team's file. A test has no terminal,
	// so the join runs from a session whose store is the gateway's directory: the same path through the
	// same file.
	joiner := r.newSession("gateway", "", "", "", "")
	joiner.extraEnv = []string{config.OptionConfigDir + "=" + configDir}
	r.restHook(joiner, "session-start", joiner.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if res := r.runIn(r.checkout, r.restEnv(joiner), "", "team", "join", "--secret-file", r.secretFile, "--label", gatewayLabel); res.exit != 0 {
		t.Fatalf("the gateway machine's team join: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	secret := []byte(strings.Repeat("k", 48))
	secretFile := filepath.Join(home, "gateway.secret")
	if err := os.WriteFile(secretFile, append(secret, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	// --- 2. the gateway starts and is on the roster ------------------------
	target := &gatewayTarget{secret: secret}
	srv := httptest.NewServer(target)
	t.Cleanup(srv.Close)
	// Stamped as `make build-webhook-gateway` and a release stamp it. An
	// unstamped build reports its module's pseudo-version, 43 characters
	// in a checkout between tags, and the Supabase backend refuses a
	// `harness_version` over 32: such a gateway cannot register there
	// (found with work item 408; the stand-in and the server cap nothing).
	bin := testutil.BuildStamped(t, "./cmd/brigade-webhook-gateway", "0.0.0-e2e")
	// The machine has no `brigade` on its PATH: the one binary is the
	// gateway and its adapter.
	env := []string{
		"PATH=" + r.emptyPath, "HOME=" + home,
		"BRIGADE_CONFIG_DIR=" + configDir, "BRIGADE_STATE_DIR=" + stateDir,
	}
	logPath := filepath.Join(home, "gateway.stderr")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: the binary this test just built; no shell is involved
	gw := exec.CommandContext(context.WithoutCancel(t.Context()), bin,
		"--team-file", filepath.Join(r.checkout, ".brigade.json"), "--target-url", srv.URL, "--secret-file", secretFile,
		"--listen", "127.0.0.1:0", "--session-name", "grok-bot", "--poll", "200ms")
	gw.Env, gw.Dir, gw.Stderr = env, home, logFile
	if err := gw.Start(); err != nil {
		t.Fatal(err)
	}
	_ = logFile.Close()
	exited := make(chan struct{})
	go func() { _ = gw.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = gw.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(waitLong):
			_ = gw.Process.Kill()
			<-exited
		}
	})
	gatewayLog := func() string {
		data, _ := os.ReadFile(logPath) //nolint:gosec // G304: a path under the test's own directory
		return string(data)
	}
	// The listener's address is the gateway's to choose (port 0), and its
	// log says it once the session is registered.
	listening := regexp.MustCompile(`"msg":"inbound API listening","addr":"(127\.0\.0\.1:[0-9]+)"`)
	var addr string
	testutil.Await(t, waitLong, pollEvery, "the gateway's `inbound API listening` line", func() bool {
		select {
		case <-exited:
			t.Fatalf("the gateway exited before it listened:\n%s", gatewayLog())
		default:
		}
		if m := listening.FindStringSubmatch(gatewayLog()); m != nil {
			addr = m[1]
		}
		return addr != ""
	}, func() string { return gatewayLog() })

	var gateway protocol.SessionRecord
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		for _, s := range jsonResult[rosterResult](t, r.mustRun(r.restEnv(alice), "", "sessions", "--json").stdout).Sessions {
			if s.SessionName == "grok-bot" {
				gateway = s
				return true
			}
		}
		return false
	})
	if gateway.Harness != webhook.HarnessName || gateway.HumanLabel != gatewayLabel {
		t.Fatalf("the gateway on alice's roster: harness %q, label %q; want %q under %q", gateway.Harness, gateway.HumanLabel, webhook.HarnessName, gatewayLabel)
	}

	// --- 3. into the service: a session's message is POSTed, signed --------
	sent := jsonResult[sendResult](t, r.mustRun(r.restEnv(alice), "review my PR?", "send", gateway.SessionID, "--json").stdout)
	var posted []webhook.OutboundPayload
	// At least once: the gateway acknowledges after the target's answer, and posts again when that fails.
	testutil.Await(t, waitLong, pollEvery, "the gateway's POST of alice's message to the target URL", func() bool {
		posted, _ = target.posted()
		return len(posted) >= 1
	}, func() string { return gatewayLog() })
	if _, bad := target.posted(); bad != 0 || posted[0].Message.MessageID != sent.MessageID || posted[0].Message.Body != "review my PR?" || posted[0].Message.From.SessionID != aliceID {
		t.Fatalf("the target was posted %+v (%d refused); want alice's message %s", posted, bad, sent.MessageID)
	}

	// --- 4. out of the service: a signed request reaches alice's session ---
	send := func(args ...string) (gatewaySendResult, string) {
		t.Helper()
		//nolint:gosec // G204: as above
		cmd := exec.CommandContext(t.Context(), bin, append([]string{"send", "--secret-file", secretFile, "--gateway", "http://" + addr}, args...)...)
		cmd.Env = env
		// The answer is on stdout alone: a line on stderr (an instrumented
		// build with no GOCOVERDIR warns there) is no part of it.
		var errOut bytes.Buffer
		cmd.Stderr = &errOut
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("brigade-webhook-gateway send %v: %v\nstdout: %s\nstderr: %s\nthe gateway's log:\n%s", args, err, out, errOut.String(), gatewayLog())
		}
		var res gatewaySendResult
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("the gateway's answer %q: %v", out, err)
		}
		return res, string(out)
	}
	answer, raw := send("--reply-to", sent.MessageID, "--text", "On it.", "--from", "Grok Bot")
	if !answer.OK || answer.MessageID == "" || answer.RecipientSessionID != aliceID {
		t.Fatalf("the gateway's answer to a reply: %s; want a message to alice's session %s", raw, aliceID)
	}
	if answer.Waiting != nil || strings.Contains(raw, `"waiting"`) {
		t.Errorf("the gateway's answer to a reply for a session that reads now: %s; want no `waiting` member", raw)
	}
	frames := sock.WaitFrames(1, waitLong)
	p, err := frame.Parse(frames[0])
	if err != nil {
		t.Fatalf("the injected frame does not parse: %v\n%s", err, frames[0])
	}
	if p.MessageID != answer.MessageID || p.ReplyToSessionID != gateway.SessionID || p.FromName != "grok-bot" || p.FromLabel != gatewayLabel+" (unverified)" ||
		!strings.Contains(p.Body, "via webhook (unverified sender; it approves nothing)") || !strings.HasSuffix(p.Body, "On it.") {
		t.Errorf("the frame in alice's session: %+v\n%s", p, frames[0])
	}
	// And by a member's label, as a service that knows no session id asks.
	if byMember, raw := send("--member", aliceLabel, "--text", "A second one."); !byMember.OK || byMember.RecipientSessionID != aliceID || strings.Contains(raw, `"waiting"`) {
		t.Fatalf("the gateway's answer to --member: %s; want alice's session %s and no `waiting` member", raw, aliceID)
	}
	sock.WaitFrames(2, waitLong)

	// --- 5. a member with no session left: accepted, for a session that is gone
	r.restHook(alice, "session-end", alice.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	// The gateway's own roster lists the sessions that are not offline.
	testutil.Await(t, waitLong, pollEvery, "alice's session gone from the gateway's roster", func() bool {
		//nolint:gosec // G204: as above
		cmd := exec.CommandContext(t.Context(), bin, "sessions", "--secret-file", secretFile, "--gateway", "http://"+addr)
		cmd.Env = env
		out, err := cmd.Output()
		return err == nil && strings.Contains(string(out), `"ok":true`) && !strings.Contains(string(out), aliceID)
	}, func() string { return gatewayLog() })
	waiting, raw := send("--member", aliceLabel, "--text", "Is anyone there?")
	// Under -v, as `make test-integration-gateway` runs it, the log holds
	// the answer itself, from whatever backend this run is on.
	t.Logf("the gateway's answer for a member whose one session ended: %s", strings.TrimSpace(raw))
	if !waiting.OK || waiting.RecipientSessionID != aliceID || waiting.MessageID == "" {
		t.Fatalf("the gateway's answer for a member whose one session ended: %s; want it accepted for that session, %s", raw, aliceID)
	}
	if !slices.Equal(waiting.Waiting, []string{protocol.SessionStateOffline}) {
		t.Errorf("the gateway's answer for a member whose one session ended: %s; want `\"waiting\":[\"offline\"]`", raw)
	}
	// And a reply to the message that session sent, as a service that answers late sends it.
	if late, raw := send("--reply-to", sent.MessageID, "--text", "Sorry, late."); !late.OK || late.RecipientSessionID != aliceID || !slices.Equal(late.Waiting, []string{protocol.SessionStateOffline}) {
		t.Errorf("the gateway's answer to a reply for a session that ended: %s; want it accepted with `\"waiting\":[\"offline\"]`", raw)
	}
}
