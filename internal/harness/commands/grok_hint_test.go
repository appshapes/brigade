package commands

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
)

type hintEvents chan time.Time

func (h hintEvents) Write(b []byte) (int, error) { h <- time.Now(); return len(b), nil }

func TestGrokHintsCoalesceAndEndWithTUI(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	state := filepath.Join(home, ".local/state/brigade")
	m := &sessionmap.ByPID{Harness: "grok", ClaudePID: 4242, ClaudeSessionID: "02000000-0000-7000-8000-000000000001", BrigadeSessionID: "s", TeamRef: "t", TeamName: "ops", SessionName: "grok", Inbound: "accept", FrameLevel: "open", TeamKey: "work", ConfigDir: "/c", Grok: &sessionmap.GrokReceiver{Cwd: "/owned", ProcessStart: "start", Incarnation: strings.Repeat("a", 32)}}
	store := sessionmap.Store{StateDir: state}
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteByHost("grok", m.ClaudeSessionID, &sessionmap.ByHost{HostPID: 4242, BrigadeSessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	path := inbound.GrokSinkPath(state, "s", m.Grok.Incarnation, time.Now())
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic owned bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var alive atomic.Bool
	alive.Store(true)
	events := make(hintEvents, 8)
	done := make(chan error, 1)
	f := &follower{stateDir: state, sessionID: "s", binding: m, inv: Invocation{Environ: []string{"HOME=" + home, "GROK_SESSION_ID=" + m.ClaudeSessionID}, Out: events, Deps: Deps{Getppid: func() int { return 4242 }, Lookup: func(int) (procutil.Info, error) {
		return procutil.Info{Exists: alive.Load(), Command: "grok", StartToken: "start"}, nil
	}}}}
	go func() { done <- f.run() }()
	t.Cleanup(func() {
		alive.Store(false)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("follower did not settle after TUI death")
		}
	})
	var first time.Time
	select {
	case first = <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("retained backlog did not wake")
	}
	// Many appends become one trailing hint; no body or delivery cursor is consumed.
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", i+100)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-events:
		t.Fatal("hint emitted before coalescing interval")
	case <-time.After(time.Second):
	}
	select {
	case next := <-events:
		if next.Sub(first) < 5*time.Second {
			t.Fatal("hint pacing violated")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("trailing change lost")
	}
	alive.Store(false)
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("receiver end was not reported")
	}
}
