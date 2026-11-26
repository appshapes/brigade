package hook

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/adapterclient"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
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
	ctx, cancel := context.WithTimeout(context.Background(), r.deps.EndBudget)
	defer cancel()

	store := sessionmap.Store{StateDir: f.stateDir}
	m, merr := store.ReadByPID(f.pid)
	if merr != nil {
		r.log.Debug("session-end: no usable session map", log.Err(merr))
		m = nil
	}
	if f.host.BindsReceiver() && !r.grokMatches(m, in) {
		return 0
	}
	// The state entry's end (card 162), before the map goes: the last
	// write this machine makes for the session.
	r.stateEnd(ctx, f, m, in.Reason, r.deps.Now())
	path := pidfile.Path(f.stateDir, f.pid)
	v, cerr := pidfile.Check(path, r.deps.Lookup)
	switch {
	case cerr != nil:
		r.log.Warn("session-end: watcher pidfile unreadable", log.Err(cerr))
	case v.Found:
		if v.Alive {
			if kerr := procutil.Terminate(v.Entry.PID); kerr != nil {
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
	if derr := store.DeleteStart(f.pid); derr != nil {
		r.log.Debug("session-end: start facts not removed", log.Err(derr))
	}
	if f.pointed() {
		if m != nil {
			owned := true
			if f.host.BindsReceiver() {
				p, err := store.ReadByHost(string(f.host), m.ClaudeSessionID)
				owned = err == nil && p.HostPID == m.ClaudePID && p.BrigadeSessionID == m.BrigadeSessionID
			}
			if owned {
				if derr := store.DeleteByHost(string(f.host), m.ClaudeSessionID); derr != nil {
					r.log.Debug("session-end: session pointer not removed", log.Err(derr))
				}
			}
		}
		if in.SessionID != "" {
			owned := true
			if f.host.BindsReceiver() {
				key, err := store.ReadStartPointer(string(f.host), in.SessionID)
				owned = err == nil && key == f.pid
			}
			if owned {
				if derr := store.DeleteStartPointer(string(f.host), in.SessionID); derr != nil {
					r.log.Debug("session-end: start pointer not removed", log.Err(derr))
				}
			}
		}
	}
	// The doing reminder stamp goes with the map (card 25, plan 5.3); the
	// sentence itself stays on the closed record (ruling 6).
	_ = os.Remove(config.TeamMovedStamp(f.stateDir, f.pid))
	_ = os.Remove(config.MovedNotice(f.stateDir, f.pid))
	if rerr := os.Remove(doingStampPath(f.stateDir, f.pid)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		r.log.Debug("session-end: doing stamp not removed", log.Err(rerr))
	}
	// The session's work on a work item stops with it (work item 346):
	// after the watcher's signal, which is the close on a host whose hook
	// has a fraction of a second, so the board's write never delays it.
	r.workEnd(m)
	if m == nil {
		return 0
	}
	if f.pointed() {
		// Muse Code gives a SessionEnd hook 0.1–0.4 s (measured, card 88)
		// and Codex CLI 1 s by default, 3 s at most (card 107): not enough
		// for an adapter call. The SIGTERM above is the close — the
		// watcher's own exit path closes the session within its budget,
		// and the lease expires if it cannot.
		return 0
	}
	adapter, aerr := config.AdapterFromArgv(m.AdapterCommand)
	if aerr != nil {
		r.log.Warn("session-end: the map's adapter command is unusable; no close", log.Err(aerr))
		return 0
	}
	client := r.client(adapter, m.TeamKey, m.ConfigDir, f.stateDir)
	cctx, ccancel := context.WithTimeout(ctx, adapterclient.CloseTimeout)
	defer ccancel()
	if _, err := client.Close(cctx, m.BrigadeSessionID); err != nil {
		r.log.Info("session-end: close did not complete; lease expiry is authoritative", log.Err(err))
	}
	return 0
}

// workEnd stops the work item the session works on, if any, as its end
// (work item 346): `brigade work-item start` set it, and the session ended
// without a `stop`. The span keeps its start; this writes its stop. A
// session that dies without a SessionEnd has its stop written by the
// watcher (watch/work.go).
func (r *run) workEnd(m *sessionmap.ByPID) {
	if m == nil || m.BoardFolder == "" || !workitem.SessionIDOK(m.BrigadeSessionID) {
		return
	}
	store := workitem.Store{Folder: m.BoardFolder, Now: r.deps.Now}
	w, err := workitem.ReadSessionWork(store.Folder, m.BrigadeSessionID)
	if err != nil {
		r.log.Debug("session-end: the session's work is unreadable", log.Err(err))
		return
	}
	if w.Current() == nil {
		return
	}
	who := workitem.Author{Session: protocol.OneLineName(m.SessionName)}
	if b, err := teamstore.LoadBinding(m.ConfigDir, m.TeamKey); err == nil {
		who.Member = protocol.SanitizeLabel(workitem.Line(b.HumanLabel))
	}
	if err := store.StopWork(w, workitem.EndSessionEnd, who); err != nil {
		r.log.Warn("session-end: the session's work on a work item was not stopped", log.Err(err))
	}
}
