package syncthing

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

const hangCatcher = 30 * time.Second

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// watcher drives one `message watch` in-process.
type watcher struct {
	t     *testing.T
	stdin *io.PipeWriter
	lines chan []byte
	done  chan int
}

func startWatch(t *testing.T, r *rig, args ...string) *watcher {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	w := &watcher{t: t, stdin: stdinW, lines: make(chan []byte, 128), done: make(chan int, 1)}
	go func() {
		code := run(args, stdinR, stdoutW, &syncBuffer{}, r.env, r.clock(), r.deps)
		_ = stdoutW.Close()
		w.done <- code
	}()
	go func() {
		defer close(w.lines)
		reader := protocol.NewLineReader(stdoutR)
		for {
			line, err := reader.Next()
			if err != nil {
				return
			}
			w.lines <- append([]byte(nil), line...)
		}
	}()
	t.Cleanup(func() {
		_ = stdinW.Close()
		_ = stdinR.Close()
	})
	return w
}

func (w *watcher) next() map[string]any {
	w.t.Helper()
	select {
	case line, ok := <-w.lines:
		if !ok {
			w.t.Fatal("the watch closed stdout before the expected event")
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			w.t.Fatalf("a watch line is not JSON: %v (%q)", err, line)
		}
		return event
	case <-time.After(hangCatcher):
		w.t.Fatalf("no watch event within %s", hangCatcher)
		return nil
	}
}

func (w *watcher) exit() int {
	w.t.Helper()
	select {
	case code := <-w.done:
		return code
	case <-time.After(hangCatcher):
		w.t.Fatalf("the watch did not exit within %s", hangCatcher)
		return -1
	}
}

// TestWatchDeliversAndEndsOnRevocation: the watch says polling, delivers
// a message written to the shared store by another profile, answers an
// ack command, and ends with one unauthorized error when the creator's
// marker appears.
func TestWatchDeliversAndEndsOnRevocation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, secret := r.team("alice", "ops")
	r.join("bob", secret)
	a := r.register("alice", "a")
	b := r.register("bob", "b")
	w := startWatch(t, r, "--profile", "bob", "message", "watch", "--session", b)
	if ready := w.next(); ready["event"] != "ready" || ready["mode"] != "polling" {
		t.Fatalf("first event = %v", ready)
	}
	sent := r.send("alice", a, b, "over the wire")
	got := w.next()
	message, _ := got["message"].(map[string]any)
	if got["event"] != "message" || message["message_id"] != sent["message_id"] {
		t.Fatalf("event = %v", got)
	}
	if _, err := io.WriteString(w.stdin, `{"type":"ack","message_ids":["`+str(t, sent, "message_id")+`"]}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if acked := w.next(); acked["event"] != "acked" {
		t.Fatalf("event = %v", acked)
	}
	r.ok("", "--profile", "alice", "team", "revoke-member", "--principal", r.principalOf("bob"))
	if errEvent := w.next(); errEvent["event"] != "error" {
		t.Fatalf("event = %v", errEvent)
	} else if object, _ := errEvent["error"].(map[string]any); object["code"] != "unauthorized" || object["retryable"] != false {
		t.Fatalf("error = %v", object)
	}
	if code := w.exit(); code != 5 {
		t.Fatalf("exit = %d, want 5", code)
	}
}

// TestWatchWithoutAnEngineStillWatches: in engine mode with an engine
// that cannot start, the watch runs on the replica alone.
func TestWatchWithoutAnEngineStillWatches(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, _ = r.team("alice", "ops")
	a := r.register("alice", "a")
	r.env = r.env[:3]
	r.env = append(r.env, storeVar+"="+r.root)
	w := startWatch(t, r, "--profile", "alice", "message", "watch", "--session", a)
	if ready := w.next(); ready["event"] != "ready" {
		t.Fatalf("first event = %v", ready)
	}
	_ = w.stdin.Close()
	if code := w.exit(); code != 0 {
		t.Fatalf("exit = %d", code)
	}
}
