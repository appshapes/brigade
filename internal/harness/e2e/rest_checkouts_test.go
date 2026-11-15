//go:build !windows

package e2e

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// TestAMoveAcrossAMembersCheckoutsThroughTheRealBinary is a team's move as
// a member with several checkouts lives it (work item 327, ADR 0008),
// through the built `brigade`, the fs adapter for the backend the team
// leaves and the built server it goes to: what the second review of the
// work held against real processes where its proof was a unit seam. It
// needs the server itself, so it runs only where `make server-test` names
// one.
//
//  1. Bob has five checkouts of the team and four running sessions with a
//     watcher each: in the first checkout, in the second, in the third, and
//     in one that never gets the changed file.
//  2. A session that starts while the file has no record behind it is
//     attached to nothing. Its registration retry starts the follow as a
//     child and says once that the backend bob joined is being asked.
//  3. A follow that is stopped while it holds the follow's lock leaves the
//     lock free: the next one is not kept waiting.
//  4. Three checkouts follow in the same instant, each by a child, and a
//     session starts in a fourth and follows in its own hook: the machine
//     joins the server once, with the ticket, every pin is written, one
//     follow says that it joined and three that the machine had, and the
//     team has two members.
//  5. The session attached to nothing registers at its next prompt, in the
//     checkout the retry resolved.
//  6. A session is its own checkout's wherever it works. A session whose
//     checkout followed moves at a prompt sent from another checkout, one
//     that is still on the old backend. Cleared from that checkout it
//     stays on the server; and the session of that checkout, cleared and
//     prompted from one that is on the server, stays on the old backend.
//     Each says the context line of the session where it is.
//  7. A prompt hook that moved a session is killed before it printed
//     anything. The session's next prompt says the move: how the checkout
//     arrived and the context line with the id the session has now.
//  8. The server stops. `/clear` in a session that has not moved yet keeps
//     it where it is, on the backend that still serves it: it says that it
//     could not be registered, never that it is not connected, and the
//     prompts of the next minute do not try again.
func TestAMoveAcrossAMembersCheckoutsThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	bin := os.Getenv(serverBinVar)
	if bin == "" {
		t.Skip("needs a built brigade-server: make server-test names it in " + serverBinVar)
	}
	r := newRig(t)
	r.createTeam()
	pinOf := func(checkout string) teamstore.Pin {
		t.Helper()
		pin, ok, err := teamstore.LookupPin(r.configDirB, checkout)
		if err != nil || !ok {
			t.Fatalf("the pin of %s: %v %v", checkout, ok, err)
		}
		return *pin
	}
	canon, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldPin := r.teamKey, pinOf(canon)
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
	lockPath := filepath.Join(r.configDirB, "team-move.lock")
	const (
		retryWords    = "names another backend for the team you joined here"
		driftWords    = "does not match the team you joined here"
		unreached     = "could not be registered on the backend its team moved to"
		followStarted = "started the follow of the team's move"
		notFollowed   = "the team file names another backend for this team, and the move was not followed"
		byTicket      = "with its move ticket"
	)
	setFile := func(checkout string, file []byte) {
		t.Helper()
		//nolint:gosec // G306: a committed team file is 0644
		if err := os.WriteFile(filepath.Join(checkout, teamfile.FileName), file, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// --- 1. bob's checkouts, and the sessions that are running when the team moves -----------------------------
	// checkoutOf is another checkout of bob's, joined to the team on the old backend as the first is.
	checkoutOf := func(name string) (dir, canonical string) {
		t.Helper()
		dir = filepath.Join(r.dirs.Root, name)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		setFile(dir, oldFile)
		canonical, err := teamfile.Canonicalize(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := write.Pin(r.configDirB, canonical, oldPin); err != nil {
			t.Fatal(err)
		}
		return dir, canonical
	}
	dir2, canon2 := checkoutOf("checkout-2")
	dir3, canon3 := checkoutOf("checkout-3")
	dir4, canon4 := checkoutOf("checkout-4")
	behind, canonBehind := checkoutOf("checkout-behind")
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	first := r.newSession("bob", "", bobName, fakesock.New(t).Path(), token)
	second := r.newSession("bob", "", "billing-second", fakesock.New(t).Path(), token)
	second.cwd = dir2
	third := r.newSession("bob", "", "billing-third", fakesock.New(t).Path(), token)
	third.cwd = behind
	fifthSock := fakesock.New(t)
	fifth := r.newSession("bob", "", "billing-fifth", fifthSock.Path(), token)
	fifth.cwd = dir3
	alice := r.newSession("alice", "", aliceName, "", "")
	start := func(s session, source string) result {
		t.Helper()
		return r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": source}))
	}
	old := map[int]string{}
	watcher := map[int]int{}
	for _, s := range []session{first, second, third, fifth, alice} {
		start(s, "startup")
		m := r.mustMap(s)
		if m.TeamKey != oldKey || m.TeamRef != r.teamRef {
			t.Fatalf("a session before the move: %+v", m)
		}
		old[s.pid] = m.BrigadeSessionID
		if s.socket != "" {
			watcher[s.pid] = r.liveWatcher(s).PID
		}
	}
	// where asserts what a session's map names: the backend, the session, its checkout and its conversation.
	where := func(s session, when, key, id, checkout string) {
		t.Helper()
		if m := r.mustMap(s); m.TeamKey != key || m.BrigadeSessionID != id || m.Checkout != checkout || m.ClaudeSessionID != s.nativeID {
			t.Fatalf("%s: the map names %q, %q, the checkout %q and the conversation %q\nwant %q, %q, %q and %q",
				when, m.TeamKey, m.BrigadeSessionID, m.Checkout, m.ClaudeSessionID, key, id, checkout, s.nativeID)
		}
	}

	// --- the operator takes the team in and opens its move, with the server stopped, then starts it ------------
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
	newKey := teamstore.Key("rest", backend.url, r.teamRef)
	journal := func() string {
		text, _ := os.ReadFile(filepath.Join(backend.dir, "server.log"))
		return string(text)
	}
	moved, err := json.Marshal(map[string]any{
		"version": 1, "adapter": "rest", "url": backend.url, "certificate": backend.certificate, "team_ref": r.teamRef, "team_name": teamName,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Four of bob's checkouts get the changed file; the fifth never does.
	for _, checkout := range []string{r.checkout, dir2, dir3, dir4} {
		setFile(checkout, append(moved, '\n'))
	}

	// --- 2. a session that starts before the record is there says that the old backend is being asked ---------
	orphan := r.newSession("bob", "billing-orphan", "", "", "")
	if res := start(orphan, "startup"); !strings.Contains(res.stdout, driftWords) || r.mapExists(orphan) {
		t.Fatalf("a start with no record behind the file: stdout %q", res.stdout)
	}
	res := r.restPrompt(orphan)
	if !strings.Contains(res.stdout, retryWords) || !strings.Contains(res.stdout, driftWords) || !strings.Contains(res.stderr, followStarted) ||
		strings.Contains(res.stdout, strings.TrimPrefix(backend.url, "https://")) || strings.Contains(res.stdout, backend.certificate) {
		t.Fatalf("the retry of a session attached to nothing: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return logLines(moveLog, notFollowed) == 1 })
	if res := r.restPrompt(orphan); strings.Contains(res.stdout, retryWords) || strings.Contains(res.stderr, followStarted) || r.mapExists(orphan) {
		t.Fatalf("a second prompt within the minute: stdout %q\nstderr %s", res.stdout, res.stderr)
	}

	// --- the move is made: the administrator joins the server from her running session and records it ---------
	if res := r.restJoin(alice, aliceLabel); res.exit != 0 {
		t.Fatalf("the administrator's join: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if res := r.runIn(r.checkout, r.terminalEnv(r.configDir), "y\n", "team", "move", "--ticket-file", ticketFile); res.exit != 0 {
		t.Fatalf("the record: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	r.restPrompt(alice)
	if m := r.mustMap(alice); m.TeamKey != newKey {
		t.Fatalf("the administrator's session after her join: %+v", m)
	}
	// follow is the child a prompt hook starts for a checkout, as that hook starts it: no session in its
	// environment, and its arguments.
	follow := func(checkout string, key int) []string {
		args := []string{"hook", "follow", "--checkout", checkout, "--config-dir", r.configDirB, "--state-dir", r.stateDir, "--log-level", "debug"}
		if key > 0 {
			args = append(args, "--key", strconv.Itoa(key))
		}
		return args
	}

	// --- 3. a follow that is stopped while it holds the lock leaves it free ------------------------------------
	// The old backend's adapter is made to hang on the follow's one question, which is asked under the lock.
	hungPID := filepath.Join(r.dirs.Root, "hung.pid")
	hang := filepath.Join(r.dirs.Root, "adapter-hangs.sh")
	writeExecutable(t, hang, "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = moved ]; then\n    echo $$ > "+shellQuote(hungPID)+
		"\n    exec /bin/sleep 300\n  fi\ndone\nexec "+shellQuote(r.fsAdapter)+" \"$@\"\n")
	if err := config.RegisterAdapter(r.configDirB, "fs", []string{hang, "--root", r.root}); err != nil {
		t.Fatal(err)
	}
	stopped := r.startDetached(r.baseEnv(), filepath.Join(r.dirs.Root, "stopped.out"), filepath.Join(r.dirs.Root, "stopped.err"), follow(canon3, 0)...)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		_, err := os.Stat(hungPID)
		return err == nil
	})
	if lock, err := adapterkit.LockFile(lockPath, 300*time.Millisecond); err == nil {
		_ = lock.Unlock()
		t.Fatalf("the follow that is asking the old backend does not hold the follow's lock")
	}
	if err := stopped.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-stopped.done
	lock, err := adapterkit.LockFile(lockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("the lock of a follow that was stopped while it held it: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	// The adapter it left asking is stopped too, and the old backend answers again.
	if text, err := os.ReadFile(hungPID); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(text))); err == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if err := config.RegisterAdapter(r.configDirB, "fs", r.adapterCmd); err != nil {
		t.Fatal(err)
	}
	if pin := pinOf(canon3); pin != oldPin || strings.Contains(journal(), byTicket) {
		t.Fatalf("a follow that was stopped before the old backend answered moved the pin or joined: %+v", pin)
	}

	// --- 4. three checkouts follow in the same instant, and a session starts in a fourth ----------------------
	type ran struct {
		exit           int
		stdout, stderr string
		err            error
	}
	launch := func(env []string, stdin string, args ...string) ran {
		//nolint:gosec // G204: the argv is a binary this test built; no shell is involved
		cmd := exec.CommandContext(t.Context(), r.brigade, args...)
		cmd.Env, cmd.Dir = env, r.dirs.Home
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		res := ran{stdout: out.String(), stderr: errb.String()}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.exit = exitErr.ExitCode()
		} else {
			res.err = err
		}
		return res
	}
	fourth := r.newSession("bob", "billing-fourth", "", "", "")
	fourth.cwd = dir4
	children := []string{canon, canon2, canon3}
	results := make([]ran, len(children)+1)
	var all sync.WaitGroup
	gate := make(chan struct{})
	for i, checkout := range children {
		// The first checkout's child was started for the session that is attached to nothing.
		key := 0
		if checkout == canon {
			key = orphan.pid
		}
		all.Go(func() {
			<-gate
			results[i] = launch(r.baseEnv(), "", follow(checkout, key)...)
		})
	}
	all.Go(func() {
		<-gate
		results[len(children)] = launch(r.restEnv(fourth), fourth.hookDoc("SessionStart", map[string]any{"source": "startup"}), "hook", "session-start")
	})
	began := time.Now()
	close(gate)
	all.Wait()
	t.Logf("three follows and a start that follows, at once, took %v", time.Since(began).Round(time.Millisecond))
	for i, res := range results[:len(children)] {
		if res.err != nil || res.exit != 0 || res.stdout != "" || strings.Contains(res.stderr, ticket) {
			t.Fatalf("the follow of %s: exit %d, %v\nstdout: %s\nstderr: %s", children[i], res.exit, res.err, res.stdout, res.stderr)
		}
	}
	started := results[len(children)]
	if started.err != nil || started.exit != 0 || !strings.Contains(started.stdout, "followed team") || !strings.Contains(started.stdout, `this session is "billing-fourth" (`) ||
		strings.Contains(started.stdout+started.stderr, ticket) {
		t.Fatalf("the start that followed: exit %d, %v\nstdout: %s\nstderr: %s", started.exit, started.err, started.stdout, started.stderr)
	}
	where(fourth, "the session that started in the fourth checkout", newKey, r.mustMap(fourth).BrigadeSessionID, canon4)
	// One join, by the ticket, and the team has two members.
	if n := strings.Count(journal(), byTicket); n != 1 {
		t.Fatalf("the machine joined the server by the ticket %d times, want once\n%s", n, journal())
	}
	res = r.runIn(r.checkout, r.terminalEnv(r.configDir), "", "team", "members", "--json")
	if members := jsonResult[roster](t, res.stdout).Members; res.exit != 0 || len(members) != 2 {
		t.Fatalf("the team's members on the server: exit %d, %d of them, want alice and bob\n%s", res.exit, len(members), res.stdout)
	}
	// Every pin is written, and the follows that had nobody to tell left word: one of the four says that the
	// machine joined with the ticket, and the others that it had joined already.
	saidJoined := 0
	if strings.Contains(started.stdout, "with the move's ticket") {
		saidJoined++
	}
	for _, checkout := range append(children, canon4) {
		if pin := pinOf(checkout); pin.Adapter != "rest" || pin.URL != backend.url || pin.Certificate != backend.certificate || pin.MovedFrom != oldPin.URL {
			t.Fatalf("the pin of %s after the follows: %+v", checkout, pin)
		}
		if checkout == canon4 {
			continue
		}
		data, err := os.ReadFile(config.MoveFollowedMark(r.stateDir, r.configDirB, checkout))
		if err != nil {
			t.Fatalf("the mark of the follow of %s: %v", checkout, err)
		}
		var mark struct {
			Joined bool `json:"joined"`
		}
		if err := json.Unmarshal(data, &mark); err != nil {
			t.Fatalf("the mark of the follow of %s: %v\n%s", checkout, err, data)
		}
		if mark.Joined {
			saidJoined++
		}
	}
	if saidJoined != 1 {
		t.Fatalf("%d of the four follows say that the machine joined with the ticket, want one", saidJoined)
	}
	if lock, err = adapterkit.LockFile(lockPath, 2*time.Second); err != nil {
		t.Fatalf("the follow's lock after the follows: %v", err)
	}
	_ = lock.Unlock()
	if left, _ := filepath.Glob(filepath.Join(r.configDirB, ".move-ticket-*")); len(left) != 0 {
		t.Fatalf("a follow left its ticket file: %v", left)
	}
	if pin := pinOf(canonBehind); pin != oldPin {
		t.Fatalf("the checkout that never got the changed file: %+v", pin)
	}

	// --- 5. the session attached to nothing registers at its next prompt --------------------------------------
	// The child that followed its checkout took the retry's stamp away, so the prompt is not held back.
	res = r.restPrompt(orphan)
	orphanMap := r.mustMap(orphan)
	if !strings.Contains(res.stdout, "followed team") || !strings.Contains(res.stdout, `this session is "billing-orphan" (`+orphanMap.BrigadeSessionID+`)`) ||
		strings.Contains(res.stdout, retryWords) || strings.Contains(res.stdout, driftWords) {
		t.Fatalf("the prompt after the follow, of the session that was attached to nothing: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	where(orphan, "the session that was attached to nothing", newKey, orphanMap.BrigadeSessionID, canon)

	// --- 6. a session is its own checkout's at a start as at a prompt ------------------------------------------
	// Bob's first session is still on the old backend, its checkout has followed, and it is working elsewhere:
	// its prompt comes from the checkout that never got the changed file. Nothing about a move is read from
	// the directory a prompt names, so the session moves with its own checkout all the same, and its map still
	// names that checkout.
	working := first
	working.cwd = behind
	res = r.restPrompt(working)
	firstMap := r.mustMap(first)
	if firstMap.TeamKey != newKey || firstMap.BrigadeSessionID == old[first.pid] || !strings.Contains(res.stdout, `this session is "`+bobName+`" (`+firstMap.BrigadeSessionID+`)`) {
		t.Fatalf("the first session after a prompt from another checkout: stdout %q\nmap %+v", res.stdout, firstMap)
	}
	where(first, "after a prompt from a checkout still on the old backend", newKey, firstMap.BrigadeSessionID, canon)
	// A prompt from a directory that is no checkout at all has nothing more to say.
	working.cwd = r.dirs.Home
	if res := r.restPrompt(working); strings.Contains(res.stdout, "followed team") || strings.Contains(res.stdout, "this session is") {
		t.Fatalf("a prompt from a directory that is no checkout, after the move: stdout %q", res.stdout)
	}
	where(first, "after a prompt from a directory that is no checkout", newKey, firstMap.BrigadeSessionID, canon)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return r.liveWatcher(first).BrigadeSessionID == firstMap.BrigadeSessionID && !alive(watcher[first.pid])
	})
	watcher[first.pid] = r.liveWatcher(first).PID
	joins, listed := strings.Count(journal(), byTicket), len(r.serverRoster(alice))
	// Cleared from the checkout that is still on the old backend, it stays on the server.
	first.cwd, first.nativeID = behind, first.nativeID+"-cleared"
	res = start(first, "clear")
	if !strings.Contains(res.stdout, `this session is "`+bobName+`" (`+firstMap.BrigadeSessionID+`)`) || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("/clear of the moved session from a checkout still on the old backend: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	where(first, "after a /clear from a checkout still on the old backend", newKey, firstMap.BrigadeSessionID, canon)
	// And the session of that checkout, cleared from one that is on the server, stays on the old backend.
	third.cwd, third.nativeID = r.checkout, third.nativeID+"-cleared"
	res = start(third, "clear")
	if !strings.Contains(res.stdout, `this session is "billing-third" (`+old[third.pid]+`)`) || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("/clear of a session that has not moved from a checkout that is on the server: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	where(third, "after a /clear from a checkout that is on the server", oldKey, old[third.pid], canonBehind)
	if w := r.liveWatcher(third); w.PID != watcher[third.pid] || w.BrigadeSessionID != old[third.pid] {
		t.Fatalf("the watcher of the session that stayed: %+v", w)
	}
	// A prompt where each now is, in the other's checkout, moves neither: the pin there matches the file there,
	// and it is not the session's own.
	for _, s := range []session{first, third} {
		if res := r.restPrompt(s); strings.Contains(res.stdout, "this session moves there") || strings.Contains(res.stdout, "this session is") {
			t.Fatalf("a prompt from the other's checkout: stdout %q", res.stdout)
		}
	}
	where(first, "after a prompt from a checkout still on the old backend", newKey, firstMap.BrigadeSessionID, canon)
	where(third, "after a prompt from a checkout that is on the server", oldKey, old[third.pid], canonBehind)
	if got := strings.Count(journal(), byTicket); got != joins || len(r.serverRoster(alice)) != listed {
		t.Fatalf("the two starts joined or registered on the server: %d joins by ticket, %d sessions listed, want %d and %d", got, len(r.serverRoster(alice)), joins, listed)
	}
	// A `/clear` in a directory that is no checkout at all resolves no team, and the session that is running is
	// kept all the same: its map names the conversation that began, and it says the context line and nothing of
	// a session that is not connected.
	third.cwd, third.nativeID = r.dirs.Home, third.nativeID+"-again"
	res = start(third, "clear")
	if !strings.Contains(res.stdout, `this session is "billing-third" (`+old[third.pid]+`)`) || strings.Contains(res.stdout, "not connected") || strings.Contains(res.stdout, "not joined") {
		t.Fatalf("/clear in a directory with no team file: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	where(third, "after a /clear in a directory with no team file", oldKey, old[third.pid], canonBehind)
	// A prompt of each, back in its own checkout, changes nothing either.
	first.cwd, third.cwd = r.checkout, behind
	for _, s := range []session{first, third} {
		if res := r.restPrompt(s); strings.Contains(res.stdout, "followed team") || strings.Contains(res.stdout, "this session is") {
			t.Fatalf("a prompt after the /clear: stdout %q", res.stdout)
		}
	}
	where(first, "after its prompt", newKey, firstMap.BrigadeSessionID, canon)
	where(third, "after its prompt", oldKey, old[third.pid], canonBehind)

	// --- 7. a hook that is stopped after it moved the session: the next prompt says the move -------------------
	// The fifth session's checkout followed in step 4, and the session has not been prompted since. Its prompt
	// hook is stopped where Claude Code's timeout would stop one: after it wrote the session's new map, before
	// it printed anything. The watcher's log is made a pipe that nobody reads, and the hook opens it to start
	// the watcher, which is the first thing it does once the map is written: it waits there until it is killed.
	watcherLog := filepath.Join(r.stateDir, "logs", "watcher-"+strconv.Itoa(fifth.pid)+".log")
	if err := os.Rename(watcherLog, watcherLog+".before"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(watcherLog, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(watcherLog) })
	//nolint:gosec // G204: the argv is a binary this test built; no shell is involved
	hook := exec.CommandContext(t.Context(), r.brigade, "hook", "prompt")
	hook.Env, hook.Dir = r.restEnv(fifth), r.dirs.Home
	hook.Stdin = strings.NewReader(fifth.hookDoc("UserPromptSubmit", map[string]any{"permission_mode": "default"}))
	var printed bytes.Buffer
	hook.Stdout = &printed
	if err := hook.Start(); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.mustMap(fifth).TeamKey == newKey })
	fifthMap := r.mustMap(fifth)
	notice := config.MovedNotice(r.stateDir, fifth.pid)
	if _, err := os.Stat(notice); err != nil || fifthMap.BrigadeSessionID == old[fifth.pid] {
		t.Fatalf("the hook that moved the session left no notice before it registered it: %v\nmap %+v", err, fifthMap)
	}
	if err := hook.Process.Kill(); err != nil {
		t.Fatalf("the hook had ended before it could be stopped: %v", err)
	}
	if err := hook.Wait(); err == nil || printed.Len() != 0 {
		t.Fatalf("the hook was not stopped before it printed: %v, stdout %q", err, printed.String())
	}
	if err := os.Remove(watcherLog); err != nil {
		t.Fatal(err)
	}
	// The session has a new id and was told nothing, and no watcher serves that id yet. A teammate who reads
	// the roster writes to it there: the message waits on the server.
	gap := jsonResult[sendResult](t, r.mustRun(r.restEnv(alice), "are you there", "send", fifthMap.BrigadeSessionID, "--summary", "gap", "--json").stdout)
	if got := fifthSock.Frames(); len(got) != 0 {
		t.Fatalf("a message reached the session before any watcher served its new id: %q", got)
	}
	// Its next prompt says the move.
	res = r.restPrompt(fifth)
	if !strings.Contains(res.stdout, "followed team") || !strings.Contains(res.stdout, `this session is "billing-fifth" (`+fifthMap.BrigadeSessionID+`)`) {
		t.Fatalf("the prompt after the hook that was stopped: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if _, err := os.Stat(notice); err == nil {
		t.Fatalf("the notice outlived the prompt that said the move")
	}
	where(fifth, "after the prompt that said the move", newKey, fifthMap.BrigadeSessionID, canon3)
	// That prompt started the watcher that serves the session on the server, beside the one the session had,
	// which was still alive or had seen within its tick that the map names another session: no further prompt
	// is needed for the session to be served, and the watcher it had is gone. No prompt says the move again.
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		v, err := pidfile.Check(r.pidfilePath(fifth), procutil.Lookup)
		return err == nil && v.Found && v.Alive && v.Entry.BrigadeSessionID == fifthMap.BrigadeSessionID && !alive(watcher[fifth.pid])
	})
	// The message that waited is delivered by that watcher, once, and a later prompt delivers it no second time.
	frames := fifthSock.WaitFrames(1, waitLong)
	if p, err := frame.Parse(frames[len(frames)-1]); err != nil || len(frames) != 1 || p.MessageID != gap.MessageID {
		t.Fatalf("the message sent while no watcher served the session: %d frames, %+v, %v", len(frames), p, err)
	}
	if res := r.restPrompt(fifth); strings.Contains(res.stdout, "followed team") || strings.Contains(res.stdout, "this session is") {
		t.Fatalf("a later prompt said the move again: stdout %q", res.stdout)
	}
	watcher[fifth.pid] = r.liveWatcher(fifth).PID

	// --- 8. the server stops: /clear keeps a session that has not moved, and says so ---------------------------
	// The sessions that are on the server end first, while it can still be told.
	for _, s := range []session{first, fifth, orphan, fourth, alice} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(watcher[first.pid]) && !alive(watcher[fifth.pid]) })
	if got := fifthSock.Frames(); len(got) != 1 {
		t.Fatalf("the message sent while no watcher served the fifth session reached it %d times, want once", len(got))
	}
	stopServer(t, backend)
	// Bob's second session was never prompted: it is on the old backend, its checkout on the server.
	where(second, "the second session before its /clear", oldKey, old[second.pid], canon2)
	second.nativeID += "-cleared"
	res = start(second, "clear")
	if !strings.Contains(res.stdout, "followed team") || !strings.Contains(res.stdout, unreached) ||
		!strings.Contains(res.stdout, `this session is "billing-second" (`+old[second.pid]+`)`) || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("/clear with the server stopped: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	where(second, "after a /clear with the server stopped", oldKey, old[second.pid], canon2)
	if w := r.liveWatcher(second); w.PID != watcher[second.pid] || w.BrigadeSessionID != old[second.pid] {
		t.Fatalf("the watcher of the session that was kept: %+v", w)
	}
	if _, err := os.Stat(config.RegisterRetryStamp(r.stateDir, second.pid)); err != nil {
		t.Fatalf("the registration that failed left no stamp: %v", err)
	}
	// Within the minute a prompt does not try again, and says neither line a second time.
	res = r.restPrompt(second)
	if strings.Contains(res.stdout, "followed team") || strings.Contains(res.stdout, unreached) || strings.Contains(res.stderr, "registering this session on the backend it is on now") {
		t.Fatalf("a prompt within the minute: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	where(second, "after its prompt", oldKey, old[second.pid], canon2)

	// --- the sessions that stayed end ---------------------------------------------------------------------------
	for _, s := range []session{second, third} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(watcher[second.pid]) && !alive(watcher[third.pid]) })
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
