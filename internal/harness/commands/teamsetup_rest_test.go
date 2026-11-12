//go:build !windows

package commands

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
)

// A team on a Brigade server (work item 89): `--adapter rest`, the second
// bundled adapter. Its backend is a url and, for a certificate no
// authority signed, that certificate's pin; it has no publishable key.

const (
	serverURL = "https://brigade.example.com:8743"
	serverPin = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	otherPin  = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
)

// opensslSpelling is a pin as `openssl x509 -fingerprint -sha256` prints
// its digits.
func opensslSpelling(pin string) string {
	digits := strings.ToUpper(strings.TrimPrefix(pin, "sha256:"))
	var pairs []string
	for i := 0; i < len(digits); i += 2 {
		pairs = append(pairs, digits[i:i+2])
	}
	return strings.Join(pairs, ":")
}

// writeServerFile puts a server's team file at the toplevel, with the pin
// when one is given.
func writeServerFile(t *testing.T, top, pin string) {
	t.Helper()
	doc := `{"version":1,"adapter":"rest","url":"` + serverURL + `","team_ref":"` + setupTeamRef + `","team_name":"devs"`
	if pin != "" {
		doc += `,"certificate":"` + pin + `"`
	}
	//nolint:gosec // G306: a committed team file IS 0644
	if err := os.WriteFile(filepath.Join(top, teamfile.FileName), []byte(doc+"}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTeamCreateOnAServer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	secretFile := filepath.Join(f.dirs.Root, "team.secret")
	tokenFile := filepath.Join(f.dirs.Root, "create.token")
	f.rec.on("profile init", answer{result: `{"name":"x","state":"unauthenticated"}`})
	f.rec.on("team create", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_1"}`})

	iv := f.inv(f.terminalEnv(), "", "create", "--adapter", "rest", "--url", serverURL,
		"--certificate", opensslSpelling(serverPin), "--create-token-file", tokenFile,
		"--name", "devs", "--secret-file", secretFile)
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}

	// The committed file: the server, its pin in the stored spelling, and no key.
	path := filepath.Join(top, teamfile.FileName)
	parsed, err := teamfile.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Adapter != "rest" || parsed.URL != serverURL || parsed.Certificate != serverPin ||
		parsed.PublishableKey != "" || parsed.TeamRef != setupTeamRef || len(parsed.Ignored) != 0 {
		t.Fatalf("team file = %+v", parsed)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "publishable_key") {
		t.Fatalf("a server's team file carries a publishable_key: %s", raw)
	}
	// The binding and the pin carry the certificate with the url.
	key := teamstore.Key("rest", serverURL, setupTeamRef)
	b, err := teamstore.LoadBinding(f.dirs.BrigadeConfig, key)
	if err != nil {
		t.Fatal(err)
	}
	if b.Adapter != "rest" || b.URL != serverURL || b.Certificate != serverPin || b.PublishableKey != "" {
		t.Fatalf("binding = %+v", b)
	}
	canon, _ := teamfile.Canonicalize(top)
	pin, ok, err := teamstore.LookupPin(f.dirs.BrigadeConfig, canon)
	if err != nil || !ok || pin.Certificate != serverPin || pin.Adapter != "rest" || !pin.Matches(parsed) {
		t.Fatalf("pin = %+v, %v, %v", pin, ok, err)
	}
	// The bundled `rest` adapter was asked, with the url and the pin and no
	// key; the creation token went by its file's path, never by value.
	init := f.rec.spec(t, 0)
	if i := slices.Index(init.Argv, "adapter"); i < 0 || i+1 >= len(init.Argv) || init.Argv[i+1] != "rest" {
		t.Fatalf("profile init argv = %v, want this executable's `adapter rest`", init.Argv)
	}
	if got := argvOf(init); !strings.Contains(got, "--url "+serverURL+" --certificate "+serverPin) || strings.Contains(got, "--key") {
		t.Fatalf("profile init argv = %s, want the url and the pin and no key", got)
	}
	if got := argvOf(f.rec.spec(t, 1)); !strings.Contains(got, "--secret-file "+secretFile) ||
		!strings.Contains(got, "--create-token-file "+tokenFile) {
		t.Fatalf("team create argv = %s, want --secret-file and --create-token-file", got)
	}
}

func TestTeamCreateOnAServerWithNoPin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	f.rec.on("profile init", answer{result: `{"name":"x","state":"unauthenticated"}`})
	f.rec.on("team create", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_1"}`})
	iv := f.inv(f.terminalEnv(), "", "create", "--adapter", "rest", "--url", serverURL,
		"--name", "devs", "--secret-file", filepath.Join(f.dirs.Root, "team.secret"))
	iv.Deps.Getwd = func() (string, error) { return top, nil }
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(top, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "certificate") || strings.Contains(string(raw), "publishable_key") {
		t.Fatalf("team file = %s, want neither a pin nor a key", raw)
	}
	if got := argvOf(f.rec.spec(t, 0)); strings.Contains(got, "--certificate") {
		t.Fatalf("profile init argv = %s, want no pin", got)
	}
	if got := argvOf(f.rec.spec(t, 1)); strings.Contains(got, "--create-token-file") {
		t.Fatalf("team create argv = %s, want no creation token", got)
	}
}

func TestTeamCreateBackendFlagRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	secretFile := filepath.Join(f.dirs.Root, "team.secret")
	for name, args := range map[string][]string{
		"a server takes no key":          {"--adapter", "rest", "--url", serverURL, "--key", "k"},
		"a server needs its url":         {"--adapter", "rest"},
		"a pin that is not one":          {"--adapter", "rest", "--url", serverURL, "--certificate", "sha256:abc"},
		"a pin with an http url":         {"--adapter", "rest", "--url", "http://127.0.0.1:8743", "--certificate", serverPin},
		"a relative creation token file": {"--adapter", "rest", "--url", serverURL, "--create-token-file", "token"},
		"a pin for another adapter":      {"--url", "https://abc.supabase.co", "--key", "k", "--certificate", serverPin},
		"a token for another adapter":    {"--url", "https://abc.supabase.co", "--key", "k", "--create-token-file", "/tmp/t"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spawns := f.rec.count()
			all := append([]string{"create"}, args...)
			all = append(all, "--name", "devs", "--secret-file", secretFile)
			iv := f.inv(f.terminalEnv(), "", all...)
			iv.Deps.Getwd = func() (string, error) { return top, nil }
			wantCode(t, Team(iv), protocol.CodeUsage, "")
			if f.rec.count() != spawns {
				t.Fatal("a refused create spawned an adapter")
			}
		})
	}
}

func TestTeamJoinOnAServerSendsThePin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	writeServerFile(t, top, serverPin)
	f.rec.on("team join", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_2","rejoined":false}`})
	var prompts []string
	if err := Team(joinInv(f, top, setupSecret, true, &prompts)); err != nil {
		t.Fatal(err)
	}
	// The gate names the adapter and what the file asks this machine to trust.
	if len(prompts) != 1 || !strings.Contains(prompts[0], `via adapter "rest"`) ||
		!strings.Contains(prompts[0], "trusting the server certificate "+serverPin) ||
		!strings.Contains(prompts[0], "brigade.example.com:8743") {
		t.Fatalf("confirm gate = %q", prompts)
	}
	// The join's backend is the url and the pin, and no key.
	join := f.rec.spec(t, f.rec.count()-1)
	stdin := string(join.Stdin)
	if !strings.Contains(stdin, `"certificate":"`+serverPin+`"`) || !strings.Contains(stdin, `"url":"`+serverURL+`"`) ||
		strings.Contains(stdin, "publishable_key") {
		t.Fatalf("team join stdin = %s", strings.ReplaceAll(stdin, setupSecret, "<secret>"))
	}
	key := teamstore.Key("rest", serverURL, setupTeamRef)
	b, err := teamstore.LoadBinding(f.dirs.BrigadeConfig, key)
	if err != nil || b.Certificate != serverPin || b.URL != serverURL {
		t.Fatalf("binding after join = %+v, %v", b, err)
	}
	canon, _ := teamfile.Canonicalize(top)
	if pin, ok, _ := teamstore.LookupPin(f.dirs.BrigadeConfig, canon); !ok || pin.Certificate != serverPin {
		t.Fatalf("pin after join = %+v, %v", pin, ok)
	}
}

// A server's certificate was replaced and the team file names the new
// one: the member reviews the change and the profile moves to it, with no
// secret and without asking the server anything through the old pin.
func TestTeamJoinCertificateOnlyDriftNeedsNoSecret(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	canon, _ := teamfile.Canonicalize(top)
	if err := write.Pin(f.dirs.BrigadeConfig, canon, teamstore.Pin{
		Adapter: "rest", URL: serverURL, Certificate: otherPin, TeamRef: setupTeamRef,
	}); err != nil {
		t.Fatal(err)
	}
	key := teamstore.Key("rest", serverURL, setupTeamRef)
	if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, key, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "rest", URL: serverURL, Certificate: otherPin,
		TeamRef: setupTeamRef, PrincipalRef: "p_keep",
	}); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, top, serverPin)
	f.rec.on("profile init", answer{result: `{"name":"x","state":"joined"}`})

	var prompts []string
	iv := joinInv(f, top, "", true, &prompts)
	iv.Deps.ReadSecret = func() (string, error) {
		t.Fatal("a certificate-only drift must not ask for the secret")
		return "", nil
	}
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "server certificate: "+otherPin+" → "+serverPin) {
		t.Fatalf("output %q lacks the old and the new pin", out)
	}
	pin, ok, _ := teamstore.LookupPin(f.dirs.BrigadeConfig, canon)
	if !ok || pin.Certificate != serverPin {
		t.Fatalf("pin after the change = %+v", pin)
	}
	b, err := teamstore.LoadBinding(f.dirs.BrigadeConfig, key)
	if err != nil || b.Certificate != serverPin || b.PrincipalRef != "p_keep" {
		t.Fatalf("binding after the change = %+v, %v", b, err)
	}
	verbs := f.rec.verbs()
	if slices.Contains(verbs, "team join") || slices.Contains(verbs, "session list") {
		t.Fatalf("verbs = %v, want neither a join nor a question through the old pin", verbs)
	}
	init := argvOf(f.rec.spec(t, f.rec.count()-1))
	if !strings.Contains(init, "profile init --force --url "+serverURL+" --certificate "+serverPin) {
		t.Fatalf("profile init argv = %s, want --force with the url and the new pin", init)
	}
}

// A join that follows a dead credential still reaches a server whose pin
// changed: the profile is moved to the file's pin before the join is sent.
func TestTeamJoinAlignsAServersProfileBeforeAJoin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	top := mkCheckout(t, f.dirs.Root)
	key := teamstore.Key("rest", serverURL, setupTeamRef)
	if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, key, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "rest", URL: serverURL, Certificate: otherPin,
		TeamRef: "t_elsewhere", PrincipalRef: "p_keep",
	}); err != nil {
		t.Fatal(err)
	}
	writeServerFile(t, top, serverPin)
	f.rec.on("profile init", answer{result: `{"name":"x","state":"not_member"}`})
	f.rec.on("team join", answer{result: `{"team_ref":"` + setupTeamRef + `","team_name":"devs","principal_ref":"p_keep","rejoined":true}`})
	if err := Team(joinInv(f, top, setupSecret, true, nil)); err != nil {
		t.Fatal(err)
	}
	verbs := f.rec.verbs()
	init, join := slices.Index(verbs, "profile init"), slices.Index(verbs, "team join")
	if init < 0 || join < 0 || init > join {
		t.Fatalf("verbs = %v, want profile init before team join", verbs)
	}
	b, err := teamstore.LoadBinding(f.dirs.BrigadeConfig, key)
	if err != nil || b.Certificate != serverPin {
		t.Fatalf("binding after the join = %+v, %v", b, err)
	}
}
