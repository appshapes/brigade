//go:build !windows

package fs

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// A join secret of a team that lives elsewhere, put together here: no
// file of the repository holds the shape of one.
func elsewhereSecret(teamRef, half string) string {
	return strings.Join([]string{"brg1", teamRef, half}, ".")
}

// TestTakeInWritesATeamTheSecretJoins: the door writes one record, the
// team under the reference inside the secret with the secret's digest and
// no creator; a member joins with the secret it has; another secret for
// the same reference is refused as any wrong secret; and the adapter has
// no command of its own for any of it.
func TestTakeInWritesATeamTheSecretJoins(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	const ref = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f"
	secret := elsewhereSecret(ref, "kept-from-the-other-backend")

	taken, err := TakeIn(r.root, "ops", secret, r.now)
	if err != nil || !taken {
		t.Fatalf("TakeIn: taken %v, %v", taken, err)
	}
	st, err := openStore(r.root, r.clock())
	if err != nil {
		t.Fatal(err)
	}
	team, held, err := st.loadTeam(ref)
	st.close()
	if err != nil || !held {
		t.Fatalf("the team is not in the store: %v", err)
	}
	if team.TeamRef != ref || team.TeamName != "ops" || team.JoinSecretSHA26 != sha256hex(secret) || team.CreatedBy != "" || !team.CreatedAt.Equal(r.now) {
		t.Errorf("the record is %+v; want the reference, the name, the secret's digest, no creator and the time given", team)
	}
	raw, err := os.ReadFile(st.teamPath(ref)) //nolint:gosec // the test's own store
	if err != nil || strings.Contains(string(raw), "kept-from-the-other-backend") {
		t.Errorf("the record holds the secret itself, or cannot be read: %v", err)
	}
	if info, err := os.Lstat(st.teamPath(ref)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the record's mode: %v %v, want 0600", info, err)
	}

	// The team's members join with the secret they have.
	joined := r.join("alice", secret)
	if str(t, joined, "team_ref") != ref || str(t, joined, "team_name") != "ops" || joined["rejoined"] != false {
		t.Errorf("team join with the secret: %v", joined)
	}
	r.fails("unauthorized", 5, `{"join_secret":"`+elsewhereSecret(ref, "another-secret-for-the-reference")+`"}`,
		"--profile", "bob", "team", "join")

	// Again, under any name and any secret: the team that is there is left as it is.
	for _, again := range []string{secret, elsewhereSecret(ref, "another-secret-for-the-reference")} {
		taken, err := TakeIn(r.root, "renamed", again, r.now)
		if err != nil || taken {
			t.Errorf("TakeIn of a team that is in the store: taken %v, %v; want false and no error", taken, err)
		}
	}
	if got := str(t, r.ok("", "--profile", "alice", "profile", "status"), "team_name"); got != "ops" {
		t.Errorf("the team's name after a second TakeIn: %q", got)
	}

	// What is no join secret, and a reference the store cannot keep, are refused and write nothing.
	var perr *protocol.Error
	if _, err := TakeIn(r.root, "ops", "not-a-join-secret", r.now); !errors.As(err, &perr) || perr.Details["field"] != "join_secret" {
		t.Errorf("TakeIn of text that is no join secret: %v", err)
	}
	if _, err := TakeIn(r.root, "ops", elsewhereSecret("../escape", "half"), r.now); !errors.Is(err, ErrTeamRefUnstorable) {
		t.Errorf("TakeIn of a reference with a separator: %v, want ErrTeamRefUnstorable", err)
	}
	if _, err := TakeIn(r.root, "ops", elsewhereSecret("with.a.dot", "half"), r.now); !errors.Is(err, ErrTeamRefUnstorable) {
		t.Errorf("TakeIn of a reference with a dot: %v, want ErrTeamRefUnstorable", err)
	}

	// The adapter itself declines the capability and has no such verb.
	described := r.ok("", "describe")
	caps, _ := described["capabilities"].([]any)
	for _, c := range caps {
		if c == "team.take_in" {
			t.Errorf("describe advertises team.take_in")
		}
	}
	for _, verb := range []string{"take-in", "open-move", "close-move"} {
		r.fails("usage", 2, "", "--profile", "alice", "team", verb)
	}
}
