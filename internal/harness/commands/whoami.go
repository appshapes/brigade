package commands

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/pidfile"
	"github.com/appshapes/brigade/internal/harness/watchstate"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

// whoamiResult is the --json result of `brigade whoami`: the session's
// identity from the by-pid map and the adapter from the cached describe.
type whoamiResult struct {
	SessionID      string   `json:"session_id"`
	SessionName    string   `json:"session_name"`
	WorkspaceLabel string   `json:"workspace_label,omitzero"`
	TeamRef        string   `json:"team_ref"`
	TeamName       string   `json:"team_name"`
	Inbound        string   `json:"inbound"`
	AdapterName    string   `json:"adapter_name"`
	AdapterVersion string   `json:"adapter_version"`
	AdapterCommand []string `json:"adapter_command"`
	AdapterSource  string   `json:"adapter_source"`
	PermissionMode string   `json:"permission_mode,omitzero"`
	NonInteractive bool     `json:"non_interactive"`
	// Delivery is whether this session is receiving, from this machine's
	// own files (card 34).
	Delivery      whoamiDelivery `json:"delivery"`
	SelfSessionID string         `json:"self_session_id"`
	Note          string         `json:"note"`
}

// whoamiDelivery answers "is this session receiving?" from four local
// facts: the watcher's pidfile, the state file it keeps, the time the seen
// file was last written — it is saved after every injection — and the
// pending file of the `hold` policy. No path and no id is in it.
type whoamiDelivery struct {
	// Watcher is WatcherRunning, WatcherNotRunning or WatcherNone.
	Watcher        string `json:"watcher"`
	WatcherVersion string `json:"watcher_version,omitzero"`
	// Connection is the running watcher's own word (watchstate), absent
	// when it wrote none: a watcher older than the state file.
	Connection      string     `json:"connection,omitzero"`
	ConnectionSince *time.Time `json:"connection_since,omitzero"`
	// LastDeliveryAt is absent when nothing was ever delivered here.
	LastDeliveryAt *time.Time `json:"last_delivery_at,omitzero"`
	// Held is absent when the pending file could not be read.
	Held *int `json:"held,omitzero"`
}

// The watcher words of whoamiDelivery.
const (
	WatcherRunning    = "running"
	WatcherNotRunning = "not_running"
	// WatcherNone: the session has no inbox socket, so no watcher runs.
	WatcherNone = "none"
)

// WhoamiNote is the note member of the `whoami --json` result.
const WhoamiNote = "identity read from this session's map and the adapter's describe, delivery from this machine's own state files; nothing was fetched from the backend"

// Whoami implements `brigade whoami [--json]` (6.4): the session's id,
// name, team, profile and adapter from the by-pid map and the cached
// `describe`; no network (the describe is one local spawn).
//
// The human form carries a second line, `terminal: <plugin binary>`, when
// the map knows the path: it is the human-facing home of the plugin
// binary's location after finding F1 took it out of the SessionStart
// context line, and what docs/setup.md tells the human to symlink from
// `~/.local/bin/brigade`. It stays OUT of `--json`, the form the model
// reads, because a path in front of the model is the finding. The same
// reasoning, with a sharper edge, keeps the frame level (P5-12) to one
// human line, `frame: <level>` — never the custom text, never the file
// path, and nothing in --json: a model told how permissive its own frame
// is might reason about it, which is not a conversation Brigade should
// start; the model already reads the clause verbatim in every frame.
//
// The last line, `delivery:`, is card 34's: "did the message get lost, or
// is my session not receiving?" is the first question when a hand-off goes
// quiet, and it is answered from this machine's own files (delivery).
func Whoami(inv Invocation) error {
	if len(inv.Args) > 0 {
		return usage("whoami takes no arguments")
	}
	if !inv.inSession() {
		return notInSession("whoami")
	}
	t, err := inv.resolveSession("")
	if err != nil {
		return err
	}
	m := t.session
	d := inv.delivery(t)
	if inv.JSON {
		return writeJSON(inv.Out, whoamiResult{
			SessionID:      sanitizeID(m.BrigadeSessionID),
			SessionName:    protocol.SanitizeName(m.SessionName),
			WorkspaceLabel: protocol.SanitizeLabel(m.WorkspaceLabel),
			TeamRef:        sanitizeID(m.TeamRef),
			TeamName:       protocol.SanitizeName(m.TeamName),
			Inbound:        protocol.SanitizeAttribute(m.Inbound),
			AdapterName:    protocol.SanitizeAttribute(t.describe.Adapter.Name),
			AdapterVersion: protocol.SanitizeAttribute(t.describe.Adapter.Version),
			AdapterCommand: append([]string{}, m.AdapterCommand...),
			AdapterSource:  t.adapter.Source,
			PermissionMode: protocol.SanitizeAttribute(m.PermissionMode),
			NonInteractive: m.NonInteractive,
			Delivery:       d,
			SelfSessionID:  sanitizeID(m.BrigadeSessionID),
			Note:           WhoamiNote,
		})
	}
	out := []string{"session " + idLine(m.BrigadeSessionID) + " \"" + nameLine(m.SessionName) + "\"" +
		" in team \"" + nameLine(m.TeamName) + "\"" +
		" (adapter " + attrLine(t.describe.Adapter.Name) + " " + attrLine(t.describe.Adapter.Version) + ")" +
		"; inbound: " + enumLine(m.Inbound)}
	if repo := workspaceLine(m.WorkspaceLabel); repo != "" {
		out = append(out, "repo: "+repo)
	}
	if p := pathLine(m.PluginBin); p != "" {
		out = append(out, "terminal: "+p)
	}
	out = append(out, "frame: "+frameLevelLine(m.FrameLevel, m.FrameText))
	out = append(out, "delivery: "+d.line(inv.Deps.now()))
	return writeLines(inv.Out, out...)
}

// delivery reads the four facts. Every one is best effort: a file that is
// missing is an answer (no watcher, nothing delivered, nothing held), and
// one that cannot be read leaves its fact out rather than guess it.
func (inv Invocation) delivery(t *target) whoamiDelivery {
	m := t.session
	d := whoamiDelivery{Watcher: WatcherNotRunning}
	if m.SocketPath == "" {
		d.Watcher = WatcherNone
	}
	lookup := inv.Deps.Lookup
	if lookup == nil {
		lookup = procutil.Lookup
	}
	v, err := pidfile.Check(pidfile.Path(t.stateDir, m.ClaudePID), lookup)
	if err == nil && v.Found && v.Alive && v.Entry.BrigadeSessionID == m.BrigadeSessionID {
		d.Watcher, d.WatcherVersion = WatcherRunning, protocol.SanitizeAttribute(v.Entry.Version)
		// The state file speaks for the watcher that wrote it and no other.
		if s, serr := watchstate.Read(watchstate.Path(t.stateDir, m.ClaudePID)); serr == nil && s.PID == v.Entry.PID {
			d.Connection, d.ConnectionSince = s.State, &s.Since
		}
	}
	if info, serr := os.Stat(inbound.SeenPath(t.stateDir, m.BrigadeSessionID)); serr == nil {
		at := info.ModTime().UTC()
		d.LastDeliveryAt = &at
	}
	pending, perr := inbound.FilePendingStore{
		Path: inbound.PendingPath(t.stateDir, m.BrigadeSessionID), SessionID: m.BrigadeSessionID,
	}.Load()
	if perr != nil {
		inv.logger().Debug("whoami: pending file unreadable; held count left out", log.Err(perr))
		return d
	}
	held := 0
	for _, e := range pending.Entries {
		if !e.Released() {
			held++
		}
	}
	d.Held = &held
	inv.logger().Debug("whoami: delivery", slog.String("watcher", d.Watcher), slog.Int("held", held))
	return d
}

// line renders the delivery facts for the human form, as of now.
func (d whoamiDelivery) line(now time.Time) string {
	var parts []string
	switch d.Watcher {
	case WatcherRunning:
		w := "watcher running"
		if d.WatcherVersion != "" {
			w += " (" + attrLine(d.WatcherVersion) + ")"
		}
		if d.ConnectionSince != nil {
			since := agoText(now.Sub(*d.ConnectionSince))
			switch d.Connection {
			case watchstate.Connected:
				w += ", connected for " + since
			case watchstate.Retrying:
				w += ", not connected for " + since + " and retrying"
			case watchstate.Connecting:
				w += ", connecting for " + since
			}
		}
		parts = append(parts, w)
	case WatcherNone:
		parts = append(parts, "no watcher: this session has no inbox socket")
	default:
		parts = append(parts, "watcher not running", "the next prompt starts it again")
	}
	if d.LastDeliveryAt != nil {
		parts = append(parts, "last delivery recorded "+agoText(now.Sub(*d.LastDeliveryAt))+" ago")
	} else {
		parts = append(parts, "no delivery recorded")
	}
	if d.Held != nil {
		parts = append(parts, strconv.Itoa(*d.Held)+" held")
	} else {
		parts = append(parts, "held count not readable")
	}
	return strings.Join(parts, "; ")
}

// agoText renders an age in its largest whole unit: 45s, 12m, 3h, 2d. A
// negative age — a clock that stepped back — reads as 0s.
func agoText(d time.Duration) string {
	switch {
	case d < time.Second:
		return "0s"
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
}

// frameLevelLine is the level name and, for a custom clause, its length in
// characters (the user's own words, without the fold's trailing space) —
// never the text itself.
func frameLevelLine(level, text string) string {
	line := enumLine(level)
	if level == string(frame.LevelCustom) {
		line += " (" + strconv.Itoa(utf8.RuneCountInString(strings.TrimSpace(text))) + " characters)"
	}
	return line
}
