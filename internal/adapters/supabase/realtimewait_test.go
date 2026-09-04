package supabase

// The bounded live-socket waits the integration tests in this package
// read Realtime through. They exist because an untimed read on a Phoenix
// socket is not bounded by the loop around it: the server closes a socket
// that never heartbeats — measured 60.007 s from the dial on the local
// stack on 2026-09-04, the ~66 s of E0-2 (docs/research/supabase-in-go.md
// line 146) on an older one — so a `for time.Now().Before(deadline)` that
// re-checks its window only BETWEEN iterations bounds the number of turns
// and not the blocking read. That is what turned a ten-second miss in
// TestIntegrationAdversarialBroadcastPayloadIsIdsOnly into a sixty-second
// `read: failed to get reader: failed to read frame header: EOF` under
// `make test`'s whole-tree -race load.
//
// The shape is the one realtime_integration_test.go measured and this
// file now owns for the whole package: ONE reader goroutine per socket
// feeding one channel, and every wait a select on time.After. A per-read
// context.WithTimeout is not an option — coder/websocket CLOSES the
// connection when a Read's context is cancelled, so the first timed-out
// wait would kill the socket for every wait after it (measured: "use of
// closed network connection").

import (
	"testing"
	"time"

	"github.com/coder/websocket"
)

// liveFrames starts the one reader goroutine for conn and returns the
// channel it feeds. The goroutine reads on t.Context(), which is
// cancelled only at cleanup, and closing the channel is how a waiter
// learns the socket died. Decode failures are dropped: the server sends
// frames no test asked about, and a wait's own window is the bound.
func liveFrames(t *testing.T, conn *websocket.Conn, vsn string) <-chan phxFrame {
	t.Helper()
	frames := make(chan phxFrame, 256)
	go func() {
		defer close(frames)
		for {
			typ, data, err := conn.Read(t.Context())
			if err != nil {
				return
			}
			if f, derr := decodePhxFrame(vsn, typ, data); derr == nil {
				select {
				case frames <- f:
				default:
				}
			}
		}
	}()
	return frames
}

// A frameWait is the outcome of one bounded wait: the two ways of not
// getting a frame are worth telling apart, because a closed socket and an
// expired window mean different things to whoever reads the failure.
type frameWait int

const (
	frameFound frameWait = iota
	frameTimedOut
	frameSocketClosed
)

// awaitFrame waits up to window for the first frame on ch matching want.
// It reports rather than fails, so a caller that retries can.
func awaitFrame(ch <-chan phxFrame, window time.Duration, want func(phxFrame) bool) (phxFrame, frameWait) {
	deadline := time.After(window)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				return phxFrame{}, frameSocketClosed
			}
			if want(f) {
				return f, frameFound
			}
		case <-deadline:
			return phxFrame{}, frameTimedOut
		}
	}
}

// mustAwaitFrame is [awaitFrame] for a wait with no retry: it fails
// naming what was waited for, on which topic and for how long.
func mustAwaitFrame(t *testing.T, ch <-chan phxFrame, what, topic string, window time.Duration, want func(phxFrame) bool) phxFrame {
	t.Helper()
	f, res := awaitFrame(ch, window, want)
	switch res {
	case frameSocketClosed:
		t.Fatalf("the socket closed before %s on %s", what, topic)
	case frameTimedOut:
		t.Fatalf("no %s on %s within %s", what, topic, window)
	case frameFound:
	}
	return f
}
