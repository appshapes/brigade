package watch

import (
	"log/slog"
	"os"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The idle close (card 61, brief .context/plans/idle-close-vscode.md).
//
// The VS Code extension keeps the `claude` process running after the
// person closes a conversation, and Claude Code fires no hook for it, so
// the watcher of such a session would heartbeat an `idle` row for as long
// as the window lives, polling the backend the whole time. The watcher
// therefore closes its own session once it has seen no activity for the
// team's idle_close_hours — only for a host that behaves so (the map's
// IdleClose is zero for every other), only when the team has not switched
// it off. Activity is what the liveness tick already reads: the registry
// saying busy (the model is working, on a prompt or on a teammate's
// message), or the transcript's mtime moving (the person prompted, or the
// conversation was cleared). A stop for reason idle_expired runs the exit
// path every stop runs: one `session close`, so the roster shows the
// session offline at once; the pidfile goes, the map stays; and the next
// prompt's hook respawns a watcher whose first heartbeat is answered
// session_closed and re-opens the session under its id with its waiting
// messages (P10-7). That watcher's clock starts at its own start.

// reasonIdleExpired is the stop reason, as the exit line logs it.
const reasonIdleExpired = "idle_expired"

// armIdleClose starts the idle clock at the watcher's start and says so
// once, so a log reader knows the threshold this session runs under.
func (w *watcher) armIdleClose() {
	w.lastActive = w.deps.Clock()
	if w.idleClose > 0 {
		w.log.Info("idle close armed", slog.Duration("idle_close", w.idleClose))
	}
}

// checkIdle is the liveness tick's last verdict: "" to go on, or
// reasonIdleExpired once the session has been idle for the threshold.
// The first look at the transcript is a baseline, not activity: a file
// that already exists at the start says nothing about now.
func (w *watcher) checkIdle() string {
	if w.idleClose <= 0 {
		return ""
	}
	now := w.deps.Clock()
	snap := w.state.snapshot()
	active := snap.activity == protocol.ActivityBusy
	if snap.transcriptPath != "" {
		if fi, err := os.Stat(snap.transcriptPath); err == nil {
			switch mt := fi.ModTime(); {
			case w.transcriptSeen.IsZero():
				w.transcriptSeen = mt
			case !mt.Equal(w.transcriptSeen):
				w.transcriptSeen = mt
				active = true
			}
		}
	}
	if active {
		w.lastActive = now
		return ""
	}
	idle := now.Sub(w.lastActive)
	if idle < w.idleClose {
		return ""
	}
	w.log.Info("no activity for the team's idle close; closing the session",
		slog.Duration("idle", idle.Truncate(time.Second)), slog.Duration("idle_close", w.idleClose))
	return reasonIdleExpired
}
