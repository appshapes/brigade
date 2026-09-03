//go:build darwin || linux

package adapterclient

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// rawLine marshals a watch event to a compact NDJSON line for a script.
func rawLine(t *testing.T, v any) jsontext.Value {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal watch line: %v", err)
	}
	return b
}

// fakeMessage builds a valid MessageEnvelope for a watch `message` event.
func fakeMessage(id, body string) protocol.MessageEnvelope {
	return protocol.MessageEnvelope{
		ProtocolVersion:    protocol.ProtocolVersion,
		Kind:               protocol.KindText,
		MessageID:          id,
		TeamRef:            "team-1",
		Sender:             protocol.Sender{PrincipalRef: "p-1", SessionID: "s-sender", SessionName: "sender"},
		RecipientSessionID: "s1",
		Body:               body,
		HopCount:           0,
		CreatedAt:          time.Unix(1_700_000_000, 0).UTC(),
		DeliveryState:      protocol.DeliveryStateAccepted,
	}
}

// TestWatchReplayAndCommands is the StartWatch seam test (brief 2.2, B-4,
// B-5, B-6): a replay of a ready, three messages, an unknown event kind, a
// status with an unknown state and an over-long line, then honouring ack,
// heartbeat and close. The unknown kind and the unknown-state status are
// skipped, the over-long line is dropped, and every other event is
// delivered in order; close exits the child 0.
func TestWatchReplayAndCommands(t *testing.T) {
	t.Parallel()

	lines := []fakeadapter.WatchLine{
		{Raw: rawLine(t, &protocol.WatchReady{
			Event: protocol.EventReady, ProtocolVersion: protocol.ProtocolVersion,
			SessionID: "s1", Mode: protocol.WatchModePolling,
		})},
		{Raw: rawLine(t, &protocol.WatchMessage{Event: protocol.EventMessage, Message: fakeMessage("m1", "one")})},
		{Raw: rawLine(t, &protocol.WatchMessage{Event: protocol.EventMessage, Message: fakeMessage("m2", "two")})},
		{Raw: rawLine(t, &protocol.WatchMessage{Event: protocol.EventMessage, Message: fakeMessage("m3", "three")})},
		{Raw: rawLine(t, &protocol.WatchStatus{Event: protocol.EventStatus, State: protocol.StatusStateLive, Detail: "SUBSCRIBED"})},
		{Raw: rawLine(t, &protocol.WatchStatus{Event: protocol.EventStatus, State: "quantum"})},
		{Raw: jsontext.Value(`{"event":"nonsense","note":"a kind this version does not know"}`)},
		{Fill: (1 << 20) + 100}, // one over-long line the reader must drop
	}
	scriptPath := writeFakeScript(t, fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	c := fakeClient(t, scriptPath)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	w, err := c.StartWatch(ctx, "s1")
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	t.Cleanup(func() { cancel(); _, _ = w.Wait() })

	var events []Event
	done := make(chan struct{})
	go func() {
		for e := range w.Events() {
			events = append(events, e)
		}
		close(done)
	}()

	if err := w.Ack([]string{"m1", "m2"}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := w.Heartbeat(protocol.WatchCommand{}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	exit, werr := w.Wait()
	<-done
	if werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0 after a close command", exit)
	}

	wantKinds := []EventKind{KindReady, KindMessage, KindMessage, KindMessage, KindStatus, KindAcked, KindHeartbeatOK}
	if len(events) != len(wantKinds) {
		t.Fatalf("delivered %d events, want %d: %+v", len(events), len(wantKinds), kindsOf(events))
	}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Fatalf("event %d kind = %v, want %v (%v)", i, events[i].Kind, want, kindsOf(events))
		}
	}
	if events[0].Ready.SessionID != "s1" {
		t.Errorf("ready session = %q, want s1", events[0].Ready.SessionID)
	}
	for i, body := range []string{"one", "two", "three"} {
		if got := events[1+i].Message.Body; got != body {
			t.Errorf("message %d body = %q, want %q", i, got, body)
		}
	}
	if events[4].Status.State != protocol.StatusStateLive {
		t.Errorf("status state = %q, want live", events[4].Status.State)
	}
	if len(events[5].Acked.MessageIDs) != 2 {
		t.Errorf("acked ids = %v, want two", events[5].Acked.MessageIDs)
	}
	if events[6].HeartbeatOK.State == "" {
		t.Errorf("heartbeat_ok carried no state: %+v", events[6].HeartbeatOK)
	}
}

func kindsOf(events []Event) []EventKind {
	out := make([]EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

// TestWatchDeliversErrorEvent proves an `error` event reaches the caller
// with its retryable flag intact (P3-5 keys the watch's survival on it).
func TestWatchDeliversErrorEvent(t *testing.T) {
	t.Parallel()
	lines := []fakeadapter.WatchLine{
		{Raw: rawLine(t, &protocol.WatchReady{
			Event: protocol.EventReady, ProtocolVersion: protocol.ProtocolVersion,
			SessionID: "s1", Mode: protocol.WatchModePolling,
		})},
		{Raw: rawLine(t, &protocol.WatchError{
			Event: protocol.EventError,
			Error: protocol.ErrorObject{Code: protocol.CodeUnavailable, Message: "backend blip", Retryable: true},
		})},
		// No `retryable` member at all: absent means false (4.3).
		{Raw: jsontext.Value(`{"event":"error","error":{"code":"internal","message":"no flag"}}`)},
	}
	scriptPath := writeFakeScript(t, fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	c := fakeClient(t, scriptPath)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	w, err := c.StartWatch(ctx, "s1")
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	t.Cleanup(func() { cancel(); _, _ = w.Wait() })

	var events []Event
	done := make(chan struct{})
	go func() {
		for e := range w.Events() {
			events = append(events, e)
		}
		close(done)
	}()
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, werr := w.Wait(); werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	<-done

	var found []*protocol.ErrorObject
	for _, e := range events {
		if e.Kind == KindError {
			found = append(found, e.Error)
		}
	}
	if len(found) != 2 {
		t.Fatalf("delivered %d error events, want 2: %+v", len(found), kindsOf(events))
	}
	if found[0].Code != protocol.CodeUnavailable || !found[0].Retryable {
		t.Errorf("error event = %+v, want unavailable/retryable", found[0])
	}
	if found[1].Code != protocol.CodeInternal || found[1].Retryable {
		t.Errorf("error event without a retryable member = %+v, want internal and retryable=false", found[1])
	}
}

// TestWatchWaitAfterCancelWithUnreadEvents is the stop sequence P3-5 will
// run — cancel ctx, then Wait — taken with events still unread: it must
// not deadlock on the reader, and a child that exits 0 on the SIGTERM
// reports (0, nil), not the context error exec attaches to it.
func TestWatchWaitAfterCancelWithUnreadEvents(t *testing.T) {
	t.Parallel()
	lines := []fakeadapter.WatchLine{{Raw: rawLine(t, &protocol.WatchReady{
		Event: protocol.EventReady, ProtocolVersion: protocol.ProtocolVersion,
		SessionID: "s1", Mode: protocol.WatchModePolling,
	})}}
	for i := range 20 {
		lines = append(lines, fakeadapter.WatchLine{Raw: rawLine(t, &protocol.WatchMessage{
			Event: protocol.EventMessage, Message: fakeMessage("m"+strconv.Itoa(i), "body"),
		})})
	}
	scriptPath := writeFakeScript(t, fakeadapter.Script{Watch: &fakeadapter.WatchScript{Lines: lines}})
	c := fakeClient(t, scriptPath)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w, err := c.StartWatch(ctx, "s1")
	if err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	t.Cleanup(func() { cancel(); _, _ = w.Wait() })

	// Take exactly one event, then stop reading and stop the child.
	if first := <-w.Events(); first.Kind != KindReady {
		t.Fatalf("first event kind = %v, want ready", first.Kind)
	}
	cancel()

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, werr := w.Wait()
		done <- result{code, werr}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second): // a hang catcher, never a bound
		t.Fatal("Wait deadlocked on the events nobody read after the cancel")
	}
	if got.code != 0 || got.err != nil {
		t.Errorf("Wait after cancel = (%d, %v), want (0, nil): the fake exits 0 on SIGTERM", got.code, got.err)
	}
	// The stream is closed and nothing blocks a later range over it.
	for range w.Events() {
		t.Error("an event was delivered after the cancel")
	}
}

// TestWatchStartMissingExecutable proves a watch whose adapter binary does
// not exist fails at start as unavailable/adapter_not_found, not a nil
// Watch a caller would dereference.
func TestWatchStartMissingExecutable(t *testing.T) {
	t.Parallel()
	d := newDirs(t)
	c := &Client{
		Adapter:   config.Adapter{Argv: []string{filepath.Join(t.TempDir(), "nope")}, Source: config.SourceMap},
		Profile:   "default",
		ConfigDir: d.BrigadeConfig,
		StateDir:  d.BrigadeState,
		Environ:   []string{"PATH=/usr/bin:/bin"},
	}
	for name, argv := range map[string][]string{
		"absolute path":     {filepath.Join(t.TempDir(), "nope")},
		"bare name on PATH": {"brigade-not-a-real-binary-xyz"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := *c
			c.Adapter = config.Adapter{Argv: argv, Source: config.SourceMap}
			w, err := c.StartWatch(t.Context(), "s1")
			if w != nil {
				t.Fatalf("StartWatch returned a Watch for a missing executable")
			}
			perr := mustProtocolError(t, err)
			if perr.Code != protocol.CodeUnavailable {
				t.Errorf("code = %q, want unavailable", perr.Code)
			}
			if perr.Details["reason"] != "adapter_not_found" {
				t.Errorf("reason = %q, want adapter_not_found", perr.Details["reason"])
			}
		})
	}
}

// TestPassThroughForwardsStdioAndExit is the P3-3 seam test (6.4): the
// human terminal commands hand the adapter their own streams. Through the
// scripted fake, stdin reaches the child as its document, the child's
// envelope lands on the caller's stdout, its stderr on the caller's
// stderr, the exit status is the child's own (a conflict → 7), and the
// environment is still the from-scratch one — the dump proves the parent's
// hostile BRIGADE_PROFILE and the messaging token never crossed.
func TestPassThroughForwardsStdioAndExit(t *testing.T) {
	t.Parallel()
	const msgTok = "cc-messaging-secret-MUST-NOT-CROSS-pt"
	dump := filepath.Join(t.TempDir(), "dump.ndjson")
	scriptPath := writeFakeScript(t, fakeadapter.Script{
		DumpFile: dump,
		Responses: map[string][]fakeadapter.Response{
			"team join": {{
				Error:  &protocol.ErrorObject{Code: protocol.CodeConflict, Message: "already bound"},
				Stderr: "adapter-stderr-line",
			}},
		},
	})
	c := fakeClient(t, scriptPath)
	c.Environ = append(c.Environ, "CLAUDE_CODE_MESSAGING_TOKEN="+msgTok, "BRIGADE_PROFILE=evil")

	var stdout, stderr bytes.Buffer
	exit, err := c.PassThrough(t.Context(), "team", "join", []string{"--label", "x"},
		strings.NewReader(`{"join_secret":"brg1.t.s"}`), &stdout, &stderr)
	if err != nil {
		t.Fatalf("PassThrough: %v", err)
	}
	if exit != protocol.CodeConflict.Exit() {
		t.Errorf("exit = %d, want %d (the child's own status forwarded)", exit, protocol.CodeConflict.Exit())
	}
	if !strings.Contains(stdout.String(), `"code":"conflict"`) {
		t.Errorf("stdout = %q, want the adapter's own envelope", stdout.String())
	}
	if !strings.Contains(stderr.String(), "adapter-stderr-line") {
		t.Errorf("stderr = %q, want the adapter's stderr passed through", stderr.String())
	}

	rec, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	var inv fakeadapter.Invocation
	if err := json.Unmarshal(bytes.TrimSpace(rec), &inv); err != nil {
		t.Fatalf("dump line: %v (%q)", err, rec)
	}
	if inv.StdinBytes != len(`{"join_secret":"brg1.t.s"}`) {
		t.Errorf("stdin_bytes = %d, want the caller's document", inv.StdinBytes)
	}
	wantArgs := []string{"--label", "x"}
	if strings.Join(inv.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("args = %v, want %v", inv.Args, wantArgs)
	}
	if inv.Env["BRIGADE_PROFILE"] != "default" {
		t.Errorf("BRIGADE_PROFILE = %q, want the computed default, never the inherited value", inv.Env["BRIGADE_PROFILE"])
	}
	for name, value := range inv.Env {
		if strings.HasPrefix(name, "CLAUDE_CODE_MESSAGING_") || strings.Contains(value, msgTok) {
			t.Errorf("the token or its socket reached the child: %s", name)
		}
	}
}

// TestPassThroughMissingExecutable pins the one error the caller has to map
// itself: an adapter that cannot be started at all is `unavailable`
// adapter_not_found with status -1, never a forwarded status.
func TestPassThroughMissingExecutable(t *testing.T) {
	t.Parallel()
	d := newDirs(t)
	c := &Client{
		Adapter:   config.Adapter{Argv: []string{filepath.Join(t.TempDir(), "no-such-adapter")}, Source: config.SourceMap},
		Profile:   "default",
		ConfigDir: d.BrigadeConfig,
		StateDir:  d.BrigadeState,
		Environ:   []string{"PATH=/usr/bin:/bin"},
	}
	var stdout, stderr bytes.Buffer
	exit, err := c.PassThrough(t.Context(), "profile", "status", nil, nil, &stdout, &stderr)
	if exit != -1 {
		t.Errorf("exit = %d, want -1", exit)
	}
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != "adapter_not_found" {
		t.Fatalf("err = %v, want unavailable/adapter_not_found", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("streams = %q / %q, want both empty", stdout.String(), stderr.String())
	}
}
