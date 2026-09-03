package watch

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"syscall"
	"time"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/protocol"
)

// guardAttempts bounds the create/check/replace loop against a pidfile
// that keeps changing under us (two hooks racing to spawn, E0-5 item 3).
const guardAttempts = 4

// replacePoll is how often the guard looks for the superseded watcher's
// pidfile to disappear after the SIGTERM.
const replacePoll = 25 * time.Millisecond

// acquire is the single-instance guard of 6.6 (E0-5 items 1, 3 and (i)):
// the pidfile ${stateDir}/watchers/<claude_pid>.json is created O_EXCL; on
// EEXIST the holder is judged through pidfile.Check — alive with the same
// Brigade session, socket path and token hash → this process is a
// duplicate and exits 0 quietly; alive with different values → it is
// SIGTERMed, given ReplaceWait to remove its own file, then replaced;
// dead (gone, a zombie, a foreign pid or a forged start token) →
// replaced by content. It returns whether to proceed and, when not, the
// exit status.
func (w *watcher) acquire() (bool, int) {
	self := os.Getpid()
	info, err := w.deps.Lookup(self)
	if err != nil || info.StartToken == "" {
		w.log.Error("cannot read this process's start token", adlog.Err(err))
		return false, protocol.CodeInternal.Exit()
	}
	tokenSHA := ""
	if w.rc.token != "" {
		tokenSHA = pidfile.TokenSHA256(w.rc.token)
	}
	w.entry = pidfile.Entry{
		PID:              self,
		StartToken:       info.StartToken,
		BrigadeSessionID: w.sessionID,
		SocketPath:       w.rc.socketPath,
		TokenSHA256:      tokenSHA,
	}
	for attempt := range guardAttempts {
		cerr := pidfile.Create(w.pidPath, w.entry)
		if cerr == nil {
			w.log.Info("pidfile created", slog.String("path", w.pidPath), slog.Int("attempt", attempt))
			return true, 0
		}
		if !errors.Is(cerr, fs.ErrExist) {
			w.log.Error("pidfile cannot be created", adlog.Err(cerr))
			return false, protocol.CodeConfig.Exit()
		}
		v, verr := pidfile.Check(w.pidPath, w.deps.Lookup)
		if verr != nil {
			if v.Found {
				// A lookup failure: the holder's pid could not be judged.
				// Fail closed rather than replace a possibly live watcher.
				w.log.Error("pidfile holder cannot be judged", slog.Int("holder_pid", v.Entry.PID), adlog.Err(verr))
				return false, protocol.CodeConfig.Exit()
			}
			// Unreadable, malformed or wrong-mode: Replace could not retire
			// it by content either. Report rather than guess.
			w.log.Error("pidfile unreadable", adlog.Err(verr))
			return false, protocol.CodeConfig.Exit()
		}
		if !v.Found {
			continue // vanished between Create and Check: try again
		}
		if v.Alive {
			if sameService(v.Entry, w.entry) {
				w.log.Info("another watcher already serves this session", slog.Int("holder_pid", v.Entry.PID))
				return false, protocol.ExitOK
			}
			w.log.Info("replacing a live watcher with different values",
				slog.Int("holder_pid", v.Entry.PID),
				slog.Bool("socket_changed", v.Entry.SocketPath != w.entry.SocketPath),
				slog.Bool("hash_changed", v.Entry.TokenSHA256 != w.entry.TokenSHA256),
				slog.Bool("session_changed", v.Entry.BrigadeSessionID != w.entry.BrigadeSessionID),
			)
			if serr := w.deps.Signal(v.Entry.PID, syscall.SIGTERM); serr != nil {
				w.log.Warn("SIGTERM to the superseded watcher failed", adlog.Err(serr))
			}
			w.waitForRetirement(v.Entry)
		} else {
			w.log.Info("replacing a dead watcher's pidfile", slog.Int("holder_pid", v.Entry.PID))
		}
		rerr := pidfile.Replace(w.pidPath, v.Entry, w.entry)
		switch {
		case rerr == nil:
			w.log.Info("pidfile replaced", slog.String("path", w.pidPath))
			return true, 0
		case errors.Is(rerr, pidfile.ErrSuperseded), errors.Is(rerr, fs.ErrExist):
			continue // a new holder: judge it on the next round
		default:
			w.log.Error("pidfile cannot be replaced", adlog.Err(rerr))
			return false, protocol.CodeConfig.Exit()
		}
	}
	w.log.Error("pidfile keeps changing; giving up the guard", slog.Int("attempts", guardAttempts))
	return false, protocol.CodeConfig.Exit()
}

// sameService reports whether two entries describe the same service: the
// same Brigade session, socket path and token hash (the pid may differ).
func sameService(a, b pidfile.Entry) bool {
	return a.BrigadeSessionID == b.BrigadeSessionID &&
		a.SocketPath == b.SocketPath &&
		a.TokenSHA256 == b.TokenSHA256
}

// waitForRetirement waits up to ReplaceWait for the superseded watcher to
// remove its pidfile (it does so by content on its own exit path), or for
// the file to change hands.
func (w *watcher) waitForRetirement(old pidfile.Entry) {
	deadline := time.Now().Add(w.deps.ReplaceWait)
	for time.Now().Before(deadline) {
		cur, err := pidfile.Read(w.pidPath)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && cur != old) {
			return
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(replacePoll):
		}
	}
	w.log.Warn("the superseded watcher did not retire in time", slog.Int("holder_pid", old.PID))
}

// release removes the pidfile by content (compare-then-delete, E0-5 item
// 3): a file that by now belongs to a replacement is left alone.
func (w *watcher) release() {
	removed, err := pidfile.Remove(w.pidPath, w.entry)
	if err != nil {
		w.log.Warn("pidfile not removed", adlog.Err(err))
		return
	}
	w.log.Info("pidfile released", slog.Bool("removed", removed))
}
