package watch

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/adapterkit"
	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

var errGrokReceiverChanged = errors.New("watch: Grok receiver incarnation is no longer live or bound; not injected")

func (w *watcher) checkGrokReceiver() error {
	m, err := w.store.ReadByPID(w.rc.env.ClaudePID)
	if err != nil || !sessionmap.SameReceiver(w.grokReceiver, m) {
		return errGrokReceiverChanged
	}
	life, lerr := m.GrokLiveness(w.deps.Lookup)
	switch life {
	case sessionmap.GrokDead:
		return errGrokReceiverChanged
	case sessionmap.GrokUnknown:
		w.log.Warn("grok pid lookup failed; injection continues", adlog.Err(lerr))
	case sessionmap.GrokLive:
		// The by-host pointer check below is the remaining binding test.
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
	if config.Host(w.harness).Delivery() != config.DeliverPagedSink {
		return
	}
	m, err := w.store.ReadByPID(w.rc.env.ClaudePID)
	if err != nil || !sessionmap.SameReceiver(w.grokReceiver, m) {
		return
	}
	if life, _ := m.GrokLiveness(w.deps.Lookup); life != sessionmap.GrokDead {
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

// pagedSink is Grok Build's delivery (config.DeliverPagedSink; cards 132,
// 135): a private sink file, under a directory named for this incarnation
// of the session, that the model reads in pages with `brigade inbox read
// --cursor`. It is bound to one live receiver: the watcher serves the
// process and the pointer it was started for and no later one.
type pagedSink struct{ socket }

// bind refuses a watcher whose receiver is dead or whose sink is not the
// one of this incarnation.
func (pagedSink) bind(rc *runConfig, m *sessionmap.ByPID, d Deps) error {
	life, _ := m.GrokLiveness(d.Lookup)
	sinkOK := m.Grok != nil && filepath.Dir(rc.sink) == inbound.GrokSinkDir(rc.env.StateDir, m.BrigadeSessionID, m.Grok.Incarnation)
	if life == sessionmap.GrokDead || !sinkOK {
		return &protocol.Error{Code: protocol.CodeConfig, Message: "Grok monitor receiver or private sink binding is invalid", Details: map[string]string{"reason": "grok_receiver_invalid"}}
	}
	return nil
}

// ready: local injection requires this exact live receiver. Successful
// append/close is ACK parity with Muse, never model receipt.
func (pagedSink) ready(w *watcher) error { return w.checkGrokReceiver() }

// openSink opens the sink read-write and not O_APPEND: repairing a torn
// tail reads the end of the file, and ReadAt fails on a write-only or
// append-only descriptor. This watcher is the only writer. No symlink is
// followed (adapterkit.OpenNoFollow), and a sink that is not a private
// regular file is refused.
func (pagedSink) openSink(path string) (*os.File, error) {
	f, err := adapterkit.OpenNoFollow(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, serr := f.Stat()
	if serr != nil {
		_ = f.Close()
		return nil, serr
	}
	if !info.Mode().IsRegular() || !adapterkit.IsPrivate(info) {
		_ = f.Close()
		return nil, errors.New("watch: inbox sink is not private")
	}
	if err := repairGrokTornTail(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// alive ends the watcher when its receiver's process is gone, or when the
// map and the pointer no longer name this receiver.
func (pagedSink) alive(w *watcher) string {
	life, lerr := w.grokReceiver.GrokLiveness(w.deps.Lookup)
	switch life {
	case sessionmap.GrokDead:
		return "claude_gone"
	case sessionmap.GrokUnknown:
		w.log.Warn("grok pid lookup failed", adlog.Err(lerr))
	case sessionmap.GrokLive:
		if err := w.checkGrokReceiver(); err != nil {
			return "map_mismatch"
		}
	}
	return ""
}

func (pagedSink) retire(w *watcher) { w.retireGrokReceiver() }

// repairGrokTornTail drops a trailing prefix that has no newline. A failed
// append is not acknowledged, and the retry must not concatenate onto it.
func repairGrokTornTail(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	const window = 64 << 20
	start := int64(0)
	if size > window {
		start = size - window
	}
	buf := make([]byte, 64<<10)
	pos := size
	for pos > start {
		n := int64(len(buf))
		if n > pos-start {
			n = pos - start
		}
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil {
			return err
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				return f.Truncate(pos + i + 1)
			}
		}
	}
	if start == 0 {
		return f.Truncate(0)
	}
	return errors.New("watch: inbox sink tail has no record boundary")
}
