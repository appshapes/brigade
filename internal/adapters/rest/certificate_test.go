//go:build !windows

package rest

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// pinOf is the pin of a TLS backend's own certificate.
func pinOf(b *backend) string {
	return adapterkit.CertificateFingerprint(b.srv.Certificate().Raw)
}

// opensslForm spells a pin the way `openssl x509 -fingerprint -sha256`
// prints the digits: uppercase, a colon between bytes.
func opensslForm(pin string) string {
	digits := strings.ToUpper(strings.TrimPrefix(pin, "sha256:"))
	var pairs []string
	for i := 0; i < len(digits); i += 2 {
		pairs = append(pairs, digits[i:i+2])
	}
	return strings.Join(pairs, ":")
}

// TestAPinnedCertificateIsTheOnlyOneTrusted: a server whose certificate no
// authority signed is reached with its pin and is refused without one; a
// profile that pins another certificate is refused before anything of a
// request is sent.
func TestAPinnedCertificateIsTheOnlyOneTrusted(t *testing.T) {
	t.Parallel()
	b := newTLSBackend(t)
	pin := pinOf(b)

	// The pin is read as a person copies it and stored in one spelling.
	pinned := newRig(t, "")
	result := pinned.ok("", "profile", "init", "--url", b.url(), "--certificate", opensslForm(pin))
	if result["certificate"] != pin {
		t.Fatalf("profile init --certificate: %v, want the pin as %s", result, pin)
	}
	created := pinned.ok(`{"team_name":"ops","human_label":"alice@example.com"}`, "team", "create")
	secret := str(t, created, "join_secret")
	if status := pinned.ok("", "profile", "status"); status["certificate"] != pin || status["state"] != protocol.ProfileStateJoined {
		t.Errorf("profile status: %v, want the pin and state joined", status)
	}
	pinned.register("pinned")

	// No pin: the usual verification, which this certificate does not pass.
	plain := newRig(t, "")
	plain.ok("", "profile", "init", "--url", b.url())
	b.forget()
	refused := plain.fails(protocol.CodeUnavailable, `{"join_secret":"`+secret+`"}`, "team", "join")
	if details(t, refused.stdout)["reason"] != reasonTLS {
		t.Errorf("no pin: details %v, want reason %s", details(t, refused.stdout), reasonTLS)
	}

	// Another certificate's pin: refused in the handshake, so the join secret
	// never left this machine.
	wrong := newRig(t, "")
	wrong.ok("", "profile", "init", "--url", b.url(), "--certificate", "sha256:"+strings.Repeat("0", 64))
	mismatch := wrong.fails(protocol.CodeUnavailable, `{"join_secret":"`+secret+`"}`, "team", "join")
	if details(t, mismatch.stdout)["reason"] != reasonCertificateMismatch {
		t.Errorf("another pin: details %v, want reason %s", details(t, mismatch.stdout), reasonCertificateMismatch)
	}
	if seen := b.seen(""); len(seen) != 0 {
		t.Errorf("the server saw %d requests from profiles that do not trust it, want none", len(seen))
	}
	for _, r := range []*rig{plain, wrong} {
		if strings.Contains(r.output(), secret) {
			t.Errorf("the join secret reached a transcript")
		}
	}

	// --force replaces the backend, the url and the pin together.
	wrong.ok("", "profile", "init", "--url", b.url(), "--certificate", pin, "--force")
	wrong.ok(`{"join_secret":"`+secret+`","human_label":"bob@example.com"}`, "team", "join")
	wrong.ok("", "profile", "init", "--url", b.url(), "--force")
	lost := wrong.fails(protocol.CodeUnavailable, "", "session", "list")
	if details(t, lost.stdout)["reason"] != reasonTLS {
		t.Errorf("after --force without a pin: details %v, want reason %s", details(t, lost.stdout), reasonTLS)
	}
}

// TestTheJoinDocumentCarriesThePin: `team join` on a fresh profile takes
// the backend from the request, the pin with the url, and a configured
// profile is not moved to another certificate by a request.
func TestTheJoinDocumentCarriesThePin(t *testing.T) {
	t.Parallel()
	b := newTLSBackend(t)
	pin := pinOf(b)
	admin := newRig(t, "")
	admin.ok("", "profile", "init", "--url", b.url(), "--certificate", pin)
	secret := str(t, admin.ok(`{"team_name":"ops"}`, "team", "create"), "join_secret")

	member := newRig(t, "")
	document := func(certificate string) string {
		return `{"join_secret":"` + secret + `","backend":{"url":"` + b.url() + `","certificate":"` + certificate + `"}}`
	}
	bad := member.fails(protocol.CodeInvalidInput, document("sha256:XYZ"), "team", "join")
	if details(t, bad.stdout)["field"] != "certificate" {
		t.Errorf("a malformed pin: details %v, want field certificate", details(t, bad.stdout))
	}
	if state := member.state(); state != protocol.ProfileStateUnconfigured {
		t.Errorf("after a refused join: state %q, want unconfigured", state)
	}
	member.ok(document(pin), "team", "join")
	var stored adapterkit.Profile
	if p, err := adapterkit.LoadProfile(member.cfg, "default"); err != nil {
		t.Fatal(err)
	} else {
		stored = *p
	}
	if stored.URL != b.url() || stored.Certificate != pin {
		t.Errorf("team.json after the join: url %q certificate %q, want the request's", stored.URL, stored.Certificate)
	}

	// The same backend again is a no-op; the same url under another pin is a
	// different backend.
	member.ok("", "team", "leave")
	member.ok(document(pin), "team", "join")
	member.ok("", "team", "leave")
	other := "sha256:" + hex.EncodeToString(make([]byte, 32))
	conflict := member.fails(protocol.CodeConflict, document(other), "team", "join")
	if details(t, conflict.stdout)["reason"] != reasonProfileExists {
		t.Errorf("another pin on a configured profile: details %v, want reason %s", details(t, conflict.stdout), reasonProfileExists)
	}
}

// TestAPinNeedsHTTPS: a pin with an http url is refused where it is given,
// and a profile edited into that state is `config`, never a silent pass.
func TestAPinNeedsHTTPS(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	pin := "sha256:" + strings.Repeat("ab", 32)
	r := newRig(t, "")
	got := r.fails(protocol.CodeInvalidInput, "", "profile", "init", "--url", b.url(), "--certificate", pin)
	if d := details(t, got.stdout); d["field"] != "certificate" || d["reason"] != reasonCertificateWithoutTLS {
		t.Errorf("a pin with an http url: details %v", d)
	}
	malformed := r.fails(protocol.CodeInvalidInput, "", "profile", "init", "--url", "https://brigade.example.com", "--certificate", "sha256:abc")
	if details(t, malformed.stdout)["field"] != "certificate" {
		t.Errorf("a short pin: details %v, want field certificate", details(t, malformed.stdout))
	}

	r.ok("", "profile", "init", "--url", b.url())
	p, err := adapterkit.LoadProfile(r.cfg, "default")
	if err != nil {
		t.Fatal(err)
	}
	p.Certificate = pin
	if err := adapterkit.SaveProfile(r.cfg, "default", p); err != nil {
		t.Fatal(err)
	}
	edited := r.fails(protocol.CodeConfig, `{"team_name":"ops"}`, "team", "create")
	if details(t, edited.stdout)["reason"] != reasonBackendCertificate {
		t.Errorf("an edited profile: details %v, want reason %s", details(t, edited.stdout), reasonBackendCertificate)
	}
}
