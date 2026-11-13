package cases

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"time"

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

// strangerLike returns a principal_ref that names nobody and has the shape
// of ref, one the backend issued: every hexadecimal digit of ref is
// replaced by a random one of its own kind (a digit by a digit, a letter
// by a letter of the same case) and every other character is kept. 4.4.10
// lets an adapter answer `unauthorized` for a principal_ref of a shape its
// backend never issues, and the Supabase adapter does for one that is not
// a UUID, so a case that wants the answer for "a principal that never was
// a member" cannot use randomRef: its 32 hexadecimal digits are the fs
// adapter's shape and no other backend's.
func strangerLike(ref string) string {
	random := make([]byte, len(ref))
	_, _ = rand.Read(random)
	out := []byte(ref)
	for i, c := range out {
		switch {
		case c >= '0' && c <= '9':
			out[i] = '0' + random[i]%10
		case c >= 'a' && c <= 'f':
			out[i] = 'a' + random[i]%6
		case c >= 'A' && c <= 'F':
			out[i] = 'A' + random[i]%6
		}
	}
	if string(out) == ref {
		return randomRef()
	}
	return string(out)
}

// scratchTeam makes a team of its own for a case that changes one, the
// administration cases C-48 to C-50: a scratch principal creates it and a
// second one joins it. The fixture's teams are every other case's, and a
// rotated secret or a revoked member there would fail them, so without
// team.create and team.join to make another team the case is skipped. The
// created result carries the team's first join secret.
func scratchTeam(t *conformance.T, prefix string) (owner, member *conformance.Principal, created *protocol.TeamCreateResult) {
	if !t.HasCap("team.create") || !t.HasCap("team.join") {
		t.Skip("needs team.create and team.join to make a team of its own")
	}
	owner = t.Scratch("owner")
	created = createTeam(t, owner, prefix+"-"+t.RunID(), prefix+"-owner@example.com")
	member = t.Scratch("member")
	if joined := joinTeam(t, member, created.JoinSecret, prefix+"-member@example.com"); joined.Rejoined || joined.TeamRef != created.TeamRef {
		t.Errorf("team join: {team_ref match %v, rejoined %v}; want the created team and false on a first join (4.4.10)",
			joined.TeamRef == created.TeamRef, joined.Rejoined)
	}
	return owner, member, created
}

// joinDocument is the `team join` stdin document for secret, for the joins
// a case sends through Exec because it expects them to be refused.
func joinDocument(t *conformance.T, secret string) []byte {
	return mustMarshal(t, &protocol.TeamJoinRequest{JoinSecret: secret})
}

// watchEndsUnauthorized asserts what a running watch does when its
// member's membership ends, by its own `team leave` (C-08) or by the
// owner's revocation (C-49): an unauthorized error event, retryable false
// and present, then exit 5 within 2 s of it (4.5.7, 4.4.9). The event's own
// deadline is the spec's push budget — 4.4.9's 5 s for anything accepted
// while the watch runs — and not a tighter number of the suite's: the
// membership is ended the instant `ready` is seen, and an adapter whose
// realtime join is still completing at that moment (the websocket dial
// and channel join through a gateway on a loaded CI runner) cannot
// receive the revocation as a push and learns it from the drain that
// follows the join. Measured: 2.24 s to the error event on a GitHub
// runner (run 33678110011, the Supabase adapter, C-08) against a 2 s
// deadline that passed in 0.5-1 s locally. after names what ended the
// membership, for the failure reasons.
func watchEndsUnauthorized(t *conformance.T, w *conformance.WatchProc, after string) {
	ev := w.Expect(protocol.EventError, t.PushDeadline())
	if ev.Error != nil && ev.Error.Error.Code != protocol.CodeUnauthorized {
		t.Errorf("watch after %s: error event code %s, want unauthorized (4.5.7)", after, ev.Error.Error.Code)
	}
	errObj, _ := ev.Raw["error"].(map[string]any)
	if rv, ok := errObj["retryable"]; !ok {
		t.Errorf("watch after %s: error event lacks retryable (B-2)", after)
	} else if rb, isBool := rv.(bool); !isBool || rb {
		t.Errorf("watch after %s: error.retryable %v, want false (4.6)", after, rv)
	}
	if exit, ok := w.Wait(2 * time.Second); !ok {
		t.Errorf("watch after %s: still running 2 s after the error event; want exit 5 (4.4.9)", after)
	} else if exit != protocol.CodeUnauthorized.Exit() {
		t.Errorf("watch after %s: exit %d, want %d (4.4.9)", after, exit, protocol.CodeUnauthorized.Exit())
	}
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

// checkDescription asserts one listed record carries exactly this
// session_description (C-13's heartbeat arm, C-19's resume arm); an
// absent member (nil pointer) is reported as such rather than as "",
// because absent and "" differ on the wire.
func checkDescription(t *conformance.T, what string, rec *protocol.SessionRecord, want string) {
	switch {
	case rec.SessionDescription == nil:
		t.Errorf("%s: session_description absent, want %q (4.4.3)", what, want)
	case *rec.SessionDescription != want:
		t.Errorf("%s: session_description %q, want %q (4.4.3)", what, *rec.SessionDescription, want)
	}
}
