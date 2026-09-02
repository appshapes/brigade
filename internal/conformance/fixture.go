package conformance

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/protocol"
)

// fixture is the three-principal, two-team fixture of plan 9.2, built
// lazily by the first case that asks for it.
type fixture struct {
	mu      sync.Mutex
	built   bool
	err     error
	a, b, c *Principal
	secret  string       // T1's join secret; "" under --setup
	extras  []*Principal // every principal T.JoinPrincipal joined into T1
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
	for _, p := range []*Principal{f.a, f.b, f.c} {
		if err := f.register(t, p); err != nil {
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

func (f *fixture) register(t *T, p *Principal) error {
	reg := protocol.SessionRegistration{
		Harness:        Harness,
		HarnessVersion: buildinfo.String(),
		SessionName:    "fixture-" + p.Name,
		Activity:       protocol.ActivityBusy,
		Inbound:        protocol.InboundAccept,
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
