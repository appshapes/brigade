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
	"github.com/appshapes/brigade/internal/harness/sound"
)

// A sounder plays the message-arrival sound (card 35): one fixed program
// per arrival, at most once per sound.MinInterval, never two at once, and
// only while the by-pid map says the `message_sound` option is on. The
// map is the hook's, re-read on every liveness tick (refreshMap), so a
// SessionStart that flips the option takes effect without a respawn. The
// player's argv is resolved once, on the first enable, from the watcher's
// own PATH; a machine with no player logs why once and plays nothing.
//
// What is played is decided by the pipeline's decision alone (offer,
// arrivedNow): the message's text, sender and summary never reach the
// player, whose argument list is fixed at resolve time.
type sounder struct {
	w *watcher

	mu       sync.Mutex
	enabled  bool
	resolved bool
	argv     []string
	last     time.Time
	playing  bool
	failed   bool

	ctx context.Context
	wg  sync.WaitGroup
}

func newSounder(w *watcher) *sounder { return &sounder{w: w} }

// bind gives the player its context: the exit path cancels it, which
// ends a sound in progress, and then joins the player.
func (s *sounder) bind(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
}

// apply sets the option's state from the map. A change is logged, and
// the first enable resolves the player.
func (s *sounder) apply(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on == s.enabled {
		return
	}
	s.enabled = on
	if !on {
		s.w.log.Info("message sound off")
		return
	}
	if !s.resolved {
		s.resolved = true
		s.argv = s.resolve()
	}
	if s.argv == nil {
		return // resolve said why
	}
	s.w.log.Info("message sound on")
}

// resolve is the player's argv: the test seam when set, else
// sound.Resolve over the watcher's PATH. nil, with one log line naming
// the reason, when this machine has none.
func (s *sounder) resolve() []string {
	if len(s.w.deps.SoundCommand) > 0 {
		return slices.Clone(s.w.deps.SoundCommand)
	}
	pathVar := adapterkit.Getenv(s.w.environ, "PATH")
	argv, why := sound.Resolve(runtime.GOOS,
		func(name string) (string, bool) { return sound.LookPath(pathVar, name) }, sound.Exists)
	if why != "" {
		s.w.log.Info("message sound off: no player on this machine", slog.String("why", why))
		return nil
	}
	return argv
}

// arrived is offer's call for a message that reached this session —
// queued for injection, or newly held — never for a duplicate, a
// redelivery, a release or a notice. It plays at most once per
// sound.MinInterval, by the watcher's clock, and never while a player is
// still running; the play itself is a goroutine, so the attempt that
// offered the message is not held for the sound's length.
func (s *sounder) arrived() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.argv == nil || s.playing {
		return
	}
	now := s.w.deps.Clock()
	if !s.last.IsZero() && now.Sub(s.last) < sound.MinInterval {
		return
	}
	s.last = now
	s.playing = true
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	argv := slices.Clone(s.argv)
	s.wg.Add(1)
	go s.play(ctx, argv)
}

// play runs the player once, bounded by sound.Timeout, with the
// from-scratch child environment every Brigade child gets. The first
// failure of a streak is a warning, the rest debug lines, so a machine
// whose player broke does not fill the log at every arrival.
func (s *sounder) play(parent context.Context, argv []string) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(parent, sound.Timeout)
	defer cancel()
	err := s.w.deps.Sound(ctx, argv, adapterkit.ChildEnv(s.w.environ))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playing = false
	switch {
	case err == nil:
		s.failed = false
		s.w.log.Debug("message sound played")
	case !s.failed:
		s.failed = true
		s.w.log.Warn("message sound failed", adlog.Err(err))
	default:
		s.w.log.Debug("message sound failed again", adlog.Err(err))
	}
}

// join waits for a running player; the caller has cancelled its context.
func (s *sounder) join() { s.wg.Wait() }

// runQuiet is the production Deps.Sound: the spawn seam's quiet runner.
func runQuiet(ctx context.Context, argv, env []string) error {
	return adapterkit.RunQuiet(ctx, adapterkit.QuietSpec{Argv: argv, Env: env})
}
