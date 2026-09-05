package conformance

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
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

// registerRecord is a `session register` result as a printf FORMAT
// string, for the shell fakes below: the first %s is the session id, the
// second is the `lease_seconds` member WITH its trailing comma (or the
// empty string, which is the absent-member shape). 4.4.2 carries
// `lease_seconds` beside the 4.4.3 record, so it is spliced into the flat
// object rather than nested anywhere.
const registerRecord = `{"ok":true,"protocol_version":"1","result":{"session_id":"%s","session_name":"fixture","principal_ref":"p","state":"active","activity":"busy","inbound":"accept","last_seen_at":"2026-08-30T12:00:00Z","lease_until":"2026-08-30T12:01:30Z","created_at":"2026-08-30T12:00:00Z","is_self":false,"resumed":false,%s"server_time":"2026-08-30T12:00:00Z"}}`

// The grant* constants are POSIX sh statements a fake runs with the
// requested lease in `$requested`; each assigns `member`, the JSON member
// registerRecord splices in. grantEcho is the honest adapter — it answers
// the lease it was asked for, which is what both shipped adapters do
// (internal/adapters/fs/session.go and internal/adapters/supabase's RPC);
// the others are the non-conforming shapes the fixture's grant check has
// to name.
const (
	grantEcho          = `member="\"lease_seconds\":$requested,"`
	grantAbsent        = `member=""`
	grantNotAnInteger  = `member="\"lease_seconds\":\"ninety\","`
	readRequestedLease = `requested=$(sed -n 's/.*"lease_seconds":\([0-9][0-9]*\).*/\1/p' "$BRIGADE_CONFIG_DIR/registration.json")`
)

// leverSentence is the clause every refusal must carry: the one thing an
// adapter author can change. Asserting the bare "lease.max_seconds" would
// not pin it — the message's opening clause already names
// `describe.lease.max_seconds` as what the fixture requested, so that
// substring survives deleting the lever entirely (measured).
const leverSentence = "the lever is lease.max_seconds in describe"

// grantFixed answers seconds whatever was requested: the adapter that
// advertises a long lease and silently clamps it, which is exactly the
// shape that reproduced P5-15's C-12 symptom with no mention of a lease.
func grantFixed(seconds int) string {
	return `member="\"lease_seconds\":` + strconv.Itoa(seconds) + `,"`
}

// leaseFake is an adapter that advertises lease, grants a lease the way
// grant says, and, once --setup has written its marker, reports a joined
// profile: enough for the fixture to build and register its three
// sessions. It is the shape TestRunSetupProvisionsTheFixture uses, with
// the lease range as the subject. Every registration document it is
// handed is kept at <config>/registration.json, which is how the tests
// read what the fixture asked for and how the fake reads it back.
func leaseFake(tb testing.TB, lease protocol.Lease, grant string) (adapter, setup string) {
	tb.Helper()
	unconfigured := describeWithLease(tb, lease)
	joined := strings.Replace(unconfigured, `"profile":{"name":"default","state":"unconfigured"}`,
		`"profile":{"name":"default","state":"joined","team_ref":"'"$(cat "$BRIGADE_CONFIG_DIR/team")"'","team_name":"ops","principal_ref":"'"$BRIGADE_CONFIG_DIR"'","human_label":"x@example.com"}`, 1)
	if joined == unconfigured {
		tb.Fatal("leaseFake: the profile member is not in the fake describe document")
	}
	adapter = writeScript(tb, "lease-adapter", `
case "$1 $2" in
  "describe ") if [ -f "$BRIGADE_CONFIG_DIR/team" ]; then printf '%s\n' '`+joined+`'; else printf '%s\n' '`+unconfigured+`'; fi ;;
  "session register")
    cat > "$BRIGADE_CONFIG_DIR/registration.json"
    `+readRequestedLease+`
    `+grant+`
    printf '`+registerRecord+`\n' "$BRIGADE_CONFIG_DIR" "$member" ;;
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

// fixtureCase is a one-case list whose only job is to build the fixture.
func fixtureCase() []Case {
	return []Case{{ID: "C-01", Rule: "fixture", Tags: []string{TagCore}, Run: func(t *T) { t.A() }}}
}

// TestFixtureRefusesAClampedLease is P5-15b: the fixture reads the lease
// it was GRANTED, not only the one it requested. An adapter that
// advertises 123 s and hands back 90 s is non-conforming, and before this
// check the suite said so only much later and in the wrong words — C-12
// failing with "A's fixture session is absent", the symptom the P5-15
// verifier reproduced against a scratch fs adapter that clamped. The
// refusal is a launcher error (exit 3) raised while the fixture is being
// built, so no case has yet read a fixture whose lease nobody knows, and
// the JSON report is still written.
func TestFixtureRefusesAClampedLease(t *testing.T) {
	t.Parallel()
	const advertisedMax, clampedTo = 123, 90
	adapter, setup := leaseFake(t, protocol.Lease{DefaultSeconds: 90, MinSeconds: 30, MaxSeconds: advertisedMax}, grantFixed(clampedTo))
	// The refusal has to STOP the run, not merely fail the case that
	// touched the fixture: `docs/adapter-authors.md` promises that no case
	// ever reads a fixture whose lease is in doubt, and only a second case
	// that records whether its body ran can pin that.
	later := false
	cases := append(fixtureCase(), Case{ID: "C-02", Rule: "after the refusal", Tags: []string{TagCore}, Run: func(*T) { later = true }})
	code, stdout, stderr := run(t, Options{Adapter: adapter, Setup: setup, JSON: true}, cases)
	if code != ExitLauncher {
		t.Errorf("a clamped lease exited %d, want %d\n%s", code, ExitLauncher, stderr)
	}
	if later {
		t.Errorf("a case body ran after the refusal; the run must stop there:\n%s", stderr)
	}
	for _, want := range []string{"123", "90", leverSentence, `A's fixture session is absent`, "C-12"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say %q:\n%s", want, stderr)
		}
	}
	// The refusal must not cost the operator the report: Run breaks out of
	// the case loop and still writes it.
	var rep Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Errorf("the JSON report was not written on the refusal: %v\nstdout: %s", err, stdout)
	}

	// Control: the same adapter granting what it advertises passes, and
	// says nothing about a grant.
	echoing, echoSetup := leaseFake(t, protocol.Lease{DefaultSeconds: 90, MinSeconds: 30, MaxSeconds: advertisedMax}, grantEcho)
	code, _, stderr = run(t, Options{Adapter: echoing, Setup: echoSetup}, fixtureCase())
	if code != ExitPass {
		t.Errorf("an adapter that grants what it advertises exited %d, want %d\n%s", code, ExitPass, stderr)
	}
	if strings.Contains(stderr, "granted") {
		t.Errorf("a conforming grant was reported anyway:\n%s", stderr)
	}
}

// TestFixtureRefusesAMissingGrant covers the other half of the check: a
// result with no `lease_seconds` at all, or one that is not an integral
// JSON number. 4.4.2 requires the member and C-10 asserts it, but C-10
// asserts it on its OWN registration and may run after every case that
// already read the fixture — so a fixture whose lease is unknown has to
// stop the run here rather than there.
func TestFixtureRefusesAMissingGrant(t *testing.T) {
	t.Parallel()
	const advertisedMax = 123
	for name, tc := range map[string]struct {
		grant string
		says  string
	}{
		"absent":         {grantAbsent, "lease_seconds is absent from the result"},
		"not an integer": {grantNotAnInteger, "the granted lease_seconds is not an integer"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter, setup := leaseFake(t, protocol.Lease{DefaultSeconds: 90, MinSeconds: 30, MaxSeconds: advertisedMax}, tc.grant)
			code, _, stderr := run(t, Options{Adapter: adapter, Setup: setup}, fixtureCase())
			if code != ExitLauncher {
				t.Errorf("exit %d, want %d\n%s", code, ExitLauncher, stderr)
			}
			for _, want := range []string{tc.says, "123", leverSentence} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr does not say %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// TestFixtureRefusesALeaseLongerThanTheRequest pins the driver's ruling
// that the check is EXACT equality, the same shape as C-14's assertion at
// the bottom of the range. A grant longer than the request is not the
// C-12 hazard — the fixture would outlive the run — but it still
// contradicts the adapter's own `describe`, so neither number can be
// trusted as the lease A, B and C hold, and the message must not claim
// the C-12 consequence it does not have.
func TestFixtureRefusesALeaseLongerThanTheRequest(t *testing.T) {
	t.Parallel()
	const advertisedMax, grantedMore = 123, 200
	adapter, setup := leaseFake(t, protocol.Lease{DefaultSeconds: 90, MinSeconds: 30, MaxSeconds: advertisedMax}, grantFixed(grantedMore))
	code, _, stderr := run(t, Options{Adapter: adapter, Setup: setup}, fixtureCase())
	if code != ExitLauncher {
		t.Errorf("a grant longer than the request exited %d, want %d\n%s", code, ExitLauncher, stderr)
	}
	for _, want := range []string{"123", "200", leverSentence, "contradicts describe"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "C-12") {
		t.Errorf("a longer grant claimed the C-12 hazard, which it does not have:\n%s", stderr)
	}
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
	adapter, setup := leaseFake(t, protocol.Lease{DefaultSeconds: 1, MinSeconds: 1, MaxSeconds: 1}, grantEcho)
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
	adapter, setup := leaseFake(t, protocol.Lease{DefaultSeconds: 90, MinSeconds: 30, MaxSeconds: advertisedMax}, grantEcho)
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
