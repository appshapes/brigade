package hook

import (
	"context"
	"log/slog"
	"syscall"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

// sessionEnd is `brigade hook session-end` (6.3, 3.8): for `clear` and
// `resume` the process continues and nothing happens; otherwise the
// watcher is signalled, the pidfile and the by-pid map are removed (the
// by-native map is kept for a later --resume) and `session close` runs
// with its 1 s budget. It is the fast path only: SessionEnd does not fire
// on SIGKILL and fires before the process exits on /exit (E0-5 (5)), so
// the watcher's own liveness poll is the authoritative close.
func (r *run) sessionEnd() int {
	in, err := r.readInput()
	if err != nil {
		// With no usable document the reason is unknown; tearing the
		// session down on a `clear` would be worse than leaving the
		// watcher to notice the real end.
		r.log.Warn("session-end: bad stdin; nothing done", log.Err(err))
		return 0
	}
	if in.Reason == reasonClear || in.Reason == reasonResume {
		return 0
	}
	f, err := r.facts()
	if err != nil {
		r.log.Warn("session-end: session facts", log.Err(err))
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), endBudget)
	defer cancel()

	store := sessionmap.Store{StateDir: f.stateDir}
	m, merr := store.ReadByPID(f.pid)
	if merr != nil {
		r.log.Debug("session-end: no usable session map", log.Err(merr))
		m = nil
	}
	path := pidfile.Path(f.stateDir, f.pid)
	v, cerr := pidfile.Check(path, r.deps.Lookup)
	switch {
	case cerr != nil:
		r.log.Warn("session-end: watcher pidfile unreadable", log.Err(cerr))
	case v.Found:
		if v.Alive {
			if kerr := syscall.Kill(v.Entry.PID, syscall.SIGTERM); kerr != nil {
				r.log.Debug("session-end: SIGTERM", log.Err(kerr), slog.Int("watcher_pid", v.Entry.PID))
			}
		}
		// Compare-then-delete: only the entry that was read is removed
		// (E0-5 item 3); the watcher's own cleanup races harmlessly.
		if _, rerr := pidfile.Remove(path, v.Entry); rerr != nil {
			r.log.Debug("session-end: pidfile not removed", log.Err(rerr))
		}
	}
	if derr := store.DeleteByPID(f.pid); derr != nil {
		r.log.Warn("session-end: session map not removed", log.Err(derr))
	}
	if m == nil {
		return 0
	}
	adapter, aerr := config.AdapterFromArgv(m.AdapterCommand)
	if aerr != nil {
		r.log.Warn("session-end: the map's adapter command is unusable; no close", log.Err(aerr))
		return 0
	}
	client := r.client(adapter, m.Profile, m.ConfigDir, f.stateDir)
	cctx, ccancel := context.WithTimeout(ctx, adapterclient.CloseTimeout)
	defer ccancel()
	if _, err := client.Close(cctx, m.BrigadeSessionID); err != nil {
		r.log.Info("session-end: close did not complete; lease expiry is authoritative", log.Err(err))
	}
	return 0
}
