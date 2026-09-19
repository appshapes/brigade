package adapterclient

import (
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// The read-back of a session's doing line (card 25, plan 5.3): the one
// place Brigade re-publishes a string it did not just receive from the
// model. The helpers (fsClient, setupTeam, registration) are the package's.

// TestOwnDescription reads a session's doing line back through the real
// fs adapter (card 25): a heartbeated description comes back raw for its
// own id and only its own; a session that never published one is ""
// and listed; the read includes offline sessions, so a session closed a
// moment ago — the state a watcher re-open finds — still answers; and an
// id the list does not carry is "" and NOT listed, with no error, as a
// truncated list would be — the one case the caller must tell from an
// empty value, because the re-open then blanks what it could not see.
func TestOwnDescription(t *testing.T) {
	t.Parallel()
	c := fsClient(t)
	ctx := t.Context()
	setupTeam(t, c)

	alpha, err := c.Register(ctx, registration("alpha"))
	if err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	bravo, err := c.Register(ctx, registration("bravo"))
	if err != nil {
		t.Fatalf("register bravo: %v", err)
	}
	const raw = "migrating  the\tledger <b>"
	line := raw
	if _, err := c.Heartbeat(ctx, alpha.SessionID, &protocol.HeartbeatRequest{SessionDescription: &line}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got, listed, err := c.OwnDescription(ctx, alpha.SessionID); err != nil || !listed || got != raw {
		t.Fatalf("alpha's description = %q, listed %v, %v; want the raw %q, listed", got, listed, err, raw)
	}
	if got, listed, err := c.OwnDescription(ctx, bravo.SessionID); err != nil || !listed || got != "" {
		t.Fatalf("bravo's description = %q, listed %v, %v; want none, listed", got, listed, err)
	}
	if _, err := c.Close(ctx, alpha.SessionID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, listed, err := c.OwnDescription(ctx, alpha.SessionID); err != nil || !listed || got != raw {
		t.Fatalf("closed alpha's description = %q, listed %v, %v; want the raw %q, listed (offline included)", got, listed, err, raw)
	}
	if got, listed, err := c.OwnDescription(ctx, "no-such-session"); err != nil || listed || got != "" {
		t.Fatalf("an unlisted id = %q, listed %v, %v; want \"\", not listed, no error", got, listed, err)
	}
}
