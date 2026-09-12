package conformance

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

const (
	readyLine   = `{"event":"ready","protocol_version":"1","session_id":"s1","mode":"polling"}`
	messageLine = `{"event":"message","message":{"protocol_version":"1","kind":"text","message_id":"m1","team_ref":"t","sender":{"principal_ref":"p","session_id":"a","session_name":"a"},"recipient_session_id":"s1","body":"hi","hop_count":0,"created_at":"2026-08-30T12:00:00Z","delivery_state":"accepted"}}`
	unknownLine = `{"event":"frobnicate","x":1}`
	statusLine  = `{"event":"status","state":"polling"}`
	ackedLine   = `{"event":"acked","message_ids":["m1"],"unknown":[]}`
)

// fakeWatch is an adapter whose `message watch` prints the given lines,
// then echoes each stdin line back as an `acked` event until EOF, then
// exits with status.
func fakeWatch(tb testing.TB, lines []string, exit int) string {
	tb.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("printf '%s\\n' '" + l + "'\n")
	}
	b.WriteString("while IFS= read -r line; do printf '%s\\n' '" + ackedLine + "'; done\n")
	b.WriteString("exit " + strconv.Itoa(exit) + "\n")
	return writeScript(tb, "watch", b.String())
}

func watchCase(t *testing.T, adapter string, body func(*T, *WatchProc)) CaseResult {
	t.Helper()
	r := newTestRunner(t, viaShell(adapter, Options{}), testEnviron)
	return runFake(t, r, func(ct *T) {
		w := ct.Watch(ct.Scratch("x"), "s1")
		body(ct, w)
	})
}

// awaitExit waits for the fake to exit through Wait and turns a hang into
// the case's failure. Wait's own timeout path kills the process, which
// marks it signalled and skips the B-11 check, so a case that ignored
// Wait's ok read "pass" off a fake that had never printed a line: 7 of 20
// loaded runs said `exit 127 waited: pass ""` and 6 LineDiscipline
// subtests `status pass, reason ""` (2026-09-11). A fake that has not
// exited by fakeDeadline is now reported as exactly that.
func awaitExit(ct *T, w *WatchProc) int {
	exit, ok := w.Wait(fakeDeadline)
	if !ok {
		ct.Fatalf("the fake did not exit within %s", fakeDeadline)
	}
	return exit
}

func TestWatchEventsAreTypedAndUnknownKindsIgnored(t *testing.T) {
	t.Parallel()
	res := watchCase(t, fakeWatch(t, []string{readyLine, unknownLine, statusLine, messageLine}, 0), func(ct *T, w *WatchProc) {
		ready := w.Expect(protocol.EventReady, fakeDeadline)
		if ready.Ready == nil || ready.Ready.Mode != protocol.WatchModePolling || ready.Raw["session_id"] != "s1" {
			ct.Errorf("ready: %+v", ready)
		}
		msg := w.Expect(protocol.EventMessage, fakeDeadline)
		if msg.Message == nil || msg.Message.Message.MessageID != "m1" {
			ct.Errorf("message: %+v", msg)
		}
		// Nothing can arrive here whatever the machine does: the fake has
		// printed everything it has and answers only stdin, still silent.
		w.ExpectNone(300 * time.Millisecond)
		w.Command(protocol.WatchCommand{Type: protocol.CommandAck, MessageIDs: []string{"m1"}})
		acked := w.Expect(protocol.EventAcked, fakeDeadline)
		if acked.Acked == nil || len(acked.Acked.MessageIDs) != 1 {
			ct.Errorf("acked: %+v", acked)
		}
		w.CloseStdin()
		if exit := awaitExit(ct, w); exit != 0 {
			ct.Errorf("exit %d", exit)
		}
		kinds := make([]string, 0)
		for _, ev := range w.Events() {
			kinds = append(kinds, ev.Kind)
		}
		if got := strings.Join(kinds, ","); got != "ready,frobnicate,status,message,acked" {
			ct.Errorf("events seen: %s", got)
		}
	})
	if res.Status != StatusPass {
		t.Fatalf("%s %q", res.Status, res.Reason)
	}
}

func TestWatchLineDisciplineFailures(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		lines []string
		want  string
	}{
		"bare text":       {[]string{"starting up", readyLine}, "C-33: watch stdout line is not a JSON object"},
		"no event member": {[]string{`{"ready":true}`}, "C-33: watch stdout line has no string `event` member"},
		"invalid ready":   {[]string{`{"event":"ready","protocol_version":"1","session_id":"s1","mode":"carrier-pigeon"}`}, "watch `ready` event does not validate"},
		"blank line":      {[]string{"", readyLine}, "C-33: watch stdout carries a blank line"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := watchCase(t, fakeWatch(t, tc.lines, 0), func(ct *T, w *WatchProc) {
				w.CloseStdin()
				awaitExit(ct, w)
			})
			if res.Status != StatusFail || !strings.Contains(res.Reason, tc.want) {
				t.Fatalf("status %s, reason %q; want %q", res.Status, res.Reason, tc.want)
			}
		})
	}
}

func TestWatchExpectDeadlineAbortsAndExpectNoneFlags(t *testing.T) {
	t.Parallel()
	// The deadline is the subject here and the input is fixed: this fake
	// never says `message`, so the 300 ms elapse whatever the machine is
	// doing and the abort names them.
	res := watchCase(t, fakeWatch(t, []string{readyLine}, 0), func(ct *T, w *WatchProc) {
		w.Expect(protocol.EventMessage, 300*time.Millisecond)
		ct.Errorf("not reached")
	})
	if res.Status != StatusFail || !strings.Contains(res.Reason, "no `message` event within 300ms") || strings.Contains(res.Reason, "not reached") {
		t.Fatalf("%s %q", res.Status, res.Reason)
	}
	// ExpectNone's flagging is asserted on a queue that is already full
	// and closed: the fake has exited before ExpectNone runs, so `message`
	// is waiting behind `ready` and the quiet period ends at EOF rather
	// than on the clock. Read live, the 500 ms were a stopwatch between the
	// fake's two printf statements, and a sh descheduled between them
	// passed the wrong way.
	res = watchCase(t, fakeWatch(t, []string{readyLine, messageLine}, 0), func(ct *T, w *WatchProc) {
		w.CloseStdin()
		awaitExit(ct, w)
		w.Expect(protocol.EventReady, fakeDeadline)
		w.ExpectNone(500 * time.Millisecond)
	})
	if res.Status != StatusFail || !strings.Contains(res.Reason, "unexpected `message` event") {
		t.Fatalf("%s %q", res.Status, res.Reason)
	}
}

func TestWatchExitRangeAndSignals(t *testing.T) {
	t.Parallel()
	res := watchCase(t, fakeWatch(t, []string{readyLine}, 9), func(ct *T, w *WatchProc) {
		w.CloseStdin()
		awaitExit(ct, w)
	})
	if res.Status != StatusPass {
		t.Fatalf("exit 9: %s %q", res.Status, res.Reason)
	}
	// A watch that exits on its own outside 0..12 is a B-11 failure, whether
	// the case waits for it or the runner reaps it.
	oneTwentySeven := writeScript(t, "watch127", "printf '%s\\n' '"+readyLine+"'; exit 127")
	res = watchCase(t, oneTwentySeven, func(ct *T, w *WatchProc) { awaitExit(ct, w) })
	if res.Status != StatusFail || !strings.Contains(res.Reason, "exit status 127 is outside 0..12 (B-11)") {
		t.Fatalf("exit 127 waited: %s %q", res.Status, res.Reason)
	}
	// The reaped arm needs a process that has ALREADY exited by itself when
	// the case returns, without Wait (whose B-11 check is the arm above's):
	// the case waits on the reader's done signal directly. A 200 ms sleep
	// stood here, racing the sh's `exit 127` against the runner's kill,
	// whose reapKilled exemption would have read the lost race as a pass.
	res = watchCase(t, oneTwentySeven, func(ct *T, w *WatchProc) {
		w.Expect(protocol.EventReady, fakeDeadline)
		select {
		case <-w.done:
		case <-time.After(fakeDeadline):
			ct.Fatalf("the fake did not exit within %s", fakeDeadline)
		}
	})
	if res.Status != StatusFail || !strings.Contains(res.Reason, "(B-11)") {
		t.Fatalf("exit 127 reaped: %s %q", res.Status, res.Reason)
	}
	// A signal the case sent is not a failure; Wait reports -1.
	res = watchCase(t, fakeWatch(t, []string{readyLine}, 0), func(ct *T, w *WatchProc) {
		w.Expect(protocol.EventReady, fakeDeadline)
		w.Kill()
		if exit := awaitExit(ct, w); exit != -1 {
			ct.Errorf("after Kill: exit %d", exit)
		}
	})
	if res.Status != StatusPass {
		t.Fatalf("killed: %s %q", res.Status, res.Reason)
	}
	res = watchCase(t, fakeWatch(t, []string{readyLine}, 0), func(ct *T, w *WatchProc) {
		w.Expect(protocol.EventReady, fakeDeadline)
		w.Signal(syscall.SIGTERM)
		awaitExit(ct, w)
	})
	if res.Status != StatusPass {
		t.Fatalf("SIGTERM: %s %q", res.Status, res.Reason)
	}
	// A watch still running at the end of the case is killed, not failed.
	res = watchCase(t, fakeWatch(t, []string{readyLine}, 0), func(_ *T, w *WatchProc) { w.Expect(protocol.EventReady, fakeDeadline) })
	if res.Status != StatusPass {
		t.Fatalf("left running: %s %q", res.Status, res.Reason)
	}
}

func TestWatchWaitDeadlineKills(t *testing.T) {
	t.Parallel()
	res := watchCase(t, fakeWatch(t, []string{readyLine}, 0), func(ct *T, w *WatchProc) {
		start := time.Now()
		if _, ok := w.Wait(300 * time.Millisecond); ok {
			ct.Errorf("Wait reported an exit while stdin was open")
		}
		// Wait's timeout path kills the process and waits for the reap.
		// SIGKILL-to-reap latency is the kernel's (the load logs put a 5 s
		// Wait plus one kill and reap at 6.7–9.9 s), so the bound on it is
		// the hang catcher, not a promptness claim.
		if time.Since(start) > fakeDeadline {
			ct.Errorf("Wait did not return after its deadline")
		}
	})
	if res.Status != StatusPass {
		t.Fatalf("%s %q", res.Status, res.Reason)
	}
}

func TestWatchOverlongLineIsNoted(t *testing.T) {
	t.Parallel()
	long := writeScript(t, "long", "printf '%s\\n' '"+readyLine+"'; head -c 1100000 /dev/zero | tr '\\0' 'x'; printf '\\n%s\\n' '"+statusLine+"'")
	res := watchCase(t, long, func(_ *T, w *WatchProc) {
		w.Expect(protocol.EventReady, fakeDeadline)
		w.Expect(protocol.EventStatus, fakeDeadline)
	})
	if res.Status != StatusPass || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "over 1 MiB") {
		t.Fatalf("%s %q notes %v", res.Status, res.Reason, res.Notes)
	}
}
