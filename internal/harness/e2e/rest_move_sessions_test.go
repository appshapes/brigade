//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// TestRunningSessionsFollowAMoveMadeByOneCommandThroughTheRealBinary is the
// two works in one tree: the move by one command (work item 328, ADR 0007)
// made from a running session of the administrator's, and the sessions that
// are running at the time, hers and a member's, moving at their next prompts
// (work item 327, ADR 0008). The command rewrites the team file and the
// checkout's pin under sessions that are still attached to the backend the
// team left; what each of them then does and says is held here, through the
// built `brigade`, the fs adapter for the backend the team leaves and a real
// server for the one it goes to (the stand-in under `make test`, the built
// server where `make server-test` names one).
//
//  1. Four sessions are running on the old backend: the administrator's
//     session that will run the command, a second one of hers in the same
//     checkout that holds a message, a third of hers in a second checkout of
//     the team, and a member's.
//  2. The command runs in her session. That session is where it was until
//     its next prompt, which moves it on her own command: nothing is asked
//     of the old backend, nothing is "followed", and the line is said once.
//  3. Her session that holds a message stays with it, says so once, and
//     moves after the message is released.
//  4. The member's session follows in two prompts, with the move's ticket,
//     and messages reach both sessions on the server, each once.
//  5. Her second checkout has the old team file: its session stays. When
//     the new file arrives there it follows by the record, as a member's
//     checkout does, and joins nothing: the machine is the team's owner on
//     the server already.
//  6. One owner and one member are on the server's roster at the end.
//
// A session moves at a prompt that has time to register it, which on a
// loaded machine is not always the first: wherever a prompt moves a session
// here, the session is prompted until it has moved (promptedUntilMoved),
// and what those prompts said is held, each line once.
func TestRunningSessionsFollowAMoveMadeByOneCommandThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	backend := startRestBackend(t)
	newKey := teamstore.Key("rest", backend.url, r.teamRef)
	where := strings.TrimPrefix(strings.TrimPrefix(backend.url, "https://"), "http://")
	if backend.certificate != "" {
		where += ", trusting the server certificate " + backend.certificate
	}
	const (
		rejoined   = `this checkout is joined to team "` + teamName + `" at `
		movedWords = "now names another backend for this session's team"
		recorded   = "the team's administrator recorded that move"
		waits      = "1 team message is held for it there"
	)

	// --- 1. the sessions that are running when the team moves -------------------------------------------------
	// A second checkout of the administrator's, joined to the team on the old backend as the first is.
	otherDir := filepath.Join(r.dirs.Root, "checkout-other")
	if err := os.MkdirAll(filepath.Join(otherDir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(otherDir, teamfile.FileName), []byte(m.fileBefore), 0o644); err != nil {
		t.Fatal(err)
	}
	canonOther, err := teamfile.Canonicalize(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(r.configDir, canonOther, m.oldPin); err != nil {
		t.Fatal(err)
	}
	sock, heldSock, bobSock := fakesock.New(t), fakesock.New(t), fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	mover := r.newSession("alice", "", aliceName, sock.Path(), token)
	held := r.newSession("alice", "", "payments-held", heldSock.Path(), token)
	other := r.newSession("alice", "payments-other", "", "", "")
	other.cwd = otherDir
	bob := r.newSession("bob", "", bobName, bobSock.Path(), token)
	old := map[int]string{}
	for _, s := range []session{mover, held, other, bob} {
		m.start(s)
		at := r.mustMap(s)
		if at.TeamKey != m.oldKey || at.TeamRef != r.teamRef {
			t.Fatalf("a session before the move: %+v", at)
		}
		old[s.pid] = at.BrigadeSessionID
	}
	oldWatcher := r.liveWatcher(mover)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return r.logHas(mover, "watch ready") && r.logHas(held, "watch ready") && r.logHas(bob, "watch ready")
	})
	// One of hers asks not to be messaged, and is sent a message all the same, which its watcher holds.
	r.mustRun(r.restEnv(held), "", "inbound", "hold")
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(held, "inbound policy changed") })
	r.mustRun(r.restEnv(bob), "read this when you can", "send", old[held.pid], "--summary", "held", "--json")
	pending := func() int {
		file, err := inbound.FilePendingStore{Path: inbound.PendingPath(r.stateDir, old[held.pid]), SessionID: old[held.pid]}.Load()
		if err != nil {
			return -1
		}
		return len(file.Entries)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return pending() == 1 })
	// The questions the old backend was asked about a move: every `team moved` its adapter was run with.
	questions := func() int {
		argv, _ := os.ReadFile(r.argvLog)
		return strings.Count(string(argv), "\nteam\nmoved\n")
	}
	unmoved := func(s session, when string) {
		t.Helper()
		if at := r.mustMap(s); at.TeamKey != m.oldKey || at.BrigadeSessionID != old[s.pid] {
			t.Fatalf("%s: the session's map names %q and %q", when, at.TeamKey, at.BrigadeSessionID)
		}
	}
	// said is everything the hooks of this test printed, for the secrets' check at the end.
	prompt := func(s session) result {
		t.Helper()
		res := r.restPrompt(s)
		m.said.WriteString(res.stdout + res.stderr)
		return res
	}

	// --- 2. the administrator moves the team from her running session, which moves at its next prompt ---------
	res := m.move(r.restEnv(mover), "", moveArgs(backend, r.secretFile)...)
	if res.exit != 0 || !strings.Contains(res.stdout, "Consented by this invocation.") || !strings.Contains(res.stdout, ": you own it there") {
		t.Fatalf("the move from her running session: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(backend)
	// The command wrote the checkout and no session: every one of them is where it was.
	for _, s := range []session{mover, held, other, bob} {
		unmoved(s, "after the command, before any prompt")
	}
	asked := questions()
	res = r.promptedUntilMoved(mover, newKey, prompt)
	moverMap := r.mustMap(mover)
	if !strings.Contains(res.stdout, rejoined+where+" now, and this session moves there with it.") ||
		strings.Count(res.stdout, `this session is "`+aliceName+`" (`+moverMap.BrigadeSessionID+`)`) != 1 ||
		strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, recorded) || strings.Contains(res.stdout, movedWords) ||
		strings.Contains(res.stdout, "not connected") || strings.Count(res.stdout, rejoined) != 1 {
		t.Fatalf("the prompts of the session that made the move: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if moverMap.TeamKey != newKey || moverMap.BrigadeSessionID == old[mover.pid] || moverMap.SessionName != aliceName {
		t.Fatalf("her map after it moved: %+v", moverMap)
	}
	if questions() != asked {
		t.Fatalf("her own move asked the old backend whether the team moved")
	}
	if res := prompt(mover); strings.Contains(res.stdout, rejoined) || strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, movedWords) {
		t.Fatalf("a second prompt said the move again: stdout %q", res.stdout)
	}
	// A new watcher serves it; the old one is gone, and closed the old session where it was registered.
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return r.liveWatcher(mover).BrigadeSessionID == moverMap.BrigadeSessionID && !alive(oldWatcher.PID)
	})
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.sessionFile(old[mover.pid]).ClosedAt != nil })
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logCount(mover, "watch ready") >= 2 })

	// --- 3. her session that holds a message stays with it, and says so once ----------------------------------
	res = prompt(held)
	if !strings.Contains(res.stdout, rejoined+where) || !strings.Contains(res.stdout, waits) ||
		strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, recorded) {
		t.Fatalf("the prompt of her session that holds a message: stdout %q", res.stdout)
	}
	unmoved(held, "with a message held")
	if res := prompt(held); strings.Contains(res.stdout, rejoined) || strings.Contains(res.stdout, "stays on the backend") {
		t.Fatalf("a later prompt said the lines again: stdout %q", res.stdout)
	}
	unmoved(held, "with a message held, a prompt later")
	if pending() != 1 {
		t.Fatalf("the held message after two prompts: %d pending", pending())
	}

	// --- 4. the member's session follows in two prompts, by the record, with the move's ticket ----------------
	asked = questions()
	res = prompt(bob)
	if !strings.Contains(res.stdout, movedWords) || !strings.Contains(res.stderr, "started the follow of the team's move") ||
		strings.Contains(res.stdout, where) {
		t.Fatalf("the member's first prompt after the move: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	unmoved(bob, "at the prompt that started the follow")
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		pin, ok, err := teamstore.LookupPin(r.configDirB, m.canon)
		return err == nil && ok && pin.Adapter == "rest"
	})
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return logLines(filepath.Join(r.stateDir, "logs", "team-move.log"), "followed the team's recorded move") == 1
	})
	if pin := m.pinOf(r.configDirB); pin.URL != backend.url || pin.Certificate != backend.certificate || pin.MovedFrom != m.oldPin.URL {
		t.Fatalf("the member's pin after the follow: %+v", pin)
	}
	res = r.promptedUntilMoved(bob, newKey, prompt)
	bobMap := r.mustMap(bob)
	if strings.Count(res.stdout, `followed team "`+teamName+`" to `+where) != 1 || !strings.Contains(res.stdout, recorded+" on "+m.oldHost) ||
		!strings.Contains(res.stdout, "with the move's ticket, under the label you had") ||
		strings.Count(res.stdout, `this session is "`+bobName+`" (`+bobMap.BrigadeSessionID+`)`) != 1 || strings.Contains(res.stdout, bobLabel) {
		t.Fatalf("the prompts that moved the member's session: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if bobMap.TeamKey != newKey || bobMap.BrigadeSessionID == old[bob.pid] || bobMap.SessionName != bobName {
		t.Fatalf("the member's map after it moved: %+v", bobMap)
	}
	if got := questions() - asked; got != 1 {
		t.Fatalf("the member's follow asked the old backend %d times, want once", got)
	}
	if res := prompt(bob); strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, movedWords) {
		t.Fatalf("the member's third prompt said the move again: stdout %q", res.stdout)
	}
	// The two sessions talk on the server, and each message arrives once.
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return r.liveWatcher(bob).BrigadeSessionID == bobMap.BrigadeSessionID && r.logCount(bob, "watch ready") >= 2
	})
	sent := jsonResult[sendResult](t, r.mustRun(r.restEnv(mover), "you are on the server now", "send", bobMap.BrigadeSessionID, "--summary", "moved", "--json").stdout)
	frames := bobSock.WaitFrames(1, waitLong)
	if p, err := frame.Parse(frames[len(frames)-1]); err != nil || len(frames) != 1 || p.MessageID != sent.MessageID ||
		p.ReplyToSessionID != moverMap.BrigadeSessionID || p.FromLabel != aliceLabel+" (unverified)" {
		t.Fatalf("the frame that reached the member's session on the server: %d frames, %+v, %v", len(frames), p, err)
	}
	sent = jsonResult[sendResult](t, r.mustRun(r.restEnv(bob), "so am I", "send", moverMap.BrigadeSessionID, "--summary", "moved", "--json").stdout)
	frames = sock.WaitFrames(1, waitLong)
	if p, err := frame.Parse(frames[len(frames)-1]); err != nil || len(frames) != 1 || p.MessageID != sent.MessageID ||
		p.ReplyToSessionID != bobMap.BrigadeSessionID || p.FromLabel != bobLabel+" (unverified)" {
		t.Fatalf("the frame that reached her session on the server: %d frames, %+v, %v", len(frames), p, err)
	}

	// --- 5. her second checkout: the old file keeps its session where it is; the new file is followed ----------
	asked = questions()
	if res := prompt(other); res.stdout != "" {
		t.Fatalf("a prompt in her second checkout, whose team file is the old one: stdout %q", res.stdout)
	}
	unmoved(other, "in a checkout whose team file is the old one")
	if pin := func() teamstore.Pin {
		pin, ok, err := teamstore.LookupPin(r.configDir, canonOther)
		if err != nil || !ok {
			t.Fatalf("the second checkout's pin: %v %v", ok, err)
		}
		return *pin
	}(); pin != m.oldPin {
		t.Fatalf("the second checkout's pin moved with the first: %+v", pin)
	}
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(otherDir, teamfile.FileName), []byte(m.fileNow()), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := prompt(other); !strings.Contains(res.stdout, movedWords) || !strings.Contains(res.stderr, "started the follow of the team's move") {
		t.Fatalf("the prompt in her second checkout after the new file arrived: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		pin, ok, err := teamstore.LookupPin(r.configDir, canonOther)
		return err == nil && ok && pin.Adapter == "rest" && pin.MovedFrom == m.oldPin.URL
	})
	res = r.promptedUntilMoved(other, newKey, prompt)
	otherMap := r.mustMap(other)
	// The line is the one a member's checkout says, the administrator's own record named as "the team's
	// administrator" recorded it: her machine was on the server already, and joined nothing.
	if strings.Count(res.stdout, `followed team "`+teamName+`" to `+where) != 1 || !strings.Contains(res.stdout, "this machine had joined it there already") ||
		strings.Contains(res.stdout, "with the move's ticket") || otherMap.TeamKey != newKey || otherMap.SessionName != "payments-other" {
		t.Fatalf("the prompts that moved the session of her second checkout: stdout %q\nmap %+v", res.stdout, otherMap)
	}
	if got := questions() - asked; got != 1 {
		t.Fatalf("her second checkout's follow asked the old backend %d times, want once", got)
	}

	// --- 6. the held message is released on the backend it was sent on, and that session moves then -----------
	if res := r.runIn(r.checkout, m.terminal, "", "inbox", "release", "--session", old[held.pid], "--all"); res.exit != 0 {
		t.Fatalf("inbox release: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if got := heldSock.WaitFrames(1, waitLong); len(got) != 1 || !strings.Contains(got[0], "read this when you can") {
		t.Fatalf("the released message did not reach the session once: %q", got)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return pending() == 0 })
	res = r.promptedUntilMoved(held, newKey, prompt)
	heldMap := r.mustMap(held)
	if heldMap.TeamKey != newKey || heldMap.BrigadeSessionID == old[held.pid] || heldMap.Inbound != "hold" ||
		strings.Count(res.stdout, `this session is "payments-held" (`+heldMap.BrigadeSessionID+`)`) != 1 || strings.Contains(res.stdout, rejoined) {
		t.Fatalf("her session after its message was released: stdout %q\nmap %+v", res.stdout, heldMap)
	}

	// --- 7. one owner and one member on the server, every session of the run listed there, none of the old ----
	members := m.serverRoster(r.configDir, backend)
	if len(members) != 2 || members[aliceLabel] == "" || members[bobLabel] == "" || members[terminalAccount] != "" {
		t.Fatalf("the team's roster on the server: %v", members)
	}
	listed := r.serverRoster(mover)
	for what, id := range map[string]string{"her session that made the move": moverMap.BrigadeSessionID, "her session that held a message": heldMap.BrigadeSessionID,
		"the session of her second checkout": otherMap.BrigadeSessionID, "the member's session": bobMap.BrigadeSessionID} {
		if _, ok := listed[id]; !ok {
			t.Errorf("the server does not know %s: %v", what, listed)
		}
	}
	for _, id := range old {
		if _, ok := listed[id]; ok {
			t.Errorf("the server lists %s, a session of the old backend", id)
		}
	}
	if backend.dir != "" {
		// The built server's journal: one join by the move's ticket, the member's.
		journal, _ := os.ReadFile(filepath.Join(backend.dir, "server.log"))
		if got := strings.Count(string(journal), "with its move ticket"); got != 1 {
			t.Errorf("the server took %d joins by the move's ticket, want the member's one", got)
		}
	}
	watchers := []int{r.liveWatcher(mover).PID, r.liveWatcher(held).PID, r.liveWatcher(bob).PID}
	for _, s := range []session{mover, held, other, bob} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(watchers[0]) && !alive(watchers[1]) && !alive(watchers[2]) })
	ticket, ticketRead := m.ticket()
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
	if hits := tokenHits(t, token, r.dirs.Root); len(hits) != 0 {
		t.Errorf("the messaging token is in %v", hits)
	}
}
