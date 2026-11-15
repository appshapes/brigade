//go:build !windows

package e2e

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/testutil"
)

// What the verification of work item 341 found once a team on a server
// could move: the ways back, the ways on, and ending the team on the server
// it left. Each test is one of those findings, through the built `brigade`
// and two or three servers on loopback: the stand-in under `make test`, the
// built brigade-server over TLS where `make server-test` names one.

// adapterAs runs the bundled `rest` adapter under one profile of a
// member's store: what a test uses to read or set a server's state without
// the command under test.
func (r *rig) adapterAs(configDir, key string, args ...string) result {
	r.t.Helper()
	return r.run(r.terminalEnv(configDir), "", append([]string{"adapter", "rest", "--profile", key}, args...)...)
}

// keyAt is the rig's team's key at a server.
func (r *rig) keyAt(backend restBackend) string {
	return teamstore.Key("rest", backend.url, r.teamRef)
}

// bobStarts starts a session of bob's in the rig's checkout, ends it, and
// returns what its start said and the key of the team it attached to, ""
// when it attached to nothing.
func (r *rig) bobStarts() (result, string) {
	r.t.Helper()
	s := r.newSession("bob", bobName, "", "", "")
	res := r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	key := ""
	if r.mapExists(s) {
		key = r.mustMap(s).TeamKey
	}
	r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	return res, key
}

// teamFile reads the rig's checkout's team file, and putTeamFile writes it:
// what a pull, or a checkout of another commit, does to a checkout.
func (r *rig) teamFile() string {
	r.t.Helper()
	text, err := os.ReadFile(filepath.Join(r.checkout, teamfile.FileName))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(text)
}

func (r *rig) putTeamFile(text string) {
	r.t.Helper()
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(r.checkout, teamfile.FileName), []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// rosterAt is the active members of the rig's team on a server, by label,
// as the administrator's profile there reads them.
func (r *rig) rosterAt(backend restBackend) map[string]int {
	r.t.Helper()
	res := r.adapterAs(r.configDir, r.keyAt(backend), "team", "members")
	if res.exit != 0 {
		r.t.Fatalf("the roster of %s: exit %d\nstdout: %s\nstderr: %s", hostOf(backend), res.exit, res.stdout, res.stderr)
	}
	out := map[string]int{}
	for _, member := range jsonResult[roster](r.t, res.stdout).Members {
		out[member.HumanLabel]++
	}
	return out
}

// movedTo runs the one command as the administrator and fails the test
// unless it moved the team.
func (r *rig) movedTo(backend restBackend, secretFile string) {
	r.t.Helper()
	res := r.runIn(r.checkout, r.terminalEnv(r.configDir), "y\n", moveArgs(backend, secretFile)...)
	if res.exit != 0 {
		r.t.Fatalf("the one command to %s: exit %d\nstdout: %s\nstderr: %s", hostOf(backend), res.exit, res.stdout, res.stderr)
	}
}

// backendOf reads the `url` member a team command's result carries.
func backendOf(t *testing.T, stdout string) string {
	t.Helper()
	var doc struct {
		Result struct {
			URL string `json:"url"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("a team command's result: %v\n%s", err, stdout)
	}
	return doc.Result.URL
}

// TestATeamIsEndedOnTheServerItLeftByNamingThatServerThroughTheRealBinary:
// after a move the administrator's checkout is pinned to the new server,
// and a team command there goes where the pin points, whatever the team
// file of an older commit names. Until `--on`, `team rotate-secret` and
// `team revoke-member` run to end the team on the old server rotated the
// secret of the new one and put the members who had followed out of it,
// and said nothing of which server they had acted on.
//
//  1. With no flag, the command says which server it acts on: the pin's.
//  2. `--on <the old server>` rotates the secret and revokes there, and
//     says so before it acts and in its result.
//  3. The new server is untouched: its roster is whole and the member's
//     session attaches there.
//  4. `--on` a server this machine never joined the team on is refused.
func TestATeamIsEndedOnTheServerItLeftByNamingThatServerThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a, b := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, a)
	term := r.terminalEnv(r.configDir)
	before := r.teamFile()
	r.movedTo(b, r.secretFile)
	after := r.teamFile()
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to the new server: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	// The commit before the move, checked out in the checkout the move rewrote.
	r.putTeamFile(before)

	// --- 1. no flag: the pin decides, and the command says which server that is --------------------------
	res := r.runIn(r.checkout, term, "", "team", "revoke-member", "--principal", strings.Repeat("0", 32))
	if !strings.Contains(res.stderr, `team revoke-member acts on team "`+teamName+`" (`+r.teamRef+`) at `+hostOf(b)) {
		t.Fatalf("a team command with no --on does not say which server it acts on: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}

	// --- 2. --on the old server ---------------------------------------------------------------------------
	rotated := filepath.Join(r.dirs.Root, "old-server-rotated.secret")
	res = r.runIn(r.checkout, term, "", "team", "rotate-secret", "--on", hostOf(a), "--secret-file", rotated)
	if res.exit != 0 || backendOf(t, res.stdout) != a.url ||
		!strings.Contains(res.stderr, `team rotate-secret acts on team "`+teamName+`" (`+r.teamRef+`) at `+hostOf(a)) {
		t.Fatalf("team rotate-secret --on the old server: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	res = r.runIn(r.checkout, term, "", "team", "revoke-member", "--on", a.url, "--max-version", "1")
	revoked := jsonResult[struct {
		Revoked int    `json:"revoked"`
		URL     string `json:"url"`
	}](t, res.stdout)
	if res.exit != 0 || revoked.Revoked != 1 || revoked.URL != a.url || !strings.Contains(res.stderr, "at "+hostOf(a)) {
		t.Fatalf("team revoke-member --on the old server: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if members := r.rosterAt(a); len(members) != 1 || members[aliceLabel] != 1 {
		t.Fatalf("the old server's roster after the revoke: %v", members)
	}

	// --- 3. the new server is as it was --------------------------------------------------------------------
	if members := r.rosterAt(b); len(members) != 2 || members[aliceLabel] != 1 || members[bobLabel] != 1 {
		t.Fatalf("the new server's roster after the team was ended on the old one: %v", members)
	}
	r.putTeamFile(after)
	if res, key := r.bobStarts(); key != r.keyAt(b) || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("the member's session on the new server after the team was ended on the old one: stdout %q\nstderr %s", res.stdout, res.stderr)
	}

	// --- 4. --on a server this machine never joined the team on -------------------------------------------
	res = r.runIn(r.checkout, term, "", "team", "revoke-member", "--on", "nowhere.example.test:1", "--max-version", "1")
	if res.exit == 0 || !strings.Contains(res.stderr, "--on names no backend this machine has joined this team on") {
		t.Fatalf("--on a server never joined: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
}

// TestAMemberRevokedOnTheServerATeamReturnsToFollowsWithTheTicketThroughTheRealBinary:
// a team moves from A to B, its administrator ends it on A as the page
// says (a new join secret there, the members who followed revoked), and
// the team later moves back to A. The member's machine still holds the
// membership it had on A, which A no longer honours. Until this the follow
// took that membership for a good one, pinned the checkout to A and left
// the session unconnected, with `team join` answering "already joined".
// Now the follow asks A with that membership, is answered `unauthorized`,
// sets it aside and joins with the move's ticket, as a machine that had
// never joined would.
func TestAMemberRevokedOnTheServerATeamReturnsToFollowsWithTheTicketThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a, b := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, a)
	r.movedTo(b, r.secretFile)
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	// The team is ended on A, with the administrator's own profile there.
	rotated := filepath.Join(r.dirs.Root, "a-rotated.secret")
	if res := r.adapterAs(r.configDir, r.keyAt(a), "team", "rotate-secret", "--secret-file", rotated); res.exit != 0 {
		t.Fatalf("the rotation on A: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	if res := r.adapterAs(r.configDir, r.keyAt(a), "team", "revoke-member", "--max-version", "1"); res.exit != 0 {
		t.Fatalf("the revocation on A: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	if res := r.adapterAs(r.configDirB, r.keyAt(a), "team", "moved"); res.exit == 0 {
		t.Fatalf("the member still reads A after he was revoked there: %s", res.stdout)
	}
	// The team comes back to A, with the secret A has now.
	r.movedTo(a, rotated)

	res, key := r.bobStarts()
	if key != r.keyAt(a) || !strings.Contains(res.stdout, `followed team "`+teamName+`" to `+hostOf(a)) ||
		!strings.Contains(res.stdout, "with the move's ticket, under the label you had") || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("the session of a member who was revoked on the server the team came back to: attached to %q\nstdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	if members := r.rosterAt(a); len(members) != 2 || members[aliceLabel] != 1 || members[bobLabel] != 1 {
		t.Fatalf("A's roster after the member followed back: %v", members)
	}
	// And the session after that is where the team is, with nothing to say.
	if res, key := r.bobStarts(); key != r.keyAt(a) || strings.Contains(res.stdout, "followed") || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("the member's next session: attached to %q, stdout %q", key, res.stdout)
	}
}

// TestTheOlderRoadBackToAServerClearsItsRecordOfTheLeavingThroughTheRealBinary:
// a team moves from A to B with the one command and back to A by the
// record's older form, `brigade team move --ticket-file`, with a ticket A
// made. A still records that the team left it for B. Until this the older
// form recorded on B and left A's record standing, so the two servers each
// said that the team was on the other, and a team file flipped between
// them sent a session back and forth. Now it clears A's record first, as
// the one command does.
func TestTheOlderRoadBackToAServerClearsItsRecordOfTheLeavingThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a, b := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, a)
	term := r.terminalEnv(r.configDir)
	fileOfA := r.teamFile()
	r.movedTo(b, r.secretFile)
	fileOfB := r.teamFile()
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	// The owner of the team on A opens its move there, and the administrator
	// points the file at A and records the move on B with that ticket.
	ticket := filepath.Join(r.dirs.Root, "back.ticket")
	if res := r.adapterAs(r.configDir, r.keyAt(a), "team", "open-move", "--ticket-file", ticket); res.exit != 0 {
		t.Fatalf("the move opened on A: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	r.putTeamFile(fileOfA)
	res := r.runIn(r.checkout, term, "y\n", "team", "move", "--ticket-file", ticket)
	cleared, recorded := strings.Index(res.stdout, "cleared the record on "+hostOf(a)+" that the team had left it for "+hostOf(b)), strings.Index(res.stdout, "recorded on "+hostOf(b))
	if res.exit != 0 || cleared < 0 || recorded < cleared {
		t.Fatalf("team move --ticket-file back to a server that records the leaving: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if record := r.recordOn(r.configDir, a); record.Moved {
		t.Fatalf("A still records that the team left it: %+v", record)
	}
	if record := r.recordOn(r.configDir, b); !record.Moved || record.URL != a.url {
		t.Fatalf("B's record of the move back: %+v", record)
	}
	// The member follows back, and a file of the earlier commit sends him nowhere.
	if res, key := r.bobStarts(); key != r.keyAt(a) || !strings.Contains(res.stdout, "this machine had joined it there already") {
		t.Fatalf("the member's session after the move back: attached to %q, stdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	r.putTeamFile(fileOfB)
	if res, key := r.bobStarts(); key != "" || strings.Contains(res.stdout, "followed") {
		t.Fatalf("a team file of the earlier commit sent the member's session to %q: stdout %q", key, res.stdout)
	}
}

// TestTwoServersThatNameEachOtherSendNoSessionAnywhereThroughTheRealBinary:
// no command of Brigade's leaves two servers that each record that the
// team moved to the other, and a follow does not rely on that. Here the
// state is made with the adapter alone. A session on B whose team file
// names A is not sent to A, though B records that move, because A itself
// says that the team went to B: it says so, once, and the checkout's pin
// stays where it was.
func TestTwoServersThatNameEachOtherSendNoSessionAnywhereThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a, b := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, a)
	fileOfA := r.teamFile()
	r.movedTo(b, r.secretFile)
	fileOfB := r.teamFile()
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	// B is made to record a move back to A by its adapter alone, with a
	// ticket A made: A's own record of the leaving stands.
	ticket := filepath.Join(r.dirs.Root, "back.ticket")
	if res := r.adapterAs(r.configDir, r.keyAt(a), "team", "open-move", "--ticket-file", ticket); res.exit != 0 {
		t.Fatalf("the move opened on A: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	record := []string{"team", "move", "--ticket-file", ticket, "--adapter", "rest", "--url", a.url}
	if a.certificate != "" {
		record = append(record, "--certificate", a.certificate)
	}
	if res := r.adapterAs(r.configDir, r.keyAt(b), record...); res.exit != 0 {
		t.Fatalf("the record made on B by its adapter: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	if onA, onB := r.recordOn(r.configDir, a), r.recordOn(r.configDir, b); !onA.Moved || onA.URL != b.url || !onB.Moved || onB.URL != a.url {
		t.Fatalf("the two servers do not name each other: A %+v, B %+v", onA, onB)
	}
	canon, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		t.Fatal(err)
	}
	pinBefore, _, err := teamstore.LookupPin(r.configDirB, canon)
	if err != nil {
		t.Fatal(err)
	}

	r.putTeamFile(fileOfA)
	res, key := r.bobStarts()
	if key != "" || strings.Contains(res.stdout, "followed") || !strings.Contains(res.stdout, "each record that the team moved to the other") {
		t.Fatalf("a session between two servers that name each other: attached to %q\nstdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	if pin, _, perr := teamstore.LookupPin(r.configDirB, canon); perr != nil || *pin != *pinBefore {
		t.Fatalf("the member's pin moved between two servers that name each other: %+v %v", pin, perr)
	}
	// With the file that names the server he is on, he is on it.
	r.putTeamFile(fileOfB)
	if res, key := r.bobStarts(); key != r.keyAt(b) || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("the member's session with the file of the server he is on: attached to %q, stdout %q", key, res.stdout)
	}

	// A session that is running when the file comes to name A stays on B,
	// and says why once: the follow its first prompt started found the two
	// naming each other, and the prompt after that says so.
	running := r.newSession("bob", bobName, "", "", "")
	r.restHook(running, "session-start", running.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if at := r.mustMap(running); at.TeamKey != r.keyAt(b) {
		t.Fatalf("the running session before the file changed: %+v", at)
	}
	r.putTeamFile(fileOfA)
	if res := r.restPrompt(running); !strings.Contains(res.stdout, "now names another backend for this session's team") || strings.Contains(res.stdout, "each record") {
		t.Fatalf("the running session's first prompt: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	moveLog := filepath.Join(r.stateDir, "logs", "team-move.log")
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		said, rerr := os.ReadFile(moveLog) //nolint:gosec // G304: a log under the test's own state directory
		return rerr == nil && strings.Contains(string(said), `"reason":"backends_name_each_other"`)
	})
	if res := r.restPrompt(running); !strings.Contains(res.stdout, "each record that the team moved to the other") || !strings.Contains(res.stdout, "stays where it is") {
		t.Fatalf("the prompt after the follow found the two naming each other: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if res := r.restPrompt(running); strings.Contains(res.stdout, "each record") || strings.Contains(res.stdout, "followed") {
		t.Fatalf("a later prompt said it again: stdout %q", res.stdout)
	}
	if at := r.mustMap(running); at.TeamKey != r.keyAt(b) {
		t.Fatalf("the running session moved between two servers that name each other: %+v", at)
	}
	if pin, _, perr := teamstore.LookupPin(r.configDirB, canon); perr != nil || *pin != *pinBefore {
		t.Fatalf("the member's pin moved at a prompt: %+v %v", pin, perr)
	}
	r.restHook(running, "session-end", running.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
}

// TestTheSameCommandRunAgainAfterAnEarlierMoveFindsTheBackendItLeavesThroughTheRealBinary:
// a team that moved from A to B and then to C leaves its administrator's
// machine joined on two earlier backends. Until this the same command run
// again, which every page and every stop of the command names, was refused
// for not knowing which of them the team is leaving.
//
//  1. Run again while B records the move to C, it finds B by that record.
//  2. With B's record cleared no backend says, and the checkout's pin
//     names C: it refuses, names the two and the flag.
//  3. With the pin still on B, as a run stopped before it wrote the pin
//     leaves it, it finds B by the pin.
func TestTheSameCommandRunAgainAfterAnEarlierMoveFindsTheBackendItLeavesThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a, b, c := startRestBackend(t), startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, a)
	term := r.terminalEnv(r.configDir)
	r.movedTo(b, r.secretFile)
	canon, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		t.Fatal(err)
	}
	pinOnB, _, err := teamstore.LookupPin(r.configDir, canon)
	if err != nil || pinOnB.URL != b.url {
		t.Fatalf("the administrator's pin after the first move: %+v %v", pinOnB, err)
	}
	r.movedTo(c, r.secretFile)

	// --- 1. the record names the destination ---------------------------------------------------------------
	res := r.runIn(r.checkout, term, "y\n", moveArgs(c, r.secretFile)...)
	if res.exit != 0 || !strings.Contains(res.stdout, "recorded on "+hostOf(b)+":") || !strings.Contains(res.stderr, "from "+hostOf(b)+" to "+hostOf(c)) {
		t.Fatalf("the same command run again after two moves: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}

	// --- 2. no record, and a pin that names the destination -------------------------------------------------
	if res := r.runIn(r.checkout, term, "", "team", "move", "--clear", "--from", hostOf(b)); res.exit != 0 {
		t.Fatalf("the clear on B: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	res = r.runIn(r.checkout, term, "y\n", moveArgs(c, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, hostOf(a)) || !strings.Contains(res.stderr, hostOf(b)) || !strings.Contains(res.stderr, "--from <host>") ||
		strings.Contains(res.stderr, "[y/N]") {
		t.Fatalf("the same command with no record to go by: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}

	// --- 3. the pin of a run that was stopped before it wrote it -------------------------------------------
	if err := write.Pin(r.configDir, canon, *pinOnB); err != nil {
		t.Fatal(err)
	}
	res = r.runIn(r.checkout, term, "y\n", moveArgs(c, r.secretFile)...)
	if res.exit != 0 || !strings.Contains(res.stdout, "recorded on "+hostOf(b)+":") {
		t.Fatalf("the same command after a run that stopped before its pin: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if record := r.recordOn(r.configDir, a); !record.Moved || record.URL != b.url {
		t.Fatalf("A's record was touched: %+v", record)
	}
}

// TestTheServerATeamIsOnUnderAnotherAddressIsNoPlaceToMoveItToThroughTheRealBinary:
// the server a team is on, named by another address than the one its
// members know (`localhost` for `127.0.0.1` here; another DNS name in the
// field). A member's machine knows a server by its address, so the take-in
// arrives with no credential and is the join of a new member. Until this
// the command stopped with "owned by someone else" and the operator's
// `--set-owner`, which on that very server takes the team from the
// administrator's own membership, and left the administrator on the roster
// twice. Now it finds its own membership of the old address on the
// server's roster, says that it is the same server or a copy of its data,
// and leaves with the membership it had just made.
func TestTheServerATeamIsOnUnderAnotherAddressIsNoPlaceToMoveItToThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a := startRestBackend(t)
	r := teamOnAServer(t, a)
	m := newMovingTeam(t, r, r.restAdapterAs(r.configDir))
	other := a
	other.url = strings.Replace(a.url, "127.0.0.1", "localhost", 1)
	if other.url == a.url {
		t.Fatalf("the server's address has no 127.0.0.1 in it: %s", a.url)
	}
	res := m.move(m.terminal, "y\n", moveArgs(other, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "the same server under another address, or a copy of its data") ||
		strings.Contains(res.stderr, "--set-owner") || !strings.Contains(res.stderr, "Nothing was recorded, and "+teamfile.FileName+" is unchanged") {
		t.Fatalf("the one command to the server the team is on, by another address: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a move to the server the team is on")
	if members := r.rosterAt(a); len(members) != 2 || members[aliceLabel] != 1 || members[bobLabel] != 1 {
		t.Fatalf("the roster after a move to the same server was refused: %v, want each member once", members)
	}
	m.notJoined("a move to the server the team is on", other)
}
