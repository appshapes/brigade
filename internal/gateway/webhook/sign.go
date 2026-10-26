package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// The signature headers, on both directions.
const (
	// HeaderTimestamp carries the signing time in Unix seconds.
	HeaderTimestamp = "X-Brigade-Timestamp"
	// HeaderSignature carries "v1=<hex HMAC-SHA256>".
	HeaderSignature = "X-Brigade-Signature"
	// HeaderDelivery carries the Brigade message id on an outbound POST,
	// so a receiver can deduplicate an at-least-once redelivery.
	HeaderDelivery = "X-Brigade-Delivery"

	signatureScheme = "v1"
	// DefaultMaxSkew is how far a timestamp may be from the verifier's
	// clock, either way (the Slack convention the hosted gateway uses).
	DefaultMaxSkew = 5 * time.Minute
	// MinSecretBytes is the shortest shared secret accepted.
	MinSecretBytes = 32
	// maxTimestampDigits bounds the parse; Unix seconds fit in 11 digits
	// until the year 5138.
	maxTimestampDigits = 12
)

// Fixed verification failures. Their text never echoes a header.
var (
	errSignatureMissing = errors.New("the request is not signed")
	errSignatureStale   = errors.New("the request timestamp is outside the accepted window")
	errSignatureInvalid = errors.New("the request signature does not match")
)

// Sign returns the HeaderSignature value for body signed at ts.
func Sign(secret []byte, ts int64, body []byte) string {
	return signatureScheme + "=" + hex.EncodeToString(mac(secret, ts, body))
}

func mac(secret []byte, ts int64, body []byte) []byte {
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(signatureScheme + ":" + strconv.FormatInt(ts, 10) + ":"))
	_, _ = h.Write(body)
	return h.Sum(nil)
}

// Verify checks a request's timestamp and signature headers against body.
// The timestamp must be within maxSkew of now, either way, and the
// signature is compared in constant time. Only the v1 scheme is accepted.
func Verify(secret []byte, tsHeader, sigHeader string, body []byte, now time.Time, maxSkew time.Duration) error {
	if tsHeader == "" || sigHeader == "" {
		return errSignatureMissing
	}
	if len(tsHeader) > maxTimestampDigits || strings.TrimLeft(tsHeader, "0123456789") != "" {
		return errSignatureStale
	}
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return errSignatureStale
	}
	if d := now.Sub(time.Unix(ts, 0)); d > maxSkew || d < -maxSkew {
		return errSignatureStale
	}
	got, ok := strings.CutPrefix(sigHeader, signatureScheme+"=")
	if !ok {
		return errSignatureInvalid
	}
	raw, err := hex.DecodeString(got)
	if err != nil {
		return errSignatureInvalid
	}
	if !hmac.Equal(raw, mac(secret, ts, body)) {
		return errSignatureInvalid
	}
	return nil
}
