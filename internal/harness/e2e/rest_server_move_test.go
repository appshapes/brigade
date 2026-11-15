//go:build !windows

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// A team's move from one Brigade server to another by the one command
// (work item 341, ADR 0010), through the built `brigade` and two servers on
// loopback: the stand-in (cmd/brigade-rest-stub) twice under `make test`,
// and the built brigade-server twice, each over TLS with a certificate it
// made itself, where `make server-test` names one.
//
// Until this work a server kept no record of where a team went, so a team
// on a server could not be moved with the one command: its adapter did not
// offer `team.move`, and the command refused. The record is now the
// server's, behind the capability `team.move` in `/v1/info` (the contract's
// section 3c). Everything else is what it was for a team that leaves a
// Supabase project: the tests of rest_move_test.go hold that.

// restAdapterAs runs the bundled `rest` adapter directly, as the member
// whose store configDir is: the one way a test reads a record's ticket
// from a server.
func (r *rig) restAdapterAs(configDir string) func(args ...string) result {
	return func(args ...string) result {
		r.t.Helper()
		return r.run(r.terminalEnv(configDir), "", append([]string{"adapter", "rest"}, args...)...)
	}
}

// teamOnAServer is a rig whose team lives on a server: the administrator
// created it there, and a member joined it from inside a session, with the
// secret's file. No session of either is left running.
func teamOnAServer(t *testing.T, backend restBackend) *rig {
	t.Helper()
	r := newRig(t)
	r.secretFile = filepath.Join(r.dirs.Root, "team.secret")
	r.createTeamOnServer(backend)
	// A join from inside a session reads the store to write from the facts
	// the session's start left, so the session starts first, attached to
	// nothing, and ends once the machine has joined.
	bob := r.newSession("bob", bobName, "", "", "")
	r.restHook(bob, "session-start", bob.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if res := r.restJoin(bob, bobLabel); res.exit != 0 {
		t.Fatalf("the member's join on the server the team is on: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	r.restHook(bob, "session-end", bob.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	return r
}

// recordOn reads what a server says of a move of the rig's team, with the
// profile a member's store holds for that server: `team moved` of the
// bundled adapter, which every active member is answered.
func (r *rig) recordOn(configDir string, backend restBackend) protocol.TeamMoveResult {
	r.t.Helper()
	key := teamstore.Key("rest", backend.url, r.teamRef)
	res := r.run(r.terminalEnv(configDir), "", "adapter", "rest", "--profile", key, "team", "moved")
	if res.exit != 0 {
		r.t.Fatalf("team moved on %s: exit %d\nstdout: %s\nstderr: %s", hostOf(backend), res.exit, res.stdout, res.stderr)
	}
	return jsonResult[protocol.TeamMoveResult](r.t, res.stdout)
}

// pointFileAt rewrites the checkout's team file to name a server, as the
// one command does, and returns the bytes it had.
func (m *movingTeam) pointFileAt(url, certificate string) string {
	m.r.t.Helper()
	file := filepath.Join(m.r.checkout, teamfile.FileName)
	before := m.fileNow()
	data, err := teamfile.ToServer(file, url, certificate)
	if err != nil {
		m.r.t.Fatal(err)
	}
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(file, data, 0o644); err != nil {
		m.r.t.Fatal(err)
	}
	return before
}

// putFile writes the checkout's team file.
func (m *movingTeam) putFile(text string) {
	m.r.t.Helper()
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(m.r.checkout, teamfile.FileName), []byte(text), 0o644); err != nil {
		m.r.t.Fatal(err)
	}
}

// staysPut starts a session of bob's and fails the test unless it follows
// nothing: it says that the team file does not match, attaches to nothing,
// and bob's pin is the one he had.
func (m *movingTeam) staysPut(what string) {
	t, r := m.r.t, m.r
	t.Helper()
	pin := m.pinOf(r.configDirB)
	s := r.newSession("bob", bobName, "", "", "")
	res := m.start(s)
	m.said.WriteString(res.stdout + res.stderr)
	if !strings.Contains(res.stdout, "not connected: .brigade.json does not match the team you joined here") || strings.Contains(res.stdout, "followed") || r.mapExists(s) {
		t.Fatalf("%s: a session followed, or said something other than that the file does not match: stdout %q\nstderr %s", what, res.stdout, res.stderr)
	}
	if now := m.pinOf(r.configDirB); now != pin {
		t.Fatalf("%s: the member's pin moved: %+v", what, now)
	}
}

// TestATeamMovesFromOneServerToAnotherByOneCommandThroughTheRealBinary: a
// team that lives on one Brigade server is moved to another by its owner,
// with the one command, and a member's next session follows at its start
// with no command of the member's.
//
//  1. A member that does not own the team is refused before the new
//     server is asked anything, in a server's words.
//  2. The owner moves the team. The team file keeps its adapter and names
//     the new server, the old server holds the record, and the old server
//     keeps the team as it was: its roster is the roster, and both members
//     still read it.
//  3. A team file that names an address the record does not is followed by
//     nobody.
//  4. The owner clears the record: no session follows. The same command
//     run again records it again.
//  5. The member's next session follows at its start, under the label he
//     had, with the move's ticket; he cannot administer the team there; and
//     a message from his session reaches the owner's on the new server.
//  6. Neither the join secret nor the ticket is in anything the command
//     printed, or in a file but the ones they travel in; and what the old
//     server said, where it is the built one, holds no ticket.
func TestATeamMovesFromOneServerToAnotherByOneCommandThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	from, to := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, from)
	m := newMovingTeam(t, r, r.restAdapterAs(r.configDir))
	if m.oldPin.Adapter != "rest" || m.oldPin.URL != from.url || m.oldHost != hostOf(from) {
		t.Fatalf("the team before the move is not on the first server: %+v", m.oldPin)
	}

	// --- 1. a member that does not own the team: the new server is asked nothing -------------------------
	asked, fronted := startFront(t, to, nil, nil)
	res := m.move(r.terminalEnv(r.configDirB), "y\n", moveArgs(fronted, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "only the owner of this team on "+m.oldHost+" can move it") ||
		!strings.Contains(res.stderr, "Nothing was changed") || strings.Contains(res.stdout, "[y/N]") {
		t.Fatalf("the one command by a member that does not own the team: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if n := asked.asked(); n != 0 {
		t.Fatalf("the new server was asked %d requests by a member that may not move the team", n)
	}
	m.unmoved("a member that does not own the team")
	if _, err := os.Lstat(filepath.Join(r.configDirB, "teams", teamstore.Key("rest", fronted.url, r.teamRef))); err == nil {
		t.Fatalf("a refused move left a profile for the new server in the member's store")
	}

	// --- 2. the owner moves the team ---------------------------------------------------------------------
	res = m.move(m.terminal, "y\n", moveArgs(to, r.secretFile)...)
	if res.exit != 0 {
		t.Fatalf("the one command: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, need := range []string{
		"move team \"" + teamName + "\" (" + r.teamRef + ") from " + m.oldHost + " to " + hostOf(to),
		"took team \"" + teamName + "\" (" + r.teamRef + ") in on " + hostOf(to) + ": you own it there",
		"recorded on " + m.oldHost + ": team \"" + teamName + "\"",
		m.oldHost + " keeps the team as it is: nothing there was ended",
	} {
		// The question is asked on stderr, and the lines are on stdout.
		if !strings.Contains(res.stderr+res.stdout, need) {
			t.Errorf("the one command's question and lines lack %q:\nstdout: %s\nstderr: %s", need, res.stdout, res.stderr)
		}
	}
	if strings.Contains(res.stdout, "cleared the record on") {
		t.Errorf("a server that never held the team is said to have had a record cleared:\n%s", res.stdout)
	}
	m.movedFile(to)
	ticket, ticketFile := m.ticket()
	// The old server holds the record, and the new one holds none.
	if record := r.recordOn(r.configDir, from); !record.Moved || record.Adapter != "rest" || record.URL != to.url || record.Certificate != to.certificate {
		t.Fatalf("the record on the server the team left: %+v", record)
	}
	if record := r.recordOn(r.configDir, to); record.Moved {
		t.Fatalf("the server the team moved to records a move: %+v", record)
	}
	// The old server keeps the team as it was: both members are on its
	// roster and read the record there, the member with the session he holds.
	oldRoster := r.run(m.terminal, "", "adapter", "rest", "--profile", m.oldKey, "team", "members")
	if oldRoster.exit != 0 || len(jsonResult[roster](t, oldRoster.stdout).Members) != 2 {
		t.Fatalf("the roster of the server the team left: exit %d\nstdout: %s\nstderr: %s", oldRoster.exit, oldRoster.stdout, oldRoster.stderr)
	}
	if record := r.recordOn(r.configDirB, from); !record.Moved || record.URL != to.url {
		t.Fatalf("the record as the member reads it on the server the team left: %+v", record)
	}

	// --- 3. a team file that names an address the record does not ----------------------------------------
	moved := m.pointFileAt("https://127.0.0.1:1", "")
	m.staysPut("a team file that names another address than the record")
	m.putFile(moved)

	// --- 4. the record cleared: nobody follows; the same command records it again ------------------------
	res = m.move(m.terminal, "", "team", "move", "--clear")
	if res.exit != 0 || !strings.Contains(res.stdout, "cleared: "+m.oldHost+" no longer says where team") {
		t.Fatalf("team move --clear: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if record := r.recordOn(r.configDirB, from); record.Moved {
		t.Fatalf("the record after the clear: %+v", record)
	}
	m.staysPut("after the record was cleared")
	res = m.move(m.terminal, "y\n", moveArgs(to, r.secretFile)...)
	if res.exit != 0 || !strings.Contains(res.stdout, "was on "+hostOf(to)+" already, and you own it there") ||
		!strings.Contains(res.stdout, "opened its move again, which replaced the ticket it had") || !strings.Contains(res.stdout, "recorded on "+m.oldHost) {
		t.Fatalf("the same command run again: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(to)
	again, _ := m.ticket()
	if again == ticket {
		t.Fatalf("the second run recorded the ticket the first had: the move was not opened again")
	}

	// --- 5. the member's next session follows at its start ----------------------------------------------
	m.followsAndTalks(to)

	// --- 6. no secret where it does not travel ----------------------------------------------------------
	m.alsoHidden = append(m.alsoHidden, hidden{name: "the ticket of the first run", value: ticket, files: []string{ticketFile}})
	m.noSecrets(again, ticketFile)
	for _, backend := range []restBackend{from, to} {
		if backend.dir == "" {
			continue
		}
		said, err := os.ReadFile(filepath.Join(backend.dir, "server.log"))
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]string{"the first ticket": secretPart(ticket), "the ticket": secretPart(again), "the join secret": secretPart(m.secret)} {
			if strings.Contains(string(said), value) {
				t.Errorf("%s is in what the server at %s said", name, hostOf(backend))
			}
		}
		if backend == from && !strings.Contains(string(said), "recorded that team "+teamName+" ("+r.teamRef+") moved to "+to.url) {
			t.Errorf("the server the team left did not say where it went:\n%s", said)
		}
	}
}

// TestRunningSessionsFollowAMoveFromOneServerToAnotherThroughTheRealBinary:
// the sessions that are running when a team moves from one server to
// another, the owner's and a member's, move at their next prompts (ADR
// 0008), as they do when the team leaves any other backend.
//
//  1. Both sessions are running on the first server, each with a watcher.
//  2. The owner runs the one command in a terminal. Neither session moves.
//  3. The member's first prompt says that the file names another backend
//     and starts the follow; a later one says the follow's line and
//     registers the session on the new server under the name it had. The
//     one after that says nothing.
//  4. The owner's session moves at its next prompt on her own command:
//     nothing is followed, and the line says so.
//  5. A new watcher serves each session, the old ones are gone, and
//     messages reach both on the new server, each once.
//  6. The first server lists neither session as online any more, and still
//     holds the team and its record.
//
// A session moves at a prompt that has time to register it, which on a
// loaded machine is not always the first: steps 3 and 4 prompt until it has
// moved (moved), and hold what those prompts said, each line once.
func TestRunningSessionsFollowAMoveFromOneServerToAnotherThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	from, to := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, from)
	m := newMovingTeam(t, r, r.restAdapterAs(r.configDir))
	newKey := teamstore.Key("rest", to.url, r.teamRef)
	where := hostOf(to)
	if to.certificate != "" {
		where += ", trusting the server certificate " + to.certificate
	}
	const (
		rejoined   = `this checkout is joined to team "` + teamName + `" at `
		movedWords = "now names another backend for this session's team"
		recorded   = "the team's administrator recorded that move"
	)

	// --- 1. the sessions that are running when the team moves --------------------------------------------
	aliceSock, bobSock := fakesock.New(t), fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", aliceName, aliceSock.Path(), token)
	bob := r.newSession("bob", "", bobName, bobSock.Path(), token)
	old, oldWatcher := map[int]string{}, map[int]int{}
	for _, s := range []session{alice, bob} {
		m.start(s)
		at := r.mustMap(s)
		if at.TeamKey != m.oldKey || at.TeamRef != r.teamRef || !sessionmap.IsBundledRest(at.AdapterCommand) {
			t.Fatalf("a session before the move: %+v", at)
		}
		old[s.pid], oldWatcher[s.pid] = at.BrigadeSessionID, r.liveWatcher(s).PID
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(alice, "watch ready") && r.logHas(bob, "watch ready") })
	unmoved := func(s session, when string) {
		t.Helper()
		if at := r.mustMap(s); at.TeamKey != m.oldKey || at.BrigadeSessionID != old[s.pid] {
			t.Fatalf("%s: the session's map names %q and %q", when, at.TeamKey, at.BrigadeSessionID)
		}
	}
	prompt := func(s session) result {
		t.Helper()
		res := r.restPrompt(s)
		m.said.WriteString(res.stdout + res.stderr)
		return res
	}
	// moved prompts s until its session is registered on the new server, and
	// returns what those prompts said (promptedUntilMoved).
	moved := func(s session) string {
		t.Helper()
		return r.promptedUntilMoved(s, newKey, prompt).stdout
	}

	// --- 2. the owner moves the team from her terminal ---------------------------------------------------
	res := m.move(m.terminal, "y\n", moveArgs(to, r.secretFile)...)
	if res.exit != 0 || !strings.Contains(res.stdout, ": you own it there") || !strings.Contains(res.stdout, "recorded on "+m.oldHost) {
		t.Fatalf("the one command: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(to)
	for _, s := range []session{alice, bob} {
		unmoved(s, "after the command, before any prompt")
	}

	// --- 3. the member's session follows in two prompts, by the first server's record --------------------
	res = prompt(bob)
	if !strings.Contains(res.stdout, movedWords) || !strings.Contains(res.stderr, "started the follow of the team's move") || strings.Contains(res.stdout, hostOf(to)) {
		t.Fatalf("the member's first prompt after the move: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	unmoved(bob, "at the prompt that started the follow")
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		pin, ok, err := teamstore.LookupPin(r.configDirB, m.canon)
		return err == nil && ok && pin.URL == to.url
	})
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return logLines(filepath.Join(r.stateDir, "logs", "team-move.log"), "followed the team's recorded move") == 1
	})
	if pin := m.pinOf(r.configDirB); pin.Adapter != "rest" || pin.Certificate != to.certificate || pin.MovedFrom != m.oldPin.URL {
		t.Fatalf("the member's pin after the follow: %+v", pin)
	}
	said := moved(bob)
	bobMap := r.mustMap(bob)
	if strings.Count(said, `followed team "`+teamName+`" to `+where) != 1 || !strings.Contains(said, recorded+" on "+m.oldHost) ||
		!strings.Contains(said, "with the move's ticket, under the label you had") ||
		strings.Count(said, `this session is "`+bobName+`" (`+bobMap.BrigadeSessionID+`)`) != 1 || strings.Contains(said, bobLabel) {
		t.Fatalf("the prompts that moved the member's session: stdout %q", said)
	}
	if bobMap.TeamKey != newKey || bobMap.BrigadeSessionID == old[bob.pid] || bobMap.SessionName != bobName || !sessionmap.IsBundledRest(bobMap.AdapterCommand) {
		t.Fatalf("the member's map after it moved: %+v", bobMap)
	}
	if res := prompt(bob); strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, movedWords) {
		t.Fatalf("the member's third prompt said the move again: stdout %q", res.stdout)
	}

	// --- 4. the owner's session moves on her own command ------------------------------------------------
	said = moved(alice)
	aliceMap := r.mustMap(alice)
	if strings.Count(said, rejoined+where+" now, and this session moves there with it.") != 1 ||
		strings.Count(said, `this session is "`+aliceName+`" (`+aliceMap.BrigadeSessionID+`)`) != 1 ||
		strings.Contains(said, "followed") || strings.Contains(said, recorded) || strings.Contains(said, "not connected") {
		t.Fatalf("the prompts of the owner's session: stdout %q", said)
	}
	if aliceMap.TeamKey != newKey || aliceMap.BrigadeSessionID == old[alice.pid] || aliceMap.SessionName != aliceName {
		t.Fatalf("the owner's map after it moved: %+v", aliceMap)
	}

	// --- 5. new watchers, the old ones gone, and messages on the new server ------------------------------
	for _, s := range []session{alice, bob} {
		at := r.mustMap(s)
		testutil.Eventually(t, waitLong, pollEvery, func() bool {
			return r.liveWatcher(s).BrigadeSessionID == at.BrigadeSessionID && !alive(oldWatcher[s.pid]) && r.logCount(s, "watch ready") >= 2
		})
	}
	sent := jsonResult[sendResult](t, r.mustRun(r.restEnv(alice), "you are on the new server now", "send", bobMap.BrigadeSessionID, "--summary", "moved", "--json").stdout)
	frames := bobSock.WaitFrames(1, waitLong)
	if p, err := frame.Parse(frames[len(frames)-1]); err != nil || len(frames) != 1 || p.MessageID != sent.MessageID ||
		p.ReplyToSessionID != aliceMap.BrigadeSessionID || p.FromLabel != aliceLabel+" (unverified)" {
		t.Fatalf("the frame that reached the member's session on the new server: %d frames, %+v, %v", len(frames), p, err)
	}
	sent = jsonResult[sendResult](t, r.mustRun(r.restEnv(bob), "so am I", "send", aliceMap.BrigadeSessionID, "--summary", "moved", "--json").stdout)
	frames = aliceSock.WaitFrames(1, waitLong)
	if p, err := frame.Parse(frames[len(frames)-1]); err != nil || len(frames) != 1 || p.MessageID != sent.MessageID ||
		p.ReplyToSessionID != bobMap.BrigadeSessionID || p.FromLabel != bobLabel+" (unverified)" {
		t.Fatalf("the frame that reached the owner's session on the new server: %d frames, %+v, %v", len(frames), p, err)
	}

	// --- 6. the first server: the sessions that left are not online, and the team is still there ----------
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		res := r.run(m.terminal, "", "adapter", "rest", "--profile", m.oldKey, "session", "list")
		return res.exit == 0 && !strings.Contains(res.stdout, old[alice.pid]) && !strings.Contains(res.stdout, old[bob.pid])
	})
	if record := r.recordOn(r.configDirB, from); !record.Moved || record.URL != to.url {
		t.Fatalf("the record on the server the team left, as the member reads it after he followed: %+v", record)
	}
	members := m.serverRoster(r.configDir, to)
	if len(members) != 2 || members[aliceLabel] == "" || members[bobLabel] == "" {
		t.Fatalf("the team's roster on the new server: %v", members)
	}
	watchers := []int{r.liveWatcher(alice).PID, r.liveWatcher(bob).PID}
	for _, s := range []session{alice, bob} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(watchers[0]) && !alive(watchers[1]) })
	ticket, ticketFile := m.ticket()
	m.noSecrets(ticket, ticketFile)
}

// TestATeamThatComesBackToAServerClearsTheRecordOfItsLeavingThroughTheRealBinary:
// a team moves from server A to server B and then back to A. A still
// records that the team left it for B, and that record is cleared by the
// move back, before B is asked to record anything: the two servers never
// name each other, so no session is sent back and forth.
//
//  1. A to B, and the member follows.
//  2. The move back while A cannot clear its own record: the command stops,
//     says which server holds what, and B records nothing. The team file
//     and every pin are as they were.
//  3. The same command again, A answering: A's record is cleared, B
//     records the move, the file names A.
//  4. The member's next session follows back to A, where his machine is a
//     member already: it joins nothing. A session after that stays on A
//     and says nothing of a move.
func TestATeamThatComesBackToAServerClearsTheRecordOfItsLeavingThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	// Server A is reached through a front, so that the test can keep it
	// from clearing its record for a while: every member knows it by the
	// front's address from the start.
	var refuseClear atomic.Bool
	_, a := startFront(t, startRestBackend(t), func(req *http.Request, body []byte) (int, string) {
		if refuseClear.Load() && req.URL.Path == "/v1/team/move" && strings.Contains(string(body), `"clear":true`) {
			return http.StatusServiceUnavailable, unreachable
		}
		return 0, ""
	}, nil)
	b := startRestBackend(t)
	r := teamOnAServer(t, a)

	// --- 1. A to B, and the member follows ---------------------------------------------------------------
	out := newMovingTeam(t, r, r.restAdapterAs(r.configDir))
	res := out.move(out.terminal, "y\n", moveArgs(b, r.secretFile)...)
	if res.exit != 0 {
		t.Fatalf("the move from A to B: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	out.movedFile(b)
	out.followsAndTalks(b)
	if record := r.recordOn(r.configDir, a); !record.Moved || record.URL != b.url {
		t.Fatalf("A's record after the team left it: %+v", record)
	}

	// --- 2. the move back, while A cannot clear its record ------------------------------------------------
	back := newMovingTeam(t, r, r.restAdapterAs(r.configDir))
	if back.oldPin.URL != b.url || back.oldHost != hostOf(b) {
		t.Fatalf("the team before the move back is not on B: %+v", back.oldPin)
	}
	bobsPin := back.pinOf(r.configDirB)
	refuseClear.Store(true)
	res = back.move(back.terminal, "y\n", moveArgs(a, r.secretFile)...)
	if res.exit == 0 {
		t.Fatalf("the move back went through while A could not clear its record:\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
	if need := "This machine has joined the team on " + hostOf(a) + " before: if " + hostOf(a) + " records that the team left it, that record is cleared first"; !strings.Contains(res.stderr+res.stdout, need) {
		t.Errorf("the question of the move back lacks %q:\nstdout: %s\nstderr: %s", need, res.stdout, res.stderr)
	}
	// The clear got no answer, which is not the same as a record that
	// stands: the refusal says that it may be cleared, and where to read it.
	for _, need := range []string{
		"the team is taken in on " + hostOf(a) + ", which recorded that the team left it for " + hostOf(b),
		"the answer to the clear of that record did not arrive, so the record may be cleared all the same",
		"`brigade team move --show --from " + hostOf(a) + "` says", "Its move was not opened",
		hostOf(b) + " was asked to record nothing: it holds what it held, and no session follows this move",
		"Run the same command again to finish the move",
	} {
		if !strings.Contains(res.stderr, need) {
			t.Errorf("the refusal of the move back lacks %q:\n%s", need, res.stderr)
		}
	}
	// Which server holds what: A its record of the team's leaving, B none.
	if record := r.recordOn(r.configDir, a); !record.Moved || record.URL != b.url {
		t.Fatalf("A's record after a move back that stopped: %+v", record)
	}
	if record := r.recordOn(r.configDir, b); record.Moved {
		t.Fatalf("B recorded a move back that stopped before it: %+v", record)
	}
	if got := back.fileNow(); got != back.fileBefore {
		t.Fatalf("a move back that stopped rewrote the team file:\n%s", got)
	}
	if pin := back.pinOf(r.configDir); pin != back.oldPin {
		t.Fatalf("a move back that stopped rewrote the owner's pin: %+v", pin)
	}

	// --- 3. the same command again, with A answering -----------------------------------------------------
	refuseClear.Store(false)
	res = back.move(back.terminal, "y\n", moveArgs(a, r.secretFile)...)
	if res.exit != 0 {
		t.Fatalf("the move back: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, need := range []string{
		"was on " + hostOf(a) + " already, and you own it there",
		"cleared the record on " + hostOf(a) + " that the team had left it for " + hostOf(b),
		"the team is on " + hostOf(a) + " again",
		"recorded on " + hostOf(b) + ": team \"" + teamName + "\"",
	} {
		if !strings.Contains(res.stdout, need) {
			t.Errorf("the move back's lines lack %q:\n%s", need, res.stdout)
		}
	}
	// The cleared record is said before the new one is made.
	if cleared, recorded := strings.Index(res.stdout, "cleared the record on "), strings.Index(res.stdout, "recorded on "+hostOf(b)); cleared < 0 || recorded < cleared {
		t.Errorf("the move back does not say the cleared record before the new one:\n%s", res.stdout)
	}
	back.movedFile(a)
	// The two servers do not name each other: A says nothing of a move, B
	// says that the team went to A.
	if record := r.recordOn(r.configDir, a); record.Moved {
		t.Fatalf("A still records that the team left it, after the team came back: %+v", record)
	}
	if record := r.recordOn(r.configDir, b); !record.Moved || record.URL != a.url || record.Certificate != a.certificate {
		t.Fatalf("B's record of the move back: %+v", record)
	}

	// --- 4. the member follows back, joins nothing, and stays -------------------------------------------
	if pin := back.pinOf(r.configDirB); pin != bobsPin {
		t.Fatalf("the member's pin moved before his session followed back: %+v", pin)
	}
	keyA := teamstore.Key("rest", a.url, r.teamRef)
	bob := r.newSession("bob", bobName, "", "", "")
	res = back.start(bob)
	if !strings.Contains(res.stdout, `followed team "`+teamName+`" to `+hostOf(a)) || !strings.Contains(res.stdout, "recorded that move on "+hostOf(b)) ||
		!strings.Contains(res.stdout, "this machine had joined it there already") || strings.Contains(res.stdout, "with the move's ticket") {
		t.Fatalf("the session that followed back: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if at := r.mustMap(bob); at.TeamKey != keyA {
		t.Fatalf("the member's map after he followed back: %+v", at)
	}
	if pin := back.pinOf(r.configDirB); pin.URL != a.url || pin.MovedFrom != b.url {
		t.Fatalf("the member's pin after he followed back: %+v", pin)
	}
	// One membership of each on A, as before the team ever left.
	if members := back.serverRoster(r.configDir, a); len(members) != 2 || members[aliceLabel] == "" || members[bobLabel] == "" {
		t.Fatalf("A's roster after the team came back: %v", members)
	}
	r.restHook(bob, "session-end", bob.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	// A session after that is where the team is, and is sent nowhere.
	next := r.newSession("bob", bobName, "", "", "")
	res = back.start(next)
	if strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, "not connected") || !strings.Contains(res.stdout, `in team "`+teamName+`"`) {
		t.Fatalf("a session after the team came back: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if at := r.mustMap(next); at.TeamKey != keyA {
		t.Fatalf("a session after the team came back is not on A: %+v", at)
	}
	if pin := back.pinOf(r.configDirB); pin.URL != a.url {
		t.Fatalf("the member's pin moved again: %+v", pin)
	}
	r.restHook(next, "session-end", next.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
}

// TestAMemberWhoMissedAMoveDoesNotFollowTheNextThroughTheRealBinary is
// what the page of the move says of a chain, held against the running
// thing (work item 341): a team moves from the backend it was created on
// to server A and, before a member's machine has followed, on to server B.
//
//  1. The member's next session, in a checkout whose file names B, is not
//     followed anywhere: the backend he joined records a move to A, which
//     is not what the file names. It says that the file does not match.
//  2. In a checkout whose file is the one of the first move, naming A, his
//     session follows to A, for as long as A's move is open.
//  3. With the file that names B again, his next session follows from A to
//     B: a chain is followed one move at a time, each on the record of the
//     backend the member's pin names.
//
// The other road, which needs no intermediate file, is the join with the
// secret's file: TestATeamOnAServerThroughTheRealBinary holds a join.
func TestAMemberWhoMissedAMoveDoesNotFollowTheNextThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	a, b := startRestBackend(t), startRestBackend(t)

	// The first move, from the backend the team was created on to A.
	first := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	if res := first.move(first.terminal, "y\n", moveArgs(a, r.secretFile)...); res.exit != 0 {
		t.Fatalf("the first move: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	first.movedFile(a)
	fileOfA := first.fileNow()
	// The second, from A to B, before the member's machine has followed the first.
	second := newMovingTeam(t, r, r.restAdapterAs(r.configDir))
	if res := second.move(second.terminal, "y\n", moveArgs(b, r.secretFile)...); res.exit != 0 {
		t.Fatalf("the second move: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	second.movedFile(b)
	fileOfB := second.fileNow()
	if pin := first.pinOf(r.configDirB); pin.Adapter != first.oldPin.Adapter || pin.URL != first.oldPin.URL {
		t.Fatalf("the member's pin before any session of his: %+v", pin)
	}

	// --- 1. the file names B; the backend he joined says A: nothing is followed --------------------------
	first.staysPut("a member who missed the first move, with the file of the second")
	if _, err := os.Lstat(filepath.Join(r.configDirB, "teams", teamstore.Key("rest", b.url, r.teamRef))); err == nil {
		t.Fatalf("a member who missed the first move holds a profile for the second server")
	}

	// --- 2. the file of the first move: he follows to A --------------------------------------------------
	second.putFile(fileOfA)
	bob := r.newSession("bob", bobName, "", "", "")
	res := first.start(bob)
	if !strings.Contains(res.stdout, `followed team "`+teamName+`" to `+hostOf(a)) || !strings.Contains(res.stdout, "with the move's ticket, under the label you had") {
		t.Fatalf("the member's session with the file of the first move: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if pin := first.pinOf(r.configDirB); pin.URL != a.url || pin.MovedFrom != first.oldPin.URL {
		t.Fatalf("the member's pin after the first follow: %+v", pin)
	}
	r.restHook(bob, "session-end", bob.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))

	// --- 3. the file of the second move: he follows from A to B ------------------------------------------
	second.putFile(fileOfB)
	bob = r.newSession("bob", bobName, "", "", "")
	res = second.start(bob)
	if !strings.Contains(res.stdout, `followed team "`+teamName+`" to `+hostOf(b)) || !strings.Contains(res.stdout, "recorded that move on "+teammove.Host(a.url)) ||
		!strings.Contains(res.stdout, "with the move's ticket, under the label you had") {
		t.Fatalf("the member's session with the file of the second move: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if at := r.mustMap(bob); at.TeamKey != teamstore.Key("rest", b.url, r.teamRef) {
		t.Fatalf("the member's map after the second follow: %+v", at)
	}
	if pin := second.pinOf(r.configDirB); pin.URL != b.url || pin.MovedFrom != a.url {
		t.Fatalf("the member's pin after the second follow: %+v", pin)
	}
	if members := second.serverRoster(r.configDir, b); len(members) != 2 || members[aliceLabel] == "" || members[bobLabel] == "" {
		t.Fatalf("the roster of the second server: %v", members)
	}
	r.restHook(bob, "session-end", bob.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
}

// copyDir copies a directory tree, files and directories with their modes.
func copyDir(t *testing.T, from, to string) {
	t.Helper()
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatalf("copying %s: %v", from, err)
	}
}

// TestAServerStartedFromACopyOfTheDataIsNoPlaceToMoveATeamToThroughTheRealBinary
// is what the operators' page says of the server a team moves to, held
// against the running thing (work item 341): it must not be a copy of the
// old server's data directory at a new address. Such a server holds the
// team already, with its members and with the owner the old one has, and a
// member's machine knows it by its address as a server it never joined. So
// the one command's take-in is the join of a new member. The command finds
// the membership it has on the old server on the copy's roster, says that
// the server is the same one under another address or a copy of its data,
// and leaves with the membership the take-in made: the copy's roster is
// what it was copied with, and nothing is recorded on the server the team
// is on. It never names the operator's `--set-owner`: followed on a copy,
// that would move the team there and put every member on it twice. It
// needs the built server, whose data directory is what is copied.
func TestAServerStartedFromACopyOfTheDataIsNoPlaceToMoveATeamToThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	bin := os.Getenv(serverBinVar)
	if bin == "" {
		t.Skip("needs a built brigade-server: make server-test names it in " + serverBinVar)
	}
	from := startServer(t, bin)
	r := teamOnAServer(t, from)
	m := newMovingTeam(t, r, r.restAdapterAs(r.configDir))
	// The copy, taken while the server is quiet: everything it answered is
	// in its directory by then. It serves the certificate the copy holds.
	copied := t.TempDir()
	copyDir(t, filepath.Join(from.dir, "data"), filepath.Join(copied, "data"))
	to := startServerOn(t, bin, copied)
	if to.certificate != from.certificate || to.url == from.url {
		t.Fatalf("the copy serves %s under %s, and the server it was copied from %s under %s", to.url, to.certificate, from.url, from.certificate)
	}

	res := m.move(m.terminal, "y\n", moveArgs(to, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, hostOf(to)+" holds team \""+teamName+"\" ("+r.teamRef+") with the membership this machine has on "+hostOf(from)) ||
		!strings.Contains(res.stderr, "the same server under another address, or a copy of its data") ||
		!strings.Contains(res.stderr, "The membership this command made on "+hostOf(to)+" to find that out is ended") ||
		strings.Contains(res.stderr, "--set-owner") || !strings.Contains(res.stderr, "Nothing was recorded, and "+teamfile.FileName+" is unchanged") {
		t.Fatalf("the one command to a copy of the server's data: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a move to a copy of the server's data")
	// The administrator's machine holds no profile for the copy: the
	// membership the take-in made was ended, and its profile removed.
	m.notJoined("a move to a copy of the server's data", to)
	// The copy holds the two members it was copied with, each once. It is
	// read with the membership the copy holds for the member, under a
	// profile made for the copy's address from the one of the old server:
	// the credential is the same, because the data are.
	fromDir, copyDir2 := filepath.Join(r.configDirB, "teams", teamstore.Key("rest", from.url, r.teamRef)), filepath.Join(r.configDirB, "teams", teamstore.Key("rest", to.url, r.teamRef))
	// A member's store is the member's alone: each file is written 0600.
	if err := os.Mkdir(copyDir2, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.ReadDir(fromDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range held {
		if !entry.Type().IsRegular() {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(fromDir, entry.Name())) //nolint:gosec // G304: a file of the test's own store
		if rerr != nil {
			t.Fatal(rerr)
		}
		//nolint:gosec // G703: a file name read from the test's own store, written into the same store
		if werr := os.WriteFile(filepath.Join(copyDir2, entry.Name()), data, 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	if err := write.PatchBindingBackend(r.configDirB, teamstore.Key("rest", to.url, r.teamRef), "rest", to.url, "", to.certificate); err != nil {
		t.Fatal(err)
	}
	listed := r.run(r.terminalEnv(r.configDirB), "", "adapter", "rest", "--profile", teamstore.Key("rest", to.url, r.teamRef), "team", "members")
	if listed.exit != 0 {
		t.Fatalf("the copy's roster: exit %d\nstdout: %s\nstderr: %s", listed.exit, listed.stdout, listed.stderr)
	}
	labels := map[string]int{}
	for _, member := range jsonResult[roster](t, listed.stdout).Members {
		labels[member.HumanLabel]++
	}
	if labels[aliceLabel] != 1 || labels[bobLabel] != 1 || len(labels) != 2 {
		t.Fatalf("the copy's roster after the refused move: %v, want each member once", labels)
	}
}
