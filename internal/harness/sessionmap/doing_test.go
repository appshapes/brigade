package sessionmap_test

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

// The `doing_mode` member of card 25 (plan 5.2): absent or one of the
// five resolved words. validByPID leaves it unset on purpose, so the
// exact-member pin of TestByPIDWireShapeIsExactlyThePlanListAndNeverATokenMember
// stays as it is and a map from before the member existed is proven to
// read; these tests cover the member on its own.

func TestDoingModeValidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		mode      string
		wantField string // "" means valid
	}{
		{"absent (a map from before card 25) is valid", "", ""},
		{"unsupported", doing.ModeUnsupported, ""},
		{"off", doing.ModeOff, ""},
		{"unasked", doing.ModeUnasked, ""},
		{"allowed", doing.ModeAllowed, ""},
		{"quiet", doing.ModeQuiet, ""},
		{"outside the set", "publish" + evilMarker, "doing_mode"},
		{"wrongly cased", "Off", "doing_mode"},
		{"a sentence smuggled into the mode member", "fixing the roster " + evilMarker, "doing_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := validByPID()
			m.DoingMode = tc.mode
			err := m.Validate()
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate: %v, want nil", err)
				}
				return
			}
			details := assertConfig(t, err, sessionmap.ReasonMapInvalid)
			if got := details["field"]; got != tc.wantField {
				t.Fatalf("details.field = %q, want %q", got, tc.wantField)
			}
		})
	}
}

// TestDoingModeIsOmittedWhenAbsentAndRoundTrips: a map without a mode
// carries no doing_mode member at all — so a file written by an older
// plugin and one written by a hook that resolved nothing look the same —
// and a map with one reads back with exactly that word.
func TestDoingModeIsOmittedWhenAbsentAndRoundTrips(t *testing.T) {
	t.Parallel()
	m := validByPID()
	data, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "doing_mode") {
		t.Fatalf("an absent doing mode was written: %s", data)
	}
	m.DoingMode = doing.ModeAllowed
	data, err = json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"doing_mode":"allowed"`) {
		t.Fatalf("the mode was not written: %s", data)
	}
	var back sessionmap.ByPID
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.DoingMode != doing.ModeAllowed || back.Validate() != nil {
		t.Fatalf("round trip: %+v", back)
	}
}
