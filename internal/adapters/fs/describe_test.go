package fs

import (
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestDescribeCreatesNothing covers C-01: `describe` on a scratch
// principal answers ok with state unconfigured, exit 0, and leaves NO file
// and NO directory behind — neither the store root nor the configuration
// directory.
func TestDescribeCreatesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	result := r.ok("", "describe")
	if got := str(t, result, "protocol_version"); got != protocol.ProtocolVersion {
		t.Fatalf("protocol_version = %q", got)
	}
	profile, _ := result["profile"].(map[string]any)
	if got := str(t, profile, "state"); got != protocol.ProfileStateUnconfigured {
		t.Fatalf("state = %q, want unconfigured", got)
	}
	for _, key := range []string{"team_ref", "team_name", "principal_ref", "human_label"} {
		if _, present := profile[key]; present {
			t.Fatalf("profile.%s is present in state unconfigured: %v", key, profile)
		}
	}
	absent(t, r.root)
	absent(t, r.cfg)
}

// TestDescribeStateMachine covers C-07: unconfigured → unauthenticated →
// not_member → joined, computed from local files only.
func TestDescribeStateMachine(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	state := func() string {
		t.Helper()
		profile, _ := r.ok("", "--profile", "alice", "describe")["profile"].(map[string]any)
		return str(t, profile, "state")
	}
	if got := state(); got != protocol.ProfileStateUnconfigured {
		t.Fatalf("fresh profile: %q", got)
	}
	r.ok("", "--profile", "alice", "profile", "init")
	if got := state(); got != protocol.ProfileStateNotMember {
		t.Fatalf("after init: %q", got)
	}
	r.ok("", "--profile", "alice", "profile", "revoke-credentials")
	if got := state(); got != protocol.ProfileStateUnauthenticated {
		t.Fatalf("after revoke-credentials: %q", got)
	}
	r.ok("", "--profile", "alice", "profile", "init", "--force")
	r.team("alice", "ops")
	if got := state(); got != protocol.ProfileStateJoined {
		t.Fatalf("after team create: %q", got)
	}
	profile, _ := r.ok("", "--profile", "alice", "describe")["profile"].(map[string]any)
	for _, key := range []string{"team_ref", "team_name", "principal_ref", "human_label"} {
		if str(t, profile, key) == "" {
			t.Fatalf("profile.%s is empty in state joined: %v", key, profile)
		}
	}
}

// TestDescribeAdvertisesTheProtocolConstants covers C-18, and the one
// deviation this adapter is allowed: lease.min_seconds is 1, not the
// 4.4.1 example's 30, so lease expiry is testable in seconds.
func TestDescribeAdvertisesTheProtocolConstants(t *testing.T) {
	t.Parallel()
	result := newRig(t).ok("", "describe")
	capabilities, _ := result["capabilities"].([]any)
	found := false
	for _, c := range capabilities {
		if c == "message.receive" {
			found = true
		}
		if c == "message.watch.push" {
			t.Fatal("message.watch.push is advertised, but the watch polls (C-33)")
		}
	}
	if !found {
		t.Fatalf("capabilities lack message.receive: %v", capabilities)
	}
	lease, _ := result["lease"].(map[string]any)
	if lease["min_seconds"] != float64(1) || lease["default_seconds"] != float64(90) ||
		lease["max_seconds"] != float64(600) {
		t.Fatalf("lease = %v", lease)
	}
	limits, _ := result["limits"].(map[string]any)
	for _, key := range []string{
		"max_body_bytes", "max_summary_chars", "max_session_name_codepoints",
		"max_team_name_codepoints", "max_description_chars", "max_human_label_chars",
		"max_workspace_label_chars", "max_idempotency_key_chars", "max_unacked_per_recipient",
		"max_unacked_per_sender_recipient", "max_hop_count", "implicit_reply_window_seconds",
	} {
		if value, _ := limits[key].(float64); value <= 0 {
			t.Fatalf("limits.%s = %v, want a positive number", key, limits[key])
		}
	}
	retention, _ := result["retention"].(map[string]any)
	if len(retention) != 3 {
		t.Fatalf("retention = %v", retention)
	}
}

// TestDescribeRefusesAWorldReadableProfile: only ABSENCE is
// `unconfigured`; a profile the adapter cannot honour is `config`, never a
// silent fresh start (4.6, U-10).
func TestDescribeRefusesAWorldReadableProfile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.ok("", "--profile", "alice", "profile", "init")
	path := filepath.Join(r.cfg, "teams", "alice", "team.json")
	if err := chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	r.fails("config", 11, "", "--profile", "alice", "describe")
}
