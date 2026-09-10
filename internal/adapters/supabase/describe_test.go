package supabase

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// newFailure renders a *protocol.Error the way fail does, for the
// byte-identity assertions.
func newFailure(t *testing.T, err *protocol.Error) string {
	t.Helper()
	var out bytes.Buffer
	adapterkit.WriteError(&out, err)
	return out.String()
}

// rewriteProfile edits team.json as a loose map, the way the
// conformance suite's default --rebind does.
func rewriteProfile(t *testing.T, r *rig, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(r.profileDir(), "team.json")
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
	for _, cap := range []string{"team.create", "team.join", "team.roster", "team.admin", "message.receive", "message.watch.push",
		"message.watch.stdin_commands", "session.description", "session.resume", "session.workspace_label", "session.inbound",
		"session.model", "session.context_used_tokens"} {
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
// team.json or session.json is `config`, never a silent state (U-10),
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
	if err := os.Chmod(filepath.Join(r.profileDir(), "team.json"), 0o640); err != nil { //nolint:gosec // G302: the group-readable file IS the case under test
		t.Fatal(err)
	}
	r.fails("config", 11, "", "describe")
	if got := entries(t, r.cfg); len(got) != len(before) {
		t.Fatalf("describe changed the directory: %v → %v", before, got)
	}
}

// TestDescribeMalformedProfileIsConfig: a team.json that does not
// parse, or carries an unknown version, is `config` (4.6).
func TestDescribeMalformedProfileIsConfig(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.initProfile()
	path := filepath.Join(r.profileDir(), "team.json")
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

// The retention drift join (P5-3, brief 3.5). `describe` answers
// protocol.DefaultRetention() from local files (TestDescribeStateMachine
// pins that) and the backend's gc_expired() enforces the same windows as
// SQL interval literals; nothing but a test joins the two. Rule 9 of the
// spec (docs/protocol-v1.md:706-708) makes unacked_message_seconds a
// FLOOR and the other two CEILINGS, so the safe directions are opposite
// and an inequality could only ever guard one side; equality is the one
// rule that catches drift in both — these numbers move together, in one
// commit. Each anchor must match exactly once per source: a vanished
// anchor is a loud failure, never a skip (the house shape of
// scripts/ci/proof_crash_resume_test.go), and a second match would make
// the join ambiguous. The anonymous-user migration repeats gc_expired()'s
// body byte for byte (migrations are append-only), so both files are
// joined; the live half in fixtures_integration_test.go runs the same
// regexps over pg_get_functiondef() of the DEPLOYED functions.
const (
	housekeepingMigrationRel = "supabase/migrations/20260830120200_brigade_housekeeping.sql"
	anonymousGCMigrationRel  = "supabase/migrations/20260905041134_anonymous_user_gc.sql"

	retentionAckedHoursAnchor  = `injected_at is not null and injected_at < now\(\) - interval '(\d+) hours'`
	retentionUnackedDaysAnchor = `or created_at < now\(\) - interval '(\d+) days'`
	retentionClosedDaysAnchor  = `closed_at is not null and closed_at < now\(\) - interval '(\d+) days'`
	retentionLeaseDaysAnchor   = `make_interval\(secs => lease_seconds\) < now\(\) - interval '(\d+) days'`
	anonymousUserDaysAnchor    = `u\.created_at < now\(\) - interval '(\d+) days'`

	// anonymousUserDays is D13's own window for the P5-3 rule — a plan
	// number, not a protocol field (BAP/1 is frozen and publishes no
	// anonymous-user retention, so `describe` cannot be joined to it), so
	// the only thing to assert is that nobody widened it silently: a wider
	// window weakens the rule that bounds auth.users on the hosted project.
	anonymousUserDays = 7
)

// sourceInterval reads the integer inside one retention literal of a SQL
// source — a migration file or a pg_get_functiondef() body — failing
// loudly unless the pattern matches exactly once.
func sourceInterval(t *testing.T, label, source, pattern string) int {
	t.Helper()
	all := regexp.MustCompile(pattern).FindAllStringSubmatch(source, -1)
	if len(all) != 1 {
		t.Fatalf("%s matches %q %d time(s), want exactly once: the retention drift join has lost its anchor", label, pattern, len(all))
	}
	n, err := strconv.Atoi(all[0][1])
	if err != nil {
		t.Fatalf("%s: %q is not an integer: %v", label, all[0][1], err)
	}
	return n
}

// retentionIn reads the three published windows out of one gc_expired()
// source, in seconds. The two session arms (closed_at and lease expiry)
// must agree with each other: describe publishes ONE
// closed_session_seconds for both.
func retentionIn(t *testing.T, label, source string) protocol.Retention {
	t.Helper()
	closed := sourceInterval(t, label, source, retentionClosedDaysAnchor)
	if lease := sourceInterval(t, label, source, retentionLeaseDaysAnchor); lease != closed {
		t.Errorf("%s: closed sessions go after %d days but lease-expired ones after %d; describe publishes one closed_session_seconds for both", label, closed, lease)
	}
	return protocol.Retention{
		AckedMessageSeconds:   sourceInterval(t, label, source, retentionAckedHoursAnchor) * 3600,
		UnackedMessageSeconds: sourceInterval(t, label, source, retentionUnackedDaysAnchor) * 86400,
		ClosedSessionSeconds:  closed * 86400,
	}
}

// repoFile reads one file under the repository root.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// describeRetention runs `describe` offline on a fresh rig and returns
// the retention it advertises — the adapter's actual answer, not the
// constant it is built from.
func describeRetention(t *testing.T) protocol.Retention {
	t.Helper()
	r := newRig(t)
	offline := append(append([]string{}, r.env...), "BRIGADE_TEST_OFFLINE=1")
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
	return d.Retention
}

// TestDescribeRetentionMatchesTheMigration is the offline half of the
// drift join: no stack, runs in `make test`. What describe advertises to
// a harness must equal what the migrations' gc_expired() enforces, in
// both files that define it, and the P5-3 window must still be D13's.
func TestDescribeRetentionMatchesTheMigration(t *testing.T) {
	t.Parallel()
	advertised := describeRetention(t)
	for _, rel := range []string{housekeepingMigrationRel, anonymousGCMigrationRel} {
		if enforced := retentionIn(t, rel, repoFile(t, rel)); enforced != advertised {
			t.Errorf("%s enforces %+v but describe advertises %+v: rule 9 makes unacked a floor and the other two ceilings, so the two must move together in one commit",
				rel, enforced, advertised)
		}
	}
	if days := sourceInterval(t, anonymousGCMigrationRel, repoFile(t, anonymousGCMigrationRel), anonymousUserDaysAnchor); days != anonymousUserDays {
		t.Errorf("gc_anonymous_users() reaps after %d days, want %d (D13): widening it silently would weaken the rule without saying so", days, anonymousUserDays)
	}
}
