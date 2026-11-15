//go:build !windows

package rest

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// The retry key of `team take-in` (the contract's section 3b): what lets
// the command, run again after its answer was lost, own the team its first
// run took in. The tests run against the stand-in server, which keeps the
// contract's rule, behind a front that lets the stub act on a request and
// then loses its answer.

// retryKeyPath is where the rig's profile keeps the retry key.
func (r *rig) retryKeyPath() string {
	return filepath.Join(r.cfg, "teams", "default", retryKeyFileName)
}

// keptRetryKey reads take-in-key.json and checks the file: a regular file
// of mode 0600 that holds a key of the contract's form. It fails the test
// when there is none.
func (r *rig) keptRetryKey() retryKey {
	r.t.Helper()
	info, err := os.Lstat(r.retryKeyPath())
	if err != nil {
		r.t.Fatalf("the retry key file: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		r.t.Errorf("the retry key file has mode %v, want a regular file of mode 0600", info.Mode())
	}
	data, err := os.ReadFile(r.retryKeyPath())
	if err != nil {
		r.t.Fatal(err)
	}
	var kept retryKey
	if err := json.Unmarshal(data, &kept); err != nil || !validRetryKey(kept.Key) {
		r.t.Fatalf("the retry key file does not hold a retry key: %v", err)
	}
	return kept
}

// noRetryKey asserts that the profile directory holds no retry key file,
// and nothing a write of one left behind.
func (r *rig) noRetryKey(when string) {
	r.t.Helper()
	entries, err := os.ReadDir(filepath.Dir(r.retryKeyPath()))
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "take-in-key") {
			r.t.Errorf("%s: the profile directory holds %s", when, e.Name())
		}
	}
}

// sentRetryKeys are the retry keys of the take-ins the backend has seen,
// in order, "" for one that carried none.
func sentRetryKeys(b *backend) []string {
	var keys []string
	for _, r := range b.seen(pathTeamTakeIn) {
		key, _ := r.Body["retry_key"].(string)
		keys = append(keys, key)
	}
	return keys
}

// TestATakeInWhoseAnswerWasLostIsRunAgainAndOwnsTheTeam: the server takes
// the team in and its answer never arrives, by a proxy's 502 or by a
// connection that is closed. The command fails `unavailable`, the profile
// holds no credential, and the retry key is in a private file. Run again,
// the command sends the same key, is answered as the owner, writes the
// credential and removes the key file; the server then opens the team's
// move for that credential. A stranger's take-in in between is a member's,
// and the key is on no stream.
func TestATakeInWhoseAnswerWasLostIsRunAgainAndOwnsTheTeam(t *testing.T) {
	t.Parallel()
	for name, how := range map[string]loss{"a proxy answers 502 in the server's place": behindAProxy, "the connection is closed": cutOff} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBackend(t)
			alice := newRig(t, b.url())
			secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
			document := takeInDocument(secret, "ops", "alice@example.com")

			// The first run: the stub takes the team in, and what it answered is lost.
			lost := 0
			b.loseAnswers(func(r *seenRequest) loss {
				if r.Path != pathTeamTakeIn {
					return answered
				}
				lost++
				return how
			})
			failed := alice.fails(protocol.CodeUnavailable, document, "team", "take-in")
			b.loseAnswers(nil)
			if lost != 1 {
				t.Fatalf("%d answers of a take-in were lost, want the one", lost)
			}
			if alice.storedCredential() != "" || alice.ok("", "profile", "status")["team_ref"] != nil {
				t.Fatalf("a take-in whose answer was lost left a credential or a binding: %s", failed.stdout)
			}
			kept := alice.keptRetryKey()
			if kept.URL != b.url() || kept.TeamRef != elsewhereRef || kept.CreatedAt.IsZero() {
				t.Errorf("the retry key file names %q and %q, want the server and the team of the take-in", kept.URL, kept.TeamRef)
			}
			alice.noLeftovers()

			// The server did act: the team is there and has its owner, so
			// whoever else presents the secret, with a key of their own, is a
			// member. This is what the same command would be answered without
			// the key it kept.
			mallory := newRig(t, b.url())
			if stranger := mallory.takeIn(secret, "ops"); stranger["taken_in"] != false || stranger["owner"] != false {
				t.Fatalf("a stranger's take-in of the team the lost answer was about: %v, want a member of a team that is there", stranger)
			}
			mallory.noRetryKey("after a take-in that was answered")
			mallory.fails(protocol.CodeUnauthorized, "", "team", "open-move", "--ticket-file", filepath.Join(t.TempDir(), "m.ticket"))

			// The second run of the same command: the key the first run kept,
			// no credential, and the answer of the owner.
			again := alice.ok(document, "team", "take-in")
			if len(again) != 5 || again["team_ref"] != elsewhereRef || again["team_name"] != "ops" || again["taken_in"] != false || again["owner"] != true {
				t.Fatalf("the take-in run again: %v, want exactly {team_ref, team_name, principal_ref, taken_in false, owner true}", again)
			}
			keys := sentRetryKeys(b)
			if len(keys) != 3 || keys[0] != kept.Key || keys[2] != kept.Key || keys[1] == kept.Key || !validRetryKey(keys[1]) {
				t.Errorf("the take-ins carried the keys of %d requests, want the first run's key again in the third and another in the stranger's", len(keys))
			}
			if last := b.seen(pathTeamTakeIn); last[len(last)-1].Authorization != "" {
				t.Errorf("the take-in run again carried a credential: the profile held none")
			}
			// The credential is written, and only then the key file is gone.
			issued := alice.storedCredential()
			if issued == "" {
				t.Fatalf("the take-in run again stored no credential")
			}
			alice.noRetryKey("after the answer was kept")
			status := alice.ok("", "profile", "status")
			if alice.state() != protocol.ProfileStateJoined || status["team_ref"] != elsewhereRef || status["principal_ref"] != again["principal_ref"] {
				t.Errorf("the profile after the take-in run again: %v", status)
			}

			// The server accepts that credential as the owner's: the move is opened.
			ticket := filepath.Join(t.TempDir(), "ops.ticket")
			opened := alice.ok("", "team", "open-move", "--ticket-file", ticket)
			if opened["team_ref"] != elsewhereRef || opened["replaced"] != false {
				t.Errorf("team open-move by the owner the take-in run again made: %v", opened)
			}
			if sent := b.seen(pathTeamOpenMove); len(sent) != 2 || sent[1].Authorization != "Bearer "+issued {
				t.Errorf("the open-move did not carry the credential the take-in run again stored")
			}
			ticketIn(t, ticket, elsewhereRef)

			// Once more, on a profile that holds its credential: a key of its
			// own, minted for this run and gone with it, and the answer of the
			// principal the credential names.
			third := alice.ok(document, "team", "take-in")
			if third["owner"] != true || third["principal_ref"] != again["principal_ref"] || alice.storedCredential() != issued {
				t.Errorf("a take-in by the owner's profile: %v, want the owner it is and the credential it had", third)
			}
			alice.noRetryKey("after a take-in by a profile that holds its credential")
			if keys := sentRetryKeys(b); len(keys) != 4 || keys[3] == kept.Key || !validRetryKey(keys[3]) {
				t.Errorf("a take-in after the key was removed carried the removed key, or none")
			}

			// The key reached no stream, at debug level. Positive control: the
			// debug lines are in what is searched, and so is the line that says
			// a kept key was used, which names no key.
			for _, r := range []*rig{alice, mallory} {
				out := r.output()
				if !strings.Contains(out, `"level":"DEBUG"`) {
					t.Errorf("the output that is searched for a retry key holds no debug line")
				}
				for _, key := range sentRetryKeys(b) {
					if strings.Contains(out, key) {
						t.Errorf("a retry key is on stdout or stderr")
					}
				}
			}
			if !strings.Contains(alice.output(), "the take-in carries the retry key an earlier attempt kept") {
				t.Errorf("the second run did not say that it used the key the first run kept")
			}
		})
	}
}

// noLeftovers asserts that the profile directory holds nothing a write of
// the retry key left behind: no temporary file beside it.
func (r *rig) noLeftovers() {
	r.t.Helper()
	entries, err := os.ReadDir(filepath.Dir(r.retryKeyPath()))
	if err != nil {
		r.t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			r.t.Errorf("the profile directory holds %s", e.Name())
		}
	}
}

// TestTheRetryKeyStaysUntilTheCredentialIsKept: the key file is written
// before the request and removed only once the credential the answer
// carried is in credential.json. A refusal, an answer that is not the
// command's and a credential that could not be written each leave the key
// where it is, and the next run sends the same one.
func TestTheRetryKeyStaysUntilTheCredentialIsKept(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	document := takeInDocument(secret, "ops", "alice@example.com")

	// The file is there, private, when the request arrives.
	var during os.FileMode
	b.answerWith(func(seen *seenRequest) *canned {
		if seen.Path == pathTeamTakeIn {
			if info, err := os.Lstat(r.retryKeyPath()); err == nil {
				during = info.Mode()
			}
			return &canned{status: http.StatusConflict, body: `{"ok":false,"protocol_version":"1","error":{"code":"conflict","message":"refused","retryable":false,"details":{"reason":"other_join_secret"}}}`}
		}
		return nil
	})
	r.fails(protocol.CodeConflict, document, "team", "take-in")
	if !during.IsRegular() || during.Perm() != 0o600 {
		t.Fatalf("the retry key file while the request was answered: mode %v, want a regular file of mode 0600", during)
	}
	first := r.keptRetryKey()

	// An answer that is not this command's, and one whose credential the
	// adapter cannot use: the key stays, and is the one that is sent again.
	for name, body := range map[string]string{
		"an answer about another team": okEnvelope(`{"team_ref":"another-team","team_name":"ops","principal_ref":"p1","taken_in":true,"owner":true,"credential":"0123456789abcdef0123456789abcdef"}`),
		"no credential":                okEnvelope(`{"team_ref":"` + elsewhereRef + `","team_name":"ops","principal_ref":"p1","taken_in":true,"owner":true}`),
		"a body that is no envelope":   "not an envelope",
	} {
		b.answerWith(only(pathTeamTakeIn, http.StatusOK, body))
		r.fails(protocol.CodeInternal, document, "team", "take-in")
		if kept := r.keptRetryKey(); kept.Key != first.Key {
			t.Errorf("%s: the retry key was replaced by a run that kept no credential", name)
		}
	}
	b.answerWith(nil)

	// The credential cannot be written: the profile directory stops taking
	// files while the server answers, as a disk that fills does. The server
	// took the team in, the command fails, and the key is still there.
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory whatever its mode")
	}
	dir := filepath.Dir(r.retryKeyPath())
	//nolint:gosec // G302: restoring a DIRECTORY mode so the test's cleanup can remove it
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	b.loseAnswers(func(seen *seenRequest) loss {
		if seen.Path == pathTeamTakeIn {
			//nolint:gosec // G302: a DIRECTORY mode, restored below
			_ = os.Chmod(dir, 0o500)
		}
		return answered
	})
	unwritten := r.fails(protocol.CodeInternal, document, "team", "take-in")
	b.loseAnswers(nil)
	if !strings.Contains(unwritten.stdout, "the credential could not be written") {
		t.Fatalf("a take-in whose credential could not be kept: %s", unwritten.stdout)
	}
	//nolint:gosec // G302: a DIRECTORY mode
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if kept := r.keptRetryKey(); kept.Key != first.Key {
		t.Errorf("the retry key was replaced by a run that could not keep its credential")
	}
	if r.storedCredential() != "" {
		t.Fatalf("the credential was written to a directory that takes no file")
	}
	// Every one of those runs sent the one key, and the next owns the team
	// the server took in while the credential could not be written.
	owned := r.ok(document, "team", "take-in")
	if owned["taken_in"] != false || owned["owner"] != true {
		t.Errorf("the take-in after the credential could be written: %v, want the owner of the team that is there", owned)
	}
	keys := sentRetryKeys(b)
	for i, key := range keys {
		if key != first.Key {
			t.Errorf("request %d of %d carried another key than the first run minted", i+1, len(keys))
		}
	}
	if len(keys) != 6 {
		t.Errorf("%d take-ins reached the server, want the six runs", len(keys))
	}
	r.noRetryKey("after the credential was kept")
	if strings.Contains(r.output(), first.Key) {
		t.Errorf("the retry key is on stdout or stderr")
	}
}

// TestARetryKeyIsForOneServerAndOneTeam: a key file an earlier run left is
// used when it names the profile's server and the team of the take-in, and
// is replaced when it names another team or another server, when it holds
// no key, and when it does not parse: a key is sent to nobody but the
// server it was minted for. A file that group or other can read is
// refused, and nothing is sent.
func TestARetryKeyIsForOneServerAndOneTeam(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	document := takeInDocument(secret, "ops", "")
	const planted = "cGxhbnRlZC1ieS10aGUtdGVzdC1uZXZlci1taW50ZWQ"
	write := func(r *rig, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(r.retryKeyPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.retryKeyPath(), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	file := func(url, team string) string {
		return `{"key":"` + planted + `","url":"` + url + `","team_ref":"` + team + `"}`
	}
	// Every take-in below is refused by the server, so each leaves its key.
	b.answerWith(only(pathTeamTakeIn, http.StatusConflict,
		`{"ok":false,"protocol_version":"1","error":{"code":"conflict","message":"refused","retryable":false,"details":{"reason":"other_join_secret"}}}`))

	// Positive control: a file for this server and this team is the key that is sent.
	same := newRig(t, b.url())
	same.ok("", "profile", "init", "--url", b.url())
	write(same, file(b.url(), elsewhereRef), 0o600)
	same.fails(protocol.CodeConflict, document, "team", "take-in")
	if keys := sentRetryKeys(b); len(keys) != 1 || keys[0] != planted {
		t.Fatalf("a key file for the server and the team of the take-in was not the key that was sent")
	}
	for name, content := range map[string]string{
		"a key for another team":       file(b.url(), "another-team"),
		"a key for another server":     file("https://another.example", elsewhereRef),
		"a key under another pin":      `{"key":"` + planted + `","url":"` + b.url() + `","certificate":"sha256:` + strings.Repeat("0", 64) + `","team_ref":"` + elsewhereRef + `"}`,
		"a file that holds no key":     `{"url":"` + b.url() + `","team_ref":"` + elsewhereRef + `"}`,
		"a key that is too short":      `{"key":"twenty-one-chars-long","url":"` + b.url() + `","team_ref":"` + elsewhereRef + `"}`,
		"a file that does not parse":   "not JSON",
		"a file that is empty":         "",
		"a file with a key as a list":  `{"key":["` + planted + `"],"url":"` + b.url() + `","team_ref":"` + elsewhereRef + `"}`,
		"a file that is a JSON string": `"` + planted + `"`,
	} {
		b.forget()
		r := newRig(t, b.url())
		r.ok("", "profile", "init", "--url", b.url())
		write(r, content, 0o600)
		refused := r.fails(protocol.CodeConflict, document, "team", "take-in")
		// A key that could still be honoured where it was minted for is not
		// given up in silence: stderr says that it was replaced, and holds
		// nothing of it. A file that held no key says nothing.
		usable := strings.HasPrefix(name, "a key for another") || strings.HasPrefix(name, "a key under another")
		said := false
		for line := range strings.Lines(refused.stderr) {
			said = said || (strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "cannot be made again from this profile"))
		}
		if said != usable {
			t.Errorf("%s: stderr warns that a kept key was replaced: %v, want %v", name, said, usable)
		}
		if strings.Contains(refused.stdout+refused.stderr, planted) {
			t.Errorf("%s: the key the file held is on stdout or stderr", name)
		}
		keys := sentRetryKeys(b)
		// A key of its own, of this adapter's minting: 32 random bytes in
		// base64url, and nothing the file held.
		if len(keys) != 1 || len(keys[0]) != 43 || strings.Contains(content, keys[0]) {
			t.Errorf("%s: the take-in did not carry a key of its own", name)
			continue
		}
		if kept := r.keptRetryKey(); kept.Key != keys[0] || kept.URL != b.url() || kept.TeamRef != elsewhereRef {
			t.Errorf("%s: the file was not replaced by the key that was sent, for this server and this team", name)
		}
	}

	// A key file others can read is not trusted and not replaced: `config`,
	// by the strict reader, and no take-in is sent.
	b.forget()
	loose := newRig(t, b.url())
	loose.ok("", "profile", "init", "--url", b.url())
	write(loose, file(b.url(), elsewhereRef), 0o644)
	refused := loose.fails(protocol.CodeConfig, document, "team", "take-in")
	if details(t, refused.stdout)["reason"] != "insecure_mode" {
		t.Errorf("a retry key file others can read: details %v, want reason insecure_mode", details(t, refused.stdout))
	}
	if n := len(b.seen(pathTeamTakeIn)); n != 0 {
		t.Errorf("a take-in was sent with a key file others can read")
	}
	if strings.Contains(refused.stdout+refused.stderr, planted) {
		t.Errorf("the refusal of a key file repeats the key")
	}
}

// warnedToMakeTheTakeInAgain reports whether stderr holds the one line that
// says an earlier take-in's key stays, and what makes that take-in again:
// a WARN that names the file, `profile revoke-credentials` and the take-in.
func warnedToMakeTheTakeInAgain(stderr string) bool {
	said := 0
	for line := range strings.Lines(stderr) {
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, retryKeyFileName+" stays") &&
			strings.Contains(line, "`profile revoke-credentials`") && strings.Contains(line, "`team take-in` again") {
			said++
		}
	}
	return said == 1
}

// TestAJoinBetweenTwoTakeInsKeepsTheKeyOfTheFirst: a take-in whose answer
// was lost, `team join` on the same profile, and the take-in run again.
// The profile holds a member's credential by then, and the server answers a
// credential it knows whatever key the request carries: the take-in is the
// member's, `owner: false`, with no credential in its answer. The key of
// the first take-in was never looked at, and is the one way to the owner
// that take-in made, so the file stays, the same key in it, and stderr
// says in one line what makes the take-in again. `profile
// revoke-credentials` and the same command then own the team, and the file
// is gone. A key that this run minted, one whose profile owns the team and
// one that was answered with a credential are each removed as before.
func TestAJoinBetweenTwoTakeInsKeepsTheKeyOfTheFirst(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	alice := newRig(t, b.url())
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	document := takeInDocument(secret, "ops", "alice@example.com")

	// The first run: the stub takes the team in, and what it answered is lost.
	b.loseAnswers(func(r *seenRequest) loss {
		if r.Path != pathTeamTakeIn {
			return answered
		}
		return cutOff
	})
	alice.fails(protocol.CodeUnavailable, document, "team", "take-in")
	b.loseAnswers(nil)
	kept := alice.keptRetryKey()

	// `team join` with the same secret: a member, and a credential in the profile.
	joined := alice.join(secret, "alice@example.com")
	members := alice.storedCredential()
	if members == "" || alice.keptRetryKey().Key != kept.Key {
		t.Fatalf("the join left no credential, or touched the key file")
	}

	// The take-in run again carries that credential and the kept key, and is
	// answered for the member.
	again := alice.exec(document, "team", "take-in")
	if again.code != 0 {
		t.Fatalf("the take-in after the join: exit %d, %s", again.code, again.stdout)
	}
	result, _ := decode(t, again.stdout)["result"].(map[string]any)
	if result["owner"] != false || result["taken_in"] != false || result["principal_ref"] != joined["principal_ref"] {
		t.Fatalf("the take-in after the join: %v, want the member the join made, which does not own", result)
	}
	sent := b.seen(pathTeamTakeIn)
	if last := sent[len(sent)-1]; last.Authorization != "Bearer "+members || last.Body["retry_key"] != kept.Key {
		t.Fatalf("the take-in after the join did not carry the member's credential and the key the first run kept")
	}
	// The key file stays, the same key in it, and the profile holds what it held.
	if still := alice.keptRetryKey(); still.Key != kept.Key || alice.storedCredential() != members {
		t.Errorf("the key of the first take-in was replaced, or the member's credential was")
	}
	if !warnedToMakeTheTakeInAgain(again.stderr) {
		t.Errorf("stderr does not say, in one line, that %s stays and what makes the take-in again:\n%s", retryKeyFileName, again.stderr)
	}
	if strings.Contains(again.stdout+again.stderr, kept.Key) {
		t.Errorf("the key is on stdout or stderr")
	}
	// Asked again as it stands, it says the same and keeps the same.
	if once := alice.exec(document, "team", "take-in"); once.code != 0 || !warnedToMakeTheTakeInAgain(once.stderr) || alice.keptRetryKey().Key != kept.Key {
		t.Errorf("the same take-in once more: exit %d, and the key or the line is not what it was", once.code)
	}

	// The way out the line names: without the member's credential the server
	// looks at the key, and answers for the owner the first take-in made.
	alice.ok("", "profile", "revoke-credentials")
	if alice.keptRetryKey().Key != kept.Key {
		t.Fatalf("revoking the credential touched the key file")
	}
	owned := alice.exec(document, "team", "take-in")
	result, _ = decode(t, owned.stdout)["result"].(map[string]any)
	if owned.code != 0 || result["owner"] != true || result["taken_in"] != false || result["principal_ref"] == joined["principal_ref"] {
		t.Fatalf("the take-in after the credential was revoked: exit %d, %v, want the owner the first take-in made", owned.code, result)
	}
	if warnedToMakeTheTakeInAgain(owned.stderr) {
		t.Errorf("the take-in that owns the team says that the key stays")
	}
	alice.noRetryKey("after the take-in that owns the team")
	if issued := alice.storedCredential(); issued == "" || issued == members {
		t.Errorf("the profile does not hold the owner's credential")
	}
	alice.ok("", "team", "open-move", "--ticket-file", filepath.Join(t.TempDir(), "ops.ticket"))

	// The three answers that settle what a key was for, and remove it.
	const planted = "cGxhbnRlZC1ieS10aGUtdGVzdC1uZXZlci1taW50ZWQ"
	plant := func(r *rig) {
		t.Helper()
		content := `{"key":"` + planted + `","url":"` + b.url() + `","team_ref":"` + elsewhereRef + `"}`
		if err := os.MkdirAll(filepath.Dir(r.retryKeyPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.retryKeyPath(), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		prepare func(r *rig)
		owner   bool
	}{
		// A member's profile with no key of an earlier run: the key is this
		// run's own, which no server kept for anybody.
		"a key this run minted, answered for a member": {func(r *rig) { r.join(secret, "") }, false},
		// A kept key on a profile with no credential, which the server does
		// not keep: the answer is a stranger's join and carries a credential.
		"a kept key that is answered with a credential": {func(r *rig) {
			r.ok("", "profile", "init", "--url", b.url())
			plant(r)
		}, false},
	} {
		r := newRig(t, b.url())
		tc.prepare(r)
		got := r.exec(document, "team", "take-in")
		result, _ := decode(t, got.stdout)["result"].(map[string]any)
		if got.code != 0 || result["owner"] != tc.owner {
			t.Errorf("%s: exit %d, %v", name, got.code, result)
			continue
		}
		r.noRetryKey(name)
		if warnedToMakeTheTakeInAgain(got.stderr) || r.storedCredential() == "" {
			t.Errorf("%s: stderr says that the key stays, or the profile holds no credential", name)
		}
	}
	// A kept key on the profile whose credential owns the team: the answer
	// is the owner's, and needs no key.
	plant(alice)
	owner := alice.exec(document, "team", "take-in")
	result, _ = decode(t, owner.stdout)["result"].(map[string]any)
	if owner.code != 0 || result["owner"] != true || warnedToMakeTheTakeInAgain(owner.stderr) {
		t.Errorf("a kept key on the owner's profile: exit %d, %v", owner.code, result)
	}
	alice.noRetryKey("a kept key on the owner's profile")
	if last := b.seen(pathTeamTakeIn); last[len(last)-1].Body["retry_key"] != planted {
		t.Errorf("the owner's take-in did not carry the key its profile kept")
	}
}

// TestARetryKeyFileThatIsNoPrivateFileIsRefused: where the key file is to
// be there is a symbolic link, to a private file that holds a key for this
// server and this team or to nothing, or a directory. The strict reader
// refuses each as `config`, as it refuses a credential.json of that kind:
// no take-in is sent, the key a link points at is not read and not sent,
// and what is there stays as it is, the file a link points at included.
func TestARetryKeyFileThatIsNoPrivateFileIsRefused(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "")
	const planted = "cGxhbnRlZC1ieS10aGUtdGVzdC1uZXZlci1taW50ZWQ"
	content := `{"key":"` + planted + `","url":"` + b.url() + `","team_ref":"` + elsewhereRef + `"}`
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reason string
		plant        func(path string) error
		mode         os.FileMode
	}{
		{"a symbolic link to a private file that holds a key", "symlink", func(path string) error { return os.Symlink(target, path) }, os.ModeSymlink},
		{"a symbolic link to nothing", "symlink", func(path string) error { return os.Symlink(filepath.Join(filepath.Dir(target), "missing"), path) }, os.ModeSymlink},
		{"a directory", "not_regular", func(path string) error { return os.Mkdir(path, 0o700) }, os.ModeDir},
	} {
		r := newRig(t, b.url())
		r.ok("", "profile", "init", "--url", b.url())
		if err := tc.plant(r.retryKeyPath()); err != nil {
			t.Fatal(err)
		}
		refused := r.fails(protocol.CodeConfig, document, "team", "take-in")
		if got := details(t, refused.stdout)["reason"]; got != tc.reason {
			t.Errorf("%s in the key file's place: details.reason %v, want %s", tc.name, got, tc.reason)
		}
		if n := len(b.seen(pathTeamTakeIn)); n != 0 {
			t.Errorf("%s in the key file's place: a take-in was sent", tc.name)
		}
		if info, err := os.Lstat(r.retryKeyPath()); err != nil || info.Mode().Type() != tc.mode {
			t.Errorf("%s in the key file's place was replaced: %v (%v)", tc.name, info, err)
		}
		if strings.Contains(refused.stdout+refused.stderr, planted) {
			t.Errorf("%s in the key file's place: the key it leads to is on stdout or stderr", tc.name)
		}
		if r.storedCredential() != "" {
			t.Errorf("%s in the key file's place: the profile holds a credential", tc.name)
		}
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != content { //nolint:gosec // the test's own temp file
		t.Errorf("the file a link pointed at was changed: %q (%v)", data, err)
	}
	// Positive control: the same content as a private file of the profile's
	// own is the key that is sent.
	r := newRig(t, b.url())
	r.ok("", "profile", "init", "--url", b.url())
	if err := os.WriteFile(r.retryKeyPath(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if taken := r.ok(document, "team", "take-in"); taken["taken_in"] != true {
		t.Fatalf("a take-in with a key file of the profile's own: %v", taken)
	}
	if keys := sentRetryKeys(b); len(keys) != 1 || keys[0] != planted {
		t.Errorf("a private key file for the server and the team was not the key that was sent")
	}
}

// TestARetryKeyThatCannotBeRemovedIsSaid: the credential is kept and the
// key file cannot be removed. The take-in is done and succeeds, and stderr
// says to remove the file, by its name: the server honours the key until
// its owner first asks for something with the credential that the server
// records, so a file that stays is not left there in silence. The line
// holds nothing of the key.
func TestARetryKeyThatCannotBeRemovedIsSaid(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "")
	// While the server answers, the key file gives way to a directory that
	// holds a file: what os.Remove refuses, in a directory that still takes
	// the credential.
	var sent string
	b.loseAnswers(func(seen *seenRequest) loss {
		if seen.Path == pathTeamTakeIn {
			sent, _ = seen.Body["retry_key"].(string)
			if err := os.Remove(r.retryKeyPath()); err != nil {
				t.Errorf("the key file was not there when the request arrived: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(r.retryKeyPath(), "held"), 0o700); err != nil {
				t.Error(err)
			}
		}
		return answered
	})
	got := r.exec(document, "team", "take-in")
	b.loseAnswers(nil)
	if got.code != 0 || r.storedCredential() == "" {
		t.Fatalf("a take-in whose key file could not be removed: exit %d, %s", got.code, got.stdout)
	}
	if !strings.Contains(got.stderr, `"level":"WARN"`) || !strings.Contains(got.stderr, "could not be removed") ||
		!strings.Contains(got.stderr, retryKeyFileName) {
		t.Errorf("stderr does not say that %s could not be removed, and to remove it:\n%s", retryKeyFileName, got.stderr)
	}
	if !validRetryKey(sent) || strings.Contains(got.stdout+got.stderr, sent) {
		t.Errorf("the key is on stdout or stderr, or none was sent")
	}
	// Positive control: a take-in whose key file is removed says no such line.
	clean := newRig(t, b.url())
	if again := clean.exec(document, "team", "take-in"); again.code != 0 || strings.Contains(again.stderr, "could not be removed") {
		t.Errorf("a take-in whose key file was removed: exit %d, stderr %s", again.code, again.stderr)
	}
	clean.noRetryKey("after a take-in that was answered")
}

// TestATakeInThatCannotKeepItsKeySendsNothing: a profile directory that
// takes no file cannot keep the key, and could not keep the credential the
// answer would carry: the command fails before the request, so the server
// takes nothing in for a caller that could never own it.
func TestATakeInThatCannotKeepItsKeySendsNothing(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory whatever its mode")
	}
	b := newBackend(t)
	r := newRig(t, b.url())
	r.ok("", "profile", "init", "--url", b.url())
	dir := filepath.Dir(r.retryKeyPath())
	//nolint:gosec // G302: a DIRECTORY mode, restored below
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G302: restoring a DIRECTORY mode so the test's cleanup can remove it
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "")
	refused := r.fails(protocol.CodeInternal, document, "team", "take-in")
	if !strings.Contains(refused.stdout, "nothing was sent") {
		t.Errorf("the refusal does not say that nothing was sent: %s", refused.stdout)
	}
	if n := len(b.seen(pathTeamTakeIn)); n != 0 {
		t.Fatalf("a take-in was sent by a profile that could not keep its key")
	}
	// Positive control: the same profile, once its directory takes a file.
	//nolint:gosec // G302: a DIRECTORY mode
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if taken := r.ok(document, "team", "take-in"); taken["taken_in"] != true || taken["owner"] != true {
		t.Errorf("the take-in once the directory takes a file: %v", taken)
	}
}

// TestAServerThatRepeatsTheRetryKeyCannotMakeTheAdapterPrintIt: the key is
// registered on the redactor before it is sent, whether it was minted for
// this run or kept by an earlier one, so a server that broke the contract
// and repeated it in a refusal gets it on no stream.
func TestAServerThatRepeatsTheRetryKeyCannotMakeTheAdapterPrintIt(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	document := takeInDocument(secretOf(elsewhereRef, "kept-from-the-other-backend"), "ops", "")
	b.answerWith(func(seen *seenRequest) *canned {
		if seen.Path != pathTeamTakeIn {
			return nil
		}
		key, _ := seen.Body["retry_key"].(string)
		return &canned{status: http.StatusConflict, body: echoing("conflict", key)}
	})
	for _, run := range []string{"a key minted for this run", "a key an earlier run kept"} {
		got := r.fails(protocol.CodeConflict, document, "team", "take-in")
		key := r.keptRetryKey().Key
		if strings.Contains(got.stdout+got.stderr, key) {
			t.Errorf("%s: the key the server repeated reached stdout or stderr", run)
		}
		// Positive control: the refusal that was printed is the server's, with
		// a marker where the key stood.
		if d := details(t, got.stdout); d["reason"] != "echoed" || d["presented"] != "[redacted]" {
			t.Errorf("%s: the refusal's details are %v, want the server's with the key replaced", run, d)
		}
	}
	if keys := sentRetryKeys(b); len(keys) != 2 || keys[0] != keys[1] {
		t.Errorf("the second run did not send the key the first one kept")
	}
}
