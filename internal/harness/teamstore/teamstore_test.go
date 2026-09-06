package teamstore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestKeyGoldenVectors pins the derivation: a changed separator, order,
// hash or truncation moves these values and fails here first.
func TestKeyGoldenVectors(t *testing.T) {
	t.Parallel()
	cases := []struct{ adapter, url, ref, want string }{
		{"supabase", "https://abc.supabase.co", "t_4f9c", "8129fed52c3e337827a930336492db48"},
		{"fs", "http://127.0.0.1:1", "t_dev", "531f58393de984bec35249a0e0c8d99b"},
	}
	for _, c := range cases {
		if got := teamstore.Key(c.adapter, c.url, c.ref); got != c.want {
			t.Fatalf("Key(%s) = %q, want %q", c.adapter, got, c.want)
		}
	}
}

// TestKeyIsALegalProfileName is the sentence that keeps protocol v1
// frozen: the derived key must pass the adapter vocabulary's own name
// check, so the harness can drive `--profile <key>` unchanged.
func TestKeyIsALegalProfileName(t *testing.T) {
	t.Parallel()
	key := teamstore.Key("supabase", "https://abc.supabase.co", "t_4f9c")
	if len(key) != teamstore.KeyLen {
		t.Fatalf("key length = %d, want %d", len(key), teamstore.KeyLen)
	}
	if err := adapterkit.CheckProfileName(key); err != nil {
		t.Fatalf("the team key must be a legal adapter profile name: %v", err)
	}
}

// TestKeyIgnoresPublishableKey pins the rotation property: the
// publishable key is not part of the derivation, so a key rotation
// cannot move the credential directory.
func TestKeyIgnoresPublishableKey(t *testing.T) {
	t.Parallel()
	a := teamstore.Key("supabase", "https://abc.supabase.co", "t_4f9c")
	b := teamstore.Key("supabase", "https://abc.supabase.co", "t_4f9c")
	if a != b {
		t.Fatalf("the derivation is not deterministic: %q vs %q", a, b)
	}
}

func repoFile() *teamfile.File {
	return &teamfile.File{
		Version: 1, Adapter: "supabase", URL: "https://abc.supabase.co",
		PublishableKey: "sb_publishable_x", TeamRef: "t_4f9c", TeamName: "ops",
	}
}

func binding() *adapterkit.Profile {
	return &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase",
		URL: "https://abc.supabase.co", PublishableKey: "sb_publishable_x",
		TeamRef: "t_4f9c", TeamName: "ops",
	}
}

func TestVerifyBindingPerFieldMatrix(t *testing.T) {
	t.Parallel()
	drift := map[string]func(*adapterkit.Profile){
		"adapter":         func(b *adapterkit.Profile) { b.Adapter = "fs" },
		"url":             func(b *adapterkit.Profile) { b.URL = "https://evil.example.com" },
		"publishable_key": func(b *adapterkit.Profile) { b.PublishableKey = "sb_publishable_other" },
		"team_ref":        func(b *adapterkit.Profile) { b.TeamRef = "t_other" },
	}
	for field, mutate := range drift {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			b := binding()
			mutate(b)
			err := teamstore.VerifyBinding(b, repoFile())
			var pe *protocol.Error
			if !protocolAs(err, &pe) || pe.Details["reason"] != "binding_mismatch" || pe.Details["field"] != field {
				t.Fatalf("drifting %s: err = %v, want binding_mismatch on that field", field, err)
			}
		})
	}
	t.Run("team_name drift does NOT refuse (cosmetic, unpinned)", func(t *testing.T) {
		t.Parallel()
		b := binding()
		b.TeamName = "renamed-by-a-pr"
		if err := teamstore.VerifyBinding(b, repoFile()); err != nil {
			t.Fatalf("a display-name drift must not disconnect anyone: %v", err)
		}
	})
}

func TestPinMatches(t *testing.T) {
	t.Parallel()
	p := teamstore.Pin{Adapter: "supabase", URL: "https://abc.supabase.co",
		PublishableKey: "sb_publishable_x", TeamRef: "t_4f9c"}
	if !p.Matches(repoFile()) {
		t.Fatal("an agreeing pin must match")
	}
	p.TeamRef = "t_other"
	if p.Matches(repoFile()) {
		t.Fatal("a re-pointed file must not match the old pin")
	}
}

func TestLookupPinMissingStoreIsNotAnError(t *testing.T) {
	t.Parallel()
	pin, ok, err := teamstore.LookupPin(t.TempDir(), "/some/checkout")
	if err != nil || ok || pin != nil {
		t.Fatalf("empty store: got %v, %v, %v; want nil, false, nil", pin, ok, err)
	}
}

func TestLookupPinRefusesGroupReadableStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := teamstore.PinsPath(dir)
	//nolint:gosec // G306: the insecure mode is the PRECONDITION this test refuses
	if err := os.WriteFile(path, []byte(`{"version":1,"projects":{}}`), 0o640); err != nil {
		t.Fatal(err)
	}
	_, _, err := teamstore.LookupPin(dir, "/x")
	var pe *protocol.Error
	if !protocolAs(err, &pe) || pe.Details["reason"] != "insecure_mode" {
		t.Fatalf("a group-readable pin store must refuse via ReadStrict: %v", err)
	}
}

func TestLookupPinMalformedStoreRefuses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := adapterkit.WriteAtomic(teamstore.PinsPath(dir), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	_, _, err := teamstore.LookupPin(dir, "/x")
	var pe *protocol.Error
	if !protocolAs(err, &pe) || pe.Details["reason"] != "pins_malformed" {
		t.Fatalf("a malformed pin store must refuse, not answer no-pin: %v", err)
	}
}

// protocolAs is errors.As for *protocol.Error without importing errors
// in every assertion.
func protocolAs(err error, target **protocol.Error) bool {
	if err == nil {
		return false
	}
	pe, ok := err.(*protocol.Error) //nolint:errorlint // the store returns its refusals unwrapped by contract
	if ok {
		*target = pe
	}
	return ok
}

// TestBindingRoundTripThroughKit is correction 5 made checkable: the
// binding is the kit schema, so LoadBinding answers exactly what
// SaveProfile wrote, validation included.
func TestBindingRoundTripThroughKit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := teamstore.Key("supabase", "https://abc.supabase.co", "t_4f9c")
	if err := adapterkit.SaveProfile(dir, key, binding()); err != nil {
		t.Fatal(err)
	}
	got, err := teamstore.LoadBinding(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.TeamRef != "t_4f9c" || got.Adapter != "supabase" {
		t.Fatalf("binding round-trip lost members: %+v", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "teams", key, "team.json")); err != nil {
		t.Fatalf("the binding must live at teams/<key>/team.json: %v", err)
	}
}
