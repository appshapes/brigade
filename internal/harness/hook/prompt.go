package hook

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// prompt is `brigade hook prompt` (6.3): refresh permission_mode, keep the
// watcher alive, print the watcher's notice once, and run the opt-in poll
// through the shared inbound pipeline. It prints nothing on the common
// path and exits 0 whatever happens (exit 2 would erase the user's
// prompt).
func (r *run) prompt() int {
	in, err := r.readInput()
	if err != nil {
		r.log.Warn("prompt: bad stdin", log.Err(err))
		return 0
	}
	f, err := r.facts()
	if err != nil {
		r.log.Warn("prompt: session facts", log.Err(err))
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), promptBudget)
	defer cancel()

	store := sessionmap.Store{StateDir: f.stateDir}
	m, err := store.ReadByPID(f.pid)
	if errors.Is(err, fs.ErrNotExist) {
		// SessionStart failed (or the plugin was enabled mid-session):
		// the "retrying at your next prompt" of the context line.
		r.retryConnect(ctx, f, in)
		return 0
	}
	if err != nil {
		r.log.Warn("prompt: the session map cannot be trusted; nothing done", log.Err(err))
		return 0
	}
	if in.PermissionMode != "" && in.PermissionMode != m.PermissionMode {
		m.PermissionMode = in.PermissionMode
		m.UpdatedAt = r.deps.Now()
		if werr := store.WriteByPID(m); werr != nil {
			r.log.Warn("prompt: session map not updated", log.Err(werr))
		}
	}
	r.ensureWatcher(ctx, f, m)
	r.printNotice(f)
	r.poll(ctx, f, m)
	return 0
}

// retryConnect re-runs the registration at most once per
// registerRetryInterval, inside the prompt budget.
func (r *run) retryConnect(ctx context.Context, f facts, in input) {
	stamp := retryStampPath(f.stateDir, f.pid)
	now := r.deps.Now()
	if data, err := adapterkit.ReadStrict(stamp); err == nil {
		if last, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); perr == nil && now.Sub(last) < registerRetryInterval {
			r.log.Debug("prompt: not registered; the last retry was recent")
			return
		}
	}
	if err := adapterkit.MkdirPrivate(filepath.Join(f.stateDir, "state")); err == nil {
		if werr := adapterkit.WriteAtomic(stamp, []byte(now.Format(time.RFC3339Nano)+"\n")); werr != nil {
			r.log.Debug("prompt: retry stamp not written", log.Err(werr))
		}
	}
	r.log.Info("prompt: not registered; retrying the registration")
	r.connect(ctx, f, in, min(r.deps.PidfileWait, promptPidfileWait))
}

// ensureWatcher respawns the watcher when its pidfile is missing or dead
// (6.6). A pidfile with a foreign start_token is dead here and replaced by
// the watcher itself. Without a socket (and no sink) there is nothing to
// inject into and nothing is spawned.
func (r *run) ensureWatcher(ctx context.Context, f facts, m *sessionmap.ByPID) {
	if f.socket == "" && r.deps.Sink == "" {
		r.log.Debug("prompt: no inbox socket; the watcher is not needed")
		return
	}
	v, err := pidfile.Check(pidfile.Path(f.stateDir, f.pid), r.deps.Lookup)
	if err != nil {
		r.log.Warn("prompt: watcher pidfile unreadable; spawning anyway", log.Err(err))
	} else if v.Found && v.Alive {
		return
	}
	r.log.Info("prompt: watcher not alive; respawning")
	r.spawnWatcher(ctx, f, m, min(r.deps.PidfileWait, promptPidfileWait))
}

// printNotice prints the watcher's one-line notice once and removes it
// (3.2). A notice that is not a private file is removed unread.
func (r *run) printNotice(f facts) {
	path := noticePath(f.stateDir, f.pid)
	data, err := adapterkit.ReadStrict(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		r.log.Warn("prompt: notice file refused and removed", log.Err(err))
		_ = os.Remove(path)
		return
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if line := oneLine(first, 512); line != "" && r.fits(line) {
		r.say(line)
	}
	_ = os.Remove(path)
}

// poll is the `poll_on_prompt` fallback (6.3): `message receive --limit
// 20` through the SAME inbound pipeline as the watcher, sharing its seen
// file. Under refuse nothing is fetched, printed or acknowledged. Under
// accept each frame is printed as frame.PollPreamble plus the bare frame
// until OutputCap would be exceeded; only the printed frames are
// acknowledged, in one `message ack`.
func (r *run) poll(ctx context.Context, f facts, m *sessionmap.ByPID) {
	opts, err := config.ParseOptions(r.environ)
	if err != nil {
		r.log.Warn("prompt: options unreadable; no poll", log.Err(err))
		return
	}
	if !opts.PollOnPrompt {
		return
	}
	if m.Inbound != string(policy.Accept) {
		r.log.Debug("prompt: inbound policy is refuse; no poll")
		return
	}
	adapter, err := config.AdapterFromArgv(m.AdapterCommand)
	if err != nil {
		r.log.Warn("prompt: the map's adapter command is unusable; no poll", log.Err(err))
		return
	}
	client := r.client(adapter, m.Profile, m.ConfigDir, f.stateDir)
	rctx, rcancel := context.WithTimeout(ctx, receiveTimeout)
	received, err := client.Receive(rctx, m.BrigadeSessionID, pollLimit)
	rcancel()
	if err != nil {
		r.log.Warn("prompt: poll failed", log.Err(err))
		return
	}
	pipe, err := inbound.New(inbound.Config{
		Policy:   policy.Accept,
		TeamName: m.TeamName,
		Wrap:     false,
		Clock:    inbound.ClockFunc(r.deps.Now),
		Seen:     inbound.FileSeenStore{Path: inbound.SeenPath(f.stateDir, f.pid)},
		Logger:   r.log,
	})
	if err != nil {
		r.log.Warn("prompt: pipeline", log.Err(err))
		return
	}
	var ack []string
	for i := range received.Messages {
		if d := pipe.Offer(received.Messages[i]); d.Ack {
			ack = append(ack, d.MessageID)
		}
	}
	for {
		item, ok := pipe.Next()
		if !ok {
			break
		}
		text := item.Content
		if item.Kind == inbound.ItemMessage {
			text = frame.PollPreamble + "\n" + item.Content
		}
		if !r.fits(text) {
			pipe.Done(item, inbound.ErrNotInjected)
			continue
		}
		r.say(text)
		if d := pipe.Done(item, nil); d.Ack {
			ack = append(ack, d.MessageID)
		}
	}
	if len(ack) == 0 {
		return
	}
	if _, err := client.Ack(ctx, m.BrigadeSessionID, &protocol.AckRequest{MessageIDs: ack}); err != nil {
		r.log.Warn("prompt: ack failed; the messages are redelivered", log.Err(err), slog.Int("count", len(ack)))
	}
}
