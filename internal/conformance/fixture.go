package conformance

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// SuiteWallClockBudget is the whole-run wall time the suite supports, and
// the reason the fixture does not take the protocol's default lease.
//
// A, B and C's fixture sessions are registered once and NOTHING heartbeats
// them, while C-12 asserts A's session is present in a LIVE `session list`
// (`include_offline = false`). Under the 4.4.1 default of 90 s
// (protocol.LeaseDefaultSeconds) that made the whole suite depend on the
// case order and on the backend's wall time: measured against the hosted
// Supabase project on 2026-09-05, C-12 started at t = 10.9 s in id order
// and passed, and at t = 124.8 s under `--shuffle 5150907` and failed —
// the fixture rows were `closed=false` with an expired lease, exactly the
// state C-14 requires an adapter to report `offline`. The fs run's whole
// wall time is ~20 s, so it never reached its own default lease and the
// dependency stayed latent there.
//
// The fixture therefore registers with the LONGEST lease the adapter
// advertises (`describe.lease.max_seconds`), and this budget is what the
// suite promises that lease has to cover: 8 minutes is 3x the slowest
// measured run (160 s for 45 cases with --slow, hosted; ~20 s on the fs
// adapter) and sits 2 minutes under protocol.LeaseMaxSeconds, the top of
// the 4.4.1 default range that both shipped adapters advertise and the
// Supabase schema pins with a check constraint — so the two numbers can
// drift apart before either becomes a lie. Three joins keep it honest:
// TestSuiteWallClockBudgetFitsTheDefaultLeaseRange (the constants),
// TestFixtureLeaseCoversTheSuiteBudget (what a real adapter grants) and
// fixture.overrun, which refuses to report a run that outlived the lease
// it was granted — the "longer suite" half, which no unit test can see.
const SuiteWallClockBudget = 8 * time.Minute

// fixture is the three-principal, two-team fixture of plan 9.2, built
// lazily by the first case that asks for it.
type fixture struct {
	mu      sync.Mutex
	built   bool
	err     error
	a, b, c *Principal
	secret  string       // T1's join secret; "" under --setup
	extras  []*Principal // every principal T.JoinPrincipal joined into T1
	// lease is the lease A, B and C's sessions were registered with and
	// registeredAt the LOCAL instant of that registration: an elapsed time
	// measured against the suite's own monotonic clock, never against an
	// adapter's `server_time`, so no clock skew reaches the check.
	lease        time.Duration
	registeredAt time.Time
}

// overrun reports, as a reason, that the run outlived the fixture's lease:
// every case that ran past it saw A, B and C offline and its result cannot
// be trusted. It returns "" when the fixture was never built, when it
// failed to build (that is already a launcher error) or when the lease was
// still valid at now.
func (f *fixture) overrun(now time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.built || f.err != nil || f.lease <= 0 || f.registeredAt.IsZero() {
		return ""
	}
	elapsed := now.Sub(f.registeredAt)
	if elapsed <= f.lease {
		return ""
	}
	return "the fixture outlived its lease: A, B and C were registered with the longest lease this adapter advertises (" +
		f.lease.String() + ") and nothing heartbeats them, but " + elapsed.Round(time.Second).String() +
		" of the run followed that registration, so every case past the lease saw them offline (4.5.8) and this run is not trustworthy" +
		"; the suite's budget is " + SuiteWallClockBudget.String() +
		", so a backend this slow needs a longer advertised lease.max_seconds"
}

// ensure builds the fixture on first use. A failure is a launcher error:
// the case aborts with launcherAbort and the run stops with ExitLauncher.
func (f *fixture) ensure(t *T) *fixture {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.built {
		f.built = true
		f.err = f.build(t)
	}
	if f.err != nil {
		panic(launcherAbort{err: f.err})
	}
	return f
}

// build provisions A, B and C and registers one session each.
func (f *fixture) build(t *T) error {
	run := t.run
	var err error
	if f.a, err = run.newPrincipal("a", "a"); err != nil {
		return err
	}
	if f.b, err = run.newPrincipal("b", "b"); err != nil {
		return err
	}
	if f.c, err = run.newPrincipal("c", "c"); err != nil {
		return err
	}
	if run.opts.Setup != "" {
		for _, p := range []*Principal{f.a, f.b, f.c} {
			if err := f.setup(t, p); err != nil {
				return err
			}
		}
	} else {
		if !t.HasCap("team.create") || !t.HasCap("team.join") {
			return errors.New("the adapter advertises neither team.create and team.join, and no --setup was given: the fixture cannot be provisioned")
		}
		secret1, err := f.create(t, f.a, "t1-"+run.runID, "alice@example.com")
		if err != nil {
			return err
		}
		f.secret = secret1
		if err := f.join(t, f.b, secret1, "bob@example.com"); err != nil {
			return err
		}
		if _, err := f.create(t, f.c, "t2-"+run.runID, "carol@example.com"); err != nil {
			return err
		}
	}
	for _, p := range []*Principal{f.a, f.b, f.c} {
		if err := f.identify(t, p); err != nil {
			return err
		}
	}
	if f.a.TeamRef != f.b.TeamRef {
		return errors.New("fixture: A and B are not in one team after provisioning")
	}
	if f.c.TeamRef == f.a.TeamRef {
		return errors.New("fixture: C is in A's team after provisioning; want a second team")
	}
	// The lease is the adapter's own maximum, not the protocol default:
	// see SuiteWallClockBudget. It is read from `describe` like every other
	// bound the suite applies, so an adapter with its own range is served
	// its own number, and it is recorded here against the suite's clock for
	// the end-of-run overrun check.
	lease := t.Describe().Lease.MaxSeconds
	f.lease = time.Duration(lease) * time.Second
	f.registeredAt = time.Now()
	for _, p := range []*Principal{f.a, f.b, f.c} {
		if err := f.register(t, p, lease); err != nil {
			return err
		}
	}
	return nil
}

// fixtureError reads the outcome of one fixture spawn: the launcher's
// own failure, then the envelope.
func fixtureError(what string, r *Result) error {
	if r.err != nil {
		return errors.New("fixture: " + what + ": " + r.err.Error())
	}
	if r.Envelope == nil {
		return errors.New("fixture: " + what + ": stdout is not one valid envelope (exit " + strconv.Itoa(r.Exit) + ")")
	}
	if !r.Envelope.OK {
		return errors.New("fixture: " + what + ": " + describeError(r.Envelope.Error) + " (exit " + strconv.Itoa(r.Exit) + ")")
	}
	return nil
}

func (f *fixture) setup(t *T, p *Principal) error {
	r, err := t.run.launcher.operator(t.ctx, t.c.ID, p, t.run.opts.Setup, nil)
	if err != nil {
		return errors.New("--setup for " + p.Name + ": " + err.Error())
	}
	if r.Exit != 0 {
		return errors.New("--setup for " + p.Name + " exited " + strconv.Itoa(r.Exit))
	}
	return nil
}

func (f *fixture) create(t *T, p *Principal, team, label string) (string, error) {
	req := protocol.TeamCreateRequest{TeamName: team, HumanLabel: label}
	r := t.Exec(p, t.mustJSON(&req), "team", "create")
	if err := fixtureError("team create for "+p.Name, r); err != nil {
		return "", err
	}
	var res protocol.TeamCreateResult
	if err := protocol.Decode(r.Envelope.Result, &res); err != nil {
		return "", errors.New("fixture: team create for " + p.Name + ": " + err.Error())
	}
	t.run.launcher.addSecret(res.JoinSecret)
	return res.JoinSecret, nil
}

func (f *fixture) join(t *T, p *Principal, secret, label string) error {
	req := protocol.TeamJoinRequest{JoinSecret: secret, HumanLabel: label}
	r := t.Exec(p, t.mustJSON(&req), "team", "join")
	if err := fixtureError("team join for "+p.Name, r); err != nil {
		return err
	}
	var res protocol.TeamJoinResult
	if err := protocol.Decode(r.Envelope.Result, &res); err != nil {
		return errors.New("fixture: team join for " + p.Name + ": " + err.Error())
	}
	return nil
}

// identify reads the principal's identity from describe, which must say
// joined whichever way the profile was provisioned.
func (f *fixture) identify(t *T, p *Principal) error {
	r := t.Exec(p, nil, "describe")
	if err := fixtureError("describe for "+p.Name, r); err != nil {
		return err
	}
	var d protocol.DescribeResult
	if err := protocol.Decode(r.Envelope.Result, &d); err != nil {
		return errors.New("fixture: describe for " + p.Name + ": " + err.Error())
	}
	if d.Profile.State != protocol.ProfileStateJoined {
		return errors.New("fixture: " + p.Name + " is " + d.Profile.State + " after provisioning, want joined")
	}
	p.PrincipalRef, p.TeamRef, p.TeamName = d.Profile.PrincipalRef, d.Profile.TeamRef, d.Profile.TeamName
	return nil
}

// register registers p's fixture session with leaseSeconds, the longest
// lease the adapter advertises, and checks the lease it was GRANTED. The
// lease is EXPLICIT and never left to `lease.default_seconds`: nothing
// heartbeats these three sessions and C-12 requires A's to still be live
// whenever it happens to run.
func (f *fixture) register(t *T, p *Principal, leaseSeconds int) error {
	reg := protocol.SessionRegistration{
		Harness:        Harness,
		HarnessVersion: buildinfo.String(),
		SessionName:    "fixture-" + p.Name,
		Activity:       protocol.ActivityBusy,
		Inbound:        protocol.InboundAccept,
		LeaseSeconds:   &leaseSeconds,
	}
	r := t.Exec(p, t.mustJSON(&reg), "session", "register")
	if err := fixtureError("session register for "+p.Name, r); err != nil {
		return err
	}
	var rec protocol.SessionRecord
	if err := protocol.Decode(r.Envelope.Result, &rec); err != nil {
		return errors.New("fixture: session register for " + p.Name + ": " + err.Error())
	}
	p.session = rec.SessionID
	return checkGrantedLease(p.Name, leaseSeconds, r.Raw)
}

// checkGrantedLease refuses the fixture unless the adapter granted
// EXACTLY the lease the fixture requested, which is the adapter's OWN
// advertised `lease.max_seconds`: any other grant contradicts its own
// `describe`, so neither number can be trusted as the lease A, B and C
// hold. It is the same shape as C-14's assertion at the bottom of the
// range, applied to the top on every run.
//
// The grant is read from the loose result object because 4.4.2 carries
// `lease_seconds` BESIDE the 4.4.3 record — protocol.SessionRecord has no
// such member and protocol.Decode tolerates the extra ones — and it is
// read here rather than left to C-10 (which asserts the member against
// `lease.default_seconds`, on its own registration, and may run after
// every case that already read the fixture) because a silent clamp
// otherwise surfaces only as C-12's "A's fixture session is absent",
// which names nothing an author can act on. Refusing here is a launcher
// error: ensure turns it into launcherAbort and Run reports it with
// ExitLauncher (3) before any case has run against a fixture whose lease
// nobody knows.
func checkGrantedLease(name string, requested int, raw map[string]any) error {
	asked := "fixture: session register for " + name + ": the fixture requested lease_seconds " +
		strconv.Itoa(requested) + " s, this adapter's own describe.lease.max_seconds, and "
	lever := "; the lever is lease.max_seconds in describe: advertise the longest lease you will actually grant"
	unknown := ", so the lease A, B and C hold is unknown and no case that reads them can be reasoned about"

	result, _ := raw["result"].(map[string]any)
	value, present := result["lease_seconds"]
	number, isNumber := value.(float64)
	switch {
	case !present:
		return errors.New(asked + "lease_seconds is absent from the result (4.4.2 carries it beside the record, and C-10 asserts it)" + unknown + lever)
	case !isNumber || number != float64(int(number)):
		return errors.New(asked + "the granted lease_seconds is not an integer" + unknown + lever)
	}

	granted := int(number)
	switch {
	case granted < requested:
		return errors.New(asked + "was granted " + strconv.Itoa(granted) +
			" s: nothing heartbeats A, B and C, so a run on this grant lists them offline " + strconv.Itoa(granted) +
			" s after the fixture is built (4.5.8) and fails C-12 later as \"A's fixture session is absent\" with no mention of the lease" + lever)
	case granted > requested:
		return errors.New(asked + "was granted " + strconv.Itoa(granted) +
			" s, longer than the maximum this adapter advertises: the grant contradicts describe" + lever)
	}
	return nil
}

// newPrincipal creates <run>/principals/<dir>/{home,config,state}, all
// 0700, and returns the principal named name.
func (r *runner) newPrincipal(name, dir string) (*Principal, error) {
	base := filepath.Join(r.launcher.runDir, "principals", dir)
	p := &Principal{
		Name:      name,
		Home:      filepath.Join(base, "home"),
		ConfigDir: filepath.Join(base, "config"),
		StateDir:  filepath.Join(base, "state"),
		dir:       base,
	}
	for _, d := range []string{p.Home, p.ConfigDir, p.StateDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return p, nil
}
