package cases

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"

	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// Helpers shared by cases across the two authoring lanes. Everything here
// builds requests the way T does, so a case that sends a document through
// Exec/SendRaw (because it expects a refusal) sends the same bytes the
// typed helpers would.

// createTeam runs `team create` for p and returns the validated result.
func createTeam(t *conformance.T, p *conformance.Principal, name, label string) *protocol.TeamCreateResult {
	req := protocol.TeamCreateRequest{TeamName: name, HumanLabel: label}
	var res protocol.TeamCreateResult
	t.OK(t.Exec(p, mustMarshal(t, &req), "team", "create"), &res)
	p.PrincipalRef, p.TeamRef, p.TeamName = res.PrincipalRef, res.TeamRef, res.TeamName
	return &res
}

// joinTeam runs `team join` for p with secret and returns the validated
// result.
func joinTeam(t *conformance.T, p *conformance.Principal, secret, label string) *protocol.TeamJoinResult {
	req := protocol.TeamJoinRequest{JoinSecret: secret, HumanLabel: label}
	var res protocol.TeamJoinResult
	t.OK(t.Exec(p, mustMarshal(t, &req), "team", "join"), &res)
	p.PrincipalRef, p.TeamRef, p.TeamName = res.PrincipalRef, res.TeamRef, res.TeamName
	return &res
}

// describe runs `describe` for p (with BRIGADE_TEST_OFFLINE=1 when offline)
// and returns the validated result.
func describe(t *conformance.T, p *conformance.Principal, offline bool) *protocol.DescribeResult {
	var extra []string
	if offline {
		extra = []string{"BRIGADE_TEST_OFFLINE=1"}
	}
	var d protocol.DescribeResult
	t.OK(t.ExecEnv(p, extra, nil, "describe"), &d)
	return &d
}

// mustMarshal marshals a request document; a failure is a suite bug.
func mustMarshal(t *conformance.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

// randomRef returns 16 random bytes as lowercase hex: an identifier that
// no adapter has issued, in a shape every adapter accepts as a path-safe
// opaque id.
func randomRef() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// syntheticSecret builds a well-formed join secret for teamRef with a
// random secret half (4.4.10 format); it is never a real secret.
func syntheticSecret(teamRef string) string {
	return protocol.JoinSecretPrefix + teamRef + "." + randomRef()
}

// newRegistration is the suite's standard registration (the one Register
// sends) with the given name, for the registrations a case sends through
// Exec because it expects them to fail.
func newRegistration(name string) protocol.SessionRegistration {
	return protocol.SessionRegistration{
		Harness:        conformance.Harness,
		HarnessVersion: buildinfo.String(),
		SessionName:    name,
		Activity:       protocol.ActivityBusy,
		Inbound:        protocol.InboundAccept,
	}
}

// registration marshals newRegistration(name) with an optional description.
func registration(t *conformance.T, name string, description *string) []byte {
	return registrationWith(t, name, func(r *protocol.SessionRegistration) { r.SessionDescription = description })
}

// registrationWith marshals newRegistration(name) after edit, for the
// registrations a case sends through Exec because it expects them to fail.
func registrationWith(t *conformance.T, name string, edit func(*protocol.SessionRegistration)) []byte {
	reg := newRegistration(name)
	if edit != nil {
		edit(&reg)
	}
	return mustMarshal(t, &reg)
}

// nameFor builds the unique session name of brief section 7,
// "<case id>-<purpose>-<run id>".
func nameFor(t *conformance.T, caseID, purpose string) string {
	return caseID + "-" + purpose + "-" + t.RunID()
}
