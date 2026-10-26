package syncthing

import (
	"strings"
	"testing"
	"time"
)

func TestProofRoundTrip(t *testing.T) {
	t.Parallel()
	digest := sha256hex("brg1.t.secret")
	const device = "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"
	at := time.Date(2026, 10, 26, 12, 0, 1, 0, time.UTC)
	name, err := proofName(digest, device, at)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, proofPrefix) || len(name) != len(proofPrefix)+len("1793016001")+1+64 {
		t.Fatalf("name = %q", name)
	}
	if got, ok := verifyProof(digest, device, name); !ok || !got.Equal(at) {
		t.Fatalf("verify = %v, %v", got, ok)
	}
	for label, bad := range map[string]struct{ digest, device, name string }{
		"other digest":   {sha256hex("other"), device, name},
		"other device":   {digest, strings.Replace(device, "MFZWI3D", "AAAAAAA", 1), name},
		"no prefix":      {digest, device, strings.TrimPrefix(name, proofPrefix)},
		"tampered mac":   {digest, device, name[:len(name)-1] + "0"},
		"tampered time":  {digest, device, strings.Replace(name, "1793016001", "1793016002", 1)},
		"empty":          {digest, device, ""},
		"garbage digest": {"zz", device, name},
	} {
		if _, ok := verifyProof(bad.digest, bad.device, bad.name); ok {
			t.Errorf("%s: verified", label)
		}
	}
	if !checkDeviceID(device) || checkDeviceID("not-a-device") || checkDeviceID(strings.ToLower(device)) {
		t.Error("checkDeviceID")
	}
}
