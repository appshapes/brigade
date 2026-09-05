package conformance

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestSuiteWallClockBudgetFitsTheDefaultLeaseRange is the drift join on
// the constants: the budget the suite promises must be grantable as a
// lease, and it must be well past the protocol default that the fixture
// used to take. Raising SuiteWallClockBudget past the top of the 4.4.1
// default range fails HERE — not months later as one case failing in one
// shuffled order against one slow backend, which is how P5-15 was found.
//
// The Supabase side has a fourth join the suite cannot assert from Go:
// supabase/migrations pins `check (lease_seconds between 30 and 600)`, so
// a budget the schema cannot grant fails the fixture build outright, as a
// launcher error on the first integration run.
func TestSuiteWallClockBudgetFitsTheDefaultLeaseRange(t *testing.T) {
	t.Parallel()
	budget := int(SuiteWallClockBudget / time.Second)
	if SuiteWallClockBudget%time.Second != 0 {
		t.Errorf("SuiteWallClockBudget is %s; lease_seconds is an integer number of seconds (4.4.2)", SuiteWallClockBudget)
	}
	if budget > protocol.LeaseMaxSeconds {
		t.Errorf("SuiteWallClockBudget is %s but the 4.4.1 default range tops out at %d s: no adapter on that range can grant the fixture a lease that covers a whole run, so the fixture would have to heartbeat",
			SuiteWallClockBudget, protocol.LeaseMaxSeconds)
	}
	if budget <= protocol.LeaseDefaultSeconds {
		t.Errorf("SuiteWallClockBudget is %s, no longer than lease.default_seconds (%d s) — the default is exactly what expired mid-run in P5-15",
			SuiteWallClockBudget, protocol.LeaseDefaultSeconds)
	}
	if got := protocol.DefaultLease().MaxSeconds; got < budget {
		t.Errorf("DefaultLease().MaxSeconds is %d s, under the suite's %s budget; the Supabase adapter advertises exactly this range", got, SuiteWallClockBudget)
	}
}

// describeWithLease is fakeDescribe with a different advertised lease
// range. The two forms are marshalled from protocol.Lease itself, so the
// substitution cannot drift with the wire encoding, and a substitution
// that matched nothing is a test failure rather than a silent default.
func describeWithLease(tb testing.TB, lease protocol.Lease, capabilities ...string) string {
	tb.Helper()
	from, err := json.Marshal(protocol.DefaultLease())
	if err != nil {
		tb.Fatal(err)
	}
	to, err := json.Marshal(&lease)
	if err != nil {
		tb.Fatal(err)
	}
	doc := fakeDescribe(tb, capabilities...)
	out := strings.Replace(doc, `"lease":`+string(from), `"lease":`+string(to), 1)
	if out == doc {
		tb.Fatalf("describeWithLease: %s is not in the fake describe document", `"lease":`+string(from))
	}
	return out
}

// leaseFake is an adapter that advertises lease and, once --setup has
// written its marker, a joined profile: enough for the fixture to build
// and register its three sessions. It is the shape
// TestRunSetupProvisionsTheFixture uses, with the lease range as the
// subject. Every registration document it is handed is kept at
// <config>/registration.json, which is how the tests read what the fixture
// asked for.
func leaseFake(tb testing.TB, lease protocol.Lease) (adapter, setup string) {
	tb.Helper()
	unconfigured := describeWithLease(tb, lease)
	joined := strings.Replace(unconfigured, `"profile":{"name":"default","state":"unconfigured"}`,
		`"profile":{"name":"default","state":"joined","team_ref":"'"$(cat "$BRIGADE_CONFIG_DIR/team")"'","team_name":"ops","principal_ref":"'"$BRIGADE_CONFIG_DIR"'","human_label":"x@example.com"}`, 1)
	if joined == unconfigured {
		tb.Fatal("leaseFake: the profile member is not in the fake describe document")
	}
	record := `{"ok":true,"protocol_version":"1","result":{"session_id":"'"$BRIGADE_CONFIG_DIR"'","session_name":"fixture","principal_ref":"p","state":"active","activity":"busy","inbound":"accept","last_seen_at":"2026-08-30T12:00:00Z","lease_until":"2026-08-30T12:01:30Z","created_at":"2026-08-30T12:00:00Z","is_self":false,"resumed":false,"lease_seconds":90,"server_time":"2026-08-30T12:00:00Z"}}`
	adapter = writeScript(tb, "lease-adapter", `
case "$1 $2" in
  "describe ") if [ -f "$BRIGADE_CONFIG_DIR/team" ]; then printf '%s\n' '`+joined+`'; else printf '%s\n' '`+unconfigured+`'; fi ;;
  "session register") cat > "$BRIGADE_CONFIG_DIR/registration.json"; printf '%s\n' '`+record+`' ;;
  *) printf '%s\n' '`+usageEnvelope+`'; exit 2 ;;
esac
`)
	setup = writeScript(tb, "lease-setup", `
case "$BRIGADE_CONFIG_DIR" in
  */principals/c/*) printf t2 > "$BRIGADE_CONFIG_DIR/team" ;;
  *) printf t1 > "$BRIGADE_CONFIG_DIR/team" ;;
esac
`)
	return adapter, setup
}

// TestRunRefusesARunThatOutlivedTheFixtureLease covers the half of the
// P5-15 drift join no unit test can reach: not a shorter lease, but a
// LONGER suite. Nothing heartbeats A, B and C, so a run that goes on past
// the lease its fixture was granted has been listing them `offline` for
// part of its length (4.5.8) and its results — green or red — mean
// nothing. That is a launcher error (exit 3), reported with the numbers,
// rather than one puzzling case failure in one case order.
//
// The adapter here advertises a 1 s maximum, so the fixture is granted 1 s
// and a case that sleeps 2 s outlives it; the control is the same adapter
// with no sleep.
func TestRunRefusesARunThatOutlivedTheFixtureLease(t *testing.T) {
	t.Parallel()
	adapter, setup := leaseFake(t, protocol.Lease{DefaultSeconds: 1, MinSeconds: 1, MaxSeconds: 1})
	opts := Options{Adapter: adapter, Setup: setup}

	slow := []Case{{ID: "C-01", Rule: "slow case", Tags: []string{TagCore}, Run: func(t *T) {
		t.A()
		t.Sleep(2 * time.Second)
	}}}
	code, _, stderr := run(t, opts, slow)
	if code != ExitLauncher {
		t.Errorf("a run that outlived the fixture lease exited %d, want %d\n%s", code, ExitLauncher, stderr)
	}
	for _, want := range []string{"the fixture outlived its lease", "nothing heartbeats them", SuiteWallClockBudget.String()} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say %q:\n%s", want, stderr)
		}
	}

	quick := []Case{{ID: "C-01", Rule: "quick case", Tags: []string{TagCore}, Run: func(t *T) { t.A() }}}
	code, _, stderr = run(t, opts, quick)
	if code != ExitPass {
		t.Errorf("a run inside the fixture lease exited %d, want %d\n%s", code, ExitPass, stderr)
	}
	if strings.Contains(stderr, "outlived") {
		t.Errorf("a run inside the fixture lease reported an overrun:\n%s", stderr)
	}
}

// TestFixtureRegistersWithTheAdvertisedMaximumLease pins the request
// itself: the fixture names lease_seconds explicitly and names the
// adapter's OWN maximum, read from describe like every other bound the
// suite applies — never protocol.LeaseMaxSeconds, which is only the
// default range's top, and never the default, which is what expired.
func TestFixtureRegistersWithTheAdvertisedMaximumLease(t *testing.T) {
	t.Parallel()
	const advertisedMax = 123
	adapter, setup := leaseFake(t, protocol.Lease{DefaultSeconds: 90, MinSeconds: 30, MaxSeconds: advertisedMax})
	read := []Case{{ID: "C-01", Rule: "registration", Tags: []string{TagCore}, Run: func(t *T) {
		for _, p := range []*Principal{t.A(), t.B(), t.C()} {
			data, err := os.ReadFile(filepath.Join(p.ConfigDir, "registration.json"))
			if err != nil {
				t.Fatalf("%s: the fixture registration was not recorded: %v", p.Name, err)
			}
			var reg protocol.SessionRegistration
			if err := json.Unmarshal(data, &reg); err != nil {
				t.Fatalf("%s: the registration does not parse: %v", p.Name, err)
			}
			if reg.LeaseSeconds == nil {
				t.Fatalf("%s: session register carries no lease_seconds; the fixture must not take lease.default_seconds", p.Name)
			}
			if *reg.LeaseSeconds != advertisedMax {
				t.Errorf("%s: session register lease_seconds %d, want the adapter's advertised max_seconds %d", p.Name, *reg.LeaseSeconds, advertisedMax)
			}
		}
	}}}
	if code, _, stderr := run(t, Options{Adapter: adapter, Setup: setup}, read); code != ExitPass {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
}
