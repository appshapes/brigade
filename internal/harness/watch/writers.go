package watch

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// The watcher's exit joins every goroutine that can write under the state
// directory (P14-6).
//
// The bug this exists for: `Run` used to return while a goroutine of its
// own was still creating files under ${stateDir}. Measured on CI run
// 34778172498 attempt 1, job 103780045371, Linux, -test.shuffle
// 1789328254153052106: TestReleaseSurvivesAWatcherRestart passed every
// assertion, both watchers had returned exit 0, and t.TempDir's cleanup
// then failed with `brigade-state: directory not empty`. The writer was
// the re-open goroutine (`go w.reopen(s)` in handleEvent, joined by
// nothing): inside adapterclient.Client.Register it opens
// ${stateDir}/logs/adapter-<profile>.log — creating the directory chain —
// and spawns a child that writes the store. It is reachable in every run
// of that test, because the fs adapter closes the session when the first
// watcher stops and the second is answered conflict:session_closed.
//
// So no goroutine that can write there is started with a bare `go`
// statement any more: the injector (the sink file, and the seen and
// pending files through the pipeline), the session's command writer (the
// ack and the close, each an adapter call with the same log file and
// child) and the re-open all start through [watcher.goWriter], and `run`
// calls [watcher.joinWriters] before its exit line and before the pidfile
// is released. The per-stage waits that remain (writerStopWait in
// attempt) are hang catchers on a stage's own diagnostics; the join is
// what the exit ordering rests on.
//
// The join is unconditional. A bound here would be the same bug with a
// larger window, so writerJoinWarn only decides when the wait says so in
// the log, never how long it lasts.

// writerJoinWarn is how long joinWriters waits before it logs that it is
// still waiting. It bounds nothing.
const writerJoinWarn = 15 * time.Second

// A WriterCount is the live count of one watcher's state-directory
// writers, for a test that must see the exit ordering rather than infer it
// from a cleanup failure: it is zero the instant [Run] returns. Deps.Writers
// is nil in production, and a nil *WriterCount counts nothing.
type WriterCount struct{ live atomic.Int64 }

// Live is the number of state-directory writers running right now.
func (c *WriterCount) Live() int64 {
	if c == nil {
		return 0
	}
	return c.live.Load()
}

func (c *WriterCount) add(delta int64) {
	if c != nil {
		c.live.Add(delta)
	}
}

// goWriter starts fn as a state-directory writer under the name the log
// gives it. Every caller is either the watcher's own line before the join
// or a writer already counted (the command writer's re-open), so the
// counter is never zero when it is raised — sync.WaitGroup's rule for an
// Add that runs alongside a Wait.
func (w *watcher) goWriter(name string, fn func()) {
	w.writers.Add(1)
	w.writersLive.Add(1)
	w.deps.Writers.add(1)
	go func() {
		defer func() {
			// The count drops BEFORE the wait group, so a test that reads
			// the count the instant Run returns sees zero.
			w.deps.Writers.add(-1)
			w.writersLive.Add(-1)
			w.writers.Done()
		}()
		w.log.Debug("state-directory writer started", slog.String("writer", name))
		fn()
	}()
}

// joinWriters blocks until every goroutine started through goWriter has
// returned. It never gives up: after writerJoinWarn it says so and goes on
// waiting.
func (w *watcher) joinWriters() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.writers.Wait()
	}()
	select {
	case <-done:
		return
	case <-time.After(writerJoinWarn):
	}
	w.log.Warn("still waiting for the state-directory writers", slog.Int64("writers", w.writersLive.Load()))
	<-done
	w.log.Info("the state-directory writers finished")
}
