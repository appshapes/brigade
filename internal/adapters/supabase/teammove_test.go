//go:build !windows

package supabase

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// ---- a team's move (4.4.10, capability team.move; work item 273) ----

// moveTicket is the canary the move tests record and read: a join secret
// of testTeamID that is no other fixture's, so a leak of THIS string is
// the move's.
var moveTicket = protocol.JoinSecretPrefix + testTeamID + "." + strings.Repeat("cd", 16)

// The destination the move tests record.
const (
	moveURL         = "https://brigade.example.test"
	moveCertificate = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

// movedBody is the answer of record_team_move and read_team_move for
// testTeamID: the destination, and the ticket when one is given
// (read_team_move's alone).
func movedBody(certificate, ticket string) string {
	body := `{"moved":true,"team_id":"` + testTeamID + `","adapter":"rest","url":"` + moveURL + `",`
	if certificate == "" {
		body += `"certificate":null,`
	} else {
		body += `"certificate":"` + certificate + `",`
	}
	if ticket != "" {
		body += `"ticket":"` + ticket + `",`
	}
	return body + `"recorded_at":"2026-11-14T12:00:00.123456+00:00"}`
}

// ticketFile writes text to a file of the rig's and returns its path.
func (r *rig) ticketFile(name, text string) string {
	r.t.Helper()
	path := filepath.Join(r.base, name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return path
}

// moveLeak asserts that the move's ticket is on neither stream and in no
// file of the profile directory, the debug log included.
func moveLeak(t *testing.T, r *rig, got outcome) {
	t.Helper()
	noSecretLeak(t, r, got, false)
	if strings.Contains(got.stdout+got.stderr, moveTicket) || strings.Contains(got.stdout, `"ticket"`) {
		t.Fatalf("the move ticket or a ticket member reached a stream: %s %s", got.stdout, got.stderr)
	}
}

// TestTeamMoveRecordsTheDestinationAndTheTicketFromTheFile:
// record_team_move is called with the bound team, the destination of the
// stdin document and the ticket of the file's first line, and with no
// certificate argument value when the request has none; the result is the
// record and carries no ticket; and the ticket is on no stream and in no
// file of the profile, at debug level too. The flag form sends the same
// call with the certificate.
func TestTeamMoveRecordsTheDestinationAndTheTicketFromTheFile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	path := r.ticketFile("ops.ticket", "  "+moveTicket+"  \na second line that is not read\n")
	args := answerRPC(t, r.be, rpcRecordTeamMove, movedBody("", ""))
	got := r.exec(`{"adapter":"rest","url":"`+moveURL+`"}`, "--log-level", "debug", "team", "move", "--ticket-file", path)
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	if (*args)["p_team_id"] != testTeamID || (*args)["p_adapter"] != "rest" || (*args)["p_url"] != moveURL ||
		(*args)["p_ticket"] != moveTicket || (*args)["p_certificate"] != nil || len(*args) != 5 {
		t.Fatalf("record_team_move args carry other than the team, the destination and the ticket")
	}
	moveLeak(t, r, got)
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if result["team_ref"] != testTeamID || result["moved"] != true || result["adapter"] != "rest" || result["url"] != moveURL || len(result) != 5 {
		t.Fatalf("result = %v", result)
	}
	if _, err := time.Parse(time.RFC3339Nano, str(t, result, "recorded_at")); err != nil {
		t.Fatalf("recorded_at: %v", err)
	}

	args = answerRPC(t, r.be, rpcRecordTeamMove, movedBody(moveCertificate, ""))
	got = r.exec("", "team", "move", "--ticket-file", path, "--adapter", "rest", "--url", moveURL, "--certificate", moveCertificate)
	if got.code != 0 {
		t.Fatalf("the flag form: exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	if (*args)["p_certificate"] != moveCertificate || (*args)["p_ticket"] != moveTicket {
		t.Fatalf("the flag form's record_team_move args carry other than the certificate and the ticket")
	}
	moveLeak(t, r, got)
	if result, _ := decode(t, got.stdout)["result"].(map[string]any); result["certificate"] != moveCertificate || len(result) != 6 {
		t.Fatalf("the flag form's result = %v", result)
	}
}

// TestTeamMoveClearRemovesTheRecord: --clear calls clear_team_move with the
// bound team alone, reads no file and no stdin, and answers the team and
// moved false, whether or not there was a record.
func TestTeamMoveClearRemovesTheRecord(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	for _, cleared := range []string{"true", "false"} {
		args := answerRPC(t, r.be, rpcClearTeamMove, `{"moved":false,"team_id":"`+testTeamID+`","cleared":`+cleared+`}`)
		result := r.ok("team", "move", "--clear")
		if (*args)["p_team_id"] != testTeamID || len(*args) != 1 {
			t.Fatalf("clear_team_move args = %v", *args)
		}
		if result["team_ref"] != testTeamID || result["moved"] != false || len(result) != 2 {
			t.Fatalf("cleared %s: result = %v", cleared, result)
		}
	}
}

// TestTeamMoveRefusals: everything the adapter can refuse on its own it
// refuses before any call (no --ticket-file, a relative one, --clear beside
// another flag and --adapter without --url are `usage`; a file that is not
// there is `config`; a file with no join secret, a join secret of another
// team and a destination with no url are `invalid_input` naming the
// member); a caller the backend refuses reads the uniform unauthorized; a
// project without the migration is `config` naming it, for a record and
// for a clear; and an answer about another team is `internal`. No refusal
// carries the ticket.
func TestTeamMoveRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	path := r.ticketFile("ops.ticket", moveTicket+"\n")
	const destination = `{"adapter":"rest","url":"` + moveURL + `"}`
	r.fails("usage", 2, destination, "team", "move")
	r.fails("usage", 2, destination, "team", "move", "--ticket-file", "relative.ticket")
	r.fails("usage", 2, "", "team", "move", "--clear", "--ticket-file", path)
	r.fails("usage", 2, "", "team", "move", "--ticket-file", path, "--adapter", "rest")
	got := r.fails("config", 11, destination, "team", "move", "--ticket-file", filepath.Join(r.base, "no-such.ticket"))
	if details(t, got.stdout)["reason"] != "ticket_file_missing" {
		t.Fatalf("a ticket file that is not there: details = %v", details(t, got.stdout))
	}
	for name, text := range map[string]string{
		"no join secret":                 "not a ticket\n",
		"an empty file":                  "",
		"a join secret of another team":  protocol.JoinSecretPrefix + otherTeamID + "." + strings.Repeat("cd", 16) + "\n",
		"a join secret with a space":     protocol.JoinSecretPrefix + testTeamID + ".cd cd\n",
		"a file larger than a ticket is": strings.Repeat("x", 64<<10+1),
	} {
		bad := r.ticketFile("bad.ticket", text)
		got := r.fails("invalid_input", 3, destination, "team", "move", "--ticket-file", bad)
		if details(t, got.stdout)["field"] != "ticket" {
			t.Fatalf("%s: details = %v", name, details(t, got.stdout))
		}
		if strings.Contains(got.stdout+got.stderr, otherTeamID) {
			t.Fatalf("%s: the refusal carries part of the file", name)
		}
	}
	got = r.fails("invalid_input", 3, `{"adapter":"rest"}`, "team", "move", "--ticket-file", path)
	if details(t, got.stdout)["field"] != "url" {
		t.Fatalf("a destination with no url: details = %v", details(t, got.stdout))
	}
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls for refusals the adapter makes on its own", r.be.total())
	}

	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized")
	}
	for _, argv := range [][]string{{"team", "move", "--ticket-file", path}, {"team", "move", "--clear"}} {
		stdin := destination
		if len(argv) == 3 {
			stdin = ""
		}
		refused := r.fails("unauthorized", 5, stdin, argv...)
		if refused.stdout != newFailure(t, errNotMember()) {
			t.Fatalf("%v by a member that is not the owner: %q, want the uniform unauthorized", argv, refused.stdout)
		}
		moveLeak(t, r, refused)
	}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusNotFound, "PGRST202", "Could not find the function brigade.record_team_move in the schema cache")
	}
	for _, argv := range [][]string{{"team", "move", "--ticket-file", path}, {"team", "move", "--clear"}} {
		stdin := destination
		if len(argv) == 3 {
			stdin = ""
		}
		drift := r.fails("config", 11, stdin, argv...)
		if details(t, drift.stdout)["reason"] != reasonMigrationDrift || !strings.Contains(drift.stdout, teamMoveMigration) {
			t.Fatalf("%v on a project without the migration: %s", argv, drift.stdout)
		}
		moveLeak(t, r, drift)
	}
	answerRPC(t, r.be, rpcRecordTeamMove, strings.Replace(movedBody("", ""), testTeamID, otherTeamID, 1))
	moveLeak(t, r, r.fails("internal", 1, destination, "team", "move", "--ticket-file", path))
	answerRPC(t, r.be, rpcClearTeamMove, `{"moved":true,"team_id":"`+testTeamID+`"}`)
	r.fails("internal", 1, "", "team", "move", "--clear")
}

// TestTeamMovedReadsTheRecordAndWritesTheTicketToTheFileOnly: with no move
// recorded the answer is the team and moved false and no file is written;
// with one, the answer is the record, and the ticket reaches the
// --ticket-file (0600) and nothing else, at debug level too; without the
// flag the ticket goes nowhere at all.
func TestTeamMovedReadsTheRecordAndWritesTheTicketToTheFileOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	path := filepath.Join(r.base, "read.ticket")
	args := answerRPC(t, r.be, rpcReadTeamMove, `{"moved":false,"team_id":"`+testTeamID+`"}`)
	result := r.ok("team", "moved", "--ticket-file", path)
	if (*args)["p_team_id"] != testTeamID || len(*args) != 1 {
		t.Fatalf("read_team_move args = %v", *args)
	}
	if result["team_ref"] != testTeamID || result["moved"] != false || len(result) != 2 {
		t.Fatalf("no move recorded: result = %v", result)
	}
	absent(t, path)

	answerRPC(t, r.be, rpcReadTeamMove, movedBody(moveCertificate, moveTicket))
	got := r.exec("", "--log-level", "debug", "team", "moved")
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	moveLeak(t, r, got)
	result, _ = decode(t, got.stdout)["result"].(map[string]any)
	if result["team_ref"] != testTeamID || result["moved"] != true || result["adapter"] != "rest" || result["url"] != moveURL ||
		result["certificate"] != moveCertificate || len(result) != 6 {
		t.Fatalf("a recorded move, no --ticket-file: result = %v", result)
	}
	absent(t, path)

	got = r.exec("", "--log-level", "debug", "team", "moved", "--ticket-file", path)
	if got.code != 0 {
		t.Fatalf("exit %d: %s %s", got.code, got.stdout, got.stderr)
	}
	moveLeak(t, r, got)
	if result, _ := decode(t, got.stdout)["result"].(map[string]any); result["ticket_file"] != path || len(result) != 7 {
		t.Fatalf("a recorded move with --ticket-file: result = %v", result)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ticket file: %v %v", info, err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // the test's own temp file
	if err != nil || string(data) != moveTicket+"\n" {
		t.Fatalf("the ticket file does not hold the ticket the backend answered: %v", err)
	}
}

// TestTeamMovedRefusalsAndABackendWithoutTheMigration: a relative
// --ticket-file is `usage` and one in a directory that is not there is
// `config`, both before any call; a caller that is not an active member
// reads the uniform unauthorized and no file is written; a project without
// the migration answers moved false and writes no file; and an answer
// about another team, one whose destination the protocol refuses, and one
// whose ticket is not this team's are `internal`, with no file written and
// no ticket on a stream.
func TestTeamMovedRefusalsAndABackendWithoutTheMigration(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	path := filepath.Join(r.base, "read.ticket")
	r.fails("usage", 2, "", "team", "moved", "--ticket-file", "relative.ticket")
	got := r.fails("config", 11, "", "team", "moved", "--ticket-file", filepath.Join(r.base, "missing", "t"))
	if details(t, got.stdout)["reason"] != "ticket_file_unwritable" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	if r.be.total() != 0 {
		t.Fatalf("%d backend calls before the local checks passed", r.be.total())
	}

	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusForbidden, "42501", "brigade:unauthorized")
	}
	if refused := r.fails("unauthorized", 5, "", "team", "moved", "--ticket-file", path); refused.stdout != newFailure(t, errNotMember()) {
		t.Fatalf("team moved by a caller that is not a member: %q, want the uniform unauthorized", refused.stdout)
	}
	absent(t, path)

	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, _, _ string, _ map[string]any) {
		postgrest(w, http.StatusNotFound, "PGRST202", "Could not find the function brigade.read_team_move in the schema cache")
	}
	if result := r.ok("team", "moved", "--ticket-file", path); result["team_ref"] != testTeamID || result["moved"] != false || len(result) != 2 {
		t.Fatalf("a project without the migration: result = %v", result)
	}
	absent(t, path)

	for name, body := range map[string]string{
		"about another team":           strings.Replace(movedBody("", moveTicket), testTeamID, otherTeamID, 1),
		"no move, about another team":  `{"moved":false,"team_id":"` + otherTeamID + `"}`,
		"a destination with no url":    strings.Replace(movedBody("", moveTicket), moveURL, "", 1),
		"a ticket of another team":     movedBody("", protocol.JoinSecretPrefix+otherTeamID+"."+strings.Repeat("cd", 16)),
		"a record with no ticket":      movedBody("", ""),
		"a record with no recorded_at": strings.Replace(movedBody("", moveTicket), `"recorded_at":"2026-11-14T12:00:00.123456+00:00"`, `"recorded_at":null`, 1),
	} {
		answerRPC(t, r.be, rpcReadTeamMove, body)
		got := r.fails("internal", 1, "", "team", "moved", "--ticket-file", path)
		if strings.Contains(got.stdout+got.stderr, protocol.JoinSecretPrefix) {
			t.Fatalf("%s: a ticket reached a stream: %s %s", name, got.stdout, got.stderr)
		}
		absent(t, path)
	}
}
