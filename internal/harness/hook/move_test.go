//go:build !windows

package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// A team that moved (work item 273, ADR 0003): the fixture's checkout is
// pinned to the team on its first backend (seedTeam: supabase,
// https://abc.supabase.co, team-1), and the team file is rewritten to name
// a Brigade server for the same team.
const (
	movedURL   = "https://brigade.example.test"
	movedPin   = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	movedLabel = "alice@example.com"
)

// moveTicket is the ticket the old backend's record carries: a join secret
// of the fixture's team, put together here.
var moveTicket = strings.Join([]string{"brg1", "team-1", strings.Repeat("ef", 16)}, ".")

// byName marks a run whose fixture drops the adapter_command option it
// otherwise sets, so the hook resolves each adapter by its name, as a
// member's session does. The seam still answers every spawn. It is an
// environment entry nothing reads, so a table can carry it beside real ones.
const byName = "BRIGADE_TEST_ADAPTERS_BY_NAME=1"

// newMemberFixture is newFixture for a run by name, on a team of the
// test's own.
func newMemberFixture(t *testing.T) *fixture {
	t.Helper()
	f := newMovingFixture(t)
	f.noAdapterOption = true
	return f
}

// newMovingFixture is newFixture with the checkout joined to a team on a
// backend whose url no other test names. A run by name spawns the bundled
// adapter, whose argv is the same in every test, and the adapter client
// keeps one describe per argv and profile for the life of the process: a
// url of the test's own gives the old backend a profile, and so a describe,
// of the test's own.
func newMovingFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	sum := sha256.Sum256([]byte(t.Name()))
	url := "https://t" + hex.EncodeToString(sum[:6]) + ".supabase.co"
	checkout := filepath.Join(t.TempDir(), "checkout")
	//nolint:gosec // G301: a checkout tree is an ordinary directory
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	const key, ref = "sb_publishable_x", "team-1"
	doc := map[string]any{"version": 1, "adapter": "supabase", "url": url, "publishable_key": key, "team_ref": ref, "team_name": teamName}
	if err := os.WriteFile(filepath.Join(checkout, teamfile.FileName), mustJSON(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	tk := teamstore.Key("supabase", url, ref)
	if err := adapterkit.SaveProfile(f.configDir, tk, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase", URL: url, PublishableKey: key, TeamRef: ref, TeamName: teamName,
	}); err != nil {
		t.Fatal(err)
	}
	canon, err := teamfile.Canonicalize(checkout)
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]any{"version": 1, "projects": map[string]any{canon: map[string]any{
		"adapter": "supabase", "url": url, "publishable_key": key, "team_ref": ref, "consented_at": "2026-09-06T18:00:00Z",
	}}}
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(f.configDir), mustJSON(pins)); err != nil {
		t.Fatal(err)
	}
	f.cwd, f.teamKey = checkout, tk
	return f
}

// moveTeamFile rewrites the checkout's team file to name the server, under
// its certificate, for the team ref names.
func (f *fixture) moveTeamFile(ref string) {
	f.t.Helper()
	doc := map[string]any{"version": 1, "adapter": "rest", "url": movedURL, "certificate": movedPin, "team_ref": ref, "team_name": teamName}
	if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), mustJSON(doc), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// labelOldBinding gives the member a label on the team's first backend.
func (f *fixture) labelOldBinding() {
	f.t.Helper()
	old, err := teamstore.LoadBinding(f.configDir, f.teamKey)
	if err != nil {
		f.t.Fatal(err)
	}
	old.HumanLabel = movedLabel
	if err := adapterkit.SaveProfile(f.configDir, f.teamKey, old); err != nil {
		f.t.Fatal(err)
	}
}

// movedDoc is the old backend's `team moved` answer for a record of a move
// to url under certificate.
func movedDoc(url, certificate string) map[string]any {
	doc := map[string]any{"team_ref": "team-1", "moved": true, "adapter": "rest", "url": url, "recorded_at": "2026-11-14T12:00:00Z"}
	if certificate != "" {
		doc["certificate"] = certificate
	}
	return doc
}

// useMoveSeam is useSeam with the one thing an adapter does that a fixed
// answer cannot: `team moved --ticket-file <path>` writes the ticket to the
// path and answers it as ticket_file. moved nil answers the seam's own
// response for the command (a refusal, or a team that has not moved).
func (f *fixture) useMoveSeam(moved map[string]any, responses map[string][]fakeadapter.Response) *adapterSeam {
	f.t.Helper()
	seam := f.useSeam(responses)
	// The adapters of these tests carry the record's two commands.
	var described protocol.DescribeResult
	if err := json.Unmarshal(seam.describe, &described); err != nil {
		f.t.Fatal(err)
	}
	described.Capabilities = append(described.Capabilities, "team.move")
	seam.describe = mustJSON(&described)
	f.deps.Spawn = func(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
		if verbOf(spec.Argv) != "team moved" || moved == nil {
			return seam.spawn(ctx, spec)
		}
		seam.mu.Lock()
		seam.calls = append(seam.calls, adapterCall{Argv: slices.Clone(spec.Argv), Env: slices.Clone(spec.Env), Stdin: slices.Clone(spec.Stdin)})
		seam.mu.Unlock()
		answer := map[string]any{}
		for k, v := range moved {
			answer[k] = v
		}
		if i := slices.Index(spec.Argv, "--ticket-file"); i >= 0 && i+1 < len(spec.Argv) {
			if err := adapterkit.WriteTicketFile(spec.Argv[i+1], moveTicket); err != nil {
				f.t.Error(err)
			}
			answer["ticket_file"] = spec.Argv[i+1]
		}
		return okResult(mustJSON(answer)), nil
	}
	return seam
}

// asked lists the adapter commands a seam saw, in order, but for describe:
// an adapter's describe is answered from local files and asks no backend
// anything.
func asked(seam *adapterSeam) []string {
	var out []string
	for _, verb := range seam.verbs() {
		if verb != "describe" {
			out = append(out, verb)
		}
	}
	return out
}

// profileOf is the --profile an adapter call was made under.
func profileOf(c adapterCall) string {
	if i := slices.Index(c.Argv, "--profile"); i >= 0 && i+1 < len(c.Argv) {
		return c.Argv[i+1]
	}
	return ""
}

// checkoutPin is the pin of the fixture's checkout as the store holds it.
func (f *fixture) checkoutPin() teamstore.Pin {
	f.t.Helper()
	canon, err := teamfile.Canonicalize(f.cwd)
	if err != nil {
		f.t.Fatal(err)
	}
	pin, ok, err := teamstore.LookupPin(f.configDir, canon)
	if err != nil || !ok {
		f.t.Fatalf("the checkout's pin: %v, %v", ok, err)
	}
	return *pin
}

// noTicketLeft asserts that no file under the store holds the ticket and
// that the follow's own ticket file is gone.
func (f *fixture) noTicketLeft() {
	f.t.Helper()
	err := filepath.WalkDir(f.configDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(d.Name(), ".move-ticket-") {
			f.t.Errorf("the follow left its ticket file in the store")
		}
		if data, rerr := os.ReadFile(path); rerr == nil && strings.Contains(string(data), moveTicket) { //nolint:gosec // the fixture's own store
			f.t.Errorf("a file of the store holds the move ticket")
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// TestHookFollowsATeamThatMovedOnItsAdministratorsRecord is the member's
// side of a move end to end, with no command run: the session's start
// finds the pin drifted to another backend for the same team, asks the
// backend the checkout joined, which answers the administrator's record of
// exactly the backend the file names, joins that backend with the record's
// ticket under the label the member had, writes the binding and the pin,
// says so in one line before any other, and attaches. The ticket is in no
// line and in no file afterwards.
func TestHookFollowsATeamThatMovedOnItsAdministratorsRecord(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.labelOldBinding()
	oldKey := f.teamKey
	before := f.checkoutPin()
	f.moveTeamFile("team-1")
	seam := f.useMoveSeam(movedDoc(movedURL, movedPin), map[string][]fakeadapter.Response{
		"team join":        {okResp(mustJSON(map[string]any{"team_ref": "team-1", "team_name": teamName, "principal_ref": "p-new", "rejoined": false}))},
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
	})
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	newKey := teamstore.Key("rest", movedURL, "team-1")
	tf, err := teamfile.Parse(filepath.Join(f.cwd, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	got := lines(out)
	if len(got) < 2 || got[0] != followedLine(tf, &before, true, "brigade") || !strings.Contains(got[1], "(brigade-sess-1)") {
		t.Fatalf("lines = %q, want the follow's line and then the context line", got)
	}
	for _, want := range []string{`team "ops"`, "brigade.example.test", movedPin, strings.TrimPrefix(before.URL, "https://"), "team leave", "follow_team_move"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the follow's line lacks %q: %s", want, got[0])
		}
	}
	if strings.Contains(out+errOut, moveTicket) || strings.Contains(out, movedLabel) {
		t.Fatalf("the ticket or the member's label reached a stream")
	}
	if verbs := asked(seam); !slices.Equal(verbs, []string{"team moved", "team join", "session register"}) {
		t.Fatalf("adapter calls: %v", verbs)
	}
	moved, joined := seam.callsFor("team moved")[0], seam.callsFor("team join")[0]
	if profileOf(moved) != oldKey || profileOf(joined) != newKey {
		t.Fatalf("team moved ran under %q and team join under %q, want the old team key and the new", profileOf(moved), profileOf(joined))
	}
	var req protocol.TeamJoinRequest
	if err := json.Unmarshal(joined.Stdin, &req); err != nil || req.JoinSecret != moveTicket || req.HumanLabel != movedLabel {
		t.Fatalf("the join's document does not carry the ticket and the member's label: %v", err)
	}
	if slices.Contains(moved.Argv, moveTicket) || slices.Contains(joined.Argv, moveTicket) {
		t.Fatalf("the ticket is on an adapter's argv")
	}
	pin := f.checkoutPin()
	if !pin.Matches(tf) || pin.MovedFrom != before.URL {
		t.Fatalf("the pin after the follow: %+v", pin)
	}
	if binding, err := teamstore.LoadBinding(f.configDir, newKey); err != nil || teamstore.VerifyBinding(binding, tf) != nil || binding.PrincipalRef != "p-new" {
		t.Fatalf("the binding after the follow: %+v, %v", binding, err)
	}
	if m := f.mustMap(); m.TeamKey != newKey {
		t.Fatalf("the session attached under team key %q, want the new backend's", m.TeamKey)
	}
	f.noTicketLeft()

	// The next start of a session in the checkout is an ordinary one: the pin
	// matches, and nothing of the move is asked or said again.
	asked := len(seam.callsFor("team moved"))
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || strings.Contains(out, "followed") {
		t.Fatalf("a second start: exit %d, lines %q", exit, lines(out))
	}
	if len(seam.callsFor("team moved")) != asked {
		t.Fatalf("a second start asked the old backend again")
	}
}

// TestHookDoesNotFollowWithoutTheRecordOrTheOption: whenever the backend
// the checkout joined does not hold a record of exactly the backend the
// file names, or the member turned the follow off, or the session names its
// adapter itself, the session attaches to nothing, says the drift line and
// not one byte of the file, and the store is as it was.
func TestHookDoesNotFollowWithoutTheRecordOrTheOption(t *testing.T) {
	t.Parallel()
	joinOK := okResp(mustJSON(map[string]any{"team_ref": "team-1", "team_name": teamName, "principal_ref": "p-new", "rejoined": false}))
	for _, tc := range []struct {
		name      string
		moved     map[string]any
		responses map[string][]fakeadapter.Response
		env       []string
		// asks is the adapter commands the start runs, in order.
		asks []string
		// first is a line printed before the drift line, "" for none.
		first string
		// spoil changes the fixture before the start, nil for nothing.
		spoil func(f *fixture, seam *adapterSeam)
	}{
		{"the record names another url", movedDoc("https://elsewhere.example.test", movedPin),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, []string{byName}, []string{"team moved"}, "", nil},
		{"the record names another certificate", movedDoc(movedURL, strings.Replace(movedPin, "3", "4", 1)),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, []string{byName}, []string{"team moved"}, "", nil},
		{"the record names no certificate", movedDoc(movedURL, ""),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, []string{byName}, []string{"team moved"}, "", nil},
		{"no move is recorded", nil,
			map[string][]fakeadapter.Response{"team moved": {okResp(mustJSON(map[string]any{"team_ref": "team-1", "moved": false}))}, "team join": {joinOK}},
			[]string{byName}, []string{"team moved"}, "", nil},
		{"the old backend refuses the member", nil,
			map[string][]fakeadapter.Response{"team moved": {errResp(protocol.CodeUnauthorized, "")}, "team join": {joinOK}},
			[]string{byName}, []string{"team moved"}, "", nil},
		{"the old backend is unreachable", nil,
			map[string][]fakeadapter.Response{"team moved": {errResp(protocol.CodeUnavailable, "network")}, "team join": {joinOK}},
			[]string{byName}, []string{"team moved"}, "", nil},
		{"the new backend refuses the ticket", movedDoc(movedURL, movedPin),
			map[string][]fakeadapter.Response{"team join": {errResp(protocol.CodeUnauthorized, "")}}, []string{byName}, []string{"team moved", "team join"}, "", nil},
		{"the option is off", movedDoc(movedURL, movedPin),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, []string{byName, config.OptionFollowTeamMove + "=off"}, nil, "", nil},
		{"the option is neither word", movedDoc(movedURL, movedPin),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, []string{byName, config.OptionFollowTeamMove + "=sometimes"}, nil, config.WarnFollowTeamMoveInvalid, nil},
		{"the session names its adapter itself", movedDoc(movedURL, movedPin),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, nil, nil, "", nil},
		{"the old backend's adapter keeps no record of moves", movedDoc(movedURL, movedPin),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, []string{byName}, nil, "", func(_ *fixture, seam *adapterSeam) {
				seam.describe = describeDoc(protocol.ProtocolVersion, teamName)
			}},
		{"this machine was a member at the new backend and left it", movedDoc(movedURL, movedPin),
			map[string][]fakeadapter.Response{"team join": {joinOK}}, []string{byName}, nil, "", func(f *fixture, _ *adapterSeam) {
				if err := adapterkit.SaveProfile(f.configDir, teamstore.Key("rest", movedURL, "team-1"), &adapterkit.Profile{
					Version: adapterkit.ProfileVersion, Adapter: "rest", URL: movedURL, Certificate: movedPin, PrincipalRef: "p-new",
				}); err != nil {
					f.t.Fatal(err)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newMovingFixture(t)
			f.noAdapterOption = slices.Contains(tc.env, byName)
			f.labelOldBinding()
			f.moveTeamFile("team-1")
			seam := f.useMoveSeam(tc.moved, tc.responses)
			if tc.spoil != nil {
				tc.spoil(f, seam)
			}
			before := storeFingerprint(t, f.configDir)
			pinBefore := f.checkoutPin()
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), tc.env...)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			want := []string{driftLine}
			if tc.first != "" {
				want = []string{tc.first, driftLine}
			}
			if got := lines(out); !slices.Equal(got, want) {
				t.Fatalf("lines = %q, want %q", got, want)
			}
			for _, marker := range []string{"brigade.example", "elsewhere.example", "sha256:", moveTicket} {
				if strings.Contains(out, marker) {
					t.Fatalf("a line echoed %q", marker)
				}
			}
			if got := asked(seam); !slices.Equal(got, tc.asks) {
				t.Fatalf("adapter calls: %v, want %v", got, tc.asks)
			}
			if f.mapExists() {
				t.Fatalf("the session attached")
			}
			if pin := f.checkoutPin(); pin != pinBefore {
				t.Fatalf("the pin changed: %+v", pin)
			}
			if b, err := teamstore.LoadBinding(f.configDir, teamstore.Key("rest", movedURL, "team-1")); err == nil && b.TeamRef != "" {
				t.Fatalf("a membership of the backend the file names was written")
			}
			f.noTicketLeft()
			// The store holds what it held, but for the empty lock file a
			// follow that got as far as asking leaves beside the pins.
			_ = os.Remove(filepath.Join(f.configDir, "team-move.lock"))
			if after := storeFingerprint(t, f.configDir); after != before {
				t.Fatalf("the store changed")
			}
		})
	}
}

// TestHookSaysAnUnusableFollowOptionAtAnOrdinaryStart: a member who misspelt
// the option learns it at every session start, as with each other option,
// and not only on the day a team moves; the session attaches as usual.
func TestHookSaysAnUnusableFollowOptionAtAnOrdinaryStart(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), config.OptionFollowTeamMove+"=sometimes")
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	if got := lines(out); !slices.Contains(got, config.WarnFollowTeamMoveInvalid) || !strings.Contains(got[0], "(brigade-sess-1)") || !f.mapExists() {
		t.Fatalf("lines = %q, map %v: want the context line first, the option's warning, and a session that attached", got, f.mapExists())
	}
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"), config.OptionFollowTeamMove+"=off"); exit != 0 || strings.Contains(out, "follow_team_move") {
		t.Fatalf("a start with the option off said something about it: %q", out)
	}
}

// TestHookNeverAsksAboutAFileThatNamesAnotherTeam: a file re-pointed to
// ANOTHER team is not a move, whatever backend it names, and no backend is
// asked anything: the drift line, as before this work item.
func TestHookNeverAsksAboutAFileThatNamesAnotherTeam(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.moveTeamFile("team-2")
	seam := f.useMoveSeam(movedDoc(movedURL, movedPin), nil)
	exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 {
		t.Fatal(exit)
	}
	if got := lines(out); !slices.Equal(got, []string{driftLine}) {
		t.Fatalf("lines = %q, want exactly the drift line", got)
	}
	if got := seam.verbs(); len(got) != 0 {
		t.Fatalf("adapter calls for a file that names another team: %v", got)
	}
}

// TestPromptRetryNeverFollows: a session that started before the move was
// recorded has no map, so each later prompt runs the registration again,
// in the few seconds a prompt hook has. That retry never follows, though
// the record is there by then: it says the drift line and asks no backend
// anything. The session follows when it is started again.
func TestPromptRetryNeverFollows(t *testing.T) {
	t.Parallel()
	f := newMemberFixture(t)
	f.labelOldBinding()
	f.moveTeamFile("team-1")
	seam := f.useMoveSeam(nil, map[string][]fakeadapter.Response{
		"team moved":       {okResp(mustJSON(map[string]any{"team_ref": "team-1", "moved": false}))},
		"team join":        {okResp(mustJSON(map[string]any{"team_ref": "team-1", "team_name": teamName, "principal_ref": "p-new", "rejoined": false}))},
		"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
	})
	if exit, out, _ := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !slices.Equal(lines(out), []string{driftLine}) || f.mapExists() {
		t.Fatalf("a start before the record: exit %d, lines %q", exit, lines(out))
	}
	// The administrator records the move.
	seam = f.useMoveSeam(movedDoc(movedURL, movedPin), seam.responses)
	if exit, out, errOut := f.run(SubPrompt, f.promptDoc("default")); exit != 0 || !slices.Equal(lines(out), []string{driftLine}) || f.mapExists() {
		t.Fatalf("the prompt's retry: exit %d, lines %q\n%s", exit, lines(out), errOut)
	}
	if got := asked(seam); len(got) != 0 {
		t.Fatalf("the prompt's retry asked a backend: %v", got)
	}
	if pin := f.checkoutPin(); pin.Adapter != "supabase" {
		t.Fatalf("the prompt's retry moved the pin: %+v", pin)
	}
	if exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 || !strings.Contains(out, "followed team") || !f.mapExists() {
		t.Fatalf("the session started again: exit %d, lines %q\n%s", exit, lines(out), errOut)
	}
}

// TestPromptTellsARunningSessionOnceThatItsTeamMoved: a session that was
// attached when the team file came to name another backend for its team is
// told once, at its next prompt, that it stays where it is and what a new
// session does; the line echoes nothing from the file, no backend is asked,
// a later prompt says nothing more, and the session's end removes the
// stamp. A file that names another team, or still the session's backend,
// says nothing.
func TestPromptTellsARunningSessionOnceThatItsTeamMoved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{
		"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
		"session heartbeat": {okResp(heartbeatDoc())},
		"session close":     {okResp(closeDoc())},
	})
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	// The map names the team by the reference the adapter's describe
	// answers, which in this fixture is teamRef.
	m := f.mustMap()
	if m.TeamRef != teamRef {
		t.Fatalf("the fixture's map names team %q", m.TeamRef)
	}
	stamp := config.TeamMovedStamp(f.stateDir, f.key())
	prompt := func() string {
		t.Helper()
		exit, out, errOut := f.run(SubPrompt, f.promptDoc("default"))
		if exit != 0 {
			t.Fatalf("prompt: exit %d: %s", exit, errOut)
		}
		return out
	}
	if out := prompt(); strings.Contains(out, movedLine) {
		t.Fatalf("a prompt with the team file unchanged: %q", out)
	}
	f.moveTeamFile("another-team")
	if out := prompt(); strings.Contains(out, movedLine) {
		t.Fatalf("a prompt with the team file naming another team: %q", out)
	}
	if _, err := os.Stat(stamp); err == nil {
		t.Fatalf("the stamp was written for a file that names another team")
	}
	// A stamp that a dead session left under this process id is not this
	// session's, and silences nothing.
	if err := adapterkit.WriteAtomic(stamp, []byte("a-session-that-ended\n")); err != nil {
		t.Fatal(err)
	}
	calls := len(seam.verbs())
	f.moveTeamFile(teamRef)
	out := prompt()
	if !slices.Contains(lines(out), movedLine) {
		t.Fatalf("the prompt after the team file changed: %q, want the moved line", out)
	}
	if strings.Contains(out, "brigade.example") || strings.Contains(out, "sha256:") {
		t.Fatalf("the moved line echoed the file: %q", out)
	}
	if got := seam.verbs()[calls:]; slices.Contains(got, "team moved") || slices.Contains(got, "team join") {
		t.Fatalf("the prompt asked a backend about the move: %v", got)
	}
	if told, err := os.ReadFile(stamp); err != nil || strings.TrimSpace(string(told)) != m.BrigadeSessionID {
		t.Fatalf("the stamp does not name the session that was told: %v", err)
	}
	if out := prompt(); strings.Contains(out, movedLine) {
		t.Fatalf("a second prompt said the line again: %q", out)
	}
	if exit, _, errOut := f.run(SubSessionEnd, f.endDoc("exit")); exit != 0 {
		t.Fatalf("session end: exit %d: %s", exit, errOut)
	}
	if _, err := os.Stat(stamp); err == nil {
		t.Fatalf("the session's end left the stamp")
	}
}
