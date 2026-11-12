package adapterkit

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// A certificate pin is how a backend whose certificate no authority signed
// is trusted (work item 89): the team file names the SHA-256 fingerprint
// of the server's own certificate, and the adapter accepts that
// certificate and no other. The one stored spelling is `sha256:` and 64
// lowercase hex digits; it is public, like the url it goes with.
const certificatePinPrefix = "sha256:"

// CertificateFingerprint is the pin of a certificate in its DER encoding,
// as TLS sends it: what `openssl x509 -noout -fingerprint -sha256` prints,
// lowercase and without its colons.
func CertificateFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return certificatePinPrefix + hex.EncodeToString(sum[:])
}

// ValidCertificatePin reports whether s is a pin in the stored spelling.
func ValidCertificatePin(s string) bool {
	digits, ok := strings.CutPrefix(s, certificatePinPrefix)
	if !ok || len(digits) != 2*sha256.Size {
		return false
	}
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// NormalizeCertificatePin reads a fingerprint as a person copies it and
// returns the stored spelling: the server's start line gives
// `sha256:<hex>`, and openssl gives `sha256 Fingerprint=AB:CD:…`, of which
// the part after the `=` is accepted, in either case, with or without the
// colons. ok is false for anything that is not 32 bytes of hex.
func NormalizeCertificatePin(s string) (pin string, ok bool) {
	v := strings.TrimSpace(s)
	if len(v) >= len(certificatePinPrefix) && strings.EqualFold(v[:len(certificatePinPrefix)], certificatePinPrefix) {
		v = v[len(certificatePinPrefix):]
	}
	v = strings.ToLower(strings.ReplaceAll(v, ":", ""))
	pin = certificatePinPrefix + v
	return pin, ValidCertificatePin(pin)
}

// SameCertificatePin compares two pins in constant time.
func SameCertificatePin(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
