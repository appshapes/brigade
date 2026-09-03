package watch

import (
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/harness/registry"
	"github.com/appshapes/brigade/internal/harness/socketpost"
	"github.com/appshapes/brigade/internal/protocol"
)

// shared is the state the event loop, the liveness tick and the injector
// read and write: the socket target (the registry may move it), the
// session name and activity (the registry: /rename, busy/idle), the
// inbound policy (the by-pid map).
type shared struct {
	mu       sync.Mutex
	target   socketpost.Target
	name     string
	activity string
	inbound  string
	flip     bool // an activity change the next tick must heartbeat at once
}

func newShared(target socketpost.Target, name, inbound string) *shared {
	return &shared{
		target:   target,
		name:     name,
		activity: protocol.ActivityIdle,
		inbound:  inbound,
	}
}

// snapshot copies the shared state under the lock.
type sharedSnapshot struct {
	target   socketpost.Target
	name     string
	activity string
	inbound  string
}

func (s *shared) snapshot() sharedSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sharedSnapshot{target: s.target, name: s.name, activity: s.activity, inbound: s.inbound}
}

// takeFlip reports and clears a pending activity flip.
func (s *shared) takeFlip() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flip
	s.flip = false
	return f
}

// checkLiveness is the 2 s tick of 6.6 (E0-5 corrections applied): the
// Claude process is looked up by STATE (gone, a zombie, a foreign pid or a
// changed start token — pid reuse — all end the watcher); the by-pid map is
// re-read (gone or naming another Brigade session or profile ends the
// watcher; its inbound policy is applied); the registry is re-read for the
// name, the activity and the socket path. A missing socket is NOT an exit
// condition (E0-5 defect 2). It returns the stop reason, "" to go on.
func (w *watcher) checkLiveness() string {
	info, err := w.deps.Lookup(w.rc.env.ClaudePID)
	switch {
	case err != nil:
		w.log.Warn("claude pid lookup failed", adlog.Err(err))
	case !info.Exists:
		w.log.Info("claude process is gone", slog.Int("claude_pid", w.rc.env.ClaudePID))
		return "claude_gone"
	case info.Zombie:
		w.log.Info("claude process is a zombie", slog.Int("claude_pid", w.rc.env.ClaudePID))
		return "claude_gone"
	case info.Foreign:
		w.log.Info("claude pid belongs to another user; treated as reuse", slog.Int("claude_pid", w.rc.env.ClaudePID))
		return "claude_gone"
	case w.claudeStart != "" && info.StartToken != "" && info.StartToken != w.claudeStart:
		w.log.Info("claude pid was reused by another process", slog.Int("claude_pid", w.rc.env.ClaudePID))
		return "claude_gone"
	}
	if reason := w.refreshMap(); reason != "" {
		return reason
	}
	w.refreshRegistry()
	return ""
}

// refreshMap re-reads the by-pid map: gone → "map_gone"; another session
// or profile → "map_mismatch"; otherwise the inbound policy and the team
// name are applied. Any other read failure is logged and the last values
// stand.
func (w *watcher) refreshMap() string {
	m, err := w.store.ReadByPID(w.rc.env.ClaudePID)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		w.log.Info("by-pid map is gone")
		return "map_gone"
	case err != nil:
		w.log.Warn("by-pid map unreadable; keeping the last values", adlog.Err(err))
		return ""
	case m.BrigadeSessionID != w.sessionID:
		w.log.Info("by-pid map names another Brigade session")
		return "map_mismatch"
	case m.Profile != w.rc.env.Profile:
		w.log.Info("by-pid map names another profile")
		return "map_mismatch"
	}
	pol := policy.Policy(m.Inbound)
	if !pol.Valid() {
		pol = policy.Refuse
	}
	if pol != w.pipeline.Policy() {
		w.log.Info("inbound policy changed", slog.String("inbound", pol.String()))
		w.pipeline.SetPolicy(pol)
	}
	w.state.mu.Lock()
	w.state.inbound = pol.String()
	if w.state.name == "" && m.SessionName != "" {
		w.state.name = m.SessionName
	}
	w.state.mu.Unlock()
	return ""
}

// refreshRegistry re-reads Claude Code's registry entry, best effort: the
// display name (sanitised, folded to one line), the busy/idle activity (a
// flip is heartbeated at once) and the inbox socket path (a new absolute
// path replaces the target; the token stays the one the watcher was
// spawned with). The socket variable is the default when the entry has
// none. Sink mode reads the registry for the name and activity only.
func (w *watcher) refreshRegistry() {
	fsys := w.deps.Registry(w.rc.claudeConfigDir)
	e, err := registry.Read(fsys, w.rc.env.ClaudePID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.log.Debug("registry entry unreadable", adlog.Err(err))
	}
	if !e.Found {
		return
	}
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	if name := oneLineName(e.Name); name != "" && name != w.state.name {
		w.log.Info("session name updated from the registry", slog.String("session_name", name))
		w.state.name = name
	}
	if act := e.Activity(); act != w.state.activity {
		w.log.Info("activity changed", slog.String("activity", act))
		w.state.activity = act
		w.state.flip = true
	}
	if w.rc.sink != "" {
		return
	}
	if p := e.MessagingSocketPath; p != "" && filepath.IsAbs(p) && p != w.state.target.Path {
		w.log.Info("socket path updated from the registry", slog.String("socket_path", p))
		w.state.target.Path = p
	}
}

// oneLineName sanitises a registry name for the heartbeat: the protocol
// sanitiser (NFC, control characters, forged tags, the 64-code-point
// cap), then every run of whitespace folded to one space.
func oneLineName(raw string) string {
	return strings.Join(strings.Fields(protocol.SanitizeName(raw)), " ")
}
