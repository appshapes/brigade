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

// MaxSettingsBytes bounds what the scan will parse of one settings file; a
// larger file is treated as malformed (none), because a settings file is
// a few KiB and the scan is best effort.
const MaxSettingsBytes = 1 << 20

// A Scan is the result of ScanNative: whether a native `hold` or `refuse`
// was found, which value and in which file. The zero Scan means none.
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
	for _, path := range SettingsFiles(claudeConfigDir, cwd) {
		s.Checked = append(s.Checked, path)
		if s.Found {
			continue // the first hit in precedence order is the one reported
		}
		data, err := readFile(path)
		if err != nil || len(data) > MaxSettingsBytes {
			continue
		}
		if v, ok := topLevelValue(data); ok {
			s.Found, s.Value, s.File = true, v, path
		}
	}
	return s
}

// topLevelValue parses data as a JSON object and returns its top-level
// crossSessionInbound when it is the string hold or refuse. json/v2 is
// case-sensitive and ignores unknown members, so a wrongly-cased key or a
// nested occurrence is not a match, and a duplicate member name (which the
// codec rejects) reads as malformed.
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
	case NativeHold, NativeRefuse:
		return v, true
	default:
		return "", false
	}
}

// Warning renders the context line the hook prints for a hit (6.10): it
// names the setting, the value (one of the two known strings, never free
// text) and the file, and says what Brigade did about it. For a Scan that
// found nothing it returns "".
func (s Scan) Warning() string {
	if !s.Found {
		return ""
	}
	return `Brigade: your Claude Code settings set "` + SettingKey + `": "` + s.Value + `" in ` + s.File +
		`; Claude Code would not deliver Brigade messages to this session, so Brigade's inbound policy is refuse` +
		` (nothing is acknowledged blind; messages wait on the server). Remove that setting, or set it to "accept", to receive team messages.`
}
