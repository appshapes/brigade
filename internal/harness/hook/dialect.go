package hook

import (
	"github.com/appshapes/brigade/internal/harness/config"
)

// A dialect is what one host's hook is handed and may print, and how its
// session is keyed (work item 381): one implementation for each kind, in a
// file of its own, chosen from the host table (config.Host.Dialect). The
// shared hook asks it and names no host.
type dialect interface {
	// native reports whether the hook speaks the host's own protocol
	// whole: run and decode are then this dialect's.
	native() bool
	// run runs the hook in the host's own protocol and returns its exit
	// status. Only a native dialect is asked.
	run(r *run) int
	// decode reads from the document what the host's own protocol
	// carries beyond the shared members. Only a native dialect is asked.
	decode(r *run, data []byte, in *input) error
	// adopt gives a run whose host came from `--host` the marker the
	// config package keys on, from the document the hook was handed.
	adopt(r *run, in input) error
	// byteCap is the cap in bytes on what the hook prints, 0 for none.
	byteCap() int
	// key is the session's key, and the process kept beside it for
	// liveness when that is not the key: from the host's native id and
	// the hook's parent process. Only a pointed host is asked: Claude
	// Code's session is keyed by CLAUDE_PID, which is its marker.
	key(nativeID string, parent int) (pid, hostProcess int)
	// identity is the host's own answer for a session's name, model,
	// activity and version; ok false leaves it to the shared resolution.
	identity(r *run, f facts, in input) (id identity, ok bool)
	// title is the session's current name where the host's own record
	// has it; ok false leaves it to the shared one.
	title(r *run, in input) (name string, ok bool)
}

// dialects holds the implementations, each under the kind the table names
// it by. A kind that is not here is the plugin's: what Claude Code has.
var dialects = map[config.DialectKind]dialect{
	config.DialectPlugin:      plugin{},
	config.DialectMonitor:     monitor{},
	config.DialectThread:      thread{},
	config.DialectNative:      nativeJSON{},
	config.DialectInjectSteps: injectSteps{},
}

// dialectOf is the dialect of a host.
func dialectOf(host config.Host) dialect {
	if d := dialects[host.Dialect()]; d != nil {
		return d
	}
	return plugin{}
}

// spokenHost is the host whose own protocol the running hook speaks, ""
// when it speaks none: the one `--host` names, or the environment's. Both
// are asked, as they were when the question was one host's name: a hook
// in an environment that carries such a host's marker is that host's
// whatever its argv says.
func (r *run) spokenHost() config.Host {
	for i, host := range []config.Host{r.args.host, config.HostOf(r.environ)} {
		// An inherited marker of a host that speaks inject-steps does not
		// overrule a hook whose argv names another host: a Codex CLI started
		// from an agy agent's command inherits ANTIGRAVITY_CONVERSATION_ID,
		// and its hook is Codex's (Rjae's ruling of 2026-11-27, work item
		// 532: this case only; the shared rule is not changed here).
		if i == 1 && r.args.host != "" && r.args.host != host && host.Dialect() == config.DialectInjectSteps {
			continue
		}
		if dialectOf(host).native() {
			return host
		}
	}
	return ""
}

// plugin is Claude Code's dialect (config.DialectPlugin), and the plain
// form the others start from: the host is told by the environment, the
// document is the shared one, nothing caps the output in bytes, the
// session is keyed by the hook's parent, and its name comes from the
// shared resolution.
type plugin struct{}

func (plugin) native() bool                                 { return false }
func (plugin) run(*run) int                                 { return 0 }
func (plugin) decode(*run, []byte, *input) error            { return nil }
func (plugin) adopt(*run, input) error                      { return nil }
func (plugin) byteCap() int                                 { return 0 }
func (plugin) key(_ string, parent int) (int, int)          { return parent, 0 }
func (plugin) identity(*run, facts, input) (identity, bool) { return identity{}, false }
func (plugin) title(*run, input) (string, bool)             { return "", false }
