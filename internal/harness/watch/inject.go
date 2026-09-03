package watch

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/socketpost"
)

// logExcerptChars is how much of a frame the debug log shows (6.6:
// bodies at debug only, truncated to 80 characters).
const logExcerptChars = 80

// A sinkRecord is one line of the --sink file (6.6): what a socket post
// would have carried, with the ids a test asserts on. A notice has an
// empty message_id.
type sinkRecord struct {
	TS              time.Time `json:"ts"`
	Frame           string    `json:"frame"`
	MessageID       string    `json:"message_id"`
	SenderSessionID string    `json:"sender_session_id"`
}

// The injector drains the pipeline into the socket or the sink (6.8 items
// 6–7): Next → inject → Done, one item at a time, acknowledging only what
// was injected. A failed post stops the drain, backs off (the adapter
// error schedule) and resumes; the failed message is left to the server's
// redelivery (the pipeline forgets it, so the redelivery is treated
// afresh). A socket that is gone makes the registry be re-read for a new
// path before the retry. Notices go through the same path and are never
// acknowledged.
type injector struct {
	w *watcher

	kick     chan struct{} // wake the drain loop (capacity 1)
	ackReady chan struct{} // tell the command writer acks are pending (capacity 1)
	done     chan struct{} // closed when loop returns

	mu    sync.Mutex
	acks  []string
	quiet bool // set by the exit path: no further posts
}

func newInjector(w *watcher) *injector {
	return &injector{w: w, kick: make(chan struct{}, 1), ackReady: make(chan struct{}, 1), done: make(chan struct{})}
}

// kickNow wakes the drain loop without blocking.
func (in *injector) kickNow() {
	select {
	case in.kick <- struct{}{}:
	default:
	}
}

// queueAck adds an id to the next ack batch and signals the command writer.
func (in *injector) queueAck(id string) {
	if id == "" {
		return
	}
	in.mu.Lock()
	in.acks = append(in.acks, id)
	in.mu.Unlock()
	select {
	case in.ackReady <- struct{}{}:
	default:
	}
}

// requeueAcks puts a failed batch back in front WITHOUT re-signalling:
// a dead child would otherwise be retried in a hot loop. The next queued
// ack, or the next attempt's writer (signalPendingAcks), sends it.
func (in *injector) requeueAcks(ids []string) {
	in.mu.Lock()
	in.acks = append(append([]string{}, ids...), in.acks...)
	in.mu.Unlock()
}

// signalPendingAcks wakes the writer when acks are waiting (a new attempt
// after a failed batch, or ids acknowledged between attempts).
func (in *injector) signalPendingAcks() {
	in.mu.Lock()
	pending := len(in.acks) > 0
	in.mu.Unlock()
	if !pending {
		return
	}
	select {
	case in.ackReady <- struct{}{}:
	default:
	}
}

// takeAcks returns and clears the pending batch, deduplicated.
func (in *injector) takeAcks() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.acks) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in.acks))
	out := make([]string, 0, len(in.acks))
	for _, id := range in.acks {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	in.acks = nil
	return out
}

// stopDraining makes every later inject report failure without posting:
// the exit path is running and nothing new may be injected or acked.
func (in *injector) stopDraining() {
	in.mu.Lock()
	in.quiet = true
	in.mu.Unlock()
}

func (in *injector) isQuiet() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.quiet
}

// errQuiet is the Done error for an item handed out after the exit path
// began: not injected, not acknowledged.
var errQuiet = errors.New("watch: exit path running; not injected")

// loop is the injector goroutine: wait for a kick, drain, back off on a
// failure, repeat until ctx ends.
func (in *injector) loop(ctx context.Context) {
	defer close(in.done)
	sched := in.w.deps.InjectSchedule()
	for {
		select {
		case <-ctx.Done():
			return
		case <-in.kick:
		}
		for !in.isQuiet() && ctx.Err() == nil {
			item, ok := in.w.pipeline.Next()
			if !ok {
				break
			}
			err := in.inject(ctx, item)
			d := in.w.pipeline.Done(item, err)
			in.w.log.Debug("injection reported",
				slog.String("kind", itemKindForLog(item)),
				slog.String("message_id", item.MessageID),
				slog.String("outcome", string(d.Outcome)),
				slog.Bool("ack", d.Ack))
			if d.Ack {
				in.queueAck(d.MessageID)
			}
			if err == nil {
				sched.Reset()
				continue
			}
			if errors.Is(err, errQuiet) || ctx.Err() != nil {
				break
			}
			if errors.Is(err, socketpost.ErrSocketGone) {
				in.w.refreshRegistry()
			}
			delay := sched.Next()
			in.w.log.Warn("injection failed; backing off",
				slog.String("kind", itemKindForLog(item)),
				slog.String("message_id", item.MessageID),
				slog.Duration("delay", delay),
				adlog.Err(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			// Leave the rest queued for this resumed drain.
		}
	}
}

// inject writes one item to the sink or the socket.
func (in *injector) inject(ctx context.Context, item inbound.Item) error {
	if in.isQuiet() {
		return errQuiet
	}
	in.w.log.Debug("injecting",
		slog.String("kind", itemKindForLog(item)),
		slog.String("message_id", item.MessageID),
		slog.String("sender_session_id", item.SenderSessionID),
		slog.String("excerpt", excerpt(item.Content)))
	if in.w.rc.sink != "" {
		return in.appendSink(item)
	}
	target := in.w.state.snapshot().target
	if target.Path == "" {
		return &socketpost.Error{Kind: socketpost.KindPrecheck, Reason: socketpost.ReasonEmpty}
	}
	opts := in.w.deps.PostOptions
	if opts.Logger == nil {
		opts.Logger = in.w.log
	}
	return in.w.deps.Post(ctx, target, item.Content, opts)
}

// appendSink appends one record to the sink file, 0600.
func (in *injector) appendSink(item inbound.Item) error {
	rec := sinkRecord{
		TS:              in.w.deps.Clock().UTC(),
		Frame:           item.Content,
		MessageID:       item.MessageID,
		SenderSessionID: item.SenderSessionID,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(in.w.rc.sink, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 0600; the path is the test harness's argv flag
	if err != nil {
		return err
	}
	if _, werr := f.Write(append(line, '\n')); werr != nil {
		_ = f.Close()
		return werr
	}
	return f.Close()
}

// excerpt is the first logExcerptChars code points of s, one line.
func excerpt(s string) string {
	if utf8.RuneCountInString(s) > logExcerptChars {
		n := 0
		for i := range s {
			if n == logExcerptChars {
				s = s[:i]
				break
			}
			n++
		}
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
