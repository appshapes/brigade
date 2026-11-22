//go:build !windows

package e2e

import (
	"strconv"
	"testing"
	"time"
)

// twoIdenticalReplies is work item 392's check against a real backend,
// through the built binary: one session answers two messages of one
// sender with the same short body inside a minute, and the backend keeps
// both; the same body in reply to the same message again is still the
// first message. send runs `brigade send` in the answering session with
// the body on stdin. It returns the two answers and the body they carry.
//
// The key of a send holds the minute it was made in, so three sends that
// straddle the turn of a minute prove nothing about the third: they are
// made again, under a body of their own, until all three fall in one,
// and nothing is asserted of an attempt that did not.
func twoIdenticalReplies(t *testing.T, send func(body string, args ...string) sendResult, to, first, second string) (a, b sendResult, body string) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		body = "Done."
		if attempt > 1 {
			body = "Done (" + strconv.Itoa(attempt) + ")."
		}
		minute := time.Now().Unix() / 60
		a = send(body, to, "--reply-to", first, "--json")
		b = send(body, to, "--reply-to", second, "--json")
		again := send(body, to, "--reply-to", first, "--json")
		if time.Now().Unix()/60 != minute {
			if attempt == 5 {
				t.Fatalf("five attempts at three sends inside one minute each straddled the turn of a minute")
			}
			continue
		}
		if a.Duplicate || b.Duplicate || a.MessageID == "" || a.MessageID == b.MessageID {
			t.Fatalf("one body in reply to two messages inside a minute: %+v and %+v, want two messages", a, b)
		}
		if !again.Duplicate || again.MessageID != a.MessageID {
			t.Fatalf("the same body in reply to the same message again: %+v, want the first message %s once more", again, a.MessageID)
		}
		return a, b, body
	}
}

// TestTwoIdenticalRepliesInAMinuteAreBothStored (work item 392, ADR 0013):
// the key `brigade send` gives a message left out the message it answers,
// so the second of two identical short answers of a minute, a "Done." to
// each of two questions, was never stored: it carried the first one's
// key, which the fs adapter and a Brigade server refused as `conflict`,
// `idempotency_key_reused`, and a Supabase project answered as the first
// one's duplicate. Here, on the fs adapter: carol asks bob two questions,
// bob answers each with the same body, and carol's inbox holds both
// answers, each with the question it answers.
func TestTwoIdenticalRepliesInAMinuteAreBothStored(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	carol := r.newSession("alice", "", aliceName, "", "")
	bob := r.newSession("bob", bobName, "", "", "")
	// Socketless sessions: no watcher takes a message out of an inbox.
	r.hook(carol, "session-start", carol.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	r.hook(bob, "session-start", bob.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	carolID, bobID := r.mustMap(carol).BrigadeSessionID, r.mustMap(bob).BrigadeSessionID

	ask := func(question string) string {
		t.Helper()
		return jsonResult[sendResult](t, r.mustRun(r.env(carol), question, "send", bobID, "--json").stdout).MessageID
	}
	first, second := ask("Is the migration in?"), ask("Is the index built?")
	answer := func(body string, args ...string) sendResult {
		t.Helper()
		return jsonResult[sendResult](t, r.mustRun(r.env(bob), body, append([]string{"send"}, args...)...).stdout)
	}
	a, b, body := twoIdenticalReplies(t, answer, carolID, first, second)

	answered := map[string]string{}
	for _, m := range r.storeMessages("inbox", carolID) {
		if m.Body != body {
			continue
		}
		if m.ReplyTo == nil {
			t.Fatalf("an answer in carol's inbox answers nothing: %+v", m)
		}
		answered[m.MessageID] = *m.ReplyTo
	}
	if len(answered) != 2 || answered[a.MessageID] != first || answered[b.MessageID] != second {
		t.Fatalf("carol's inbox holds the answers %v, want %s to %s and %s to %s", answered, a.MessageID, first, b.MessageID, second)
	}

	// The summary is part of the key as the reply is: the same body to
	// the same session with two summaries is two messages.
	one := answer(body, carolID, "--summary", "the migration", "--json")
	two := answer(body, carolID, "--summary", "the index", "--json")
	if one.Duplicate || two.Duplicate || one.MessageID == two.MessageID {
		t.Fatalf("one body under two summaries: %+v and %+v, want two messages", one, two)
	}
}
