package watch

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

var errGrokReceiverChanged = errors.New("watch: Grok receiver incarnation is no longer live or bound; not injected")

func (w *watcher) checkGrokReceiver() error {
	m, err := w.store.ReadByPID(w.rc.env.ClaudePID)
	if err != nil || !sessionmap.SameReceiver(w.grokReceiver, m) || !m.GrokAlive(w.deps.Lookup) {
		return errGrokReceiverChanged
	}
	ptr, err := w.store.ReadByHost("grok", m.ClaudeSessionID)
	if err != nil || ptr.HostPID != m.ClaudePID || ptr.BrigadeSessionID != m.BrigadeSessionID {
		return errGrokReceiverChanged
	}
	return nil
}

// Only retire this incarnation. An old watcher must leave a replacement
// native pointer/map alone, even if it reused the UUID or Brigade session ID.
func (w *watcher) retireGrokReceiver() {
	if w.harness != string(config.HostGrok) {
		return
	}
	m, err := w.store.ReadByPID(w.rc.env.ClaudePID)
	if err != nil || !sessionmap.SameReceiver(w.grokReceiver, m) || m.GrokAlive(w.deps.Lookup) {
		return
	}
	ptr, err := w.store.ReadByHost("grok", m.ClaudeSessionID)
	if err == nil && ptr.HostPID == m.ClaudePID && ptr.BrigadeSessionID == m.BrigadeSessionID {
		_ = w.store.DeleteByHost("grok", m.ClaudeSessionID)
	}
	if key, err := w.store.ReadStartPointer("grok", m.ClaudeSessionID); err == nil && key == m.ClaudePID {
		_ = w.store.DeleteStartPointer("grok", m.ClaudeSessionID)
	}
	_ = w.store.DeleteByPID(m.ClaudePID)
	_ = w.store.DeleteStart(m.ClaudePID)
	_ = os.Remove(filepath.Join(w.rc.env.StateDir, "state", "grok-arm-"+m.Grok.Incarnation))
}
