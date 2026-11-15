//go:build !windows

package e2e

import (
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
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
	return r.startsIn("bob", bobName, r.checkout)
}

// startsIn is bobStarts for a session of either member in any checkout.
func (r *rig) startsIn(profile, title, dir string) (result, string) {
	r.t.Helper()
	s := r.newSession(profile, title, "", "", "")
	s.cwd = dir
	res := r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	key := ""
	if r.mapExists(s) {
		key = r.mustMap(s).TeamKey
	}
	r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	return res, key
}

// anotherCheckout makes one more checkout of the team for the member whose
// store configDir is: a directory with the team file the rig's checkout has
// now, pinned as that checkout is pinned in that store.
func (r *rig) anotherCheckout(configDir, name string) (dir, canon string) {
	r.t.Helper()
	first, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		r.t.Fatal(err)
	}
	pin, pinned, err := teamstore.LookupPin(configDir, first)
	if err != nil || !pinned {
		r.t.Fatalf("the pin of the rig's checkout: %v %v", pinned, err)
	}
	dir = filepath.Join(r.dirs.Root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		r.t.Fatal(err)
	}
	r.putFileIn(dir, r.teamFile())
	if canon, err = teamfile.Canonicalize(dir); err != nil {
		r.t.Fatal(err)
	}
	if err := write.Pin(configDir, canon, *pin); err != nil {
		r.t.Fatal(err)
	}
	return dir, canon
}

// putFileIn writes a checkout's team file.
func (r *rig) putFileIn(dir, text string) {
	r.t.Helper()
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(dir, teamfile.FileName), []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// principalsAt is the principals of the active members of the rig's team on
// a server, by label, as the administrator's profile there reads them.
func (r *rig) principalsAt(backend restBackend) map[string][]string {
	r.t.Helper()
	res := r.adapterAs(r.configDir, r.keyAt(backend), "team", "members")
	if res.exit != 0 {
		r.t.Fatalf("the roster of %s: exit %d\nstdout: %s\nstderr: %s", hostOf(backend), res.exit, res.stdout, res.stderr)
	}
	out := map[string][]string{}
	for _, member := range jsonResult[roster](r.t, res.stdout).Members {
		out[member.HumanLabel] = append(out[member.HumanLabel], member.PrincipalRef)
	}
	return out
}

// principalOf is the principal a member's machine holds for the rig's team
// on a server.
func (r *rig) principalOf(configDir string, backend restBackend) string {
	r.t.Helper()
	binding, err := teamstore.LoadBinding(configDir, r.keyAt(backend))
	if err != nil || binding.PrincipalRef == "" {
		r.t.Fatalf("the membership of %s on %s: %+v %v", configDir, hostOf(backend), binding, err)
	}
	return binding.PrincipalRef
}

// notAMember is a server's answer to a caller that is no active member of
// the team, as a front gives it in the server's place.
const notAMember = `{"ok":false,"protocol_version":"` + protocol.ProtocolVersion + `","error":{"code":"unauthorized","message":"not an active member of this team","retryable":false}}`

// silent is a front's switch: while it is on, the front answers every
// request as a server that cannot be reached.
type silent struct{ on atomic.Bool }

func (s *silent) before(*http.Request, []byte) (int, string) {
	if s.on.Load() {
		return http.StatusServiceUnavailable, unreachable
	}
	return 0, ""
}

// printed is the text a command printed between backticks that begins with
// start: a line it told its reader to run, as it printed it.
func printed(t *testing.T, said, start string) string {
	t.Helper()
	_, rest, found := strings.Cut(said, "`"+start)
	line, _, closed := strings.Cut(rest, "`")
	if !found || !closed {
		t.Fatalf("nothing printed between backticks begins with %q:\n%s", start, said)
	}
	return start + line
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
// and joins with the move's ticket under the credential it holds, so the
// server acts for the same principal: he comes back as who he was.
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
	had := r.principalOf(r.configDirB, a)
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
	// He is who he was on A: the machine joined with the credential it held,
	// so the server acted for the same principal, and anything A holds
	// against that principal, a ban above all, still knows him.
	if now, listed := r.principalOf(r.configDirB, a), r.principalsAt(a)[bobLabel]; now != had || len(listed) != 1 || listed[0] != had {
		t.Fatalf("the member came back to A as %q, on its roster as %v; he was %q", now, listed, had)
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

	// The administrator clears the record that is out of date, A's, and the
	// member's next session follows at its start. What the follow of a
	// prompt left for the checkout, that the two named each other, is gone
	// with it: left there, it would have a running session say "follows
	// neither" at the team's next move between the two, and then follow.
	marks := func() []string {
		left, gerr := filepath.Glob(filepath.Join(r.stateDir, "state", "move-*.each-other"))
		if gerr != nil {
			t.Fatal(gerr)
		}
		return left
	}
	if len(marks()) != 1 {
		t.Fatalf("the follow that found the two naming each other left %v", marks())
	}
	if res := r.runIn(r.checkout, r.terminalEnv(r.configDir), "", "team", "move", "--clear", "--from", hostOf(a)); res.exit != 0 {
		t.Fatalf("the clear of A's record: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	if res, key := r.bobStarts(); key != r.keyAt(a) || !strings.Contains(res.stdout, "followed team") {
		t.Fatalf("the member's session after the repair: attached to %q, stdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	if left := marks(); len(left) != 0 {
		t.Fatalf("a start that followed left the mark of two backends that named each other: %v", left)
	}
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

// TestAMemberPutOutOfTheServerTheTeamMovedToComesBackOrStaysOutAsTheServerSaysThroughTheRealBinary:
// while a move is open its ticket admits the team's members, and a
// machine that holds a membership on the server the team moved to joins
// with the ticket when the server answers `unauthorized` to it. It joins
// with the credential it holds, so the server acts for the same principal
// (BAP/1 4.4.10). Until this the machine set that membership aside and
// joined as a principal the server had never seen, which a ban, made by
// principal, did not know: a member the administrator had banned on the
// server the team lives on was back on its roster at his next session in
// another checkout.
//
//  1. Revoked and not banned, he comes back by the ticket, as who he was:
//     that is the protocol's "may come back", and what keeps a member out
//     during an open move is a ban, or closing the move.
//  2. Banned, he stays out: his other checkout does not follow, he is not
//     on the roster, and the machine holds the membership it held.
func TestAMemberPutOutOfTheServerTheTeamMovedToComesBackOrStaysOutAsTheServerSaysThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a, b := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, a)
	term := r.terminalEnv(r.configDir)
	second, _ := r.anotherCheckout(r.configDirB, "checkout-2")
	third, thirdCanon := r.anotherCheckout(r.configDirB, "checkout-3")
	r.movedTo(b, r.secretFile)
	r.putFileIn(second, r.teamFile())
	r.putFileIn(third, r.teamFile())
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	onB := r.principalOf(r.configDirB, b)

	// --- 1. revoked, and the move is open: he comes back as who he was ------------------------------------
	if res := r.runIn(r.checkout, term, "", "team", "revoke-member", "--principal", onB); res.exit != 0 {
		t.Fatalf("the revocation on B: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	if members := r.rosterAt(b); members[bobLabel] != 0 {
		t.Fatalf("B's roster after the revocation: %v", members)
	}
	res, key := r.startsIn("bob", bobName, second)
	if key != r.keyAt(b) || !strings.Contains(res.stdout, "with the move's ticket") {
		t.Fatalf("the second checkout of a member revoked while the move is open: attached to %q\nstdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	if listed := r.principalsAt(b)[bobLabel]; len(listed) != 1 || listed[0] != onB || r.principalOf(r.configDirB, b) != onB {
		t.Fatalf("he came back to B as %v (the machine holds %q); he was %q", listed, r.principalOf(r.configDirB, b), onB)
	}

	// --- 2. banned: he stays out ----------------------------------------------------------------------------
	if res := r.runIn(r.checkout, term, "", "team", "revoke-member", "--principal", onB, "--ban"); res.exit != 0 {
		t.Fatalf("the ban on B: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	pinBefore, _, err := teamstore.LookupPin(r.configDirB, thirdCanon)
	if err != nil {
		t.Fatal(err)
	}
	res, key = r.startsIn("bob", bobName, third)
	if key != "" || strings.Contains(res.stdout, "followed") || !strings.Contains(res.stdout, "not connected") {
		t.Fatalf("the third checkout of a member banned on the server the team moved to: attached to %q\nstdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	if members := r.principalsAt(b); len(members[bobLabel]) != 0 || len(members) != 1 {
		t.Fatalf("B's roster after a banned member's session started: %v", members)
	}
	if pin, _, perr := teamstore.LookupPin(r.configDirB, thirdCanon); perr != nil || *pin != *pinBefore {
		t.Fatalf("the pin of a banned member's checkout moved: %+v %v", pin, perr)
	}
	if now := r.principalOf(r.configDirB, b); now != onB {
		t.Fatalf("the machine of a banned member holds %q for B; it held %q", now, onB)
	}
	// His first checkout, pinned to B, is told what it was always told.
	if res, key := r.bobStarts(); key != "" || !strings.Contains(res.stdout, "not connected (unauthorized)") {
		t.Fatalf("the checkout that had followed, after the ban: attached to %q, stdout %q", key, res.stdout)
	}
}

// TestTheOwnerAnsweredUnauthorizedOnceStillOwnsTheTeamThroughTheRealBinary:
// something in front of the server the team moved to answers the follow's
// question once as the server would answer a stranger. The machine is the
// team's owner's, in a second checkout of hers. Until this the follow set
// her membership aside on that one answer and joined with the ticket as a
// new principal, which a ticket never makes an owner: she lost the team.
// Now the join is made with the credential she holds, the server acts for
// the principal that owns the team, and she owns it afterwards.
func TestTheOwnerAnsweredUnauthorizedOnceStillOwnsTheTeamThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	var refuse atomic.Bool
	a := startRestBackend(t)
	_, b := startFront(t, startRestBackend(t), func(req *http.Request, _ []byte) (int, string) {
		if req.URL.Path == "/v1/team/moved" && refuse.CompareAndSwap(true, false) {
			return http.StatusForbidden, notAMember
		}
		return 0, ""
	}, nil)
	r := teamOnAServer(t, a)
	second, _ := r.anotherCheckout(r.configDir, "admin-checkout-2")
	r.movedTo(b, r.secretFile)
	r.putFileIn(second, r.teamFile())
	owner := r.principalOf(r.configDir, b)

	refuse.Store(true)
	res, key := r.startsIn("alice", aliceName, second)
	if refuse.Load() {
		t.Fatalf("the front was not asked what the server records: the test holds nothing\nstdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if key != r.keyAt(b) || strings.Contains(res.stdout, "not connected") {
		t.Fatalf("the owner's second checkout: attached to %q\nstdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	if now, listed := r.principalOf(r.configDir, b), r.principalsAt(b); now != owner || len(listed[aliceLabel]) != 1 || listed[aliceLabel][0] != owner {
		t.Fatalf("the owner is %q on her machine and %v on the roster; she was %q", now, listed, owner)
	}
	// What only the team's owner is answered: the clear of a record, here of none.
	if res := r.adapterAs(r.configDir, r.keyAt(b), "team", "move", "--clear"); res.exit != 0 {
		t.Fatalf("the owner no longer owns the team after one unauthorized answer: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
}

// TestAnEarlierServerUnderAnotherAddressIsKnownForWhatItIsThroughTheRealBinary:
// the team moved from A to B, and the one command is pointed at A by
// another address than the one this machine knows it by. The server holds
// the team with the membership the machine has on A, which is not the
// backend the team is on now. Until this only the membership on the
// backend the team is leaving was looked for, so the command printed the
// operator's `--set-owner` and left the administrator on A's roster twice.
func TestAnEarlierServerUnderAnotherAddressIsKnownForWhatItIsThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a, b := startRestBackend(t), startRestBackend(t)
	r := teamOnAServer(t, a)
	r.movedTo(b, r.secretFile)
	moved := r.teamFile()
	other := a
	other.url = strings.Replace(a.url, "127.0.0.1", "localhost", 1)
	if other.url == a.url {
		t.Fatalf("the server's address has no 127.0.0.1 in it: %s", a.url)
	}
	res := r.runIn(r.checkout, r.terminalEnv(r.configDir), "y\n", moveArgs(other, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "the same server under another address, or a copy of its data") ||
		!strings.Contains(res.stderr, "with the membership this machine has on "+hostOf(a)) || strings.Contains(res.stderr, "--set-owner") {
		t.Fatalf("the one command to an earlier server by another address: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if members := r.rosterAt(a); len(members) != 2 || members[aliceLabel] != 1 || members[bobLabel] != 1 {
		t.Fatalf("A's roster after the refusal: %v, want each member once", members)
	}
	if _, err := os.Lstat(filepath.Join(r.configDir, "teams", r.keyAt(other))); err == nil {
		t.Fatalf("the refusal left a profile for the other address in the administrator's store")
	}
	if r.teamFile() != moved {
		t.Fatalf("the refusal rewrote the team file:\n%s", r.teamFile())
	}
	if record := r.recordOn(r.configDir, b); record.Moved {
		t.Fatalf("B recorded a move that was refused: %+v", record)
	}
	// The address the refusal prints, run as it is printed, moves the team
	// back: it carries the certificate's pin where the server has one.
	back := append([]string{"team", "move"}, strings.Fields(printed(t, res.stderr, "--to "))...)
	res = r.runIn(r.checkout, r.terminalEnv(r.configDir), "y\n", append(back, "--secret-file", r.secretFile)...)
	if res.exit != 0 || !strings.Contains(res.stdout, "recorded on "+hostOf(b)+":") {
		t.Fatalf("the address the refusal printed, run as printed: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if record := r.recordOn(r.configDir, b); !record.Moved || record.URL != a.url || record.Certificate != a.certificate {
		t.Fatalf("B's record of the move back: %+v", record)
	}
}

// TestARerunDoesNotGuessWhileABackendTheTeamWasOnDoesNotAnswerThroughTheRealBinary:
// a team went from A to B to C and back to B, and the same command is run
// again while C does not answer. A still records the first move, to B; C
// holds the record of the last. Until this a backend whose record could
// not be read counted as one with none, so A was taken for the backend
// the team is leaving: the command recorded there, and replaced B's
// ticket. A backend that cannot be read is not one with no record: the
// command changes nothing, names it, and says to run it again when it
// answers or with --from.
func TestARerunDoesNotGuessWhileABackendTheTeamWasOnDoesNotAnswerThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	down := &silent{}
	a, b := startRestBackend(t), startRestBackend(t)
	_, c := startFront(t, startRestBackend(t), down.before, nil)
	r := teamOnAServer(t, a)
	term := r.terminalEnv(r.configDir)
	r.movedTo(b, r.secretFile)
	r.movedTo(c, r.secretFile)
	r.movedTo(b, r.secretFile)
	onA, onC := r.recordOn(r.configDir, a), r.recordOn(r.configDir, c)
	if !onA.Moved || onA.URL != b.url || !onC.Moved || onC.URL != b.url {
		t.Fatalf("the records before the rerun: A %+v, C %+v", onA, onC)
	}
	ticket := filepath.Join(r.dirs.Root, "b.ticket")
	read := func() string {
		t.Helper()
		if res := r.adapterAs(r.configDir, r.keyAt(a), "team", "moved", "--ticket-file", ticket); res.exit != 0 {
			t.Fatalf("A's record with its ticket: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
		}
		text, err := os.ReadFile(ticket)
		if err != nil {
			t.Fatal(err)
		}
		return string(text)
	}
	held := read()

	down.on.Store(true)
	res := r.runIn(r.checkout, term, "y\n", moveArgs(b, r.secretFile)...)
	down.on.Store(false)
	if res.exit == 0 || !strings.Contains(res.stderr, hostOf(c)+" did not answer") || !strings.Contains(res.stderr, "--from <host>") ||
		!strings.Contains(res.stderr, "when it answers") || !strings.Contains(res.stderr, "Nothing was changed") || strings.Contains(res.stderr, "[y/N]") {
		t.Fatalf("the same command while a backend the team was on does not answer: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if now := r.recordOn(r.configDir, a); !now.RecordedAt.Equal(onA.RecordedAt) || read() != held {
		t.Fatalf("A's record was written while C did not answer: %+v, was %+v", now, onA)
	}
	// The line it prints, run as it is printed with the backend named and the
	// secret's file, runs: it carries the certificate's pin where the server
	// has one.
	line := strings.NewReplacer("<path>", r.secretFile, "<host>", hostOf(c)).Replace(printed(t, res.stderr, "brigade team move --to "))
	if res := r.runIn(r.checkout, term, "y\n", strings.Fields(line)[1:]...); res.exit != 0 || !strings.Contains(res.stdout, "recorded on "+hostOf(c)+":") {
		t.Fatalf("the line the refusal printed, %q: exit %d\nstdout: %s\nstderr: %s", line, res.exit, res.stdout, res.stderr)
	}
}

// TestAServerThatDoesNotAnswerIsNotToldAsATeamFileThatDoesNotMatchThroughTheRealBinary:
// a member followed the team to its new server in one checkout, and starts
// a session in another while that server does not answer. The follow asks
// it with the membership the machine holds, gets no answer, and changes
// nothing. Until this the start then said that `.brigade.json` does not
// match the team and to run `brigade team join`, which is what a file
// nobody vouched for is told. It says that the server did not answer, and
// the next start follows.
func TestAServerThatDoesNotAnswerIsNotToldAsATeamFileThatDoesNotMatchThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	down := &silent{}
	a := startRestBackend(t)
	_, b := startFront(t, startRestBackend(t), down.before, nil)
	r := teamOnAServer(t, a)
	second, canon := r.anotherCheckout(r.configDirB, "checkout-2")
	r.movedTo(b, r.secretFile)
	r.putFileIn(second, r.teamFile())
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	pinBefore, _, err := teamstore.LookupPin(r.configDirB, canon)
	if err != nil {
		t.Fatal(err)
	}
	silentWords := func(said string) bool {
		return strings.Contains(said, "did not answer") && !strings.Contains(said, "does not match") && !strings.Contains(said, "--secret-file")
	}

	down.on.Store(true)
	s := r.newSession("bob", bobName, "", "", "")
	s.cwd = second
	res := r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if r.mapExists(s) || !silentWords(res.stdout) || strings.Contains(res.stdout, "brigade team join") {
		t.Fatalf("a start while the server the team moved to does not answer: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if pin, _, perr := teamstore.LookupPin(r.configDirB, canon); perr != nil || *pin != *pinBefore {
		t.Fatalf("the pin moved while the server did not answer: %+v %v", pin, perr)
	}
	// The session's next prompt says what its start said, and not that the
	// team file does not match: the follow it starts finds the same.
	moveLog := filepath.Join(r.stateDir, "logs", "team-move.log")
	if res := r.promptedUntilAFollowStarts(s, r.configDirB, canon); r.mapExists(s) || !silentWords(res.stdout) {
		t.Fatalf("the prompt after that start: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return logLines(moveLog, "the team file names another backend for this team, and the move was not followed") == 1
	})
	if pin, _, perr := teamstore.LookupPin(r.configDirB, canon); perr != nil || *pin != *pinBefore {
		t.Fatalf("the pin moved at a prompt while the server did not answer: %+v %v", pin, perr)
	}
	// The server answers again. A minute later the session's prompt starts
	// the follow once more, and the prompt after it connects the session.
	down.on.Store(false)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		for _, stamp := range []string{config.MoveAttemptStamp(r.stateDir, r.configDirB, canon), config.RegisterRetryStamp(r.stateDirOf(s), s.pid)} {
			if err := os.Remove(stamp); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a minute's stamp: %v", err)
			}
		}
		res = r.restPrompt(s)
		return r.mapExists(s)
	})
	if at := r.mustMap(s); at.TeamKey != r.keyAt(b) || strings.Contains(res.stdout, "does not match") {
		t.Fatalf("the session after the server answered again: %+v\nstdout %q", at, res.stdout)
	}
	r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
}

// unknownCredential is a server's answer to a credential it does not know,
// as a front gives it in the server's place: what a server that was started
// again from an older copy of its data answers a member who joined since.
const unknownCredential = `{"ok":false,"protocol_version":"` + protocol.ProtocolVersion + `","error":{"code":"unauthenticated","message":"the credential is not known to this server","retryable":false}}`

// TestAServerThatRefusesIsNotToldAsOneThatDidNotAnswerThroughTheRealBinary:
// the server the team moved to answers the follow's question, and its
// answer is that it does not know the credential this machine holds for it.
// That is an answer. Until this every failure of that question but
// `unauthorized` was told as a server that did not answer, with nothing to
// do but wait, at every start: a member whose credential the server no
// longer knows was never told to join. A server that answers a refusal is
// told as that, with the command that joins; the line is the same at the
// session's next prompt; and the command, run as the line prints it,
// connects the session.
func TestAServerThatRefusesIsNotToldAsOneThatDidNotAnswerThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	var forgets atomic.Bool
	a := startRestBackend(t)
	_, b := startFront(t, startRestBackend(t), func(req *http.Request, _ []byte) (int, string) {
		if forgets.Load() && req.URL.Path == "/v1/team/moved" {
			return http.StatusUnauthorized, unknownCredential
		}
		return 0, ""
	}, nil)
	r := teamOnAServer(t, a)
	second, canon := r.anotherCheckout(r.configDirB, "checkout-2")
	r.movedTo(b, r.secretFile)
	r.putFileIn(second, r.teamFile())
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	pinBefore, _, err := teamstore.LookupPin(r.configDirB, canon)
	if err != nil {
		t.Fatal(err)
	}
	const join = "brigade team join --secret-file <path>"
	refusedWords := func(said string) bool {
		return strings.Contains(said, "refuses the membership this machine holds there") && strings.Contains(said, "`"+join+"`") &&
			!strings.Contains(said, "did not answer") && !strings.Contains(said, "does not match")
	}

	forgets.Store(true)
	s := r.newSession("bob", bobName, "", "", "")
	s.cwd = second
	res := r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if r.mapExists(s) || !refusedWords(res.stdout) {
		t.Fatalf("a start while the server the team moved to does not know the credential: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if res := r.promptedUntilAFollowStarts(s, r.configDirB, canon); r.mapExists(s) || !refusedWords(res.stdout) {
		t.Fatalf("the prompt after that start: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	if pin, _, perr := teamstore.LookupPin(r.configDirB, canon); perr != nil || *pin != *pinBefore {
		t.Fatalf("the pin moved on a refusal: %+v %v", pin, perr)
	}
	// The command the line prints, run as printed with the secret's file.
	line := strings.Replace(printed(t, res.stdout, "brigade team join "), "<path>", r.secretFile, 1)
	if res := r.runIn(second, r.restEnv(s), "", strings.Fields(line)[1:]...); res.exit != 0 || !strings.Contains(res.stdout, "joined team") {
		t.Fatalf("the command the line printed, %q: exit %d\nstdout: %s\nstderr: %s", line, res.exit, res.stdout, res.stderr)
	}
	// The session connects at a prompt after the member's own join.
	r.promptedUntilMoved(s, r.keyAt(b), r.restPrompt)
	r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
}

// joinsCounted puts a front before a server that counts the joins the
// server is asked for: every `/v1/team/join`, whoever makes it and whatever
// the server answers.
func joinsCounted(t *testing.T, backend restBackend) (*atomic.Int32, restBackend) {
	t.Helper()
	joins := &atomic.Int32{}
	_, front := startFront(t, backend, func(req *http.Request, _ []byte) (int, string) {
		if req.URL.Path == "/v1/team/join" {
			joins.Add(1)
		}
		return 0, ""
	}, nil)
	return joins, front
}

// refusalOf is the path of the record a follow leaves in a member's store
// when a server refused the join it made under the membership the machine
// holds there.
func (r *rig) refusalOf(configDir string, backend restBackend) string {
	return filepath.Join(configDir, "teams", r.keyAt(backend), "rejoin-refused.json")
}

// aMinuteLater makes the minute between two follows of a checkout, and
// between two registrations of a session, pass: it takes the two stamps
// away.
func (r *rig) aMinuteLater(s session, configDir, canon string) {
	r.t.Helper()
	for _, stamp := range []string{config.MoveAttemptStamp(r.stateDirOf(s), configDir, canon), config.RegisterRetryStamp(r.stateDirOf(s), s.pid)} {
		if err := os.Remove(stamp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			r.t.Fatalf("a minute's stamp: %v", err)
		}
	}
}

// promptedUntilAFollowStarts prompts s, a minute apart, until one of its
// prompts starts the follow of its checkout's move, and returns what those
// prompts printed. One prompt does it where the machine has time to spare;
// on a loaded machine a prompt can run out of its few seconds before it
// gets there, and the member's next prompt does it.
func (r *rig) promptedUntilAFollowStarts(s session, configDir, canon string) result {
	r.t.Helper()
	var said result
	testutil.Eventually(r.t, waitLong, pollEvery, func() bool {
		r.aMinuteLater(s, configDir, canon)
		res := r.restPrompt(s)
		said.stdout += res.stdout
		said.stderr += res.stderr
		return strings.Contains(res.stderr, "started the follow of the team's move")
	})
	return said
}

// TestARefusedRejoinIsAskedForOnceAndLeavesTheMembersOwnJoinToHimThroughTheRealBinary:
// a member followed the team to its new server, was put out of it there,
// and the move was closed by a rotation: the ticket admits nobody. His
// other checkout has not followed. Until this every session that started
// there, and every minute of prompts, presented the dead ticket under the
// membership the machine holds, and a server counts the joins a principal
// fails: after five in fifteen minutes the member's own `brigade team join`
// with the right secret was answered `rate_limited`.
//
//  1. The first start asks for one join, is refused, and says once that
//     the server refuses the membership and which command joins.
//  2. Four more starts and three prompts a minute apart ask for no join,
//     and say what a team file nobody followed is told, not that line again.
//  3. The command the line printed, run as printed with the file of the
//     secret the server has now, joins him: he is the member he was.
//  4. The record of the refusal is gone with that join.
func TestARefusedRejoinIsAskedForOnceAndLeavesTheMembersOwnJoinToHimThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a := startRestBackend(t)
	joins, b := joinsCounted(t, startRestBackend(t))
	r := teamOnAServer(t, a)
	term := r.terminalEnv(r.configDir)
	second, canon := r.anotherCheckout(r.configDirB, "checkout-2")
	r.movedTo(b, r.secretFile)
	r.putFileIn(second, r.teamFile())
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	onB := r.principalOf(r.configDirB, b)
	// The administrator puts him out of B and closes the move with a new secret.
	if res := r.runIn(r.checkout, term, "", "team", "revoke-member", "--principal", onB); res.exit != 0 {
		t.Fatalf("the revocation on B: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	rotated := filepath.Join(r.dirs.Root, "b-rotated.secret")
	if res := r.runIn(r.checkout, term, "", "team", "rotate-secret", "--secret-file", rotated); res.exit != 0 {
		t.Fatalf("the rotation on B: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	const refusedWords, join = "refuses the membership this machine holds there", "brigade team join --secret-file <path>"
	asked := joins.Load()

	// --- 1. the first start: one join, refused, and the line that helps -----------------------------------
	s := r.newSession("bob", bobName, "", "", "")
	s.cwd = second
	first := r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if r.mapExists(s) || !strings.Contains(first.stdout, refusedWords) || !strings.Contains(first.stdout, "`"+join+"`") || strings.Contains(first.stdout, "does not match") {
		t.Fatalf("the first start after the refusal: stdout %q\nstderr %s", first.stdout, first.stderr)
	}
	if got := joins.Load() - asked; got != 1 {
		t.Fatalf("the first start asked the server for %d joins, want one", got)
	}
	if _, err := os.Stat(r.refusalOf(r.configDirB, b)); err != nil {
		t.Fatalf("the refusal left no record in the member's store: %v", err)
	}

	// --- 2. four more starts and three prompts a minute apart: no join, and the line is not said again ------
	for i := range 4 {
		if res, key := r.startsIn("bob", bobName, second); key != "" || strings.Contains(res.stdout, refusedWords) || !strings.Contains(res.stdout, "not connected") {
			t.Fatalf("start %d after the refusal: attached to %q\nstdout %q\nstderr %s", i+2, key, res.stdout, res.stderr)
		}
	}
	moveLog := filepath.Join(r.stateDir, "logs", "team-move.log")
	for i := range 3 {
		if res := r.promptedUntilAFollowStarts(s, r.configDirB, canon); r.mapExists(s) || strings.Contains(res.stdout, refusedWords) {
			t.Fatalf("prompt %d after the refusal: stdout %q\nstderr %s", i+1, res.stdout, res.stderr)
		}
		// The follow the prompt started has ended before the next one.
		testutil.Eventually(t, waitLong, pollEvery, func() bool {
			return logLines(moveLog, "the team file names another backend for this team, and the move was not followed") == i+1
		})
	}
	if got := joins.Load() - asked; got != 1 {
		t.Fatalf("five starts and three prompts asked the server for %d joins in all, want one", got)
	}

	// --- 3. the command the line printed, with the file of the secret the server has now --------------------
	line := strings.Replace(printed(t, first.stdout, "brigade team join "), "<path>", rotated, 1)
	res := r.runIn(second, r.restEnv(s), "", strings.Fields(line)[1:]...)
	if res.exit != 0 || !strings.Contains(res.stdout, "joined team") || strings.Contains(res.stderr, "rate_limited") {
		t.Fatalf("the member's own join, %q: exit %d\nstdout: %s\nstderr: %s", line, res.exit, res.stdout, res.stderr)
	}
	// The session connects at a prompt after the member's own join.
	r.promptedUntilMoved(s, r.keyAt(b), r.restPrompt)
	if listed := r.principalsAt(b)[bobLabel]; len(listed) != 1 || listed[0] != onB {
		t.Fatalf("he came back to B as %v; he was %q", listed, onB)
	}

	// --- 4. the record of the refusal went with the join -----------------------------------------------------
	if _, err := os.Stat(r.refusalOf(r.configDirB, b)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the record of the refusal after the member's own join: %v", err)
	}
	r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
}

// TestALiftedBanBringsTheMemberBackWhenTheRefusalIsFifteenMinutesOldThroughTheRealBinary:
// a member is banned on the server the team moved to, and a session of his
// in a checkout that has not followed is refused. The ban is lifted. For
// fifteen minutes after the refusal no session presents the ticket again;
// the first to start after that asks once more and is let in, as the
// member he was, with no command of his.
func TestALiftedBanBringsTheMemberBackWhenTheRefusalIsFifteenMinutesOldThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	a := startRestBackend(t)
	joins, b := joinsCounted(t, startRestBackend(t))
	r := teamOnAServer(t, a)
	term := r.terminalEnv(r.configDir)
	second, _ := r.anotherCheckout(r.configDirB, "checkout-2")
	r.movedTo(b, r.secretFile)
	r.putFileIn(second, r.teamFile())
	if res, key := r.bobStarts(); key != r.keyAt(b) {
		t.Fatalf("the member did not follow to B: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	onB := r.principalOf(r.configDirB, b)
	if res := r.runIn(r.checkout, term, "", "team", "revoke-member", "--principal", onB, "--ban"); res.exit != 0 {
		t.Fatalf("the ban on B: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	asked := joins.Load()
	if res, key := r.startsIn("bob", bobName, second); key != "" || !strings.Contains(res.stdout, "not connected") {
		t.Fatalf("a banned member's start: attached to %q\nstdout %q\nstderr %s", key, res.stdout, res.stderr)
	}
	// The ban is lifted: he is revoked, and the move is open.
	if res := r.runIn(r.checkout, term, "", "team", "revoke-member", "--principal", onB); res.exit != 0 {
		t.Fatalf("the lifting of the ban: exit %d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	// Within the fifteen minutes nothing is asked of the server's join.
	if res, key := r.startsIn("bob", bobName, second); key != "" || joins.Load()-asked != 1 {
		t.Fatalf("a start inside the fifteen minutes: attached to %q, %d joins asked for\nstdout %q", key, joins.Load()-asked, res.stdout)
	}
	// Fifteen minutes pass: the record of the refusal is made that old.
	record := r.refusalOf(r.configDirB, b)
	text, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the record of the refusal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(text, &doc); err != nil {
		t.Fatal(err)
	}
	doc["refused_at"] = time.Now().Add(-15*time.Minute - time.Second).UTC().Format(time.RFC3339Nano)
	aged, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, aged, 0o600); err != nil {
		t.Fatal(err)
	}
	res, key := r.startsIn("bob", bobName, second)
	if key != r.keyAt(b) || !strings.Contains(res.stdout, "with the move's ticket") || joins.Load()-asked != 2 {
		t.Fatalf("the start fifteen minutes after the refusal: attached to %q, %d joins asked for\nstdout %q\nstderr %s", key, joins.Load()-asked, res.stdout, res.stderr)
	}
	if listed := r.principalsAt(b)[bobLabel]; len(listed) != 1 || listed[0] != onB {
		t.Fatalf("he came back to B as %v; he was %q", listed, onB)
	}
	if _, err := os.Stat(record); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the record of the refusal after he was let in: %v", err)
	}
}
