//go:build !windows

package hook

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teammove"
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

// TestAFirstJoinThatWasNotAnsweredIsToldAsAServerThatDidNotAnswer (work item
// 341): the member never held a membership on the server the team moved to,
// so the first thing a follow asks of it is the join itself, and the server
// does not answer it. Until this every failure of that join was a join the
// server refused, and the start said that the team file does not match and
// to run `brigade team join`, of a server that was only down. The start
// says that the server did not answer, the prompts after it say the same,
// every follow presents the ticket again because nothing was refused, and
// the session connects once the server answers.
func TestAFirstJoinThatWasNotAnsweredIsToldAsAServerThatDidNotAnswer(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.labelOldBinding()
	f.moveTeamFile("team-1")
	f.useMoveSeam(movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"team join":        {joinOK()},
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
	})
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	var answers atomic.Bool
	joins := serverSays(f, func(verb string) *protocol.ErrorObject {
		if answers.Load() || verb != "team join" {
			return nil
		}
		return &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "the backend could not be reached", Retryable: true}
	})
	before, tf := f.checkoutPin(), f.movedFile()
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !slices.Equal(lines(out), []string{newBackendSilentLine}) || f.mapExists() {
		t.Fatalf("the start: exit %d, lines %q", exit, lines(out))
	}
	if got := f.prompted(); !slices.Equal(got, []string{movedRetryLine, newBackendSilentLine}) {
		t.Fatalf("the prompt after the start: %q", got)
	}
	f.now = f.now.Add(registerRetryInterval)
	if got := f.prompted(); !slices.Equal(got, []string{newBackendSilentLine}) {
		t.Fatalf("the prompt a minute later: %q", got)
	}
	// The start's follow and the two that the prompts started each presented
	// the ticket: a join that was not answered is no refusal, and pauses nothing.
	if pin := f.checkoutPin(); pin != before || joins.Load() != 3 {
		t.Fatalf("the pin moved (%+v), or the server was asked for %d joins, want three", pin, joins.Load())
	}
	if _, err := teamstore.LoadRejoinRefusal(f.configDir, teamstore.Key("rest", movedURL, "team-1")); err == nil {
		t.Fatalf("a join that was not answered is on record as one the server refused")
	}
	// The server answers: the next follow joins, and the prompt after it
	// connects the session.
	answers.Store(true)
	f.now = f.now.Add(registerRetryInterval)
	f.prompted()
	if pin := f.checkoutPin(); !pin.Matches(tf) || joins.Load() != 4 {
		t.Fatalf("the follow after the server answered: pin %+v, %d joins", pin, joins.Load())
	}
	f.now = f.now.Add(registerRetryInterval)
	got := f.prompted()
	if len(got) < 2 || got[0] != followedLine(tf, &before, true, "brigade") || !strings.Contains(got[1], "(brigade-sess-1)") {
		t.Fatalf("the prompt after the follow: %q", got)
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

// refusing has the server refuse the membership this machine holds, and
// the join made under it for as long as banned is true; a join that is let
// in ends the refusals. It counts the joins the server was asked for.
func refusing(f *fixture, banned *atomic.Bool) *atomic.Int32 {
	f.t.Helper()
	var in atomic.Bool
	return serverSays(f, func(verb string) *protocol.ErrorObject {
		switch {
		case in.Load():
		case verb == "team moved":
			return &protocol.ErrorObject{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
		case banned.Load():
			return &protocol.ErrorObject{Code: protocol.CodeUnauthorized, Message: "join secret rejected"}
		default:
			in.Store(true)
		}
		return nil
	})
}

// TestARefusedRejoinIsAskedForOnceAndToldOnce: the server the team moved to
// does not honour the membership this machine holds and refuses the join
// made with the ticket under it, as it refuses a member who is banned and
// one whose ticket no longer admits. A server counts the joins a principal
// fails. Until this every start and every minute of prompts asked for the
// join again, and each start said the same thing.
//
// Five starts and three prompts a minute apart ask the server for one join
// in all. The first start says that the server refuses the membership and
// which command joins; the others say what a team file nobody followed is
// told. The refusal is lifted: nothing is asked inside the fifteen minutes,
// and the first start after them asks once more and follows.
func TestARefusedRejoinIsAskedForOnceAndToldOnce(t *testing.T) {
	t.Parallel()
	f := memberOfTheServer(t)
	var banned atomic.Bool
	banned.Store(true)
	joins := refusing(f, &banned)
	before, tf, began := f.checkoutPin(), f.movedFile(), f.now
	start := func() []string {
		t.Helper()
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
		if exit != 0 {
			t.Fatalf("session-start: exit %d: %s", exit, errOut)
		}
		return lines(out)
	}
	if got := start(); !slices.Equal(got, []string{membershipRefusedLine}) || joins.Load() != 1 || f.mapExists() {
		t.Fatalf("the start that is refused: lines %q, %d joins", got, joins.Load())
	}
	for i := range 4 {
		if got := start(); !slices.Equal(got, []string{driftLine}) || joins.Load() != 1 {
			t.Fatalf("start %d after the refusal: lines %q, %d joins", i+2, got, joins.Load())
		}
	}
	for i, want := range [][]string{{movedRetryLine, driftLine}, {driftLine}, {driftLine}} {
		if got := f.prompted(); !slices.Equal(got, want) || len(f.spawner.followed()) != i+1 || joins.Load() != 1 {
			t.Fatalf("prompt %d after the refusal: lines %q, %d follows, %d joins", i+1, got, len(f.spawner.followed()), joins.Load())
		}
		f.now = f.now.Add(registerRetryInterval)
	}
	if pin := f.checkoutPin(); pin != before {
		t.Fatalf("the pin moved: %+v", pin)
	}
	// The ban is lifted. Inside the fifteen minutes no join is asked for.
	banned.Store(false)
	f.now = began.Add(teammove.RejoinPause - time.Second)
	if got := start(); !slices.Equal(got, []string{driftLine}) || joins.Load() != 1 {
		t.Fatalf("a start inside the fifteen minutes: lines %q, %d joins", got, joins.Load())
	}
	// After them the next start asks once more, and the member is back.
	f.now = began.Add(teammove.RejoinPause)
	got := start()
	if len(got) < 2 || got[0] != followedLine(tf, &before, true, "brigade") || joins.Load() != 2 || !f.mapExists() {
		t.Fatalf("the first start after the fifteen minutes: lines %q, %d joins", got, joins.Load())
	}
	if _, err := teamstore.LoadRejoinRefusal(f.configDir, teamstore.Key("rest", movedURL, "team-1")); err == nil {
		t.Fatalf("the record of the refusal is still there after the member was let in")
	}
}

// TestARunningSessionIsToldOnceThatTheServerRefusesTheMembership: a session
// that is running on the backend the team left, whose checkout comes to
// name the server. The follow its prompt starts is refused there. The
// session stays where it is, its next prompt says so once, with the
// command that joins, and the prompts after it say nothing and ask the
// server for no join.
func TestARunningSessionIsToldOnceThatTheServerRefusesTheMembership(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
	f.memberOf("team-1")
	var banned atomic.Bool
	banned.Store(true)
	joins := refusing(f, &banned)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	f.moveTeamFile("team-1")
	for i, want := range [][]string{{movedLine}, {movedRefusedLine}, nil, nil} {
		if got := f.prompted(); !slices.Equal(got, want) || joins.Load() != 1 {
			t.Fatalf("prompt %d: lines %q, %d joins; want %q and one join", i+1, got, joins.Load(), want)
		}
		f.now = f.now.Add(followInterval)
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || m.BrigadeSessionID != "brigade-sess-1" {
		t.Fatalf("the session moved: %+v", m)
	}
	if !strings.Contains(movedRefusedLine, "`brigade team join --secret-file <path>`") || !strings.Contains(movedRefusedLine, "stays where it is") {
		t.Fatalf("the line: %q", movedRefusedLine)
	}
}

// TestAFollowThatLearnedNothingLeavesWhatTheLastOneLeft (work item 341):
// the last follow of the checkout found the server the team moved to
// silent, and left that for the checkout's prompts to say. The next follow
// does not get as far as that server: the backend the member joined does
// not answer either. It learned nothing about the server, and until this it
// removed what the last one had left, so the prompt after it said that the
// team file does not match and to run `brigade team join`, of a server that
// is only down. A follow that could not ask leaves every mark as it is.
func TestAFollowThatLearnedNothingLeavesWhatTheLastOneLeft(t *testing.T) {
	t.Parallel()
	f := memberOfTheServer(t)
	silent := func(string) *protocol.ErrorObject {
		return &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "the backend could not be reached", Retryable: true}
	}
	serverSays(f, func(verb string) *protocol.ErrorObject {
		if verb != "team moved" {
			return nil
		}
		return silent(verb)
	})
	before := f.checkoutPin()
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !slices.Equal(lines(out), []string{newBackendSilentLine}) {
		t.Fatalf("the start: exit %d, lines %q", exit, lines(out))
	}
	// From here the backend the member joined does not answer either.
	inner := f.deps.Spawn
	var asked atomic.Int32
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if i := slices.Index(spec.Argv, "--profile"); i >= 0 && i+1 < len(spec.Argv) && spec.Argv[i+1] == f.teamKey && verbOf(spec.Argv) == "team moved" {
			asked.Add(1)
			refusal := silent("")
			return &adapterkit.SpawnResult{
				Envelope: &protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: refusal},
				ExitCode: refusal.Code.Exit(),
			}, nil
		}
		return inner(ctx, spec)
	}
	if got := f.prompted(); !slices.Equal(got, []string{movedRetryLine, newBackendSilentLine}) {
		t.Fatalf("the prompt after the start: %q", got)
	}
	if asked.Load() == 0 {
		t.Fatalf("the follow of that prompt did not ask the backend the member joined: the test holds nothing")
	}
	f.now = f.now.Add(registerRetryInterval)
	if got := f.prompted(); !slices.Equal(got, []string{newBackendSilentLine}) {
		t.Fatalf("the prompt after a follow that learned nothing: %q, want what the last follow that reached the server left", got)
	}
	if pin := f.checkoutPin(); pin != before {
		t.Fatalf("the pin moved: %+v", pin)
	}
}

// joinedBackendSilent has the backend the member joined stop answering
// what it records of the team, from here on: a follow then cannot ask
// whether the team moved, and learns nothing about the server the team file
// names. It counts the questions that went unanswered.
func joinedBackendSilent(f *fixture) *atomic.Int32 {
	f.t.Helper()
	inner, asked := f.deps.Spawn, &atomic.Int32{}
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if i := slices.Index(spec.Argv, "--profile"); i < 0 || i+1 >= len(spec.Argv) || spec.Argv[i+1] != f.teamKey || verbOf(spec.Argv) != "team moved" {
			return inner(ctx, spec)
		}
		asked.Add(1)
		silent := &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "the backend could not be reached", Retryable: true}
		return &adapterkit.SpawnResult{
			Envelope: &protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: silent},
			ExitCode: silent.Code.Exit(),
		}, nil
	}
	return asked
}

// TestAStartThatCouldNotAskSaysWhatTheLastFollowLeft (work item 341): the
// last follow of the checkout reached the server the team moved to and left
// what it found for the checkout's prompts: the server did not answer, it
// refuses the membership this machine holds, or it and the backend the
// member joined each record that the team moved to the other. A session
// then starts while the backend the member joined does not answer, so its
// own follow cannot ask and learns nothing. Until this the start said that
// the team file does not match and to run `brigade team join`, while the
// mark stayed and the next prompt said the mark's line. A start whose
// follow learned nothing says what the mark says, as a prompt does, and
// leaves the mark as it was.
func TestAStartThatCouldNotAskSaysWhatTheLastFollowLeft(t *testing.T) {
	t.Parallel()
	refusal := func(code protocol.Code) func(*fixture) *adapterkit.SpawnResult {
		return func(*fixture) *adapterkit.SpawnResult {
			said := &protocol.ErrorObject{Code: code, Message: "scripted " + string(code), Retryable: code.Retryable()}
			return &adapterkit.SpawnResult{Envelope: &protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: said}, ExitCode: code.Exit()}
		}
	}
	for name, tc := range map[string]struct {
		// answer is what the server answers the follow's question.
		answer func(f *fixture) *adapterkit.SpawnResult
		line   string
	}{
		"the server did not answer":         {refusal(protocol.CodeUnavailable), newBackendSilentLine},
		"the server refuses the membership": {refusal(protocol.CodeUnauthenticated), membershipRefusedLine},
		"the two name each other": {func(f *fixture) *adapterkit.SpawnResult {
			pin := f.checkoutPin()
			return okResult(mustJSON(map[string]any{"team_ref": "team-1", "moved": true, "adapter": pin.Adapter, "url": pin.URL, "recorded_at": "2026-11-15T12:00:00Z"}))
		}, eachOtherLine},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := memberOfTheServer(t)
			inner, server := f.deps.Spawn, teamstore.Key("rest", movedURL, "team-1")
			f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
				if i := slices.Index(spec.Argv, "--profile"); i >= 0 && i+1 < len(spec.Argv) && spec.Argv[i+1] == server && verbOf(spec.Argv) == "team moved" {
					return tc.answer(f), nil
				}
				return inner(ctx, spec)
			}
			before := f.checkoutPin()
			start := func() []string {
				t.Helper()
				exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
				if exit != 0 || f.mapExists() {
					t.Fatalf("session-start: exit %d, a map is there: %v: %s", exit, f.mapExists(), errOut)
				}
				return lines(out)
			}
			if got := start(); !slices.Equal(got, []string{tc.line}) {
				t.Fatalf("the start that reached the server: %q", got)
			}
			mark := config.MoveNotFollowedMark(f.stateDir, f.configDir, f.canon())
			left, err := os.ReadFile(mark) //nolint:gosec // the fixture's own state directory
			if err != nil {
				t.Fatalf("the start left no mark for the checkout's prompts: %v", err)
			}
			// From here the backend the member joined does not answer.
			asked := joinedBackendSilent(f)
			if got := start(); !slices.Equal(got, []string{tc.line}) {
				t.Fatalf("a start whose follow could not ask: %q, want what the last follow that reached the server left", got)
			}
			if asked.Load() != 1 {
				t.Fatalf("that start asked the backend the member joined %d times: the test holds nothing", asked.Load())
			}
			if now, err := os.ReadFile(mark); err != nil || string(now) != string(left) { //nolint:gosec // as above
				t.Fatalf("the mark after a start that learned nothing: %q, %v; it was %q", now, err, left)
			}
			if pin := f.checkoutPin(); pin != before {
				t.Fatalf("the pin moved: %+v", pin)
			}
		})
	}
}

// TestAStartThatCouldNotAskTellsOfARefusalNobodyWasTold (work item 341): a
// follow that ran beside a prompt found that the server refuses the
// membership this machine holds and the join made under it, and left that
// on record with nobody to tell. A session then starts while the backend
// the member joined does not answer. Its follow learned nothing, and it
// says what a prompt would say in its place: that the server refuses the
// membership, with the command that joins, once. The start after it says
// what a team file nobody followed is told.
func TestAStartThatCouldNotAskTellsOfARefusalNobodyWasTold(t *testing.T) {
	t.Parallel()
	f := memberOfTheServer(t)
	var banned atomic.Bool
	banned.Store(true)
	joins := refusing(f, &banned)
	// The prompt reads what there is to say before it starts the follow, and
	// the follow is the one that is refused: nobody has been told.
	if got := f.prompted(); !slices.Equal(got, []string{movedRetryLine, driftLine}) || joins.Load() != 1 {
		t.Fatalf("the prompt whose follow was refused: lines %q, %d joins", got, joins.Load())
	}
	if _, err := teamstore.LoadRejoinRefusal(f.configDir, teamstore.Key("rest", movedURL, "team-1")); err != nil {
		t.Fatalf("the refused join left no record: %v", err)
	}
	joinedBackendSilent(f)
	for i, want := range []string{membershipRefusedLine, driftLine} {
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
		if exit != 0 || !slices.Equal(lines(out), []string{want}) || joins.Load() != 1 {
			t.Fatalf("start %d while the backend the member joined does not answer: exit %d, lines %q, %d joins; want %q\n%s", i+1, exit, lines(out), joins.Load(), want, errOut)
		}
	}
}
