package supabase

import (
	"bytes"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestWatchEventHelpers pins the event grammar the watch lane builds on
// (4.4.9): a fatal failure is one `error` event with retryable false and
// the code's exit status; a rejected stdin command is one `error` event
// with retryable TRUE and the loop goes on (BAP/1.x (a)); emit writes
// exactly one line per event; an unclassified error never leaks its text.
func TestWatchEventHelpers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := r.command("message", "watch")
	var out bytes.Buffer
	events := protocol.NewLineWriter(&out)

	if code := c.watchFatal(events, errNotMember()); code != 5 {
		t.Fatalf("watchFatal exit %d, want 5", code)
	}
	if code := c.watchRetryable(events, protocol.Lease{MinSeconds: 30, MaxSeconds: 600}.CheckSeconds("lease_seconds", ptr(5))); code != protocol.ExitOK {
		t.Fatalf("watchRetryable exit %d, want 0", code)
	}
	if code, done := c.emit(events, &protocol.WatchStatus{Event: protocol.EventStatus, State: protocol.StatusStatePolling}); code != 0 || done {
		t.Fatalf("emit = %d %v", code, done)
	}
	if code := c.watchFatal(events, assertErr("raw server text with a token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig")); code != 1 {
		t.Fatalf("unclassified exit %d, want 1", code)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %d: %s", len(lines), out.String())
	}
	var fatal, retry protocol.WatchError
	if err := protocol.Decode([]byte(lines[0]), &fatal); err != nil || fatal.Error.Code != protocol.CodeUnauthorized || fatal.Error.Retryable {
		t.Fatalf("fatal event = %s (%v)", lines[0], err)
	}
	if err := protocol.Decode([]byte(lines[1]), &retry); err != nil || retry.Error.Code != protocol.CodeInvalidInput || !retry.Error.Retryable {
		t.Fatalf("retryable event = %s (%v)", lines[1], err)
	}
	if !strings.Contains(lines[2], `"state":"polling"`) {
		t.Fatalf("status event = %s", lines[2])
	}
	if strings.Contains(lines[3], "eyJ") || !strings.Contains(lines[3], `"internal error"`) {
		t.Fatalf("an unclassified error leaked its text: %s", lines[3])
	}
}

func ptr(n int) *int { return &n }

// assertErr is a plain error with no protocol code.
type assertErr string

func (e assertErr) Error() string { return string(e) }
