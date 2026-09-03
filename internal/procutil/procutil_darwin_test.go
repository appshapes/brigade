//go:build darwin

package procutil_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// darwinToken is "<sec>.<usec>" with usec zero-padded to six digits.
var darwinToken = regexp.MustCompile(`^[0-9]+\.[0-9]{6}$`)

// sameSecond reports that two darwin tokens share their seconds part.
func sameSecond(a, b string) bool {
	sa, _, _ := strings.Cut(a, ".")
	sb, _, _ := strings.Cut(b, ".")
	return sa == sb
}

func TestDarwinTokenIsSecondsDotMicroseconds(t *testing.T) {
	t.Parallel()
	info := mustLookup(t, os.Getpid())
	if !darwinToken.MatchString(info.StartToken) {
		t.Fatalf("token %q does not match %s", info.StartToken, darwinToken)
	}
}

// TestStartTokensDifferWithinOneSecond is E0-5's "unfixed limitation"
// fixed by construction: `ps -o lstart=` has 1 s resolution and six
// processes were seen sharing one token. Two sleepers started
// back-to-back must carry different tokens even when they start in the
// same wall-clock second. Starting a pair can straddle a second boundary,
// which would prove the weaker property only, so the test insists on a
// same-second pair and retries until it has one. This is a darwin
// property: linux's token is in 10 ms clock ticks (see the linux test).
func TestStartTokensDifferWithinOneSecond(t *testing.T) {
	t.Parallel()
	const attempts = 20
	for range attempts {
		a := testutil.NewSleeper(t)
		b := testutil.NewSleeper(t)
		ta, tb := mustLookup(t, a).StartToken, mustLookup(t, b).StartToken
		if ta == tb {
			t.Fatalf("two processes share the start token %q (pids %d and %d)", ta, a, b)
		}
		if sameSecond(ta, tb) {
			return
		}
	}
	t.Fatalf("no same-second pair in %d attempts; the machine is too slow for this test to be meaningful", attempts)
}
