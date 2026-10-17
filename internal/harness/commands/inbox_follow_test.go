package commands

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

func TestRenderRecord(t *testing.T) {
	t.Parallel()
	frame := "<brigade-message team=\"ops\" message-id=\"m1\">\npreamble\n----\nSender summary (untrusted): hi\nline one\nline two\n</brigade-message>"
	rec := `{"ts":"2026-10-17T02:00:00Z","frame":` + string(mustJSONString(frame)) + `,"message_id":"m1","sender_session_id":"s1"}`
	got := renderRecord([]byte(rec))
	want := "Brigade: team message m1 follows (7 lines)\n" + frame + "\nBrigade: end of message m1\n"
	if got != want {
		t.Fatalf("renderRecord =\n%q\nwant\n%q", got, want)
	}
	notice := `{"ts":"2026-10-17T02:00:00Z","frame":"Brigade: 3 messages from \"x\" were dropped\n","message_id":"","sender_session_id":"s1"}`
	if got := renderRecord([]byte(notice)); got != "Brigade: Brigade: 3 messages from \"x\" were dropped\n" {
		t.Fatalf("notice rendered as %q", got)
	}
	if got := renderRecord([]byte("not json")); !strings.HasPrefix(got, "Brigade: an inbox record could not be read") {
		t.Fatalf("bad record rendered as %q", got)
	}
	hostile := `{"ts":"2026-10-17T02:00:00Z","frame":"x","message_id":"m1\" evil=\"<x>\n","sender_session_id":"s1"}`
	if got := renderRecord([]byte(hostile)); strings.Contains(got, "<x>") || strings.Contains(got, `evil="`) {
		t.Fatalf("a hostile id reached the output: %q", got)
	}
}

func mustJSONString(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return b
}

// syncBuffer is a bytes.Buffer safe for a writer goroutine and a reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestInboxFollow runs the follower against a Muse session's state (card
// 88): it replays the newest sink from its beginning, follows appends,
// switches to a newer sink, and stops when the session's map is gone.
func TestInboxFollow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A Muse session resolves Brigade's directories from HOME alone (card
	// 88): HOME/.local/state/brigade, whatever XDG_* says.
	stateDir := filepath.Join(root, ".local", "state", "brigade")
	const id = "01a14785-947f-7d93-a4f4-6ee1f6b2be94"
	store := sessionmap.Store{StateDir: stateDir}
	m := sessionmap.ByPID{
		ClaudePID: 4242, ClaudeSessionID: id, BrigadeSessionID: "sess-1", TeamRef: "t", TeamName: "ops",
		SessionName: "n", Inbound: protocol.InboundAccept, FrameLevel: "open", TeamKey: "work",
		ConfigDir: "/c", AdapterCommand: []string{}, Harness: "muse",
	}
	if err := store.WriteByPID(&m); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteByHost("muse", id, &sessionmap.ByHost{HostPID: 4242, BrigadeSessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + root, "XDG_STATE_HOME=/nowhere", "MUSE_SESSION_ID=" + id}
	if err := os.MkdirAll(inbound.SinkDir(stateDir, "sess-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	first := inbound.NewSinkPath(stateDir, "sess-1", time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC))
	rec := func(id, body string) string {
		return `{"ts":"2026-10-17T02:00:00Z","frame":"` + body + `","message_id":"` + id + `","sender_session_id":"s1"}` + "\n"
	}
	out := &syncBuffer{}
	done := make(chan error, 1)
	// The follower starts BEFORE any sink exists — the watcher creates the
	// file at its first append, which under `hold` may be much later — and
	// must pick the file up when it appears, from its beginning.
	go func() {
		done <- Inbox(Invocation{Args: []string{"follow"}, Environ: env, Out: out}, InboxOptions{})
	}()
	time.Sleep(600 * time.Millisecond)
	if out.String() != "" {
		t.Fatalf("output before any sink: %q", out.String())
	}
	if err := os.WriteFile(first, []byte(rec("m1", "one")), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(out.String(), what) {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %q in %q", what, out.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("Brigade: team message m1 follows (1 lines)\none\nBrigade: end of message m1\n")
	// An append to the sink being followed.
	f, err := os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rec("m2", "two")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	waitFor("Brigade: end of message m2\n")
	// A newer sink (the watcher was replaced): read from its beginning.
	second := inbound.NewSinkPath(stateDir, "sess-1", time.Date(2026, 10, 17, 2, 0, 1, 0, time.UTC))
	if err := os.WriteFile(second, []byte(rec("m3", "three")), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor("Brigade: end of message m3\n")
	if got := out.String(); strings.Count(got, "follows") != 3 {
		t.Fatalf("records printed more than once: %q", got)
	}
	// The session ends: the map goes, the follower stops.
	if err := store.DeleteByPID(4242); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the follower did not stop after the map was removed")
	}
	if !strings.HasSuffix(out.String(), FollowEndedLine+"\n") {
		t.Fatalf("no ended line: %q", out.String())
	}
}

func TestInboxFollowRefusals(t *testing.T) {
	t.Parallel()
	if err := Inbox(Invocation{Args: []string{"follow"}, Environ: []string{"HOME=/h"}}, InboxOptions{}); err == nil {
		t.Fatal("follow outside a session was accepted")
	}
	if err := Inbox(Invocation{Args: []string{"follow", "x"}, Environ: []string{"HOME=/h", "MUSE_SESSION_ID=abc"}}, InboxOptions{}); err == nil || !strings.Contains(err.Error(), "no arguments") {
		t.Fatalf("follow with an argument: %v", err)
	}
	if err := Inbox(Invocation{Args: []string{"follow"}, Environ: []string{"HOME=/h", "MUSE_SESSION_ID=abc"}}, InboxOptions{All: true}); err == nil || !strings.Contains(err.Error(), "no flags") {
		t.Fatalf("follow with a flag: %v", err)
	}
}
