package adapterkit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

func sampleProfile() *adapterkit.Profile {
	return &adapterkit.Profile{
		Version:        adapterkit.ProfileVersion,
		Adapter:        "supabase",
		URL:            "https://ref.supabase.co",
		PublishableKey: "sb_publishable_x",
		TeamRef:        "team-1",
		TeamName:       "ops",
		PrincipalRef:   "principal-1",
		HumanLabel:     "alice@example.com",
		SecretStore:    adapterkit.SecretStoreFile,
		CreatedAt:      time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}
}

func TestProfileSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	want := sampleProfile()
	if err := adapterkit.SaveProfile(configDir, "default", want); err != nil {
		t.Fatal(err)
	}
	got, err := adapterkit.LoadProfile(configDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	gotNoTime, wantNoTime := *got, *want
	gotNoTime.CreatedAt, wantNoTime.CreatedAt = time.Time{}, time.Time{}
	if gotNoTime != wantNoTime {
		t.Fatalf("round trip changed the profile:\ngot  %+v\nwant %+v", got, want)
	}

	path := filepath.Join(configDir, "teams", "default", "team.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("profile is not at the 5.2 path: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("team.json mode = %o, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("profile directory mode = %o, want 0700", di.Mode().Perm())
	}

	// The written members are the 5.2 names, and no secret-shaped member
	// sneaks in.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{`"version"`, `"adapter"`, `"url"`, `"publishable_key"`, `"team_ref"`, `"team_name"`, `"principal_ref"`, `"human_label"`, `"secret_store"`, `"created_at"`} {
		if !strings.Contains(string(raw), member) {
			t.Fatalf("team.json lacks %s:\n%s", member, raw)
		}
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "join_secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("team.json carries a secret-shaped member %q:\n%s", forbidden, raw)
		}
	}
}

func expectConfigError(t *testing.T, err error, reason string) {
	t.Helper()
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeConfig {
		t.Fatalf("code = %q, want config", perr.Code)
	}
	if exit := perr.Code.Exit(); exit != 11 {
		t.Fatalf("exit = %d, want 11", exit)
	}
	if reason != "" && perr.Details["reason"] != reason {
		t.Fatalf("details = %v, want reason=%s", perr.Details, reason)
	}
}

func TestLoadProfileMissing(t *testing.T) {
	t.Parallel()
	_, err := adapterkit.LoadProfile(t.TempDir(), "default")
	expectConfigError(t, err, "profile_missing")
}

func TestLoadProfileWorldReadable(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	if err := adapterkit.SaveProfile(configDir, "default", sampleProfile()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "teams", "default", "team.json")
	//nolint:gosec // G302: making the profile world-readable is the PRECONDITION of this refusal test
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := adapterkit.LoadProfile(configDir, "default")
	expectConfigError(t, err, "insecure_mode")
}

func TestLoadProfileMalformedJSON(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "teams", "default")
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(filepath.Join(dir, "team.json"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	_, err := adapterkit.LoadProfile(configDir, "default")
	expectConfigError(t, err, "malformed_json")
}

func TestLoadProfileUnsupportedVersion(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "teams", "default")
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(filepath.Join(dir, "team.json"), []byte(`{"version":2,"adapter":"supabase"}`)); err != nil {
		t.Fatal(err)
	}
	_, err := adapterkit.LoadProfile(configDir, "default")
	expectConfigError(t, err, "unsupported_version")
}

func TestLoadProfileMissingAdapter(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "teams", "default")
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(filepath.Join(dir, "team.json"), []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	_, err := adapterkit.LoadProfile(configDir, "default")
	expectConfigError(t, err, "")
}

func TestCheckProfileName(t *testing.T) {
	t.Parallel()
	valid := []string{"default", "work", "a", "Team-2_x.y", strings.Repeat("a", 64)}
	for _, name := range valid {
		if err := adapterkit.CheckProfileName(name); err != nil {
			t.Fatalf("CheckProfileName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{
		"",
		".",
		"..",
		".hidden",
		"a/b",
		"../escape",
		"a b",
		"a\x00b",
		"a\\b",
		"pfx~",
		strings.Repeat("a", 65),
	}
	for _, name := range invalid {
		err := adapterkit.CheckProfileName(name)
		if err == nil {
			t.Fatalf("CheckProfileName(%q) accepted an invalid name", name)
		}
		expectConfigError(t, err, "invalid_profile_name")
	}
}

func TestProfilePathRejectsTraversal(t *testing.T) {
	t.Parallel()
	if _, err := adapterkit.ProfilePath("/cfg", "../../etc"); err == nil {
		t.Fatal("ProfilePath accepted a traversal name")
	}
	path, err := adapterkit.ProfilePath("/cfg", "default")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/cfg/teams/default/team.json" {
		t.Fatalf("path = %q, want /cfg/teams/default/team.json", path)
	}
}

func TestSaveProfileValidates(t *testing.T) {
	t.Parallel()
	bad := sampleProfile()
	bad.Adapter = ""
	if err := adapterkit.SaveProfile(t.TempDir(), "default", bad); err == nil {
		t.Fatal("SaveProfile wrote a profile that fails Validate")
	}
}

// TestProfilePathGolden pins the store layout: teams/<key>/team.json,
// with a 32-hex team key as the profile name (P7-3; reverting either
// constant fails here first).
func TestProfilePathGolden(t *testing.T) {
	t.Parallel()
	key := "0123456789abcdef0123456789abcdef"
	if err := adapterkit.CheckProfileName(key); err != nil {
		t.Fatalf("a 32-hex team key must be a legal profile name (the sentence that keeps protocol v1 frozen): %v", err)
	}
	got, err := adapterkit.ProfilePath("/cfg", key)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/cfg", "teams", key, "team.json")
	if got != want {
		t.Fatalf("ProfilePath = %q, want %q", got, want)
	}
}
