package watch

import (
	"context"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/notify"
)

// An announcer runs one fixed program when a message arrives (cards 35
// and 36): the sound player, or the desktop notifier. At most once per
// interval (the map's `message_interval_seconds`, card 38; notify's
// default without it), never two at once, and only while the by-pid map
// says its option is on. The map is the hook's, re-read on every liveness
// tick (refreshMap), so a SessionStart that flips the option takes effect
// without a respawn. The program's argv is resolved once, on the first
// enable, from the watcher's own PATH; a machine with none logs why once
// and runs nothing.
//
// What runs is decided by the pipeline's decision alone (offer,
// arrivedNow): the message's text, sender and summary never reach the
// program. The sound's argv is fixed at resolve time; the banner's is
// completed per run with Brigade's own words, the session's own name and
// the count of arrivals since the last banner, every one an argument
// (notify.BannerArgv).
type announcer struct {
	w *watcher
	// kind names the announcer in log lines ("sound", "notification"),
	// done its success line's verb ("played", "shown").
	kind, done string
	// resolve finds the program: the test seam's argv, else notify's
	// resolver over PATH, with the reason when there is none. build
	// completes the argv for one run from the resolved base and the count.
	resolve func() ([]string, string)
	build   func(base []string, count int) []string

	mu       sync.Mutex
	enabled  bool
	interval time.Duration
	resolved bool
	base     []string
	last     time.Time
	running  bool
	failed   bool
	// count is the arrivals since the last run, this one included when a
	// run starts; the sound ignores it, the banner says it.
	count int

	ctx context.Context
	wg  sync.WaitGroup
}

// newSoundAnnouncer is the message sound (card 35): a fixed argv.
func newSoundAnnouncer(w *watcher) *announcer {
	a := &announcer{w: w, kind: "sound", done: "played"}
	a.resolve = func() ([]string, string) {
		if len(w.deps.SoundCommand) > 0 {
			return slices.Clone(w.deps.SoundCommand), ""
		}
		return notify.Sound(runtime.GOOS, a.lookPath(), notify.Exists)
	}
	a.build = func(base []string, _ int) []string { return slices.Clone(base) }
	return a
}

// newBannerAnnouncer is the desktop notification (card 36): the resolved
// notifier completed per run with the session's own name — as the state
// carries it, kept current from the registry — and the count.
func newBannerAnnouncer(w *watcher) *announcer {
	a := &announcer{w: w, kind: "notification", done: "shown"}
	a.resolve = func() ([]string, string) {
		if len(w.deps.BannerCommand) > 0 {
			return slices.Clone(w.deps.BannerCommand), ""
		}
		return notify.Banner(runtime.GOOS, a.lookPath())
	}
	a.build = func(base []string, count int) []string {
		return notify.BannerArgv(base, w.sessionID, w.state.snapshot().name, count)
	}
	return a
}

// lookPath is notify's PATH search over the watcher's own environment.
func (a *announcer) lookPath() func(string) (string, bool) {
	pathVar := adapterkit.Getenv(a.w.environ, "PATH")
	return func(name string) (string, bool) { return notify.LookPath(pathVar, name) }
}

// bind gives the program its context: the exit path cancels it, which
// ends a run in progress, and then joins the announcer.
func (a *announcer) bind(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
}

// apply sets the option's state and the interval from the map. A change
// is logged, and the first enable resolves the program.
func (a *announcer) apply(on bool, interval time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if interval != a.interval {
		a.interval = interval
		a.w.log.Info("message "+a.kind+" interval", slog.Int64("seconds", int64(interval/time.Second)))
	}
	if on == a.enabled {
		return
	}
	a.enabled = on
	if !on {
		// Arrivals the interval kept quiet are forgotten with the option:
		// the first banner after it comes back counts afresh.
		a.count = 0
		a.w.log.Info("message " + a.kind + " off")
		return
	}
	if !a.resolved {
		a.resolved = true
		base, why := a.resolve()
		if why != "" {
			a.w.log.Info("message "+a.kind+" off: no program on this machine", slog.String("why", why))
		}
		a.base = base
	}
	if a.base == nil {
		return // resolve said why
	}
	a.w.log.Info("message " + a.kind + " on")
}

// arrived is offer's call for a message that reached this session —
// queued for injection, or newly held — never for a duplicate, a
// redelivery, a release or a notice. It counts the arrival, runs at most
// once per interval, by the watcher's clock, and never while a
// run is still in progress; the run itself is a goroutine, so the
// attempt that offered the message is not held for the program's length.
func (a *announcer) arrived() {
	a.mu.Lock()
	if !a.enabled || a.base == nil {
		a.mu.Unlock()
		return
	}
	a.count++
	now := a.w.deps.Clock()
	if a.running || (!a.last.IsZero() && now.Sub(a.last) < a.interval) {
		a.mu.Unlock()
		return
	}
	a.last = now
	a.running = true
	n := a.count
	a.count = 0
	base := a.base
	ctx := a.ctx
	a.wg.Add(1)
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	// Built outside the lock: the banner reads the session's name from
	// the shared state, which has a lock of its own.
	go a.run(ctx, a.build(base, n))
}

// run runs the program once, bounded by notify.Timeout, with the
// from-scratch child environment every Brigade child gets. The first
// failure of a streak is a warning, the rest debug lines, so a machine
// whose program broke does not fill the log at every arrival.
func (a *announcer) run(parent context.Context, argv []string) {
	defer a.wg.Done()
	ctx, cancel := context.WithTimeout(parent, notify.Timeout)
	defer cancel()
	err := a.w.deps.Announce(ctx, argv, adapterkit.ChildEnv(a.w.environ))
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	switch {
	case err == nil:
		a.failed = false
		a.w.log.Debug("message " + a.kind + " " + a.done)
	case !a.failed:
		a.failed = true
		a.w.log.Warn("message "+a.kind+" failed", adlog.Err(err))
	default:
		a.w.log.Debug("message "+a.kind+" failed again", adlog.Err(err))
	}
}

// join waits for a running program; the caller has cancelled its context.
func (a *announcer) join() { a.wg.Wait() }

// runQuiet is the production Deps.Announce: the spawn seam's quiet runner.
func runQuiet(ctx context.Context, argv, env []string) error {
	return adapterkit.RunQuiet(ctx, adapterkit.QuietSpec{Argv: argv, Env: env})
}
