package watch

import (
	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/protocol"
)

// stopWork stops the work item the session works on, if any, when the
// session's process is gone (work item 346, ADR 0035). The SessionEnd
// hook writes that stop on an ordinary end; a process that was killed, or
// a host that gave its hook no time, runs no hook, and the watcher's
// liveness poll is the authoritative end. It is the session's own
// directory of the board, so this writer is the only one there.
func (w *watcher) stopWork() {
	if w.sync.board == "" || !workitem.SessionIDOK(w.sessionID) {
		return
	}
	store := workitem.Store{Folder: w.sync.board, Now: w.deps.Clock}
	sw, err := workitem.ReadSessionWork(store.Folder, w.sessionID)
	if err != nil {
		w.log.Debug("the session's work is unreadable", adlog.Err(err))
		return
	}
	if sw.Current() == nil {
		return
	}
	w.state.mu.Lock()
	name := w.state.name
	w.state.mu.Unlock()
	who := workitem.Author{Session: protocol.OneLineName(name)}
	if err := store.StopWork(sw, workitem.EndSessionEnd, who); err != nil {
		w.log.Warn("the session's work on a work item was not stopped", adlog.Err(err))
		return
	}
	w.log.Info("the session's work on a work item stopped with the session")
}
