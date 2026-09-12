//go:build darwin || linux

package adapterkit_test

import (
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

func TestReadInputTerminalRefusal(t *testing.T) {
	t.Parallel()
	master, slave := openPTY(t)
	// If the terminal check were broken, ReadInput would try to READ the
	// slave; feed it a document now and hang up after 30 s, so a defect
	// fails the test rather than hanging it. The delayed close matters:
	// closing the master at once could unmake the slave's terminal-ness
	// before the check under test even ran.
	_, _ = master.WriteString(`{"x":1}`)
	timer := time.AfterFunc(30*time.Second, func() { _ = master.Close() })
	defer timer.Stop()
	_, err := adapterkit.ReadInput(slave)
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeUsage {
		t.Fatalf("code = %q, want usage for a terminal on stdin", perr.Code)
	}
	if got := perr.Code.Exit(); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	if perr.Details["reason"] != "terminal" {
		t.Fatalf("details = %v, want reason=terminal", perr.Details)
	}
}

// TestChildProcessTTYUsageEnvelope drives the whole chain across a real
// process boundary: a child whose stdin IS a pty slave must refuse with
// the usage envelope on stdout, exactly as a human running an
// input-taking adapter command by hand would see.
func TestChildProcessTTYUsageEnvelope(t *testing.T) {
	t.Parallel()
	master, slave := openPTY(t)
	// Anti-hang: a correct child never reads its stdin; a broken terminal
	// check would block on the slave. Feed it a document now and hang up
	// after 30 s (a hang catcher: a child that has not reached its
	// terminal check by then is stuck, not slow), so a defect fails the
	// assertions below instead of wedging the suite.
	_, _ = master.WriteString(`{"x":1}`)
	timer := time.AfterFunc(30*time.Second, func() { _ = master.Close() })
	defer timer.Stop()
	stdout, stderr := runChildEcho(t, slave)
	env := decodeEnvelope(t, childEnvelope(t, stdout))
	if env.OK || env.Error.Code != protocol.CodeUsage {
		t.Fatalf("envelope = %+v, want ok=false code=usage", env)
	}
	if strings.Contains(stderr, "protocol_version") {
		t.Fatalf("envelope material leaked to stderr: %q", stderr)
	}
}
