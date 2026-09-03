//go:build linux

package procutil_test

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// linuxToken is field 22 of /proc/<pid>/stat: clock ticks since boot.
var linuxToken = regexp.MustCompile(`^[0-9]+$`)

func TestLinuxTokenIsClockTicks(t *testing.T) {
	t.Parallel()
	info := mustLookup(t, os.Getpid())
	if !linuxToken.MatchString(info.StartToken) {
		t.Fatalf("token %q does not match %s", info.StartToken, linuxToken)
	}
}

// TestLinuxTokensAreNonDecreasing pins what the linux token can promise:
// clock ticks since boot never run backwards, so a process started later
// never carries a smaller token, and one started in a LATER tick carries a
// larger one. Two children started inside the same 10 ms tick (CLK_TCK
// 100) legitimately share a token, so — unlike darwin — this test does not
// demand inequality; it demands order, and that the test process (started
// earlier by the go tool) is at or below every child.
func TestLinuxTokensAreNonDecreasing(t *testing.T) {
	t.Parallel()
	ticks := func(pid int) int64 {
		tok := mustLookup(t, pid).StartToken
		n, err := strconv.ParseInt(tok, 10, 64)
		if err != nil {
			t.Fatalf("token %q of pid %d is not a tick count: %v", tok, pid, err)
		}
		return n
	}
	self := ticks(os.Getpid())
	prev := self
	for range 5 {
		pid := testutil.NewSleeper(t)
		cur := ticks(pid)
		if cur < prev {
			t.Fatalf("pid %d started at tick %d, before its predecessor's %d", pid, cur, prev)
		}
		prev = cur
	}
	if prev < self {
		t.Fatalf("children at tick %d, the test process at %d", prev, self)
	}
}
