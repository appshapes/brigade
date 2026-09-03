//go:build darwin || linux

package fakeadapter

import (
	"testing"
)

// TestNotifyTermReturnsALiveContext proves notifyTerm hands back a context
// that is not already cancelled (the watch loop selects on it) and a stop
// function that is safe to call. The SIGTERM-to-exit-0 behaviour itself is
// exercised across a real process boundary by the adapterclient watch
// tests; a unit test here would have to signal its own test process.
func TestNotifyTermReturnsALiveContext(t *testing.T) {
	t.Parallel()
	ctx, stop := notifyTerm()
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("notifyTerm's context was already cancelled")
	default:
	}
	stop()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("stop did not cancel the context")
	}
}
