package commands

import (
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
)

type closedConsumer struct{}

func (closedConsumer) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestGrokFollowFramingAndClosedConsumer(t *testing.T) {
	t.Parallel()
	frame := "<brigade-message message-id=\"m\">\nuntrusted\n----\n$(touch /never)\n</brigade-message>"
	b, _ := json.Marshal(inbound.SinkRecord{MessageID: "m", Frame: frame})
	b = append(b, '\n')
	line := renderGrokRecord(b)
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("native event split into lines: %q", line)
	}
	if len(line) >= 400 || strings.Contains(line, "touch") || strings.Contains(line, frame) || !strings.Contains(line, "brigade_inbox_changed") {
		t.Fatal("unsafe or oversized wake notice")
	}
	f := &follower{inv: Invocation{Out: closedConsumer{}}}
	if !errors.Is(f.write("synthetic"), errFollowConsumerClosed) {
		t.Fatal("consumer failure swallowed")
	}
}
func TestGrokFollowerBindsIncarnationAndLiveness(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	state := filepath.Join(home, ".local/state/brigade")
	store := sessionmap.Store{StateDir: state}
	m := &sessionmap.ByPID{Harness: "grok", ClaudePID: 4242, ClaudeSessionID: "02000000-0000-7000-8000-000000000001", BrigadeSessionID: "s", TeamRef: "t", TeamName: "ops", SessionName: "grok", Inbound: "accept", FrameLevel: "open", TeamKey: "work", ConfigDir: "/c", Grok: &sessionmap.GrokReceiver{Cwd: "/owned", ProcessStart: "start", Incarnation: strings.Repeat("a", 32), MonitorState: "unarmed"}, PluginBin: "/owned/path with 'quotes'/brigade"}
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteByHost("grok", m.ClaudeSessionID, &sessionmap.ByHost{HostPID: m.ClaudePID, BrigadeSessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	lookup := func(int) (procutil.Info, error) {
		return procutil.Info{Exists: true, Command: "grok", StartToken: "start"}, nil
	}
	inv := Invocation{Environ: []string{"HOME=" + home, config.GrokSessionIDVar + "=" + m.ClaudeSessionID}, Deps: Deps{Lookup: lookup, Getppid: func() int { return 4242 }}}
	f := &follower{inv: inv, stateDir: state, sessionID: "s", binding: m}
	if !f.receiverValid() {
		t.Fatal("valid receiver rejected")
	}
	if got := m.MonitorCommand(); !strings.Contains(got, `'"'"'`) || !strings.Contains(got, "--incarnation "+m.Grok.Incarnation) {
		t.Fatalf("unsafe command %q", got)
	}
	for _, mutate := range []func(*sessionmap.ByPID){func(n *sessionmap.ByPID) { n.Grok.Incarnation = strings.Repeat("b", 32) }, func(n *sessionmap.ByPID) { n.Grok.Cwd = "/elsewhere" }, func(n *sessionmap.ByPID) { n.BrigadeSessionID = "other" }} {
		n := *m
		g := *m.Grok
		n.Grok = &g
		mutate(&n)
		if err := store.WriteByPID(&n); err != nil {
			t.Fatal(err)
		}
		if f.receiverValid() {
			t.Fatal("replacement accepted")
		}
	}
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	f.inv.Deps.Lookup = func(int) (procutil.Info, error) {
		return procutil.Info{Exists: true, Command: "grok", StartToken: "reused"}, nil
	}
	if f.receiverValid() {
		t.Fatal("PID reuse accepted")
	}
	// A closed stdout ends a real follower promptly rather than polling forever.
	sink := inbound.GrokSinkPath(state, "s", m.Grok.Incarnation, time.Now())
	if err := os.MkdirAll(filepath.Dir(sink), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sink, []byte("{\"message_id\":\"m\",\"frame\":\"x\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inv.Out = closedConsumer{}
	if err := inboxFollow(inv, []string{"--incarnation", m.Grok.Incarnation}); err != nil {
		t.Fatalf("closed consumer returned diagnostic: %v", err)
	}
}
