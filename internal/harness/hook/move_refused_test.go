//go:build !windows

package hook

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
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
	return joinedBackendSays(f, &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "the backend could not be reached", Retryable: true})
}

// joinedBackendSays has the backend the member joined answer what it
// records of the team with said, from here on, and counts the questions it
// answered so.
func joinedBackendSays(f *fixture, said *protocol.ErrorObject) *atomic.Int32 {
	f.t.Helper()
	inner, asked := f.deps.Spawn, &atomic.Int32{}
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if i := slices.Index(spec.Argv, "--profile"); i < 0 || i+1 >= len(spec.Argv) || spec.Argv[i+1] != f.teamKey || verbOf(spec.Argv) != "team moved" {
			return inner(ctx, spec)
		}
		asked.Add(1)
		return &adapterkit.SpawnResult{
			Envelope: &protocol.Envelope{OK: false, ProtocolVersion: protocol.ProtocolVersion, Error: said},
			ExitCode: said.Code.Exit(),
		}, nil
	}
	return asked
}

// TestAMemberTheBackendHeJoinedRefusesIsNotToldToWait (work item 341): the
// member's first follow found the server the team moved to silent, and left
// that for the checkout's prompts. The server answers again, and the
// administrator puts the member out of the backend he joined, as the last
// step of a move has it for a member who is thought to have followed. From
// then on that backend refuses the follow's question. A refusal is an
// answer, not a backend that did not answer: until this the follow counted
// as one that had learned nothing, the mark stayed, and every start and
// prompt said that the server did not answer and that a session tries
// again, of a member whom only his own join lets in. The mark is taken
// back, and the start and the prompts say what a team file nobody vouched
// for is told, which names that join.
func TestAMemberTheBackendHeJoinedRefusesIsNotToldToWait(t *testing.T) {
	t.Parallel()
	f := memberOfTheServer(t)
	var down atomic.Bool
	down.Store(true)
	joins := serverSays(f, func(verb string) *protocol.ErrorObject {
		if !down.Load() || verb != "team moved" {
			return nil
		}
		return &protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "the backend could not be reached", Retryable: true}
	})
	before, mark := f.checkoutPin(), config.MoveNotFollowedMark(f.stateDir, f.configDir, f.canon())
	start := func() []string {
		t.Helper()
		exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
		if exit != 0 || f.mapExists() {
			t.Fatalf("session-start: exit %d, a map is there: %v: %s", exit, f.mapExists(), errOut)
		}
		return lines(out)
	}
	if got := start(); !slices.Equal(got, []string{newBackendSilentLine}) || !exists(mark) {
		t.Fatalf("the start while the server does not answer: %q; a mark is left: %v", got, exists(mark))
	}
	// The server answers again, and the member is put out of the backend he joined.
	down.Store(false)
	asked := joinedBackendSays(f, &protocol.ErrorObject{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"})
	if got := start(); !slices.Equal(got, []string{driftLine}) || exists(mark) || asked.Load() != 1 {
		t.Fatalf("the start of a member the backend he joined refuses: %q; the mark is still there: %v; that backend was asked %d times", got, exists(mark), asked.Load())
	}
	for i, want := range [][]string{{movedRetryLine, driftLine}, {driftLine}} {
		if got := f.prompted(); !slices.Equal(got, want) || exists(mark) {
			t.Fatalf("prompt %d of that member's session: %q, want %q; the mark is there: %v", i+1, got, want, exists(mark))
		}
		f.now = f.now.Add(registerRetryInterval)
	}
	if pin := f.checkoutPin(); pin != before || joins.Load() != 0 {
		t.Fatalf("the pin moved (%+v), or the server was asked for %d joins", pin, joins.Load())
	}
}

// TestOnlyAFollowThatCouldNotAskLearnedNothing (work item 341): a follow
// learned nothing about the backend the team file names when it did not get
// the lock, or when the backend the member joined did not answer it. A
// refusal from that backend is an answer, and so is every outcome that
// comes after its answer, a store that failed among them: each takes back
// what an earlier follow left.
func TestOnlyAFollowThatCouldNotAskLearnedNothing(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]bool{
		teammove.ReasonBusy: true, teammove.ReasonOldBackendFailed: true,
		teammove.ReasonOldBackendRefused: false, teammove.ReasonStore: false,
		teammove.ReasonNewBackendFailed: false, teammove.ReasonNewBackendRefused: false, teammove.ReasonEachOther: false,
		teammove.ReasonRejoinRefused: false, teammove.ReasonJoinRefused: false, teammove.ReasonNoRecord: false,
		teammove.ReasonRecordElsewhere: false, teammove.ReasonLeft: false, "": false,
	} {
		if got := learnedNothing(reason); got != want {
			t.Errorf("learnedNothing(%q) = %v, want %v", reason, got, want)
		}
	}
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

// TestAPromptTakesBackWhatAFollowLeftOfABackendTheFileNoLongerNames (work
// item 341): a follow that a running session's prompt started found the
// server the team file names and the backend the session is on naming each
// other, and left that for the checkout's prompts. The file then goes back
// to naming the backend the session is on, and no session starts in the
// checkout, which is where such a mark was taken back. Until this the mark
// stayed for as long as only running sessions were prompted there, and the
// next time the file named that server, after the record that was out of
// date was cleared and the team really moved, a running session said that
// the two name each other and that it follows neither, of records that no
// longer did. The prompt of a session whose checkout's file names the
// backend the session is on takes the mark back, as a start does, so the
// prompt at the real move says nothing of the two and the session follows.
// A session whose map names no checkout yet does the same at the prompt
// that gives it one.
func TestAPromptTakesBackWhatAFollowLeftOfABackendTheFileNoLongerNames(t *testing.T) {
	t.Parallel()
	for name, old := range map[string]bool{"a session whose map names its checkout": false, "a session whose map names no checkout": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			attachedMember(t, f, movedDoc(movedURL, movedPin), nil)
			f.memberOf("team-1")
			file := filepath.Join(f.cwd, teamfile.FileName)
			original, err := os.ReadFile(file) //nolint:gosec // the fixture's own checkout
			if err != nil {
				t.Fatal(err)
			}
			// While back is true the server records that the team moved to the
			// backend the session is on.
			var back atomic.Bool
			back.Store(true)
			inner, server, before := f.deps.Spawn, teamstore.Key("rest", movedURL, "team-1"), f.checkoutPin()
			f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
				if i := slices.Index(spec.Argv, "--profile"); back.Load() && i >= 0 && i+1 < len(spec.Argv) && spec.Argv[i+1] == server && verbOf(spec.Argv) == "team moved" {
					return okResult(mustJSON(map[string]any{"team_ref": "team-1", "moved": true, "adapter": before.Adapter, "url": before.URL, "recorded_at": "2026-11-15T12:00:00Z"})), nil
				}
				return inner(ctx, spec)
			}
			f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
			mark := config.MoveNotFollowedMark(f.stateDir, f.configDir, f.canon())

			// The file names the server: the prompt starts the follow, which
			// finds the two naming each other and leaves that for the next prompt.
			f.moveTeamFile("team-1")
			if got := f.prompted(); !slices.Equal(got, []string{movedLine}) || !exists(mark) {
				t.Fatalf("the prompt whose follow found the two naming each other: %q; a mark is left: %v", got, exists(mark))
			}
			// The file goes back to naming the backend the session is on, before
			// any prompt said what the follow found.
			if err := os.WriteFile(file, original, 0o600); err != nil { //nolint:gosec // G703: the team file of the fixture's own checkout
				t.Fatal(err)
			}
			if old {
				f.withoutCheckout()
			}
			if got := f.prompted(); len(got) != 0 || exists(mark) {
				t.Errorf("the prompt with the file back on the backend the session is on: %q; the mark of a backend the file no longer names is still there: %v", got, exists(mark))
			}
			// The record that was out of date is cleared, and the team moves to
			// the server for good.
			back.Store(false)
			f.moveTeamFile("team-1")
			tf := f.movedFile()
			f.now = f.now.Add(followInterval)
			if got := f.prompted(); len(got) != 0 {
				t.Fatalf("the prompt at the real move: %q, want nothing: the conversation heard that Brigade is asking, and the two no longer name each other", got)
			}
			if pin := f.checkoutPin(); !pin.Matches(tf) {
				t.Fatalf("the follow that prompt started did not follow: %+v", pin)
			}
			got := f.prompted()
			if m := f.mustMap(); m.TeamKey != server || m.BrigadeSessionID != "brigade-sess-2" || len(got) == 0 || got[0] != followedLine(tf, &before, false, "brigade") {
				t.Fatalf("the prompt after the follow: %q\nmap %+v", got, m)
			}
		})
	}
}

// TestAPromptThatFindsItsCheckoutWhereTheFileSaysTakesTheMarkBack (work item
// 341): the checkout followed, so its pin matches its team file, and the
// session has not moved yet. What an earlier follow left for the checkout's
// prompts, when the file named another backend, is nothing to say of the
// checkout now. A prompt that registers the session takes it back on its
// way (resolveTeam). One that does not get that far does too: here the
// server could not be reached a moment ago, so this prompt tries nothing,
// and a session that messages are held for stays in the same way.
func TestAPromptThatFindsItsCheckoutWhereTheFileSaysTakesTheMarkBack(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.spawner.onFollow = func(spec SpawnSpec) { f.runFollow(spec) }
	seam := attachedMember(t, f, movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false)), errResp(protocol.CodeUnavailable, "network")},
	})
	before := f.checkoutPin()
	f.moveTeamFile("team-1")
	tf := f.movedFile()
	got := f.prompted()
	if pin := f.checkoutPin(); !slices.Equal(got, []string{movedLine}) || !pin.Matches(tf) {
		t.Fatalf("the prompt whose follow followed: %q, pin %+v", got, pin)
	}
	if got = f.prompted(); !slices.Equal(got, []string{followedLine(tf, &before, true, "brigade"), moveUnreachedLine}) {
		t.Fatalf("the prompt that could not register the session: %q", got)
	}
	// What a follow left when the file named another backend, laid out by hand.
	mark := config.MoveNotFollowedMark(f.stateDir, f.configDir, f.canon())
	left := mustJSON(followedMark{Version: followedMarkVersion, TeamKey: teamstore.Key("rest", "https://another.example.test", "team-1"), FollowedAt: f.now, Reason: teammove.ReasonEachOther})
	if err := adapterkit.WriteAtomic(mark, append(left, '\n')); err != nil {
		t.Fatal(err)
	}
	// Within the minute the registration is not tried again.
	registers := len(seam.callsFor("session register"))
	if got = f.prompted(); len(got) != 0 || len(seam.callsFor("session register")) != registers {
		t.Fatalf("a prompt within the minute: lines %q, %d registrations", got, len(seam.callsFor("session register"))-registers)
	}
	if m := f.mustMap(); m.TeamKey != f.teamKey || exists(mark) {
		t.Fatalf("the session is on %q, want %q where it was; the mark is still there: %v", m.TeamKey, f.teamKey, exists(mark))
	}
}

// TestAPromptLeavesWhatAFollowLeftThatIsStillTrue (work item 341): a
// session runs on the backend X in a checkout whose pin has gone on to the
// server Y, while the checkout's team file names X again: the team came
// back, and this session never left. Another session of the checkout
// follows from Y to X, finds X silent, and leaves that for the checkout's
// prompts. Until this the first session's every prompt removed that mark,
// because its own file names the backend it is on, and the other session's
// next prompt said that the team file does not match, of a backend that
// was only down. The common prompt takes a mark back only where it speaks
// of nothing: where the checkout's pin matches its file. Here it does not,
// and the mark stays as it was.
func TestAPromptLeavesWhatAFollowLeftThatIsStillTrue(t *testing.T) {
	t.Parallel()
	for name, old := range map[string]bool{"a session whose map names its checkout": false, "a session whose map names no checkout": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newMemberFixture(t)
			attachedMember(t, f, nil, nil)
			// The checkout is joined to the server now, and its file names the
			// backend the session is on: what a follow there and a file that
			// went back leave.
			server := teamstore.Key("rest", movedURL, "team-1")
			f.memberOf("team-1")
			pins := map[string]any{"version": 1, "projects": map[string]any{f.canon(): map[string]any{
				"adapter": "rest", "url": movedURL, "certificate": movedPin, "team_ref": "team-1", "consented_at": "2026-11-14T12:00:00Z",
			}}}
			if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), mustJSON(pins)); err != nil {
				t.Fatal(err)
			}
			if pin, tf := f.checkoutPin(), f.movedFile(); pin.Matches(tf) || teamstore.Key(tf.Adapter, tf.URL, tf.TeamRef) != f.teamKey {
				t.Fatalf("the arrangement: the pin (%+v) matches the file, or the file does not name the backend the session is on", pin)
			}
			// What another session's follow to the backend the file names left.
			mark := config.MoveNotFollowedMark(f.stateDir, f.configDir, f.canon())
			left := append(mustJSON(followedMark{Version: followedMarkVersion, TeamKey: f.teamKey, FollowedAt: f.now, Reason: teammove.ReasonNewBackendFailed}), '\n')
			if err := adapterkit.WriteAtomic(mark, left); err != nil {
				t.Fatal(err)
			}
			if old {
				f.withoutCheckout()
			}
			for i := range 2 {
				if got := f.prompted(); len(got) != 0 {
					t.Fatalf("prompt %d of the session whose file names the backend it is on: %q", i+1, got)
				}
				if now, err := os.ReadFile(mark); err != nil || string(now) != string(left) { //nolint:gosec // the fixture's own state directory
					t.Fatalf("prompt %d: what the follow left is %q, %v; it was %q, and it is still true: the checkout's pin does not match its file", i+1, now, err, left)
				}
			}
			if m := f.mustMap(); m.TeamKey != f.teamKey || m.TeamKey == server {
				t.Fatalf("the session moved: %+v", m.TeamKey)
			}
		})
	}
}
