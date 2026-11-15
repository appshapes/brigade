//go:build !windows

package rest

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// The secret half of a join secret and of a move ticket: what follows the
// last dot. The redactor's pattern knows the whole `brg1.<team_ref>.<secret>`
// and nothing of the half alone, which is the one part of it that is
// secret, so the adapter registers the half (hideSecretHalf).

// echoing is a refusal a server that breaks the contract answers: its
// message, a value of its details and a name of its details each repeat
// text, the secret half of what the request presented and nothing more of
// it.
func echoing(code, text string) string {
	return `{"ok":false,"protocol_version":"1","error":{"code":"` + code + `","message":"nothing here for ` + text +
		`","retryable":false,"details":{"reason":"echoed","presented":"` + text + `","` + text + `":"a name"}}}`
}

// TestAServerThatRepeatsOnlyTheSecretHalfCannotMakeTheAdapterPrintIt: a
// server that repeats the secret half alone, in the message of its refusal
// and in its details, gets it on no stream of `team take-in` and of `team
// join`, whether what the command presented was the team's join secret or
// the ticket of its move. The refusal arrives all the same, with a marker
// where the half was.
func TestAServerThatRepeatsOnlyTheSecretHalfCannotMakeTheAdapterPrintIt(t *testing.T) {
	t.Parallel()
	const secretHalf, ticketHalf = "kept-from-the-other-backend", "0123456789abcdef0123456789abcdef"
	for name, tc := range map[string]struct {
		path, half, input string
		args              []string
		code              protocol.Code
		status            int
	}{
		"team take-in, the join secret": {
			pathTeamTakeIn, secretHalf, takeInDocument(secretOf(elsewhereRef, secretHalf), "ops", ""),
			[]string{"team", "take-in"}, protocol.CodeConflict, http.StatusConflict,
		},
		"team take-in, a move ticket": {
			pathTeamTakeIn, ticketHalf, takeInDocument(secretOf(elsewhereRef, ticketHalf), "ops", ""),
			[]string{"team", "take-in"}, protocol.CodeConflict, http.StatusConflict,
		},
		"team join, the join secret": {
			pathTeamJoin, secretHalf, `{"join_secret":"` + secretOf(elsewhereRef, secretHalf) + `"}`,
			[]string{"team", "join"}, protocol.CodeUnauthorized, http.StatusForbidden,
		},
		"team join, a move ticket": {
			pathTeamJoin, ticketHalf, `{"join_secret":"` + secretOf(elsewhereRef, ticketHalf) + `"}`,
			[]string{"team", "join"}, protocol.CodeUnauthorized, http.StatusForbidden,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBackend(t)
			r := newRig(t, b.url())
			b.answerWith(only(tc.path, tc.status, echoing(string(tc.code), tc.half)))
			got := r.fails(tc.code, tc.input, tc.args...)
			if len(b.seen(tc.path)) != 1 {
				t.Fatalf("the request did not reach the server that repeats it")
			}
			if strings.Contains(got.stdout+got.stderr, tc.half) {
				t.Errorf("the secret half the server repeated reached stdout or stderr:\n%s%s", got.stdout, got.stderr)
			}
			// Positive control: the server's refusal is what was printed, the
			// half replaced where it stood, in the message, in a value of the
			// details and in a name of them, and on stderr's line as well.
			object, _ := decode(t, got.stdout)["error"].(map[string]any)
			if object["message"] != "nothing here for "+adapterlog.Redacted {
				t.Errorf("the refusal's message is %q, want the server's with the half replaced", object["message"])
			}
			d := details(t, got.stdout)
			if d["reason"] != "echoed" || d["presented"] != adapterlog.Redacted || d[adapterlog.Redacted] != "a name" {
				t.Errorf("the refusal's details are %v, want the server's with the half replaced", d)
			}
			if !strings.Contains(got.stderr, "nothing here for "+adapterlog.Redacted) {
				t.Errorf("stderr does not carry the refusal with the half replaced: %q", got.stderr)
			}
		})
	}
}

// TestTheSecretHalfOfAnAnswerIsHidden: `team rotate-secret` and `team
// open-move` learn their secret from the server's answer, and register its
// half before they do anything else with it. Nothing the server chose is
// written after that answer but the result, so no stream can show the
// registration: the test hands the command its redactor and asks it
// afterwards. The secret a `team take-in` and a `team join` present is
// asked about in the same way.
func TestTheSecretHalfOfAnAnswerIsHidden(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	alice := newRig(t, b.url())
	secret := secretOf(elsewhereRef, "kept-from-the-other-backend")
	// with runs one command of alice's with a redactor of the test's own.
	with := func(input string, args ...string) *adapterlog.Redactor {
		t.Helper()
		redactor := adapterlog.NewRedactor()
		var stdout, stderr strings.Builder
		if code := run(args, strings.NewReader(input), &stdout, &stderr, alice.env, options{redactor: redactor}); code != 0 {
			t.Fatalf("%v: exit %d (%s %s)", args, code, stdout.String(), stderr.String())
		}
		return redactor
	}
	hides := func(what string, redactor *adapterlog.Redactor, text string) {
		t.Helper()
		if got := redactor.Redact("x " + half(text) + " y"); got != "x "+adapterlog.Redacted+" y" {
			t.Errorf("%s: its secret half is not registered on the command's redactor", what)
		}
	}
	// Positive control: a redactor that was told nothing replaces no half.
	if got := adapterlog.NewRedactor().Redact(half(secret)); got != half(secret) {
		t.Fatalf("a half is replaced by a redactor nothing was registered on: %q", got)
	}
	hides("team take-in", with(takeInDocument(secret, "ops", ""), "team", "take-in"), secret)

	rotated := filepath.Join(t.TempDir(), "rotated.secret")
	afterRotation := with("", "team", "rotate-secret", "--secret-file", rotated)
	hides("team rotate-secret", afterRotation, secretIn(t, rotated))

	ticket := filepath.Join(t.TempDir(), "ops.ticket")
	afterOpening := with("", "team", "open-move", "--ticket-file", ticket)
	hides("team open-move", afterOpening, secretIn(t, ticket))
	// Each command hides what it learned itself, and nothing of another's.
	if got := afterRotation.Redact(half(secretIn(t, ticket))); got == adapterlog.Redacted {
		t.Errorf("the rotation's redactor hides a ticket it never saw")
	}

	bob := newRig(t, b.url())
	redactor := adapterlog.NewRedactor()
	var stdout, stderr strings.Builder
	joining := `{"join_secret":"` + secretIn(t, ticket) + `"}`
	if code := run([]string{"team", "join"}, strings.NewReader(joining), &stdout, &stderr, bob.env, options{redactor: redactor}); code != 0 {
		t.Fatalf("team join with the ticket: exit %d (%s %s)", code, stdout.String(), stderr.String())
	}
	hides("team join", redactor, secretIn(t, ticket))
}

// TestTheSecretHalfThatIsHidden: hideSecretHalf registers what follows the
// last dot of a join secret or a ticket when it is sixteen characters or
// longer, and nothing else: not a shorter half, which would be cut out of
// every line that happens to hold those characters, not the team reference,
// which is public, and nothing of a text with no dot.
func TestTheSecretHalfThatIsHidden(t *testing.T) {
	t.Parallel()
	hidden := func(secret string) *adapterlog.Redactor {
		c := &command{redactor: adapterlog.NewRedactor()}
		c.hideSecretHalf(secret)
		return c.redactor
	}
	const sixteen, fifteen = "0123456789abcdef", "0123456789abcde"
	if got := hidden(secretOf(elsewhereRef, sixteen)).Redact("a line with " + sixteen + " in it"); got != "a line with "+adapterlog.Redacted+" in it" {
		t.Errorf("a half of sixteen characters: %q, want it replaced", got)
	}
	if got := hidden(secretOf(elsewhereRef, fifteen)).Redact("a line with " + fifteen + " in it"); got != "a line with "+fifteen+" in it" {
		t.Errorf("a half of fifteen characters: %q, want the line as it was", got)
	}
	// The reference is in the committed team file: a line names it freely.
	if got := hidden(secretOf(elsewhereRef, sixteen)).Redact("team " + elsewhereRef); got != "team "+elsewhereRef {
		t.Errorf("the team reference was hidden with the half: %q", got)
	}
	// A reference with dots in it: the half is what follows the last one.
	dotted := hidden(secretOf("team.with.dots-0123456789abcdef", sixteen+"-the-half"))
	if got := dotted.Redact("dots-0123456789abcdef and " + sixteen + "-the-half"); got != "dots-0123456789abcdef and "+adapterlog.Redacted {
		t.Errorf("a reference with dots: %q, want the half alone replaced", got)
	}
	if got := hidden("no-dot-in-this-text-at-all").Redact("no-dot-in-this-text-at-all"); got != "no-dot-in-this-text-at-all" {
		t.Errorf("a text with no dot registered something: %q", got)
	}
}
