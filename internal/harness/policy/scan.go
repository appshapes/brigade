package policy

import (
	"encoding/json/v2"
	"os"
	"path/filepath"

	"github.com/appshapes/brigade/internal/protocol"
)

// SettingKey is the native Claude Code setting the scan looks for, at the
// TOP LEVEL of a settings file only (6.10).
const SettingKey = "crossSessionInbound"

// The two native values that flip Brigade to Refuse (6.10, E0-9). A native
// `accept`, or any other value, is not a hit.
const (
	NativeHold   = protocol.InboundHold
	NativeRefuse = protocol.InboundRefuse
)

// NativeAccept is the native value a session that bypasses permission
// prompts needs in its USER settings file (card 50): since Claude Code
// 2.1.224 a receiving session in `bypassPermissions` holds every message
// that does not identify its sender as bypassing too, for the human to
// approve in a dialog that expires, unless an explicit `accept` applies —
// and an `accept` in a repository's `.claude/settings.json` or
// `.claude/settings.local.json` is NOT one: Claude Code lets a repository
// only tighten this setting ("a project or local value that isn't stricter
// is ignored", the settings reference). Managed settings and `--settings`
// also count as explicit, and this scan cannot see either.
const NativeAccept = protocol.InboundAccept

// PermissionBypass is the hook-stdin `permission_mode` of a session that
// bypasses permission prompts (`--dangerously-skip-permissions`). Plan mode
// counts as bypassing too when bypass is available to the session, which
// the hook cannot tell; it is read as prompting, so a plan-mode prompt in
// such a session fails open to accept — Claude Code's dialog, never a
// silent loss, and the next bypass prompt re-decides.
const PermissionBypass = "bypassPermissions"

// BypassesPrompts reports whether mode is the bypass class as the hook can
// know it.
func BypassesPrompts(mode string) bool { return mode == PermissionBypass }

// MaxSettingsBytes bounds what the scan will parse of one settings file; a
// larger file is treated as malformed (none), because a settings file is
// a few KiB and the scan is best effort.
const MaxSettingsBytes = 1 << 20

// A Scan is the result of ScanNative: whether a native `hold` or `refuse`
// was found, which value and in which file, and where an explicit `accept`
// stands (card 50). The zero Scan means none.
type Scan struct {
	// Found is true when Value is NativeHold or NativeRefuse.
	Found bool
	// Value is the native value found, "" when none.
	Value string
	// File is the settings file the value came from (the most specific
	// file in native precedence order when several carry one).
	File string
	// Checked lists every file the scan looked at, in the order it looked,
	// for diagnostics.
	Checked []string
	// UserFile is the user settings file the scan read —
	// <claudeConfigDir>/settings.json — or "" when no config dir was known.
	UserFile string
	// UserAccept is true when UserFile carries a top-level
	// `"crossSessionInbound": "accept"`: the one explicit accept this scan
	// can see, the one a bypassPermissions session needs.
	UserAccept bool
	// RepoAccept is the most specific project or local file carrying a
	// top-level `accept`, "" when none. It counts for nothing — Claude Code
	// lets a repository only tighten the setting — and is named in the
	// parity warning so the user learns why their accept did not work.
	RepoAccept string
}

// SettingsFiles names the three files the scan reads, most specific first
// (the native precedence, so a hit in the local file is reported over one
// in the project file over one in the user file): <cwd>/.claude/
// settings.local.json, <cwd>/.claude/settings.json, <claudeConfigDir>/
// settings.json. An empty cwd skips the two project files; an empty
// claudeConfigDir skips the user file.
func SettingsFiles(claudeConfigDir, cwd string) []string {
	var files []string
	if cwd != "" {
		files = append(files,
			filepath.Join(cwd, ".claude", "settings.local.json"),
			filepath.Join(cwd, ".claude", "settings.json"),
		)
	}
	if claudeConfigDir != "" {
		files = append(files, filepath.Join(claudeConfigDir, "settings.json"))
	}
	return files
}

// ScanNative reads, best effort, the three settings files of SettingsFiles
// for a top-level `crossSessionInbound` of `hold` or `refuse` (6.10).
// readFile is injected (nil means os.ReadFile) so tests never touch a real
// config directory. A missing, unreadable, over-large or malformed file, a
// value that is not a string, a string other than the two, and a
// `crossSessionInbound` nested under another key are all NOT hits; any
// `hold` or `refuse` in any of the three files IS one, whatever the other
// files say — the documentation says a project or local `refuse` "applies
// over every other source", and this scan cannot model the native
// precedence exactly, so it fails closed: a hit anywhere makes Brigade
// refuse, and the warning names the file so the user can fix it.
//
// What the scan CANNOT see, and the hook's warning must not pretend to:
// managed settings (a policy file outside these paths), a `--settings`
// file or JSON on the command line, and any value Claude Code computes
// itself. A native `hold` or `refuse` from those sources makes `injected`
// a lie the plugin can only document (6.10, E2E-03).
func ScanNative(claudeConfigDir, cwd string, readFile func(string) ([]byte, error)) Scan {
	if readFile == nil {
		readFile = os.ReadFile
	}
	s := Scan{}
	if claudeConfigDir != "" {
		s.UserFile = filepath.Join(claudeConfigDir, "settings.json")
	}
	for _, path := range SettingsFiles(claudeConfigDir, cwd) {
		s.Checked = append(s.Checked, path)
		data, err := readFile(path)
		if err != nil || len(data) > MaxSettingsBytes {
			continue
		}
		v, ok := topLevelValue(data)
		if !ok {
			continue
		}
		switch {
		case v == NativeAccept && path == s.UserFile:
			s.UserAccept = true
		case v == NativeAccept:
			if s.RepoAccept == "" {
				s.RepoAccept = path // the most specific repository file
			}
		case !s.Found:
			s.Found, s.Value, s.File = true, v, path // the first hit in precedence order is the one reported
		}
	}
	return s
}

// topLevelValue parses data as a JSON object and returns its top-level
// crossSessionInbound when it is the string accept, hold or refuse.
// json/v2 is case-sensitive and ignores unknown members, so a
// wrongly-cased key or a nested occurrence is not a match, and a duplicate
// member name (which the codec rejects) reads as malformed.
func topLevelValue(data []byte) (string, bool) {
	var doc struct {
		Value any `json:"crossSessionInbound"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", false
	}
	v, ok := doc.Value.(string)
	if !ok {
		return "", false
	}
	switch v {
	case NativeAccept, NativeHold, NativeRefuse:
		return v, true
	default:
		return "", false
	}
}

// Warning renders the context line the hook prints for a hit (6.10): it
// names the setting, the value (one of the two known strings, never free
// text) and the file, says what Brigade did about it, names Brigade's own
// `hold` as the review option once the native setting is gone (P5-9, 3.6:
// the native setting must be "accept" for a release to be delivered), and
// points at `brigade inbox`, which lists a refusing session's waiting
// messages without acknowledging anything. For a Scan that found nothing
// it returns "".
func (s Scan) Warning() string {
	if !s.Found {
		return ""
	}
	return `Brigade: your Claude Code settings set "` + SettingKey + `": "` + s.Value + `" in ` + s.File +
		`; Claude Code would not deliver Brigade messages to this session, so Brigade's inbound policy is refuse` +
		` (nothing is acknowledged blind; messages wait on the server). Remove that setting, or set it to "accept", to receive team messages;` +
		` Brigade's own team_inbound "hold" reviews messages in a terminal before delivery, but it still needs Claude Code's setting to be "accept".` +
		" Run `brigade inbox` in a terminal to read what is waiting."
}

// ParityWarning renders the context line the hook prints when the session
// bypasses permission prompts and the scan found no explicit accept in the
// user file (card 50): what Claude Code would do with every Brigade post
// (hold it for approval in a dialog that expires — a headless session has
// no dialog, so there it is simply dropped), what Brigade does instead
// (refuse, so nothing is acknowledged blind), and the ONE line to add and
// where, in the words a person can act on. When a repository file carries
// an accept, the line says why that one does not count. Fixed text: the
// only variable parts are file paths this harness computed itself.
func (s Scan) ParityWarning() string {
	user := s.UserFile
	if user == "" {
		user = "your Claude Code user settings.json"
	}
	w := `Brigade: this session bypasses permission prompts and ` + user + ` has no "` + SettingKey + `": "accept", ` +
		`so Claude Code would hold every Brigade message for your approval in a dialog that expires (and drop it unseen in a headless session); ` +
		`Brigade's inbound policy is refuse instead (nothing is acknowledged blind; messages wait on the server). ` +
		`To receive team messages, add "` + SettingKey + `": "accept" to ` + user + ` — Brigade applies it at your next prompt.`
	if s.RepoAccept != "" {
		w += ` The "accept" in ` + s.RepoAccept + ` does not count: Claude Code lets a repository only tighten this setting.`
	}
	w += ` Brigade cannot see managed settings or --settings; an "accept" there needs this line too.`
	return w
}
