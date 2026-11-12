package adapterkit_test

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
)

func TestCertificatePin(t *testing.T) {
	t.Parallel()
	// SHA-256 of the three bytes "abc".
	const want = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := adapterkit.CertificateFingerprint([]byte("abc")); got != want {
		t.Fatalf("CertificateFingerprint: %s, want %s", got, want)
	}
	digits := strings.TrimPrefix(want, "sha256:")
	var colons []string
	for i := 0; i < len(digits); i += 2 {
		colons = append(colons, strings.ToUpper(digits[i:i+2]))
	}
	for _, spelled := range []string{
		want,
		digits,
		"SHA256:" + strings.ToUpper(digits),
		strings.Join(colons, ":"),
		"  " + want + "\n",
	} {
		got, ok := adapterkit.NormalizeCertificatePin(spelled)
		if !ok || got != want {
			t.Errorf("NormalizeCertificatePin(%q) = %q, %v; want %q", spelled, got, ok, want)
		}
	}
	for _, bad := range []string{"", "sha256:", "sha256:abc", digits + "00", "md5:" + digits, strings.Repeat("g", 64), "sha256 Fingerprint=" + strings.Join(colons, ":")} {
		if got, ok := adapterkit.NormalizeCertificatePin(bad); ok {
			t.Errorf("NormalizeCertificatePin(%q) accepted as %q", bad, got)
		}
	}
	if !adapterkit.ValidCertificatePin(want) {
		t.Errorf("ValidCertificatePin refused the stored spelling")
	}
	for _, loose := range []string{digits, "sha256:" + strings.ToUpper(digits), want + " "} {
		if adapterkit.ValidCertificatePin(loose) {
			t.Errorf("ValidCertificatePin(%q) accepted a spelling that is not the stored one", loose)
		}
	}
	if !adapterkit.SameCertificatePin(want, want) || adapterkit.SameCertificatePin(want, "sha256:"+strings.Repeat("0", 64)) {
		t.Errorf("SameCertificatePin does not compare")
	}
}
