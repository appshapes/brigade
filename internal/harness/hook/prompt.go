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
	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// prompt is `brigade hook prompt` (6.3): refresh permission_mode and the
// transcript path in the map, keep the watcher alive, print the watcher's
// notice once, run the opt-in poll
// through the shared inbound pipeline, and print the held notice while
// anything is held under the `hold` policy (P5-9). It prints nothing on
// the common path and exits 0 whatever happens (exit 2 would erase the
// user's prompt).
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
	changed := false
	if in.PermissionMode != "" && in.PermissionMode != m.PermissionMode {
		m.PermissionMode = in.PermissionMode
		changed = true
	}
	if p := transcriptPath(in); p != "" && p != m.TranscriptPath {
		m.TranscriptPath = p
		changed = true
	}
	if changed {
		m.UpdatedAt = r.deps.Now()
		if werr := store.WriteByPID(m); werr != nil {
			r.log.Warn("prompt: session map not updated", log.Err(werr))
		}
	}
	r.ensureWatcher(ctx, f, m)
	r.printNotice(f)
	r.poll(ctx, f, m)
	r.heldNotice(f, m)
	return 0
}

// heldNotice prints inbound.HeldNotice for the messages held under the
// hold policy and not yet released (3.8): on EVERY prompt while anything is
// held — unlike the watcher's one-shot notice, which printNotice removes —
// and after the poll, so the count reflects what the poll just released.
// It reads the pending file directly, not through a pipeline: the notice
// needs no policy, no clock and no adapter. A missing file prints nothing;
// a refused or malformed one prints nothing and logs one Warn — a broken
// file is a diagnostic, not something the model needs. The names in the
// line are sender-controlled text and go through the notice's sanitiser.
func (r *run) heldNotice(f facts, m *sessionmap.ByPID) {
	pending, err := inbound.FilePendingStore{
		Path: inbound.PendingPath(f.stateDir, m.BrigadeSessionID), SessionID: m.BrigadeSessionID,
	}.Load()
	if err != nil {
		r.log.Warn("prompt: pending file refused; no held notice", log.Err(err))
		return
	}
	var held []inbound.PendingEntry
	for _, e := range pending.Entries {
		if !e.Released() {
			held = append(held, e)
		}
	}
	if line := inbound.HeldNotice(held); line != "" && r.fits(line) {
		r.say(line)
	}
}

// retryConnect re-runs the registration at most once per
// registerRetryInterval, inside the prompt budget. The stamp is written
// only AFTER the attempt returns (P5-18): Claude Code kills a hook that
// outlives its timeout with SIGTERM, and a stamp written before the
// attempt (E5 §3 measured it on a cold cache: download inside the budget,
// registration killed at 5 s) silenced every prompt for the next minute
// with nothing on screen. A killed attempt now leaves no stamp and the next
// prompt tries again; a RETURNED failure still stamps, so a backend that is
// down is asked once a minute, not once a prompt.
func (r *run) retryConnect(ctx context.Context, f facts, in input) {
	stamp := retryStampPath(f.stateDir, f.pid)
	if data, err := adapterkit.ReadStrict(stamp); err == nil {
		if last, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); perr == nil && r.deps.Now().Sub(last) < registerRetryInterval {
			r.log.Debug("prompt: not registered; the last retry was recent")
			return
		}
	}
	r.log.Info("prompt: not registered; retrying the registration")
	r.connect(ctx, f, in, min(r.deps.PidfileWait, promptPidfileWait))
	if err := adapterkit.MkdirPrivate(filepath.Join(f.stateDir, "state")); err == nil {
		if werr := adapterkit.WriteAtomic(stamp, []byte(r.deps.Now().Format(time.RFC3339Nano)+"\n")); werr != nil {
			r.log.Debug("prompt: retry stamp not written", log.Err(werr))
		}
	}
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
	switch {
	case err != nil:
		r.log.Warn("prompt: watcher pidfile unreadable; spawning anyway", log.Err(err))
	case v.Found && v.Alive && v.Entry.Version == buildinfo.String():
		return
	case v.Found && v.Alive:
		// A live watcher of another Brigade version — the plugin was
		// updated under a running session. Replace it here, at the next
		// prompt, rather than when the session ends (the SessionStart
		// path's respawnReason, applied to the coordinates it cannot
		// have changed). Within the prompt budget: watcherStopWait plus
		// promptPidfileWait leave room for the process to exit.
		r.log.Info("prompt: watcher version changed; respawning", slog.Int("watcher_pid", v.Entry.PID),
			slog.String("watcher_version", v.Entry.Version))
		r.stopWatcher(v.Entry, f)
		r.spawnWatcher(ctx, f, m, min(r.deps.PidfileWait, promptPidfileWait))
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
// file and its pending file. Under refuse nothing is fetched, printed or
// acknowledged. Under accept each frame is printed as frame.PollPreamble
// plus the bare frame until OutputCap would be exceeded; only the printed
// frames are acknowledged, in one `message ack`. Under hold (P5-9, 3.9)
// the fetched messages become pending entries — nothing is printed and
// nothing acknowledged — and the release file is applied exactly as the
// watcher applies it, so a host with no inbox socket has a working release
// path: released frames are printed under the accept path up to the cap,
// only the printed ones are acknowledged, and the rest stay stamped in the
// pending file for the next prompt.
func (r *run) poll(ctx context.Context, f facts, m *sessionmap.ByPID) {
	opts, err := config.ParseOptions(r.environ)
	if err != nil {
		r.log.Warn("prompt: options unreadable; no poll", log.Err(err))
		return
	}
	if !opts.PollOnPrompt {
		return
	}
	pol := policy.Policy(m.Inbound)
	if pol != policy.Accept && pol != policy.Hold {
		r.log.Debug("prompt: inbound policy is refuse; no poll")
		return
	}
	adapter, err := config.AdapterFromArgv(m.AdapterCommand)
	if err != nil {
		r.log.Warn("prompt: the map's adapter command is unusable; no poll", log.Err(err))
		return
	}
	client := r.client(adapter, m.TeamKey, m.ConfigDir, f.stateDir)
	rctx, rcancel := context.WithTimeout(ctx, receiveTimeout)
	received, err := client.Receive(rctx, m.BrigadeSessionID, pollLimit)
	rcancel()
	if err != nil {
		r.log.Warn("prompt: poll failed", log.Err(err))
		return
	}
	pipe, err := inbound.New(inbound.Config{
		Policy:      pol,
		Instruction: m.Instruction(),
		SessionID:   m.BrigadeSessionID,
		TeamName:    m.TeamName,
		Wrap:        false,
		Clock:       inbound.ClockFunc(r.deps.Now),
		Seen:        inbound.FileSeenStore{Path: inbound.SeenPath(f.stateDir, m.BrigadeSessionID)},
		Pending: inbound.FilePendingStore{
			Path: inbound.PendingPath(f.stateDir, m.BrigadeSessionID), SessionID: m.BrigadeSessionID,
		},
		Logger: r.log,
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
	r.applyRelease(f, m, pipe)
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

// applyRelease is the poll's copy of the watcher's release path (3.5's
// four steps): read the release file, stamp and persist through the
// pipeline, delete the file by content, then queue whatever is stamped and
// deliverable so the drain below prints it. A file naming another session
// or one the strict reader refuses is left in place.
func (r *run) applyRelease(f facts, m *sessionmap.ByPID, pipe *inbound.Pipeline) {
	path := inbound.ReleasePath(f.stateDir, m.BrigadeSessionID)
	rel, raw, err := inbound.ReadRelease(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		r.log.Warn("prompt: release file refused; left in place", log.Err(err))
	case rel.SessionID != m.BrigadeSessionID:
		r.log.Warn("prompt: release file names another session; left in place")
	default:
		res := pipe.Release(rel.MessageIDs)
		r.log.Info("prompt: release file applied", slog.Int("stamped", len(res.Stamped)), slog.Int("unknown", len(res.Unknown)))
		if _, cerr := inbound.ConsumeRelease(path, raw); cerr != nil {
			r.log.Warn("prompt: release file not removed", log.Err(cerr))
		}
	}
	pipe.Release(nil)
}
