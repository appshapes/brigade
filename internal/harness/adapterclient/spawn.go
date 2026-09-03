// spawn.go holds the ONE exec.CommandContext in the harness (plan 7.3,
// 6.6): the long-running `message watch` child, which needs stdin/stdout
// pipes the request/response path does not. It uses syscall.SIGTERM to
// cancel, so it carries the darwin || linux constraint (D33) that makes a
// third OS fail to build rather than link a watcher that cannot stop its
// child.

//go:build darwin || linux

package adapterclient

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// watchWaitDelay is how long the child may outlive a SIGTERM before SIGKILL
// and pipe closure (6.6).
const watchWaitDelay = 5 * time.Second

// An EventKind names which watch event a decoded [Event] carries.
type EventKind int

// The event kinds a Watch delivers. Unknown wire kinds are never
// delivered — they are logged and skipped (B-5).
const (
	KindReady EventKind = iota + 1
	KindMessage
	KindStatus
	KindAcked
	KindHeartbeatOK
	KindError
)

// An Event is one decoded, validated `message watch` event (4.4.9).
// Exactly one typed member is set, selected by Kind. The supervision,
// backoff and restart policy of 6.6 is P3-5's; this seam only decodes and
// delivers.
type Event struct {
	Kind        EventKind
	Ready       *protocol.WatchReady
	Message     *protocol.MessageEnvelope
	Status      *protocol.WatchStatus
	Acked       *protocol.WatchAcked
	HeartbeatOK *protocol.WatchHeartbeatOK
	Error       *protocol.ErrorObject
}

// A Watch is one running `message watch` child. Events are delivered on
// [Watch.Events] until the stream ends; commands are written with
// [Watch.Ack], [Watch.Heartbeat] and [Watch.Close]; [Watch.Wait] reaps the
// child and reports its exit status. Cancel the ctx passed to StartWatch to
// stop the child (SIGTERM, then SIGKILL after five seconds).
//
// Delivery is unbuffered: the reader blocks until the caller takes each
// event. Once ctx is cancelled the reader stops delivering and drains the
// stream to EOF instead, so the stop sequence — cancel, then Wait — never
// deadlocks on events the caller no longer reads; a message that was not
// delivered is not acknowledged and the server redelivers it. A caller
// that has NOT cancelled ctx must consume Events until it closes before
// Wait can return, exactly as with an exec.Cmd stdout pipe.
type Watch struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	writer *protocol.LineWriter
	events chan Event
	stop   <-chan struct{} // ctx.Done(): the caller is stopping the child
	log    *slog.Logger
	closer io.Closer // the adapter log, closed on Wait

	writeMu sync.Mutex
	done    chan struct{}

	waitOnce sync.Once
	waitCode int
	waitErr  error
}

// StartWatch spawns `message watch --session <id>` with stdin and stdout
// pipes and the same from-scratch environment as [Client.Call]. It returns
// once the child has started; events arrive on Events.
func (c *Client) StartWatch(ctx context.Context, sessionID string) (*Watch, error) {
	argv, err := c.argv("message", "watch", sessionFlag(sessionID))
	if err != nil {
		return nil, err
	}

	//nolint:forbidigo // adapterclient/spawn.go IS the one watch-spawn seam the 7.3 rule allows (6.6)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = c.childEnv()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = watchWaitDelay

	// The child's stderr goes to the adapter log, never a surfaced stream
	// (U-24); a *os.File is passed straight to the child, so nothing here
	// has to drain a third pipe.
	logWriter := c.adapterLog()
	if logWriter != nil {
		cmd.Stderr = logWriter
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		closeQuietly(logWriter)
		return nil, pipeFailure()
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		closeQuietly(logWriter)
		return nil, pipeFailure()
	}
	if serr := cmd.Start(); serr != nil {
		closeQuietly(logWriter)
		return nil, startFailure(serr)
	}

	w := &Watch{
		cmd:    cmd,
		stdin:  stdinPipe,
		writer: protocol.NewLineWriter(stdinPipe),
		events: make(chan Event),
		stop:   ctx.Done(),
		log:    c.logger(),
		closer: logWriter,
		done:   make(chan struct{}),
	}
	go w.readEvents(stdoutPipe)
	return w, nil
}

// Events delivers each decoded event. The channel closes when the child's
// stdout reaches EOF or fails.
func (w *Watch) Events() <-chan Event { return w.events }

// readEvents decodes the child's NDJSON stdout, dropping over-long lines
// (B-6), unknown event kinds (B-5) and undecodable lines, and closes the
// events channel at end of stream. After the caller cancels ctx nothing
// more is delivered: the stream is read to EOF so the child can be reaped
// without a reader blocked on an event nobody will take.
func (w *Watch) readEvents(stdout io.Reader) {
	defer close(w.events)
	defer close(w.done)
	reader := protocol.NewLineReader(stdout)
	stopping := false
	for {
		line, err := reader.Next()
		switch {
		case errors.Is(err, protocol.ErrLineTooLong):
			w.log.Warn("watch line dropped", slog.String("reason", "line_too_long"))
			continue
		case errors.Is(err, io.EOF):
			return
		case err != nil:
			w.log.Warn("watch stream ended", slog.String("reason", "read_error"))
			return
		}
		event, ok := decodeEvent(line, w.log)
		if !ok || stopping {
			continue
		}
		select {
		case w.events <- event:
		case <-w.stop:
			stopping = true
			w.log.Debug("watch event not delivered", slog.String("reason", "stopping"))
		}
	}
}

// decodeEvent decodes one NDJSON line by its `event` discriminator. An
// unknown kind, an undecodable line and a status with an unknown state are
// logged and skipped (B-4, B-5).
func decodeEvent(line []byte, log *slog.Logger) (Event, bool) {
	var disc struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(line, &disc); err != nil {
		log.Warn("watch line ignored", slog.String("reason", "malformed"))
		return Event{}, false
	}
	switch disc.Event {
	case protocol.EventReady:
		var e protocol.WatchReady
		if !decodeInto(line, &e, log) {
			return Event{}, false
		}
		return Event{Kind: KindReady, Ready: &e}, true
	case protocol.EventMessage:
		var e protocol.WatchMessage
		if !decodeInto(line, &e, log) {
			return Event{}, false
		}
		msg := e.Message
		return Event{Kind: KindMessage, Message: &msg}, true
	case protocol.EventStatus:
		var e protocol.WatchStatus
		if !decodeInto(line, &e, log) {
			return Event{}, false
		}
		if !e.Known() {
			// An unknown transport state is ignored (B-4): informational
			// and never acted on, so it is not delivered.
			log.Debug("watch status ignored", slog.String("reason", "unknown_state"))
			return Event{}, false
		}
		return Event{Kind: KindStatus, Status: &e}, true
	case protocol.EventAcked:
		var e protocol.WatchAcked
		if !decodeInto(line, &e, log) {
			return Event{}, false
		}
		return Event{Kind: KindAcked, Acked: &e}, true
	case protocol.EventHeartbeatOK:
		var e protocol.WatchHeartbeatOK
		if !decodeInto(line, &e, log) {
			return Event{}, false
		}
		return Event{Kind: KindHeartbeatOK, HeartbeatOK: &e}, true
	case protocol.EventError:
		var e protocol.WatchError
		if !decodeInto(line, &e, log) {
			return Event{}, false
		}
		errObj := e.Error
		return Event{Kind: KindError, Error: &errObj}, true
	default:
		log.Warn("watch event skipped", slog.String("reason", "unknown_kind"))
		return Event{}, false
	}
}

// decodeInto runs the loose parse plus the shape's Validate, logging and
// skipping a line that fails.
func decodeInto(line []byte, v protocol.Validator, log *slog.Logger) bool {
	if err := protocol.Decode(line, v); err != nil {
		log.Warn("watch event skipped", slog.String("reason", "invalid"))
		return false
	}
	return true
}

// Ack writes an `ack` command on the child's stdin.
func (w *Watch) Ack(ids []string) error {
	return w.send(&protocol.WatchCommand{Type: protocol.CommandAck, MessageIDs: ids})
}

// Heartbeat writes a `heartbeat` command; Type is set for the caller.
func (w *Watch) Heartbeat(cmd protocol.WatchCommand) error {
	cmd.Type = protocol.CommandHeartbeat
	return w.send(&cmd)
}

// Close writes a `close` command, which ends the watch (the child exits 0).
func (w *Watch) Close() error {
	return w.send(&protocol.WatchCommand{Type: protocol.CommandClose})
}

// send writes one NDJSON command line under the write mutex.
func (w *Watch) send(cmd *protocol.WatchCommand) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.writer.WriteLine(cmd)
}

// Wait waits for the event stream to end (the caller has consumed it, or
// ctx was cancelled and the reader drained it), reaps the child and
// returns its exit status. A child that exited by itself — 0 or a 4.6
// status, including a 0 after the SIGTERM a cancel sends — reports that
// status with a nil error. (-1, err) means the child did NOT exit by
// itself: it was killed by a signal (the SIGKILL that follows a cancel by
// WaitDelay, or a death of its own) or the spawn failed at wait level.
// Wait is idempotent, so a test may both call it and register it in a
// cleanup; it must be called, since it is what reaps the child and closes
// the adapter log.
func (w *Watch) Wait() (int, error) {
	w.waitOnce.Do(func() {
		<-w.done
		err := w.cmd.Wait()
		closeQuietly(w.closer)
		// The exit status is read from the process state, not from err:
		// after a cancel, exec reports ctx.Err() even for a child that
		// exited 0 on the SIGTERM, and that clean exit is what P3-5's
		// restart policy must see.
		state := w.cmd.ProcessState
		if state == nil || state.ExitCode() < 0 {
			w.waitCode, w.waitErr = -1, err
			return
		}
		w.waitCode = state.ExitCode()
	})
	return w.waitCode, w.waitErr
}

// closeQuietly closes c when it is non-nil.
func closeQuietly(c io.Closer) {
	if c != nil {
		_ = c.Close()
	}
}

// pipeFailure and startFailure map a pipe or start error to `unavailable`,
// the same class Spawn gives a child it could not run.
func pipeFailure() error {
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: "cannot open the watch child's pipes",
		Details: map[string]string{"reason": "watch_pipe"},
	}
}

func startFailure(err error) error {
	reason := "watch_start"
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		reason = "adapter_not_found"
	}
	return &protocol.Error{
		Code:    protocol.CodeUnavailable,
		Message: "cannot start the watch child",
		Details: map[string]string{"reason": reason},
	}
}

// --- pass-through -------------------------------------------------------------

// PassThrough runs one adapter command with the CALLER's streams instead of
// captured ones: the human terminal commands of 6.4 (`brigade team
// create|join|leave`, `brigade profile …`) hand their stdin, stdout and
// stderr straight to the adapter, so `--prompt` can read a join secret from
// the TTY without echo and the adapter's own envelope reaches the human
// unchanged. Nothing is parsed here: the argv is `<adapter prefix>
// --profile <p> <group> <verb> <args…>` and the child's environment is the
// same from-scratch one every other child gets (inherited BRIGADE_* and
// CLAUDE_CODE_MESSAGING_* never cross). stdin may be nil for a command that
// takes none; an *os.File is handed to the child as a descriptor, which is
// what keeps a terminal a terminal.
//
// It returns the child's own exit status, forwarded verbatim (4.6 gives it
// meaning) with a nil error. The error is non-nil only when the adapter did
// NOT run to an exit of its own — the executable was not found (
// `unavailable`, details.reason adapter_not_found), it died on a signal or
// ctx ended (`unavailable`), or the start failed (`internal`) — and the
// status is then -1. It is the one other exec.CommandContext of the harness
// beside StartWatch, which is why it lives in this file.
func (c *Client) PassThrough(ctx context.Context, group, verb string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	argv, err := c.argv(group, verb, args)
	if err != nil {
		return -1, err
	}

	//nolint:forbidigo // adapterclient/spawn.go IS the one spawn seam the 7.3 rule allows (6.4: inherited stdio)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = c.childEnv()
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = watchWaitDelay

	runErr := cmd.Run()
	if runErr == nil {
		return 0, nil
	}
	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, os.ErrNotExist) {
		return -1, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "adapter executable not found",
			Details: map[string]string{"reason": "adapter_not_found"},
		}
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode(), nil
	}
	if ctx.Err() != nil {
		return -1, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "adapter did not finish within its deadline",
			Details: map[string]string{"reason": "timeout"},
		}
	}
	if errors.As(runErr, &exitErr) {
		return -1, &protocol.Error{
			Code:    protocol.CodeUnavailable,
			Message: "adapter was killed by a signal",
			Details: map[string]string{"reason": "signal"},
		}
	}
	c.logger().Error("adapter pass-through failed", slog.String("error", runErr.Error()))
	return -1, &protocol.Error{
		Code:    protocol.CodeInternal,
		Message: "adapter spawn failed",
		Details: map[string]string{"reason": "spawn_error"},
	}
}
