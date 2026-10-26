package webhook

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"text":"hi"}`)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := Sign(testSecret, now.Unix(), body)
	if err := Verify(testSecret, ts, sig, body, now, DefaultMaxSkew); err != nil {
		t.Fatalf("a good signature was refused: %v", err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"text":"hi"}`)
	good := func(at time.Time) (string, string) {
		return strconv.FormatInt(at.Unix(), 10), Sign(testSecret, at.Unix(), body)
	}
	ts, sig := good(now)
	staleTS, staleSig := good(now.Add(-DefaultMaxSkew - time.Second))
	futureTS, futureSig := good(now.Add(DefaultMaxSkew + time.Second))
	for _, c := range []struct {
		name, ts, sig string
		body          []byte
		secret        []byte
		want          error
	}{
		{"unsigned", "", "", body, testSecret, errSignatureMissing},
		{"no timestamp", "", sig, body, testSecret, errSignatureMissing},
		{"stale", staleTS, staleSig, body, testSecret, errSignatureStale},
		{"future", futureTS, futureSig, body, testSecret, errSignatureStale},
		{"non-digit timestamp", "-1", sig, body, testSecret, errSignatureStale},
		{"overlong timestamp", "0000000000001800000000", sig, body, testSecret, errSignatureStale},
		{"wrong secret", ts, sig, body, []byte("another-secret-another-secret-0123"), errSignatureInvalid},
		{"tampered body", ts, sig, []byte(`{"text":"hj"}`), testSecret, errSignatureInvalid},
		{"other timestamp", strconv.FormatInt(now.Unix()+1, 10), sig, body, testSecret, errSignatureInvalid},
		{"wrong scheme", ts, "v0=" + sig[3:], body, testSecret, errSignatureInvalid},
		{"not hex", ts, "v1=zz", body, testSecret, errSignatureInvalid},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := Verify(c.secret, c.ts, c.sig, c.body, now, DefaultMaxSkew)
			if !errors.Is(err, c.want) || (err == nil) != (c.want == nil) {
				t.Fatalf("Verify = %v, want %v", err, c.want)
			}
		})
	}
}
