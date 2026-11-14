//go:build !windows

package rest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapters/rest/stub"
	"github.com/appshapes/brigade/internal/protocol"
)

// The three commands of capability team.take_in (4.4.10): `team take-in`,
// `team open-move` and `team close-move`, against the stand-in server,
// which carries their endpoints, and against one that leaves them out.

// elsewhereRef is the reference of a team on a backend that names its
// teams with UUIDs.
const elsewhereRef = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"

// secretOf is a join secret of team with the given secret half, put
// together here: no file of the repository holds the shape of one.
func secretOf(team, half string) string {
	return protocol.JoinSecretPrefix + team + "." + half
}

// takeInDocument is the stdin document of a take-in.
func takeInDocument(secret, name, label string) string {
	return `{"join_secret":"` + secret + `","team_name":"` + name + `","human_label":"` + label + `","unknown":1}`
}

// takeIn runs `team take-in` with the document, labelled alice, and
// returns the result.
func (r *rig) takeIn(secret, name string) map[string]any {
	r.t.Helper()
	return r.ok(takeInDocument(secret, name, "alice@example.com"), "team", "take-in")
}

// ticketIn reads the move ticket a command wrote to path and checks the
// file: mode 0600, one line, a join secret of team.
func ticketIn(t *testing.T, path, team string) string {
	t.Helper()
	ticket := secretIn(t, path)
	if parsed, _ := protocol.ParseJoinSecret(ticket); parsed.TeamRef() != team {
		t.Errorf("the ticket in the file names another team")
	}
	return ticket
}

// half is the secret half of a join secret or a move ticket: what follows
// its last dot. A test that looks for a ticket on a stream looks for this
// as well as for the whole. The redactor replaces every text of a join
// secret's form in a log line, so the whole ticket can be found on stderr
// by no test, whatever the code does; the half is not of that form, and is
// found wherever any part of the ticket was written.
func half(secret string) string {
	return secret[strings.LastIndexByte(secret, '.')+1:]
}

// TestTeamTakeIn: a team the server does not hold is taken in under the
// reference inside its secret and the caller owns it; the profile is
// bound and holds the credential the server issued; the same command
// again, and anyone else's with the secret, is a join; another secret for
// the reference is `conflict` other_join_secret and binds nothing; and the
// secret is in no output and no log line.
func TestTeamTakeIn(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	alice := newRig(t, b.url())
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")

	got := alice.exec(takeInDocument(secret, "ops", "alice@example.com"), "team", "take-in")
	if got.code != 0 {
		t.Fatalf("team take-in: exit %d (%s %s)", got.code, got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if len(result) != 5 || result["team_ref"] != elsewhereRef || result["team_name"] != "ops" ||
		result["taken_in"] != true || result["owner"] != true || result["principal_ref"] == "" {
		t.Errorf("team take-in: result %v, want exactly {team_ref, team_name, principal_ref, taken_in true, owner true}", result)
	}
	// The server was asked whether it carries the endpoint, then the one request.
	seen := b.seen("")
	if len(seen) != 2 || seen[0].Path != pathInfo || seen[1].Path != pathTeamTakeIn {
		t.Fatalf("team take-in made %d requests, want /v1/info and the take-in", len(seen))
	}
	sent := seen[1]
	if len(sent.Body) != 3 || sent.Body["join_secret"] != secret || sent.Body["team_name"] != "ops" || sent.Body["human_label"] != "alice@example.com" {
		t.Errorf("the take-in's body has the members %d, want exactly join_secret, team_name and human_label", len(sent.Body))
	}
	if sent.Authorization != "" {
		t.Errorf("a take-in by a profile that holds no credential carried one")
	}
	// The profile is bound, and the credential the server issued is kept.
	status := alice.ok("", "profile", "status")
	if alice.state() != protocol.ProfileStateJoined || status["team_ref"] != elsewhereRef || status["team_name"] != "ops" ||
		status["human_label"] != "alice@example.com" || status["principal_ref"] != result["principal_ref"] {
		t.Errorf("the profile after a take-in: %v", status)
	}
	issued := alice.storedCredential()
	if issued == "" {
		t.Fatalf("the take-in stored no credential")
	}
	// The team is a team: a session registers, and its taker administers it.
	alice.register("alice-session")
	if closed := alice.ok("", "team", "close-move"); closed["closed"] != false || closed["team_ref"] != elsewhereRef {
		t.Errorf("team close-move by the taker of a team that is not moving: %v", closed)
	}

	// Again: the bound team itself is a repeat and proceeds, as a join. No
	// new credential, and the name the team has.
	again := alice.takeIn(secret, "renamed")
	if len(again) != 5 || again["taken_in"] != false || again["owner"] != true || again["team_name"] != "ops" || again["principal_ref"] != result["principal_ref"] {
		t.Errorf("the same take-in again: %v, want taken_in false, owner true and the team as it is", again)
	}
	if alice.storedCredential() != issued {
		t.Errorf("a repeated take-in replaced the credential")
	}
	if last := b.seen(pathTeamTakeIn); last[len(last)-1].Authorization != "Bearer "+issued {
		t.Errorf("a repeated take-in did not act as the principal the profile holds")
	}

	// Someone else with the secret, from a file and flags: a member that
	// does not own. Stdin is not read.
	file := filepath.Join(t.TempDir(), "ops.secret")
	if err := os.WriteFile(file, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bob := newRig(t, b.url())
	joined := bob.ok("not JSON", "team", "take-in", "--secret-file", file, "--name", "renamed", "--label", "bob@example.com")
	if joined["taken_in"] != false || joined["owner"] != false || joined["team_name"] != "ops" || joined["principal_ref"] == result["principal_ref"] {
		t.Errorf("a second principal's take-in: %v, want another principal, taken_in false, owner false", joined)
	}
	if status := bob.ok("", "profile", "status"); status["team_ref"] != elsewhereRef || status["human_label"] != "bob@example.com" {
		t.Errorf("the second principal's profile: %v", status)
	}
	notOwner := bob.fails(protocol.CodeUnauthorized, "", "team", "close-move")
	ticket := filepath.Join(t.TempDir(), "bob.ticket")
	if refused := bob.fails(protocol.CodeUnauthorized, "", "team", "open-move", "--ticket-file", ticket); errorOf(t, refused.stdout) != errorOf(t, notOwner.stdout) {
		t.Errorf("open-move and close-move by a member that is not the owner differ:\n%s%s", refused.stdout, notOwner.stdout)
	}
	if _, err := os.Stat(ticket); !os.IsNotExist(err) {
		t.Errorf("a refused open-move wrote the ticket file (%v)", err)
	}

	// Another secret for the same reference: a conflict, and nothing is bound.
	carol := newRig(t, b.url())
	other := secretOf(elsewhereRef, "another-secret-for-the-reference")
	conflict := carol.fails(protocol.CodeConflict, takeInDocument(other, "ops", "carol@example.com"), "team", "take-in")
	if details(t, conflict.stdout)["reason"] != "other_join_secret" {
		t.Errorf("a take-in under another secret: details %v, want reason other_join_secret", details(t, conflict.stdout))
	}
	if status := carol.ok("", "profile", "status"); status["team_ref"] != nil || carol.storedCredential() != "" {
		t.Errorf("a refused take-in bound the profile or stored a credential: %v", status)
	}

	// No join secret reached stdout, stderr or a log line, at debug level.
	for _, r := range []*rig{alice, bob, carol} {
		if out := r.output(); strings.Contains(out, "kept-from-the-other-backend") || strings.Contains(out, "another-secret-for-the-reference") {
			t.Errorf("a join secret is on stdout or stderr")
		}
	}
}

// errorOf is the error object of a failing envelope, as text.
func errorOf(t *testing.T, out string) string {
	t.Helper()
	object, _ := decode(t, out)["error"].(map[string]any)
	return strings.Join([]string{str(t, object, "code"), str(t, object, "message")}, ": ")
}

// TestTeamTakeInRefusals: what the adapter refuses itself, before any
// request: the binding rule, a request that fails its shape, a secret
// that is not one, a secret file that cannot be read, and a secret on
// argv; and an answer that is not this command's is `internal`.
func TestTeamTakeInRefusals(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	alice := newRig(t, b.url())
	alice.create("here", "alice@example.com")
	b.forget()

	// A profile bound to another team: `conflict`, locally.
	bound := alice.fails(protocol.CodeConflict, takeInDocument(secret, "ops", ""), "team", "take-in")
	if details(t, bound.stdout)["reason"] != reasonProfileBound {
		t.Errorf("a take-in on a profile bound to another team: details %v", details(t, bound.stdout))
	}
	fresh := newRig(t, b.url())
	for name, tc := range map[string]struct {
		input, field string
		args         []string
	}{
		"no secret":                   {`{"team_name":"ops"}`, "join_secret", nil},
		"no name":                     {`{"join_secret":"` + secret + `"}`, "team_name", nil},
		"a secret that is not one":    {takeInDocument("brg1-nonsense", "ops", ""), "join_secret", nil},
		"a backend that is no object": {`{"join_secret":"` + secret + `","team_name":"ops","backend":"x"}`, "backend", nil},
		"no stdin":                    {``, "stdin", nil},
		"a long --name":               {takeInDocument(secret, "ops", ""), "team_name", []string{"--name", strings.Repeat("n", 65)}},
	} {
		got := fresh.fails(protocol.CodeInvalidInput, tc.input, append([]string{"team", "take-in"}, tc.args...)...)
		if details(t, got.stdout)["field"] != tc.field {
			t.Errorf("%s: details %v, want field %s", name, details(t, got.stdout), tc.field)
		}
		if strings.Contains(got.stdout+got.stderr, "nonsense") {
			t.Errorf("%s: the refusal echoes the secret", name)
		}
	}
	dir := t.TempDir()
	fresh.fails(protocol.CodeUsage, takeInDocument(secret, "ops", ""), "team", "take-in", "--secret-file", "relative.secret")
	fresh.fails(protocol.CodeUsage, takeInDocument(secret, "ops", ""), "team", "take-in", "extra")
	fresh.fails(protocol.CodeUsage, `{"team_name":"ops"}`, "team", "take-in", "--join-secret", secret)
	missing := fresh.fails(protocol.CodeConfig, takeInDocument(secret, "ops", ""), "team", "take-in", "--secret-file", filepath.Join(dir, "missing"))
	if details(t, missing.stdout)["reason"] != "secret_file_missing" {
		t.Errorf("a --secret-file that is not there: details %v", details(t, missing.stdout))
	}
	unreadable := fresh.fails(protocol.CodeConfig, takeInDocument(secret, "ops", ""), "team", "take-in", "--secret-file", dir)
	if details(t, unreadable.stdout)["reason"] != "secret_file_unreadable" {
		t.Errorf("a --secret-file that is a directory: details %v", details(t, unreadable.stdout))
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("the refused take-ins made %d requests, want none", n)
	}
	if strings.Contains(fresh.output(), "kept-from-the-other-backend") {
		t.Errorf("a refused take-in put the secret on stdout or stderr")
	}

	// An answer about another team, and one that says a team was taken in
	// and is not owned, is not this command's result; nothing is bound.
	answer := func(team, takenIn, owner string) string {
		return okEnvelope(`{"team_ref":"` + team + `","team_name":"ops","principal_ref":"p1","taken_in":` + takenIn +
			`,"owner":` + owner + `,"credential":"0123456789abcdef0123456789abcdef"}`)
	}
	for name, body := range map[string]string{
		"another team":                      answer("another-team", "true", "true"),
		"taken in and not owned":            answer(elsewhereRef, "true", "false"),
		"no principal":                      okEnvelope(`{"team_ref":"` + elsewhereRef + `","team_name":"ops","taken_in":true,"owner":true}`),
		"no credential for a new principal": okEnvelope(`{"team_ref":"` + elsewhereRef + `","team_name":"ops","principal_ref":"p1","taken_in":true,"owner":true}`),
	} {
		b.answerWith(only(pathTeamTakeIn, http.StatusOK, body))
		got := fresh.fails(protocol.CodeInternal, takeInDocument(secret, "ops", ""), "team", "take-in")
		if details(t, got.stdout)["reason"] != reasonBackendResponse {
			t.Errorf("%s: details %v, want reason %s", name, details(t, got.stdout), reasonBackendResponse)
		}
		if status := fresh.ok("", "profile", "status"); status["team_ref"] != nil {
			t.Errorf("%s: the profile was bound by an answer that is not the command's", name)
		}
	}
	// A server that breaks the contract and repeats the secret in its
	// refusal cannot make the adapter print it: a refusal's text goes
	// through the redactor, which replaces every text of a join secret's
	// form (readEnvelope).
	b.answerWith(only(pathTeamTakeIn, http.StatusConflict,
		`{"ok":false,"protocol_version":"1","error":{"code":"conflict","message":"no team for `+secret+
			`","retryable":false,"details":{"reason":"other_join_secret","presented":"`+secret+`"}}}`))
	echoed := fresh.fails(protocol.CodeConflict, takeInDocument(secret, "ops", ""), "team", "take-in")
	if strings.Contains(echoed.stdout+echoed.stderr, "kept-from-the-other-backend") {
		t.Errorf("a join secret the server repeated in its refusal reached stdout or stderr: %s", echoed.stdout)
	}
	b.answerWith(nil)

	// The backend member configures a profile that names none, as on a join.
	bare := newRig(t, "")
	taken := bare.ok(`{"join_secret":"`+secret+`","team_name":"ops","backend":{"url":"`+b.url()+`"}}`, "team", "take-in")
	if taken["taken_in"] != true || bare.ok("", "profile", "status")["url"] != b.url() {
		t.Errorf("a take-in whose document names the backend: %v", taken)
	}
	for _, r := range b.seen(pathTeamTakeIn) {
		if _, sent := r.Body["backend"]; sent {
			t.Errorf("the backend member travelled to the server")
		}
	}
}

// TestTeamTakeInCarriesTheCreationToken: a server that requires a creation
// token requires it of a take-in, and the adapter reads it from a file and
// sends it as it does on a create.
func TestTeamTakeInCarriesTheCreationToken(t *testing.T) {
	t.Parallel()
	const token = "operator-creation-token"
	b := newBackendWith(t, stub.Options{CreateToken: token})
	r := newRig(t, b.url())
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	document := takeInDocument(secret, "ops", "alice@example.com")
	r.fails(protocol.CodeUnauthorized, document, "team", "take-in")

	dir := t.TempDir()
	wrong, right := filepath.Join(dir, "wrong"), filepath.Join(dir, "right")
	for path, content := range map[string]string{wrong: "another-token\n", right: token + "\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.fails(protocol.CodeUnauthorized, document, "team", "take-in", "--create-token-file", wrong)
	b.forget()
	r.fails(protocol.CodeUsage, document, "team", "take-in", "--create-token-file", "relative")
	missing := r.fails(protocol.CodeConfig, document, "team", "take-in", "--create-token-file", filepath.Join(dir, "missing"))
	if details(t, missing.stdout)["reason"] != reasonCreateTokenFile {
		t.Errorf("a --create-token-file that is not there: details %v", details(t, missing.stdout))
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("a refused --create-token-file made %d requests, want none", n)
	}
	if status := r.ok("", "profile", "status"); status["team_ref"] != nil {
		t.Errorf("a refused take-in bound the profile: %v", status)
	}
	taken := r.ok(document, "team", "take-in", "--create-token-file", right)
	if taken["taken_in"] != true || taken["owner"] != true {
		t.Errorf("team take-in with the token: %v", taken)
	}
	if seen := b.seen(pathTeamTakeIn); len(seen) != 1 || seen[0].CreateToken != token {
		t.Errorf("the take-in did not carry the token in Brigade-Create-Token")
	}
	if strings.Contains(r.output(), token) || strings.Contains(r.output(), "kept-from-the-other-backend") {
		t.Errorf("the creation token or the join secret appears on stdout or stderr")
	}
}

// TestTeamOpenAndCloseMove: the owner's open-move writes the ticket to the
// file and to nothing else and answers the five members of 4.4.10; a join
// with the ticket is a member that does not own; a second open replaces
// the ticket and says so; close-move ends the move and then has nothing
// to close; and a member that is not the owner is refused both.
func TestTeamOpenAndCloseMove(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	alice := newRig(t, b.url())
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	alice.takeIn(secret, "ops")
	bob := newRig(t, b.url())
	bob.join(secret, "bob@example.com")
	path := filepath.Join(t.TempDir(), "ops.ticket")
	b.forget()

	got := alice.exec("", "team", "open-move", "--ticket-file", path)
	if got.code != 0 {
		t.Fatalf("team open-move: exit %d (%s %s)", got.code, got.stdout, got.stderr)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if len(result) != 5 || result["team_ref"] != elsewhereRef || result["team_name"] != "ops" ||
		result["replaced"] != false || result["ticket_file"] != path {
		t.Errorf("team open-move: result %v, want exactly {team_ref, team_name, ends_at, replaced false, ticket_file}", result)
	}
	ends, err := time.Parse(time.RFC3339Nano, str(t, result, "ends_at"))
	if days := time.Until(ends).Hours() / 24; err != nil || days < 29 || days > 30 {
		t.Errorf("ends_at %v (%v): want thirty days from now", result["ends_at"], err)
	}
	ticket := ticketIn(t, path, elsewhereRef)
	if ticket == secret {
		t.Errorf("the ticket is the join secret")
	}
	if out := alice.output(); strings.Contains(out, ticket) || strings.Contains(out, half(ticket)) || strings.Contains(got.stdout, `"ticket"`) {
		t.Errorf("the ticket, a part of it, or a ticket member, reached stdout or stderr")
	}
	if !strings.Contains(got.stderr, "--ticket-file") {
		t.Errorf("stderr %q does not say where the ticket went", got.stderr)
	}
	seen := b.seen("")
	if len(seen) != 2 || seen[0].Path != pathInfo || seen[1].Path != pathTeamOpenMove ||
		len(seen[1].Body) != 1 || seen[1].Body["team_ref"] != elsewhereRef {
		t.Errorf("team open-move made %v, want /v1/info and an open-move that names the team alone", seen)
	}

	// A member comes in with the ticket, and is a member that does not own.
	carol := newRig(t, b.url())
	if joined := carol.join(ticket, "carol@example.com"); joined["team_ref"] != elsewhereRef {
		t.Errorf("team join with the ticket: %v", joined)
	}
	for who, r := range map[string]*rig{"a member the secret let in": bob, "a member the ticket let in": carol} {
		other := filepath.Join(t.TempDir(), "refused.ticket")
		r.fails(protocol.CodeUnauthorized, "", "team", "open-move", "--ticket-file", other)
		if _, err := os.Stat(other); !os.IsNotExist(err) {
			t.Errorf("an open-move refused to %s wrote the ticket file (%v)", who, err)
		}
		r.fails(protocol.CodeUnauthorized, "", "team", "close-move")
	}

	// A second open, for the days asked: the ticket before it admits nobody,
	// the answer says so, and the file is replaced.
	b.forget()
	second := alice.ok("", "team", "open-move", "--ticket-file", path, "--ticket-days", "7")
	if second["replaced"] != true {
		t.Errorf("a second open-move: replaced %v, want true", second["replaced"])
	}
	if sent := b.seen(pathTeamOpenMove); len(sent) != 1 || len(sent[0].Body) != 2 || sent[0].Body["ticket_days"] != float64(7) {
		t.Errorf("an open-move with --ticket-days 7 did not send exactly team_ref and ticket_days 7")
	}
	ends, err = time.Parse(time.RFC3339Nano, str(t, second, "ends_at"))
	if days := time.Until(ends).Hours() / 24; err != nil || days < 6 || days > 7 {
		t.Errorf("ends_at of a move of seven days %v (%v)", second["ends_at"], err)
	}
	next := ticketIn(t, path, elsewhereRef)
	if next == ticket {
		t.Errorf("a second open-move left the first ticket in the file")
	}
	stranger := newRig(t, b.url())
	stranger.fails(protocol.CodeUnauthorized, `{"join_secret":"`+ticket+`"}`, "team", "join")
	newRig(t, b.url()).join(next, "dave@example.com")

	// The owner closes the move: its ticket admits nobody, the members stay,
	// and there is nothing left to close.
	closed := alice.ok("", "team", "close-move")
	if len(closed) != 3 || closed["closed"] != true || closed["team_ref"] != elsewhereRef || closed["team_name"] != "ops" {
		t.Errorf("team close-move: %v, want exactly {team_ref, team_name, closed true}", closed)
	}
	stranger.fails(protocol.CodeUnauthorized, `{"join_secret":"`+next+`"}`, "team", "join")
	if again := alice.ok("", "team", "close-move"); again["closed"] != false {
		t.Errorf("a second close-move: %v, want closed false", again)
	}
	if members, _ := carol.ok("", "team", "members")["members"].([]any); len(members) != 4 {
		t.Errorf("the roster after the move was closed: %d members, want the four that came in", len(members))
	}
	for _, r := range []*rig{alice, bob, carol, stranger} {
		out := r.output()
		// Positive control: the debug lines are in what is searched.
		if !strings.Contains(out, `"level":"DEBUG"`) {
			t.Errorf("the output that is searched for a ticket holds no debug line")
		}
		// A rig that presented a ticket on its own stdin never prints it;
		// the owner's rig never held one but in the server's answer.
		for _, held := range []string{ticket, next} {
			if strings.Contains(out, held) || strings.Contains(out, half(held)) {
				t.Errorf("a move ticket, or its secret half, is on stdout or stderr")
			}
		}
	}
}

// TestTeamOpenMoveRefusals: --ticket-file is required, absolute and in a
// directory that exists, and --ticket-days is 1 to 365, all checked before
// any request; an answer that says ok and that the adapter cannot use is
// `internal`, with a line on stderr that says the move was opened and its
// ticket lost; and a file that cannot be written AFTER the move was opened
// is `config` ticket_file_unwritable, with the ticket on no stream.
func TestTeamOpenMoveRefusals(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	alice := newRig(t, b.url())
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	alice.takeIn(secret, "ops")
	path := filepath.Join(t.TempDir(), "ops.ticket")
	b.forget()
	alice.fails(protocol.CodeUsage, "", "team", "open-move")
	alice.fails(protocol.CodeUsage, "", "team", "open-move", "--ticket-file", "relative.ticket")
	alice.fails(protocol.CodeUsage, "", "team", "open-move", "--ticket-file", path, "extra")
	for _, days := range []string{"0", "366", "-3", "many"} {
		alice.fails(protocol.CodeUsage, "", "team", "open-move", "--ticket-file", path, "--ticket-days", days)
	}
	alice.fails(protocol.CodeUsage, "", "team", "close-move", "--ticket-file", path)
	missing := alice.fails(protocol.CodeConfig, "", "team", "open-move", "--ticket-file", filepath.Join(t.TempDir(), "missing", "t"))
	if details(t, missing.stdout)["reason"] != reasonTicketFile {
		t.Errorf("a --ticket-file in a directory that does not exist: details %v", details(t, missing.stdout))
	}
	if n := len(b.seen("")); n != 0 {
		t.Errorf("the refused open-moves made %d requests, want none", n)
	}

	// An answer that says ok and is not this command's result is `internal`,
	// nothing is written, and stderr says what it cost.
	const lostLine = "the ticket is lost"
	here := secretOf(elsewhereRef, strings.Repeat("cd", 16))
	foreign := secretOf("another-team", strings.Repeat("ab", 16))
	answer := func(team, ticket string) string {
		return okEnvelope(`{"team_ref":"` + team + `","team_name":"ops","ticket":"` + ticket + `","ends_at":"2026-12-14T12:00:00Z","replaced":false}`)
	}
	for name, body := range map[string]string{
		"another team":             answer("another-team", foreign),
		"a ticket of another team": answer(elsewhereRef, foreign),
		"a ticket that is not one": answer(elsewhereRef, "not-a-ticket"),
		"no ticket":                okEnvelope(`{"team_ref":"` + elsewhereRef + `","team_name":"ops","ends_at":"2026-12-14T12:00:00Z","replaced":false}`),
		"no end":                   okEnvelope(`{"team_ref":"` + elsewhereRef + `","team_name":"ops","ticket":"` + here + `","replaced":false}`),
	} {
		b.answerWith(only(pathTeamOpenMove, http.StatusOK, body))
		got := alice.fails(protocol.CodeInternal, "", "team", "open-move", "--ticket-file", path)
		// The whole, the secret half, and the text of a ticket that is no
		// join secret in form, which no pattern of the redactor knows.
		for _, held := range []string{here, half(here), foreign, half(foreign), "not-a-ticket"} {
			if strings.Contains(got.stdout+got.stderr, held) {
				t.Errorf("%s: the refused answer's ticket, or a part of it, reached a stream", name)
			}
		}
		if !strings.Contains(got.stderr, lostLine) {
			t.Errorf("%s: stderr does not say the move was opened and its ticket lost: %q", name, got.stderr)
		}
	}
	b.answerWith(only(pathTeamOpenMove, http.StatusOK, "not an envelope"))
	if got := alice.fails(protocol.CodeInternal, "", "team", "open-move", "--ticket-file", path); strings.Contains(got.stderr, lostLine) {
		t.Errorf("an answer that is not an envelope: stderr claims a move: %q", got.stderr)
	}
	b.answerWith(nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a malformed answer wrote the ticket file (%v)", err)
	}

	// The directory goes away between the check and the write: the move was
	// opened, the ticket is lost, and the answer says what to do.
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path == pathTeamOpenMove {
			_ = os.Remove(dir)
		}
		return nil
	})
	lost := alice.fails(protocol.CodeConfig, "", "team", "open-move", "--ticket-file", filepath.Join(dir, "t"))
	b.answerWith(nil)
	if details(t, lost.stdout)["reason"] != reasonTicketFile || !strings.Contains(lost.stdout, "open the move again") {
		t.Errorf("a write that failed after the move was opened: %s", lost.stdout)
	}
	if strings.Contains(lost.stdout+lost.stderr, protocol.JoinSecretPrefix+elsewhereRef+".") {
		t.Errorf("the lost ticket reached stdout or stderr")
	}
	// The same loss with a ticket this test knows, so that its secret half
	// can be looked for: the stand-in's own ticket is in no hand but the
	// adapter's, and the redactor hides the whole of it from the check above.
	again := filepath.Join(t.TempDir(), "gone-again")
	if err := os.Mkdir(again, 0o700); err != nil {
		t.Fatal(err)
	}
	b.answerWith(func(r *seenRequest) *canned {
		if r.Path != pathTeamOpenMove {
			return nil
		}
		_ = os.Remove(again)
		return &canned{status: http.StatusOK, body: answer(elsewhereRef, here)}
	})
	known := alice.fails(protocol.CodeConfig, "", "team", "open-move", "--ticket-file", filepath.Join(again, "t"))
	b.answerWith(nil)
	if details(t, known.stdout)["reason"] != reasonTicketFile {
		t.Errorf("a write that failed after the move was opened: %s", known.stdout)
	}
	if out := known.stdout + known.stderr; strings.Contains(out, here) || strings.Contains(out, half(here)) {
		t.Errorf("the lost ticket, or its secret half, reached stdout or stderr")
	}
	// The move was opened all the same: the next open replaces it.
	if again := alice.ok("", "team", "open-move", "--ticket-file", path); again["replaced"] != true {
		t.Errorf("an open-move after one whose ticket was lost: replaced %v, want true", again["replaced"])
	}
	alice.ok("", "team", "close-move")

	// The ladder is the other commands': an unbound profile is `config`.
	alice.ok("", "team", "leave")
	for _, args := range [][]string{{"team", "open-move", "--ticket-file", path}, {"team", "close-move"}} {
		unbound := alice.fails(protocol.CodeConfig, "", args...)
		if details(t, unbound.stdout)["reason"] != reasonNoTeamBound {
			t.Errorf("%v on an unbound profile: details %v", args, details(t, unbound.stdout))
		}
	}
	// An answer about another team is not a close of this one.
	alice.takeIn(secret, "ops")
	b.answerWith(only(pathTeamCloseMove, http.StatusOK, okEnvelope(`{"team_ref":"another-team","team_name":"ops","closed":true}`)))
	if got := alice.fails(protocol.CodeInternal, "", "team", "close-move"); details(t, got.stdout)["reason"] != reasonBackendResponse {
		t.Errorf("a close-move answered for another team: details %v", details(t, got.stdout))
	}
}

// TestAServerWithoutTeamTakeIn: a server that leaves the three endpoints
// out does not list `team.take_in` in /v1/info. The adapter asks before
// each of the three commands and answers `config`
// server_lacks_team_take_in, in words that say what is missing, without
// sending the request, so the join secret never travels to a server that
// would answer it `not_found`. `describe` names the capability either
// way, because it never asks.
func TestAServerWithoutTeamTakeIn(t *testing.T) {
	t.Parallel()
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	check := func(t *testing.T, b *backend) {
		t.Helper()
		p := newPair(t, b)
		fresh := newRig(t, b.url())
		path := filepath.Join(t.TempDir(), "ops.ticket")
		for _, tc := range []struct {
			r     *rig
			input string
			args  []string
		}{
			{fresh, takeInDocument(secret, "ops", ""), []string{"team", "take-in"}},
			{p.alice, "", []string{"team", "open-move", "--ticket-file", path}},
			{p.alice, "", []string{"team", "close-move"}},
		} {
			b.forget()
			got := tc.r.fails(protocol.CodeConfig, tc.input, tc.args...)
			if details(t, got.stdout)["reason"] != reasonServerLacksTeamTakeIn {
				t.Errorf("%v: details %v, want reason %s", tc.args, details(t, got.stdout), reasonServerLacksTeamTakeIn)
			}
			if !strings.Contains(got.stdout, "does not offer taking a team in") || !strings.Contains(got.stdout, "operator") {
				t.Errorf("%v: the message does not say what is missing and whose it is to mend: %s", tc.args, got.stdout)
			}
			if seen := b.seen(""); len(seen) != 1 || seen[0].Path != pathInfo {
				t.Errorf("%v made %d requests, want the one to /v1/info", tc.args, len(seen))
			}
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("a refused open-move wrote the ticket file (%v)", err)
		}
		if status := fresh.ok("", "profile", "status"); status["team_ref"] != nil || fresh.storedCredential() != "" {
			t.Errorf("a refused take-in bound the profile or stored a credential: %v", status)
		}
		capabilities, _ := fresh.ok("", "describe")["capabilities"].([]any)
		named := false
		for _, c := range capabilities {
			named = named || c == capTeamTakeIn
		}
		if !named {
			t.Errorf("describe does not name %s; it never asks the server, so it names it either way", capTeamTakeIn)
		}
	}
	t.Run("a server that leaves the endpoints out", func(t *testing.T) {
		t.Parallel()
		check(t, newBackendWith(t, stub.Options{Poll: 20 * time.Millisecond, WithoutTakeIn: true}))
	})
	t.Run("capabilities that name others", func(t *testing.T) {
		t.Parallel()
		b := newBackend(t)
		b.answerWith(only(pathInfo, http.StatusOK, okEnvelope(`{"contract_version":"1","capabilities":["team.admin","team.takein","TEAM.TAKE_IN"]}`)))
		check(t, b)
	})

	// A server that cannot be asked is the failure it is, not a missing capability.
	b := newBackend(t)
	fresh := newRig(t, b.url())
	b.answerWith(only(pathInfo, http.StatusServiceUnavailable, "down"))
	fresh.fails(protocol.CodeUnavailable, takeInDocument(secret, "ops", ""), "team", "take-in")
	if n := len(b.seen(pathTeamTakeIn)); n != 0 {
		t.Errorf("a take-in was sent to a server that could not say whether it carries it")
	}
}
