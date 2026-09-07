package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The P7-5 roster: the rebuilt create/join against the spawn recorder,
// the secret gates, and the terminal resolution chain — every test
// written against the bridge-free paths so P7-7's bridge deletion
// cannot orphan them.

const (
	setupTeamRef = "t_9"
	setupSecret  = "brg1." + setupTeamRef + ".join-secret-value-123"
)

// mkCheckout creates a plain-clone checkout and returns its toplevel.
func mkCheckout(t *testing.T, root string) string {
	t.Helper()
	top := filepath.Join(root, "checkout")
	//nolint:gosec // G301: an ordinary checkout tree
	if err := os.MkdirAll(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return top
}

// writeCheckoutFile puts the fixture's .brigade.json at the toplevel:
// the bundled adapter, the fixture backend, team ` + "`t_9`" + `.
func writeCheckoutFile(t *testing.T, top string) {
	t.Helper()
	doc := `{"version":1,"adapter":"supabase","url":"https://abc.supabase.co","publishable_key":"sb_publishable_x","team_ref":"` + setupTeamRef + `","team_name":"devs"}`
	//nolint:gosec // G306: a committed team file IS 0644
	if err := os.WriteFile(filepath.Join(top, teamfile.FileName), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storeHash fingerprints the config dir tree so a declined gate can be
// shown to write nothing.
func storeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	var paths []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestTeamCreateWritesFileBindingAndPin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	secretFile := filepath.Join(f.dirs.Root, "team.secret")
	f.rec.on("profile init", answer{result: `{"profile":"x","initialized":true}`})
	f.rec.on("team create", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_1"}`})

	iv := f.inv(f.terminalEnv(), "", "create", "--url", "https://abc.supabase.co",
		"--key", "sb_publishable_x", "--name", "devs", "--secret-file", secretFile)
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	// The committed file: 0644, the six public fields, at the toplevel.
	path := filepath.Join(top, teamfile.FileName)
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("team file mode = %o, want 0644", fi.Mode().Perm())
	}
	parsed, err := teamfile.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TeamRef != setupTeamRef || parsed.Adapter != "supabase" {
		t.Fatalf("team file = %+v", parsed)
	}
	// The binding under the derived key, backend members patched.
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	b, err := teamstore.LoadBinding(f.dirs.BrigadeConfig, key)
	if err != nil {
		t.Fatal(err)
	}
	if b.URL != "https://abc.supabase.co" || b.PublishableKey != "sb_publishable_x" {
		t.Fatalf("binding backend not patched: %+v", b)
	}
	// The pin for this checkout.
	canon, _ := teamfile.Canonicalize(top)
	pin, ok, err := teamstore.LookupPin(f.dirs.BrigadeConfig, canon)
	if err != nil || !ok || pin.TeamRef != setupTeamRef {
		t.Fatalf("pin = %+v, %v, %v", pin, ok, err)
	}
	// The supabase dialect got the backend pair; create got --secret-file.
	if got := argvOf(f.rec.spec(t, 0)); !strings.Contains(got, "--url") || !strings.Contains(got, "--key") {
		t.Fatalf("profile init argv = %s, want the supabase backend pair", got)
	}
	if got := argvOf(f.rec.spec(t, 1)); !strings.Contains(got, "--secret-file "+secretFile) {
		t.Fatalf("team create argv = %s, want --secret-file", got)
	}
	// The human output ends with the honest next step.
	if out := f.out.String(); !strings.Contains(out, "git add "+teamfile.FileName) {
		t.Fatalf("output %q lacks the commit-and-push step", out)
	}
}

// argvOf joins a recorded spec's argv for substring asserts.
func argvOf(spec adapterkit.SpawnSpec) string { return strings.Join(spec.Argv, " ") }

func TestTeamCreateSecretFileRefusals(t *testing.T) { //nolint:tparallel // the subtests share one fixture and its spawn counter, in order
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	base := []string{"create", "--url", "https://x.co", "--key", "k", "--name", "n"}

	t.Run("missing is usage", func(t *testing.T) {
		iv := f.inv(f.terminalEnv(), "", base...)
		iv.Deps.Getwd = func() (string, error) { return top, nil }
		wantCode(t, Team(iv), protocol.CodeUsage, "")
	})
	t.Run("relative refuses before any work", func(t *testing.T) {
		iv := f.inv(f.terminalEnv(), "", append(base, "--secret-file", "team.secret")...)
		iv.Deps.Getwd = func() (string, error) { return top, nil }
		before := f.rec.count()
		wantCode(t, Team(iv), protocol.CodeUsage, "")
		if f.rec.count() != before {
			t.Fatal("a relative --secret-file must refuse before any spawn")
		}
	})
	t.Run("inside the toplevel refuses", func(t *testing.T) {
		iv := f.inv(f.terminalEnv(), "", append(base, "--secret-file", filepath.Join(top, "sub", "team.secret"))...)
		iv.Deps.Getwd = func() (string, error) { return top, nil }
		before := f.rec.count()
		wantCode(t, Team(iv), protocol.CodeUsage, "")
		if f.rec.count() != before {
			t.Fatal("an in-toplevel --secret-file must refuse before any spawn")
		}
	})
	t.Run("outside a checkout refuses", func(t *testing.T) {
		outside := filepath.Join(f.dirs.Root, "not-a-repo")
		//nolint:gosec // G301: an ordinary directory
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		iv := f.inv(f.terminalEnv(), "", append(base, "--secret-file", filepath.Join(f.dirs.Root, "s"))...)
		iv.Deps.Getwd = func() (string, error) { return outside, nil }
		wantCode(t, Team(iv), protocol.CodeConfig, "")
	})
}

func TestTeamCreateConflictAndForce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	writeCheckoutFile(t, top)
	args := []string{"create", "--url", "https://abc.supabase.co", "--key", "sb_publishable_x",
		"--name", "devs", "--secret-file", filepath.Join(f.dirs.Root, "s")}
	iv := f.inv(f.terminalEnv(), "", args...)
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	wantCode(t, Team(iv), protocol.CodeConflict, "")

	f.rec.on("profile init", answer{result: `{"ok":true}`})
	f.rec.on("team create", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_1"}`})
	iv = f.inv(f.terminalEnv(), "", append(args, "--force")...)
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	if err := Team(iv); err != nil {
		t.Fatalf("--force must replace the file: %v", err)
	}
}

// joinFixture prepares a checkout with a team file and the join answers.
func joinFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	writeCheckoutFile(t, top)
	f.rec.on("team join", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_2","rejoined":false}`})
	return f, top
}

// joinInv builds the TTY join invocation with recording seams.
func joinInv(f *fixture, top, secret string, confirmAnswer bool, prompts *[]string) Invocation {
	iv := f.inv(f.terminalEnv(), "", "join")
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	iv.Deps.IsTerminal = func(io.Reader) bool { return true }
	iv.Deps.ReadSecret = func() (string, error) { return secret, nil }
	iv.Deps.Confirm = func(prompt string) (bool, error) {
		if prompts != nil {
			*prompts = append(*prompts, prompt)
		}
		return confirmAnswer, nil
	}
	return iv
}

func TestTeamJoinFirstJoin(t *testing.T) {
	t.Parallel()
	f, top := joinFixture(t)
	var prompts []string
	if err := Team(joinInv(f, top, setupSecret, true, &prompts)); err != nil {
		t.Fatal(err)
	}
	// The gate showed name, ref and host from the file, before the secret.
	if len(prompts) != 1 || !strings.Contains(prompts[0], `"devs"`) ||
		!strings.Contains(prompts[0], setupTeamRef) || !strings.Contains(prompts[0], "abc.supabase.co") {
		t.Fatalf("confirm gate = %q", prompts)
	}
	// Binding patched and pin written.
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if _, err := teamstore.LoadBinding(f.dirs.BrigadeConfig, key); err != nil {
		t.Fatalf("binding after join: %v", err)
	}
	canon, _ := teamfile.Canonicalize(top)
	if _, ok, _ := teamstore.LookupPin(f.dirs.BrigadeConfig, canon); !ok {
		t.Fatal("no pin after join")
	}
	// The bundled adapter is NOT named in the gate.
	if strings.Contains(prompts[0], "via adapter") {
		t.Fatalf("the bundled adapter must not be named: %q", prompts[0])
	}
}

func TestTeamJoinDeclinedWritesNothing(t *testing.T) {
	t.Parallel()
	f, top := joinFixture(t)
	before := storeHash(t, f.dirs.BrigadeConfig)
	spawns := f.rec.count()
	wantCode(t, Team(joinInv(f, top, setupSecret, false, nil)), protocol.CodeUsage, "")
	if storeHash(t, f.dirs.BrigadeConfig) != before {
		t.Fatal("a declined gate wrote to the store")
	}
	if f.rec.count() != spawns {
		t.Fatal("a declined gate spawned an adapter")
	}
}

func TestTeamJoinWrongTeamSecretRefusedLocally(t *testing.T) {
	t.Parallel()
	f, top := joinFixture(t)
	spawns := f.rec.count()
	err := Team(joinInv(f, top, "brg1.t_other.join-secret-value-999", true, nil))
	wantCode(t, err, protocol.CodeInvalidInput, "")
	if f.rec.count() != spawns {
		t.Fatal("a wrong-team secret must refuse locally with zero spawns")
	}
}

func TestTeamJoinNonTTYNeverOpensTheFile(t *testing.T) {
	t.Parallel()
	// A file that would REFUSE if parsed AND cannot be read at all: the
	// stdin join (the scripted path, correction 7) must succeed through
	// the real pass-through regardless, which proves the non-TTY path
	// never opens the repo file.
	f, dump := passThroughFixture(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"team join": {{Result: []byte(`{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_2","rejoined":false}`)}},
		},
	})
	top := mkCheckout(t, f.dirs.Root)
	path := filepath.Join(top, teamfile.FileName)
	if err := os.WriteFile(path, []byte(`{"unknown_field":true}`), 0o000); err != nil {
		t.Fatal(err)
	}
	doc := `{"join_secret":"` + setupSecret + `"}`
	iv := f.inv(f.terminalEnv(), doc, "join", "--team", "t_bob")
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	iv.Deps.IsTerminal = func(io.Reader) bool { return false }
	iv.Deps.Spawn = nil // the real fake adapter, across the process boundary
	if err := Team(iv); err != nil {
		t.Fatalf("the stdin join must not consult the poisoned file: %v", err)
	}
	if invs := readDump(t, dump); len(invs) != 1 || invs[0].Verb != "join" {
		t.Fatalf("dump = %+v, want exactly the one join", invs)
	}
}

func TestTeamJoinCrossTeamRepointDemandsTheSecret(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	// The checkout was consented to team A; a PR re-points the file to
	// team B, which this user ALSO has a credential for.
	canon, _ := teamfile.Canonicalize(top)
	if err := write.Pin(f.dirs.BrigadeConfig, canon, teamstore.Pin{
		Adapter: "supabase", URL: "https://abc.supabase.co",
		PublishableKey: "sb_publishable_x", TeamRef: "t_A",
	}); err != nil {
		t.Fatal(err)
	}
	keyB := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, keyB, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase",
		URL: "https://abc.supabase.co", PublishableKey: "sb_publishable_x",
		TeamRef: setupTeamRef,
	}); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, top)
	f.rec.on("session list", answer{result: `{"sessions":[],"server_time":"2026-09-06T18:00:00Z"}`})
	f.rec.on("team join", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_2","rejoined":true}`})

	secretAsked := false
	iv := joinInv(f, top, setupSecret, true, nil)
	iv.Deps.ReadSecret = func() (string, error) { secretAsked = true; return setupSecret, nil }
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	if !secretAsked {
		t.Fatal("a cross-team re-point MUST demand the join secret (the high fix)")
	}
	pin, ok, _ := teamstore.LookupPin(f.dirs.BrigadeConfig, canon)
	if !ok || pin.TeamRef != setupTeamRef {
		t.Fatalf("pin after re-point = %+v", pin)
	}
}

func TestTeamJoinKeyOnlyDriftNeedsNoSecret(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	canon, _ := teamfile.Canonicalize(top)
	// Same team, rotated publishable key in the file.
	if err := write.Pin(f.dirs.BrigadeConfig, canon, teamstore.Pin{
		Adapter: "supabase", URL: "https://abc.supabase.co",
		PublishableKey: "sb_publishable_OLD", TeamRef: setupTeamRef,
	}); err != nil {
		t.Fatal(err)
	}
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, key, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase",
		URL: "https://abc.supabase.co", PublishableKey: "sb_publishable_OLD",
		TeamRef: setupTeamRef, PrincipalRef: "p_keep",
	}); err != nil {
		t.Fatal(err)
	}
	writeCheckoutFile(t, top)
	f.rec.on("session list", answer{result: `{"sessions":[],"server_time":"2026-09-06T18:00:00Z"}`})
	f.rec.on("profile init", answer{result: `{"ok":true}`})

	iv := joinInv(f, top, "", true, nil)
	iv.Deps.ReadSecret = func() (string, error) {
		t.Fatal("a key-only drift must not ask for the secret")
		return "", nil
	}
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	// Correction 1: pin AND binding both carry the new key; the
	// principal is untouched.
	pin, ok, _ := teamstore.LookupPin(f.dirs.BrigadeConfig, canon)
	if !ok || pin.PublishableKey != "sb_publishable_x" {
		t.Fatalf("pin after rotation = %+v", pin)
	}
	b, err := teamstore.LoadBinding(f.dirs.BrigadeConfig, key)
	if err != nil || b.PublishableKey != "sb_publishable_x" || b.PrincipalRef != "p_keep" {
		t.Fatalf("binding after rotation = %+v, %v", b, err)
	}
	// The supabase dialect drove profile init (the rotation rewrite)
	// and never team join.
	joined := false
	for _, v := range f.rec.verbs() {
		if v == "team join" {
			joined = true
		}
	}
	if joined {
		t.Fatal("a key-only drift must not re-join")
	}
}

func TestResolveTeamKeyPrecedence(t *testing.T) { //nolint:tparallel // the subtests build the store stepwise, in order
	f := newFixture(t)
	cfg := f.dirs.BrigadeConfig
	save := func(key, ref, name string) {
		t.Helper()
		if err := adapterkit.SaveProfile(cfg, key, &adapterkit.Profile{
			Version: adapterkit.ProfileVersion, Adapter: "supabase",
			URL: "https://abc.supabase.co", PublishableKey: "k",
			TeamRef: ref, TeamName: name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	keyA := teamstore.Key("supabase", "https://abc.supabase.co", "t_A")
	outside := filepath.Join(f.dirs.Root, "elsewhere")
	//nolint:gosec // G301: an ordinary directory
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("empty store answers the legacy default", func(t *testing.T) {
		got, err := resolveTeamKeyIn(cfg, "", outside)
		if err != nil || got != adapterkit.DefaultProfileName {
			t.Fatalf("= %q, %v", got, err)
		}
	})

	save(keyA, "t_A", "alpha")
	t.Run("a sole team is the fallback", func(t *testing.T) {
		got, err := resolveTeamKeyIn(cfg, "", outside)
		if err != nil || got != keyA {
			t.Fatalf("= %q, %v", got, err)
		}
	})

	keyB := teamstore.Key("supabase", "https://abc.supabase.co", "t_B")
	save(keyB, "t_B", "beta")
	t.Run("an ambiguous store refuses, never guesses", func(t *testing.T) {
		_, err := resolveTeamKeyIn(cfg, "", outside)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Details["reason"] != "team_ambiguous" {
			t.Fatalf("two teams, no pin, no --team: %v", err)
		}
	})
	t.Run("--team by ref beats the ambiguity", func(t *testing.T) {
		got, err := resolveTeamKeyIn(cfg, "t_B", outside)
		if err != nil || got != keyB {
			t.Fatalf("= %q, %v", got, err)
		}
	})
	t.Run("--team by name works and an unknown one refuses", func(t *testing.T) {
		if got, err := resolveTeamKeyIn(cfg, "alpha", outside); err != nil || got != keyA {
			t.Fatalf("= %q, %v", got, err)
		}
		_, err := resolveTeamKeyIn(cfg, "nosuch", outside)
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Details["reason"] != "team_unknown" {
			t.Fatalf("unknown --team: %v", err)
		}
	})

	top := mkCheckout(t, f.dirs.Root)
	canon, _ := teamfile.Canonicalize(top)
	if err := write.Pin(cfg, canon, teamstore.Pin{
		Adapter: "supabase", URL: "https://abc.supabase.co",
		PublishableKey: "k", TeamRef: "t_B",
	}); err != nil {
		t.Fatal(err)
	}
	t.Run("the pin governs in a checkout", func(t *testing.T) {
		got, err := resolveTeamKeyIn(cfg, "", top)
		if err != nil || got != keyB {
			t.Fatalf("= %q, %v", got, err)
		}
	})
	t.Run("--team beats the pin", func(t *testing.T) {
		got, err := resolveTeamKeyIn(cfg, "t_A", top)
		if err != nil || got != keyA {
			t.Fatalf("= %q, %v", got, err)
		}
	})
}

func TestTeamListRendersTheStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	key := teamstore.Key("supabase", "https://abc.supabase.co", setupTeamRef)
	if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, key, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase",
		URL: "https://abc.supabase.co", PublishableKey: "k",
		TeamRef: setupTeamRef, TeamName: "devs", PrincipalRef: "p_1",
	}); err != nil {
		t.Fatal(err)
	}
	iv := f.inv(f.terminalEnv(), "", "list")
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{"devs", setupTeamRef, "abc.supabase.co", "key=" + key} {
		if !strings.Contains(out, want) {
			t.Fatalf("team list output %q lacks %q", out, want)
		}
	}
}
