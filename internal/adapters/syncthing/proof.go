package syncthing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The joining proof (README, "Joining is device acceptance"). A machine
// that joins names its Syncthing device
//
//	brigade1-<unix seconds>-<hex HMAC-SHA256(key, "<device id>\n<unix seconds>")>
//
// with key the raw SHA-256 of the join secret — the digest team.json
// stores, so every member can verify the name and nobody who lacks the
// secret can produce it. Syncthing announces the name to every device the
// machine connects to, and a member's engine lists it with the pending
// device. The timestamp is the join's own: a member revoked at T is
// re-accepted only on a proof made after T, a deliberate `team join`, not
// a running engine reconnecting.
const proofPrefix = "brigade1-"

// deviceIDShape is a Syncthing device id: eight groups of seven base-32
// characters (Syncthing's own alphabet, no 0, 1, 8 or 9).
var deviceIDShape = regexp.MustCompile(`^[A-Z2-7]{7}(-[A-Z2-7]{7}){7}$`)

// checkDeviceID reports whether id has a Syncthing device id's shape. The
// check digits are Syncthing's to verify; an id that passes here and
// fails there is refused by its REST API, and skipped.
func checkDeviceID(id string) bool {
	return deviceIDShape.MatchString(id)
}

// proofName is the device name a joining machine announces.
func proofName(digestHex, deviceID string, at time.Time) (string, error) {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac, err := proofMAC(digestHex, deviceID, ts)
	if err != nil {
		return "", err
	}
	return proofPrefix + ts + "-" + mac, nil
}

// verifyProof checks a pending device's announced name against the team's
// current digest, and answers when the proof was made.
func verifyProof(digestHex, deviceID, name string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(name, proofPrefix)
	if !ok {
		return time.Time{}, false
	}
	ts, mac, ok := strings.Cut(rest, "-")
	if !ok || ts == "" || mac == "" {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	want, err := proofMAC(digestHex, deviceID, ts)
	if err != nil {
		return time.Time{}, false
	}
	if !hmac.Equal([]byte(mac), []byte(want)) {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}

func proofMAC(digestHex, deviceID, ts string) (string, error) {
	key, err := hex.DecodeString(digestHex)
	if err != nil || len(key) != sha256.Size {
		return "", errInternal("the team's secret digest is not usable as a key")
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(deviceID + "\n" + ts))
	return hex.EncodeToString(h.Sum(nil)), nil
}
