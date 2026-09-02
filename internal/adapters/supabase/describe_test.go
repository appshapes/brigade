package supabase

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// newFailure renders a *protocol.Error the way fail does, for the
// byte-identity assertions.
func newFailure(t *testing.T, err *protocol.Error) string {
	t.Helper()
	var out bytes.Buffer
	adapterkit.WriteError(&out, err)
	return out.String()
}

// rewriteProfile edits profile.json as a loose map, the way the
// conformance suite's default --rebind does.
func rewriteProfile(t *testing.T, r *rig, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(r.profileDir(), "profile.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	edit(raw)
	next, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, next, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDescribeStateMachine walks profile.state from local files only
// (4.4.1, C-07): unconfigured → unauthenticated (profile, no session.json;
// then an unusable one) → not_member → joined, with the four team members
// present only when joined, and validates every result as a
// DescribeResult.
func TestDescribeStateMachine(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	offline := append(append([]string{}, r.env...), "BRIGADE_TEST_OFFLINE=1")
	describe := func(want string) *protocol.DescribeResult {
		t.Helper()
		got := r.execEnv(offline, "", "describe")
		if got.code != 0 {
			t.Fatalf("describe: exit %d, %s", got.code, got.stdout)
		}
		var env protocol.Envelope
		if err := protocol.Decode([]byte(got.stdout), &env); err != nil {
			t.Fatalf("describe envelope: %v", err)
		}
		var d protocol.DescribeResult
		if err := protocol.Decode(env.Result, &d); err != nil {
			t.Fatalf("describe result: %v", err)
		}
		if d.Profile.State != want {
			t.Fatalf("profile.state = %q, want %q", d.Profile.State, want)
		}
		if want != protocol.ProfileStateJoined && (d.Profile.TeamRef != "" || d.Profile.TeamName != "" || d.Profile.PrincipalRef != "" || d.Profile.HumanLabel != "") {
			t.Fatalf("profile carries team members while %s: %+v", want, d.Profile)
		}
		return &d
	}
	d := describe(protocol.ProfileStateUnconfigured)
	if d.ProtocolVersion != protocol.ProtocolVersion || d.Adapter.Name != adapterName {
		t.Fatalf("describe: %+v", d)
	}
	if d.Lease != protocol.DefaultLease() || d.Limits != protocol.DefaultLimits() || d.Retention != protocol.DefaultRetention() {
		t.Fatalf("describe advertises other than the protocol's own limits, lease and retention")
	}
	for _, cap := range []string{"team.create", "team.join", "team.roster", "message.receive", "message.watch.push",
		"message.watch.stdin_commands", "session.description", "session.resume", "session.workspace_label", "session.inbound"} {
		found := false
		for _, have := range d.Capabilities {
			found = found || have == cap
		}
		if !found {
			t.Errorf("capability %s is not advertised", cap)
		}
	}
	if d.Delivery.Ordering != "none" || d.Delivery.Guarantee != protocol.GuaranteeAtLeastOnce || d.Delivery.AckState != protocol.AckStateInjected {
		t.Fatalf("delivery = %+v", d.Delivery)
	}
	if got := entries(t, r.cfg); len(got) != 0 {
		t.Fatalf("describe created %v", got)
	}

	r.initProfile()
	describe(protocol.ProfileStateUnauthenticated)
	if err := os.WriteFile(r.sessionPath(), []byte(`{"access_token":"not-a-jwt","refresh_token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	describe(protocol.ProfileStateUnauthenticated)
	if err := os.WriteFile(r.sessionPath(), []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	describe(protocol.ProfileStateUnauthenticated)

	r.writeSession(r.session(time.Hour, "rt-1"))
	describe(protocol.ProfileStateNotMember)

	r.bindTeam(testTeamID, "ops")
	d = describe(protocol.ProfileStateJoined)
	if d.Profile.TeamRef != testTeamID || d.Profile.TeamName != "ops" || d.Profile.PrincipalRef != testUserID || d.Profile.HumanLabel != "alice@example.com" {
		t.Fatalf("joined profile = %+v", d.Profile)
	}
	// Without the offline switch as well: the switch short-circuits every
	// dial inside the client, so only a run WITHOUT it can prove that
	// describe makes no request of the (fake) backend at all.
	if got := r.exec("", "describe"); got.code != 0 {
		t.Fatalf("describe without the offline switch: exit %d, %s", got.code, got.stdout)
	}
	if r.be.total() != 0 {
		t.Fatalf("describe dialled the backend %d times", r.be.total())
	}
}

// TestDescribeRefusesInsecureFiles: a group- or world-readable
// profile.json or session.json is `config`, never a silent state (U-10),
// and describe still creates nothing.
func TestDescribeRefusesInsecureFiles(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	before := entries(t, r.cfg)
	if err := os.Chmod(r.sessionPath(), 0o644); err != nil { //nolint:gosec // G302: the world-readable file IS the case under test
		t.Fatal(err)
	}
	r.fails("config", 11, "", "describe")
	r.fails("config", 11, "", "session", "list")
	if err := os.Chmod(r.sessionPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(r.profileDir(), "profile.json"), 0o640); err != nil { //nolint:gosec // G302: the group-readable file IS the case under test
		t.Fatal(err)
	}
	r.fails("config", 11, "", "describe")
	if got := entries(t, r.cfg); len(got) != len(before) {
		t.Fatalf("describe changed the directory: %v → %v", before, got)
	}
}

// TestDescribeMalformedProfileIsConfig: a profile.json that does not
// parse, or carries an unknown version, is `config` (4.6).
func TestDescribeMalformedProfileIsConfig(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	path := filepath.Join(r.profileDir(), "profile.json")
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := r.fails("config", 11, "", "describe")
	if details(t, got.stdout)["reason"] != "malformed_json" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"adapter":"supabase"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got = r.fails("config", 11, "", "describe")
	if details(t, got.stdout)["reason"] != "unsupported_version" {
		t.Fatalf("details = %v", details(t, got.stdout))
	}
}

// TestDescribeNeedsNoEnvironmentButHome: with only HOME set, describe
// answers from $HOME/.config/brigade and creates nothing there (4.1).
func TestDescribeNeedsNoEnvironmentButHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	r := newRig(t)
	got := r.execEnv([]string{"HOME=" + home}, "", "describe")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	absent(t, filepath.Join(home, ".config"))
	got = r.execEnv([]string{"BRIGADE_CONFIG_DIR=relative/path"}, "", "describe")
	if got.code != 11 {
		t.Fatalf("relative BRIGADE_CONFIG_DIR: exit %d, want 11", got.code)
	}
}
