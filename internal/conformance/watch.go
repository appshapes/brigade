package conformance

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// watchEventBuffer bounds the events queued for Next before the reader
// blocks; C-40's 60 catch-up messages fit many times over.
const watchEventBuffer = 4096

// An Event is one NDJSON line of `message watch` stdout, parsed loosely
// into Raw and, for a known kind, decoded and validated into the typed
// member for that kind (the others stay nil).
type Event struct {
	Kind string
	// Line is the raw stdout line as the adapter wrote it, with the
	// terminator removed and nothing else changed. It is what the
	// byte-identity rules of 4.5.6/4.5.7 are asserted on: two refusals that
	// must be indistinguishable have to match here, not merely in the
	// members a parse recovers (C-37).
	Line        []byte
	Raw         map[string]any
	Ready       *protocol.WatchReady
	Message     *protocol.WatchMessage
	Status      *protocol.WatchStatus
	Acked       *protocol.WatchAcked
	HeartbeatOK *protocol.WatchHeartbeatOK
	Error       *protocol.WatchError
}

// A WatchProc is one running `message watch --session <id>`: stdin is a
// pipe the case writes NDJSON commands to, stdout is read line by line on
// a goroutine, stderr is captured. Every wait has a deadline and never
// blocks the case; the runner kills every WatchProc still alive when the
// case ends.
type WatchProc struct {
	t      *T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr bytes.Buffer
	events chan Event

	mu          sync.Mutex
	all         []Event
	exited      bool
	exit        int
	signalled   bool // the case sent a signal: its exit is the case's to assert
	reapKilled  bool // the runner killed it at the end of the case
	stdinClosed bool
	checked     bool
	done        chan struct{}
}

// newWatch spawns `message watch` with args for p. T.Watch supplies the
// ordinary --session form; T.WatchArgs is the seam for a case whose
// subject is the arguments themselves (C-37's control watch, which passes
// no --session at all).
func newWatch(t *T, p *Principal, args []string) (*WatchProc, error) {
	l := t.run.launcher
	argv := l.argv(append([]string{"message", "watch"}, args...))
	env := l.env(p, nil)
	cmd := exec.CommandContext(t.ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	w := &WatchProc{t: t, cmd: cmd, events: make(chan Event, watchEventBuffer), done: make(chan struct{})}
	cmd.Stderr = &w.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	w.stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	l.logf(t.c.ID, "watch started: "+commandName(argv[1:]))
	go w.read(stdout)
	return w, nil
}

// read is the stdout goroutine: every line is parsed, checked and queued,
// then the process is reaped.
func (w *WatchProc) read(stdout io.Reader) {
	reader := protocol.NewLineReader(stdout)
	for {
		line, err := reader.Next()
		switch {
		case errors.Is(err, protocol.ErrLineTooLong):
			w.t.Note("watch stdout: a line over 1 MiB was dropped by the reader (4.4.9)")
			continue
		case errors.Is(err, io.EOF):
			w.finish()
			return
		case err != nil:
			w.t.Logf("watch stdout read error: %v", err)
			w.finish()
			return
		}
		if len(line) == 0 {
			w.t.Errorf("C-33: watch stdout carries a blank line (4.4.9)")
			continue
		}
		w.t.Logf("watch stdout: %s", w.t.run.launcher.redact(preview(line)))
		ev, ok := parseEvent(w.t, line)
		if !ok {
			continue
		}
		w.mu.Lock()
		w.all = append(w.all, ev)
		w.mu.Unlock()
		select {
		case w.events <- ev:
		default:
			// Never block the reader: Events() still has it.
			w.t.Note("watch event queue full; a `%s` event was not queued for Next", ev.Kind)
		}
	}
}

// parseEvent turns one line into an Event. A line that is not a JSON
// object with a string `event` is a C-33 failure; a known kind that fails
// validation is a failure naming the event and the member; an unknown
// kind is logged and delivered with only Kind and Raw set (4.4.9).
func parseEvent(t *T, line []byte) (Event, bool) {
	raw := looseObject(line)
	if raw == nil {
		t.Errorf("C-33: watch stdout line is not a JSON object (4.4.9)")
		return Event{}, false
	}
	kind, ok := raw["event"].(string)
	if !ok || kind == "" {
		t.Errorf("C-33: watch stdout line has no string `event` member (4.4.9)")
		return Event{}, false
	}
	ev := Event{Kind: kind, Line: bytes.Clone(line), Raw: raw}
	var typed protocol.Validator
	switch kind {
	case protocol.EventReady:
		ev.Ready = &protocol.WatchReady{}
		typed = ev.Ready
	case protocol.EventMessage:
		ev.Message = &protocol.WatchMessage{}
		typed = ev.Message
	case protocol.EventStatus:
		ev.Status = &protocol.WatchStatus{}
		typed = ev.Status
	case protocol.EventAcked:
		ev.Acked = &protocol.WatchAcked{}
		typed = ev.Acked
	case protocol.EventHeartbeatOK:
		ev.HeartbeatOK = &protocol.WatchHeartbeatOK{}
		typed = ev.HeartbeatOK
	case protocol.EventError:
		ev.Error = &protocol.WatchError{}
		typed = ev.Error
	default:
		t.Logf("watch: unknown event kind %q ignored (4.4.9)", kind)
		return ev, true
	}
	if err := protocol.Decode(line, typed); err != nil {
		t.Errorf("watch `%s` event does not validate: %v", kind, err)
		ev.Ready, ev.Message, ev.Status, ev.Acked, ev.HeartbeatOK, ev.Error = nil, nil, nil, nil, nil, nil
	}
	return ev, true
}

// finish reaps the process after stdout closed and closes the queue.
func (w *WatchProc) finish() {
	err := w.cmd.Wait()
	w.mu.Lock()
	w.exited = true
	w.exit = -1
	if w.cmd.ProcessState != nil {
		w.exit = w.cmd.ProcessState.ExitCode()
	}
	w.mu.Unlock()
	if err != nil {
		w.t.Logf("watch exited: %v", err)
	} else {
		w.t.Logf("watch exited 0")
	}
	if s := w.stderr.Bytes(); len(s) > 0 {
		w.t.Logf("watch stderr: %s", w.t.run.launcher.redact(preview(s)))
		for _, v := range w.t.run.launcher.scanSecrets("message watch", "stderr", s) {
			w.t.Errorf("%s", v)
		}
	}
	close(w.events)
	close(w.done)
}

// Next returns the next event, or false when deadline passes or the
// process's stdout has ended. Every parsed line is delivered, unknown
// kinds included (Kind says which).
func (w *WatchProc) Next(deadline time.Duration) (Event, bool) {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case ev, ok := <-w.events:
		return ev, ok
	case <-timer.C:
		return Event{}, false
	}
}

// Expect reads events until one of kind arrives and returns it; other
// kinds are logged and skipped. Not seeing it within deadline (or before
// EOF) aborts the case.
func (w *WatchProc) Expect(kind string, deadline time.Duration) Event {
	until := time.Now().Add(deadline)
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			w.t.Fatalf("watch: no `%s` event within %s", kind, deadline)
		}
		ev, ok := w.Next(remaining)
		if !ok {
			if w.hasExited() {
				w.t.Fatalf("watch: exited (status %d) before a `%s` event", w.exitStatus(), kind)
			}
			w.t.Fatalf("watch: no `%s` event within %s", kind, deadline)
		}
		if ev.Kind == kind {
			return ev
		}
		w.t.Logf("watch: skipping `%s` while waiting for `%s`", ev.Kind, kind)
	}
}

// ExpectNone records a failure if any event other than an informational
// `status` or an unknown kind arrives within quiet (C-33).
func (w *WatchProc) ExpectNone(quiet time.Duration) {
	until := time.Now().Add(quiet)
	for {
		remaining := time.Until(until)
		if remaining <= 0 {
			return
		}
		ev, ok := w.Next(remaining)
		if !ok {
			return
		}
		switch ev.Kind {
		case protocol.EventReady, protocol.EventMessage, protocol.EventAcked, protocol.EventHeartbeatOK, protocol.EventError:
			w.t.Errorf("watch: unexpected `%s` event during a %s quiet period", ev.Kind, quiet)
		default:
			w.t.Logf("watch: `%s` event during the quiet period ignored", ev.Kind)
		}
	}
}

// Command writes v as one NDJSON line to the watch's stdin.
func (w *WatchProc) Command(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.t.Fatalf("watch command: marshal %T: %v", v, err)
	}
	w.WriteStdin(append(b, '\n'))
}

// WriteStdin writes raw bytes to the watch's stdin (B-6's over-long line,
// B-5's unknown type).
func (w *WatchProc) WriteStdin(b []byte) {
	w.mu.Lock()
	closed := w.stdinClosed
	w.mu.Unlock()
	if closed {
		w.t.Errorf("watch: write to stdin after CloseStdin")
		return
	}
	w.t.Logf("watch stdin (%d bytes): %s", len(b), w.t.run.launcher.redact(preview(b)))
	if _, err := w.stdin.Write(b); err != nil {
		w.t.Errorf("watch: stdin write failed: %v", err)
	}
}

// CloseStdin closes the watch's stdin (the EOF of C-38).
func (w *WatchProc) CloseStdin() {
	w.mu.Lock()
	already := w.stdinClosed
	w.stdinClosed = true
	w.mu.Unlock()
	if already {
		return
	}
	w.t.Logf("watch stdin closed")
	_ = w.stdin.Close()
}

// Signal sends sig to the watch. The launcher's exit-range check then no
// longer applies: the case asserts the exit itself through Wait.
func (w *WatchProc) Signal(sig os.Signal) {
	w.mu.Lock()
	w.signalled = true
	w.mu.Unlock()
	w.t.Logf("watch signalled: %v", sig)
	if err := w.cmd.Process.Signal(sig); err != nil && !w.hasExited() {
		w.t.Errorf("watch: signal %v: %v", sig, err)
	}
}

// Kill sends SIGKILL.
func (w *WatchProc) Kill() { w.Signal(syscall.SIGKILL) }

// Wait waits for the process to exit and returns its status (-1 when it
// died by a signal). On deadline it kills the process and returns ok
// false. The B-11 exit-range check runs here once, unless the case
// signalled the process.
func (w *WatchProc) Wait(deadline time.Duration) (exit int, ok bool) {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case <-w.done:
		w.checkExit()
		return w.exitStatus(), true
	case <-timer.C:
		w.t.Logf("watch: still running after %s; killing", deadline)
		w.Kill()
		<-w.done
		return w.exitStatus(), false
	}
}

// checkExit applies the B-11 range check once. A case that signalled the
// watch asserts the exit itself; the runner's own end-of-case kill exempts
// only the signal death it caused, never a status the watch chose.
func (w *WatchProc) checkExit() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.checked || !w.exited || w.signalled || (w.reapKilled && w.exit < 0) {
		return
	}
	w.checked = true
	if w.exit < 0 || w.exit > maxExit {
		w.t.Errorf("message watch: exit status %d is outside 0..12 (B-11)", w.exit)
	}
}

func (w *WatchProc) hasExited() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exited
}

func (w *WatchProc) exitStatus() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exit
}

// Events returns everything seen so far, in arrival order.
func (w *WatchProc) Events() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Event, len(w.all))
	copy(out, w.all)
	return out
}

// reap is the runner's end-of-case cleanup: a watch still running is
// killed without a failure; one that exited on its own gets the exit
// check.
func (w *WatchProc) reap() {
	select {
	case <-w.done:
		w.checkExit()
		return
	default:
	}
	w.t.Logf("watch still running at the end of the case; killing it")
	w.mu.Lock()
	w.reapKilled = true
	w.mu.Unlock()
	_ = w.cmd.Process.Kill()
	select {
	case <-w.done:
		w.checkExit()
	case <-time.After(waitDelay + time.Second):
		w.t.Logf("watch did not reap after SIGKILL")
	}
}
