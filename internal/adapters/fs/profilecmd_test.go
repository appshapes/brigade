package fs

import (
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestProfileInitIsConflictWithoutForce covers 4.2: `profile init` on a
// configured profile is `conflict` unless the adapter's own --force is
// given, and --force mints a NEW principal.
func TestProfileInitIsConflictWithoutForce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	first := str(t, r.ok("", "--profile", "alice", "profile", "init"), "principal_ref")

	got := r.fails("conflict", 7, "", "--profile", "alice", "profile", "init")
	object, _ := decode(t, got.stdout)["error"].(map[string]any)
	details, _ := object["details"].(map[string]any)
	if details["reason"] != reasonProfileExists {
		t.Fatalf("details = %v, want reason %q", details, reasonProfileExists)
	}

	second := str(t, r.ok("", "--profile", "alice", "profile", "init", "--force"), "principal_ref")
	if second == first {
		t.Fatal("--force kept the old principal; it must mint a new one")
	}
}

// TestProfileFilesAre0600 pins the file modes of 3.2: a credential that
// anyone could read is a credential already leaked (U-10).
func TestProfileFilesAre0600(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.ok("", "--profile", "alice", "profile", "init")
	for _, name := range []string{"team.json", credentialFileName} {
		path := filepath.Join(r.cfg, "teams", "alice", name)
		info, err := lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
}

// TestProfileStatusNeverCarriesASecret covers 4.2 and 4.5.14.
func TestProfileStatusNeverCarriesASecret(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	got := r.exec("", "--profile", "alice", "profile", "status")
	if got.code != 0 {
		t.Fatalf("exit %d", got.code)
	}
	if contains(got.stdout, secret) || contains(got.stdout, protocol.JoinSecretPrefix) {
		t.Fatalf("profile status leaked a secret: %s", got.stdout)
	}
	result, _ := decode(t, got.stdout)["result"].(map[string]any)
	if str(t, result, "state") != protocol.ProfileStateJoined {
		t.Fatalf("state = %v", result)
	}
}

// TestProfileResetRevokesAndDeletes: the profile directory is gone, the
// membership is revoked and the principal's sessions are closed, so a
// leaked copy of the credential file buys nothing.
func TestProfileResetRevokesAndDeletes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	alice := r.register("alice", "alice-1")
	r.join("bob", secret)
	bobSession := r.register("bob", "bob-1")

	r.ok("", "--profile", "alice", "profile", "reset")
	absent(t, filepath.Join(r.cfg, "teams", "alice"))

	// Bob still works, and Alice's sessions have vanished from his view.
	result := r.ok("", "--profile", "bob", "session", "list", "--include-offline")
	sessions, _ := result["sessions"].([]any)
	for _, s := range sessions {
		entry, _ := s.(map[string]any)
		if str(t, entry, "session_id") == alice {
			t.Fatal("a reset principal's session is still listed")
		}
	}
	if len(sessions) != 1 || str(t, sessions[0].(map[string]any), "session_id") != bobSession {
		t.Fatalf("bob's own session is missing: %v", sessions)
	}
	if teamRef == "" {
		t.Fatal("no team ref")
	}
}

// TestRevokeCredentialsThenRejoinKeepsThePrincipal covers 4.2: `profile
// revoke-credentials` leaves the profile and its binding in place, and a
// later `team join` with the bound team's secret restores the SAME
// principal (the rejoin of C-08).
func TestRevokeCredentialsThenRejoinKeepsThePrincipal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	before := str(t, r.ok("", "--profile", "alice", "profile", "status"), "principal_ref")

	r.ok("", "--profile", "alice", "profile", "revoke-credentials")
	r.fails("unauthenticated", 4, "", "--profile", "alice", "session", "list")

	result := r.join("alice", secret)
	if str(t, result, "principal_ref") != before {
		t.Fatalf("principal changed: %v, want %q", result, before)
	}
	if rejoined, _ := result["rejoined"].(bool); !rejoined {
		t.Fatalf("rejoined = false, want true: %v", result)
	}
}
