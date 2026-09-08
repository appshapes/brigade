package commands

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// P7-11: the three verbs that handle the join secret run inside a Claude
// Code session. Every test here runs under sessionEnv (CLAUDE_PID set,
// stdin not a terminal) and arms the two terminal seams to FAIL the test
// if touched: nothing may prompt inside a session. The store an in-session
// command writes is the one the start facts name — a persona directory
// that is deliberately NOT the fixture's XDG default, so a passing test
// proves the facts were read rather than guessed.

// writeStartFacts is what SessionStart leaves for an in-session command.
func writeStartFacts(t *testing.T, f *fixture, configDir string) {
	t.Helper()
	if err := (sessionmap.Store{StateDir: f.stateDir}).WriteStart(&sessionmap.StartFacts{
		ClaudePID: fixturePID, ClaudeSessionID: "native-1", ConfigDir: configDir, WrittenAt: fixtureNow,
	}); err != nil {
		t.Fatal(err)
	}
}

// writeSecretFile puts secret in a file at path — mode 0644, the mode a
// member's saved copy usually has: Brigade checks where the file is and
// nothing about its mode or owner (owner ruling 4), so every test that
// joins through such a file is the positive control for that.
func writeSecretFile(t *testing.T, path, secret string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o644); err != nil { //nolint:gosec // G306: a member's own copy, mode deliberately NOT 0600
		t.Fatal(err)
	}
}

// unattach removes the fixture's by-pid map: the session of a member who
// has not joined yet has none (the hook wrote only the start facts).
func unattach(t *testing.T, f *fixture) {
	t.Helper()
	if err := (sessionmap.Store{StateDir: f.stateDir}).DeleteByPID(fixturePID); err != nil {
		t.Fatal(err)
	}
}

// xdgStore is the store an in-session command would guess without the
// start facts: the XDG default under sessionEnv.
func xdgStore(f *fixture) string { return filepath.Join(f.dirs.XDGConfig, "brigade") }

// sessionInv is an in-session team invocation with the terminal seams
// armed.
func sessionInv(t *testing.T, f *fixture, top string, args ...string) Invocation {
	t.Helper()
	iv := f.inv(f.sessionEnv(), "", args...)
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	iv.Deps.IsTerminal = func(io.Reader) bool { return false }
	iv.Deps.ReadSecret = func() (string, error) {
		t.Fatal("an in-session command asked for the secret at a terminal")
		return "", nil
	}
	iv.Deps.Confirm = func(string) (bool, error) {
		t.Fatal("an in-session command asked a y/N question")
		return false, nil
	}
	iv.Deps.PromptLine = func(string) (string, error) {
		t.Fatal("an in-session command prompted for a line")
		return "", nil
	}
	return iv
}

func TestTeamJoinInSessionWithSecretFile(t *testing.T) {
	t.Parallel()
	f, top := joinFixture(t)
	unattach(t, f)
	persona := filepath.Join(f.dirs.Root, "persona-config")
	writeStartFacts(t, f, persona)
	secretFile := filepath.Join(f.dirs.Root, "join.secret")
	writeSecretFile(t, secretFile, setupSecret)
	stamp := config.RegisterRetryStamp(f.stateDir, fixturePID)
	if err := adapterkit.MkdirPrivate(filepath.Dir(stamp)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stamp, []byte("2026-09-08T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	xdgBefore := storeHash(t, xdgStore(f))

	if err := Team(sessionInv(t, f, top, "join", "--secret-file", secretFile)); err != nil {
		t.Fatal(err)
	}
	// The consent line is a statement, the success line names the next
	// prompt; nothing else, and no secret on either stream.
	want := "join team \"devs\" (" + setupTeamRef + ") at abc.supabase.co — consented by this invocation\n" +
		"joined team \"devs\" (" + setupTeamRef + "); this session attaches at your next prompt\n"
	if f.out.String() != want {
		t.Fatalf("stdout = %q, want %q", f.out.String(), want)
	}
	if strings.Contains(f.out.String()+f.errb.String(), setupSecret) {
		t.Fatal("the secret reached a stream")
	}
	// Binding and pin in the persona store the start facts name; the XDG
	// default store untouched.
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if _, err := teamstore.LoadBinding(persona, key); err != nil {
		t.Fatalf("binding in the persona store: %v", err)
	}
	canon, _ := teamfile.Canonicalize(top)
	if _, ok, _ := teamstore.LookupPin(persona, canon); !ok {
		t.Fatal("no pin in the persona store")
	}
	if storeHash(t, xdgStore(f)) != xdgBefore {
		t.Fatal("the XDG default store was written")
	}
	// The adapter got exactly the captured join a terminal join sends,
	// the secret inside the document and never on argv.
	if verbs := f.rec.verbs(); !slices.Equal(verbs, []string{"team join"}) {
		t.Fatalf("spawns = %v, want the one join", verbs)
	}
	if argv := argvOf(f.rec.spec(t, 0)); strings.Contains(argv, setupSecret) || strings.Contains(argv, "secret-file") {
		t.Fatalf("argv = %q carries the secret or the flag", argv)
	}
	// The prompt hook's retry stamp is gone: the next prompt attaches.
	if _, err := os.Lstat(stamp); err == nil {
		t.Fatal("the registration-retry stamp survived the join")
	}
}

func TestTeamJoinInSessionNeedsTheSecretFile(t *testing.T) {
	t.Parallel()
	f, top := joinFixture(t)
	persona := filepath.Join(f.dirs.Root, "persona-config")
	writeStartFacts(t, f, persona)
	before := storeHash(t, persona)
	err := Team(sessionInv(t, f, top, "join"))
	perr := wantCodeErr(t, err, protocol.CodeUsage, "")
	if !strings.Contains(perr.Message, "--secret-file") || !strings.Contains(perr.Message, "never paste") {
		t.Fatalf("refusal = %q, want the file remedy and the never-paste rule", perr.Message)
	}
	if f.rec.count() != 0 || f.out.Len() != 0 || storeHash(t, persona) != before {
		t.Fatalf("spawned %d, stdout %q, store changed %v", f.rec.count(), f.out.String(), storeHash(t, persona) != before)
	}
}

func TestTeamJoinInSessionSecretFileRefusals(t *testing.T) { //nolint:tparallel // the subtests share one fixture and its spawn counter, in order
	t.Parallel()
	f, top := joinFixture(t)
	persona := filepath.Join(f.dirs.Root, "persona-config")
	writeStartFacts(t, f, persona)
	huge := filepath.Join(f.dirs.Root, "huge.secret")
	if err := os.WriteFile(huge, make([]byte, 65<<10), 0o644); err != nil { //nolint:gosec // G306: a member's own file
		t.Fatal(err)
	}
	other := filepath.Join(f.dirs.Root, "other.secret")
	writeSecretFile(t, other, "brg1.t_other.join-secret-value-999")
	malformed := filepath.Join(f.dirs.Root, "malformed.secret")
	writeSecretFile(t, malformed, "not-a-secret")
	// `lnk` lives OUTSIDE the checkout and points INSIDE it: the lexical
	// `lnk/..` spelling resolves under the checkout in the kernel.
	if err := os.Symlink(filepath.Join(top, "sub"), filepath.Join(f.dirs.Root, "lnk")); err != nil {
		t.Fatal(err)
	}
	// A symlink outside the checkout to a real secret file inside it: the
	// file's real location is inside, so it is refused as inside.
	writeSecretFile(t, filepath.Join(top, "real.secret"), setupSecret)
	if err := os.Symlink(filepath.Join(top, "real.secret"), filepath.Join(f.dirs.Root, "sl.secret")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		code       protocol.Code
	}{
		{"relative", "rel.secret", protocol.CodeUsage},
		{"inside the checkout", filepath.Join(top, "in.secret"), protocol.CodeUsage},
		{"dot-dot through a symlink into the checkout", f.dirs.Root + "/lnk/../in.secret", protocol.CodeUsage},
		{"symlink to a file inside the checkout", filepath.Join(f.dirs.Root, "sl.secret"), protocol.CodeUsage},
		{"missing", filepath.Join(f.dirs.Root, "missing.secret"), protocol.CodeConfig},
		{"a directory", f.dirs.Root, protocol.CodeConfig},
		{"too large to be a secret file", huge, protocol.CodeInvalidInput},
		{"malformed", malformed, protocol.CodeInvalidInput},
		{"another team's secret", other, protocol.CodeInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.out.Reset()
			before := storeHash(t, persona)
			err := Team(sessionInv(t, f, top, "join", "--secret-file", tc.path))
			perr := wantCodeErr(t, err, tc.code, "")
			if strings.Contains(perr.Message, setupSecret) {
				t.Fatal("the message carries the secret")
			}
			// No half-record: a refusal prints no consent line.
			if f.rec.count() != 0 || f.out.Len() != 0 || storeHash(t, persona) != before {
				t.Fatalf("spawned %d, stdout %q, or wrote the store", f.rec.count(), f.out.String())
			}
		})
	}
}

// TestTeamJoinInSessionCrossTeamRepoint: a pinned checkout whose file now
// names another team is a membership-grade event (owner ruling 3): in a
// session it refuses without --secret-file — with no consent line — and
// joins with it, printing the diff and exactly one consent line.
func TestTeamJoinInSessionCrossTeamRepoint(t *testing.T) {
	t.Parallel()
	f, top := joinFixture(t)
	persona := filepath.Join(f.dirs.Root, "persona-config")
	if err := os.MkdirAll(persona, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStartFacts(t, f, persona)
	canon, _ := teamfile.Canonicalize(top)
	if err := write.Pin(persona, canon, teamstore.Pin{
		Adapter: "supabase", URL: "https://abc.supabase.co",
		PublishableKey: "sb_publishable_x", TeamRef: "t_old",
	}); err != nil {
		t.Fatal(err)
	}
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if err := adapterkit.SaveProfile(persona, key, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase",
		URL: "https://abc.supabase.co", PublishableKey: "sb_publishable_x",
		TeamRef: setupTeamRef, PrincipalRef: "p_keep",
	}); err != nil {
		t.Fatal(err)
	}
	f.rec.on("session list", answer{result: `{"sessions":[],"server_time":"2026-09-08T18:00:00Z"}`})

	err := Team(sessionInv(t, f, top, "join"))
	perr := wantCodeErr(t, err, protocol.CodeUsage, "")
	if !strings.Contains(perr.Message, "--secret-file") || strings.Contains(f.out.String(), "consented") {
		t.Fatalf("refusal = %q, stdout = %q", perr.Message, f.out.String())
	}
	if pin, ok, _ := teamstore.LookupPin(persona, canon); !ok || pin.TeamRef != "t_old" {
		t.Fatalf("pin moved without the secret: %+v", pin)
	}

	f.out.Reset()
	secretFile := filepath.Join(f.dirs.Root, "join.secret")
	writeSecretFile(t, secretFile, setupSecret)
	if err := Team(sessionInv(t, f, top, "join", "--secret-file", secretFile)); err != nil {
		t.Fatal(err)
	}
	// The fixture's session is attached to team "ops" (its by-pid map):
	// the join says so instead of promising an attach the prompt hook
	// would never perform.
	out := f.out.String()
	if !strings.Contains(out, "team: t_old → "+setupTeamRef) || strings.Count(out, "consented by this invocation") != 1 ||
		!strings.Contains(out, "joined team \"devs\" ("+setupTeamRef+"); this session stays on team \""+fixtureTeamName+"\" until /reload-plugins or a new session") {
		t.Fatalf("stdout = %q", out)
	}
	if pin, ok, _ := teamstore.LookupPin(persona, canon); !ok || pin.TeamRef != setupTeamRef {
		t.Fatalf("pin after the re-point: %+v", pin)
	}
	if !slices.Contains(f.rec.verbs(), "team join") {
		t.Fatal("a cross-team re-point must join")
	}
}

func TestTeamCreateInSessionWithoutStartFacts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	before := storeHash(t, xdgStore(f))
	err := Team(sessionInv(t, f, top, "create", "--url", "https://abc.supabase.co",
		"--key", "sb_publishable_x", "--name", "devs", "--secret-file", filepath.Join(f.dirs.Root, "team.secret")))
	wantCode(t, err, protocol.CodeConfig, config.ReasonNotRegistered)
	if f.rec.count() != 0 || storeHash(t, xdgStore(f)) != before {
		t.Fatalf("spawned %d or guessed the XDG store", f.rec.count())
	}
	if _, err := os.Lstat(filepath.Join(top, teamfile.FileName)); err == nil {
		t.Fatal("a team file was written without a store to bind it to")
	}
}

func TestTeamJoinInSessionWithoutStartFacts(t *testing.T) {
	t.Parallel()
	f, top := joinFixture(t)
	secretFile := filepath.Join(f.dirs.Root, "join.secret")
	writeSecretFile(t, secretFile, setupSecret)
	before := storeHash(t, xdgStore(f))
	err := Team(sessionInv(t, f, top, "join", "--secret-file", secretFile))
	wantCode(t, err, protocol.CodeConfig, config.ReasonNotRegistered)
	if f.rec.count() != 0 || storeHash(t, xdgStore(f)) != before {
		t.Fatalf("spawned %d or guessed the XDG store", f.rec.count())
	}
}

func TestTeamJoinInSessionSecondCheckoutNeedsNoSecret(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	unattach(t, f)
	persona := filepath.Join(f.dirs.Root, "persona-config")
	writeStartFacts(t, f, persona)
	// A credential for the team already in the persona store (a first
	// join happened elsewhere); a second checkout of the same project.
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if err := adapterkit.SaveProfile(persona, key, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase",
		URL: "https://abc.supabase.co", PublishableKey: "sb_publishable_x",
		TeamRef: setupTeamRef, PrincipalRef: "p_keep",
	}); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(f.dirs.Root, "checkout2")
	//nolint:gosec // G301: an ordinary checkout tree
	if err := os.MkdirAll(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, top)
	f.rec.on("session list", answer{result: `{"sessions":[],"server_time":"2026-09-08T18:00:00Z"}`})

	if err := Team(sessionInv(t, f, top, "join")); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "re-consent to team \"devs\"") || !strings.Contains(out, "re-consented team \"devs\" ("+setupTeamRef+"); this session attaches at your next prompt") {
		t.Fatalf("stdout = %q", out)
	}
	canon, _ := teamfile.Canonicalize(top)
	if _, ok, _ := teamstore.LookupPin(persona, canon); !ok {
		t.Fatal("the second checkout was not pinned")
	}
	if slices.Contains(f.rec.verbs(), "team join") {
		t.Fatal("a second checkout must not re-join")
	}
}

func TestTeamCreateInSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	persona := filepath.Join(f.dirs.Root, "persona-config")
	writeStartFacts(t, f, persona)
	secretFile := filepath.Join(f.dirs.Root, "team.secret")
	f.rec.on("profile init", answer{result: `{"profile":"x","initialized":true}`})
	f.rec.on("team create", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_1"}`})
	xdgBefore := storeHash(t, xdgStore(f))

	err := Team(sessionInv(t, f, top, "create", "--url", "https://abc.supabase.co",
		"--key", "sb_publishable_x", "--name", "devs", "--secret-file", secretFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teamfile.Parse(filepath.Join(top, teamfile.FileName)); err != nil {
		t.Fatalf("team file: %v", err)
	}
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if _, err := teamstore.LoadBinding(persona, key); err != nil {
		t.Fatalf("binding in the persona store: %v", err)
	}
	canon, _ := teamfile.Canonicalize(top)
	if _, ok, _ := teamstore.LookupPin(persona, canon); !ok {
		t.Fatal("no pin in the persona store")
	}
	if storeHash(t, xdgStore(f)) != xdgBefore {
		t.Fatal("the XDG default store was written")
	}
	// The four lines, and no absolute path of the checkout on the
	// model's stdout (F1): line 2 names the file, not where it is.
	lines := strings.Split(strings.TrimSuffix(f.out.String(), "\n"), "\n")
	if len(lines) != 4 || lines[1] != "wrote "+teamfile.FileName+" at the repository toplevel" {
		t.Fatalf("stdout = %q", f.out.String())
	}
	if strings.Contains(f.out.String(), top) {
		t.Fatalf("stdout %q carries the checkout's absolute path", f.out.String())
	}
	if got := argvOf(f.rec.spec(t, 1)); !strings.Contains(got, "--secret-file "+secretFile) {
		t.Fatalf("team create argv = %s, want --secret-file", got)
	}
}

// TestTeamRotateSecretInSession: the pass-through runs on the session's
// own adapter (the map's command) and forwards --secret-file, and the
// harness applies create's outside-the-repository rule to it first —
// inside the checkout is refused before any spawn.
func TestTeamRotateSecretInSession(t *testing.T) {
	t.Parallel()
	f, dump := passThroughFixture(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"team rotate-secret": {{Result: []byte(`{"team_ref":"t1","team_name":"x","secret_version":2}`)}},
		},
	})
	m := f.byPID()
	m.AdapterCommand = []string{fakeAdapterBin, "--script", filepath.Join(f.dirs.Root, "script.json")}
	f.writeMap(t, m)
	top := mkCheckout(t, f.dirs.Root)

	inside := filepath.Join(top, "rotated.secret")
	err := Team(sessionInv(t, f, top, "rotate-secret", "--secret-file", inside))
	perr := wantCodeErr(t, err, protocol.CodeUsage, "")
	if !strings.Contains(perr.Message, "must not be inside this project") {
		t.Fatalf("refusal = %q", perr.Message)
	}
	if _, err := os.Lstat(dump); err == nil {
		t.Fatal("the adapter was spawned for a secret file inside the checkout")
	}
	err = Team(sessionInv(t, f, top, "rotate-secret", "--secret-file="+inside))
	wantCode(t, err, protocol.CodeUsage, "")

	outside := filepath.Join(f.dirs.Root, "rotated.secret")
	if err := Team(sessionInv(t, f, top, "rotate-secret", "--secret-file", outside)); err != nil {
		t.Fatalf("rotate-secret outside the checkout: %v", err)
	}
	invs := readDump(t, dump)
	if len(invs) != 1 || invs[0].Verb != "rotate-secret" || !slices.Equal(invs[0].Args, []string{"--secret-file", outside}) || invs[0].Env["BRIGADE_PROFILE"] != fixtureProfile {
		t.Fatalf("dump = %+v, want one rotate-secret with the flag under the map's team key %q", invs, fixtureProfile)
	}

	// A secret where the path should be never reaches a child's argv.
	err = Team(sessionInv(t, f, top, "rotate-secret", "--secret-file", "brg1.t1.a-secret-not-a-path"))
	perr = wantCodeErr(t, err, protocol.CodeUsage, "")
	if strings.Contains(perr.Message, "brg1") || len(readDump(t, dump)) != 1 {
		t.Fatalf("poison: %q, spawns %d", perr.Message, len(readDump(t, dump)))
	}
}

// TestTeamRotateSecretInSessionBeforeTheFirstPrompt: right after an
// in-session create or join there is no map yet; the pass-through then
// takes the store from the start facts and the team from the cwd's pin,
// so `rotate-secret` works before the next prompt.
func TestTeamRotateSecretInSessionBeforeTheFirstPrompt(t *testing.T) {
	t.Parallel()
	f, dump := passThroughFixture(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"team rotate-secret": {{Result: []byte(`{"team_ref":"t_bob","team_name":"bobteam","secret_version":2}`)}},
		},
	})
	if err := (sessionmap.Store{StateDir: f.stateDir}).DeleteByPID(fixturePID); err != nil {
		t.Fatal(err)
	}
	writeStartFacts(t, f, f.dirs.BrigadeConfig) // where the fake adapter is registered
	top := mkCheckout(t, f.dirs.Root)
	canon, _ := teamfile.Canonicalize(top)
	key := teamstore.Key("fake", "https://abc.supabase.co", "t_bob")
	if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, key, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "fake",
		URL: "https://abc.supabase.co", PublishableKey: "k", TeamRef: "t_bob", TeamName: "bobteam",
	}); err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(f.dirs.BrigadeConfig, canon, teamstore.Pin{
		Adapter: "fake", URL: "https://abc.supabase.co", PublishableKey: "k", TeamRef: "t_bob",
	}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.dirs.Root, "rotated.secret")
	if err := Team(sessionInv(t, f, top, "rotate-secret", "--secret-file", outside)); err != nil {
		t.Fatalf("rotate-secret before the first prompt: %v", err)
	}
	invs := readDump(t, dump)
	if len(invs) != 1 || invs[0].Verb != "rotate-secret" || invs[0].Env["BRIGADE_PROFILE"] != key {
		t.Fatalf("dump = %+v, want one rotate-secret under the pinned team key %q", invs, key)
	}
}
