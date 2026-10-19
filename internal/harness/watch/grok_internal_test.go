package watch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

func monitorWatcher(t *testing.T) *watcher {
	t.Helper()
	state := t.TempDir()
	store := sessionmap.Store{StateDir: state}
	m := &sessionmap.ByPID{ClaudePID: 4242, ClaudeSessionID: "02000000-0000-7000-8000-000000000001", BrigadeSessionID: "s", TeamRef: "t", TeamName: "ops", SessionName: "n", Inbound: "accept", FrameLevel: "open", TeamKey: "work", ConfigDir: "/c", Harness: "grok", Grok: &sessionmap.GrokReceiver{Cwd: "/owned", ProcessStart: "start", Incarnation: strings.Repeat("a", 32), MonitorState: "unarmed"}}
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteByHost("grok", m.ClaudeSessionID, &sessionmap.ByHost{HostPID: m.ClaudePID, BrigadeSessionID: m.BrigadeSessionID}); err != nil {
		t.Fatal(err)
	}
	sink := inbound.GrokSinkPath(state, "s", m.Grok.Incarnation, time.Now())
	if err := os.MkdirAll(filepath.Dir(sink), 0700); err != nil {
		t.Fatal(err)
	}
	return &watcher{store: store, grokReceiver: m, harness: "grok", hostPID: 4242, claudeStart: "start", rc: runConfig{env: config.WatcherEnv{StateDir: state, ClaudePID: 4242}, sink: sink}, deps: Deps{Clock: time.Now, Lookup: func(int) (procutil.Info, error) {
		return procutil.Info{Exists: true, Command: "grok", StartToken: "start"}, nil
	}}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}
func TestGrokSinkUsesLocalInjectionBoundary(t *testing.T) {
	t.Parallel()
	w := monitorWatcher(t)
	in := newInjector(w)
	item := inbound.Item{Content: "synthetic", MessageID: "m"}
	if err := in.inject(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(w.rc.sink)
	if err != nil || !strings.Contains(string(b), `"message_id":"m"`) {
		t.Fatalf("sink %q %v", b, err)
	}
	// Retirement while the process exists refuses another append.
	m := *w.grokReceiver
	g := *m.Grok
	g.Incarnation = strings.Repeat("b", 32)
	m.Grok = &g
	if err := w.store.WriteByPID(&m); err != nil {
		t.Fatal(err)
	}
	if err := in.inject(context.Background(), item); !errors.Is(err, errGrokReceiverChanged) {
		t.Fatalf("stale inject %v", err)
	}
	w.retireGrokReceiver()
	current, err := w.store.ReadByPID(4242)
	if err != nil || current.Grok.Incarnation != g.Incarnation {
		t.Fatal("old watcher retired replacement")
	}
}
func TestGrokSinkFailuresAndCrash(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"directory", "symlink", "public", "gone"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			w := monitorWatcher(t)
			switch kind {
			case "directory":
				if err := os.Mkdir(w.rc.sink, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "unrelated"), w.rc.sink); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.WriteFile(w.rc.sink, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(w.rc.sink, 0644); err != nil { //nolint:gosec // insecure owned sink is the refusal precondition
					t.Fatal(err)
				}
			case "gone":
				w.deps.Lookup = func(int) (procutil.Info, error) { return procutil.Info{}, nil }
			}
			if err := newInjector(w).inject(context.Background(), inbound.Item{Content: "synthetic", MessageID: "m"}); err == nil {
				t.Fatal("failed sink reported injected")
			}
			if kind == "gone" {
				w.retireGrokReceiver()
				if _, err := w.store.ReadByHost("grok", w.grokReceiver.ClaudeSessionID); err == nil {
					t.Fatal("dead receiver pointer remained")
				}
			}
		})
	}
}

type failingSink struct {
	writeErr, closeErr error
	short              bool
}

func (f failingSink) Write(p []byte) (int, error) {
	if f.short {
		return 0, nil
	}
	return len(p), f.writeErr
}
func (f failingSink) Close() error { return f.closeErr }
func TestGrokAppendFailuresAreNotSuccess(t *testing.T) {
	t.Parallel()
	e := errors.New("fixture")
	for _, f := range []failingSink{{writeErr: e}, {closeErr: e}, {short: true}} {
		if appendRecord(f, []byte("record\n")) == nil {
			t.Fatal("append boundary swallowed failure")
		}
	}
}

func TestGrokWatcherDoesNotBorrowClaudeLabel(t *testing.T) {
	t.Parallel()
	w := monitorWatcher(t)
	w.environ = []string{"HOME=" + t.TempDir(), "CLAUDE_CONFIG_DIR=/synthetic/unrelated"}
	if label := w.humanLabel("account"); label != "" {
		t.Fatal("borrowed Claude account label")
	}
}

func TestGrokLiveWatcherReplacementPreservesReceiver(t *testing.T) {
	t.Parallel()
	w := monitorWatcher(t)
	w.retireGrokReceiver()
	if err := w.checkGrokReceiver(); err != nil {
		t.Fatal("watch-only restart retired live receiver:", err)
	}
	w.deps.Lookup = func(int) (procutil.Info, error) {
		return procutil.Info{Exists: true, Command: "grok", StartToken: "reused"}, nil
	}
	if err := w.checkGrokReceiver(); err == nil {
		t.Fatal("PID reuse accepted")
	}
}

func TestGrokSinkFailureRetryAndDedupeACK(t *testing.T) {
	t.Parallel()
	w := monitorWatcher(t)
	now := time.Now()
	p, err := inbound.New(inbound.Config{Policy: policy.Accept, Host: "grok", Instruction: frame.Instruction{Level: frame.LevelOpen}, SessionID: "s", Clock: inbound.ClockFunc(func() time.Time { return now })})
	if err != nil {
		t.Fatal(err)
	}
	m := protocol.MessageEnvelope{ProtocolVersion: protocol.ProtocolVersion, Kind: protocol.KindText, MessageID: "m", TeamRef: "t", Sender: protocol.Sender{PrincipalRef: "p", SessionID: "sender", SessionName: "synthetic"}, RecipientSessionID: "s", Body: "synthetic body", CreatedAt: now, DeliveryState: protocol.DeliveryStateAccepted}
	if d := p.Offer(m); d.Outcome != inbound.OutcomeQueued {
		t.Fatalf("offer %+v", d)
	}
	if err := os.Mkdir(w.rc.sink, 0700); err != nil {
		t.Fatal(err)
	}
	in := newInjector(w)
	acks := p.Drain(func(it inbound.Item) error { return in.inject(context.Background(), it) })
	if len(acks) != 0 {
		t.Fatal("sink failure ACKed", acks)
	}
	if err := os.Remove(w.rc.sink); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if d := p.Offer(m); d.Ack || d.Outcome != inbound.OutcomeQueued {
		t.Fatalf("retry %+v", d)
	}
	acks = p.Drain(func(it inbound.Item) error { return in.inject(context.Background(), it) })
	if len(acks) != 1 || acks[0] != "m" {
		t.Fatal("successful local sink not ACKed", acks)
	}
	if d := p.Offer(m); !d.Ack || d.Outcome != inbound.OutcomeDuplicate {
		t.Fatalf("dedupe %+v", d)
	}
}
