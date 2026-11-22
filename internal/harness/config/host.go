package config

import (
	"path/filepath"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/frame"
)

// A Host is the coding harness a session runs in: the value the
// registration's `harness` member carries (protocol.SessionRegistration).
type Host string

// The hosts Brigade recognizes. The empty Host means "not in a session".
//
// This file is the one place a host is named (work item 380, ADR 0019).
// What differs between the harnesses and is a value, or a yes or a no, is
// a column of the table below, and the rest of the tree asks a Host for
// it. `make lint` refuses one of these four names anywhere else, a test
// aside; the files that still decide by host, each for a reason of its
// own, are listed by name beside that rule in .golangci.yml.
//
// A fifth host adds its constant here, its row below, and ITS NAME TO THE
// RULE'S PATTERN in .golangci.yml, which spells the four out: a name the
// pattern does not hold is refused nowhere.
const (
	HostClaude Host = "claude-code"
	HostMuse   Host = "muse"
	HostCodex  Host = "codex"
	HostGrok   Host = "grok"
)

// A HistoryKind names a way a host keeps its own record of a session.
// The reader of each is the watcher's, one implementation a kind
// (internal/harness/watch); the table says which a host has, so that the
// watcher names no host (work item 381). A host that keeps its
// record as another does shares that kind and its reader.
type HistoryKind string

// The kinds of history, by how a session is filed.
const (
	// HistoryNone: the host keeps no record Brigade reads.
	HistoryNone HistoryKind = ""
	// HistoryDatedStore: sessions/YYYY/MM/DD/<id>/session.jsonl, a log
	// of envelopes that repeat the session's id.
	HistoryDatedStore HistoryKind = "dated-store"
	// HistoryRollouts: sessions/YYYY/MM/DD/rollout-…-<id>.jsonl, with an
	// index of the sessions' names beside them.
	HistoryRollouts HistoryKind = "rollouts"
	// HistorySummaries: sessions/<the launch directory>/<id>/summary.json.
	HistorySummaries HistoryKind = "summaries"
)

// A DeliveryKind names a way a message is put in front of a host's model
// while its session is idle. The watcher has one implementation a kind
// (internal/harness/watch); the table says which a host has (work item
// 381).
type DeliveryKind string

// The kinds of delivery.
const (
	// DeliverSocket: a post on the session's inbox socket, which the host
	// owns. Also what a host the table does not hold is given.
	DeliverSocket DeliveryKind = ""
	// DeliverFollowedSink: a sink file the watcher appends to and the
	// model's monitor follows (`brigade inbox follow`).
	DeliverFollowedSink DeliveryKind = "followed-sink"
	// DeliverQueue: a command of the host's own that queues a turn into
	// the session from outside.
	DeliverQueue DeliveryKind = "queue"
	// DeliverPagedSink: a private sink file, bound to one live receiver,
	// that the model reads in pages (`brigade inbox read --cursor`).
	DeliverPagedSink DeliveryKind = "paged-sink"
)

// A DialectKind names what a host's hook is handed and may print, and how
// its session is keyed: the hook's dialect. The hook has one
// implementation a kind (internal/harness/hook); the table says which a
// host has (work item 381).
type DialectKind string

// The kinds of dialect.
const (
	// DialectPlugin: a plugin's hook as Claude Code runs it. The host is
	// told by its marker in the environment, the document is the shared
	// one, and what the hook prints is lines of text. Also what a host
	// the table does not hold is given.
	DialectPlugin DialectKind = ""
	// DialectMonitor: the same documents, and a cap in bytes on what the
	// hook prints, for a host whose model reads the hook's lines through
	// a monitor.
	DialectMonitor DialectKind = "monitor"
	// DialectThread: the hook carries no marker of its own, so its argv
	// names the host, the document's id is adopted as the marker, and the
	// session is keyed by that id and not by the hook's parent.
	DialectThread DialectKind = "thread"
	// DialectNative: the host's own protocol, JSON in and JSON out, which
	// the hook speaks whole.
	DialectNative DialectKind = "native"
)

// hostRow is what the table holds for one host. Every yes or no is worded
// so that no is what a host the table does not know gets: a column added
// for one host changes nothing for the others.
type hostRow struct {
	// name is the product's name, for a line a person reads.
	name string
	// marker is the variable whose presence in an environment means "a
	// session of this host": CLAUDE_PID, or the host's native session id.
	marker string
	// pointed: the session is found through the by-host pointer
	// (sessionmap.ByHost), because the model's shell knows the native id
	// and not the pid. The marker is then that native id.
	pointed bool
	// homeOnly: Brigade's directories are resolved from HOME alone and
	// XDG_* is ignored, because the host's hooks and its shell tool need
	// not agree on XDG_* (cards 88, 107).
	homeOnly bool
	// exclusive: another host's marker beside this one's is refused and
	// not resolved (HostOf's comment has the reason).
	exclusive bool
	// receiver: the session's map must bind a receiver, and an attached
	// pointer is held to it before a command uses the pointer. The check
	// is sessionmap's ValidGrokReceiver, which knows Grok Build's receiver
	// and no other: a second host that says yes here gives that check a
	// form of its own first.
	receiver bool
	// optionsFile is the options file under the config directory, "" for
	// a host whose options arrive as CLAUDE_PLUGIN_OPTION_*.
	optionsFile string
	// ownOptions: inherited CLAUDE_PLUGIN_OPTION_* are no defaults for a
	// key the options file leaves out.
	ownOptions bool
	// pluginRootFlag: the hook's --plugin-root names the package root.
	// pluginRootVar is the variable that names it otherwise, "" for none.
	pluginRootFlag bool
	pluginRootVar  string
	// frame is the wording a message is framed in for this host's model:
	// the key frame.BuildFor takes, "" for the text it has always had.
	frame string
	// historyVar and historyDir say where the host keeps its own record
	// of a session, which the watcher reads a name, a model and a context
	// size from: the variable that names the directory, when the host has
	// one, and the directory under HOME otherwise. No historyDir: none.
	historyVar string
	historyDir []string
	// history is how that record is filed and read.
	history HistoryKind
	// delivery is how a message reaches the host's model.
	delivery DeliveryKind
	// dialect is what the host's hook is handed and may print.
	dialect DialectKind
	// hookNamed: the host's hooks name it on their argv (`--host`),
	// because they carry no marker the environment could be told by.
	hookNamed bool
	// rename: the host can be told a session's name from inside it.
	rename bool
	// stop: the host runs a Stop hook, so the prompt hook need not carry
	// what is asked at the end of a turn.
	stop bool
	// promptDiscarded: the host shows nothing a prompt hook prints.
	promptDiscarded bool
	// staysOnMove: a running session does not follow its team to another
	// backend; it stays until it starts again.
	staysOnMove bool
	// unnamed: `brigade whoami` has no `host:` line for it, as it had
	// none before there was a second host.
	unnamed bool
}

// hosts is the table, in the order HostOf asks: Claude Code first, so
// another host's variable leaking into a Claude session changes nothing,
// and Grok Build last, for the reason HostOf gives.
var hosts = []struct {
	host Host
	row  hostRow
}{
	{HostClaude, hostRow{
		name:          "Claude Code",
		marker:        ClaudePIDVar,
		pluginRootVar: ClaudePluginRootVar,
		rename:        true,
		stop:          true,
		unnamed:       true,
	}},
	{HostMuse, hostRow{
		name:          "Muse Code",
		marker:        MuseSessionIDVar,
		pointed:       true,
		homeOnly:      true,
		optionsFile:   MuseOptionsFile,
		pluginRootVar: "MUSE_PLUGIN_ROOT",
		frame:         frame.HostMuse,
		historyDir:    []string{".local", "share", "muse"},
		history:       HistoryDatedStore,
		delivery:      DeliverFollowedSink,
		dialect:       DialectMonitor,
	}},
	{HostCodex, hostRow{
		name:           "Codex CLI",
		marker:         CodexThreadIDVar,
		pointed:        true,
		homeOnly:       true,
		optionsFile:    CodexOptionsFile,
		pluginRootFlag: true,
		frame:          frame.HostCodex,
		historyVar:     "CODEX_HOME",
		historyDir:     []string{".codex"},
		history:        HistoryRollouts,
		delivery:       DeliverQueue,
		dialect:        DialectThread,
		hookNamed:      true,
	}},
	{HostGrok, hostRow{
		name:            "Grok Build",
		marker:          GrokSessionIDVar,
		pointed:         true,
		homeOnly:        true,
		exclusive:       true,
		receiver:        true,
		optionsFile:     GrokOptionsFile,
		ownOptions:      true,
		pluginRootFlag:  true,
		pluginRootVar:   "GROK_PLUGIN_ROOT",
		frame:           frame.HostGrok,
		historyVar:      "GROK_HOME",
		historyDir:      []string{".grok"},
		history:         HistorySummaries,
		delivery:        DeliverPagedSink,
		dialect:         DialectNative,
		hookNamed:       true,
		stop:            true,
		promptDiscarded: true,
		staysOnMove:     true,
	}},
}

// Hosts are the hosts of the table, in its order: for what must hold of
// every one of them, a fifth included when it comes.
func Hosts() []Host {
	out := make([]Host, 0, len(hosts))
	for i := range hosts {
		out = append(out, hosts[i].host)
	}
	return out
}

// row is h's row, and the empty row for a host the table does not hold.
func (h Host) row() hostRow {
	for i := range hosts {
		if hosts[i].host == h {
			return hosts[i].row
		}
	}
	return hostRow{}
}

// Name is the host's product name for a line a person reads.
func (h Host) Name() string {
	if name := h.row().name; name != "" {
		return name
	}
	return string(h)
}

// NativeIDVar is the environment variable that carries the host's native
// session id in the model's shell: MUSE_SESSION_ID on Muse Code,
// CODEX_THREAD_ID on Codex CLI, GROK_SESSION_ID on Grok Build, "" for
// Claude Code (whose shell carries CLAUDE_PID instead) and for a terminal.
func (h Host) NativeIDVar() string {
	if r := h.row(); r.pointed {
		return r.marker
	}
	return ""
}

// Pointed reports whether a session of this host is found through the
// by-host pointer (sessionmap.ByHost): the hosts whose shell knows the
// native id and not the pid.
func (h Host) Pointed() bool { return h.row().pointed }

// Frame is the wording a message is framed in for this host's model, as
// frame.BuildFor takes it: "" for Claude Code's, which is also what a
// host the table does not hold reads.
func (h Host) Frame() string { return h.row().frame }

// PluginRoot is the root of the host's Brigade package as a hook of that
// host learns it: from its --plugin-root argument where the host's hooks
// pass one (Codex CLI names nothing in a hook's environment, card 107),
// else from the variable the host sets for a plugin's hook. "" when
// neither says.
func (h Host) PluginRoot(environ []string, flag string) string {
	r := h.row()
	if r.pluginRootFlag && flag != "" {
		return flag
	}
	if r.pluginRootVar == "" {
		return ""
	}
	return adapterkit.Getenv(environ, r.pluginRootVar)
}

// HistoryRoot is the directory the host keeps its own record of its
// sessions in, "" when it keeps none Brigade reads or the directory cannot
// be told: the host's own variable when it is set, which must then be an
// absolute path (a relative one is never joined onto HOME, where it would
// name some other tree), else the host's directory under HOME. XDG_* is
// never asked: as with Brigade's own directories in a pointed session, an
// inherited override is not trusted.
func (h Host) HistoryRoot(environ []string) string {
	r := h.row()
	if len(r.historyDir) == 0 {
		return ""
	}
	if r.historyVar != "" {
		if dir := adapterkit.Getenv(environ, r.historyVar); dir != "" {
			if filepath.IsAbs(dir) {
				return filepath.Clean(dir)
			}
			return ""
		}
	}
	home := adapterkit.HomeDir(environ)
	if home == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, r.historyDir...)...)
}

// Dialect is what the host's hook is handed and may print: DialectPlugin
// for Claude Code, and for a host the table does not hold.
func (h Host) Dialect() DialectKind { return h.row().dialect }

// NamedByHook reports whether the host's hooks name it on their argv
// (`brigade hook <event> --host <name>`): Codex CLI and Grok Build, whose
// hooks are given no marker of their own.
func (h Host) NamedByHook() bool { return h.row().hookNamed }

// BindsReceiver reports whether a session of the host has a receiver its
// map must bind: one incarnation of one process, which the hook claims at
// a start, holds a prompt and an end to, and retires. Grok Build.
func (h Host) BindsReceiver() bool { return h.row().receiver }

// Delivery is how a message reaches the host's model: DeliverSocket for
// Claude Code, and for a host the table does not hold.
func (h Host) Delivery() DeliveryKind { return h.row().delivery }

// HistoryVar is the variable that names the directory of the host's own
// history, "" for a host that has none: the watcher is given it, since it
// reads that history and is started with an environment built from
// scratch.
func (h Host) HistoryVar() string { return h.row().historyVar }

// History is how the host keeps its own record of a session, HistoryNone
// for a host that keeps none Brigade reads.
func (h Host) History() HistoryKind { return h.row().history }

// Root is the directory the history of this kind is kept in: the
// HistoryRoot of the host that keeps it so, "" for HistoryNone and for a
// kind no host of the table has. A reader that knows its kind and no host
// asks here. It is the FIRST such host's: once two hosts keep one kind,
// each in a directory of its own, a reader must be told its host and ask
// Host.HistoryRoot, and TestAKindOfHistoryKnowsItsRoot fails to say so.
func (k HistoryKind) Root(environ []string) string {
	if k == HistoryNone {
		return ""
	}
	for i := range hosts {
		if hosts[i].row.history == k {
			return hosts[i].host.HistoryRoot(environ)
		}
	}
	return ""
}

// CanRename reports whether the host can be told a session's name from
// inside it (card 174): Claude Code, whose prompt hook has a field for it.
func (h Host) CanRename() bool { return h.row().rename }

// HasStop reports whether the host runs a Stop hook of its own, so the
// prompt hook need not carry what is asked at the end of a turn.
func (h Host) HasStop() bool { return h.row().stop }

// DiscardsPromptOutput reports whether the host shows nothing that a
// prompt hook prints, to the model or to its user: Grok Build.
func (h Host) DiscardsPromptOutput() bool { return h.row().promptDiscarded }

// StaysOnMove reports whether a running session of the host stays on the
// backend its team left until it starts again, where another follows at
// its next prompt: Grok Build, which could be told neither that it
// followed nor the id it has afterwards.
func (h Host) StaysOnMove() bool { return h.row().staysOnMove }

// NamedInWhoami reports whether `brigade whoami` says which host the
// session runs in: every host but Claude Code, whose lines stay as they
// were before there was a second one (card 88).
func (h Host) NamedInWhoami() bool { return !h.row().unnamed }

// HostOf names the host environ belongs to: Claude Code when CLAUDE_PID is
// set (it wins, so another host's variable leaking into a Claude session
// changes nothing), then Muse Code, then Codex CLI, then Grok Build, then
// a terminal. Grok is LAST for the same reason Claude is first: a
// GROK_SESSION_ID a Grok terminal tool exports is inherited by every
// session started from it, and relabelling such a session would route its
// hooks into the Grok path, where they register nothing and receive
// nothing. The Grok direction of the same leak is REFUSED rather than
// resolved: NativeID answers ambiguous_host for Grok Build when a
// competing marker is set, and the Grok hooks — which name their host with
// --host grok and so do not depend on this function — fail closed there.
// The order is the table's.
func HostOf(environ []string) Host {
	for i := range hosts {
		if adapterkit.Getenv(environ, hosts[i].row.marker) != "" {
			return hosts[i].host
		}
	}
	return ""
}

// competingMarker reports whether environ carries the marker of a host
// other than h.
func (h Host) competingMarker(environ []string) bool {
	for i := range hosts {
		if hosts[i].host != h && adapterkit.Getenv(environ, hosts[i].row.marker) != "" {
			return true
		}
	}
	return false
}

// MuseSessionID reads MUSE_SESSION_ID as a native session id (card 88):
// NativeID for HostMuse.
func MuseSessionID(environ []string) (string, error) { return NativeID(HostMuse, environ) }

// CodexThreadID reads CODEX_THREAD_ID as a native session id (card 107):
// NativeID for HostCodex.
func CodexThreadID(environ []string) (string, error) { return NativeID(HostCodex, environ) }
