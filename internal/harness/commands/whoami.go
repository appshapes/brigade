package commands

import "github.com/appshapes/brigade/internal/protocol"

// whoamiResult is the --json result of `brigade whoami`: the session's
// identity from the by-pid map and the adapter from the cached describe.
type whoamiResult struct {
	SessionID      string   `json:"session_id"`
	SessionName    string   `json:"session_name"`
	TeamRef        string   `json:"team_ref"`
	TeamName       string   `json:"team_name"`
	Profile        string   `json:"profile"`
	Inbound        string   `json:"inbound"`
	AdapterName    string   `json:"adapter_name"`
	AdapterVersion string   `json:"adapter_version"`
	AdapterCommand []string `json:"adapter_command"`
	AdapterSource  string   `json:"adapter_source"`
	PermissionMode string   `json:"permission_mode,omitzero"`
	NonInteractive bool     `json:"non_interactive"`
	SelfSessionID  string   `json:"self_session_id"`
	Note           string   `json:"note"`
}

// WhoamiNote is the note member of the `whoami --json` result.
const WhoamiNote = "identity read from this session's map and the adapter's describe; nothing was fetched from the backend"

// Whoami implements `brigade whoami [--json]` (6.4): the session's id,
// name, team, profile and adapter from the by-pid map and the cached
// `describe`; no network (the describe is one local spawn).
//
// The human form carries a second line, `terminal: <plugin binary>`, when
// the map knows the path: it is the human-facing home of the plugin
// binary's location after finding F1 took it out of the SessionStart
// context line, and what docs/setup.md tells the human to symlink from
// `~/.local/bin/brigade`. It stays OUT of `--json`, the form the model
// reads, because a path in front of the model is the finding.
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
	if inv.JSON {
		return writeJSON(inv.Out, whoamiResult{
			SessionID:      sanitizeID(m.BrigadeSessionID),
			SessionName:    protocol.SanitizeName(m.SessionName),
			TeamRef:        sanitizeID(m.TeamRef),
			TeamName:       protocol.SanitizeName(m.TeamName),
			Profile:        m.Profile,
			Inbound:        protocol.SanitizeAttribute(m.Inbound),
			AdapterName:    protocol.SanitizeAttribute(t.describe.Adapter.Name),
			AdapterVersion: protocol.SanitizeAttribute(t.describe.Adapter.Version),
			AdapterCommand: append([]string{}, m.AdapterCommand...),
			AdapterSource:  t.adapter.Source,
			PermissionMode: protocol.SanitizeAttribute(m.PermissionMode),
			NonInteractive: m.NonInteractive,
			SelfSessionID:  sanitizeID(m.BrigadeSessionID),
			Note:           WhoamiNote,
		})
	}
	out := []string{"session " + idLine(m.BrigadeSessionID) + " \"" + nameLine(m.SessionName) + "\"" +
		" in team \"" + nameLine(m.TeamName) + "\"" +
		" (profile " + m.Profile + ", adapter " + attrLine(t.describe.Adapter.Name) + " " + attrLine(t.describe.Adapter.Version) + ")" +
		"; inbound: " + enumLine(m.Inbound)}
	if p := pathLine(m.PluginBin); p != "" {
		out = append(out, "terminal: "+p)
	}
	return writeLines(inv.Out, out...)
}
