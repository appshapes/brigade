//go:build !windows

package e2e

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// What the one command of a team's move (work item 328, ADR 0006) leaves
// when it is run where a move already stands and when it is killed, through
// the built `brigade`, the fs adapter for the backend the team leaves and a
// real server: the stand-in under `make test`, the built server over TLS
// where `make server-test` names one. rest_move_test.go holds the move
// itself and its refusals; these are the cases beside it.

// recordJSON is the record on the backend the team leaves as `team move
// --show --json` prints it: where the team went and when that was recorded,
// so two readings that are equal are one record, never one that was cleared
// and made again.
func (m *movingTeam) recordJSON() string {
	m.r.t.Helper()
	res := m.move(m.terminal, "", "team", "move", "--show", "--from", m.oldHost, "--json")
	if res.exit != 0 {
		m.r.t.Fatalf("team move --show --json: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	return strings.TrimSpace(res.stdout)
}

// recorded is what the record on the backend the team leaves names, as
// `team move --show --json` prints it, its date left out: a record that was
// written again as it was names the same.
func (m *movingTeam) recorded() string {
	m.r.t.Helper()
	record := jsonResult[protocol.TeamMoveResult](m.r.t, m.recordJSON())
	if !record.Moved {
		m.r.t.Fatalf("the backend the team leaves holds no record")
	}
	return record.Adapter + " " + record.URL + " " + record.Certificate
}

// stored is the record as the fs adapter keeps it, read from the store's
// own file: every member of it but its date, the ticket among them, and its
// date. Two readings whose members are equal are one record byte for byte,
// whatever became of its date.
func (m *movingTeam) stored() (members map[string]string, at time.Time) {
	t := m.r.t
	t.Helper()
	text, err := os.ReadFile(filepath.Join(m.r.root, "teams", m.r.teamRef, "move.json"))
	if err != nil {
		t.Fatalf("the record in the fs adapter's store: %v", err)
	}
	if err := json.Unmarshal(text, &members); err != nil {
		t.Fatalf("the record in the fs adapter's store is not an object of strings: %v", err)
	}
	if at, err = time.Parse(time.RFC3339Nano, members["recorded_at"]); err != nil {
		t.Fatalf("the date of the record in the fs adapter's store: %v", err)
	}
	delete(members, "recorded_at")
	if members["ticket"] == "" || members["url"] == "" {
		t.Fatalf("the record in the fs adapter's store names no ticket or no address")
	}
	return members, at
}

// clears counts the `team move --clear` the old backend's adapter was run
// with: the command's way to learn that the backend keeps records, which it
// may use only where there is no record to lose.
func (m *movingTeam) clears() int {
	m.r.t.Helper()
	argv, err := os.ReadFile(m.r.argvLog)
	if err != nil {
		m.r.t.Fatalf("the adapter wrapper recorded no argv: %v", err)
	}
	return strings.Count(string(argv), "\nteam\nmove\n--clear\n")
}

// hostOf is a server as a line of the command names it.
func hostOf(backend restBackend) string {
	return strings.TrimPrefix(strings.TrimPrefix(backend.url, "https://"), "http://")
}

// TestARecordThatExistsIsNotClearedByAMoveThroughTheRealBinary: the command
// learns whether its caller may record a move, and whether the backend the
// team leaves keeps records at all, before it asks the server anything.
// Where there is no record it clears one that is not there. A record that
// is there is never cleared for that: it is recorded again as it is, which
// only the team's creator is answered. So the same command run again once
// the move is recorded, and a move to another server of a team that moved
// once before, each stopped after its checks at a take-in whose answer is
// lost, leave the record the first move made: the same destination and the
// same ticket, byte for byte in the store's own file, under a later date.
// A run whose check has nowhere to put the record's ticket stops with what
// to do about it, asks the server nothing and leaves the record to its
// date. Then the second move is let through, and replaces the record as
// the question said it would.
func TestARecordThatExistsIsNotClearedByAMoveThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	first, server := startLossyFront(t, startRestBackend(t))
	first.lose(false)
	// same fails the test unless the record is the one kept: what `--show` names, the ticket a member's
	// follow reads, and every member of the store's file but its date, which is later than after.
	same := func(what string, names, ticket string, kept map[string]string, after time.Time) time.Time {
		t.Helper()
		if got := m.recorded(); got != names {
			t.Fatalf("the record after %s names %s, want what the first move recorded: %s", what, got, names)
		}
		if held, _ := m.ticket(); held != ticket {
			t.Fatalf("the record's ticket changed in %s", what)
		}
		members, at := m.stored()
		if !maps.Equal(members, kept) {
			t.Fatalf("the record in the store after %s is not the one the first move made, its date aside", what)
		}
		if !at.After(after) {
			t.Fatalf("the record's date after %s is %s, want later than %s: the check records it again", what, at, after)
		}
		if got := m.clears(); got != 1 {
			t.Fatalf("%s cleared the record: %d clears", what, got)
		}
		return at
	}

	// --- 1. the move: its checks found no record, and asked the one clear that finds that out ------------
	res := m.move(m.terminal, "y\n", moveArgs(server, r.secretFile)...)
	if res.exit != 0 {
		t.Fatalf("the move: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(server)
	if got := m.clears(); got != 1 {
		t.Fatalf("the move's checks cleared %d times, want the one that finds a backend that keeps records", got)
	}
	names := m.recorded()
	ticket, _ := m.ticket()
	kept, made := m.stored()

	// --- 2. the same command again, stopped after its checks ---------------------------------------------
	first.lose(true)
	res = m.move(m.terminal, "y\n", moveArgs(server, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "did not take the team in") {
		t.Fatalf("the move run again, its take-in's answer lost: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	again := same("a second run that stopped", names, ticket, kept, made)
	m.movedFile(server)

	// --- 3. the check has nowhere to put the record's ticket: it says so, and nobody is asked ------------
	first.lose(false)
	dir := filepath.Join(r.stateDir, "team-move")
	if err := os.Remove(dir); err != nil {
		t.Fatalf("the ticket's directory after two runs is not an empty directory: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	asked, before := first.asked(), m.recordJSON()
	res = m.move(m.terminal, "y\n", moveArgs(server, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, m.oldHost+" holds a record of a move already") ||
		!strings.Contains(res.stderr, "a private file for its ticket, which could not be written") || !strings.Contains(res.stderr, dir) ||
		!strings.Contains(res.stderr, "and run this again. Nothing was changed") || strings.Contains(res.stderr, "[y/N]") {
		t.Fatalf("a run whose check cannot keep the record's ticket: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if got := first.asked(); got != asked {
		t.Fatalf("a run that stopped in its checks asked the server %d requests", got-asked)
	}
	if got := m.recordJSON(); got != before {
		t.Fatalf("a run that could not check the record changed it:\n%s\nwant:\n%s", got, before)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}

	// --- 4. a team that moved once before is moved to another server, and that run stops too -------------
	// The checkout is one that was kept at the commit before the move: its team file names the old backend.
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(r.checkout, teamfile.FileName), []byte(m.fileBefore), 0o644); err != nil {
		t.Fatal(err)
	}
	second, other := startLossyFront(t, startRestBackend(t))
	res = m.move(m.terminal, "y\n", moveArgs(other, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "did not take the team in") ||
		!strings.Contains(res.stderr, "This replaces the record "+m.oldHost+" holds of a move to "+hostOf(server)) {
		t.Fatalf("a second move, its take-in's answer lost: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	same("a move to another server that stopped", names, ticket, kept, again)
	if got := m.fileNow(); got != m.fileBefore {
		t.Fatalf("a second move that stopped rewrote the team file:\n%s", got)
	}

	// --- 5. the second move, let through: the record is replaced, as the question said --------------------
	second.lose(false)
	res = m.move(m.terminal, "y\n", moveArgs(other, r.secretFile)...)
	if res.exit != 0 || !strings.Contains(res.stderr, "This replaces the record "+m.oldHost+" holds of a move to "+hostOf(server)) {
		t.Fatalf("the second move: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(other)
	if got := m.clears(); got != 1 {
		t.Fatalf("the second move cleared the record it replaced: %d clears", got)
	}
	replaced, ticketRead := m.ticket()
	if replaced == ticket {
		t.Fatalf("the record holds the first move's ticket after the second move")
	}
	m.noSecrets(replaced, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
	if hits := tokenHits(t, ticket, r.dirs.Root); len(hits) != 0 {
		t.Errorf("the ticket of the move that was replaced is still in %v", hits)
	}
}

// A holdingFront is a front that, while it is told to, holds the answer to
// one path: it hands the request to the server, lets the server carry it
// out, says that it did and then keeps the member's machine waiting, until
// the test lets go or the machine hangs up. The answer is lost either way.
// It is how a test kills a command in the middle of a step, after the
// server acted and before the command knows.
type holdingFront struct {
	*front
	mu   sync.Mutex
	path string
	// reached gets one value for each held request the server carried out.
	reached chan struct{}
	// released ends every hold when it is closed, at the end of the test.
	released chan struct{}
}

func startHoldingFront(t *testing.T, backend restBackend) (*holdingFront, restBackend) {
	t.Helper()
	h := &holdingFront{reached: make(chan struct{}, 8), released: make(chan struct{})}
	var where restBackend
	h.front, where = startFront(t, backend, nil, func(req *http.Request, _ int) bool {
		h.mu.Lock()
		hold := h.path != "" && req.URL.Path == h.path
		h.mu.Unlock()
		if !hold {
			return false
		}
		h.reached <- struct{}{}
		select {
		case <-h.released:
		case <-req.Context().Done():
		}
		return true
	})
	// Registered after the front's own cleanup, so it runs before it: a
	// server does not close while a request is held.
	t.Cleanup(func() { close(h.released) })
	return h, where
}

// hold names the path whose answers are held from now on, "" for none.
func (h *holdingFront) hold(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.path = path
}

// A runningMove is one `brigade team move` the test started and did not
// wait for.
type runningMove struct {
	cmd *exec.Cmd
	// out is what it printed, stdout and stderr together, and is read once
	// done has answered.
	out bytes.Buffer
	// done gets what the command's end was, once.
	done chan error
}

// begin starts `brigade team move …` in the checkout, from the
// administrator's terminal, with `y` as her answer, and returns without
// waiting. The command is the leader of a process group of its own, so its
// adapter children are in it: a kill of the group takes all of them, as the
// kill of a terminal's job does.
func (m *movingTeam) begin(args ...string) *runningMove {
	t := m.r.t
	t.Helper()
	run := &runningMove{done: make(chan error, 1)}
	//nolint:gosec // G204: the argv is a binary this test built; no shell is involved
	run.cmd = exec.CommandContext(context.WithoutCancel(t.Context()), m.r.brigade, args...)
	run.cmd.Env, run.cmd.Dir = m.terminal, m.r.checkout
	run.cmd.Stdin = strings.NewReader("y\n")
	run.cmd.Stdout, run.cmd.Stderr = &run.out, &run.out
	run.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := run.cmd.Start(); err != nil {
		t.Fatalf("brigade %v: %v", args, err)
	}
	group := run.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })
	go func() { run.done <- run.cmd.Wait() }()
	return run
}

// kill ends a running move and its children with SIGKILL, which no process
// sees coming: nothing of the command runs after it, no deferred removal
// and no handler. It keeps what the command had printed.
func (m *movingTeam) kill(run *runningMove) {
	t := m.r.t
	t.Helper()
	if err := syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("the kill: %v", err)
	}
	err := <-run.done
	m.said.WriteString(run.out.String())
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatalf("the command ended on its own before it was killed (%v):\n%s", err, run.out.String())
	}
}

// reaches waits until reached answers, which says that the command got as
// far as the test means to kill it at. A command that ended before that, or
// that got nowhere, fails the test with what it printed.
func (m *movingTeam) reaches(run *runningMove, reached <-chan struct{}, what string) {
	t := m.r.t
	t.Helper()
	select {
	case <-reached:
	case err := <-run.done:
		t.Fatalf("the command ended before %s (%v):\n%s", what, err, run.out.String())
	case <-time.After(waitLong):
		_ = syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
		<-run.done
		t.Fatalf("the command did not get as far as %s:\n%s", what, run.out.String())
	}
}

// TestAMoveKilledInTheMiddleOfAStepIsFinishedByTheSameCommandThroughTheRealBinary:
// the one command is killed, with its adapter, while the server is taking
// the team in, while the server is opening the move and while the backend
// the team leaves is recording it. Each time the server or the backend has
// acted and the command never learns of it. Each kill leaves the checkout
// and the record as they were, and the last run, with nothing in its way,
// leaves what a move that was never killed leaves: one owner on the server
// and no second member, one record, a whole team file, and no ticket file.
//
// The third kill is also where the ticket's file is seen while it exists:
// under Brigade's state directory, in a directory that is the member's
// alone, mode 0600, holding a ticket of this team. A command that is killed
// there cannot remove it. The file stays, as private as it was, and the
// next run of the same move writes over it and removes it.
func TestAMoveKilledInTheMiddleOfAStepIsFinishedByTheSameCommandThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	gate, server := startHoldingFront(t, startRestBackend(t))
	args := moveArgs(server, r.secretFile)
	profile := filepath.Join(r.configDir, "teams", teamstore.Key("rest", server.url, r.teamRef))
	tickets := func() []string {
		left, err := filepath.Glob(filepath.Join(r.stateDir, "team-move", "*"))
		if err != nil {
			t.Fatal(err)
		}
		return left
	}

	// --- 1. killed while the server takes the team in ------------------------------------------------------
	gate.hold("/v1/team/take-in")
	run := m.begin(args...)
	m.reaches(run, gate.reached, "the server taking the team in")
	m.kill(run)
	m.unmoved("a move killed while the server took the team in")
	// The server holds the team and its owner, and this machine holds no credential for it: what it does hold
	// is the key the adapter wrote down before it asked, which makes the next run that owner.
	if info, err := os.Lstat(filepath.Join(profile, "take-in-key.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the retry key's file after the kill: %v, %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(profile, "credential.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a take-in that was killed left a credential: %v", err)
	}
	if left := tickets(); len(left) != 0 {
		t.Fatalf("a move that opened nothing left a ticket file: %v", left)
	}

	// --- 2. killed while the server opens the move ---------------------------------------------------------
	gate.hold("/v1/team/open-move")
	run = m.begin(args...)
	m.reaches(run, gate.reached, "the server opening the move")
	m.kill(run)
	m.unmoved("a move killed while the server opened it")
	// The take-in before it was answered: the same owner, and no second member.
	if members := m.serverRoster(r.configDir, server); len(members) != 1 || members[aliceLabel] == "" {
		t.Fatalf("the server's roster after a second take-in: %v", members)
	}
	// The ticket was in an answer that never arrived: it is in no file.
	if left := tickets(); len(left) != 0 {
		t.Fatalf("an open whose answer never arrived left a ticket file: %v", left)
	}

	// --- 3. killed while the backend the team leaves records the move --------------------------------------
	gate.hold("")
	asked := filepath.Join(r.dirs.Root, "record-asked")
	script := filepath.Join(r.dirs.Root, "adapter-waiting.sh")
	body := "verb=''; hit=0; prev=''\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$prev $a\" in 'team move') verb=move;; esac\n" +
		"  case \"$a\" in --ticket-file) hit=1;; esac\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"if [ \"$verb\" = move ] && [ \"$hit\" = 1 ]; then\n" +
		"  : > " + shellQuote(asked) + "\n" +
		"  exec /bin/sleep 120\n" +
		"fi\n" +
		"exec " + shellQuote(r.wrapper) + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.RegisterAdapter(r.configDir, "fs", []string{"/bin/sh", script, "--root", r.root}); err != nil {
		t.Fatal(err)
	}
	run = m.begin(args...)
	recording := make(chan struct{})
	go func() {
		defer close(recording)
		for {
			if _, err := os.Lstat(asked); err == nil {
				return
			}
			select {
			case <-gate.released:
				return
			case <-time.After(pollEvery):
			}
		}
	}()
	m.reaches(run, recording, "the old backend recording the move")
	// The ticket's file, while the record is being asked: one file, named after the team's key at the server.
	left := tickets()
	if len(left) != 1 || left[0] != filepath.Join(r.stateDir, "team-move", filepath.Base(profile)+".ticket") {
		t.Fatalf("the ticket's file while the move is recorded: %v", left)
	}
	held := func(when string) string {
		t.Helper()
		if info, err := os.Lstat(left[0]); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("the ticket's file %s: %v, %v", when, info, err)
		}
		if info, err := os.Lstat(filepath.Dir(left[0])); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("the ticket's directory %s: %v, %v", when, info, err)
		}
		text, err := os.ReadFile(left[0])
		if err != nil {
			t.Fatal(err)
		}
		ticket := strings.TrimSpace(string(text))
		if parsed, err := protocol.ParseJoinSecret(ticket); err != nil || parsed.TeamRef() != r.teamRef || ticket == m.secret {
			t.Fatalf("the ticket's file %s does not hold a ticket of this team", when)
		}
		return ticket
	}
	lost := held("while the move is recorded")
	m.kill(run)
	if err := config.RegisterAdapter(r.configDir, "fs", r.adapterCmd); err != nil {
		t.Fatal(err)
	}
	m.unmoved("a move killed while it was recorded")
	// Nothing of a killed command runs: the file is where it was, as private as it was.
	if again := held("after the kill"); again != lost {
		t.Fatalf("the ticket's file changed after the command was killed")
	}

	// --- 4. the same command, with nothing in its way ------------------------------------------------------
	res := m.move(m.terminal, "y\n", args...)
	if res.exit != 0 || !strings.Contains(res.stdout, "already, and you own it there") ||
		!strings.Contains(res.stdout, "opened its move again, which replaced the ticket it had") ||
		!strings.Contains(res.stdout, "wrote "+teamfile.FileName+": it names ") {
		t.Fatalf("the last run: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(server)
	if members := m.serverRoster(r.configDir, server); len(members) != 1 || members[aliceLabel] == "" {
		t.Fatalf("the server's roster after four runs: %v", members)
	}
	if left := tickets(); len(left) != 0 {
		t.Fatalf("the run that finished the move left the ticket file of the one that was killed: %v", left)
	}
	if _, err := os.Lstat(filepath.Join(profile, "take-in-key.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the retry key's file after the take-in was answered: %v", err)
	}
	ticket, ticketRead := m.ticket()
	if ticket == lost {
		t.Fatalf("the record holds the ticket of the run that was killed, which the last open replaced")
	}
	m.followsAndTalks(server)
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
	// The ticket the killed run left in its file is in no file now, and was in nothing that was printed.
	if hits := tokenHits(t, lost, r.dirs.Root); len(hits) != 0 {
		t.Errorf("the ticket of the run that was killed is still in %v", hits)
	}
	if strings.Contains(m.said.String(), lost) {
		t.Errorf("the ticket of the run that was killed is in what a team move printed")
	}
}

// TestAFinishedMoveRunAgainAndStoppedIsMendedByTheSameCommandThroughTheRealBinary:
// a second open replaces the move's ticket on the server, so a second run
// of a move that was finished, stopped between its open and its record,
// leaves the old backend handing out a ticket that admits nobody: the
// record still reads as the same move, and a member who has not followed
// yet does not follow. The command says to run it again, and that run puts
// the ticket the server now holds into the record: the member's next
// session follows.
func TestAFinishedMoveRunAgainAndStoppedIsMendedByTheSameCommandThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	backend := startRestBackend(t)
	args := moveArgs(backend, r.secretFile)
	if res := m.move(m.terminal, "y\n", args...); res.exit != 0 {
		t.Fatalf("the move: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(backend)
	names := m.recorded()
	first, _ := m.ticket()

	// --- 1. run again, and the backend the team leaves does not take the record ---------------------------
	// It takes the one the checks make, of the record that is there, and not the move's own after it.
	m.withFsAdapterAfter(1, "--ticket-file", unreachable, func() {
		res := m.move(m.terminal, "y\n", args...)
		if res.exit == 0 || !strings.Contains(res.stderr, "its move is open, and "+m.oldHost+" did not record it; the record it holds from a run before this one hands out a ticket that this run replaced") ||
			!strings.Contains(res.stderr, "Run the same command again to finish the move") {
			t.Fatalf("a second run that was not recorded: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
	})
	// The record names what the first run recorded, with the ticket the second open replaced.
	if got := m.recorded(); got != names {
		t.Fatalf("the record after a second run that recorded nothing names %s, want %s", got, names)
	}
	if stale, _ := m.ticket(); stale != first {
		t.Fatalf("the record's ticket changed in a run that recorded nothing")
	}
	// A member whose checkout has the new file is not let in with it, and stays where he was.
	early := r.newSession("bob", "billing-early", "", "", "")
	res := m.start(early)
	m.said.WriteString(res.stdout + res.stderr)
	if strings.Contains(res.stdout, "followed team") || !strings.Contains(res.stdout, teamfile.FileName+" does not match the team you joined here") {
		t.Fatalf("a member's session while the record holds a ticket that was replaced: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if pin := m.pinOf(r.configDirB); pin != pinInBobsStore(m) {
		t.Fatalf("bob's pin moved in a session that did not follow: %+v", pin)
	}

	// --- 2. the same command again, as it said: the record holds a ticket that admits ---------------------
	res = m.move(m.terminal, "y\n", args...)
	if res.exit != 0 || !strings.Contains(res.stdout, "opened its move again, which replaced the ticket it had") {
		t.Fatalf("the run that mends it: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(backend)
	ticket, ticketRead := m.ticket()
	if ticket == first {
		t.Fatalf("the record still holds the ticket the second open replaced")
	}
	m.followsAndTalks(backend)
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
}

// A tokenFront is a front that asks for a creation token as a server its
// operator gave one asks: a take-in that does not carry the token in the
// contract's header is refused `unauthorized` in the server's place, and
// the server never sees it. It counts what it was asked.
type tokenFront struct {
	*front
	mu sync.Mutex
	// admitted counts the take-ins that carried the token; elsewhere the
	// requests to another path that carried a creation token at all.
	admitted, elsewhere int
}

func startTokenFront(t *testing.T, backend restBackend, token string) (*tokenFront, restBackend) {
	t.Helper()
	const header = "Brigade-Create-Token" //nolint:gosec // G101: a header's name
	f := &tokenFront{}
	var where restBackend
	f.front, where = startFront(t, backend, func(req *http.Request, _ []byte) (int, string) {
		presented := req.Header.Get(header)
		f.mu.Lock()
		defer f.mu.Unlock()
		if req.URL.Path != "/v1/team/take-in" {
			if presented != "" {
				f.elsewhere++
			}
			return 0, ""
		}
		if presented != token {
			return http.StatusForbidden, `{"ok":false,"protocol_version":"` + protocol.ProtocolVersion +
				`","error":{"code":"unauthorized","message":"this server does not let this caller create a team","retryable":false}}` + "\n"
		}
		f.admitted++
		return 0, ""
	}, nil)
	return f, where
}

func (f *tokenFront) counts() (admitted, elsewhere int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.admitted, f.elsewhere
}

// TestATeamMovesToAServerThatAsksForACreationTokenThroughTheRealBinary: a
// server whose operator limits who may put a team on it. A token file that
// is not there, is inside the checkout or is a link into it stops the
// command before it asks, and a move the administrator declines stops
// there: until she says yes the server is asked nothing at all, and no
// directory for a ticket is made. Without the token the move stops before
// anything is recorded or written and says to ask the operator for the
// token's file; with a file that holds another token it says that one was
// refused. With the operator's file the team moves, typed with a slash at
// its end and with the adapters logging all they can (--log-level debug),
// and the token went to the take-in and to no other request, is on no
// adapter's argv and in no environment, in no line that was printed or
// logged and in no file but its own.
func TestATeamMovesToAServerThatAsksForACreationTokenThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	token := "create-token-MUST-NOT-LEAK-" + testutil.RunID()
	write := func(name, text string) string {
		t.Helper()
		path := filepath.Join(r.dirs.Root, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tokenFile := write("create.token", token+"\n")
	gate, server := startTokenFront(t, startRestBackend(t), token)
	m.alsoHidden = append(m.alsoHidden, hidden{name: "the creation token", value: token, files: []string{tokenFile}})

	// --- 1. a token file that cannot be the operator's, and a move she declines: the server is asked nothing
	inside := filepath.Join(r.checkout, "create.token")
	if err := os.WriteFile(inside, []byte("a-token-in-the-checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r.dirs.Root, "linked.token")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	for what, c := range map[string]struct{ path, says string }{
		"a token file that is not there":        {filepath.Join(r.dirs.Root, "no-such.token"), "the file named by --create-token-file is not there or is not a file"},
		"a token file that is a directory":      {r.dirs.Root, "the file named by --create-token-file is not there or is not a file"},
		"a token file inside the checkout":      {inside, "--create-token-file must be a plainly spelled path outside this project"},
		"a token file linked into the checkout": {link, "--create-token-file must be a plainly spelled path outside this project"},
		"a relative token file":                 {"create.token", "--create-token-file must be an absolute path"},
	} {
		res := m.move(m.terminal, "y\n", moveArgs(server, r.secretFile, "--create-token-file", c.path)...)
		if res.exit == 0 || !strings.Contains(res.stderr, c.says) || strings.Contains(res.stderr, "[y/N]") {
			t.Fatalf("%s: exit %d\nstdout: %s\nstderr: %s", what, res.exit, res.stdout, res.stderr)
		}
		m.unmoved(what)
		m.notJoined(what, server)
	}
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	res := m.move(m.terminal, "n\n", moveArgs(server, r.secretFile, "--create-token-file", tokenFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "[y/N]") || !strings.Contains(res.stderr, "not moved; nothing was changed") {
		t.Fatalf("a move she declined: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a move she declined")
	m.notJoined("a move she declined", server)
	// Until she says yes the server has not heard of the move, and nothing of it is on this machine.
	if asked := gate.asked(); asked != 0 {
		t.Fatalf("the server was asked %d requests before she said yes", asked)
	}
	if _, err := os.Lstat(filepath.Join(r.stateDir, "team-move")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the ticket's directory was made before she said yes: %v", err)
	}

	// --- 2. no token: the server asks for one -------------------------------------------------------------
	res = m.move(m.terminal, "y\n", moveArgs(server, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "it asks for a creation token: ask its operator for the token's file and pass --create-token-file <path>") ||
		!strings.Contains(res.stderr, "Nothing was recorded") {
		t.Fatalf("a move to a server that asks for a token, with none: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a move with no creation token")
	m.notJoined("a move with no creation token", server)

	// --- 3. another token: the server refuses it ----------------------------------------------------------
	res = m.move(m.terminal, "y\n", moveArgs(server, r.secretFile, "--create-token-file", write("other.token", "another-token\n"))...)
	if res.exit == 0 || !strings.Contains(res.stderr, "it refused the creation token in the file --create-token-file names: ask its operator for the current one") {
		t.Fatalf("a move with another creation token: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a move with another creation token")
	m.notJoined("a move with another creation token", server)

	if admitted, _ := gate.counts(); admitted != 0 {
		t.Fatalf("%d take-ins carried the token before the operator's file was named", admitted)
	}

	// --- 4. the operator's file: the team moves, with every adapter logging all it can --------------------
	// The address is typed with a slash at its end, which is not part of what is recorded or written.
	typed := restBackend{url: server.url + "/", certificate: server.certificate}
	res = m.move(m.terminal, "y\n", moveArgs(typed, r.secretFile, "--create-token-file", tokenFile, "--log-level", "debug")...)
	// The server's adapter did log at that level, into the file noSecrets reads with every other one.
	logged, err := os.ReadFile(filepath.Join(r.stateDir, "logs", "adapter-"+teamstore.Key("rest", server.url, r.teamRef)+".log"))
	if err != nil || !bytes.Contains(logged, []byte(`"level":"DEBUG"`)) || !bytes.Contains(logged, []byte("/v1/team/take-in")) {
		t.Fatalf("the server's adapter did not log the take-in at debug level (%v):\n%s", err, logged)
	}
	if res.exit != 0 || !strings.Contains(res.stdout, `took team "`+teamName+`" (`+r.teamRef+`) in on `) {
		t.Fatalf("the move with the operator's token: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(server)
	if admitted, elsewhere := gate.counts(); admitted != 1 || elsewhere != 0 {
		t.Fatalf("the token rode %d take-ins and %d other requests, want one take-in and nothing else", admitted, elsewhere)
	}
	if info, err := os.Stat(tokenFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the operator's token file after the move: %v, %v", info, err)
	}

	// --- 5. a member's next session follows with no token, and nothing hidden is anywhere -----------------
	ticket, ticketRead := m.ticket()
	m.followsAndTalks(server)
	if _, elsewhere := gate.counts(); elsewhere != 0 {
		t.Fatalf("a creation token rode %d requests that take no team in", elsewhere)
	}
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
}

// TestAMemberWhoDidNotCreateTheTeamIsRefusedBeforeAServerIsAskedThroughTheRealBinary
// holds the sentence the option team_move_in_session is described with, in
// the plugin's manifest and on the options page: "It changes nothing for a
// team you did not create". A member who joined the team and did not create
// it holds what the command asks for, a checkout that is joined and the
// team's secret file, and is not its administrator. The command's checks
// find that out on the backend the team is on, where only the team's
// creator is answered, before the server he names is asked anything, and
// with it before the join secret is sent anywhere. That holds while the
// backend holds no record of a move, where the question is a clear of
// nothing, and once the team has moved, where it is the record that is
// there recorded again; from his terminal and from a session of his, where
// nobody is asked. Each time the server was asked nothing, not what it
// offers either, his machine holds no profile for it, and the checkout,
// both pins and the old backend's record are as they were.
func TestAMemberWhoDidNotCreateTheTeamIsRefusedBeforeAServerIsAskedThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	const promise = "It changes nothing for a team you did not create"
	for _, page := range []string{"plugin/.claude-plugin/plugin.json", "docs/team-member/configure/options.md"} {
		text, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), page))
		if err != nil || !strings.Contains(string(text), promise) {
			t.Fatalf("%s does not say %q (%v): this test is what holds those words", page, promise, err)
		}
	}
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	elsewhere, server := startFront(t, startRestBackend(t), nil, nil)
	bobsPin := pinInBobsStore(m)
	session := r.newSession("bob", bobName, "", "", "")
	m.start(session)
	if got := r.mustMap(session); got.TeamKey != m.oldKey {
		t.Fatalf("the member's session before the move: %+v", got)
	}
	from := map[string][]string{"his terminal": r.terminalEnv(r.configDirB), "a session of his": r.restEnv(session)}

	// --- 1. no record of a move: the question is a clear of nothing ------------------------------------------
	for where, env := range from {
		res := m.move(env, "y\n", moveArgs(server, r.secretFile)...)
		if res.exit == 0 || !strings.Contains(res.stderr, "only the member who created this team on "+m.oldHost+" can move it") ||
			!strings.Contains(res.stderr, "Nothing was changed") || strings.Contains(res.stdout+res.stderr, "[y/N]") || strings.Contains(res.stdout, "Consented") {
			t.Fatalf("a member's move from %s: exit %d\nstdout: %s\nstderr: %s", where, res.exit, res.stdout, res.stderr)
		}
		if asked := elsewhere.asked(); asked != 0 {
			t.Fatalf("a member's move from %s asked the server %d requests", where, asked)
		}
		m.unmoved("a member's move from " + where)
		if pin := m.pinOf(r.configDirB); pin != bobsPin {
			t.Fatalf("a member's move from %s rewrote his pin: %+v", where, pin)
		}
		key := teamstore.Key("rest", server.url, r.teamRef)
		for whose, store := range map[string]string{"the member's": r.configDirB, "the administrator's": r.configDir} {
			if _, err := os.Lstat(filepath.Join(store, "teams", key)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a member's move from %s left a profile for the server in %s store (%v)", where, whose, err)
			}
		}
	}

	// --- 2. the team has moved: the question is the record that is there, recorded again --------------------
	// The administrator moves the team to her server. His checkout has not pulled the new file.
	hers := startRestBackend(t)
	if res := m.move(m.terminal, "y\n", moveArgs(hers, r.secretFile)...); res.exit != 0 {
		t.Fatalf("the administrator's move: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(hers)
	ticket, ticketRead := m.ticket()
	for _, env := range from {
		m.memberCannotMove(env, ticket)
	}
	if asked := elsewhere.asked(); asked != 0 {
		t.Fatalf("the server the member named first was asked %d requests", asked)
	}
	// Her move is as she left it: the file, her pin, her membership and the record that names them.
	m.movedFile(hers)
	r.restHook(session, "session-end", session.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
}
