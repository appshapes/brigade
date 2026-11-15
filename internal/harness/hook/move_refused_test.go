//go:build !windows

package hook

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// What a session says when the backend its team moved to does not take the
// membership this machine holds there, or does not answer (work item 341):
// at its start, and at the prompts after it.

// serverSays has the adapter of the server the team file names answer its
// `team moved` and its `team join` as says decides, a nil being the seam's
// own answer; every other call is the seam's. It counts the joins that
// server was asked for.
func serverSays(f *fixture, says func(verb string) *protocol.ErrorObject) *atomic.Int32 {
	f.t.Helper()
	inner, joins := f.deps.Spawn, &atomic.Int32{}
	server := teamstore.Key("rest", movedURL, "team-1")
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		verb := verbOf(spec.Argv)
		if i := slices.Index(spec.Argv, "--profile"); i < 0 || i+1 >= len(spec.Argv) || spec.Argv[i+1] != server || (verb != "team moved" && verb != "team join") {
			return inner(ctx, spec)
		}
		if verb == "team join" {
			joins.Add(1)
		}
		if refusal := says(verb); refusal != nil {
			return &adapterkit.SpawnResult{
				Envelope: &protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: refusal},
				ExitCode: refusal.Code.Exit(),
			}, nil
		}
		return inner(ctx, spec)
	}
	return joins
}

// memberOfTheServer is a member's machine whose team moved to the server,
// where the machine holds a membership already: the team file names the
// server, the backend the checkout joined records that move, and a follow a
// prompt starts runs before the prompt returns.
func memberOfTheServer(t *testing.T) *fixture {
	t.Helper()
	f := newMemberFixture(t)
	f.labelOldBinding()
	f.moveTeamFile("team-1")
	f.memberOf("team-1")
	f.useMoveSeam(movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"team join":        {joinOK()},
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
	})
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	return f
}

// TestAPromptSaysOfTheServerTheTeamMovedToWhatTheStartSaid: the backend the
// member joined recorded the move, this machine holds a membership on the
// server, and the server does not answer the follow's question, or answers
// that it does not know the credential. A start says which, in a line of
// its own. Until this the session's next prompt said that the team file
// does not match and to run `brigade team join`, whatever the start had
// said, and a server that answered a refusal was told as one that did not
// answer. The prompt says the start's line, a refusal is told as one, with
// the command that joins, and nothing is joined meanwhile. When the server
// answers again, the next follow finds the membership good and the prompt
// after it connects the session.
func TestAPromptSaysOfTheServerTheTeamMovedToWhatTheStartSaid(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		code protocol.Code
		line string
	}{
		"it does not answer":              {protocol.CodeUnavailable, newBackendSilentLine},
		"it does not know the credential": {protocol.CodeUnauthenticated, membershipRefusedLine},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := memberOfTheServer(t)
			var answers atomic.Bool
			joins := serverSays(f, func(verb string) *protocol.ErrorObject {
				if answers.Load() || verb != "team moved" {
					return nil
				}
				return &protocol.ErrorObject{Code: tc.code, Message: "scripted " + string(tc.code), Retryable: tc.code.Retryable()}
			})
			before, tf := f.checkoutPin(), f.movedFile()
			if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !slices.Equal(lines(out), []string{tc.line}) || f.mapExists() {
				t.Fatalf("the start: exit %d, lines %q", exit, lines(out))
			}
			if got := f.prompted(); !slices.Equal(got, []string{movedRetryLine, tc.line}) {
				t.Fatalf("the prompt after the start: %q", got)
			}
			f.now = f.now.Add(registerRetryInterval)
			if got := f.prompted(); !slices.Equal(got, []string{tc.line}) {
				t.Fatalf("the prompt a minute later: %q", got)
			}
			if pin := f.checkoutPin(); pin != before || joins.Load() != 0 {
				t.Fatalf("the pin moved (%+v) or the server was asked for %d joins", pin, joins.Load())
			}
			// The server answers again, and honours the membership.
			answers.Store(true)
			f.now = f.now.Add(registerRetryInterval)
			f.prompted()
			if pin := f.checkoutPin(); !pin.Matches(tf) || joins.Load() != 0 {
				t.Fatalf("the follow after the server answered: pin %+v, %d joins", pin, joins.Load())
			}
			// The follow ran beside the prompt that started it, which ended as
			// one that registered nothing: the registration is due a minute on.
			f.now = f.now.Add(registerRetryInterval)
			got := f.prompted()
			if len(got) < 2 || got[0] != followedLine(tf, &before, false, "brigade") || !strings.Contains(got[1], "(brigade-sess-1)") {
				t.Fatalf("the prompt after the follow: %q", got)
			}
		})
	}
}

// TestTheLineOfAServerThatRefusesNamesTheJoinThatHelps: the line a session
// says when the server refuses the membership this machine holds names the
// one command that helps, with the flag a session needs, and echoes
// nothing of the team file.
func TestTheLineOfAServerThatRefusesNamesTheJoinThatHelps(t *testing.T) {
	t.Parallel()
	if !strings.Contains(membershipRefusedLine, "`brigade team join --secret-file <path>`") || !strings.Contains(membershipRefusedLine, "current join secret") ||
		strings.Contains(membershipRefusedLine, "did not answer") || strings.Contains(membershipRefusedLine, "does not match") {
		t.Fatalf("the line: %q", membershipRefusedLine)
	}
	if notFollowedLine("new_backend_refused") != membershipRefusedLine || notFollowedLine("new_backend_failed") != newBackendSilentLine || notFollowedLine("join_refused") != driftLine {
		t.Fatalf("the lines of a follow that did not follow are not the ones their reasons have")
	}
}
