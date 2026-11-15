//go:build !windows

package e2e

import (
	"bytes"
	"encoding/json/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// logLines counts the lines with msg in the JSON-lines log at path.
func logLines(path, msg string) int {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a log under the test's own state directory
	if err != nil {
		return 0
	}
	n := 0
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		var m map[string]any
		if json.Unmarshal(line, &m) == nil && m["msg"] == msg {
			n++
		}
	}
	return n
}

// restPrompt runs one prompt hook of a session of a team whose adapter is
// named by its team file.
func (r *rig) restPrompt(s session) result {
	r.t.Helper()
	return r.restHook(s, "prompt", s.hookDoc("UserPromptSubmit", map[string]any{"permission_mode": "default"}))
}

// serverRoster is what `brigade sessions --json` answers from s, by
// session id.
func (r *rig) serverRoster(s session) map[string]struct{ name, inbound string } {
	r.t.Helper()
	res := r.mustRun(r.restEnv(s), "", "sessions", "--json")
	var doc struct {
		Result struct {
			Sessions []struct {
				SessionID   string `json:"session_id"`
				SessionName string `json:"session_name"`
				Inbound     string `json:"inbound"`
			} `json:"sessions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		r.t.Fatalf("sessions --json: %v\n%s", err, res.stdout)
	}
	out := map[string]struct{ name, inbound string }{}
	for _, s := range doc.Result.Sessions {
		out[s.SessionID] = struct{ name, inbound string }{s.SessionName, s.Inbound}
	}
	return out
}

// TestRunningSessionsFollowTheirTeamToAServerThroughTheRealBinary is a
// team's move as the sessions that are running at the time live it (work
// item 327, ADR 0008), through the built `brigade`, a real adapter for the
// backend the team leaves (the fs adapter, which carries team.move as the
// Supabase adapter does) and the built server it goes to. It needs the
// server itself, so it runs only where `make server-test` names one.
//
//  1. Sessions of one member are running on the old backend: one with a
//     watcher, one that asked for quiet, one that holds a message, and one
//     in a second checkout.
//  2. A team file with no record behind it, naming an address the test
//     listens on, earns one question to the old backend and not one
//     connection to that address; a second prompt within the minute asks
//     nothing.
//  3. The move is made: the administrator, whose own session is running
//     too, joins the server from inside it and records the move; her
//     session moves at its next prompt on her own join.
//  4. The member's session follows in two prompts: the first starts the
//     follow, the second registers the session on the server under the
//     name it had. The server's roster lists it, a new watcher serves it,
//     the old one is gone and has closed the old session, and a message
//     sent on the server reaches it.
//  5. The quiet session moves and stays quiet.
//  6. The session that holds a message stays until the message is
//     released, through a /clear too, and moves then. A prompt of a moved
//     session that comes from a checkout still on the old backend does not
//     take it back.
//  7. /clear in the second checkout follows in the start's own run and
//     ends with a map that names the server and a session the server
//     knows, with no heartbeat of the old session sent there first.
func TestRunningSessionsFollowTheirTeamToAServerThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	bin := os.Getenv(serverBinVar)
	if bin == "" {
		t.Skip("needs a built brigade-server: make server-test names it in " + serverBinVar)
	}
	r := newRig(t)
	r.createTeam()
	canon, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		t.Fatal(err)
	}
	pinOf := func(configDir, checkout string) teamstore.Pin {
		t.Helper()
		pin, ok, err := teamstore.LookupPin(configDir, checkout)
		if err != nil || !ok {
			t.Fatalf("the pin of %s: %v %v", checkout, ok, err)
		}
		return *pin
	}
	oldKey, oldPin := r.teamKey, pinOf(r.configDirB, canon)
	oldHost := teammove.Host(oldPin.URL)
	oldFile, err := os.ReadFile(filepath.Join(r.checkout, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(r.secretFile)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(string(text))
	moveLog := filepath.Join(r.stateDir, "logs", "team-move.log")
	const (
		movedWords    = "now names another backend for this session's team"
		notFollowed   = "the team file names another backend for this team, and the move was not followed"
		followStarted = "started the follow of the team's move"
		followedMsg   = "followed the team's recorded move"
	)

	// --- 1. the sessions that are running when the team moves ------------------------------------------------
	// A second checkout of bob's, joined to the team on the old backend as the first is.
	clearDir := filepath.Join(r.dirs.Root, "checkout-clear")
	if err := os.MkdirAll(filepath.Join(clearDir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(clearDir, teamfile.FileName), oldFile, 0o644); err != nil {
		t.Fatal(err)
	}
	canonClear, err := teamfile.Canonicalize(clearDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(r.configDirB, canonClear, oldPin); err != nil {
		t.Fatal(err)
	}
	sock, heldSock := fakesock.New(t), fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	bob := r.newSession("bob", "", bobName, sock.Path(), token)
	quiet := r.newSession("bob", "billing-quiet", "", "", "")
	held := r.newSession("bob", "", "billing-held", heldSock.Path(), token)
	clearer := r.newSession("bob", "billing-clear", "", "", "")
	clearer.cwd = clearDir
	alice := r.newSession("alice", "", aliceName, "", "")
	start := func(s session) result {
		return r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	}
	old := map[int]string{}
	for _, s := range []session{bob, quiet, held, clearer, alice} {
		start(s)
		m := r.mustMap(s)
		if m.TeamKey != oldKey || m.TeamRef != r.teamRef {
			t.Fatalf("a session before the move: %+v", m)
		}
		old[s.pid] = m.BrigadeSessionID
	}
	oldWatcher := r.liveWatcher(bob)
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(bob, "watch ready") && r.logHas(held, "watch ready") })
	// Two of them ask not to be messaged; one is sent a message all the same, which its watcher holds.
	for _, s := range []session{quiet, held} {
		r.mustRun(r.restEnv(s), "", "inbound", "hold")
		if m := r.mustMap(s); m.Inbound != "hold" || m.InboundRequest != "hold" {
			t.Fatalf("a session that asked for quiet: %+v", m)
		}
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(held, "inbound policy changed") })
	r.mustRun(r.restEnv(alice), "read this when you can", "send", old[held.pid], "--summary", "held", "--json")
	pending := func() int {
		file, err := inbound.FilePendingStore{Path: inbound.PendingPath(r.stateDir, old[held.pid]), SessionID: old[held.pid]}.Load()
		if err != nil {
			return -1
		}
		return len(file.Entries)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return pending() == 1 })

	// --- 2. the operator takes the team in and opens its move, with the server stopped, then starts it -------
	dir := t.TempDir()
	operator := func(args ...string) {
		t.Helper()
		//nolint:gosec // G204: the server binary the caller named; no shell is involved
		out, err := exec.CommandContext(t.Context(), bin, append([]string{"--data", filepath.Join(dir, "data"), "--log", "file"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	operator("--import-team", teamName, "--join-secret-file", r.secretFile)
	ticketFile := filepath.Join(r.dirs.Root, "team.ticket")
	operator("--open-move", r.teamRef, "--ticket-file", ticketFile)
	text, err = os.ReadFile(ticketFile)
	if err != nil {
		t.Fatalf("the ticket's file: %v", err)
	}
	ticket := strings.TrimSpace(string(text))
	backend := startServerOn(t, bin, dir)
	journal := func() string {
		text, _ := os.ReadFile(filepath.Join(backend.dir, "server.log"))
		return string(text)
	}
	moved := func(url, certificate string) []byte {
		t.Helper()
		doc := map[string]any{"version": 1, "adapter": "rest", "url": url, "team_ref": r.teamRef, "team_name": teamName}
		if certificate != "" {
			doc["certificate"] = certificate
		}
		file, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return append(file, '\n')
	}
	setFile := func(checkout string, file []byte) {
		t.Helper()
		//nolint:gosec // G306: a committed team file is 0644
		if err := os.WriteFile(filepath.Join(checkout, teamfile.FileName), file, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The questions the old backend was asked about a move: every `team moved` its adapter was run with.
	questions := func() int {
		argv, _ := os.ReadFile(r.argvLog)
		return strings.Count(string(argv), "\nteam\nmoved\n")
	}
	unmoved := func(s session, when string) {
		t.Helper()
		if m := r.mustMap(s); m.TeamKey != oldKey || m.BrigadeSessionID != old[s.pid] {
			t.Fatalf("%s: the session's map names %q and %q", when, m.TeamKey, m.BrigadeSessionID)
		}
	}

	// --- 3. a team file with no record behind it: one question to the old backend, nothing to its address -----
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var connections atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	hostile := "https://" + listener.Addr().String()
	setFile(r.checkout, moved(hostile, ""))
	res := r.restPrompt(bob)
	if !strings.Contains(res.stdout, movedWords) || strings.Contains(res.stdout, listener.Addr().String()) || !strings.Contains(res.stderr, followStarted) {
		t.Fatalf("the prompt after the file changed: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return logLines(moveLog, notFollowed) == 1 })
	if got := questions(); got != 1 {
		t.Fatalf("the old backend was asked about a move %d times, want once", got)
	}
	if n := connections.Load(); n != 0 || strings.Contains(journal(), "joined team") {
		t.Fatalf("the address the file names was connected to %d times, and the server's journal says:\n%s", n, journal())
	}
	if pin := pinOf(r.configDirB, canon); pin != oldPin {
		t.Fatalf("the pin moved with no record: %+v", pin)
	}
	unmoved(bob, "after a file with no record")
	// A second prompt, of another session of the checkout, within the minute: no follow is started, nothing asked.
	if res := r.restPrompt(quiet); !strings.Contains(res.stdout, movedWords) || strings.Contains(res.stderr, followStarted) {
		t.Fatalf("a second prompt within the minute: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if res := r.restPrompt(bob); strings.Contains(res.stdout, movedWords) || strings.Contains(res.stderr, followStarted) {
		t.Fatalf("a third prompt within the minute: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if got := questions(); got != 1 || connections.Load() != 0 {
		t.Fatalf("after two more prompts: %d questions, %d connections", got, connections.Load())
	}

	// --- 4. the move is made: the administrator joins the server from her running session and records it -----
	setFile(r.checkout, moved(backend.url, backend.certificate))
	newKey := teamstore.Key("rest", backend.url, r.teamRef)
	if res := r.restJoin(alice, aliceLabel); res.exit != 0 || !strings.Contains(res.stdout, "this session moves there at your next prompt") {
		t.Fatalf("the administrator's join: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	terminal := r.terminalEnv(r.configDir)
	if res := r.runIn(r.checkout, terminal, "y\n", "team", "move", "--ticket-file", ticketFile); res.exit != 0 || !strings.Contains(res.stdout, "recorded on "+oldHost) {
		t.Fatalf("the record: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	// Her own session moves on her own join: nothing is followed, and the line says what happened.
	asked := questions()
	res = r.restPrompt(alice)
	aliceMap := r.mustMap(alice)
	if aliceMap.TeamKey != newKey || aliceMap.BrigadeSessionID == old[alice.pid] || aliceMap.SessionName != aliceName ||
		!strings.Contains(res.stdout, `this checkout is joined to team "`+teamName+`" at `+strings.TrimPrefix(backend.url, "https://")) ||
		!strings.Contains(res.stdout, `this session is "`+aliceName+`" (`+aliceMap.BrigadeSessionID+`)`) || strings.Contains(res.stdout, "followed") {
		t.Fatalf("the administrator's session after her join: stdout %q\nmap %+v", res.stdout, aliceMap)
	}
	if questions() != asked || strings.Contains(journal(), "with its move ticket") {
		t.Fatalf("the administrator's own move asked the old backend, or presented the ticket")
	}

	// --- 5. the member's session follows in two prompts -------------------------------------------------------
	// The follow started for the file with no record bounds this one for a minute. On a machine that took less
	// than that to get here the prompt starts nothing, and the minute is made to pass by taking the stamp away.
	began := time.Now()
	res = r.restPrompt(bob)
	first := time.Since(began)
	if !strings.Contains(res.stderr, followStarted) {
		if err := os.Remove(config.MoveAttemptStamp(r.stateDir, r.configDirB, canon)); err != nil {
			t.Fatalf("the attempt's stamp: %v", err)
		}
		began = time.Now()
		res = r.restPrompt(bob)
		first = time.Since(began)
	}
	if !strings.Contains(res.stderr, followStarted) || strings.Contains(res.stdout, "followed") {
		t.Fatalf("the first prompt after the record: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	unmoved(bob, "at the prompt that started the follow")
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		pin, ok, err := teamstore.LookupPin(r.configDirB, canon)
		return err == nil && ok && pin.Adapter == "rest"
	})
	if pin := pinOf(r.configDirB, canon); pin.URL != backend.url || pin.Certificate != backend.certificate || pin.MovedFrom != oldPin.URL {
		t.Fatalf("bob's pin after the follow: %+v", pin)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return strings.Count(journal(), "with its move ticket") == 1 })
	// The child has ended: its last line is in its log, and its ticket file went as it returned.
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return logLines(moveLog, followedMsg) == 1 })
	if left, _ := filepath.Glob(filepath.Join(r.configDirB, ".move-ticket-*")); len(left) != 0 {
		t.Fatalf("the follow left its ticket file: %v", left)
	}
	if n := connections.Load(); n != 0 {
		t.Fatalf("the address of the file with no record was connected to %d times", n)
	}
	began = time.Now()
	res = r.restPrompt(bob)
	// The prompt hook's timeout is 5 s: both prompts are whole processes of the built binary, measured here.
	t.Logf("the prompt that started the follow took %v, and the prompt that moved the session %v", first.Round(time.Millisecond), time.Since(began).Round(time.Millisecond))
	bobMap := r.mustMap(bob)
	if !strings.Contains(res.stdout, `followed team "`+teamName+`" to `+strings.TrimPrefix(backend.url, "https://")+`, trusting the server certificate `+backend.certificate) ||
		!strings.Contains(res.stdout, "recorded that move on "+oldHost) || !strings.Contains(res.stdout, "with the move's ticket, under the label you had") ||
		!strings.Contains(res.stdout, `this session is "`+bobName+`" (`+bobMap.BrigadeSessionID+`)`) ||
		strings.Contains(res.stdout+res.stderr, ticket) || strings.Contains(res.stdout, bobLabel) ||
		strings.Contains(res.stderr, "session is gone server-side") {
		t.Fatalf("the prompt that moved the session: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if bobMap.TeamKey != newKey || bobMap.BrigadeSessionID == old[bob.pid] || bobMap.SessionName != bobName || !sessionmap.IsBundledRest(bobMap.AdapterCommand) {
		t.Fatalf("bob's map after the second prompt: %+v", bobMap)
	}
	// The server's roster lists the session under the name it had.
	if listed := r.serverRoster(alice); listed[bobMap.BrigadeSessionID].name != bobName || listed[aliceMap.BrigadeSessionID].name != aliceName {
		t.Fatalf("the server's roster: %v", listed)
	}
	// A new watcher serves it; the old one is gone, and closed the old session where it was registered.
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return r.liveWatcher(bob).BrigadeSessionID == bobMap.BrigadeSessionID && !alive(oldWatcher.PID)
	})
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.sessionFile(old[bob.pid]).ClosedAt != nil })
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logCount(bob, "watch ready") >= 2 })
	sent := jsonResult[sendResult](t, r.mustRun(r.restEnv(alice), "you are on the server now", "send", bobMap.BrigadeSessionID, "--summary", "moved", "--json").stdout)
	frames := sock.WaitFrames(1, waitLong)
	if p, err := frame.Parse(frames[len(frames)-1]); err != nil || p.MessageID != sent.MessageID || p.ReplyToSessionID != aliceMap.BrigadeSessionID || p.Body != "you are on the server now" {
		t.Fatalf("the frame that reached the session on the server: %+v, %v", p, err)
	}
	// A third prompt has nothing to say of the move, and nothing is asked again.
	asked = questions()
	if res := r.restPrompt(bob); strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, movedWords) || questions() != asked {
		t.Fatalf("a third prompt: stdout %q", res.stdout)
	}
	// A session moves with the checkout it registered in and no other. Bob's second checkout has not pulled
	// the changed file and is still pinned to the old backend: a prompt of the moved session that comes from
	// there (the model changed directory) does not take the session back, and says and asks nothing.
	elsewhere := bob
	elsewhere.cwd = clearDir
	if res := r.restPrompt(elsewhere); strings.Contains(res.stdout, "this session moves there") || strings.Contains(res.stdout, movedWords) || questions() != asked {
		t.Fatalf("a prompt of the moved session from a checkout still on the old backend: stdout %q", res.stdout)
	}
	if m := r.mustMap(bob); m.TeamKey != newKey || m.BrigadeSessionID != bobMap.BrigadeSessionID || m.Checkout != canon {
		t.Fatalf("bob's map after a prompt from his other checkout: %+v", m)
	}

	// --- 6. the quiet session moves in one prompt, the checkout having followed, and stays quiet ---------------
	res = r.restPrompt(quiet)
	quietMap := r.mustMap(quiet)
	if !strings.Contains(res.stdout, "this machine had joined it there already") || quietMap.TeamKey != newKey ||
		quietMap.Inbound != "hold" || quietMap.InboundRequest != "hold" || quietMap.SessionName != "billing-quiet" ||
		!strings.Contains(res.stdout, "inbound: hold") {
		t.Fatalf("the quiet session after its prompt: stdout %q\nmap %+v", res.stdout, quietMap)
	}
	if listed := r.serverRoster(alice); listed[quietMap.BrigadeSessionID].inbound != "hold" {
		t.Fatalf("the server's roster shows the quiet session as %q", listed[quietMap.BrigadeSessionID].inbound)
	}

	// --- 7. the session that holds a message stays until it is released ----------------------------------------
	res = r.restPrompt(held)
	if !strings.Contains(res.stdout, "followed team") || !strings.Contains(res.stdout, "1 team message is held for it there") {
		t.Fatalf("the prompt of the session that holds a message: stdout %q", res.stdout)
	}
	unmoved(held, "with a message held")
	if res := r.restPrompt(held); strings.Contains(res.stdout, "followed team") || strings.Contains(res.stdout, "stays on the backend") {
		t.Fatalf("a later prompt said the lines again: stdout %q", res.stdout)
	}
	unmoved(held, "with a message held, a prompt later")
	// /clear fires the session's start again: it keeps the session with its message, as a prompt does, tells
	// the conversation that begins why, and says the context line of the session where it is.
	held.nativeID += "-cleared"
	res = r.restHook(held, "session-start", held.hookDoc("SessionStart", map[string]any{"source": "clear"}))
	if !strings.Contains(res.stdout, "followed team") || !strings.Contains(res.stdout, "1 team message is held for it there") ||
		!strings.Contains(res.stdout, `this session is "billing-held" (`+old[held.pid]+`)`) || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("/clear with a message held: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	unmoved(held, "after a /clear with a message held")
	if m := r.mustMap(held); m.ClaudeSessionID != held.nativeID || pending() != 1 {
		t.Fatalf("after a /clear with a message held: the map names the conversation %q, %d messages wait", m.ClaudeSessionID, pending())
	}
	if res := r.restPrompt(held); strings.Contains(res.stdout, "followed team") || strings.Contains(res.stdout, "stays on the backend") {
		t.Fatalf("the prompt after the /clear said the lines again: stdout %q", res.stdout)
	}
	// The member releases it from a terminal: the watcher delivers it, on the backend it was sent on.
	bobTerminal := r.terminalEnv(r.configDirB)
	if res := r.runIn(r.checkout, bobTerminal, "", "inbox", "release", "--session", old[held.pid], "--all"); res.exit != 0 {
		t.Fatalf("inbox release: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if got := heldSock.WaitFrames(1, waitLong); len(got) == 0 || !strings.Contains(got[len(got)-1], "read this when you can") {
		t.Fatalf("the released message did not reach the session: %q", got)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return pending() == 0 })
	res = r.restPrompt(held)
	heldMap := r.mustMap(held)
	if heldMap.TeamKey != newKey || heldMap.BrigadeSessionID == old[held.pid] || heldMap.Inbound != "hold" ||
		!strings.Contains(res.stdout, `this session is "billing-held" (`+heldMap.BrigadeSessionID+`)`) || strings.Contains(res.stdout, "followed team") {
		t.Fatalf("the session after its message was released: stdout %q\nmap %+v", res.stdout, heldMap)
	}

	// --- 8. /clear in the second checkout: the start follows, and registers anew -------------------------------
	setFile(clearDir, moved(backend.url, backend.certificate))
	joins := strings.Count(journal(), "with its move ticket")
	clearer.nativeID += "-cleared"
	res = r.restHook(clearer, "session-start", clearer.hookDoc("SessionStart", map[string]any{"source": "clear"}))
	clearMap := r.mustMap(clearer)
	if !strings.Contains(res.stdout, "this machine had joined it there already") ||
		!strings.Contains(res.stdout, `this session is "billing-clear" (`+clearMap.BrigadeSessionID+`)`) ||
		strings.Contains(res.stderr, "session is gone server-side") || strings.Contains(res.stderr, "heartbeat failed") {
		t.Fatalf("/clear after the move: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if clearMap.TeamKey != newKey || clearMap.BrigadeSessionID == old[clearer.pid] || clearMap.ClaudeSessionID != clearer.nativeID {
		t.Fatalf("the map after /clear: %+v", clearMap)
	}
	if pin := pinOf(r.configDirB, canonClear); pin.Adapter != "rest" || pin.MovedFrom != oldPin.URL {
		t.Fatalf("the second checkout's pin: %+v", pin)
	}
	listed := r.serverRoster(alice)
	for what, id := range map[string]string{"the session that was cleared": clearMap.BrigadeSessionID, "the session that held a message": heldMap.BrigadeSessionID} {
		if _, ok := listed[id]; !ok {
			t.Errorf("the server does not know %s: %v", what, listed)
		}
	}
	for _, id := range old {
		if _, ok := listed[id]; ok {
			t.Errorf("the server lists %s, a session of the old backend", id)
		}
	}
	if got := strings.Count(journal(), "with its move ticket"); got != joins {
		t.Fatalf("the second checkout joined the server again: %d joins by ticket, want %d", got, joins)
	}

	// --- 9. the sessions end ----------------------------------------------------------------------------------
	watchers := []int{r.liveWatcher(bob).PID, r.liveWatcher(held).PID}
	for _, s := range []session{bob, quiet, held, clearer, alice} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(watchers[0]) && !alive(watchers[1]) })
	// The join secret, the ticket and the messaging token are in no file the run left behind but the two files
	// they travel in and the old backend's own record of the move, which holds the ticket by design.
	for _, hidden := range []string{secret, ticket, token} {
		for _, hit := range tokenHits(t, hidden, r.dirs.Root) {
			inRecord := hidden == ticket && strings.HasPrefix(hit, r.root)
			if hit != r.secretFile && hit != ticketFile && !inRecord {
				t.Errorf("a secret of the run is in %s", hit)
			}
		}
	}
}
